//! `/api/v1/keys` 用户 API Key 管理路由。
//!
//! 对齐 Go 版 `internal/server/routes/user.go` 的 `/keys` 路由组与
//! `internal/handler/api_key_handler.go`。
//!
//! 当前已实现：`GET /api/v1/keys`（列表）。
//! 其余（创建/更新/删除/单查）待后续期次补齐。

use axum::extract::{Query, State};
use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use axum::{Extension, Json};

use sub2api_auth::jwt_auth::AuthUser;

use crate::handler::api_key::{build_api_key_dto, parse_pagination, ApiKeyDto};
use crate::repository::api_key_repo::{ApiKeyListFilters, ApiKeyRepository};
use crate::response::{ApiResponse, PaginatedData};
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
/// Rust 的 `&str` 不允许非法 UTF-8，这里退到最近的字符边界，
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
