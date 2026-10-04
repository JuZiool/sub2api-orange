//! API Key 数据访问。
//!
//! 对齐 Go 版 `internal/repository/api_key_repo.go` 的
//! `apiKeyListByUserIDQuery` / `ListByUserID` / `apiKeyListOrder`。
//!
//! 表 `api_keys`，只读 schema。

use anyhow::Context;
use sqlx::{PgPool, Row};

use crate::handler::api_key::{ApiKeyRecord, GroupDto, Pagination};

/// 列表过滤条件，对应 Go 版 `service.APIKeyListFilters`。
#[derive(Debug, Clone, Default)]
pub struct ApiKeyListFilters {
    /// 名称或密钥的模糊匹配（大小写不敏感）。
    pub search: String,
    /// 精确状态匹配。
    pub status: String,
    /// `None` = 不筛选；`Some(0)` = 仅无分组；`Some(n)` = 指定分组。
    pub group_id: Option<i64>,
}

/// 排序字段（白名单，避免拼接注入）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SortField {
    Name,
    Status,
    ExpiresAt,
    LastUsedAt,
    CreatedAt,
    Id,
    Group,
}

/// 解析排序字段，对齐 Go 版 `apiKeyListOrder` 的 `switch`（未知值回退 `id`）。
pub fn parse_sort_field(sort_by: &str) -> SortField {
    match sort_by.trim().to_lowercase().as_str() {
        "name" => SortField::Name,
        "status" => SortField::Status,
        "expires_at" => SortField::ExpiresAt,
        "last_used_at" => SortField::LastUsedAt,
        "created_at" => SortField::CreatedAt,
        "group" => SortField::Group,
        _ => SortField::Id,
    }
}

/// 归一化排序方向，对齐 Go 版 `PaginationParams.NormalizedSortOrder`。
///
/// 仅接受 `asc` / `desc`（大小写不敏感），其余回退到 `default`。
pub fn normalize_sort_order(order: &str, default: &str) -> String {
    match order.trim().to_lowercase().as_str() {
        "asc" => "asc".to_string(),
        "desc" => "desc".to_string(),
        _ => default.to_string(),
    }
}

impl SortField {
    /// 数据库列名（白名单常量，非用户输入）。
    fn column(self) -> &'static str {
        match self {
            SortField::Name => "name",
            SortField::Status => "status",
            SortField::ExpiresAt => "expires_at",
            SortField::LastUsedAt => "last_used_at",
            SortField::CreatedAt => "created_at",
            SortField::Id => "id",
            // group 分支单独处理，不用此列。
            SortField::Group => "id",
        }
    }
}

/// 生成 ORDER BY 子句，对齐 Go 版 `apiKeyListOrder`。
///
/// - `group`：`NULLS LAST`，并以 `id` 升/降序作为次序键
/// - 其它字段：主序 + `id` 次序（主序已是 `id` 时不重复）
pub fn order_clause(field: SortField, order: &str) -> String {
    let desc = order == "desc";
    if field == SortField::Group {
        let dir = if desc { "DESC" } else { "ASC" };
        return format!("group_id {dir} NULLS LAST, id {dir}");
    }

    let col = field.column();
    let dir = if desc { "DESC" } else { "ASC" };
    if field == SortField::Id {
        format!("id {dir}")
    } else {
        format!("{col} {dir}, id {dir}")
    }
}

/// API Key 仓储。
#[derive(Clone)]
pub struct ApiKeyRepository {
    pool: PgPool,
}

impl ApiKeyRepository {
    pub fn new(pool: PgPool) -> Self {
        Self { pool }
    }

