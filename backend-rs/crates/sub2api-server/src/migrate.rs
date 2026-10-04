//! 数据库迁移运行器。
//!
//! ⚠️ 重要：这不是 `sqlx::migrate!` 的默认约定。
//!
//! Go 版使用**自研迁移运行器**，Rust 侧必须复刻其语义，否则会在现网库上
//! 建出并行的跟踪表并重跑全部迁移，破坏数据。必须对齐的点：
//!
//! | 项 | Go 版取值 | 说明 |
//! |---|---|---|
//! | 跟踪表名 | `schema_migrations` | **不是** sqlx 默认的 `_sqlx_migrations` |
//! | 表结构 | `filename TEXT PK, checksum TEXT, applied_at TIMESTAMPTZ` | |
//! | 校验和 | 文件内容 SHA256（hex） | 用于检测迁移文件被篡改 |
//! | 加锁方式 | `pg_try_advisory_lock` + 重试循环 | 多实例串行化，可被取消 |
//! | Advisory Lock ID | `694208311321144027` | |
//! | 非事务后缀 | `_notx.sql` | 此类迁移**逐条语句**执行 |
//! | 空文件 | 跳过 | |
//!
//! ## 为什么 `_notx.sql` 必须逐条执行
//!
//! `CREATE INDEX CONCURRENTLY` 不能在事务块内运行。PostgreSQL 会把「一次
//! 发送多条语句」的简单查询协议请求**隐式包成事务块**，因此把整个文件内容
//! 一次性发给服务器会导致：
//!
//! ```text
//! ERROR: CREATE INDEX CONCURRENTLY cannot run inside a transaction block
//! ```
//!
//! 必须按 `;` 拆分后逐条发送，每条独立执行。这是 Go 版 `splitSQLStatements`
//! 存在的原因，本模块必须保持一致。
//!
//! 迁移 SQL 文件复用 Go 版目录 `../backend/migrations/`，**不复制、不修改**，
//! 保证两边读的是同一份 schema 定义。

use anyhow::{bail, Context};
use sha2::{Digest, Sha256};
use sqlx::{Connection, PgPool, Row};
use std::path::{Path, PathBuf};
use std::time::Duration;

