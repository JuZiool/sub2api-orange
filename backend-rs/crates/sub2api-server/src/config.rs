//! 配置加载。
//!
//! 目标：与 Go 版 `config.example.yaml` 的结构保持兼容（36 个顶层分组）。
//! 期 0 只解析骨架需要的字段，其余分组在后续期次逐步补齐。
//!
//! 注意：Go 版配置同时支持 YAML 文件与环境变量覆盖。
//! 这里保留了同样的文件名（config.yaml）以确保运维侧无需改动。

use std::path::PathBuf;

use anyhow::Context;
use serde::Deserialize;

/// 顶层配置。
///
/// 字段命名沿用 Go 版 YAML 的 snake_case，避免运维配置需要改写。
#[derive(Debug, Clone, Deserialize, Default)]
pub struct Config {
    #[serde(default)]
    pub server: ServerConfig,
    #[serde(default)]
    pub database: DatabaseConfig,
    #[serde(default)]
    pub log: LogConfig,
}

#[derive(Debug, Clone, Deserialize)]
pub struct ServerConfig {
    /// 监听主机。默认 0.0.0.0 与 Go 版一致。
    #[serde(default = "default_host")]
    pub host: String,
    /// 监听端口。
    ///
    /// 规则 9：固定 8080，不得擅自修改。
    #[serde(default = "default_port")]
    pub port: u16,
}

impl Default for ServerConfig {
    fn default() -> Self {
        Self {
            host: default_host(),
            port: default_port(),
        }
    }
}

impl ServerConfig {
    pub fn listen_addr(&self) -> String {
        format!("{}:{}", self.host, self.port)
    }
}

#[derive(Debug, Clone, Deserialize, Default)]
pub struct DatabaseConfig {
    /// PostgreSQL 连接串。为空则跳过后端存储（骨架验证模式）。
    #[serde(default)]
    pub url: Option<String>,
    #[serde(default)]
    pub max_open_conns: Option<u32>,
    /// 是否启动时自动执行迁移，对齐 Go 版行为。
    #[serde(default)]
    pub auto_migrate: Option<bool>,
}

impl DatabaseConfig {
    pub fn url(&self) -> Option<String> {
        self.url.clone()
    }
}

#[derive(Debug, Clone, Deserialize, Default)]
pub struct LogConfig {
    #[serde(default)]
    pub level: Option<String>,
}

fn default_host() -> String {
    "0.0.0.0".to_string()
}

/// 规则 9：端口固定 8080。
fn default_port() -> u16 {
    8080
}

impl Config {
    /// 按 Go 版约定加载配置。
    ///
    /// 查找顺序：
    /// 1. 环境变量 `SUB2API_CONFIG` 指定的路径
    /// 2. 当前目录 `config.yaml`
    /// 3. 均不存在则使用默认值（骨架验证模式）
    pub fn load() -> anyhow::Result<Self> {
        let path = std::env::var("SUB2API_CONFIG")
            .ok()
            .map(PathBuf::from)
            .or_else(|| {
                let p = PathBuf::from("config.yaml");
                p.exists().then_some(p)
            });

        let Some(path) = path else {
            tracing::warn!("未找到 config.yaml，使用默认配置（骨架验证模式）");
            return Ok(Self::default());
        };

        let raw = std::fs::read_to_string(&path)
            .with_context(|| format!("读取配置文件失败: {}", path.display()))?;
        let cfg: Config = serde_yaml::from_str(&raw)
            .with_context(|| format!("解析配置文件失败: {}", path.display()))?;

        tracing::info!(path = %path.display(), "已加载配置");
        Ok(cfg)
    }
}
