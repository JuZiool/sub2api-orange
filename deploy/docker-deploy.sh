#!/usr/bin/env bash

set -Eeuo pipefail

readonly RAW_BASE_URL="${SUB2API_RAW_BASE_URL:-https://raw.githubusercontent.com/JuZiool/sub2api-orange/main/deploy}"
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" 2>/dev/null && pwd -P || true)"

if [[ -n "$SCRIPT_DIR" && -f "$SCRIPT_DIR/to-install.sh" ]]; then
  exec bash "$SCRIPT_DIR/to-install.sh" "$@"
fi

TEMP_FILE="$(mktemp)"
trap 'rm -f -- "$TEMP_FILE"' EXIT
curl --proto '=https' --tlsv1.2 -fsSL "$RAW_BASE_URL/to-install.sh" -o "$TEMP_FILE"
exec bash "$TEMP_FILE" "$@"
