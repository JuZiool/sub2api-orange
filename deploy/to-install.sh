#!/usr/bin/env bash

set -Eeuo pipefail
IFS=$'\n\t'

readonly DEFAULT_IMAGE="ghcr.io/juziool/sub2api-orange:latest"
readonly DEFAULT_HEALTH_TIMEOUT=180
readonly DEFAULT_RAW_BASE_URL="https://raw.githubusercontent.com/JuZiool/sub2api-orange/main/deploy"
readonly ROLLBACK_IMAGE_PREFIX="sub2api-orange:rollback-"

DEPLOY_DIR="${SUB2API_DEPLOY_DIR:-$(pwd -P)}"
RAW_BASE_URL="${SUB2API_RAW_BASE_URL:-$DEFAULT_RAW_BASE_URL}"
HEALTH_TIMEOUT="${SUB2API_HEALTH_TIMEOUT:-$DEFAULT_HEALTH_TIMEOUT}"
MODE=""
IMAGE_OVERRIDE=""
NO_BACKUP=false
NO_PULL=false
ASSUME_YES=false
INSTALL_DOCKER=true
COMPOSE_MODE=""
LOCK_FILE=""
LOCK_DIR=""
LOCK_MODE=""
LOCK_ACQUIRED=false
STATE_FILE=""
BACKUP_ROOT=""
TEMP_FILES=()

log() { printf '[Sub2API] %s\n' "$*"; }
warn() { printf '[Sub2API] 警告：%s\n' "$*" >&2; }
die() { printf '[Sub2API] 错误：%s\n' "$*" >&2; exit 1; }
cleanup() {
  if [[ "$LOCK_MODE" == mkdir && "$LOCK_ACQUIRED" == true ]]; then
    rm -rf -- "$LOCK_DIR" 2>/dev/null || true
  fi
  local file
  for file in "${TEMP_FILES[@]}"; do
    rm -f -- "$file" 2>/dev/null || true
  done
}
trap cleanup EXIT

usage() {
  cat <<'EOF'
Orange Docker 部署入口

用法：
  to-install.sh --mode 1              全新安装
  to-install.sh --mode 2              迁移后安装
  to-install.sh --mode 3              更新应用镜像
  to-install.sh update                更新应用镜像
  to-install.sh backup                创建基础备份
  to-install.sh health                检查 Compose、容器和 /health
  to-install.sh rollback              回滚到上一次成功更新前的镜像

选项：
  --dir <路径>                        部署目录，默认当前目录
  --image <tag|digest>                覆盖 SUB2API_IMAGE
  --health-timeout <秒>              健康检查超时时间，默认 180
  --no-backup                         更新前跳过数据库逻辑备份
  --no-pull                           使用本地已有镜像，不从注册表拉取
  --no-install-docker                 缺少 Docker 时不尝试安装
  --yes                               跳过可确认的交互提示
  -h, --help                          显示帮助

环境变量：
  SUB2API_DEPLOY_DIR                  默认部署目录
  SUB2API_IMAGE                       默认 ghcr.io/juziool/sub2api-orange:latest
  SUB2API_RAW_BASE_URL                部署文件下载地址
  SUB2API_HEALTH_TIMEOUT              健康检查超时时间
  SUB2API_BACKUP_DIR                  备份根目录
EOF
}

