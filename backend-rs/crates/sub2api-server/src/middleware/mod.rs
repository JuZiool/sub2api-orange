//! 中间件集合。
//!
//! 目标：与 Go 版 `internal/server/middleware/` 的可观测行为保持一致。
//!
//! 本期（期 1）实现契约最关键的四个：
//! - [`cors`]：跨域。前端依赖其头部，含 `x-stainless-*` 放行（OpenAI SDK）。
//! - [`request_id`]：`X-Client-Request-ID` 注入，Ops 模块端到端关联用。
//! - [`recovery`]：panic 转标准 JSON 错误信封。
//!
//! 后续期次补齐：`security_headers`（CSP）、`server_timing`、`logger`、
//! `audit_log`、`jwt_auth`、`api_key_auth`、`panel_rate_limit` 等。

pub mod cors;
pub mod recovery;
pub mod request_id;
