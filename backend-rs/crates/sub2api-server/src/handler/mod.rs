//! HTTP 处理器层。
//!
//! 每个模块对应 Go 版 `internal/handler/` 中的一个处理器及其请求/响应类型。

pub mod api_key;
pub mod api_key_write;
pub mod user_password;
pub mod user_profile;
