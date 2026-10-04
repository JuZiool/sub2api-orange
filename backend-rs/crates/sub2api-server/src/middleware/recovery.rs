//! panic 恢复中间件。
//!
//! 对齐 Go 版 `internal/server/middleware/recovery.go`：把 panic 转成项目标准的
//! JSON 错误信封（HTTP 500），而不是让连接直接断开。
//!
//! Go 版有两个例外分支：broken pipe / 响应已写出。在 axum 中：
//! - 客户端断连时，写出响应会失败但不会 panic，因此无需特判；
//! - panic 发生在 handler 内时，响应尚未开始写出，等价于 Go 版
//!   `!c.Writer.Written()` 分支。
//!
//! 因此这里实现 Go 版的主路径即可，语义等价。

use axum::extract::Request;
use axum::http::StatusCode;
use axum::middleware::Next;
use axum::response::{IntoResponse, Response};
use futures::FutureExt;
use std::any::Any;
use std::panic::AssertUnwindSafe;

use crate::response::ApiResponse;

/// 把 panic 载荷转成可读字符串，便于日志排查。
fn panic_message(payload: &(dyn Any + Send)) -> String {
    if let Some(s) = payload.downcast_ref::<&str>() {
        (*s).to_string()
    } else if let Some(s) = payload.downcast_ref::<String>() {
        s.clone()
    } else {
        "unknown panic".to_string()
    }
}

/// 构造 panic 对应的标准错误响应。
///
/// 与 Go 版 `response.ErrorWithDetails(c, 500, infraerrors.UnknownMessage, ...)` 对齐：
/// `reason`/`metadata`/`data` 均为空 → 序列化时省略。
pub fn panic_response() -> Response {
    let body = ApiResponse::error(StatusCode::INTERNAL_SERVER_ERROR, "Internal Server Error");
    (StatusCode::INTERNAL_SERVER_ERROR, axum::Json(body)).into_response()
}

/// panic 恢复中间件。
pub async fn recovery(request: Request, next: Next) -> Response {
    // AssertUnwindSafe：与 Go 版 recovery 一样，接受 panic 后状态可能不完整，
    // 但要求进程继续存活并返回标准错误信封。
    let result = AssertUnwindSafe(next.run(request)).catch_unwind().await;

    match result {
        Ok(response) => response,
        Err(payload) => {
            let msg = panic_message(payload.as_ref());
            // 与 Go 版一致：记录完整 panic 信息，便于排查。
            tracing::error!(panic = %msg, "请求处理发生 panic，已转换为 500 响应");
            panic_response()
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn panic_response_matches_go_envelope() {
        // 与 Go 版 response.ErrorWithDetails(c, 500, UnknownMessage, UnknownReason, nil) 对齐：
        // reason 为空 → 省略；data/metadata 为 nil → 省略。
        let body = ApiResponse::error(StatusCode::INTERNAL_SERVER_ERROR, "Internal Server Error");
        let v = serde_json::to_value(&body).unwrap();
        assert_eq!(v, json!({"code": 500, "message": "Internal Server Error"}));
    }

    #[test]
    fn panic_message_extracts_str() {
        let payload: Box<dyn Any + Send> = Box::new("boom");
        assert_eq!(panic_message(payload.as_ref()), "boom");
    }

    #[test]
    fn panic_message_extracts_string() {
        let payload: Box<dyn Any + Send> = Box::new("boom".to_string());
        assert_eq!(panic_message(payload.as_ref()), "boom");
    }

    #[test]
    fn panic_message_falls_back_for_other_types() {
        let payload: Box<dyn Any + Send> = Box::new(42u32);
        assert_eq!(panic_message(payload.as_ref()), "unknown panic");
    }
}
