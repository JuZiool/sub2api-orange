//! 安全响应头中间件（含 CSP）。
//!
//! 逐条对齐 Go 版 `internal/server/middleware/security_headers.go`：
//!
//! 1. 三个基线头对**所有**响应生效（包括 API 路径）：
//!    - `X-Content-Type-Options: nosniff`
//!    - `X-Frame-Options: DENY`
//!    - `Referrer-Policy: strict-origin-when-cross-origin`
//! 2. API 路径（`/v1/`、`/v1beta/`、`/antigravity/`、`/responses`、`/images`）
//!    到此为止，**不**下发 CSP。
//! 3. 其余路径在 `csp.enabled` 为真时下发 CSP：
//!    - 生成 16 字节随机 nonce（base64 标准编码）；
//!    - 用 `'nonce-<值>'` 替换策略中的 `__CSP_NONCE__` 占位符；
//!    - 随机数生成失败时降级为 `'unsafe-inline'`（与 Go 版一致）。
//! 4. 策略在启动时经 `enhance_csp_policy` 补全：确保含 nonce 占位符，
//!    并补齐验证码/支付/统计等组件必需的域名。
//! 5. 额外 frame-src 来源（来自系统设置的 iframe 白名单）逐个注入。

use std::sync::Arc;

use base64::Engine;

use axum::extract::{Request, State};
use axum::http::HeaderValue;
use axum::middleware::Next;
use axum::response::Response;

/// 策略中的 nonce 占位符，对应 Go 版 `NonceTemplate`。
pub const NONCE_TEMPLATE: &str = "__CSP_NONCE__";

/// 默认 CSP 策略，与 Go 版 `config.DefaultCSPPolicy` 逐字一致。
///
/// 注意：策略文本本身就是契约（浏览器按此放行资源），任何增删域名都可能
/// 导致验证码或支付组件加载失败，因此不能改写格式。
pub const DEFAULT_CSP_POLICY: &str = "default-src 'self'; worker-src 'self' blob:; script-src 'self' __CSP_NONCE__ https://challenges.cloudflare.com https://*.alicdn.com https://static.cloudflareinsights.com https://turing.captcha.qcloud.com https://turing.captcha.gtimg.com https://ca.turing.captcha.qcloud.com https://global.turing.captcha.gtimg.com https://www.tycaptcha.com https://cloudcache.tencentcs.com https://*.stripe.com https://static.airwallex.com https://checkout.airwallex.com https://static-demo.airwallex.com https://checkout-demo.airwallex.com; style-src 'self' 'unsafe-inline' https://*.captcha.gtimg.com https://fonts.googleapis.com https://*.alicdn.com https://static.airwallex.com https://checkout.airwallex.com https://static-demo.airwallex.com https://checkout-demo.airwallex.com; img-src 'self' data: blob: https:; font-src 'self' data: https://fonts.gstatic.com; connect-src 'self' https://turing.captcha.qcloud.com https://www.tycaptcha.com https://rce.tencentrio.com https:; frame-src 'self' https://challenges.cloudflare.com https://turing.captcha.qcloud.com https://ca.turing.captcha.qcloud.com https://www.tycaptcha.com https://*.stripe.com https://checkout.airwallex.com https://checkout-demo.airwallex.com; frame-ancestors 'none'; base-uri 'self'; form-action 'self'";

