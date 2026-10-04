//! 客户端请求 ID 中间件。
//!
//! 对齐 Go 版 `internal/server/middleware/client_request_id.go`：
//!
//! - 响应头固定使用 `X-Client-Request-ID`。
//! - 若请求已带该头且合法（trim 后非空且长度 ≤ 64 字节），复用之；
//!   否则生成新的 UUID v4。
//! - 请求 ID 写入 tracing span，供后续处理器与日志做端到端关联。
//!
//! ## 挂载范围（重要）
//!
//! Go 版把该中间件**只挂在网关路由组**（`internal/server/routes/gateway.go`），
//! 而非全局。因此 `/health` 等通用端点**不会**返回 `X-Client-Request-ID`。
//! Rust 侧保持同样范围：本模块由期 5 的网关路由组挂载，
//! **不要**加到全局中间件链上，否则会多出 Go 没有的响应头。

use axum::extract::Request;
use axum::http::{HeaderName, HeaderValue};
use axum::middleware::Next;
use axum::response::Response;

/// 响应/请求头名，与 Go 版 `clientRequestIDHeader` 一致。
pub const CLIENT_REQUEST_ID_HEADER: &str = "X-Client-Request-ID";

/// 与 Go 版 `maxPersistentRequestIDBytes` 一致。
const MAX_PERSISTENT_REQUEST_ID_BYTES: usize = 64;

/// 归一化请求 ID：trim，且长度不超过 64 字节。
///
/// 与 Go 版 `normalizeCorrelationID` 行为一致。
fn normalize_correlation_id(value: &str) -> Option<String> {
    let trimmed = value.trim();
    if trimmed.is_empty() || trimmed.len() > MAX_PERSISTENT_REQUEST_ID_BYTES {
        return None;
    }
    Some(trimmed.to_string())
}

/// 生成 UUID v4 字符串。
fn new_request_id() -> String {
    uuid::Uuid::new_v4().to_string()
}

/// 中间件实现。
pub async fn request_id(request: Request, next: Next) -> Response {
    // 复用客户端传入的合法 ID，否则生成新的。
    let id = request
        .headers()
        .get(CLIENT_REQUEST_ID_HEADER)
        .and_then(|v| v.to_str().ok())
        .and_then(normalize_correlation_id)
        .unwrap_or_else(new_request_id);

    // 与 Go 版一致：把 client_request_id 带入日志上下文，用于端到端关联排查。
    let span = tracing::info_span!("request", client_request_id = %id);
    let _guard = span.enter();

    let mut response = next.run(request).await;

    // 回写响应头。
    if let Ok(name) = HeaderName::from_bytes(CLIENT_REQUEST_ID_HEADER.as_bytes()) {
        if let Ok(value) = HeaderValue::from_str(&id) {
            response.headers_mut().insert(name, value);
        }
    }

    response
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn valid_id_is_reused() {
        assert_eq!(
            normalize_correlation_id("abc-123"),
            Some("abc-123".to_string())
        );
    }

    #[test]
    fn whitespace_is_trimmed() {
        assert_eq!(normalize_correlation_id("  abc  "), Some("abc".to_string()));
    }

    #[test]
    fn empty_is_rejected() {
        assert_eq!(normalize_correlation_id(""), None);
        assert_eq!(normalize_correlation_id("   "), None);
    }

    #[test]
    fn too_long_is_rejected() {
        let long = "a".repeat(65);
        assert_eq!(normalize_correlation_id(&long), None);

        let ok = "a".repeat(64);
        assert_eq!(normalize_correlation_id(&ok), Some(ok));
    }

    #[test]
    fn boundary_length_64_is_accepted() {
        // 与 Go 版 `len(value) <= maxPersistentRequestIDBytes` 一致（含 64）。
        let v = "b".repeat(64);
        assert!(normalize_correlation_id(&v).is_some());
    }

    #[test]
    fn generated_id_is_uuid_v4_shape() {
        let id = new_request_id();
        assert_eq!(id.len(), 36);
        assert_eq!(id.matches('-').count(), 4);
    }

    #[test]
    fn generated_ids_are_unique() {
        let a = new_request_id();
        let b = new_request_id();
        assert_ne!(a, b);
    }

    #[test]
    fn header_name_matches_go() {
        assert_eq!(CLIENT_REQUEST_ID_HEADER, "X-Client-Request-ID");
    }
}
