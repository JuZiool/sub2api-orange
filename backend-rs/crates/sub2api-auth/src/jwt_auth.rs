//! JWT 认证中间件。
//!
//! 对齐 Go 版 `internal/server/middleware/jwt_auth.go` 的判定顺序：
//!
//! 1. 缺少 `Authorization` 头 → 401 `UNAUTHORIZED`
//! 2. 格式不是 `Bearer {token}`（scheme 大小写不敏感）→ 401 `INVALID_AUTH_HEADER`
//! 3. token 去空白后为空 → 401 `EMPTY_TOKEN`
//! 4. 校验失败：过期 → 401 `TOKEN_EXPIRED`；其余 → 401 `INVALID_TOKEN`
//! 5. 按 `user_id` 载入用户：不存在 → 401 `USER_NOT_FOUND`；查询失败 → 500 `INTERNAL_ERROR`
//! 6. 用户非 active → 401 `USER_INACTIVE`
//! 7. `claims.token_version != user.token_version` → 401 `TOKEN_REVOKED`
//!
//! 通过后把 [`AuthUser`] 写入请求扩展，供后续 handler 读取。
//!
//! ## 尚未实现
//!
//! - **会话绑定校验**（`enforceSessionBinding`）：依赖系统设置与审计服务，
//!   待这些模块实现后接入。当前不做该校验，属**已知的宽松差异**。
//! - `TouchLastActiveForUser`（活跃时间更新）：依赖用户仓储。

use std::sync::Arc;

use axum::extract::{Request, State};
use axum::http::StatusCode;
use axum::middleware::Next;
use axum::response::Response;

use crate::errors::{abort_with_error, codes};
use crate::jwt::{validate_token, TokenError};

/// 认证成功后的用户上下文，对应 Go 版写入 gin context 的 `AuthSubject` 等字段。
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AuthUser {
    pub user_id: i64,
    pub role: String,
    pub email: String,
    /// 会话 ID，来自 claims 的 `sid`。
    pub session_id: Option<String>,
    /// 并发数（Go 版 `AuthSubject.Concurrency`），供后续限流/并发控制使用。
    pub concurrency: i32,
}

/// 用户载入结果。
pub enum LoadOutcome {
    /// 用户存在且可用于认证。
    Found(AuthUser, i64),
    /// 用户不存在。
    NotFound,
    /// 查询过程出错（对应 Go 的 500 INTERNAL_ERROR）。
    Failed,
    /// 用户存在但状态非 active。
    Inactive,
}

/// 用户目录：按 ID 载入认证所需的用户信息。
///
/// Rust 侧的用户仓储尚未实现，因此以 trait 作为集成点。
/// 返回装箱 future 是为了让该 trait 可作为 `dyn UserDirectory` 使用。
pub trait UserDirectory: Send + Sync {
    fn load<'a>(
        &'a self,
        user_id: i64,
    ) -> std::pin::Pin<Box<dyn std::future::Future<Output = LoadOutcome> + Send + 'a>>;
}

/// JWT 认证中间件状态。
#[derive(Clone)]
pub struct JwtAuthState {
    /// 签名密钥，对应 Go 的 `jwt.secret`。
    pub secret: Arc<String>,
    /// 允许的签名算法集合（默认 HS256/384/512），由 `jwt::validate_token` 内部约束。
    pub users: Arc<dyn UserDirectory>,
}

/// 认证失败原因，承载 HTTP 状态码、错误码与文案。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AuthFailure {
    MissingHeader,
    InvalidHeaderFormat,
    EmptyToken,
    TokenExpired,
    InvalidToken,
    UserNotFound,
    InternalError,
    UserInactive,
    TokenRevoked,
}

impl AuthFailure {
    pub fn status(self) -> StatusCode {
        match self {
            AuthFailure::InternalError => StatusCode::INTERNAL_SERVER_ERROR,
            _ => StatusCode::UNAUTHORIZED,
        }
    }

    /// 错误码与文案与 Go 版逐字一致。
    pub fn code_and_message(self) -> (&'static str, &'static str) {
        match self {
            AuthFailure::MissingHeader => (codes::UNAUTHORIZED, "Authorization header is required"),
            AuthFailure::InvalidHeaderFormat => (
                codes::INVALID_AUTH_HEADER,
                "Authorization header format must be 'Bearer {token}'",
            ),
            AuthFailure::EmptyToken => (codes::EMPTY_TOKEN, "Token cannot be empty"),
            AuthFailure::TokenExpired => (codes::TOKEN_EXPIRED, "Token has expired"),
            AuthFailure::InvalidToken => (codes::INVALID_TOKEN, "Invalid token"),
            AuthFailure::UserNotFound => (codes::USER_NOT_FOUND, "User not found"),
            AuthFailure::InternalError => (codes::INTERNAL_ERROR, "Failed to load user"),
            AuthFailure::UserInactive => (codes::USER_INACTIVE, "User account is not active"),
            AuthFailure::TokenRevoked => (
                codes::TOKEN_REVOKED,
                "Token has been revoked (password changed)",
            ),
        }
    }

    pub fn to_response(self) -> Response {
        let (code, message) = self.code_and_message();
        abort_with_error(self.status(), code, message)
    }
}

/// 从 `Authorization` 头中解析 Bearer token。
///
/// 纯函数，便于单测。scheme 比较大小写不敏感（对应 Go 的 `strings.EqualFold`）。
pub fn parse_bearer_token(header: Option<&str>) -> Result<String, AuthFailure> {
    let header = match header {
        Some(v) if !v.is_empty() => v,
        _ => return Err(AuthFailure::MissingHeader),
    };

    let mut parts = header.splitn(2, ' ');
    let scheme = parts.next().unwrap_or("");
    let rest = parts.next();

    match rest {
        Some(token) if scheme.eq_ignore_ascii_case("Bearer") => {
            let token = token.trim();
            if token.is_empty() {
                Err(AuthFailure::EmptyToken)
            } else {
                Ok(token.to_string())
            }
        }
        _ => Err(AuthFailure::InvalidHeaderFormat),
    }
}