    /// 按用户列出 API Key，对应 Go 版 `ListByUserID`。
    ///
    /// 返回 `(记录, 总数)`。同时加载关联分组（对齐 `WithGroup().WithFallbackGroup()`）。
    pub async fn list_by_user_id(
        &self,
        user_id: i64,
        pagination: Pagination,
        filters: &ApiKeyListFilters,
        sort_by: &str,
        sort_order: &str,
    ) -> anyhow::Result<(Vec<ApiKeyRecord>, Vec<GroupDto>, i64)> {
        let field = parse_sort_field(sort_by);
        let order = normalize_sort_order(sort_order, "desc");
        let order_sql = order_clause(field, &order);

        // 过滤条件：search / status / group_id，与 Go 版逐一对应。
        let search = filters.search.clone();
        let status = filters.status.clone();
        let group_id = filters.group_id;
        // search 使用 ILIKE 模糊匹配 name 或 key（对应 NameContainsFold / KeyContainsFold）。
        let search_pattern = format!("%{}%", search);

        let limit = pagination.page_size.clamp(1, 1000);
        let offset = (pagination.page.max(1) - 1) * limit;

        let base_where = r#"
            FROM api_keys
            WHERE deleted_at IS NULL
              AND user_id = $1
              AND ($2 = '' OR name ILIKE $3 OR key ILIKE $3)
              AND ($4 = '' OR status = $4)
              AND ($5::bigint IS NULL
                   OR ($5 = 0 AND group_id IS NULL)
                   OR ($5 > 0 AND group_id = $5))
        "#;

        // 总数
        let count_sql = format!("SELECT count(*) AS total {base_where}");
        let total: i64 = sqlx::query(&count_sql)
            .bind(user_id)
            .bind(&search)
            .bind(&search_pattern)
            .bind(&status)
            .bind(group_id)
            .fetch_one(&self.pool)
            .await
            .with_context(|| format!("统计 API Key 失败: user_id={user_id}"))?
            .try_get("total")?;

        // 列表
        let list_sql = format!(
            r#"
            SELECT id, user_id, key, name, group_id, fallback_group_id, status,
                   ip_whitelist, ip_blacklist, last_used_at,
                   quota::double precision AS quota,
                   quota_used::double precision AS quota_used,
                   expires_at, created_at, updated_at,
                   rate_limit_5h::double precision AS rate_limit_5h,
                   rate_limit_1d::double precision AS rate_limit_1d,
                   rate_limit_7d::double precision AS rate_limit_7d,
                   usage_5h::double precision AS usage_5h,
                   usage_1d::double precision AS usage_1d,
                   usage_7d::double precision AS usage_7d,
                   window_5h_start, window_1d_start, window_7d_start
            {base_where}
            ORDER BY {order_sql}
            LIMIT $6 OFFSET $7
            "#
        );

        let rows = sqlx::query(&list_sql)
            .bind(user_id)
            .bind(&search)
            .bind(&search_pattern)
            .bind(&status)
            .bind(group_id)
            .bind(limit)
            .bind(offset)
            .fetch_all(&self.pool)
            .await
            .with_context(|| format!("查询 API Key 列表失败: user_id={user_id}"))?;

        let keys: Vec<ApiKeyRecord> = rows
            .into_iter()
            .map(|row| ApiKeyRecord {
                id: row.try_get("id").unwrap_or_default(),
                user_id: row.try_get("user_id").unwrap_or_default(),
                key: row.try_get("key").unwrap_or_default(),
                name: row.try_get("name").unwrap_or_default(),
                group_id: row.try_get("group_id").ok().flatten(),
                fallback_group_id: row.try_get("fallback_group_id").ok().flatten(),
                status: row.try_get("status").unwrap_or_default(),
                ip_whitelist: decode_string_array(&row, "ip_whitelist"),
                ip_blacklist: decode_string_array(&row, "ip_blacklist"),
                last_used_at: row.try_get("last_used_at").ok().flatten(),
                // last_used_ip 来自 usage_logs 聚合（Go 版 attachLastUsedIPs），
                // 待该模块实现后接入；当前列表不返回。
                last_used_ip: None,
                quota: row.try_get("quota").unwrap_or(0.0),
                quota_used: row.try_get("quota_used").unwrap_or(0.0),
                expires_at: row.try_get("expires_at").ok().flatten(),
                // 这两列在 schema 中为 NOT NULL，解码失败属异常；用 fallback 避免
                // 让整页列表因单行异常而失败（与其余字段的容错策略一致）。
                created_at: row
                    .try_get("created_at")
                    .unwrap_or_else(|_| chrono::Utc::now()),
                updated_at: row
                    .try_get("updated_at")
                    .unwrap_or_else(|_| chrono::Utc::now()),
                rate_limit_5h: row.try_get("rate_limit_5h").unwrap_or(0.0),
                rate_limit_1d: row.try_get("rate_limit_1d").unwrap_or(0.0),
                rate_limit_7d: row.try_get("rate_limit_7d").unwrap_or(0.0),
                usage_5h: row.try_get("usage_5h").unwrap_or(0.0),
                usage_1d: row.try_get("usage_1d").unwrap_or(0.0),
                usage_7d: row.try_get("usage_7d").unwrap_or(0.0),
                window_5h_start: row.try_get("window_5h_start").ok().flatten(),
                window_1d_start: row.try_get("window_1d_start").ok().flatten(),
                window_7d_start: row.try_get("window_7d_start").ok().flatten(),
            })
            .collect();

        // 加载关联分组（去重后的 group_id 集合）。
        let mut ids: Vec<i64> = Vec::new();
        for k in &keys {
            if let Some(g) = k.group_id {
                ids.push(g);
            }
            if let Some(g) = k.fallback_group_id {
                ids.push(g);
            }
        }
        ids.sort_unstable();
        ids.dedup();

        let groups = if ids.is_empty() {
            Vec::new()
        } else {
            self.load_groups(&ids).await?
        };

        Ok((keys, groups, total))
    }

