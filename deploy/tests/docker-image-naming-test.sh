#!/usr/bin/env bash

set -Eeuo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd -P)"
EXPECTED="ghcr.io/juziool/sub2api-orange"

assert_contains() {
  local file="$1" text="$2"
  grep -Fq -- "$text" "$ROOT_DIR/$file" || {
    printf 'missing %s in %s\n' "$text" "$file" >&2
    exit 1
  }
}

assert_not_contains() {
  local file="$1" text="$2"
  if grep -Fq -- "$text" "$ROOT_DIR/$file"; then
    printf 'unexpected %s in %s\n' "$text" "$file" >&2
    exit 1
  fi
}

for file in \
  deploy/docker-compose.yml \
  deploy/docker-compose.local.yml \
  deploy/docker-compose.standalone.yml \
  deploy/docker-compose.ghcr.yml \
  deploy/to-install.sh \
  deploy/apple-container.sh \
  deploy/.env.example \
  frontend/src/components/common/VersionBadge.vue; do
  assert_contains "$file" "$EXPECTED"
done

assert_contains .goreleaser.yaml 'sub2api-orange'
assert_contains .goreleaser.simple.yaml 'sub2api-orange'
assert_contains .github/workflows/release.yml 'sub2api-orange'

for file in \
  .goreleaser.yaml \
  .goreleaser.simple.yaml \
  .github/workflows/release.yml \
  deploy/docker-compose.yml \
  deploy/docker-compose.local.yml \
  deploy/docker-compose.standalone.yml \
  deploy/docker-compose.ghcr.yml \
  deploy/apple-container.sh \
  deploy/.env.example \
  frontend/src/components/common/VersionBadge.vue; do
  assert_not_contains "$file" 'ghcr.io/juziool/sub2api:'
done

# Keep the internal binary/service contract unchanged while the image repository moves.
assert_contains Dockerfile '/app/sub2api'
assert_contains deploy/Dockerfile '/app/sub2api'
assert_contains deploy/to-install.sh 'sub2api-orange:rollback-'

printf 'Docker image naming consistency checks passed\n'
