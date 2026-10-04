//! 路由注册。
//!
//! 期 0 实现通用路由（健康检查等）；期 1 挂载全局中间件与标准响应信封；
//! 期 2 接入 JWT 认证与首个业务端点（`GET /api/v1/user/profile`）。
//! 后续期次按 Go 版 `internal/server/routes/` 逐个模块补齐，
//! 目标是最终覆盖 658 条路由。

pub mod api_key;
pub mod common;
pub mod user;

use std::sync::Arc;

use axum::Router;
use sqlx::PgPool;

use crate::config::Config;
use crate::middleware::cors::CorsState;
use crate::middleware::security_headers::SecurityHeadersState;
use crate::repository::user_repo::UserRepository;

/// 应用共享状态。后续期次会继续挂载各类 service。
#[derive(Clone)]
pub struct AppState {
    pub pool: Option<PgPool>,
}

/// 构建路由表（含中间件）。
pub fn router(config: Config, pool: Option<PgPool>) -> Router {
    let state = AppState { pool: pool.clone() };

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
    // 当前传空列表，与「未配置额外来源」的行为一致。
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
        .merge(authenticated_routes(&config, &pool))
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

/// 需要认证的路由（当前为 `/api/v1/user/**`）。
///
/// 使用 `route_layer` 挂 JWT 认证，使其**只对已匹配的路由生效**：未匹配的路径
/// 仍返回 404 且不进入认证逻辑——与 Go 版把中间件挂在路由组上的效果一致。
///
/// 未配置数据库时**不注册**这些路由（而非放行），避免出现无鉴权的业务端点。
fn authenticated_routes(config: &Config, pool: &Option<PgPool>) -> Router<AppState> {
    let Some(pool) = pool else {
        tracing::warn!("未配置数据库，用户面路由未注册");
        return Router::new();
    };

    if config.jwt.secret.trim().is_empty() {
        tracing::warn!("jwt.secret 为空，认证端点将拒绝所有请求（与 Go 版校验行为一致）");
    } else {
        // 记录生效的 token 有效期，便于运维核对配置是否按预期生效。
        // 传 now=0 时，access_token_expiry 的返回值即为「有效期秒数」。
        let ttl_seconds = sub2api_auth::access_token_expiry(
            0,
            config.jwt.access_token_expire_minutes,
            config.jwt.expire_hour,
        );
        tracing::info!(
            access_token_expire_minutes = config.jwt.access_token_expire_minutes,
            expire_hour = config.jwt.expire_hour,
            ttl_seconds,
            "JWT access token 有效期"
        );
    }

    let users = Arc::new(UserRepository::new(pool.clone()));
    let auth_state = sub2api_auth::jwt_auth::JwtAuthState {
        secret: Arc::new(config.jwt.secret.clone()),
        users,
    };

    Router::new()
        .route(
            "/api/v1/user/profile",
            axum::routing::get(user::get_profile),
        )
        .route(
            "/api/v1/user/password",
            axum::routing::put(user::change_password),
        )
        .route(
            "/api/v1/keys",
            axum::routing::get(api_key::list_api_keys).post(api_key::create_api_key),
        )
        .route(
            // 注意：axum 0.7 的路径参数语法是 `:id`；`{id}` 是 0.8 才引入的写法。
            // 写错会导致路由静默不匹配（表现为 404），且不会报编译错误。
            "/api/v1/keys/:id",
            axum::routing::get(api_key::get_api_key).delete(api_key::delete_api_key),
        )
        .route_layer(axum::middleware::from_fn_with_state(
            auth_state,
            sub2api_auth::jwt_auth::jwt_auth,
        ))
}