    /// 按 ID 载入单个 API Key（含软删除过滤），对应 Go 版 `GetByID`。
    pub async fn get_by_id(&self, id: i64) -> anyhow::Result<Option<ApiKeyRecord>> {
        let row = sqlx::query(
            r#"
            SELECT id, user_id, key, name, group_id, fallback_group_id, status,
                   ip_whitelist, ip_blacklist, last_used_at,
                   quota::double precision AS quota,
                   quota_used::double precision AS quota_used,
                   expires_at, created_at, updated_at,
                   rate_limit_5h::double precision AS rate_limit_5h,
                   rate_limit_1d::double precision AS rate_limit_1d,
                   rate_limit_7d::double precision AS rate_limit_7d,
                   usage_5h::double precision AS usage_5h,
                   usage_1d::double precision AS usage_1d,
                   usage_7d::double precision AS usage_7d,
                   window_5h_start, window_1d_start, window_7d_start
            FROM api_keys
            WHERE id = $1 AND deleted_at IS NULL
            "#,
        )
        .bind(id)
        .fetch_optional(&self.pool)
        .await
        .with_context(|| format!("查询 API Key 失败: id={id}"))?;

        Ok(row.map(row_to_api_key_record))
    }

    /// 查询 key 是否存在（任意用户），对应 Go 版 `ExistsByKey`。
    ///
    /// 不加 `deleted_at` 过滤：tombstone 已改写 key 列，正常不会与已删除记录冲突。
    pub async fn exists_by_key(&self, key: &str) -> anyhow::Result<bool> {
        let exists: bool =
            sqlx::query_scalar("SELECT EXISTS(SELECT 1 FROM api_keys WHERE key = $1)")
                .bind(key)
                .fetch_one(&self.pool)
                .await
                .context("查询 API Key 是否存在失败")?;
        Ok(exists)
    }

    /// 创建 API Key，对应 Go 版 `Create`。
    ///
    /// `ip_whitelist` / `ip_blacklist` 为空时写 NULL（与 Go 的 `if len(...) > 0` 一致）。
    #[allow(clippy::too_many_arguments)]
    pub async fn create(
        &self,
        user_id: i64,
        key: &str,
        name: &str,
        group_id: Option<i64>,
        fallback_group_id: Option<i64>,
        ip_whitelist: &[String],
        ip_blacklist: &[String],
        quota: f64,
        expires_at: Option<chrono::DateTime<chrono::Utc>>,
        rate_limit_5h: f64,
        rate_limit_1d: f64,
        rate_limit_7d: f64,
    ) -> anyhow::Result<i64> {
        let wl = (!ip_whitelist.is_empty())
            .then(|| serde_json::to_value(ip_whitelist).unwrap_or(serde_json::Value::Null));
        let bl = (!ip_blacklist.is_empty())
            .then(|| serde_json::to_value(ip_blacklist).unwrap_or(serde_json::Value::Null));

        let id: i64 = sqlx::query_scalar(
            r#"
            INSERT INTO api_keys
                (user_id, key, name, group_id, fallback_group_id, status,
                 ip_whitelist, ip_blacklist, quota, quota_used,
                 expires_at, rate_limit_5h, rate_limit_1d, rate_limit_7d,
                 usage_5h, usage_1d, usage_7d)
            VALUES ($1, $2, $3, $4, $5, 'active', $6, $7, $8, 0, $9, $10, $11, $12, 0, 0, 0)
            RETURNING id
            "#,
        )
        .bind(user_id)
        .bind(key)
        .bind(name)
        .bind(group_id)
        .bind(fallback_group_id)
        .bind(wl)
        .bind(bl)
        .bind(quota)
        .bind(expires_at)
        .bind(rate_limit_5h)
        .bind(rate_limit_1d)
        .bind(rate_limit_7d)
        .fetch_one(&self.pool)
        .await
        .context("创建 API Key 失败")?;

        Ok(id)
    }

