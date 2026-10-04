//! 路由注册。
//!
//! 期 0 实现了通用路由（健康检查等），用于验证构建与部署链路。
//! 期 1 起挂载中间件（CORS / 请求 ID / panic 恢复）与标准响应信封。
//! 后续期次按 Go 版 `internal/server/routes/` 逐个模块补齐，
//! 目标是最终覆盖 658 条路由。

pub mod common;

use std::sync::Arc;

use axum::Router;
use sqlx::PgPool;

use crate::config::Config;
use crate::middleware::cors::CorsState;

/// 应用共享状态，对应 Go 版 `handler.Handlers` + 各 Service 的组合。
///
/// 后续期次会在这里挂载各类 service 与 repository；当前期次尚无 handler
/// 读取这些字段，故暂时允许未使用，避免 `clippy -D warnings` 失败。
#[allow(dead_code)]
#[derive(Clone)]
pub struct AppState {
    pub config: Arc<Config>,
    pub pool: Option<PgPool>,
}

/// 构建路由表（含中间件）。
pub fn router(config: Config, pool: Option<PgPool>) -> Router {
    let state = AppState {
        config: Arc::new(config.clone()),
        pool,
    };

    // CORS 配置在启动时归一化一次，并复现 Go 版的启动告警。
    let cors_state = Arc::new(CorsState::new(
        &config.cors.allowed_origins,
        config.cors.allow_credentials,
    ));
    if cors_state.has_no_origins() {
        tracing::warn!("CORS allowed_origins 未配置；跨域请求将被拒绝");
    }
    if cors_state.wildcard_with_specific {
        tracing::warn!("CORS allowed_origins 含 '*'；通配将优先于显式来源");
    }
    if cors_state.allow_all() && config.cors.allow_credentials {
        tracing::warn!("CORS allowed_origins 为 '*'，已禁用 allow_credentials");
    }

    Router::new()
        .merge(common::routes())
        .with_state(state)
        // 中间件顺序与 Go 版 `SetupRouter` 对齐：
        //   RequestLogger → SessionBinding → Logger → CORS → SecurityHeaders → ServerTiming
        // 期 1 先落地 CORS、请求 ID、panic 恢复；其余在后续期次补齐。
        //
        // 注意：axum 的 layer 为「后加先执行」，因此这里按反向顺序添加，
        // 使实际执行顺序为 recovery → request_id → cors。
        .layer(axum::middleware::from_fn(
            crate::middleware::recovery::recovery,
        ))
        .layer(axum::middleware::from_fn(
            crate::middleware::request_id::request_id,
        ))
        .layer(axum::middleware::from_fn_with_state(
            cors_state,
            crate::middleware::cors::cors,
        ))
}
