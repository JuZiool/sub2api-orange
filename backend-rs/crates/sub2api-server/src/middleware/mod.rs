//! 中间件集合。
//!
//! 目标：与 Go 版 `internal/server/middleware/` 的可观测行为保持一致。
//!
//! 已实现（期 1）：
//! - [`access_log`]：HTTP 访问日志（`component=http.access`，跳过探针路径）。
//! - [`request_logger`]：`X-Request-ID` 与请求作用域日志上下文。
//! - [`request_id`]：`X-Client-Request-ID` 注入，Ops 端到端关联用。
//! - [`cors`]：跨域，含 `x-stainless-*` 放行（OpenAI SDK）。
//! - [`security_headers`]：安全头与 CSP（含验证码/支付必需域名补全）。
//! - [`recovery`]：panic 转标准 JSON 错误信封。
//!
//! 后续期次补齐：`server_timing`、`session_binding`、`audit_log`、
//! `jwt_auth`、`api_key_auth`、`panel_rate_limit`、`step_up` 等。

pub mod access_log;
pub mod cors;
pub mod recovery;
pub mod request_logger;
pub mod security_headers;

// 该中间件在 Go 版中只挂在网关路由组（见模块文档说明），
// 而网关路由尚未实现（期 5），因此本模块暂未被二进制引用。
// 其行为已由单元测试覆盖，接线时机到达后移除此标注。
#[allow(dead_code)]
pub mod request_id;