    /// 软删除 API Key（tombstone），对应 Go 版 `DeleteWithAudit` / `deleteWithTombstone`。
    ///
    /// 把 `key` 改写为 tombstone 以释放唯一键，同时设置 `deleted_at`。
    ///
    /// 返回受影响行数：0 表示目标不存在或已被删除。
    pub async fn delete_with_tombstone(&self, id: i64, tombstone: &str) -> anyhow::Result<u64> {
        let result = sqlx::query(
            "UPDATE api_keys SET key = $1, deleted_at = NOW(), updated_at = NOW() \
             WHERE id = $2 AND deleted_at IS NULL",
        )
        .bind(tombstone)
        .bind(id)
        .execute(&self.pool)
        .await
        .with_context(|| format!("删除 API Key 失败: id={id}"))?;

        Ok(result.rows_affected())
    }

    /// 查询 key 的 id 与所有者（**包含已软删除**），用于删除前鉴权。
    ///
    /// 对应 Go 版 `GetKeyAndOwnerID`。
    pub async fn get_key_and_owner_id(&self, id: i64) -> anyhow::Result<Option<(String, i64)>> {
        let row = sqlx::query("SELECT key, user_id FROM api_keys WHERE id = $1")
            .bind(id)
            .fetch_optional(&self.pool)
            .await
            .with_context(|| format!("查询 API Key 归属失败: id={id}"))?;

        Ok(row.map(|r| {
            (
                r.try_get::<String, _>("key").unwrap_or_default(),
                r.try_get::<i64, _>("user_id").unwrap_or_default(),
            )
        }))
    }

    /// 读取用户的分组绑定信息与限制开关，用于分组绑定权限校验。
    ///
    /// 返回 `(allowed_groups, restrict_public_groups)`。
    pub async fn get_user_group_bind_info(&self, user_id: i64) -> anyhow::Result<(Vec<i64>, bool)> {
        let row = sqlx::query(
            "SELECT restrict_public_groups FROM users WHERE id = $1 AND deleted_at IS NULL",
        )
        .bind(user_id)
        .fetch_optional(&self.pool)
        .await
        .context("查询用户分组绑定信息失败")?;

        let restrict = row
            .as_ref()
            .and_then(|r| r.try_get::<bool, _>("restrict_public_groups").ok())
            .unwrap_or(false);

        let groups: Vec<i64> =
            sqlx::query_scalar("SELECT group_id FROM user_allowed_groups WHERE user_id = $1")
                .bind(user_id)
                .fetch_all(&self.pool)
                .await
                .context("查询用户可用分组失败")?;

        Ok((groups, restrict))
    }

    /// 读取分组的关键属性，用于绑定校验与 fallback 校验。
    pub async fn get_group_bind_attrs(
        &self,
        group_id: i64,
    ) -> anyhow::Result<Option<GroupBindAttrs>> {
        let row = sqlx::query(
            r#"
            SELECT id, platform, is_exclusive, status, subscription_type
            FROM groups WHERE id = $1 AND deleted_at IS NULL
            "#,
        )
        .bind(group_id)
        .fetch_optional(&self.pool)
        .await
        .context("查询分组属性失败")?;

        Ok(row.map(|r| GroupBindAttrs {
            id: r.try_get("id").unwrap_or_default(),
            platform: r.try_get("platform").unwrap_or_default(),
            is_exclusive: r.try_get("is_exclusive").unwrap_or(false),
            status: r.try_get("status").unwrap_or_default(),
            subscription_type: r.try_get("subscription_type").unwrap_or_default(),
        }))
    }

    /// 用户在某分组下是否有有效订阅（订阅型分组的绑定前置条件）。
    pub async fn has_active_subscription(
        &self,
        user_id: i64,
        group_id: i64,
    ) -> anyhow::Result<bool> {
        let exists: bool = sqlx::query_scalar(
            r#"
            SELECT EXISTS(
                SELECT 1 FROM user_subscriptions
                WHERE user_id = $1 AND group_id = $2
                  AND status = 'active' AND deleted_at IS NULL
            )
            "#,
        )
        .bind(user_id)
        .bind(group_id)
        .fetch_one(&self.pool)
        .await
        .context("查询订阅状态失败")?;
        Ok(exists)
    }