/// 把 JWT 校验错误映射为认证失败原因。
pub fn map_token_error(err: &TokenError) -> AuthFailure {
    match err {
        TokenError::Expired(_) => AuthFailure::TokenExpired,
        TokenError::TooLarge | TokenError::Invalid => AuthFailure::InvalidToken,
    }
}

/// 中间件实现。
pub async fn jwt_auth(
    State(state): State<JwtAuthState>,
    mut request: Request,
    next: Next,
) -> Response {
    // 1~3：解析 Authorization 头。
    let header = request
        .headers()
        .get(axum::http::header::AUTHORIZATION)
        .and_then(|v| v.to_str().ok());

    let token = match parse_bearer_token(header) {
        Ok(t) => t,
        Err(failure) => return failure.to_response(),
    };

    // 4：校验签名与有效期。
    let claims = match validate_token(&state.secret, &token) {
        Ok(c) => c,
        Err(e) => return map_token_error(&e).to_response(),
    };

    // 5：载入用户。
    let (mut user, user_token_version) = match state.users.load(claims.user_id).await {
        LoadOutcome::Found(user, token_version) => (user, token_version),
        LoadOutcome::NotFound => return AuthFailure::UserNotFound.to_response(),
        LoadOutcome::Failed => return AuthFailure::InternalError.to_response(),
        LoadOutcome::Inactive => return AuthFailure::UserInactive.to_response(),
    };

    // 会话 ID 来自 claims（Go 版写入 context 的 ContextKeySessionID）。
    user.session_id = claims.sid.clone();

    // 6：TokenVersion 校验（改密后旧 token 失效）。
    if claims.token_version != user_token_version {
        return AuthFailure::TokenRevoked.to_response();
    }

    request.extensions_mut().insert(user);
    next.run(request).await
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn missing_header_rejected() {
        assert_eq!(parse_bearer_token(None), Err(AuthFailure::MissingHeader));
        assert_eq!(
            parse_bearer_token(Some("")),
            Err(AuthFailure::MissingHeader)
        );
    }

    #[test]
    fn bearer_token_parsed() {
        assert_eq!(
            parse_bearer_token(Some("Bearer abc.def.ghi")).unwrap(),
            "abc.def.ghi"
        );
    }

    #[test]
    fn bearer_scheme_case_insensitive() {
        // 与 Go 版 strings.EqualFold 一致。
        for scheme in ["bearer", "BEARER", "BeArEr"] {
            assert_eq!(
                parse_bearer_token(Some(&format!("{scheme} tok"))).unwrap(),
                "tok"
            );
        }
    }

    #[test]
    fn bearer_token_is_trimmed() {
        assert_eq!(parse_bearer_token(Some("Bearer   tok  ")).unwrap(), "tok");
    }

    #[test]
    fn empty_token_after_scheme_rejected() {
        assert_eq!(
            parse_bearer_token(Some("Bearer    ")),
            Err(AuthFailure::EmptyToken)
        );
    }

    #[test]
    fn wrong_scheme_rejected() {
        assert_eq!(
            parse_bearer_token(Some("Basic abc")),
            Err(AuthFailure::InvalidHeaderFormat)
        );
    }

    #[test]
    fn scheme_only_rejected() {
        assert_eq!(
            parse_bearer_token(Some("Bearer")),
            Err(AuthFailure::InvalidHeaderFormat)
        );
    }

    #[test]
    fn token_error_maps_to_failure() {
        assert_eq!(
            map_token_error(&TokenError::TooLarge),
            AuthFailure::InvalidToken
        );
        assert_eq!(
            map_token_error(&TokenError::Invalid),
            AuthFailure::InvalidToken
        );
    }

    #[test]
    fn failure_codes_and_messages_match_go() {
        let cases = [
            (
                AuthFailure::MissingHeader,
                401,
                "UNAUTHORIZED",
                "Authorization header is required",
            ),
            (
                AuthFailure::InvalidHeaderFormat,
                401,
                "INVALID_AUTH_HEADER",
                "Authorization header format must be 'Bearer {token}'",
            ),
            (
                AuthFailure::EmptyToken,
                401,
                "EMPTY_TOKEN",
                "Token cannot be empty",
            ),
            (
                AuthFailure::TokenExpired,
                401,
                "TOKEN_EXPIRED",
                "Token has expired",
            ),
            (
                AuthFailure::InvalidToken,
                401,
                "INVALID_TOKEN",
                "Invalid token",
            ),
            (
                AuthFailure::UserNotFound,
                401,
                "USER_NOT_FOUND",
                "User not found",
            ),
            (
                AuthFailure::InternalError,
                500,
                "INTERNAL_ERROR",
                "Failed to load user",
            ),
            (
                AuthFailure::UserInactive,
                401,
                "USER_INACTIVE",
                "User account is not active",
            ),
            (
                AuthFailure::TokenRevoked,
                401,
                "TOKEN_REVOKED",
                "Token has been revoked (password changed)",
            ),
        ];

        for (failure, status, code, message) in cases {
            assert_eq!(failure.status().as_u16(), status, "{failure:?} 状态码");
            let (c, m) = failure.code_and_message();
            assert_eq!(c, code, "{failure:?} 错误码");
            assert_eq!(m, message, "{failure:?} 文案");
        }
    }
}
