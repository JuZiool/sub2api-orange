//! `/api/v1/keys` 用户 API Key 管理路由。
//!
//! 对齐 Go 版 `internal/server/routes/user.go` 的 `/keys` 路由组与
//! `internal/handler/api_key_handler.go`。
//!
//! 已实现：
//! - `GET /api/v1/keys`（列表）
//! - `POST /api/v1/keys`（创建）
//! - `GET /api/v1/keys/:id`（单查）
//! - `DELETE /api/v1/keys/:id`（删除）
//!
//! 更新（PUT）涉及部分更新 + 字段 set/unset + 配额重置，待后续期次补齐。

use axum::extract::{Path, Query, State};
use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use axum::{Extension, Json};

use sub2api_auth::jwt_auth::AuthUser;

use crate::handler::api_key::{build_api_key_dto, parse_pagination, ApiKeyDto};
use crate::handler::api_key_write::{
    can_bind_group, escape_html_name, generate_key, is_subscription_type, tombstone_key,
    validate_create_request, validate_custom_key, validate_fallback_group, CreateApiKeyRequest,
    CreateValidationError, DeleteApiKeyResponse,
};
use crate::repository::api_key_repo::{ApiKeyListFilters, ApiKeyRepository};
use crate::response::{self, ApiResponse, PaginatedData};
use crate::routes::AppState;

/// 列表查询参数，字段名对齐 Go 版 handler 读取的 query 键。
#[derive(Debug, Default, serde::Deserialize)]
pub struct ApiKeyListQuery {
    pub page: Option<String>,
    pub page_size: Option<String>,
    pub limit: Option<String>,
    pub sort_by: Option<String>,
    pub sort_order: Option<String>,
    pub search: Option<String>,
    pub status: Option<String>,
    pub group_id: Option<String>,
}

/// `GET /api/v1/keys`
///
/// 对应 Go 版 `APIKeyHandler.List`。
pub async fn list_api_keys(
    State(state): State<AppState>,
    Extension(auth): Extension<AuthUser>,
    Query(q): Query<ApiKeyListQuery>,
) -> Response {
    let Some(pool) = state.pool.clone() else {
        tracing::error!("未配置数据库，无法列出 API Key");
        return crate::response::internal_error("Failed to list API keys");
    };
    let repo = ApiKeyRepository::new(pool);

    // 分页：与 Go 的 response.ParsePagination 一致。
    let pagination = parse_pagination(
        q.page.as_deref(),
        q.page_size.as_deref(),
        q.limit.as_deref(),
    );

    // 排序：Go 的默认值为 sort_by=created_at、sort_order=desc。
    let sort_by = q
        .sort_by
        .clone()
        .unwrap_or_else(|| "created_at".to_string());
    let sort_order = q.sort_order.clone().unwrap_or_else(|| "desc".to_string());

    // 过滤条件。search 上限 100 字节（对齐 Go 的 `len(search) > 100` 截断）。
    let mut filters = ApiKeyListFilters {
        status: q.status.clone().unwrap_or_default(),
        ..Default::default()
    };
    if let Some(raw) = q.search.as_deref() {
        let trimmed = raw.trim();
        if !trimmed.is_empty() {
            filters.search = truncate_search(trimmed);
        }
    }
    if let Some(raw) = q.group_id.as_deref() {
        if let Ok(gid) = raw.trim().parse::<i64>() {
            filters.group_id = Some(gid);
        }
    }

    let (keys, groups, total) = match repo
        .list_by_user_id(auth.user_id, pagination, &filters, &sort_by, &sort_order)
        .await
    {
        Ok(v) => v,
        Err(e) => {
            tracing::error!(error = %e, user_id = auth.user_id, "查询 API Key 列表失败");
            return crate::response::internal_error("Failed to list API keys");
        }
    };

    // 组装 DTO：预加载的分组按 id 建立索引后回填。
    let now = chrono::Utc::now();
    let find_group = |id: Option<i64>| -> Option<crate::handler::api_key::GroupDto> {
        let id = id?;
        groups.iter().find(|g| g.id == id).cloned()
    };

    let items: Vec<ApiKeyDto> = keys
        .iter()
        .map(|k| {
            build_api_key_dto(
                k,
                find_group(k.group_id),
                find_group(k.fallback_group_id),
                now,
            )
        })
        .collect();

    let data = PaginatedData::new(
        serde_json::to_value(&items).unwrap_or(serde_json::Value::Array(vec![])),
        total,
        pagination.page,
        pagination.page_size,
    );

    (
        StatusCode::OK,
        Json(ApiResponse::success(Some(
            serde_json::to_value(data).unwrap_or(serde_json::Value::Null),
        ))),
    )
        .into_response()
}