/// 必须补齐的指令值。与 Go 版 `requiredCSPDirectiveValues` 逐条一致。
const REQUIRED_CSP_DIRECTIVE_VALUES: &[(&str, &str)] = &[
    ("frame-src", "'self'"),
    ("script-src", "https://static.cloudflareinsights.com"),
    ("script-src", "https://turing.captcha.qcloud.com"),
    ("frame-src", "https://turing.captcha.qcloud.com"),
    ("style-src", "https://*.captcha.gtimg.com"),
    ("script-src", "https://turing.captcha.gtimg.com"),
    ("script-src", "https://ca.turing.captcha.qcloud.com"),
    ("script-src", "https://global.turing.captcha.gtimg.com"),
    ("script-src", "https://www.tycaptcha.com"),
    ("script-src", "https://cloudcache.tencentcs.com"),
    ("connect-src", "https://turing.captcha.qcloud.com"),
    ("connect-src", "https://www.tycaptcha.com"),
    ("connect-src", "https://rce.tencentrio.com"),
    ("frame-src", "https://ca.turing.captcha.qcloud.com"),
    ("frame-src", "https://www.tycaptcha.com"),
    ("worker-src", "blob:"),
    ("script-src", "https://*.stripe.com"),
    ("frame-src", "https://*.stripe.com"),
    ("script-src", "https://static.airwallex.com"),
    ("script-src", "https://checkout.airwallex.com"),
    ("style-src", "https://static.airwallex.com"),
    ("style-src", "https://checkout.airwallex.com"),
    ("frame-src", "https://checkout.airwallex.com"),
    ("script-src", "https://static-demo.airwallex.com"),
    ("script-src", "https://checkout-demo.airwallex.com"),
    ("style-src", "https://static-demo.airwallex.com"),
    ("style-src", "https://checkout-demo.airwallex.com"),
    ("frame-src", "https://checkout-demo.airwallex.com"),
];

/// 安全头中间件状态。
#[derive(Clone)]
pub struct SecurityHeadersState {
    /// 是否下发 CSP（对应 `csp.enabled`）。为假时只设置三个基线头。
    csp_enabled: bool,
    /// 已增强的策略（启动时计算一次）。
    policy: Arc<String>,
    /// 额外注入 `frame-src` 的来源（来自系统设置的 iframe 白名单）。
    frame_src_origins: Arc<Vec<String>>,
}

impl SecurityHeadersState {
    /// 构建状态。`policy` 为空时回退到默认策略，与 Go 版一致。
    pub fn new(csp_enabled: bool, policy: &str, frame_src_origins: Vec<String>) -> Self {
        let base = if policy.trim().is_empty() {
            DEFAULT_CSP_POLICY
        } else {
            policy
        };
        Self {
            csp_enabled,
            policy: Arc::new(enhance_csp_policy(base)),
            frame_src_origins: Arc::new(frame_src_origins),
        }
    }

    /// 为本请求计算最终策略：注入额外 frame-src 来源。
    fn final_policy(&self) -> String {
        let mut p = (*self.policy).clone();
        for origin in self.frame_src_origins.iter() {
            if !origin.is_empty() {
                p = add_to_directive(&p, "frame-src", origin);
            }
        }
        p
    }
}

/// 生成 16 字节随机 nonce 的 base64（标准编码），与 Go 版 `GenerateNonce` 一致。
///
/// 随机源不可用时返回 `None`，由调用方降级处理（与 Go 版返回 error 等价）。
pub fn generate_nonce() -> Option<String> {
    let mut buf = [0u8; 16];
    getrandom::getrandom(&mut buf).ok()?;
    Some(base64::engine::general_purpose::STANDARD.encode(buf))
}

/// 判断是否 API 路径（不下发 CSP）。与 Go 版 `isAPIRoutePath` 一致。
pub fn is_api_route_path(path: &str) -> bool {
    const PREFIXES: [&str; 5] = ["/v1/", "/v1beta/", "/antigravity/", "/responses", "/images"];
    PREFIXES.iter().any(|p| path.starts_with(p))
}

/// 增强策略：补 nonce 占位符与必需域名。与 Go 版 `enhanceCSPPolicy` 一致。
pub fn enhance_csp_policy(policy: &str) -> String {
    let mut p = policy.to_string();

    // 若既无占位符也无已替换的 nonce，则补上占位符。
    if !p.contains(NONCE_TEMPLATE) && !p.contains("'nonce-") {
        p = add_to_directive(&p, "script-src", NONCE_TEMPLATE);
    }

    for (directive, value) in REQUIRED_CSP_DIRECTIVE_VALUES {
        if !directive_has_value(&p, directive, value) {
            p = add_to_directive(&p, directive, value);
        }
    }

    p
}

