//! 请求作用域日志中间件。
//!
//! 对齐 Go 版 `internal/server/middleware/request_logger.go`：
//!
//! - 响应头 `X-Request-ID`（注意与 `X-Client-Request-ID` 是**两个不同的头**）。
//! - 输入合法则复用，否则生成 UUID。
//! - 建立带 `component=http`、`request_id`、`client_request_id`、`path`、`method`
//!   的请求作用域日志上下文。
//!
//! 说明：Go 版通过 `context.WithValue` 传递 logger。Rust 侧使用 tracing span，
//! 等价效果是这些字段自动附加到该请求产生的所有日志上。

use axum::extract::Request;
use axum::http::{HeaderName, HeaderValue};
use axum::middleware::Next;
use axum::response::Response;

/// 请求 ID 响应头，与 Go 版 `requestIDHeader` 一致。
pub const REQUEST_ID_HEADER: &str = "X-Request-ID";

/// `X-Client-Request-ID`，用于在请求作用域日志中一并记录。
pub const CLIENT_REQUEST_ID_HEADER: &str = "X-Client-Request-ID";

/// 与 Go 版 `maxPersistentRequestIDBytes` 一致。
const MAX_PERSISTENT_REQUEST_ID_BYTES: usize = 64;

/// 归一化关联 ID：trim，且长度不超过 64 字节。
fn normalize_correlation_id(value: &str) -> Option<String> {
    let trimmed = value.trim();
    if trimmed.is_empty() || trimmed.len() > MAX_PERSISTENT_REQUEST_ID_BYTES {
        return None;
    }
    Some(trimmed.to_string())
}

fn read_header(request: &Request, name: &str) -> Option<String> {
    request
        .headers()
        .get(name)
        .and_then(|v| v.to_str().ok())
        .map(|s| s.to_string())
}

/// 中间件实现。
pub async fn request_logger(request: Request, next: Next) -> Response {
    let request_id = read_header(&request, REQUEST_ID_HEADER)
        .and_then(|v| normalize_correlation_id(&v))
        .unwrap_or_else(|| uuid::Uuid::new_v4().to_string());

    // 与 Go 版一致：client_request_id 先归一化，非法时记录为空串。
    let client_request_id = read_header(&request, CLIENT_REQUEST_ID_HEADER)
        .and_then(|v| normalize_correlation_id(&v))
        .unwrap_or_default();

    let path = request.uri().path().to_string();
    let method = request.method().to_string();

    // 请求作用域 span：下列字段会自动附加到该请求的所有日志。
    let span = tracing::info_span!(
        "http",
        component = "http",
        request_id = %request_id,
        client_request_id = %client_request_id,
        path = %path,
        method = %method,
    );

    let mut response = {
        let _guard = span.enter();
        next.run(request).await
    };

    if let Ok(name) = HeaderName::from_bytes(REQUEST_ID_HEADER.as_bytes()) {
        if let Ok(value) = HeaderValue::from_str(&request_id) {
            response.headers_mut().insert(name, value);
        }
    }

    response
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn two_request_id_headers_are_distinct() {
        assert_eq!(REQUEST_ID_HEADER, "X-Request-ID");
        assert_eq!(CLIENT_REQUEST_ID_HEADER, "X-Client-Request-ID");
        assert_ne!(REQUEST_ID_HEADER, CLIENT_REQUEST_ID_HEADER);
    }

    #[test]
    fn valid_correlation_id_reused() {
        assert_eq!(normalize_correlation_id("abc"), Some("abc".to_string()));
    }

    #[test]
    fn blank_correlation_id_rejected() {
        assert_eq!(normalize_correlation_id("  "), None);
    }

    #[test]
    fn overlong_correlation_id_rejected() {
        assert_eq!(normalize_correlation_id(&"a".repeat(65)), None);
        assert!(normalize_correlation_id(&"a".repeat(64)).is_some());
    }
}
