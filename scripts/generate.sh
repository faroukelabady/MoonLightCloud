#!/usr/bin/env bash
# Canonical code generation. Regenerates sqlc output deterministically.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
cd "$REPO_ROOT"

run_sqlc generate
gofmt -l internal/ db/ cmd/ | grep . && { echo "gofmt needed above" >&2; exit 1; } || true
echo "generated code up to date"
