//! 通用路由（健康检查、状态等）。
//!
//! 与 Go 版 `internal/server/routes/common.go` 的响应**逐字节对齐**。
//! 这些端点被部署脚本与前端用于健康探测，契约不能变。

use axum::{
    http::StatusCode,
    routing::{get, post},
    Json, Router,
};
use serde_json::{json, Value};

use super::AppState;

pub fn routes() -> Router<AppState> {
    Router::new()
        // GET /health → {"status":"ok"}
        // 对应 Go: c.JSON(http.StatusOK, gin.H{"status": "ok"})
        .route("/health", get(health))
        // POST /api/event_logging/batch → 200 空响应体
        // Claude Code 遥测日志，Go 版直接忽略并返回 200。
        // 对应 Go: c.Status(http.StatusOK)
        .route("/api/event_logging/batch", post(event_logging_batch))
        // GET /setup/status → 固定返回已完成
        // 用于前端探测服务在初始化后是否重启。
        .route("/setup/status", get(setup_status))
}

/// 健康检查。
async fn health() -> Json<Value> {
    Json(json!({ "status": "ok" }))
}

/// Claude Code 遥测日志：忽略请求体，返回 200 空响应。
async fn event_logging_batch() -> StatusCode {
    StatusCode::OK
}

/// Setup 状态：正常模式下恒定返回 `needs_setup = false`。
async fn setup_status() -> Json<Value> {
    Json(json!({
        "code": 0,
        "data": {
            "needs_setup": false,
            "step": "completed"
        }
    }))
}
