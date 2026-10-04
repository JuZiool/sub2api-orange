//! HTTP 访问日志中间件。
//!
//! 对齐 Go 版 `internal/server/middleware/logger.go`：
//!
//! - 记录字段 `component=http.access`、`status_code`、`latency_ms`、`client_ip`、
//!   `protocol`、`method`、`path`。
//! - **跳过** `/health` 与 `/setup/status`（高频探针，避免刷日志）。
//! - 日志消息为 `http request completed`。
//!
//! ## 与 Go 版的已知差异
//!
//! Go 版还会附加 `account_id` / `platform` / `model`（来自网关处理链路的
//! context）以及 ingress 拒绝采样。这些依赖后续期次的网关与 Ops 模块，
//! 待实现后在此补齐。当前实现覆盖基线访问日志。

use std::net::SocketAddr;
use std::time::Instant;

use axum::extract::{ConnectInfo, Request};
use axum::middleware::Next;
use axum::response::Response;

/// 不记录访问日志的探针路径，与 Go 版一致。
const SKIP_PATHS: [&str; 2] = ["/health", "/setup/status"];

/// 从请求头与连接信息中解析客户端 IP。
///
/// 与 Go 版 `ip.GetClientIP` 的完整逻辑（受信代理白名单）尚有差距：
/// 这里优先取 `X-Forwarded-For` 首个地址，其次 `X-Real-IP`，最后回退到
/// 连接的对端地址。受信代理校验将在配置模块补齐后对齐。
pub fn extract_client_ip(headers: &axum::http::HeaderMap, peer: Option<SocketAddr>) -> String {
    if let Some(xff) = headers.get("x-forwarded-for").and_then(|v| v.to_str().ok()) {
        if let Some(first) = xff.split(',').next() {
            let trimmed = first.trim();
            if !trimmed.is_empty() {
                return trimmed.to_string();
            }
        }
    }

    if let Some(real) = headers.get("x-real-ip").and_then(|v| v.to_str().ok()) {
        let trimmed = real.trim();
        if !trimmed.is_empty() {
            return trimmed.to_string();
        }
    }

    peer.map(|p| p.ip().to_string()).unwrap_or_default()
}

/// 中间件实现。
pub async fn access_log(request: Request, next: Next) -> Response {
    let start = Instant::now();
    let path = request.uri().path().to_string();
    let method = request.method().to_string();
    let protocol = format!("{:?}", request.version());
    let peer = request
        .extensions()
        .get::<ConnectInfo<SocketAddr>>()
        .map(|ci| ci.0);
    let client_ip = extract_client_ip(request.headers(), peer);

    let response = next.run(request).await;

    // 与 Go 版一致：健康检查等探针路径不记录。
    if SKIP_PATHS.contains(&path.as_str()) {
        return response;
    }

    let latency_ms = start.elapsed().as_millis() as u64;
    let status_code = response.status().as_u16();

    tracing::info!(
        component = "http.access",
        status_code,
        latency_ms,
        client_ip = %client_ip,
        protocol = %protocol,
        method = %method,
        path = %path,
        "http request completed"
    );

    response
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::http::HeaderMap;

    fn headers(pairs: &[(&str, &str)]) -> HeaderMap {
        let mut h = HeaderMap::new();
        for (k, v) in pairs {
            h.insert(
                axum::http::HeaderName::from_bytes(k.as_bytes()).unwrap(),
                axum::http::HeaderValue::from_str(v).unwrap(),
            );
        }
        h
    }

    #[test]
    fn probe_paths_are_skipped() {
        assert!(SKIP_PATHS.contains(&"/health"));
        assert!(SKIP_PATHS.contains(&"/setup/status"));
        assert!(!SKIP_PATHS.contains(&"/api/v1/user"));
    }

    #[test]
    fn xff_first_entry_wins() {
        let h = headers(&[("x-forwarded-for", "1.2.3.4, 5.6.7.8")]);
        assert_eq!(extract_client_ip(&h, None), "1.2.3.4");
    }

    #[test]
    fn xff_whitespace_trimmed() {
        let h = headers(&[("x-forwarded-for", "  9.9.9.9  , 1.1.1.1")]);
        assert_eq!(extract_client_ip(&h, None), "9.9.9.9");
    }

    #[test]
    fn falls_back_to_x_real_ip() {
        let h = headers(&[("x-real-ip", "2.2.2.2")]);
        assert_eq!(extract_client_ip(&h, None), "2.2.2.2");
    }

    #[test]
    fn falls_back_to_peer_address() {
        let h = HeaderMap::new();
        let peer: SocketAddr = "10.0.0.1:5555".parse().unwrap();
        assert_eq!(extract_client_ip(&h, Some(peer)), "10.0.0.1");
    }

    #[test]
    fn empty_headers_and_no_peer_yields_empty() {
        assert_eq!(extract_client_ip(&HeaderMap::new(), None), "");
    }

    #[test]
    fn empty_xff_falls_through() {
        let h = headers(&[("x-forwarded-for", "   "), ("x-real-ip", "3.3.3.3")]);
        assert_eq!(extract_client_ip(&h, None), "3.3.3.3");
    }
}
