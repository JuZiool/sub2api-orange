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
    └── sub2api-server/           # 服务二进制
        └── src/
            ├── main.rs           # 入口
            ├── config.rs         # 配置加载
            ├── migrate.rs        # 迁移运行器（复刻 Go 语义）
            └── routes/
                ├── mod.rs
                └── common.rs     # /health 等
```

## 迁移运行器的重要说明 ⚠️

Go 版使用的是**自研迁移运行器**，不是 `sqlx::migrate!` 的默认约定：

- 跟踪表：`schema_migrations(filename PK, checksum, applied_at)`
- **不是** sqlx 默认的 `_sqlx_migrations`
- Advisory Lock ID：`694208311321144027`
- 非事务迁移后缀：`_notx.sql`

因此 Rust 侧**必须复刻这套语义**，否则会在现网库上建出并行表并重跑全部 300 个迁移。
详见 `src/migrate.rs`。

## 工具链

本机当前**未安装** Rust 工具链。安装：

```bash
# Windows (winget)
winget install Rustlang.Rustup

# 或 WSL / Linux
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh
```

安装后 `rust-toolchain.toml` 会自动拉取锁定的 1.83.0。

## 构建与运行

```bash
cd backend-rs
cargo build
cargo run -p sub2api-server
```

构建后访问 `http://localhost:8080/health`，应返回 `{"status":"ok"}`。

## 当前进度

**期 0：工程骨架**（进行中）

- [x] workspace 结构
- [x] 健康检查端点
- [x] 配置加载骨架
- [x] 迁移运行器骨架（表结构与锁 ID 已对齐）
- [ ] 工具链安装后首次编译验证
- [ ] CI 骨架

后续分期见 `文档/方案/2026-10-04-Orange-全面Rust化迁移方案-正式实施.md`。
