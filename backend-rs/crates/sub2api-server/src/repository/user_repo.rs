//! 用户仓储：从 PostgreSQL 读取认证与资料所需的用户数据。
//!
//! 表结构由 Go 版迁移维护，**本模块只读、不改 schema**。
//!
//! 对照的 Go 版实现：
//! - `internal/repository/user_repo.go` 的 `GetByID`
//! - `internal/service/user_service.go` 的 `normalizeLoadedUserTokenVersion`
//! - `internal/service/user.go` 的 `User` 结构体

use anyhow::Context;
use chrono::{DateTime, Utc};
use sqlx::{PgPool, Row};

use sub2api_auth::identity::IdentityRecord;
use sub2api_auth::jwt_auth::{LoadOutcome, UserDirectory};
use sub2api_auth::resolved_token_version as derive_token_version;
use sub2api_auth::AuthUser;

/// 用户状态 `active`，与 Go 版 `domain.StatusActive` 一致。
pub const STATUS_ACTIVE: &str = "active";

/// 用户实体（本模块所需子集），字段对应 `users` 表列。
#[derive(Debug, Clone)]
pub struct User {
    pub id: i64,
    pub email: String,
    pub username: String,
    pub role: String,
    pub balance: f64,
    pub frozen_balance: f64,
    pub concurrency: i32,
    pub status: String,
    pub allowed_groups: Vec<i64>,
    pub last_active_at: Option<DateTime<Utc>>,
    pub created_at: DateTime<Utc>,
    pub updated_at: DateTime<Utc>,
    pub deleted_at: Option<DateTime<Utc>>,
    pub balance_notify_enabled: bool,
    pub balance_notify_threshold_type: String,
    pub balance_notify_threshold: Option<f64>,
    pub total_recharged: f64,
    pub rpm_limit: i32,
    pub signup_source: String,
    pub password_hash: String,
}

impl User {
    /// 是否处于可用状态，对应 Go 版 `User.IsActive()`。
    pub fn is_active(&self) -> bool {
        self.status == STATUS_ACTIVE
    }

    /// 派生 TokenVersion 指纹，对应 Go 版 `normalizeLoadedUserTokenVersion`。
    ///
    /// 注意：`users` 表**没有** `token_version` 列，该值由 email + password_hash 派生。
    pub fn resolved_token_version(&self) -> i64 {
        derive_token_version(&self.email, &self.password_hash, 0, false)
    }
}

/// 用户仓储。
#[derive(Clone)]
pub struct UserRepository {
    pool: PgPool,
}

impl UserRepository {
    pub fn new(pool: PgPool) -> Self {
        Self { pool }
    }

    /// 按 ID 载入用户，对应 Go 版 `GetByID`。
    ///
    /// 软删除的用户（`deleted_at` 非空）视为不存在。
    pub async fn get_by_id(&self, user_id: i64) -> anyhow::Result<Option<User>> {
        let row = sqlx::query(
            r#"
            SELECT id, email, username, role,
                   balance::double precision AS balance,
                   frozen_balance::double precision AS frozen_balance,
                   concurrency, status,
                   last_active_at, created_at, updated_at, deleted_at,
                   balance_notify_enabled, balance_notify_threshold_type,
                   balance_notify_threshold::double precision AS balance_notify_threshold,
                   total_recharged::double precision AS total_recharged,
                   rpm_limit, signup_source, password_hash
            FROM users
            WHERE id = $1 AND deleted_at IS NULL
            "#,
        )
        .bind(user_id)
        .fetch_optional(&self.pool)
        .await
        .with_context(|| format!("查询用户失败: id={user_id}"))?;

        let Some(row) = row else {
            return Ok(None);
        };

        Ok(Some(User {
            id: row.try_get("id")?,
            email: row.try_get("email").unwrap_or_default(),
            username: row.try_get("username").unwrap_or_default(),
            role: row.try_get("role").unwrap_or_default(),
            balance: row.try_get("balance").unwrap_or(0.0),
            frozen_balance: row.try_get("frozen_balance").unwrap_or(0.0),
            concurrency: row.try_get("concurrency").unwrap_or(0),
            status: row.try_get("status").unwrap_or_default(),
            // 关联表单独查询，见 list_allowed_groups。
            allowed_groups: Vec::new(),
            last_active_at: row.try_get("last_active_at").ok().flatten(),
            created_at: row.try_get("created_at")?,
            updated_at: row.try_get("updated_at")?,
            deleted_at: row.try_get("deleted_at").ok().flatten(),
            balance_notify_enabled: row.try_get("balance_notify_enabled").unwrap_or(false),
            balance_notify_threshold_type: row
                .try_get("balance_notify_threshold_type")
                .unwrap_or_default(),
            balance_notify_threshold: row.try_get("balance_notify_threshold").ok().flatten(),
            total_recharged: row.try_get("total_recharged").unwrap_or(0.0),
            rpm_limit: row.try_get("rpm_limit").unwrap_or(0),
            signup_source: row.try_get("signup_source").unwrap_or_default(),
            password_hash: row.try_get("password_hash").unwrap_or_default(),
        }))
    }

