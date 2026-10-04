//! 路由注册。
//!
//! 期 0 实现了通用路由（健康检查等），用于验证构建与部署链路。
//! 期 1 挂载全局中间件并引入标准响应信封。
//! 后续期次按 Go 版 `internal/server/routes/` 逐个模块补齐，
//! 目标是最终覆盖 658 条路由。

pub mod common;

use std::sync::Arc;

use axum::Router;
use sqlx::PgPool;

use crate::config::Config;
use crate::middleware::cors::CorsState;
use crate::middleware::security_headers::SecurityHeadersState;

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

    // 安全头 / CSP：策略在启动时增强一次。
    // 动态 frame-src 来源（系统设置中的 iframe 白名单）待设置模块实现后接入，
    // 当前传空列表，与“未配置额外来源”的行为一致。
    let security_state = SecurityHeadersState::new(
        config.security.csp.enabled,
        &config.security.csp.policy,
        Vec::new(),
    );

    // 中间件执行顺序（外层先执行），对齐 Go 版 `SetupRouter`：
    //   Recovery → RequestLogger → Logger → CORS → SecurityHeaders
    //
    // 注：axum 的 `layer` 为「后加者更外层」，因此下面的调用顺序与执行顺序相反。
    // `ClientRequestID` 与 `ServerTiming` 不在此处：前者在 Go 版仅作用于网关
    // 路由组，后者待实现。
    Router::new()
        .merge(common::routes())
        .with_state(state)
        .layer(axum::middleware::from_fn_with_state(
            security_state,
            crate::middleware::security_headers::security_headers,
        ))
        .layer(axum::middleware::from_fn_with_state(
            cors_state,
            crate::middleware::cors::cors,
        ))
        .layer(axum::middleware::from_fn(
            crate::middleware::access_log::access_log,
        ))
        .layer(axum::middleware::from_fn(
            crate::middleware::request_logger::request_logger,
        ))
        .layer(axum::middleware::from_fn(
            crate::middleware::recovery::recovery,
        ))
}
