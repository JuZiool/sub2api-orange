//! Sub2API Orange — Rust 后端入口。
//!
//! 迁移原则：固定契约，替换实现。
//! - HTTP API 与 Go 版保持一致（658 条路由）
//! - PostgreSQL schema 不变（复用 `../backend/migrations/*.sql`）
//! - 前端零改动
//!
//! 本期（期 0）仅实现工程骨架与健康检查，用于验证构建与部署链路。

mod config;
mod migrate;
mod routes;

use std::net::SocketAddr;

use anyhow::Context;
use tracing_subscriber::{layer::SubscriberExt, util::SubscriberInitExt, EnvFilter};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    // 先加载配置，再用配置中的日志级别初始化日志（对齐 Go 版 log.level 语义）。
    let cfg = config::Config::load().context("加载配置失败")?;
    let default_level = cfg.log.level.clone().unwrap_or_else(|| "info".to_string());

    // 初始化日志。对齐 Go 版 zap 的输出风格（结构化、带级别）。
    // 环境变量 RUST_LOG 优先于配置文件，便于本地调试。
    tracing_subscriber::registry()
        .with(EnvFilter::try_from_default_env().unwrap_or_else(|_| EnvFilter::new(default_level)))
        .with(tracing_subscriber::fmt::layer())
        .init();

    match config::Config::loaded_from() {
        Some(path) => tracing::info!(path = %path, "已加载配置"),
        None => tracing::warn!("未找到 config.yaml，使用默认配置（骨架验证模式）"),
    }

    // 注意：规则 9 要求端口固定 8080，不得擅自修改。
    // 这里默认值即 8080，配置可覆盖但不应被随意改动。
    let addr: SocketAddr = cfg
        .server
        .listen_addr()
        .parse()
        .with_context(|| format!("无法解析监听地址: {}", cfg.server.listen_addr()))?;

    // 数据库连接（可选：期 0 允许无库启动，便于先验证 HTTP 链路）。
    let pool = match cfg.database.url() {
        Some(url) if !url.is_empty() => {
            let pool = sqlx::postgres::PgPoolOptions::new()
                .max_connections(cfg.database.max_open_conns.unwrap_or(10))
                .connect(&url)
                .await
                .context("连接 PostgreSQL 失败")?;
            tracing::info!("已连接 PostgreSQL");

            // 复刻 Go 版迁移语义执行迁移。
            if cfg.database.auto_migrate.unwrap_or(true) {
                migrate::run(&pool).await.context("执行迁移失败")?;
            }
            Some(pool)
        }
        _ => {
            tracing::warn!("未配置 database.url，跳过后端存储（仅用于骨架验证）");
            None
        }
    };

    let app = routes::router(cfg.clone(), pool);

    let listener = tokio::net::TcpListener::bind(addr)
        .await
        .with_context(|| format!("监听 {addr} 失败"))?;
    tracing::info!("sub2api-server (Rust) 已启动，监听 {addr}");

    axum::serve(listener, app)
        .with_graceful_shutdown(shutdown_signal())
        .await
        .context("HTTP 服务异常退出")?;

    Ok(())
}

/// 优雅停机：等待 Ctrl-C 或 SIGTERM。
async fn shutdown_signal() {
    let ctrl_c = async {
        tokio::signal::ctrl_c()
            .await
            .expect("无法注册 Ctrl-C 处理器");
    };

    #[cfg(unix)]
    let terminate = async {
        tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
            .expect("无法注册 SIGTERM 处理器")
            .recv()
            .await;
    };

    #[cfg(not(unix))]
    let terminate = std::future::pending::<()>();

    tokio::select! {
        _ = ctrl_c => {},
        _ = terminate => {},
    }

    tracing::info!("收到停机信号，正在优雅退出");
}