    /// 读取用户绑定的分组 ID 列表。
    ///
    /// 数据在 `user_allowed_groups` 关联表中（`users.allowed_groups` 列已于迁移 014
    /// 删除）。按 `group_id` 升序返回，保证输出稳定。
    pub async fn list_allowed_groups(&self, user_id: i64) -> anyhow::Result<Vec<i64>> {
        let rows = sqlx::query(
            "SELECT group_id FROM user_allowed_groups WHERE user_id = $1 ORDER BY group_id",
        )
        .bind(user_id)
        .fetch_all(&self.pool)
        .await
        .with_context(|| format!("查询用户可用分组失败: user_id={user_id}"))?;

        Ok(rows
            .into_iter()
            .filter_map(|r| r.try_get::<i64, _>("group_id").ok())
            .collect())
    }

    /// 载入用户的认证身份记录，对应 Go 版 `ListUserAuthIdentities`。
    ///
    /// 表 `auth_identities`（注意是复数形式），仅取摘要构建所需字段。
    pub async fn list_auth_identities(&self, user_id: i64) -> anyhow::Result<Vec<IdentityRecord>> {
        let rows = sqlx::query(
            r#"
            SELECT provider_type, provider_key, provider_subject,
                   verified_at, updated_at, created_at, metadata
            FROM auth_identities
            WHERE user_id = $1
            "#,
        )
        .bind(user_id)
        .fetch_all(&self.pool)
        .await
        .with_context(|| format!("查询认证身份失败: user_id={user_id}"))?;

        Ok(rows
            .into_iter()
            .map(|row| IdentityRecord {
                provider_type: row.try_get("provider_type").unwrap_or_default(),
                provider_key: row.try_get("provider_key").unwrap_or_default(),
                provider_subject: row.try_get("provider_subject").unwrap_or_default(),
                verified_at: row.try_get("verified_at").ok().flatten(),
                updated_at: row.try_get("updated_at").ok().flatten(),
                created_at: row.try_get("created_at").ok().flatten(),
                metadata: row
                    .try_get::<Option<serde_json::Value>, _>("metadata")
                    .ok()
                    .flatten()
                    .and_then(|v| match v {
                        serde_json::Value::Object(m) => Some(m),
                        _ => None,
                    })
                    .unwrap_or_default(),
            })
            .collect())
    }
    /// 更新用户的密码哈希。
    ///
    /// 对应 Go 版 `UserService.ChangePassword` 中的
    /// `userRepo.Update(ctx, user, UserUpdateFields{PasswordHash: true})`。
    ///
    /// ⚠️ `users` 表**没有** `token_version` 列（见 [`User::resolved_token_version`]）：
    /// 旧 token 的失效**不需要**额外写库，只要 `password_hash` 变化，
    /// 派生的 TokenVersion 指纹就随之变化。因此本方法只更新哈希一项。
    ///
    /// 返回受影响行数：0 表示用户不存在或已被软删除。
    pub async fn update_password_hash(
        &self,
        user_id: i64,
        password_hash: &str,
    ) -> anyhow::Result<u64> {
        let result = sqlx::query(
            "UPDATE users SET password_hash = $1, updated_at = NOW() \
             WHERE id = $2 AND deleted_at IS NULL",
        )
        .bind(password_hash)
        .bind(user_id)
        .execute(&self.pool)
        .await
        .with_context(|| format!("更新密码哈希失败: user_id={user_id}"))?;

        Ok(result.rows_affected())
    }

