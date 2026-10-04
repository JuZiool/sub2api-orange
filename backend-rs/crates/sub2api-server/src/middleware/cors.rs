//! CORS 中间件。
//!
//! 逐条对齐 Go 版 `internal/server/middleware/cors.go` 的行为：
//!
//! 1. `allowed_origins` 为空 → 拒绝所有跨域（预检返回 403）。
//! 2. 含 `*` → 通配优先，忽略其它显式 origin。
//! 3. `*` + `allow_credentials=true` → 强制关闭 credentials（浏览器不允许该组合）。
//! 4. `Vary: Origin` 仅在非通配且 origin 非空时附加。
//! 5. 预检（OPTIONS）命中 → 204；未命中 → 403。
//! 6. 允许头固定列表，并放行全部 `x-stainless-*`（OpenAI Node SDK 会发送）。

use std::sync::Arc;

use axum::extract::{Request, State};
use axum::http::{header, HeaderValue, Method, StatusCode};
use axum::middleware::Next;
use axum::response::{IntoResponse, Response};

/// CORS 运行期配置（已在构建时完成归一化）。
#[derive(Debug, Clone)]
pub struct CorsState {
    /// 归一化后的允许来源（已 trim、去空）。若含 `*` 则被规整为仅 `["*"]`。
    allowed_origins: Vec<String>,
    /// 是否通配放行。
    allow_all: bool,
    /// 是否允许携带凭证（通配时强制 false）。
    allow_credentials: bool,
    /// 是否配置了通配与显式 origin 混用（启动时警告一次）。
    pub wildcard_with_specific: bool,
    /// 是否未配置任何允许来源（此时拒绝所有跨域）。
    no_origins: bool,
}

impl CorsState {
    /// 构建 CORS 状态，归一化逻辑与 Go 版一致。
    pub fn new(allowed_origins: &[String], allow_credentials: bool) -> Self {
        // 1) trim 并丢弃空项
        let normalized: Vec<String> = allowed_origins
            .iter()
            .map(|s| s.trim().to_string())
            .filter(|s| !s.is_empty())
            .collect();

        // 2) 检测通配
        let allow_all = normalized.iter().any(|o| o == "*");

        // 3) 通配与显式混用 → 仅保留 "*"
        let wildcard_with_specific = allow_all && normalized.len() > 1;
        let allowed_origins = if wildcard_with_specific {
            vec!["*".to_string()]
        } else {
            normalized
        };

        // 4) 通配 + credentials 互斥 → 强制关闭
        let allow_credentials = if allow_all { false } else { allow_credentials };

        Self {
            no_origins: allowed_origins.is_empty(),
            allow_all,
            allowed_origins,
            allow_credentials,
            wildcard_with_specific,
        }
    }

    /// 是否未配置任何允许来源。
    pub fn has_no_origins(&self) -> bool {
        self.no_origins
    }

    /// 是否通配放行。
    pub fn allow_all(&self) -> bool {
        self.allow_all
    }

    /// 判断来源是否被允许。
    fn origin_allowed(&self, origin: &str) -> bool {
        if self.allow_all {
            return true;
        }
        if origin.is_empty() {
            return false;
        }
        self.allowed_origins.iter().any(|o| o == origin)
    }

    /// 允许的请求头列表，与 Go 版 `allowHeaders` 一致。
    fn allow_headers_value() -> String {
        let base = [
            "Content-Type",
            "Content-Length",
            "Accept-Encoding",
            "X-CSRF-Token",
            "Authorization",
            "accept",
            "origin",
            "Cache-Control",
            "X-Requested-With",
            "X-API-Key",
            "X-Admin-UI-Request",
            "X-User-UI-Request",
        ];

        // OpenAI Node SDK 会发送 x-stainless-* 请求头，需显式放行。
        let openai_properties = [
            "lang",
            "package-version",
            "os",
            "arch",
            "retry-count",
            "runtime",
            "runtime-version",
            "async",
            "helper-method",
            "poll-helper",
            "custom-poll-interval",
            "timeout",
        ];

        let mut all: Vec<String> = base.iter().map(|s| s.to_string()).collect();
        for p in openai_properties {
            all.push(format!("x-stainless-{p}"));
        }
        all.join(", ")
    }
}

