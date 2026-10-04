//! 路由注册。
//!
//! 期 0 只实现通用路由（健康检查等），用于验证构建与部署链路。
//! 后续期次按 Go 版 `internal/server/routes/` 逐个模块补齐，
//! 目标是最终覆盖 658 条路由。

pub mod common;

use std::sync::Arc;

use axum::Router;
use sqlx::PgPool;

use crate::config::Config;

/// 应用共享状态，对应 Go 版 `handler.Handlers` + 各 Service 的组合。
///
/// 后续期次会在这里挂载 config、各类 service 与 repository。
#[derive(Clone)]
pub struct AppState {
    pub config: Arc<Config>,
    pub pool: Option<PgPool>,
}

/// 构建路由表。
pub fn router(config: Config, pool: Option<PgPool>) -> Router {
    let state = AppState {
        config: Arc::new(config),
        pool,
    };

    Router::new()
        .merge(common::routes())
        .with_state(state)
}
