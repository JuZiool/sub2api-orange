//! 中间件层错误信封。
//!
//! ⚠️ 注意：这与 `response::ApiResponse` **不是同一个信封**。
//!
//! | | `response::ApiResponse` | 本模块 `ErrorResponse` |
//! |---|---|---|
//! | `code` 类型 | **整数**（HTTP 状态码 / 0） | **字符串**（如 `"UNAUTHORIZED"`） |
//! | 使用方 | 业务 handler（`/api/v1/**`） | 中间件（认证、限流等） |
//!
//! 两者混用会导致前端错误分支判断失效，必须区分。来源：
//! `internal/server/middleware/middleware.go` 的 `ErrorResponse` / `AbortWithError`。

use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use axum::Json;
use serde::Serialize;

/// 中间件错误响应，`code` 为字符串。
#[derive(Debug, Clone, Serialize)]
pub struct ErrorResponse {
    pub code: String,
    pub message: String,
}

impl ErrorResponse {
    pub fn new(code: impl Into<String>, message: impl Into<String>) -> Self {
        Self {
            code: code.into(),
            message: message.into(),
        }
    }

    /// 转成完整响应。
    pub fn into_response_with_status(self, status: StatusCode) -> Response {
        (status, Json(self)).into_response()
    }
}

/// 对应 Go 版 `AbortWithError(c, statusCode, code, message)`。
pub fn abort_with_error(
    status: StatusCode,
    code: impl Into<String>,
    message: impl Into<String>,
) -> Response {
    ErrorResponse::new(code, message).into_response_with_status(status)
}

/// 认证相关错误码，取值与 Go 版 `jwt_auth.go` 逐字一致。
pub mod codes {
    /// 缺少 Authorization 头。
    pub const UNAUTHORIZED: &str = "UNAUTHORIZED";
    /// Authorization 头格式不是 `Bearer {token}`。
    pub const INVALID_AUTH_HEADER: &str = "INVALID_AUTH_HEADER";
    /// token 为空。
    pub const EMPTY_TOKEN: &str = "EMPTY_TOKEN";
    /// token 已过期。
    pub const TOKEN_EXPIRED: &str = "TOKEN_EXPIRED";
    /// token 非法。
    pub const INVALID_TOKEN: &str = "INVALID_TOKEN";
    /// 用户不存在。
    pub const USER_NOT_FOUND: &str = "USER_NOT_FOUND";
    /// 用户被禁用。
    pub const USER_INACTIVE: &str = "USER_INACTIVE";
    /// token 因改密等原因被撤销。
    pub const TOKEN_REVOKED: &str = "TOKEN_REVOKED";
    /// 服务端内部错误。
    pub const INTERNAL_ERROR: &str = "INTERNAL_ERROR";
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn code_is_string_not_number() {
        let r = ErrorResponse::new(codes::UNAUTHORIZED, "Authorization header is required");
        let v = serde_json::to_value(&r).unwrap();
        assert_eq!(
            v,
            json!({"code": "UNAUTHORIZED", "message": "Authorization header is required"})
        );
        assert!(v["code"].is_string(), "code 必须是字符串，不能是数字");
    }

    #[test]
    fn all_codes_match_go_literals() {
        assert_eq!(codes::UNAUTHORIZED, "UNAUTHORIZED");
        assert_eq!(codes::INVALID_AUTH_HEADER, "INVALID_AUTH_HEADER");
        assert_eq!(codes::EMPTY_TOKEN, "EMPTY_TOKEN");
        assert_eq!(codes::TOKEN_EXPIRED, "TOKEN_EXPIRED");
        assert_eq!(codes::INVALID_TOKEN, "INVALID_TOKEN");
        assert_eq!(codes::USER_NOT_FOUND, "USER_NOT_FOUND");
        assert_eq!(codes::USER_INACTIVE, "USER_INACTIVE");
        assert_eq!(codes::TOKEN_REVOKED, "TOKEN_REVOKED");
        assert_eq!(codes::INTERNAL_ERROR, "INTERNAL_ERROR");
    }
}
