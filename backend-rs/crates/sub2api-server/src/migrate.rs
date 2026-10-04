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
//! | Advisory Lock | `694208311321144027` | 多实例串行化 |
//! | 非事务后缀 | `_notx.sql` | 此类迁移不在事务中执行 |
//!
//! 迁移 SQL 文件复用 Go 版目录 `../backend/migrations/`，**不复制、不修改**，
//! 保证两边读的是同一份 schema 定义。

use anyhow::{bail, Context};
use sha2::{Digest, Sha256};
use sqlx::{PgPool, Row};
use std::path::{Path, PathBuf};

/// 迁移记录表 DDL，与 Go 版 `schemaMigrationsTableDDL` 逐字对齐。
const SCHEMA_MIGRATIONS_TABLE_DDL: &str = r#"
CREATE TABLE IF NOT EXISTS schema_migrations (
	filename   TEXT PRIMARY KEY,
	checksum   TEXT NOT NULL,
	applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
"#;

/// Advisory Lock ID，与 Go 版 `migrationsAdvisoryLockID` 保持一致。
/// 任何稳定的 int64 值均可，只要与同库其它锁不冲突。
const MIGRATIONS_ADVISORY_LOCK_ID: i64 = 694208311321144027;

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

/// 收集并按文件名排序迁移文件。
///
/// Go 版使用零填充数字前缀（`001_`、`9000_`）保证顺序，
/// 这里直接按文件名字典序排序即可得到相同结果。
fn collect_migrations(dir: &Path) -> anyhow::Result<Vec<PathBuf>> {
    let mut files: Vec<PathBuf> = std::fs::read_dir(dir)
        .with_context(|| format!("读取迁移目录失败: {}", dir.display()))?
        .filter_map(|e| e.ok())
        .map(|e| e.path())
        .filter(|p| {
            p.is_file()
                && p.extension().map(|x| x == "sql").unwrap_or(false)
        })
        .collect();

    files.sort();
    if files.is_empty() {
        bail!("迁移目录为空: {}", dir.display());
    }
    Ok(files)
}

/// 执行全部未应用的迁移。
///
/// 语义对齐 Go 版 `ApplyMigrations`：
/// 1. 获取 advisory lock（多实例串行化）
/// 2. 确保 `schema_migrations` 表存在
/// 3. 逐文件：已应用且 checksum 一致则跳过；checksum 变化则报错
/// 4. `_notx.sql` 不在事务中执行，其余在事务中执行
pub async fn run(pool: &PgPool) -> anyhow::Result<()> {
    let dir = migrations_dir()?;
    let files = collect_migrations(&dir)?;
    tracing::info!(dir = %dir.display(), count = files.len(), "开始检查迁移");

    // 使用连接级 advisory lock 串行化迁移，避免多实例并发。
    let mut conn = pool
        .acquire()
        .await
        .context("获取迁移连接失败")?;

    sqlx::query("SELECT pg_advisory_lock($1)")
        .bind(MIGRATIONS_ADVISORY_LOCK_ID)
        .execute(&mut *conn)
        .await
        .context("获取迁移 advisory lock 失败")?;

    let result = apply_all(&mut conn, &files).await;

    // 无论成功失败都释放锁。
    let _ = sqlx::query("SELECT pg_advisory_unlock($1)")
        .bind(MIGRATIONS_ADVISORY_LOCK_ID)
        .execute(&mut *conn)
        .await;

    result
}

async fn apply_all(
    conn: &mut sqlx::PgConnection,
    files: &[PathBuf],
) -> anyhow::Result<()> {
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

        let content = std::fs::read(path)
            .with_context(|| format!("读取迁移文件失败: {}", path.display()))?;
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
                    "迁移文件已被修改，拒绝执行: {name}\n  已记录 checksum: {recorded}\n  当前 checksum:   {checksum}"
                );
            }
            skipped += 1;
            continue;
        }

        let non_tx = name.ends_with(NON_TRANSACTIONAL_SUFFIX);

        if non_tx {
            // 非事务迁移：直接执行（如 CREATE INDEX CONCURRENTLY）。
            sqlx::raw_sql(&sql)
                .execute(&mut *conn)
                .await
                .with_context(|| format!("执行非事务迁移失败: {name}"))?;
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