/// 截断搜索词到 100 字节（对齐 Go 版 `len(search) > 100`）。
///
/// 与 Go 的差异：Go 直接 `search[:100]` 可能切断多字节字符产生非法 UTF-8；
/// Rust 的 `&str` 不允许该情形，这里退到最近的字符边界，
/// 结果长度**不超过** 100 字节。ASCII 输入下两者完全一致。
fn truncate_search(s: &str) -> String {
    if s.len() <= 100 {
        return s.to_string();
    }
    let mut end = 100;
    while end > 0 && !s.is_char_boundary(end) {
        end -= 1;
    }
    s[..end].to_string()
}

/// 校验分组是否可被该用户绑定，对应 Go 版 `loadBindableAPIKeyGroup`。
///
/// 订阅型分组需有效订阅；标准分组走 `CanBindGroup`（公开可绑 / 专属需在列表内）。
/// 返回 `Ok(platform)` 或错误响应（装箱以缩小 `Result` 体积）。
async fn check_bindable_group(
    repo: &ApiKeyRepository,
    user_id: i64,
    group_id: i64,
) -> Result<Option<String>, Box<Response>> {
    let attrs = match repo.get_group_bind_attrs(group_id).await {
        Ok(Some(a)) => a,
        Ok(None) => {
            return Err(Box::new(response::forbidden_with_reason(
                "GROUP_NOT_ALLOWED",
                "user is not allowed to bind this group",
            )))
        }
        Err(e) => {
            tracing::error!(error = %e, group_id, "查询分组失败");
            return Err(Box::new(response::internal_error(
                "Failed to create API key",
            )));
        }
    };

    if is_subscription_type(&attrs.subscription_type) {
        match repo.has_active_subscription(user_id, group_id).await {
            Ok(true) => return Ok(Some(attrs.platform)),
            Ok(false) => {
                return Err(Box::new(response::forbidden_with_reason(
                    "GROUP_NOT_ALLOWED",
                    "user is not allowed to bind this group",
                )))
            }
            Err(e) => {
                tracing::error!(error = %e, "查询订阅失败");
                return Err(Box::new(response::internal_error(
                    "Failed to create API key",
                )));
            }
        }
    }

    let (allowed, restrict) = match repo.get_user_group_bind_info(user_id).await {
        Ok(v) => v,
        Err(e) => {
            tracing::error!(error = %e, "查询用户分组绑定信息失败");
            return Err(Box::new(response::internal_error(
                "Failed to create API key",
            )));
        }
    };

    if !can_bind_group(&allowed, restrict, attrs.id, attrs.is_exclusive) {
        return Err(Box::new(response::forbidden_with_reason(
            "GROUP_NOT_ALLOWED",
            "user is not allowed to bind this group",
        )));
    }
    Ok(Some(attrs.platform))
}

