# syntax=docker/dockerfile:1.7
# =============================================================================
# Sub2API Orange — Rust 后端多阶段 Dockerfile
# =============================================================================
# 与仓库根目录的 Go 版 Dockerfile 并存，互不影响：
#   - 根 Dockerfile          → Go 后端（现有部署流程继续使用）
#   - backend-rs/Dockerfile  → Rust 后端（本文件，迁移期使用）
#
# 该镜像的最终二进制路径刻意与 Go 版保持一致的约定：
#   /app/sub2api  ← 由 deploy/docker-entrypoint.sh 默认 exec
# 因此现有的部署脚本无需修改即可复用。
# =============================================================================

ARG RUST_IMAGE=rust:1.99-slim-bookworm
ARG DEBIAN_IMAGE=debian:bookworm-slim
ARG POSTGRES_IMAGE=postgres:18-alpine

# -----------------------------------------------------------------------------
# Stage 1: Rust Builder
# -----------------------------------------------------------------------------
FROM ${RUST_IMAGE} AS rust-builder

# 构建期依赖：sqlx 使用 rustls，无需 openssl；ca-certificates 供 crates.io 拉取。
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        pkg-config \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# 先复制清单文件以复用依赖层缓存。
# Cargo.lock 可能尚未生成（首次构建会生成），因此不强制要求存在。
COPY backend-rs/Cargo.toml ./
COPY backend-rs/crates/sub2api-server/Cargo.toml ./crates/sub2api-server/
COPY backend-rs/rust-toolchain.toml ./

# 复制源码
COPY backend-rs/crates/ ./crates/

# 构建 release 产物（缓存挂载避免每次重编依赖）
RUN --mount=type=cache,id=sub2api-rs-cargo-registry,target=/usr/local/cargo/registry \
    --mount=type=cache,id=sub2api-rs-target,target=/app/target \
    cargo build --release --locked 2>/dev/null || cargo build --release \
    && cp /app/target/release/sub2api-server /app/sub2api

# -----------------------------------------------------------------------------
# Stage 2: PostgreSQL Client（与 docker-compose 版本对齐，供备份工具使用）
# -----------------------------------------------------------------------------
FROM ${POSTGRES_IMAGE} AS pg-client

# -----------------------------------------------------------------------------
# Stage 3: 最终运行镜像
# -----------------------------------------------------------------------------
FROM ${DEBIAN_IMAGE}

LABEL maintainer="JuZiool <github.com/JuZiool>"
LABEL description="Sub2API Orange (Rust) - AI API Gateway Platform"
LABEL org.opencontainers.image.source="https://github.com/JuZiool/sub2api-orange"

# 运行期依赖：
#   ca-certificates/tzdata → TLS 与本地时间
#   libpq5                 → 供 pg_dump/psql 使用
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        tzdata \
        libpq5 \
        wget \
    && rm -rf /var/lib/apt/lists/*

# 复制与 docker-compose 中数据库版本一致的客户端工具
COPY --from=pg-client /usr/local/bin/pg_dump /usr/local/bin/pg_dump
COPY --from=pg-client /usr/local/bin/psql /usr/local/bin/psql
COPY --from=pg-client /usr/local/lib/libpq.so.5* /usr/local/lib/

# 非 root 用户，与 Go 版保持相同的 uid/gid 约定
RUN groupadd -g 1000 sub2api \
    && useradd -u 1000 -g sub2api -s /bin/bash -m sub2api

WORKDIR /app

# 二进制约定路径与 Go 版一致
COPY --from=rust-builder --chown=sub2api:sub2api /app/sub2api /app/sub2api

# 迁移 SQL 复用 Go 版目录，保证两边读同一份 schema 定义。
# migrate.rs 通过 SUB2API_MIGRATIONS_DIR 定位，见下方 ENV。
COPY --chown=sub2api:sub2api backend/migrations/ /app/migrations/

RUN mkdir -p /app/data && chown sub2api:sub2api /app/data

# 迁移目录指向镜像内路径（migrate.rs 优先读取该环境变量）
ENV SUB2API_MIGRATIONS_DIR=/app/migrations

# 规则 9：端口固定 8080，不得擅自修改
EXPOSE 8080

# 健康检查：与 Go 版一致，探 /health
HEALTHCHECK --interval=30s --timeout=10s --start-period=10s --retries=3 \
    CMD wget -q -T 5 -O /dev/null http://localhost:${SERVER_PORT:-8080}/health || exit 1

USER sub2api

CMD ["/app/sub2api"]
