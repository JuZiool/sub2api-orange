# Sub2API Orange 部署指南

Sub2API Orange 是一个可自托管的 AI API 网关。本 README 只说明部署、升级、备份和迁移。

## 推荐方式：Docker Compose

Docker 部署会运行三个服务：

```text
sub2api -> PostgreSQL
         -> Redis
```

默认镜像：

```text
ghcr.io/juziool/sub2api-orange:latest
```

### 全新安装

要求：Docker Engine、Docker Compose v2、Linux amd64/arm64。

```bash
mkdir -p /opt/sub2api-orange
cd /opt/sub2api-orange
curl -fsSL https://raw.githubusercontent.com/JuZiool/sub2api-orange/main/deploy/to-install.sh \
  | bash
```

不带参数且在交互终端执行时会进入菜单：

```text
1) 全新安装
2) 迁移安装
3) 更新镜像
4) 创建备份
5) 健康检查
6) 回滚镜像
```

选择 **1：全新安装** 后，脚本会询问服务端口、管理员邮箱和管理员初始密码。选择其他操作时使用已有 `.env` 和数据，不会重新生成密钥。

自动化或明确指定模式时，可使用：

```bash
curl -fsSL https://raw.githubusercontent.com/JuZiool/sub2api-orange/main/deploy/to-install.sh \
  | bash -s -- --mode 1
```

脚本会：

- 下载 Compose 配置和 GHCR 镜像 overlay；
- 生成数据库、Redis、JWT、TOTP 密钥；
- 创建 `.env`、`data/`、`postgres_data/`、`redis_data/`；
- 启动 PostgreSQL、Redis 和 Sub2API；
- 等待容器健康状态和 `/health` 通过。

首次安装不会在终端打印完整密钥。请妥善保存部署目录中的 `.env`。

### 已有部署迁移

将以下内容从旧服务器复制到新服务器：

```text
.env
data/
postgres_data/
redis_data/
```

然后在部署目录执行：

```bash
bash to-install.sh --mode 2
```

迁移模式不会重新生成密钥，不会删除 Docker volume，也不会覆盖已有配置。跨 PostgreSQL 大版本迁移时，优先使用逻辑备份，不要直接复制正在运行中的数据库目录。

### 日常更新

```bash
bash to-install.sh update
```

更新流程默认会：

1. 创建基础备份；
2. 拉取 `ghcr.io/juziool/sub2api-orange:latest`；
3. 只重建 `sub2api` 应用容器；
4. 保持 PostgreSQL 和 Redis 继续运行；
5. 检查三个容器和 `/health`；
6. 失败时恢复上一次应用镜像。

固定版本或 digest：

```bash
bash to-install.sh update \
  --image ghcr.io/juziool/sub2api-orange:0.2.1-3
```

本地已有镜像时可跳过拉取：

```bash
bash to-install.sh update \
  --image ghcr.io/juziool/sub2api-orange:local \
  --no-pull
```

### 备份、健康检查和回滚

```bash
# 创建基础备份
bash to-install.sh backup

# 检查 Compose、容器和 /health
bash to-install.sh health

# 回滚到上一次成功更新前的应用镜像
bash to-install.sh rollback
```

备份目录：

```text
backups/<timestamp>/
├── env
├── data.tar.gz
├── postgres.sql.gz
├── redis-data.tar.gz
├── metadata.txt
└── compose/
```

数据库迁移是向前的。应用镜像回滚不会自动回滚数据库结构；如需恢复数据库，请使用 `postgres.sql.gz` 或其他可靠备份。

## 常用命令

```bash
# 查看状态
to-install.sh health

# 查看应用日志
docker compose --env-file .env \
  -f docker-compose.local.yml \
  -f docker-compose.ghcr.yml \
  logs -f sub2api

# 查看全部容器
docker compose --env-file .env \
  -f docker-compose.local.yml \
  -f docker-compose.ghcr.yml \
  ps

# 健康接口
curl http://127.0.0.1:8080/health
```

默认访问地址：

```text
http://服务器IP:8080
```

生产环境建议使用 Nginx 或 Caddy 提供 HTTPS、域名和管理端访问限制。PostgreSQL `5432` 与 Redis `6379` 默认只在 Compose 内部网络使用，不应暴露到公网。

## 传统二进制/systemd 部署

如果不使用 Docker，可以使用独立的二进制安装器：

```bash
curl -fsSL https://raw.githubusercontent.com/JuZiool/sub2api-orange/main/deploy/install.sh \
  | sudo bash
```

该方式使用：

```text
程序：/opt/sub2api/sub2api
配置：/etc/sub2api/config.yaml
服务：sub2api.service
```

二进制模式的升级、回滚和后台在线更新与 Docker 镜像更新相互独立。Docker 部署请使用 `to-install.sh`，不要在容器内执行二进制替换更新。

## 版本与镜像

Orange 版本基于官方上游版本增加修订号：

```text
上游 0.2.1
Orange 0.2.1-1、0.2.1-2、0.2.1-3、...
```

当上游升级到 `0.2.2` 后，Orange 再从 `0.2.2-1` 开始编号。

Release：

- https://github.com/JuZiool/sub2api-orange/releases

GHCR：

```text
ghcr.io/juziool/sub2api-orange
```

## 配置和安全

- `.env` 包含数据库密码和加密密钥，不要提交到 Git 或公开分享；
- `JWT_SECRET` 和 `TOTP_ENCRYPTION_KEY` 必须在迁移、更新和重启过程中保持不变；
- 全新安装不能用于已有数据目录；已有部署使用迁移模式；
- 更新前保留可恢复的 PostgreSQL 备份；
- 生产环境使用固定镜像 tag 或 digest，不要依赖不可追踪的 `latest`；
- 只开放 HTTPS 和必要的管理端口。

详细 Docker 配置见 [`deploy/README.md`](deploy/README.md)，镜像说明见 [`deploy/DOCKER.md`](deploy/DOCKER.md)。