    /// 按 ID 批量加载分组 DTO（公开入口，供单查/创建后回填使用）。
    pub async fn load_groups_by_ids(&self, ids: &[i64]) -> anyhow::Result<Vec<GroupDto>> {
        self.load_groups(ids).await
    }

    /// 按 ID 批量加载分组 DTO，对应 Go 的 `WithGroup()` / `WithFallbackGroup()` 预加载。
    async fn load_groups(&self, ids: &[i64]) -> anyhow::Result<Vec<GroupDto>> {
        let rows = sqlx::query(
            r#"
            SELECT id, name, description, platform,
                   rate_multiplier::double precision AS rate_multiplier,
                   model_rate_multipliers,
                   is_exclusive, status, subscription_type,
                   daily_limit_usd::double precision AS daily_limit_usd,
                   weekly_limit_usd::double precision AS weekly_limit_usd,
                   monthly_limit_usd::double precision AS monthly_limit_usd,
                   long_context_pricing_enabled,
                   allow_image_generation, allow_batch_image_generation,
                   image_rate_independent,
                   image_rate_multiplier::double precision AS image_rate_multiplier,
                   batch_image_discount_multiplier::double precision AS batch_image_discount_multiplier,
                   batch_image_hold_multiplier::double precision AS batch_image_hold_multiplier,
                   video_rate_independent,
                   video_rate_multiplier::double precision AS video_rate_multiplier,
                   peak_rate_enabled, peak_start, peak_end,
                   peak_rate_multiplier::double precision AS peak_rate_multiplier,
                   image_price_1k::double precision AS image_price_1k,
                   image_price_2k::double precision AS image_price_2k,
                   image_price_4k::double precision AS image_price_4k,
                   video_price_480p::double precision AS video_price_480p,
                   video_price_720p::double precision AS video_price_720p,
                   video_price_1080p::double precision AS video_price_1080p,
                   video_model_prices, web_search_price_per_call::double precision AS web_search_price_per_call,
                   search_price_per_1k::double precision AS search_price_per_1k,
                   audio_realtime_price_per_min::double precision AS audio_realtime_price_per_min,
                   audio_tts_price_per_million_chars::double precision AS audio_tts_price_per_million_chars,
                   audio_stt_price_per_hour::double precision AS audio_stt_price_per_hour,
                   claude_code_only, fallback_group_id, fallback_group_id_on_invalid_request,
                   allow_messages_dispatch, allow_live, require_oauth_only, require_privacy_set,
                   rpm_limit, max_reasoning_effort, max_reasoning_effort_over_limit,
                   reasoning_effort_mappings, created_at, updated_at
            FROM groups
            WHERE id = ANY($1) AND deleted_at IS NULL
            "#,
        )
        .bind(ids)
        .fetch_all(&self.pool)
        .await
        .context("加载分组失败")?;

        Ok(rows.into_iter().map(row_to_group_dto).collect())
    }
}

/// 分组绑定校验所需的属性子集。
#[derive(Debug, Clone)]
pub struct GroupBindAttrs {
    pub id: i64,
    pub platform: String,
    pub is_exclusive: bool,
    pub status: String,
    pub subscription_type: String,
}

/// 把一行 `api_keys` 记录转为实体（列表与单查共用）。
fn row_to_api_key_record(row: sqlx::postgres::PgRow) -> ApiKeyRecord {
    use chrono::Utc;
    ApiKeyRecord {
        id: row.try_get("id").unwrap_or_default(),
        user_id: row.try_get("user_id").unwrap_or_default(),
        key: row.try_get("key").unwrap_or_default(),
        name: row.try_get("name").unwrap_or_default(),
        group_id: row.try_get("group_id").ok().flatten(),
        fallback_group_id: row.try_get("fallback_group_id").ok().flatten(),
        status: row.try_get("status").unwrap_or_default(),
        ip_whitelist: decode_string_array(&row, "ip_whitelist"),
        ip_blacklist: decode_string_array(&row, "ip_blacklist"),
        last_used_at: row.try_get("last_used_at").ok().flatten(),
        // last_used_ip 来自 usage_logs 聚合（Go 版 attachLastUsedIPs），待后续接入。
        last_used_ip: None,
        quota: row.try_get("quota").unwrap_or(0.0),
        quota_used: row.try_get("quota_used").unwrap_or(0.0),
        expires_at: row.try_get("expires_at").ok().flatten(),
        created_at: row.try_get("created_at").unwrap_or_else(|_| Utc::now()),
        updated_at: row.try_get("updated_at").unwrap_or_else(|_| Utc::now()),
        rate_limit_5h: row.try_get("rate_limit_5h").unwrap_or(0.0),
        rate_limit_1d: row.try_get("rate_limit_1d").unwrap_or(0.0),
        rate_limit_7d: row.try_get("rate_limit_7d").unwrap_or(0.0),
        usage_5h: row.try_get("usage_5h").unwrap_or(0.0),
        usage_1d: row.try_get("usage_1d").unwrap_or(0.0),
        usage_7d: row.try_get("usage_7d").unwrap_or(0.0),
        window_5h_start: row.try_get("window_5h_start").ok().flatten(),
        window_1d_start: row.try_get("window_1d_start").ok().flatten(),
        window_7d_start: row.try_get("window_7d_start").ok().flatten(),
    }
}