/// `POST /api/v1/keys`
///
/// 对应 Go 版 `APIKeyHandler.Create` → `APIKeyService.Create`。
/// 成功返回 HTTP 200 与业务信封（Go 用 `response.Success`）。
pub async fn create_api_key(
    State(state): State<AppState>,
    Extension(auth): Extension<AuthUser>,
    Json(req): Json<CreateApiKeyRequest>,
) -> Response {
    // 1) 请求体校验（名称必填、限额有限非负、过期天数为正）。
    if let Err(e) = validate_create_request(&req) {
        let (code, msg) = e.code_and_message();
        return match e {
            CreateValidationError::InvalidExpiry => response::bad_request_with_reason(code, msg),
            _ => response::bad_request(msg),
        };
    }

    let Some(pool) = state.pool.clone() else {
        return response::internal_error("Failed to create API key");
    };
    let repo = ApiKeyRepository::new(pool);

    let name = req.name.clone().unwrap_or_default();

    // 2) 自定义 key 校验（长度与字符集），并检查是否已存在。
    let key = if let Some(custom) = req.custom_key.as_deref().filter(|s| !s.is_empty()) {
        if let Err(e) = validate_custom_key(custom) {
            let (code, msg) = e.code_and_message();
            return response::bad_request_with_reason(code, msg);
        }
        match repo.exists_by_key(custom).await {
            Ok(true) => {
                return response::abort_with_status(
                    StatusCode::CONFLICT,
                    "API_KEY_EXISTS",
                    "api key already exists",
                )
            }
            Ok(false) => {}
            Err(e) => {
                tracing::error!(error = %e, "检查 key 是否已存在失败");
                return response::internal_error("Failed to create API key");
            }
        }
        custom.to_string()
    } else {
        match generate_key(None) {
            Some(k) => k,
            None => {
                tracing::error!("生成 API Key 失败（随机源不可用）");
                return response::internal_error("Failed to create API key");
            }
        }
    };

    // 3) 分组绑定权限校验（含平台信息，供 fallback 校验使用）。
    let primary_platform = match req.group_id {
        Some(gid) => match check_bindable_group(&repo, auth.user_id, gid).await {
            Ok(p) => p,
            Err(resp) => return *resp,
        },
        None => None,
    };

    let fallback_platform = match req.fallback_group_id {
        Some(gid) => match check_bindable_group(&repo, auth.user_id, gid).await {
            Ok(p) => p,
            Err(resp) => return *resp,
        },
        None => None,
    };

    // fallback 活跃状态：需要单独取（check_bindable_group 未返回 status）。
    let fallback_active = match req.fallback_group_id {
        Some(gid) => match repo.get_group_bind_attrs(gid).await {
            Ok(Some(a)) => a.status == "active",
            _ => false,
        },
        None => false,
    };

    if !validate_fallback_group(
        req.group_id,
        primary_platform.as_deref(),
        req.fallback_group_id,
        fallback_platform.as_deref(),
        fallback_active,
    ) {
        return response::bad_request_with_reason(
            "FALLBACK_GROUP_INVALID",
            "fallback group must differ from the primary group and use the same platform",
        );
    }

    // 4) 名称入库前做 HTML 转义（对齐 Go 的 html.EscapeString）。
    let escaped_name = escape_html_name(&name);

    // 5) 过期时间：expires_in_days 天后的当前时刻。
    let expires_at = req
        .expires_in_days
        .filter(|d| *d > 0)
        .and_then(chrono::Duration::try_days)
        .map(|d| chrono::Utc::now() + d);

    let id = match repo
        .create(
            auth.user_id,
            &key,
            &escaped_name,
            req.group_id,
            req.fallback_group_id,
            &req.ip_whitelist,
            &req.ip_blacklist,
            req.quota.unwrap_or(0.0),
            expires_at,
            req.rate_limit_5h.unwrap_or(0.0),
            req.rate_limit_1d.unwrap_or(0.0),
            req.rate_limit_7d.unwrap_or(0.0),
        )
        .await
    {
        Ok(id) => id,
        Err(e) => {
            tracing::error!(error = %e, user_id = auth.user_id, "创建 API Key 失败");
            return response::internal_error("Failed to create API key");
        }
    };

    // 6) 回读并返回完整 DTO（含分组预加载），与 Go 的 dto.APIKeyFromService 一致。
    let created = match repo.get_by_id(id).await {
        Ok(Some(k)) => k,
        _ => {
            tracing::error!(id, "创建后回读 API Key 失败");
            return response::internal_error("Failed to create API key");
        }
    };
    let groups = load_groups_for(&repo, &created).await;
    let dto = build_api_key_dto(
        &created,
        find_group(&groups, created.group_id),
        find_group(&groups, created.fallback_group_id),
        chrono::Utc::now(),
    );

    (
        StatusCode::OK,
        Json(ApiResponse::success(Some(
            serde_json::to_value(dto).unwrap_or(serde_json::Value::Null),
        ))),
    )
        .into_response()
}

/// `GET /api/v1/keys/:id`
///
/// 对应 Go 版 `APIKeyHandler.GetByID`。非本人拥有时返回 **404**
/// （Go 用 `response.NotFound`，不暴露存在性）。
pub async fn get_api_key(
    State(state): State<AppState>,
    Extension(auth): Extension<AuthUser>,
    Path(id): Path<i64>,
) -> Response {
    let Some(pool) = state.pool.clone() else {
        return response::internal_error("Failed to load API key");
    };
    let repo = ApiKeyRepository::new(pool);

    let key = match repo.get_by_id(id).await {
        Ok(Some(k)) => k,
        Ok(None) => {
            return response::not_found_with_reason("API_KEY_NOT_FOUND", "api key not found")
        }
        Err(e) => {
            tracing::error!(error = %e, id, "查询 API Key 失败");
            return response::internal_error("Failed to load API key");
        }
    };

    // 所有权校验：非本人 → 404（与 Go 一致，不区分「不存在」与「非本人」）。
    if key.user_id != auth.user_id {
        return response::not_found_with_reason("API_KEY_NOT_FOUND", "api key not found");
    }

    let groups = load_groups_for(&repo, &key).await;
    let dto = build_api_key_dto(
        &key,
        find_group(&groups, key.group_id),
        find_group(&groups, key.fallback_group_id),
        chrono::Utc::now(),
    );

    (
        StatusCode::OK,
        Json(ApiResponse::success(Some(
            serde_json::to_value(dto).unwrap_or(serde_json::Value::Null),
        ))),
    )
        .into_response()
}

