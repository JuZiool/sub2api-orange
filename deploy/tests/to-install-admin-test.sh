#!/usr/bin/env bash

set -Eeuo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
# 与 install-github-token-test.sh 一样，只加载函数，不执行安装入口。
source <(head -n -1 "$ROOT_DIR/to-install.sh")
TEMP_ROOT="$(cd -- "${TMPDIR:-/tmp}" && pwd -P)"
TEMP_DIR="$(mktemp -d "$TEMP_ROOT/orange-admin-tests.XXXXXX")"
cleanup_test_directory() {
  local resolved
  resolved="$(cd -- "$TEMP_DIR" && pwd -P)" || return 1
  [[ "$resolved" == "$TEMP_ROOT"/orange-admin-tests.* ]] || return 1
  rm -rf -- "$resolved"
}
trap 'cleanup; cleanup_test_directory' EXIT

generate_hex() { printf 'test-secret'; }
compose() { die '凭据测试不得调用 Docker'; }

validate_admin_password '' || die '空密码应交给后端生成'
validate_admin_password '12345678' || die '8 字节密码应有效'
printf -v password72 '%072d' 0
validate_admin_password "$password72" || die '72 字节密码应有效'
validate_admin_password '中文密' || die '9 字节的 UTF-8 密码应有效'
printf -v chinese72 '密%.0s' {1..24}
validate_admin_password "$chinese72" || die '72 字节的 UTF-8 密码应有效'
for password in '1234567' "${password72}0" '中文' "${chinese72}密"; do
  if validate_admin_password "$password"; then
    die '错误接受了长度不在 8–72 字节内的密码'
  fi
done

if ! { : </dev/tty; } 2>/dev/null; then
  ADMIN_EMAIL='stale@example.com'
  ADMIN_PASSWORD='stale-password'
  prompt_fresh_values
  [[ -z "$ADMIN_EMAIL" && -z "$ADMIN_PASSWORD" ]] || die '无终端时必须使用随机凭据默认值'
  [[ "$SERVER_PORT" == 8080 ]] || die '默认端口必须为 8080'
else
  printf 'SKIP: 无终端默认值用例需要在非交互式 shell 中执行\n'
fi

DEPLOY_DIR="$TEMP_DIR"
cp -- "$ROOT_DIR/.env.example" "$DEPLOY_DIR/.env.example"
SERVER_PORT=8080
BIND_HOST=0.0.0.0
ADMIN_EMAIL=''
ADMIN_PASSWORD=''
write_fresh_env
[[ "$(read_env_value ADMIN_EMAIL '')" == '' ]] || die '空邮箱不能变成固定默认邮箱'
[[ "$(read_env_value ADMIN_PASSWORD '')" == '' ]] || die '空密码不能被安装器替换'
grep -Fxq "ADMIN_EMAIL=''" "$DEPLOY_DIR/.env"
grep -Fxq "ADMIN_PASSWORD=''" "$DEPLOY_DIR/.env"

ADMIN_EMAIL='owner@example.com'
ADMIN_PASSWORD='explicit-password'
write_fresh_env
[[ "$(read_env_value ADMIN_EMAIL '')" == "$ADMIN_EMAIL" ]] || die '显式邮箱必须保留'
[[ "$(read_env_value ADMIN_PASSWORD '')" == "$ADMIN_PASSWORD" ]] || die '显式密码必须保留'
before="$(cksum "$DEPLOY_DIR/.env")"
if (fresh_install) >"$TEMP_DIR/rejected.log" 2>&1; then
  die '已有 .env 时必须拒绝全新安装'
fi
[[ "$(cksum "$DEPLOY_DIR/.env")" == "$before" ]] || die '不得重置已有凭据'
grep -Fq '已有 .env' "$TEMP_DIR/rejected.log"
printf 'PASS: Orange 管理员默认值、密码字节边界、env 输出及旧配置保护（未调用 Docker）\n'