parse_args() {
  while (($# > 0)); do
    case "$1" in
      --dir)
        (($# >= 2)) || die "--dir 需要提供路径。"
        DEPLOY_DIR="$2"
        shift 2
        ;;
      --mode)
        (($# >= 2)) || die "--mode 需要提供 1、2 或 3。"
        MODE="$2"
        shift 2
        ;;
      --image)
        (($# >= 2)) || die "--image 需要提供镜像 tag 或 digest。"
        IMAGE_OVERRIDE="$2"
        shift 2
        ;;
      --health-timeout)
        (($# >= 2)) || die "--health-timeout 需要提供秒数。"
        HEALTH_TIMEOUT="$2"
        shift 2
        ;;
      --no-backup)
        NO_BACKUP=true
        shift
        ;;
      --no-pull)
        NO_PULL=true
        shift
        ;;
      --no-install-docker)
        INSTALL_DOCKER=false
        shift
        ;;
      --yes)
        ASSUME_YES=true
        shift
        ;;
      backup|health|rollback|update)
        [[ -z "$MODE" ]] || die "不能同时使用位置命令和 --mode。"
        MODE="$1"
        shift
        ;;
      1|2|3)
        [[ -z "$MODE" ]] || die "重复指定安装模式。"
        MODE="$1"
        shift
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        die "未知选项：$1"
        ;;
    esac
  done

  MODE="${MODE:-1}"
  [[ "$MODE" =~ ^(1|2|3|backup|health|rollback|update)$ ]] || die "模式必须是 1、2、3、backup、health、rollback 或 update。"
  [[ "$HEALTH_TIMEOUT" =~ ^[1-9][0-9]*$ ]] || die "健康检查超时时间必须是正整数。"
}

