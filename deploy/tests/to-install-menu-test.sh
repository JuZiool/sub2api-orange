#!/usr/bin/env bash

set -Eeuo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)"
SCRIPT="$ROOT_DIR/to-install.sh"
TEMP_DIR="$(mktemp -d)"
FAKE_BIN="$TEMP_DIR/bin"
trap 'rm -rf -- "$TEMP_DIR"' EXIT

mkdir -p "$FAKE_BIN"
cat > "$FAKE_BIN/docker" <<'EOF'
#!/usr/bin/env bash
if [[ "$1" == compose && "$2" == version ]]; then exit 0; fi
if [[ "$1" == info ]]; then exit 0; fi
exit 0
EOF
cat > "$FAKE_BIN/curl" <<'EOF'
#!/usr/bin/env bash
exit 99
EOF
cat > "$FAKE_BIN/openssl" <<'EOF'
#!/usr/bin/env bash
printf 'not-used'
EOF
chmod +x "$FAKE_BIN"/*

help_output="$(bash "$SCRIPT" --help)"
grep -Fq -- '--mode <模式>' <<<"$help_output"
grep -Fq -- '无参数且存在终端时会进入操作菜单' <<<"$help_output"

expect_rejected() {
  local name="$1"
  shift
  local output status
  set +e
  output="$(PATH="$FAKE_BIN:$PATH" bash "$SCRIPT" --no-install-docker "$@" 2>&1)"
  status=$?
  set -e
  [[ "$status" -ne 0 ]] || { printf 'expected rejection: %s\n%s\n' "$name" "$output" >&2; exit 1; }
}

expect_rejected invalid-mode --mode invalid
expect_rejected missing-mode-value --mode
expect_rejected unknown-option --not-an-option

# Explicit modes are parsed without trying to read a menu from stdin.
expect_rejected explicit-health health --dir "$TEMP_DIR/health"
expect_rejected explicit-update --mode update --dir "$TEMP_DIR/update"

printf 'to-install menu and argument checks passed\n'
