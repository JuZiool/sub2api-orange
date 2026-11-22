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

run_rejected() {
  local name="$1"
  shift
  local output status
  set +e
  output="$(PATH="$FAKE_BIN:$PATH" bash "$SCRIPT" --no-install-docker "$@" 2>&1)"
  status=$?
  set -e
  [[ "$status" -ne 0 ]] || {
    printf '%s\n' "$output" >&2
    printf 'expected rejection: %s\n' "$name" >&2
    exit 1
  }
  [[ "$output" != *"POSTGRES_PASSWORD="* && "$output" != *"JWT_SECRET="* ]] || {
    printf 'secret leaked during rejection: %s\n' "$name" >&2
    exit 1
  }
}

fresh="$TEMP_DIR/fresh"
mkdir -p "$fresh"
printf 'POSTGRES_PASSWORD=keep\nJWT_SECRET=keep\n' > "$fresh/.env"
run_rejected fresh-existing-env --dir "$fresh" --mode 1

o_data="$TEMP_DIR/no-data"
mkdir -p "$o_data"
printf 'POSTGRES_PASSWORD=keep\n' > "$o_data/.env"
run_rejected migration-without-data --dir "$o_data" --mode 2

no_env="$TEMP_DIR/no-env"
mkdir -p "$no_env"
run_rejected update-without-env --dir "$no_env" --mode 3

[[ ! -e "$fresh/data" && ! -e "$o_data/data" ]] || {
  printf 'rejection created data directories\n' >&2
  exit 1
}

printf 'to-install safety checks passed\n'