/// 迁移记录表 DDL，与 Go 版 `schemaMigrationsTableDDL` 逐字对齐。
const SCHEMA_MIGRATIONS_TABLE_DDL: &str = r#"
CREATE TABLE IF NOT EXISTS schema_migrations (
	filename   TEXT PRIMARY KEY,
	checksum   TEXT NOT NULL,
	applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
"#;

/// Advisory Lock ID，与 Go 版 `migrationsAdvisoryLockID` 保持一致。
const MIGRATIONS_ADVISORY_LOCK_ID: i64 = 694208311321144027;

/// 锁重试间隔，与 Go 版 `migrationsLockRetryInterval` 对齐。
const MIGRATIONS_LOCK_RETRY_INTERVAL: Duration = Duration::from_millis(500);

/// 非事务迁移文件后缀，与 Go 版 `nonTransactionalMigrationSuffix` 对齐。
const NON_TRANSACTIONAL_SUFFIX: &str = "_notx.sql";

/// 定位迁移目录。
///
/// 优先使用环境变量 `SUB2API_MIGRATIONS_DIR`，否则按相对路径查找
/// `../backend/migrations`（即复用 Go 版的迁移文件）。
fn migrations_dir() -> anyhow::Result<PathBuf> {
    if let Ok(dir) = std::env::var("SUB2API_MIGRATIONS_DIR") {
        let p = PathBuf::from(dir);
        if p.is_dir() {
            return Ok(p);
        }
        bail!("SUB2API_MIGRATIONS_DIR 不是有效目录: {}", p.display());
    }

    // 运行目录可能是 backend-rs/ 或 backend-rs/crates/sub2api-server/
    let candidates = [
        PathBuf::from("../backend/migrations"),
        PathBuf::from("backend/migrations"),
        PathBuf::from("../../backend/migrations"),
    ];
    for c in candidates {
        if c.is_dir() {
            return Ok(c);
        }
    }

    bail!("未找到迁移目录，请设置 SUB2API_MIGRATIONS_DIR 指向 backend/migrations")
}

/// 计算文件内容 SHA256（hex），与 Go 版 checksum 算法一致。
fn checksum_of(content: &[u8]) -> String {
    let mut hasher = Sha256::new();
    hasher.update(content);
    format!("{:x}", hasher.finalize())
}

/// 按 `;` 拆分 SQL 语句，与 Go 版 `splitSQLStatements` 行为一致。
///
/// 注意：这是朴素拆分（不解析字符串字面量内的分号），与 Go 版实现保持一致。
/// 现有迁移文件中不包含此类用例。
fn split_sql_statements(content: &str) -> Vec<String> {
    content
        .split(';')
        .filter(|p| !p.trim().is_empty())
        .map(|p| p.to_string())
        .collect()
}

/// 剥离行注释（`--` 之后的内容），与 Go 版 `stripSQLLineComment` 行为一致。
fn strip_sql_line_comment(s: &str) -> String {
    let joined: Vec<&str> = s
        .lines()
        .map(|line| match line.find("--") {
            Some(idx) => &line[..idx],
            None => line,
        })
        .collect();
    joined.join("\n").trim().to_string()
}

/// 校验迁移执行模式，与 Go 版 `validateMigrationExecutionMode` 对齐。
///
/// 返回值：`true` 表示该迁移必须以非事务方式执行。
///
/// 规则：
/// - 普通迁移不得包含 `CONCURRENTLY`
/// - `*_notx.sql` 不得包含事务控制语句
/// - `*_notx.sql` 中每条语句都必须是带 `IF NOT EXISTS` / `IF EXISTS` 的
///   `CREATE/DROP INDEX CONCURRENTLY`
fn validate_migration_execution_mode(name: &str, content: &str) -> anyhow::Result<bool> {
    let normalized_name = name.trim().to_lowercase();
    let upper_content = content.to_uppercase();
    let non_tx = normalized_name.ends_with(NON_TRANSACTIONAL_SUFFIX);

    if !non_tx {
        if upper_content.contains("CONCURRENTLY") {
            bail!("CONCURRENTLY statements must be placed in *_notx.sql migrations");
        }
        return Ok(false);
    }

    if upper_content.contains("BEGIN")
        || upper_content.contains("COMMIT")
        || upper_content.contains("ROLLBACK")
    {
        bail!("*_notx.sql must not contain transaction control statements (BEGIN/COMMIT/ROLLBACK)");
    }

    for stmt in split_sql_statements(content) {
        let normalized_stmt = strip_sql_line_comment(stmt.trim()).to_uppercase();
        if normalized_stmt.is_empty() {
            continue;
        }

        if normalized_stmt.contains("CONCURRENTLY") {
            let is_create_index =
                normalized_stmt.contains("CREATE") && normalized_stmt.contains("INDEX");
            let is_drop_index =
                normalized_stmt.contains("DROP") && normalized_stmt.contains("INDEX");
            if !is_create_index && !is_drop_index {
                bail!(
                    "*_notx.sql currently only supports CREATE/DROP INDEX CONCURRENTLY statements"
                );
            }
            if is_create_index && !normalized_stmt.contains("IF NOT EXISTS") {
                bail!("CREATE INDEX CONCURRENTLY in *_notx.sql must include IF NOT EXISTS for idempotency");
            }
            if is_drop_index && !normalized_stmt.contains("IF EXISTS") {
                bail!(
                    "DROP INDEX CONCURRENTLY in *_notx.sql must include IF EXISTS for idempotency"
                );
            }
            continue;
        }

        bail!("*_notx.sql must not mix non-CONCURRENTLY SQL statements");
    }

    Ok(true)
}

/// 收集并按文件名排序迁移文件。
///
/// Go 版使用零填充数字前缀（`001_`、`9000_`）保证顺序，
/// 这里按文件名字典序排序即可得到相同结果。
fn collect_migrations(dir: &Path) -> anyhow::Result<Vec<PathBuf>> {
    let mut files: Vec<PathBuf> = std::fs::read_dir(dir)
        .with_context(|| format!("读取迁移目录失败: {}", dir.display()))?
        .filter_map(|e| e.ok())
        .map(|e| e.path())
        .filter(|p| p.is_file() && p.extension().map(|x| x == "sql").unwrap_or(false))
        .collect();

    files.sort();
    if files.is_empty() {
        bail!("迁移目录为空: {}", dir.display());
    }
    Ok(files)
}

/// 执行全部未应用的迁移。
pub async fn run(pool: &PgPool) -> anyhow::Result<()> {
    let dir = migrations_dir()?;
    let files = collect_migrations(&dir)?;
    tracing::info!(dir = %dir.display(), count = files.len(), "开始检查迁移");

    // 使用连接级 advisory lock 串行化迁移，避免多实例并发。
    let mut conn = pool.acquire().await.context("获取迁移连接失败")?;

    // 与 Go 版一致：使用 pg_try_advisory_lock + 重试，而非阻塞等待。
    acquire_advisory_lock(&mut conn).await?;

    let result = apply_all(&mut conn, &files).await;

    // 无论成功失败都释放锁。
    if let Err(e) = sqlx::query("SELECT pg_advisory_unlock($1)")
        .bind(MIGRATIONS_ADVISORY_LOCK_ID)
        .execute(&mut *conn)
        .await
    {
        tracing::warn!(error = %e, "释放迁移 advisory lock 失败");
    }

    result
}

async fn acquire_advisory_lock(conn: &mut sqlx::PgConnection) -> anyhow::Result<()> {
    loop {
        let locked: bool = sqlx::query_scalar("SELECT pg_try_advisory_lock($1)")
            .bind(MIGRATIONS_ADVISORY_LOCK_ID)
            .fetch_one(&mut *conn)
            .await
            .context("获取迁移 advisory lock 失败")?;

        if locked {
            return Ok(());
        }
        tokio::time::sleep(MIGRATIONS_LOCK_RETRY_INTERVAL).await;
    }
}

async fn apply_all(conn: &mut sqlx::PgConnection, files: &[PathBuf]) -> anyhow::Result<()> {
    sqlx::query(SCHEMA_MIGRATIONS_TABLE_DDL)
        .execute(&mut *conn)
        .await
        .context("创建 schema_migrations 表失败")?;

    let mut applied = 0usize;
    let mut skipped = 0usize;

    for path in files {
        let name = path
            .file_name()
            .and_then(|n| n.to_str())
            .context("迁移文件名非法 UTF-8")?
            .to_string();

        let content =
            std::fs::read(path).with_context(|| format!("读取迁移文件失败: {}", path.display()))?;

        // 与 Go 版一致：跳过空文件。
        if content.is_empty() {
            continue;
        }

        let checksum = checksum_of(&content);
        let sql = String::from_utf8_lossy(&content).to_string();

        // 查询是否已应用。
        let existing = sqlx::query("SELECT checksum FROM schema_migrations WHERE filename = $1")
            .bind(&name)
            .fetch_optional(&mut *conn)
            .await
            .with_context(|| format!("查询迁移记录失败: {name}"))?;

        if let Some(row) = existing {
            let recorded: String = row.try_get("checksum").unwrap_or_default();
            if recorded != checksum {
                bail!(
                    "migration {name} checksum mismatch (db={recorded} file={checksum})\n\
                     这意味着迁移文件在应用后被修改，属于危险操作。\n\
                     正确做法是新增迁移文件，而不是修改已应用的迁移。"
                );
            }
            skipped += 1;
            continue;
        }

        let non_tx = validate_migration_execution_mode(&name, &sql)
            .with_context(|| format!("校验迁移执行模式失败: {name}"))?;

        if non_tx {
            // *_notx.sql：必须逐条语句执行。
            // 若一次性发送整个文件，PostgreSQL 会隐式包成事务块，
            // 导致 CREATE INDEX CONCURRENTLY 失败。
            for (i, stmt) in split_sql_statements(&sql).iter().enumerate() {
                let trimmed = stmt.trim();
                if trimmed.is_empty() || strip_sql_line_comment(trimmed).is_empty() {
                    continue;
                }
                sqlx::raw_sql(trimmed)
                    .execute(&mut *conn)
                    .await
                    .with_context(|| format!("执行非事务迁移失败: {name} (第 {} 条语句)", i + 1))?;
            }

            sqlx::query("INSERT INTO schema_migrations (filename, checksum) VALUES ($1, $2)")
                .bind(&name)
                .bind(&checksum)
                .execute(&mut *conn)
                .await
                .with_context(|| format!("记录迁移失败: {name}"))?;
        } else {
            let mut tx = conn.begin().await.context("开启迁移事务失败")?;
            sqlx::raw_sql(&sql)
                .execute(&mut *tx)
                .await
                .with_context(|| format!("执行迁移失败: {name}"))?;
            sqlx::query("INSERT INTO schema_migrations (filename, checksum) VALUES ($1, $2)")
                .bind(&name)
                .bind(&checksum)
                .execute(&mut *tx)
                .await
                .with_context(|| format!("记录迁移失败: {name}"))?;
            tx.commit().await.context("提交迁移事务失败")?;
        }

        tracing::info!(migration = %name, "已应用迁移");
        applied += 1;
    }

    tracing::info!(applied, skipped, "迁移检查完成");
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn split_statements_ignores_empty_fragments() {
        let stmts = split_sql_statements("SELECT 1; SELECT 2;;  ;SELECT 3;");
        assert_eq!(stmts.len(), 3);
        assert_eq!(stmts[0].trim(), "SELECT 1");
        assert_eq!(stmts[2].trim(), "SELECT 3");
    }

    #[test]
    fn strip_line_comment_removes_comment_text() {
        let out = strip_sql_line_comment("SELECT 1;\n-- comment here\nSELECT 2;");
        assert!(!out.contains("comment here"));
        assert!(out.contains("SELECT 1"));
        assert!(out.contains("SELECT 2"));
    }

    #[test]
    fn plain_migration_with_concurrently_is_rejected() {
        let r = validate_migration_execution_mode(
            "001_bad.sql",
            "CREATE INDEX CONCURRENTLY idx ON t (c);",
        );
        assert!(r.is_err());
    }

    #[test]
    fn plain_migration_is_transactional() {
        let r = validate_migration_execution_mode("001_ok.sql", "CREATE TABLE t (id int);");
        assert!(!r.unwrap());
    }

    #[test]
    fn notx_migration_is_non_transactional() {
        let r = validate_migration_execution_mode(
            "062_idx_notx.sql",
            "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx ON t (c);",
        );
        assert!(r.unwrap());
    }

    #[test]
    fn notx_without_if_not_exists_is_rejected() {
        let r = validate_migration_execution_mode(
            "062_idx_notx.sql",
            "CREATE INDEX CONCURRENTLY idx ON t (c);",
        );
        assert!(r.is_err());
    }

    #[test]
    fn notx_with_transaction_control_is_rejected() {
        let r = validate_migration_execution_mode(
            "062_idx_notx.sql",
            "BEGIN; CREATE INDEX CONCURRENTLY IF NOT EXISTS idx ON t (c);",
        );
        assert!(r.is_err());
    }

    #[test]
    fn notx_mixing_plain_statement_is_rejected() {
        let r = validate_migration_execution_mode(
            "062_idx_notx.sql",
            "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx ON t (c); CREATE TABLE x (id int);",
        );
        assert!(r.is_err());
    }

    #[test]
    fn checksum_matches_known_sha256() {
        // SHA256("abc") 的 hex，用于确认算法与 Go 版 crypto/sha256 一致。
        assert_eq!(
            checksum_of(b"abc"),
            "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
        );
    }
}