    /// 读取 `balance_notify_extra_emails` 列的原始文本。
    ///
    /// 该列为 `TEXT`，内容是 JSON 数组字符串；解析交给调用方，
    /// 以便解析失败时不影响资料响应的其余字段。
    pub async fn get_notify_emails_raw(&self, user_id: i64) -> anyhow::Result<String> {
        let row = sqlx::query(
            "SELECT balance_notify_extra_emails FROM users WHERE id = $1 AND deleted_at IS NULL",
        )
        .bind(user_id)
        .fetch_optional(&self.pool)
        .await
        .with_context(|| format!("查询通知邮箱失败: user_id={user_id}"))?;

        Ok(row
            .and_then(|r| {
                r.try_get::<Option<String>, _>("balance_notify_extra_emails")
                    .ok()
                    .flatten()
            })
            .unwrap_or_default())
    }
}

/// 把 [`UserRepository`] 接入认证中间件的 [`UserDirectory`]。
///
/// 这是「认证中间件」与「数据库」之间的桥：中间件只依赖 trait，
/// 具体查询由本实现提供。
impl UserDirectory for UserRepository {
    fn load<'a>(
        &'a self,
        user_id: i64,
    ) -> std::pin::Pin<Box<dyn std::future::Future<Output = LoadOutcome> + Send + 'a>> {
        Box::pin(async move {
            match self.get_by_id(user_id).await {
                Ok(Some(user)) => {
                    if !user.is_active() {
                        return LoadOutcome::Inactive;
                    }
                    let auth_user = AuthUser {
                        user_id: user.id,
                        role: user.role.clone(),
                        email: user.email.clone(),
                        // 会话 ID 由 claims 提供，这里不重复携带。
                        session_id: None,
                        concurrency: user.concurrency,
                    };
                    LoadOutcome::Found(auth_user, user.resolved_token_version())
                }
                Ok(None) => LoadOutcome::NotFound,
                Err(_) => LoadOutcome::Failed,
            }
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sample_user() -> User {
        User {
            id: 1,
            email: "a@b.com".to_string(),
            username: "u".to_string(),
            role: "user".to_string(),
            balance: 0.0,
            frozen_balance: 0.0,
            concurrency: 5,
            status: STATUS_ACTIVE.to_string(),
            allowed_groups: vec![],
            last_active_at: None,
            created_at: Utc::now(),
            updated_at: Utc::now(),
            deleted_at: None,
            balance_notify_enabled: false,
            balance_notify_threshold_type: String::new(),
            balance_notify_threshold: None,
            total_recharged: 0.0,
            rpm_limit: 0,
            signup_source: String::new(),
            password_hash: "hash".to_string(),
        }
    }

    #[test]
    fn active_status_detection() {
        let mut u = sample_user();
        assert!(u.is_active());
        u.status = "banned".to_string();
        assert!(!u.is_active());
    }

    /// 派生指纹必须与 Go 版一致（同 email+hash 得同值，且改密变值）。
    #[test]
    fn token_version_is_derived_from_email_and_hash() {
        let a = sample_user();
        let mut b = sample_user();
        assert_eq!(a.resolved_token_version(), b.resolved_token_version());

        b.password_hash = "different".to_string();
        assert_ne!(
            a.resolved_token_version(),
            b.resolved_token_version(),
            "改密后指纹必须变化"
        );
    }
}