/// 判断某指令是否已包含给定值。与 Go 版 `directiveHasValue` 一致。
pub fn directive_has_value(policy: &str, directive: &str, value: &str) -> bool {
    for raw in policy.split(';') {
        let fields: Vec<&str> = raw.split_whitespace().collect();
        if fields.is_empty() || fields[0] != directive {
            continue;
        }
        return fields[1..].contains(&value);
    }
    false
}

/// 向指定指令追加值。与 Go 版 `addToDirective` 一致。
pub fn add_to_directive(policy: &str, directive: &str, value: &str) -> String {
    if let Some(end) = csp_directive_end(policy, directive) {
        let mut out = String::with_capacity(policy.len() + value.len() + 1);
        out.push_str(&policy[..end]);
        out.push(' ');
        out.push_str(value);
        out.push_str(&policy[end..]);
        return out;
    }

    let trimmed = policy.trim();
    if trimmed.is_empty() {
        return new_csp_directive(directive, value);
    }

    let mut out = trimmed.to_string();
    if !out.ends_with(';') {
        out.push(';');
    }
    out.push(' ');
    out.push_str(&new_csp_directive(directive, value));
    out
}

/// 定位指令结束位置（分号下标，或串尾）。与 Go 版 `cspDirectiveEnd` 一致。
fn csp_directive_end(policy: &str, directive: &str) -> Option<usize> {
    let bytes = policy.as_bytes();
    let mut start = 0usize;
    while start <= bytes.len() {
        let end = match policy[start..].find(';') {
            Some(rel) => start + rel,
            None => bytes.len(),
        };
        let fields: Vec<&str> = policy[start..end].split_whitespace().collect();
        if !fields.is_empty() && fields[0] == directive {
            return Some(end);
        }
        if end == bytes.len() {
            break;
        }
        start = end + 1;
    }
    None
}

/// 生成单条指令文本。与 Go 版 `newCSPDirective` 一致。
fn new_csp_directive(directive: &str, value: &str) -> String {
    if value == "'self'" {
        format!("{directive} 'self';")
    } else {
        format!("{directive} 'self' {value};")
    }
}

