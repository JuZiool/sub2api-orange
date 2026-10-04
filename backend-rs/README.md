# Sub2API Orange — Rust 后端

这是 `sub2api-orange` 后端的 **Rust 重写版本**，工作在 `sub2api-orange-rust` 分支上。

## 迁移原则

**固定契约，替换实现。**

| 资产 | 处理 |
|---|---|
| PostgreSQL schema | **不变**（复用 `../backend/migrations/*.sql`） |
| HTTP API（658 条路由） | **不变** |
| 前端 `frontend/` | **零改动** |
| `config.example.yaml` | **不变** |
| Go 测试（1486 个） | 保留为**行为规格（oracle）** |

前端和数据库是兼容性契约。Rust 后端只要提供完全相同的 HTTP API 和 PG schema，
前端一行都不用改。

## 目录结构

```
backend-rs/
├── Cargo.toml                    # workspace
├── rust-toolchain.toml           # 工具链锁定
└── crates/
    ├── sub2api-auth/             # 认证：JWT、TokenVersion 指纹、认证中间件
    │   └── src/
    │       ├── lib.rs
    │       ├── jwt.rs            # HS256 签发与校验
    │       ├── jwt_auth.rs       # 认证中间件
    │       ├── token_version.rs  # 派生指纹（改密撤销 token）
    │       └── errors.rs         # 中间件错误信封（code 为字符串）
    └── sub2api-server/           # 服务二进制
        └── src/
            ├── main.rs           # 入口
            ├── config.rs         # 配置加载
            ├── migrate.rs        # 迁移运行器（复刻 Go 语义）
            ├── response.rs       # 业务响应信封（code 为整数）
            ├── middleware/       # 全局中间件
            └── routes/
                ├── mod.rs
                └── common.rs     # /health 等
```

## ⚠️ 两个信封不要混淆

| | `sub2api_server::response` | `sub2api_auth::errors` |
|---|---|---|
| `code` 类型 | **整数**（HTTP 状态码 / 0） | **字符串**（如 `"UNAUTHORIZED"`） |
| 使用方 | 业务 handler 与 `sub2api-server` | 认证中间件 |

来源分别是 Go 版的 `internal/pkg/response` 与 `internal/server/middleware`，
两者本就不同，混用会让前端错误分支判断失效。

## 迁移运行器的重要说明 ⚠️

Go 版使用的是**自研迁移运行器**，不是 `sqlx::migrate!` 的默认约定：

- 跟踪表：`schema_migrations(filename PK, checksum, applied_at)`
- **不是** sqlx 默认的 `_sqlx_migrations`
- Advisory Lock ID：`694208311321144027`
- 非事务迁移后缀：`_notx.sql`

因此 Rust 侧**必须复刻这套语义**，否则会在现网库上建出并行表并重跑全部 300 个迁移。
详见 `src/migrate.rs`。

## 工具链

本机已安装（`rustup` 1.29.1）：

```powershell
winget install Rustlang.Rustup
```

`rust-toolchain.toml` 锁定 **1.99**。注意不要往下调：当前 crates.io 生态
（sqlx 0.8、uuid 1.x、icu 2.x 等）普遍要求 Rust >= 1.85，部分要求 >= 1.88/1.89；
锁定 1.83 会因 `edition2024` 特性缺失而无法解析依赖清单。

构建产物在 `target/`（已 gitignore）。若 `cargo build` 报
`failed to remove ... sub2api-server.exe: 拒绝访问`，说明上一次运行的进程未退出：

```powershell
Get-Process sub2api-server -ErrorAction SilentlyContinue | Stop-Process -Force
```

## 构建与运行

```bash
cd backend-rs
cargo build
cargo run -p sub2api-server
```

构建后访问 `http://localhost:8080/health`，应返回 `{"status":"ok"}`。

带数据库运行（迁移会自动执行）：

```bash
$env:DATABASE_URL="postgres://user:pass@127.0.0.1:5432/sub2api"
$env:SUB2API_MIGRATIONS_DIR="../backend/migrations"
cargo run -p sub2api-server
```

> 端口固定 **8080**（规则 9）。若被占用，先处理占用服务，不得擅自换端口。

## 质量门禁

与 CI（`.github/workflows/rust-backend-ci.yml`）保持一致，提交前应全部通过：

```bash
cargo fmt --all -- --check
cargo clippy --all-targets -- -D warnings
cargo test --all
```

## 当前进度

**期 0：工程骨架** —— 已完成并通过验证

- [x] workspace 结构
- [x] 健康检查端点（响应与 Go 版逐字节一致）
- [x] 配置加载（字段对齐 `config.yaml`，支持 `DATABASE_URL` 覆盖）
- [x] 迁移运行器（复刻 Go 版语义，296 个迁移实测通过 + 幂等性验证）
- [x] CI 骨架（`rust-backend-ci.yml`，与 Go CI 并存）
- [x] 多阶段 Dockerfile（`rust:1.99-slim-bookworm` → `debian:bookworm-slim`）

**期 1：基础设施层** —— 契约核心与全局中间件已完成

- [x] 标准响应信封 `response.rs`（字段与省略行为对齐 Go 版）
- [x] CORS 中间件（含 `x-stainless-*` 放行）
- [x] 安全头 + CSP 中间件（验证码/支付必需域名补全、nonce 生成）
- [x] 请求日志 `X-Request-ID` + 访问日志（跳过探针路径）
- [x] panic 恢复中间件
- [x] 单元测试 62 个
- [x] wslc 镜像构建 + 端到端部署验证（容器 healthy）

**待续**：`Server-Timing`、`SessionBinding`、审计日志、Redis 连接、
前端产物嵌入（`rust-embed`）、其余配置分组。

**期 2：认证与用户面** —— 认证核心 + 首个业务端点已完成

- [x] JWT HS256 签发与校验（允许 HS256/384/512，防算法混淆）
- [x] **Go↔Rust 交叉验证**：Go 签发的 token 可被 Rust 校验通过
- [x] TokenVersion 派生指纹（`email + password_hash`，改密即撤销旧 token）
- [x] 认证中间件（7 步判定顺序与错误码逐字对齐 Go）
- [x] 认证错误信封（`code` 为字符串，区别于业务信封）
- [x] 身份摘要构建（邮箱/linuxdo/oidc/wechat/dingtalk 绑定状态与可解绑判定）
- [x] 用户仓储（真实 DB 查询，`numeric` 转换、`user_allowed_groups` 关联表）
- [x] **`GET /api/v1/user/profile` 端到端打通**（第一个可用业务端点）
- [x] 重构为独立 lib crate `sub2api-auth`
- [x] 单元测试 121 个（49 auth + 72 server）
- [x] wslc 镜像构建 + 端到端部署验证（容器 healthy，9 条认证路径实测）

**待续**：改密、通知邮箱、TOTP、Passkey、API Key 管理、OAuth 流程。

完整分期计划见 `文档/方案/2026-10-04-Orange-全面Rust化迁移方案-正式实施.md`。
