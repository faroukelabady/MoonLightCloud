#!/usr/bin/env bash
# Canonical tests: unit + integration against an isolated database.
# Starts a throwaway postgres:18 container when TEST_DATABASE_URL is unset.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
load_env
ensure_dev_reporting_env

if [[ -z "${TEST_DATABASE_URL:-}" ]]; then
  cname="moonlight-test-pg-$$"
  cleanup() { podman rm -f "$cname" >/dev/null 2>&1 || docker rm -f "$cname" >/dev/null 2>&1 || true; }
  trap cleanup EXIT
  if command -v podman >/dev/null 2>&1; then
    podman run -d --name "$cname" -e POSTGRES_PASSWORD=postgres -p 55433:5432 docker.io/library/postgres:18.1-bookworm >/dev/null
  else
    docker run -d --name "$cname" -e POSTGRES_PASSWORD=postgres -p 55433:5432 docker.io/library/postgres:18.1-bookworm >/dev/null
  fi
  export TEST_DATABASE_URL="postgres://postgres:postgres@localhost:55433/postgres?sslmode=disable"
  wait_db_ready "$TEST_DATABASE_URL"
fi

go test ${GO_TEST_FLAGS:--race} ./...
