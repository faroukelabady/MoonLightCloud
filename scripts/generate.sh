#!/usr/bin/env bash
# Canonical code generation. Regenerates sqlc output deterministically.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
cd "$REPO_ROOT"

if ! command -v sqlc >/dev/null 2>&1; then
  echo "error: sqlc not installed (see README prerequisites, pinned v1.31.1)" >&2
  exit 1
fi
sqlc generate
gofmt -l internal/ db/ cmd/ | grep . && { echo "gofmt needed above" >&2; exit 1; } || true
echo "generated code up to date"