/// 安全头中间件。
pub async fn security_headers(
    State(state): State<SecurityHeadersState>,
    request: Request,
    next: Next,
) -> Response {
    let path = request.uri().path().to_string();
    let is_api = is_api_route_path(&path);

    let mut response = if is_api {
        // API 路径：仅设置基线头，不下发 CSP。
        next.run(request).await
    } else if state.csp_enabled {
        // 生成 nonce；失败则降级为 'unsafe-inline'（与 Go 版一致）。
        let nonce = generate_nonce();
        let replacement = match &nonce {
            Some(n) => format!("'nonce-{n}'"),
            None => {
                tracing::warn!("生成 CSP nonce 失败（随机源不可用）— 降级为无 nonce 的 CSP");
                "'unsafe-inline'".to_string()
            }
        };
        let csp_value = state.final_policy().replace(NONCE_TEMPLATE, &replacement);

        let mut resp = next.run(request).await;
        if let Ok(v) = HeaderValue::from_str(&csp_value) {
            resp.headers_mut()
                .insert(axum::http::header::CONTENT_SECURITY_POLICY, v);
        }
        // 注：Go 版还会 c.Set(CSPNonceKey, nonce) 供 HTML 渲染注入。
        // Rust 侧的前端产物嵌入尚未实现（期 1 待办），届时再补充 nonce 存储。
        resp
    } else {
        next.run(request).await
    };

    let h = response.headers_mut();
    h.insert(
        axum::http::header::X_CONTENT_TYPE_OPTIONS,
        HeaderValue::from_static("nosniff"),
    );
    h.insert(
        axum::http::header::X_FRAME_OPTIONS,
        HeaderValue::from_static("DENY"),
    );
    h.insert(
        axum::http::header::REFERRER_POLICY,
        HeaderValue::from_static("strict-origin-when-cross-origin"),
    );

    response
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn default_policy_contains_nonce_template() {
        assert!(DEFAULT_CSP_POLICY.contains(NONCE_TEMPLATE));
    }

    #[test]
    fn api_paths_detected() {
        assert!(is_api_route_path("/v1/messages"));
        assert!(is_api_route_path("/v1beta/models"));
        assert!(is_api_route_path("/antigravity/x"));
        assert!(is_api_route_path("/responses"));
        assert!(is_api_route_path("/images/generations"));
        assert!(!is_api_route_path("/api/v1/user"));
        assert!(!is_api_route_path("/health"));
    }

    #[test]
    fn directive_has_value_finds_existing() {
        let policy = "default-src 'self'; script-src 'self' https://x.com";
        assert!(directive_has_value(policy, "script-src", "https://x.com"));
        assert!(!directive_has_value(policy, "script-src", "https://y.com"));
    }

    #[test]
    fn directive_has_value_unknown_directive_is_false() {
        let policy = "default-src 'self'";
        assert!(!directive_has_value(policy, "frame-src", "'self'"));
    }

    #[test]
    fn add_to_existing_directive_appends_in_place() {
        let policy = "default-src 'self'; script-src 'self'";
        let out = add_to_directive(policy, "script-src", "https://a.com");
        assert_eq!(out, "default-src 'self'; script-src 'self' https://a.com");
    }

    #[test]
    fn add_to_missing_directive_appends_new_one() {
        let policy = "default-src 'self'";
        let out = add_to_directive(policy, "frame-src", "https://a.com");
        assert_eq!(out, "default-src 'self'; frame-src 'self' https://a.com;");
    }

    #[test]
    fn add_to_empty_policy_creates_directive() {
        let out = add_to_directive("", "default-src", "'self'");
        assert_eq!(out, "default-src 'self';");
    }

    #[test]
    fn add_to_empty_policy_with_non_self_value() {
        let out = add_to_directive("   ", "script-src", "https://a.com");
        assert_eq!(out, "script-src 'self' https://a.com;");
    }

    #[test]
    fn enhance_is_idempotent() {
        let once = enhance_csp_policy(DEFAULT_CSP_POLICY);
        let twice = enhance_csp_policy(&once);
        assert_eq!(once, twice, "增强应为幂等，重复应用不应改变策略");
    }

    #[test]
    fn enhance_adds_nonce_template_when_missing() {
        let out = enhance_csp_policy("default-src 'self'");
        assert!(out.contains(NONCE_TEMPLATE));
    }

    #[test]
    fn enhance_keeps_existing_nonce() {
        // 已有具体 nonce 时不再插入占位符。
        let out = enhance_csp_policy("script-src 'self' 'nonce-abc'");
        assert!(!out.contains(NONCE_TEMPLATE));
    }

    #[test]
    fn enhance_adds_required_domains() {
        let out = enhance_csp_policy("default-src 'self'");
        for (directive, value) in REQUIRED_CSP_DIRECTIVE_VALUES {
            assert!(
                directive_has_value(&out, directive, value),
                "应补齐 {directive} {value}"
            );
        }
    }

    #[test]
    fn nonce_is_base64_of_16_bytes() {
        let nonce = generate_nonce().expect("随机源应可用");
        // 16 字节 base64 标准编码长度恒为 24（含一个 '=' 填充）。
        assert_eq!(nonce.len(), 24);
        assert!(nonce.ends_with('='));
    }

    #[test]
    fn nonces_are_unique() {
        let a = generate_nonce().unwrap();
        let b = generate_nonce().unwrap();
        assert_ne!(a, b);
    }

    #[test]
    fn final_policy_injects_extra_frame_src() {
        let st = SecurityHeadersState::new(true, "", vec!["https://iframe.example.com".into()]);
        let p = st.final_policy();
        assert!(directive_has_value(
            &p,
            "frame-src",
            "https://iframe.example.com"
        ));
    }

    #[test]
    fn final_policy_ignores_empty_origins() {
        let st = SecurityHeadersState::new(true, "", vec!["".into()]);
        let with_empty = st.final_policy();
        let base = SecurityHeadersState::new(true, "", vec![]).final_policy();
        assert_eq!(with_empty, base);
    }

    #[test]
    fn blank_policy_falls_back_to_default() {
        let st = SecurityHeadersState::new(true, "   ", vec![]);
        assert!(st.final_policy().contains("static.cloudflareinsights.com"));
    }
}