resolve_deploy_dir() {
  if [[ "$DEPLOY_DIR" != /* ]]; then
    DEPLOY_DIR="$(pwd -P)/$DEPLOY_DIR"
  fi
  mkdir -p -- "$DEPLOY_DIR"
  DEPLOY_DIR="$(cd -- "$DEPLOY_DIR" && pwd -P)"
  if [[ "$MODE" != 1 && -f "$DEPLOY_DIR/deploy/.env" && ! -f "$DEPLOY_DIR/.env" ]]; then
    DEPLOY_DIR="$DEPLOY_DIR/deploy"
  fi
  mkdir -p -- "$DEPLOY_DIR"
  DEPLOY_DIR="$(cd -- "$DEPLOY_DIR" && pwd -P)"
  STATE_FILE="$DEPLOY_DIR/.deploy-state.json"
  BACKUP_ROOT="${SUB2API_BACKUP_DIR:-$DEPLOY_DIR/backups}"
  LOCK_FILE="$DEPLOY_DIR/.sub2api-docker.lock"
}

confirm() {
  local prompt="$1" answer
  [[ "$ASSUME_YES" == true ]] && return 0
  [[ -t 0 || -r /dev/tty ]] || return 1
  read -r -p "$prompt [y/N]: " answer </dev/tty
  [[ "${answer,,}" == y || "${answer,,}" == yes ]]
}

package_manager() {
  if command -v apt-get >/dev/null 2>&1; then printf 'apt-get'
  elif command -v apk >/dev/null 2>&1; then printf 'apk'
  elif command -v dnf >/dev/null 2>&1; then printf 'dnf'
  elif command -v yum >/dev/null 2>&1; then printf 'yum'
  else printf ''
  fi
}

install_docker() {
  local manager
  manager="$(package_manager)"
  case "$manager" in
    apt-get)
      DEBIAN_FRONTEND=noninteractive apt-get update >/dev/null 2>&1 || true
      DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io docker-compose-plugin >/dev/null 2>&1 || true
      ;;
    apk)
      apk add --no-cache docker docker-cli-compose >/dev/null 2>&1 || true
      ;;
    dnf|yum)
      "$manager" install -y docker docker-compose-plugin >/dev/null 2>&1 || true
      ;;
    *)
      return 1
      ;;
  esac
  command -v docker >/dev/null 2>&1
}

detect_compose() {
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    COMPOSE_MODE=plugin
  elif command -v docker-compose >/dev/null 2>&1 && docker-compose version >/dev/null 2>&1; then
    COMPOSE_MODE=standalone
  else
    return 1
  fi
}

start_docker() {
  docker info >/dev/null 2>&1 && return 0
  if command -v systemctl >/dev/null 2>&1; then
    systemctl enable --now docker >/dev/null 2>&1 || true
  elif command -v service >/dev/null 2>&1; then
    service docker start >/dev/null 2>&1 || true
  fi
  docker info >/dev/null 2>&1
}

ensure_docker() {
  command -v curl >/dev/null 2>&1 || die "缺少 curl，请先安装 curl。"
  command -v openssl >/dev/null 2>&1 || die "缺少 openssl，请先安装 openssl。"

  if ! command -v docker >/dev/null 2>&1; then
    "$INSTALL_DOCKER" || die "未检测到 Docker，请先安装 Docker Engine。"
    [[ "$EUID" -eq 0 ]] || die "自动安装 Docker 需要 root 权限，请先安装 Docker 或使用 root。"
    install_docker || die "无法自动安装 Docker，请手动安装后重试。"
  fi
  detect_compose || {
    "$INSTALL_DOCKER" || die "未检测到 Docker Compose，请先安装 Compose v2。"
    [[ "$EUID" -eq 0 ]] || die "自动安装 Docker Compose 需要 root 权限。"
    install_docker
    detect_compose || die "未检测到 Docker Compose（支持 docker compose 或 docker-compose）。"
  }
  start_docker || die "Docker 服务未运行，或当前用户无权访问 Docker。"
}

compose() {
  (
    cd -- "$DEPLOY_DIR"
    if [[ "$COMPOSE_MODE" == plugin ]]; then
      docker compose --env-file .env -f docker-compose.local.yml -f docker-compose.ghcr.yml "$@"
    else
      docker-compose --env-file .env -f docker-compose.local.yml -f docker-compose.ghcr.yml "$@"
    fi
  )
}

source_file() {
  local name="$1" local_source="${SOURCE_DIR:-}/$1"
  if [[ -f "$local_source" ]]; then
    printf '%s' "$local_source"
  else
    printf ''
  fi
}

prepare_file() {
  local name="$1" target="$DEPLOY_DIR/$1" temp local_source
  [[ -f "$target" ]] && return 0
  local_source="$(source_file "$name")"
  if [[ -n "$local_source" ]]; then
    cp -- "$local_source" "$target"
  else
    temp="$(mktemp "$DEPLOY_DIR/.${name}.XXXXXX")"
    TEMP_FILES+=("$temp")
    curl --proto '=https' --tlsv1.2 -fsSL "$RAW_BASE_URL/$name" -o "$temp" || die "无法下载 $name。"
    mv -f -- "$temp" "$target"
  fi
  chmod 644 "$target"
}

prepare_runtime_files() {
  mkdir -p -- "$DEPLOY_DIR"
  prepare_file to-install.sh
  prepare_file docker-compose.local.yml
  prepare_file docker-compose.ghcr.yml
  prepare_file .env.example
  chmod 755 "$DEPLOY_DIR/to-install.sh"
  mkdir -p -- "$DEPLOY_DIR/data" "$DEPLOY_DIR/postgres_data" "$DEPLOY_DIR/redis_data"
}

has_persistent_data() {
  [[ -f "$DEPLOY_DIR/.env" ]] ||
    [[ -f "$DEPLOY_DIR/data/config.yaml" ]] ||
    [[ -f "$DEPLOY_DIR/data/.installed" ]] ||
    [[ -f "$DEPLOY_DIR/postgres_data/PG_VERSION" ]] ||
    [[ -n "$(find "$DEPLOY_DIR/data" "$DEPLOY_DIR/postgres_data" "$DEPLOY_DIR/redis_data" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null)" ]]
}

generate_hex() { openssl rand -hex "$1"; }

dotenv_quote() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\'/\'\\\'\'}"
  printf "'%s'" "$value"
}

prompt_fresh_values() {
  SERVER_PORT="8080"
  BIND_HOST="0.0.0.0"
  ADMIN_EMAIL="admin@sub2api.local"
  ADMIN_PASSWORD=""
  if [[ -t 0 || -r /dev/tty ]]; then
    local value confirm_value
    while true; do
      read -r -p "服务端口 [8080]: " value </dev/tty
      value="${value:-8080}"
      [[ "$value" =~ ^[0-9]+$ ]] && ((value >= 1 && value <= 65535)) && break
      warn "请输入 1 到 65535 之间的端口。"
    done
    SERVER_PORT="$value"
    read -r -p "管理员邮箱 [admin@sub2api.local]: " value </dev/tty
    ADMIN_EMAIL="${value:-$ADMIN_EMAIL}"
    read -r -s -p "管理员初始密码（留空则首次启动时生成）: " value </dev/tty
    printf '\n'
    if [[ -n "$value" ]]; then
      ((${#value} >= 8)) || die "管理员密码至少需要 8 个字符。"
      read -r -s -p "再次输入管理员初始密码: " confirm_value </dev/tty
      printf '\n'
      [[ "$value" == "$confirm_value" ]] || die "两次输入的密码不一致。"
      ADMIN_PASSWORD="$value"
    fi
  fi
}

write_fresh_env() {
  local temp line
  temp="$(mktemp "$DEPLOY_DIR/.env.XXXXXX")"
  TEMP_FILES+=("$temp")
  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    case "$line" in
      BIND_HOST=*) printf 'BIND_HOST=%s\n' "$BIND_HOST" ;;
      SERVER_PORT=*) printf 'SERVER_PORT=%s\n' "$SERVER_PORT" ;;
      POSTGRES_PASSWORD=*) printf 'POSTGRES_PASSWORD=%s\n' "$(generate_hex 24)" ;;
      REDIS_PASSWORD=*) printf 'REDIS_PASSWORD=%s\n' "$(generate_hex 24)" ;;
      ADMIN_EMAIL=*) printf 'ADMIN_EMAIL=%s\n' "$(dotenv_quote "$ADMIN_EMAIL")" ;;
      ADMIN_PASSWORD=*) printf 'ADMIN_PASSWORD=%s\n' "$(dotenv_quote "$ADMIN_PASSWORD")" ;;
      JWT_SECRET=*) printf 'JWT_SECRET=%s\n' "$(generate_hex 32)" ;;
      TOTP_ENCRYPTION_KEY=*) printf 'TOTP_ENCRYPTION_KEY=%s\n' "$(generate_hex 32)" ;;
      SUB2API_IMAGE=*) printf 'SUB2API_IMAGE=%s\n' "${IMAGE_OVERRIDE:-$DEFAULT_IMAGE}" ;;
      *) printf '%s\n' "$line" ;;
    esac
  done < "$DEPLOY_DIR/.env.example" > "$temp"
  if ! grep -q '^SUB2API_IMAGE=' "$temp"; then
    printf 'SUB2API_IMAGE=%s\n' "${IMAGE_OVERRIDE:-$DEFAULT_IMAGE}" >> "$temp"
  fi
  chmod 600 "$temp"
  mv -f -- "$temp" "$DEPLOY_DIR/.env"
}

read_env_value() {
  local key="$1" fallback="$2" value
  value="$(sed -n "s/^${key}=//p" "$DEPLOY_DIR/.env" | tail -n 1 | tr -d '\r')"
  value="${value#\"}"; value="${value%\"}"
  value="${value#\'}"; value="${value%\'}"
  printf '%s' "${value:-$fallback}"
}

set_image() {
  local configured_image
  configured_image="$(read_env_value SUB2API_IMAGE "")"
  if [[ -z "$IMAGE_OVERRIDE" && "$configured_image" == ghcr.io/juziool/sub2api:* ]]; then
    die "现有 .env 仍使用旧镜像 $configured_image；请使用 --image ghcr.io/juziool/sub2api-orange:<tag> 完成镜像迁移。"
  fi
  export SUB2API_IMAGE="${IMAGE_OVERRIDE:-${configured_image:-$DEFAULT_IMAGE}}"
  [[ -n "$SUB2API_IMAGE" ]] || die "SUB2API_IMAGE 不能为空。"
}

health_url() {
  local host port
  host="$(read_env_value BIND_HOST 0.0.0.0)"
  port="$(read_env_value SERVER_PORT 8080)"
  [[ "$host" == 0.0.0.0 || -z "$host" ]] && host=127.0.0.1
  printf 'http://%s:%s/health' "$host" "$port"
}

wait_for_health() {
  local deadline=$((SECONDS + HEALTH_TIMEOUT)) status id url
  url="$(health_url)"
  log "等待服务健康检查：$url"
  while ((SECONDS < deadline)); do
    status=healthy
    for service in postgres redis sub2api; do
      id="$(compose ps -q "$service" 2>/dev/null || true)"
      if [[ -z "$id" ]]; then status=starting; break; fi
      case "$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$id" 2>/dev/null || true)" in
        healthy) ;;
        unhealthy|exited|dead) status=failed; break ;;
        *) status=starting; break ;;
      esac
    done
    if [[ "$status" == healthy ]] && curl -fsS --max-time 5 "$url" | grep -q '"status"[[:space:]]*:[[:space:]]*"ok"'; then
      return 0
    fi
    [[ "$status" == failed ]] && break
    sleep 3
  done
  compose ps >&2 || true
  compose logs --tail=200 sub2api postgres redis >&2 || true
  return 1
}

health_command() {
  compose config --quiet
  wait_for_health || die "服务未在 ${HEALTH_TIMEOUT} 秒内通过健康检查。"
  log "健康检查通过。"
}

json_escape() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  value="${value//$'\n'/\\n}"
  printf '%s' "$value"
}

write_state() {
  local status="$1" image="$2" image_id="$3" rollback_tag="$4" previous_image="$5" backup="$6" error="${7:-}" temp
  temp="$(mktemp "$STATE_FILE.XXXXXX")"
  TEMP_FILES+=("$temp")
  {
    printf '{\n'
    printf '  "schema": 1,\n'
    printf '  "status": "%s",\n' "$(json_escape "$status")"
    printf '  "image": "%s",\n' "$(json_escape "$image")"
    printf '  "imageId": "%s",\n' "$(json_escape "$image_id")"
    printf '  "rollbackTag": "%s",\n' "$(json_escape "$rollback_tag")"
    printf '  "previousImage": "%s",\n' "$(json_escape "$previous_image")"
    printf '  "backup": "%s",\n' "$(json_escape "$backup")"
    printf '  "recordedAt": "%s",\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    printf '  "error": "%s"\n' "$(json_escape "$error")"
    printf '}\n'
  } > "$temp"
  chmod 600 "$temp"
  mv -f -- "$temp" "$STATE_FILE"
}

state_value() {
  local key="$1"
  [[ -f "$STATE_FILE" ]] || return 0
  sed -nE 's/.*"'"$key"'"[[:space:]]*:[[:space:]]*"([^"]*)".*/\1/p' "$STATE_FILE" | head -n 1
}

backup_command() {
  local stamp tmp image image_id compose_image
  stamp="$(date +%Y%m%d-%H%M%S)"
  tmp="$BACKUP_ROOT/.${stamp}.tmp"
  mkdir -p -- "$BACKUP_ROOT"
  chmod 700 "$BACKUP_ROOT"
  rm -rf -- "$tmp"
  mkdir -p -- "$tmp/compose" "$tmp/data"
  chmod 700 "$tmp" "$tmp/compose" "$tmp/data"

  compose config --quiet
  wait_for_health || die "备份前健康检查失败。"
  image="$(compose images --format '{{.Repository}}:{{.Tag}}' sub2api 2>/dev/null | head -n 1 || true)"
  [[ -n "$image" ]] || image="$(read_env_value SUB2API_IMAGE "$DEFAULT_IMAGE")"
  image_id="$(compose images -q sub2api 2>/dev/null | head -n 1 || true)"
  compose_image="$image"

  cp -- "$DEPLOY_DIR/.env" "$tmp/env"
  cp -- "$DEPLOY_DIR/docker-compose.local.yml" "$tmp/compose/"
  cp -- "$DEPLOY_DIR/docker-compose.ghcr.yml" "$tmp/compose/"
  tar -czf "$tmp/data.tar.gz" -C "$DEPLOY_DIR" data
  if ! compose exec -T postgres sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB"' | gzip -9 > "$tmp/postgres.sql.gz"; then
    rm -rf -- "$tmp"
    die "PostgreSQL 逻辑备份失败。"
  fi
  compose exec -T redis sh -c 'redis-cli SAVE' >/dev/null 2>&1 || warn "Redis SAVE 失败，将继续保存数据目录。"
  tar -czf "$tmp/redis-data.tar.gz" -C "$DEPLOY_DIR" redis_data
  {
    printf 'created_at=%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    printf 'image=%s\n' "$image"
    printf 'image_id=%s\n' "$image_id"
    printf 'compose_image=%s\n' "$compose_image"
    printf 'database=%s/%s\n' "$(read_env_value POSTGRES_USER sub2api)" "$(read_env_value POSTGRES_DB sub2api)"
  } > "$tmp/metadata.txt"
  chmod 600 "$tmp/env" "$tmp/postgres.sql.gz" "$tmp/redis-data.tar.gz" "$tmp/metadata.txt"
  mv -f -- "$tmp" "$BACKUP_ROOT/$stamp"
  log "备份完成：$BACKUP_ROOT/$stamp"
}

pull_image() {
  if [[ "$NO_PULL" == true ]]; then
    docker image inspect "$SUB2API_IMAGE" >/dev/null 2>&1 || die "本地不存在镜像：$SUB2API_IMAGE；请先构建镜像或移除 --no-pull。"
    log "使用本地镜像：$SUB2API_IMAGE"
  else
    compose pull sub2api || return 1
  fi
}

fresh_install() {
  [[ ! -e "$DEPLOY_DIR/.env" ]] || die "当前目录已有 .env，请使用迁移或更新模式。"
  if has_persistent_data; then
    die "当前目录已有配置或持久化数据，全新安装已拒绝，避免覆盖现有部署。"
  fi
  prepare_runtime_files
  prompt_fresh_values
  write_fresh_env
  set_image
  compose config --quiet
  pull_image || die "GHCR 镜像拉取失败，请检查网络或设置 --image。"
  compose up -d --remove-orphans
  wait_for_health || die "全新安装未通过健康检查。"
  log "Docker 全新安装完成。"
}

migration_install() {
  [[ -f "$DEPLOY_DIR/.env" ]] || die "迁移安装要求部署目录已有 .env。"
  if [[ ! -f "$DEPLOY_DIR/postgres_data/PG_VERSION" && ! -f "$DEPLOY_DIR/data/config.yaml" && ! -f "$DEPLOY_DIR/data/.installed" && -z "$(find "$DEPLOY_DIR/redis_data" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null)" ]]; then
    die "未检测到本地持久化数据。请先复制 data、postgres_data、redis_data 后再执行迁移安装；全新目录请使用 mode 1。"
  fi
  prepare_runtime_files
  chmod 600 "$DEPLOY_DIR/.env"
  set_image
  compose config --quiet
  pull_image || die "GHCR 镜像拉取失败。"
  compose up -d --remove-orphans
  wait_for_health || die "迁移安装未通过健康检查。"
  log "Docker 迁移安装完成，原有 .env 和数据未重新生成。"
}

update_command() {
  [[ -f "$DEPLOY_DIR/.env" ]] || die "更新要求部署目录已有 .env。"
  [[ -f "$DEPLOY_DIR/docker-compose.local.yml" && -f "$DEPLOY_DIR/docker-compose.ghcr.yml" ]] || die "缺少 Compose 文件，请先执行 mode 1 或 mode 2。"
  set_image
  compose config --quiet

  local old_id old_image rollback_tag backup_dir new_id
  old_id="$(compose images -q sub2api 2>/dev/null | head -n 1 || true)"
  old_image="$(compose images --format '{{.Repository}}:{{.Tag}}' sub2api 2>/dev/null | head -n 1 || true)"
  rollback_tag=""
  if [[ -n "$old_id" ]]; then
    rollback_tag="${ROLLBACK_IMAGE_PREFIX}$(date +%Y%m%d%H%M%S)"
    docker tag "$old_id" "$rollback_tag" || rollback_tag=""
  fi

  backup_dir=""
  if ! "$NO_BACKUP"; then
    backup_command
    backup_dir="$(find "$BACKUP_ROOT" -mindepth 1 -maxdepth 1 -type d -name '20*' -printf '%T@ %p\n' 2>/dev/null | sort -nr | head -n 1 | cut -d' ' -f2-)"
  fi

  log "准备应用镜像：$SUB2API_IMAGE"
  if ! pull_image; then
    write_state failed "$SUB2API_IMAGE" "" "$rollback_tag" "$old_image" "$backup_dir" "镜像拉取失败"
    die "镜像拉取失败，未重建现有应用容器。"
  fi
  if compose up -d --force-recreate --remove-orphans sub2api && wait_for_health; then
    new_id="$(docker image inspect --format '{{.Id}}' "$SUB2API_IMAGE" 2>/dev/null || compose images -q sub2api 2>/dev/null | head -n 1 || true)"
    write_state success "$SUB2API_IMAGE" "$new_id" "$rollback_tag" "$old_image" "$backup_dir"
    log "应用更新完成。"
    return 0
  fi

  warn "应用更新后健康检查失败，尝试恢复旧镜像。"
  if [[ -n "$rollback_tag" ]]; then
    export SUB2API_IMAGE="$rollback_tag"
    if compose up -d --no-build --force-recreate sub2api && wait_for_health; then
      write_state failed-recovered "$old_image" "$old_id" "" "$old_image" "$backup_dir" "新镜像未通过健康检查，已恢复旧镜像"
      die "更新失败，已恢复旧镜像；数据库迁移不会自动回滚。"
    fi
  fi
  write_state failed "$SUB2API_IMAGE" "" "$rollback_tag" "$old_image" "$backup_dir" "更新失败且旧镜像恢复失败"
  die "更新失败，旧镜像无法自动恢复；请检查日志和备份。"
}

rollback_command() {
  [[ -f "$DEPLOY_DIR/.env" ]] || die "回滚要求部署目录已有 .env。"
  local rollback_tag
  rollback_tag="$(state_value rollbackTag)"
  [[ "$rollback_tag" == ${ROLLBACK_IMAGE_PREFIX}* ]] || die "没有可用的 Orange 回滚镜像。"
  docker image inspect "$rollback_tag" >/dev/null 2>&1 || die "本地不存在回滚镜像：$rollback_tag"
  export SUB2API_IMAGE="$rollback_tag"
  compose config --quiet
  compose up -d --no-build --force-recreate sub2api || die "回滚容器启动失败。"
  wait_for_health || die "回滚后健康检查失败。"
  write_state rollback-success "$rollback_tag" "$(docker image inspect --format '{{.Id}}' "$rollback_tag")" "" "$(state_value image)" "$(state_value backup)"
  log "回滚完成。数据库迁移不会自动回滚，请按备份恢复流程处理。"
}

acquire_lock() {
  if command -v flock >/dev/null 2>&1; then
    exec 9>"$LOCK_FILE"
    flock -n 9 || die "另一个 Docker 安装、更新或备份任务正在运行。"
    LOCK_MODE=flock
    LOCK_ACQUIRED=true
    return 0
  fi

  LOCK_DIR="${LOCK_FILE}.d"
  if ! mkdir -- "$LOCK_DIR" 2>/dev/null; then
    die "另一个 Docker 安装、更新或备份任务正在运行。"
  fi
  printf '%s\n' "$$" > "$LOCK_DIR/pid"
  LOCK_MODE=mkdir
  LOCK_ACQUIRED=true
}

main() {
  parse_args "$@"
  resolve_deploy_dir
  if [[ ${BASH_SOURCE[0]:-} != bash && -f "${BASH_SOURCE[0]:-}" ]]; then
    SOURCE_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
  elif [[ -f "$DEPLOY_DIR/to-install.sh" ]]; then
    SOURCE_DIR="$DEPLOY_DIR"
  else
    SOURCE_DIR=""
  fi
  acquire_lock

  case "$MODE" in
    1) ensure_docker; fresh_install ;;
    2) ensure_docker; migration_install ;;
    3|update) ensure_docker; update_command ;;
    backup) ensure_docker; [[ -f "$DEPLOY_DIR/.env" ]] || die "备份要求部署目录已有 .env。"; backup_command ;;
    health) ensure_docker; [[ -f "$DEPLOY_DIR/.env" ]] || die "健康检查要求部署目录已有 .env。"; health_command ;;
    rollback) ensure_docker; rollback_command ;;
  esac
}

main "$@"