/// `DELETE /api/v1/keys/:id`
///
/// 对应 Go 版 `APIKeyHandler.Delete` → `APIKeyService.Delete`：
/// 1. 取 key 与所有者；不存在 → 404
/// 2. 所有者不匹配 → 403 `INSUFFICIENT_PERMISSIONS`
/// 3. tombstone 软删除（改写 key 释放唯一键 + 置 deleted_at）
pub async fn delete_api_key(
    State(state): State<AppState>,
    Extension(auth): Extension<AuthUser>,
    Path(id): Path<i64>,
) -> Response {
    let Some(pool) = state.pool.clone() else {
        return response::internal_error("Failed to delete API key");
    };
    let repo = ApiKeyRepository::new(pool);

    let (_, owner_id) = match repo.get_key_and_owner_id(id).await {
        Ok(Some(v)) => v,
        Ok(None) => {
            return response::not_found_with_reason("API_KEY_NOT_FOUND", "api key not found")
        }
        Err(e) => {
            tracing::error!(error = %e, id, "查询 API Key 失败");
            return response::internal_error("Failed to delete API key");
        }
    };

    if owner_id != auth.user_id {
        return response::abort_with_status(
            StatusCode::FORBIDDEN,
            "INSUFFICIENT_PERMISSIONS",
            "insufficient permissions",
        );
    }

    let tombstone = tombstone_key(id, chrono::Utc::now());
    match repo.delete_with_tombstone(id, &tombstone).await {
        Ok(0) => {
            // 并发/重复删除：Go 对已软删的记录幂等返回成功。
            return (
                StatusCode::OK,
                Json(ApiResponse::success(Some(
                    serde_json::to_value(DeleteApiKeyResponse::default())
                        .unwrap_or(serde_json::Value::Null),
                ))),
            )
                .into_response();
        }
        Ok(_) => {}
        Err(e) => {
            tracing::error!(error = %e, id, "删除 API Key 失败");
            return response::internal_error("Failed to delete API key");
        }
    }

    tracing::info!(id, user_id = auth.user_id, "用户已删除 API Key");

    (
        StatusCode::OK,
        Json(ApiResponse::success(Some(
            serde_json::to_value(DeleteApiKeyResponse::default())
                .unwrap_or(serde_json::Value::Null),
        ))),
    )
        .into_response()
}

/// 加载某条 key 的 group / fallback_group（用于回填 DTO）。
async fn load_groups_for(
    repo: &ApiKeyRepository,
    key: &crate::handler::api_key::ApiKeyRecord,
) -> Vec<crate::handler::api_key::GroupDto> {
    let mut ids: Vec<i64> = Vec::new();
    if let Some(g) = key.group_id {
        ids.push(g);
    }
    if let Some(g) = key.fallback_group_id {
        ids.push(g);
    }
    ids.sort_unstable();
    ids.dedup();
    if ids.is_empty() {
        return Vec::new();
    }
    match repo.load_groups_by_ids(&ids).await {
        Ok(v) => v,
        Err(e) => {
            tracing::warn!(error = %e, "加载分组失败，DTO 将不含分组信息");
            Vec::new()
        }
    }
}

fn find_group(
    groups: &[crate::handler::api_key::GroupDto],
    id: Option<i64>,
) -> Option<crate::handler::api_key::GroupDto> {
    let id = id?;
    groups.iter().find(|g| g.id == id).cloned()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn truncate_keeps_short_search() {
        assert_eq!(truncate_search("abc"), "abc");
        assert_eq!(truncate_search(&"a".repeat(100)).len(), 100);
    }

    #[test]
    fn truncate_cuts_to_100_bytes() {
        let long = "a".repeat(150);
        assert_eq!(truncate_search(&long).len(), 100);
    }

    /// 多字节字符不得被切断（Rust 侧保证合法 UTF-8）。
    #[test]
    fn truncate_respects_char_boundary() {
        // 每个中文 3 字节，34 个 = 102 字节 > 100。
        let s = "中".repeat(34);
        let out = truncate_search(&s);
        assert!(out.len() <= 100);
        assert_eq!(out.chars().count(), 33); // 99 字节
    }
}
