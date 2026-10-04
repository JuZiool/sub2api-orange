//! 用户面路由（`/api/v1/user/**`）。
//!
//! 对齐 Go 版 `internal/server/routes/user.go` 的挂载方式：路由组带 JWT 认证
//! 中间件，认证通过后处理器从请求扩展读取 [`AuthUser`]。
//!
//! 当前已实现：`GET /api/v1/user/profile`。
//! 其余用户端点（改密、通知邮箱、TOTP、Passkey、API Key 等）待后续期次补齐。

use axum::extract::State;
use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use axum::{Extension, Json};

use sub2api_auth::identity::build_identity_summary_set;
use sub2api_auth::jwt_auth::AuthUser;

use crate::handler::user_profile::build_profile_response;
use crate::repository::user_repo::UserRepository;
use crate::response::ApiResponse;
use crate::routes::AppState;

/// `GET /api/v1/user/profile`
///
/// 对应 Go 版 `UserHandler.GetProfile`。认证由中间件完成，此处从扩展取
/// [`AuthUser`]，载入用户与身份摘要后组装响应。
pub async fn get_profile(
    State(state): State<AppState>,
    Extension(auth): Extension<AuthUser>,
) -> Response {
    let Some(pool) = state.pool.clone() else {
        tracing::error!("未配置数据库，无法提供用户资料");
        return crate::response::internal_error("Failed to load user");
    };
    let users = UserRepository::new(pool);

    let user = match users.get_by_id(auth.user_id).await {
        Ok(Some(mut u)) => {
            // allowed_groups 存在独立关联表中，需单独查询。
            match users.list_allowed_groups(u.id).await {
                Ok(groups) => u.allowed_groups = groups,
                Err(e) => {
                    tracing::error!(error = %e, user_id = auth.user_id, "加载可用分组失败");
                    return crate::response::internal_error("Failed to load user");
                }
            }
            u
        }
        Ok(None) => {
            // 中间件已确认用户存在；此处覆盖并发删除等竞态。
            return crate::response::unauthorized("User not found");
        }
        Err(e) => {
            tracing::error!(error = %e, user_id = auth.user_id, "加载用户失败");
            return crate::response::internal_error("Failed to load user");
        }
    };

    let identities = match users.list_auth_identities(user.id).await {
        Ok(records) => build_identity_summary_set(&user.email, &user.signup_source, &records),
        Err(e) => {
            tracing::error!(error = %e, user_id = auth.user_id, "加载认证身份失败");
            return crate::response::internal_error("Failed to load user");
        }
    };

    // balance_notify_extra_emails 为 TEXT 列，需单独取原始文本。
    let notify_raw = match users.get_notify_emails_raw(user.id).await {
        Ok(raw) => raw,
        Err(e) => {
            tracing::error!(error = %e, user_id = auth.user_id, "加载通知邮箱失败");
            return crate::response::internal_error("Failed to load user");
        }
    };

    let profile = build_profile_response(&user, identities, &notify_raw);
    (
        StatusCode::OK,
        Json(ApiResponse::success(Some(
            serde_json::to_value(profile).unwrap_or(serde_json::Value::Null),
        ))),
    )
        .into_response()
}
