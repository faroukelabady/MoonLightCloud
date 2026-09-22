#!/usr/bin/env bash
# Canonical migrations. Uses the app binary (goose embedded); no global
# goose install required. Usage: ./scripts/migrate.sh up|status
set -euo pipefail
source "$(dirname "$0")/lib.sh"
load_env
ensure_dev_reporting_env

export ENVIRONMENT="${ENVIRONMENT:-development}"
export DATABASE_URL="${DATABASE_URL:-postgres://moonlight:moonlight@localhost:5432/moonlight_dev?sslmode=disable}"

cmd="${1:-up}"
case "$cmd" in
  up)     go run ./cmd/moonlight-cloud migrate up ;;
  status) go run ./cmd/moonlight-cloud migrate status ;;
  *) echo "usage: migrate.sh up|status" >&2; exit 1 ;;
esac