/// CORS 中间件。需通过 `axum::middleware::from_fn_with_state` 挂载。
pub async fn cors(
    State(state): State<Arc<CorsState>>,
    request: Request<axum::body::Body>,
    next: Next,
) -> Response {
    let headers = request.headers();
    let origin = headers
        .get(header::ORIGIN)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("")
        .trim()
        .to_string();

    let allowed = state.origin_allowed(&origin);
    let is_preflight = request.method() == Method::OPTIONS;

    let mut response = if is_preflight {
        if allowed {
            StatusCode::NO_CONTENT.into_response()
        } else {
            StatusCode::FORBIDDEN.into_response()
        }
    } else {
        next.run(request).await
    };

    if allowed {
        let h = response.headers_mut();

        if state.allow_all {
            h.insert(
                header::ACCESS_CONTROL_ALLOW_ORIGIN,
                HeaderValue::from_static("*"),
            );
        } else if !origin.is_empty() {
            if let Ok(v) = HeaderValue::from_str(&origin) {
                h.insert(header::ACCESS_CONTROL_ALLOW_ORIGIN, v);
            }
            // Go 版使用 Add（追加），此处保持一致。
            h.append(header::VARY, HeaderValue::from_static("Origin"));
        }

        if state.allow_credentials {
            h.insert(
                header::ACCESS_CONTROL_ALLOW_CREDENTIALS,
                HeaderValue::from_static("true"),
            );
        }

        if let Ok(v) = HeaderValue::from_str(&CorsState::allow_headers_value()) {
            h.insert(header::ACCESS_CONTROL_ALLOW_HEADERS, v);
        }
        h.insert(
            header::ACCESS_CONTROL_ALLOW_METHODS,
            HeaderValue::from_static("POST, OPTIONS, GET, PUT, DELETE, PATCH"),
        );
        h.insert(
            header::ACCESS_CONTROL_EXPOSE_HEADERS,
            HeaderValue::from_static("ETag, Server-Timing"),
        );
        h.insert(
            header::ACCESS_CONTROL_MAX_AGE,
            HeaderValue::from_static("86400"),
        );
    }

    // 未命中的非预检请求仍会进入后续链路（与 Go 版一致：不阻断，仅不加 CORS 头）。
    response
}

#[cfg(test)]
mod tests {
    use super::*;

    fn s(v: &[&str]) -> Vec<String> {
        v.iter().map(|x| x.to_string()).collect()
    }

    #[test]
    fn empty_origins_rejects_all() {
        let st = CorsState::new(&[], true);
        assert!(st.has_no_origins());
        assert!(!st.origin_allowed("https://example.com"));
    }

    #[test]
    fn explicit_origin_allowed() {
        let st = CorsState::new(&s(&["https://a.com", "https://b.com"]), true);
        assert!(st.origin_allowed("https://a.com"));
        assert!(!st.origin_allowed("https://c.com"));
    }

    #[test]
    fn whitespace_is_trimmed_and_empties_dropped() {
        let st = CorsState::new(&s(&["  https://a.com  ", "   "]), false);
        assert_eq!(st.allowed_origins, vec!["https://a.com".to_string()]);
    }

    #[test]
    fn wildcard_wins_over_specific_origins() {
        let st = CorsState::new(&s(&["*", "https://a.com"]), false);
        assert!(st.wildcard_with_specific);
        assert_eq!(st.allowed_origins, vec!["*".to_string()]);
        assert!(st.allow_all());
    }

    #[test]
    fn wildcard_forces_credentials_off() {
        let st = CorsState::new(&s(&["*"]), true);
        assert!(!st.allow_credentials);
    }

    #[test]
    fn credentials_kept_without_wildcard() {
        let st = CorsState::new(&s(&["https://a.com"]), true);
        assert!(st.allow_credentials);
    }

    #[test]
    fn allow_headers_include_stainless() {
        let v = CorsState::allow_headers_value();
        assert!(v.contains("X-API-Key"));
        assert!(v.contains("x-stainless-lang"));
        assert!(v.contains("x-stainless-timeout"));
    }
}
