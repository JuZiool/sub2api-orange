//! 用户面路由（`/api/v1/user/**`）。
//!
//! 对齐 Go 版 `internal/server/routes/user.go` 的挂载方式：路由组带 JWT 认证
//! 中间件，认证通过后处理器从请求扩展读取 [`AuthUser`]。
//!
//! 已实现：
//! - `GET /api/v1/user/profile`
//! - `PUT /api/v1/user/password`
//!
//! 其余用户端点（通知邮箱、TOTP、Passkey、API Key 管理等）待后续期次补齐。

use axum::extract::State;
use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use axum::{Extension, Json};

use sub2api_auth::identity::build_identity_summary_set;
use sub2api_auth::jwt_auth::AuthUser;
use sub2api_auth::{hash_password, verify_password};

use crate::handler::user_password::{ChangePasswordRequest, ChangePasswordResponse};
use crate::handler::user_profile::build_profile_response;
use crate::repository::user_repo::UserRepository;
use crate::response::{self, business_codes, ApiResponse};
use crate::routes::AppState;

/// 从 AppState 取用户仓储。
///
/// 认证路由在无数据库时不会注册，因此这里正常不会拿到 `None`，仅作防御。
fn users_of(state: &AppState) -> Option<UserRepository> {
    state.pool.clone().map(UserRepository::new)
}

/// `GET /api/v1/user/profile`
///
/// 对应 Go 版 `UserHandler.GetProfile`。认证由中间件完成，此处从扩展取
/// [`AuthUser`]，载入用户与身份摘要后组装响应。
pub async fn get_profile(
    State(state): State<AppState>,
    Extension(auth): Extension<AuthUser>,
) -> Response {
    let Some(users) = users_of(&state) else {
        tracing::error!("未配置数据库，无法提供用户资料");
        return response::internal_error("Failed to load user");
    };

    let user = match users.get_by_id(auth.user_id).await {
        Ok(Some(mut u)) => {
            // allowed_groups 存在独立关联表中，需单独查询。
            match users.list_allowed_groups(u.id).await {
                Ok(groups) => u.allowed_groups = groups,
                Err(e) => {
                    tracing::error!(error = %e, user_id = auth.user_id, "加载可用分组失败");
                    return response::internal_error("Failed to load user");
                }
            }
            u
        }
        Ok(None) => {
            // 中间件已确认用户存在；此处覆盖并发删除等竞态。
            return response::unauthorized("User not found");
        }
        Err(e) => {
            tracing::error!(error = %e, user_id = auth.user_id, "加载用户失败");
            return response::internal_error("Failed to load user");
        }
    };

    let identities = match users.list_auth_identities(user.id).await {
        Ok(records) => build_identity_summary_set(&user.email, &user.signup_source, &records),
        Err(e) => {
            tracing::error!(error = %e, user_id = auth.user_id, "加载认证身份失败");
            return response::internal_error("Failed to load user");
        }
    };

    // balance_notify_extra_emails 为 TEXT 列，需单独取原始文本。
    let notify_raw = match users.get_notify_emails_raw(user.id).await {
        Ok(raw) => raw,
        Err(e) => {
            tracing::error!(error = %e, user_id = auth.user_id, "加载通知邮箱失败");
            return response::internal_error("Failed to load user");
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

/// `PUT /api/v1/user/password`
///
/// 对应 Go 版 `UserHandler.ChangePassword` + `UserService.ChangePassword`：
/// 1. 校验请求体（`old_password` 必填、`new_password` 必填且长度 ≥ 6）
/// 2. 校验当前密码（bcrypt），不匹配 → 400 `PASSWORD_INCORRECT`
/// 3. 写入新哈希
///
/// 改密后将 `password_hash` 变更，使**所有既有 access token 失效**——
/// 因为 TokenVersion 是 `email + password_hash` 的派生指纹，
/// 中间件第 7 步会以 `TOKEN_REVOKED` 拒绝旧 token。
pub async fn change_password(
    State(state): State<AppState>,
    Extension(auth): Extension<AuthUser>,
    Json(req): Json<ChangePasswordRequest>,
) -> Response {
    // 1) 请求体校验，对应 Go 的 binding 标签。
    if let Err(e) = req.validate() {
        return response::bad_request(format!("Invalid request: {}", e.message()));
    }

    let Some(users) = users_of(&state) else {
        tracing::error!("未配置数据库，无法改密");
        return response::internal_error("Failed to change password");
    };

    // 载入用户（含 password_hash）。
    let user = match users.get_by_id(auth.user_id).await {
        Ok(Some(u)) => u,
        Ok(None) => {
            return response::not_found_with_reason(
                business_codes::USER_NOT_FOUND,
                "user not found",
            );
        }
        Err(e) => {
            tracing::error!(error = %e, user_id = auth.user_id, "改密时加载用户失败");
            return response::internal_error("Failed to change password");
        }
    };

    // 2) 校验当前密码，对应 Go 版 `user.CheckPassword(req.CurrentPassword)`。
    if !verify_password(&req.old_password, &user.password_hash) {
        return response::bad_request_with_reason(
            business_codes::PASSWORD_INCORRECT,
            "current password is incorrect",
        );
    }

    // 3) 生成新哈希并写库。
    let new_hash = match hash_password(&req.new_password) {
        Ok(h) => h,
        Err(e) => {
            tracing::error!(error = %e, user_id = auth.user_id, "生成密码哈希失败");
            return response::internal_error("Failed to change password");
        }
    };

    match users.update_password_hash(user.id, &new_hash).await {
        Ok(0) => {
            // 用户在此期间被删除。
            return response::not_found_with_reason(
                business_codes::USER_NOT_FOUND,
                "user not found",
            );
        }
        Ok(_) => {}
        Err(e) => {
            tracing::error!(error = %e, user_id = auth.user_id, "写入新密码哈希失败");
            return response::internal_error("Failed to change password");
        }
    }

    tracing::info!(user_id = user.id, "用户已修改密码，既有 token 将失效");

    (
        StatusCode::OK,
        Json(ApiResponse::success(Some(
            serde_json::to_value(ChangePasswordResponse::default())
                .unwrap_or(serde_json::Value::Null),
        ))),
    )
        .into_response()
}