/// 把 `jsonb` 列解码为 `Vec<String>`；`NULL` 保持 `None`（对应 Go 的 nil slice）。
fn decode_string_array(row: &sqlx::postgres::PgRow, col: &str) -> Option<Vec<String>> {
    let v: Option<serde_json::Value> = row.try_get(col).ok().flatten();
    match v {
        Some(serde_json::Value::Array(items)) => Some(
            items
                .into_iter()
                .filter_map(|i| i.as_str().map(|s| s.to_string()))
                .collect(),
        ),
        // NULL 或非数组 → None（序列化为 null，与 Go 的 nil slice 一致）。
        _ => None,
    }
}

/// `jsonb` → 结构体，失败时用默认值（不中断整个列表）。
fn decode_json<T: serde::de::DeserializeOwned + Default>(
    row: &sqlx::postgres::PgRow,
    col: &str,
) -> T {
    row.try_get::<Option<serde_json::Value>, _>(col)
        .ok()
        .flatten()
        .and_then(|v| serde_json::from_value(v).ok())
        .unwrap_or_default()
}

/// 可选数值列。
fn opt_f64(row: &sqlx::postgres::PgRow, col: &str) -> Option<f64> {
    row.try_get::<Option<f64>, _>(col).ok().flatten()
}

fn row_to_group_dto(row: sqlx::postgres::PgRow) -> GroupDto {
    use chrono::{DateTime, Utc};
    GroupDto {
        id: row.try_get("id").unwrap_or_default(),
        name: row.try_get("name").unwrap_or_default(),
        description: row.try_get("description").unwrap_or_default(),
        platform: row.try_get("platform").unwrap_or_default(),
        rate_multiplier: row.try_get("rate_multiplier").unwrap_or(1.0),
        model_rate_multipliers: decode_json(&row, "model_rate_multipliers"),
        is_exclusive: row.try_get("is_exclusive").unwrap_or(false),
        status: row.try_get("status").unwrap_or_default(),
        subscription_type: row.try_get("subscription_type").unwrap_or_default(),
        daily_limit_usd: opt_f64(&row, "daily_limit_usd"),
        weekly_limit_usd: opt_f64(&row, "weekly_limit_usd"),
        monthly_limit_usd: opt_f64(&row, "monthly_limit_usd"),
        long_context_pricing_enabled: row.try_get("long_context_pricing_enabled").unwrap_or(false),
        allow_image_generation: row.try_get("allow_image_generation").unwrap_or(false),
        allow_batch_image_generation: row.try_get("allow_batch_image_generation").unwrap_or(false),
        image_rate_independent: row.try_get("image_rate_independent").unwrap_or(false),
        image_rate_multiplier: row.try_get("image_rate_multiplier").unwrap_or(0.0),
        batch_image_discount_multiplier: row
            .try_get("batch_image_discount_multiplier")
            .unwrap_or(0.0),
        batch_image_hold_multiplier: row.try_get("batch_image_hold_multiplier").unwrap_or(0.0),
        video_rate_independent: row.try_get("video_rate_independent").unwrap_or(false),
        video_rate_multiplier: row.try_get("video_rate_multiplier").unwrap_or(0.0),
        peak_rate_enabled: row.try_get("peak_rate_enabled").unwrap_or(false),
        peak_start: row.try_get("peak_start").unwrap_or_default(),
        peak_end: row.try_get("peak_end").unwrap_or_default(),
        peak_rate_multiplier: row.try_get("peak_rate_multiplier").unwrap_or(0.0),
        image_price_1k: opt_f64(&row, "image_price_1k"),
        image_price_2k: opt_f64(&row, "image_price_2k"),
        image_price_4k: opt_f64(&row, "image_price_4k"),
        video_price_480p: opt_f64(&row, "video_price_480p"),
        video_price_720p: opt_f64(&row, "video_price_720p"),
        video_price_1080p: opt_f64(&row, "video_price_1080p"),
        video_model_prices: decode_json(&row, "video_model_prices"),
        web_search_price_per_call: opt_f64(&row, "web_search_price_per_call"),
        search_price_per_1k: opt_f64(&row, "search_price_per_1k"),
        audio_realtime_price_per_min: opt_f64(&row, "audio_realtime_price_per_min"),
        audio_tts_price_per_million_chars: opt_f64(&row, "audio_tts_price_per_million_chars"),
        audio_stt_price_per_hour: opt_f64(&row, "audio_stt_price_per_hour"),
        claude_code_only: row.try_get("claude_code_only").unwrap_or(false),
        fallback_group_id: row.try_get("fallback_group_id").ok().flatten(),
        fallback_group_id_on_invalid_request: row
            .try_get("fallback_group_id_on_invalid_request")
            .ok()
            .flatten(),
        allow_messages_dispatch: row.try_get("allow_messages_dispatch").unwrap_or(false),
        allow_live: row.try_get("allow_live").unwrap_or(false),
        require_oauth_only: row.try_get("require_oauth_only").unwrap_or(false),
        require_privacy_set: row.try_get("require_privacy_set").unwrap_or(false),
        rpm_limit: row.try_get("rpm_limit").unwrap_or(0),
        max_reasoning_effort: row.try_get("max_reasoning_effort").unwrap_or_default(),
        max_reasoning_effort_over_limit: row
            .try_get("max_reasoning_effort_over_limit")
            .unwrap_or_default(),
        reasoning_effort_mappings: decode_json(&row, "reasoning_effort_mappings"),
        created_at: row
            .try_get::<DateTime<Utc>, _>("created_at")
            .unwrap_or_else(|_| Utc::now()),
        updated_at: row
            .try_get::<DateTime<Utc>, _>("updated_at")
            .unwrap_or_else(|_| Utc::now()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn sort_field_parsing_matches_go() {
        assert_eq!(parse_sort_field("name"), SortField::Name);
        assert_eq!(parse_sort_field("STATUS"), SortField::Status);
        assert_eq!(parse_sort_field("created_at"), SortField::CreatedAt);
        assert_eq!(parse_sort_field("group"), SortField::Group);
        // 未知值回退 id（与 Go 的 default 分支一致）。
        assert_eq!(parse_sort_field("bogus"), SortField::Id);
        assert_eq!(parse_sort_field(""), SortField::Id);
    }

    #[test]
    fn sort_order_normalization() {
        assert_eq!(normalize_sort_order("ASC", "desc"), "asc");
        assert_eq!(normalize_sort_order("Desc", "asc"), "desc");
        // 非法值回退默认。
        assert_eq!(normalize_sort_order("bad", "desc"), "desc");
        assert_eq!(normalize_sort_order("", "desc"), "desc");
    }

    #[test]
    fn order_clause_appends_id_tiebreak() {
        assert_eq!(
            order_clause(SortField::CreatedAt, "desc"),
            "created_at DESC, id DESC"
        );
        assert_eq!(order_clause(SortField::Name, "asc"), "name ASC, id ASC");
    }

    /// 主序为 id 时不重复追加（与 Go 的 `if field != FieldID` 一致）。
    #[test]
    fn order_clause_no_duplicate_id() {
        assert_eq!(order_clause(SortField::Id, "desc"), "id DESC");
        assert_eq!(order_clause(SortField::Id, "asc"), "id ASC");
    }

    /// group 排序：未分组置末，方向同时作用于 id 次序键。
    #[test]
    fn order_clause_group_uses_nulls_last() {
        assert_eq!(
            order_clause(SortField::Group, "desc"),
            "group_id DESC NULLS LAST, id DESC"
        );
        assert_eq!(
            order_clause(SortField::Group, "asc"),
            "group_id ASC NULLS LAST, id ASC"
        );
    }
}
