#!/usr/bin/env bash
# Canonical quality gate: format, generation freshness, migration validity,
# tests with -race, vet, vulnerability audit, container build, clean tree.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
cd "$REPO_ROOT"
load_env
ensure_dev_reporting_env

echo "== gofmt =="
test -z "$(gofmt -l cmd/ internal/ db/)" || { echo "unformatted files above" >&2; exit 1; }

echo "== openapi strict gate =="
./scripts/check-openapi.sh

echo "== sqlc freshness =="
cp -r internal/adapter/postgres/sqlcgen /tmp/sqlcgen.before
sqlc generate
diff -r /tmp/sqlcgen.before internal/adapter/postgres/sqlcgen || { echo "stale generated code: run ./scripts/generate.sh" >&2; exit 1; }
rm -rf /tmp/sqlcgen.before

echo "== required files (baseline completeness) =="
./scripts/check-required-files.sh

echo "== migration validity (throwaway postgres:18) =="
cname="moonlight-check-pg-$$"
cleanup() { podman rm -f "$cname" >/dev/null 2>&1 || docker rm -f "$cname" >/dev/null 2>&1 || true; }
trap cleanup EXIT
if command -v podman >/dev/null 2>&1; then
  podman run -d --name "$cname" -e POSTGRES_PASSWORD=postgres -p 55434:5432 docker.io/library/postgres:18.1-bookworm >/dev/null
else
  docker run -d --name "$cname" -e POSTGRES_PASSWORD=postgres -p 55434:5432 docker.io/library/postgres:18.1-bookworm >/dev/null
fi
export DATABASE_URL="postgres://postgres:postgres@localhost:55434/postgres?sslmode=disable"
export ENVIRONMENT=development
wait_db_ready "$DATABASE_URL"
go run ./cmd/moonlight-cloud migrate up
go run ./cmd/moonlight-cloud migrate status
export TEST_DATABASE_URL="$DATABASE_URL"

echo "== tests (race) =="
go test -race ./...

echo "== go vet =="
go vet ./...

echo "== govulncheck =="
if command -v govulncheck >/dev/null 2>&1; then
  govulncheck ./...
else
  go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...
fi

echo "== frontend (typecheck, unit tests, build) =="
if ! command -v npm >/dev/null 2>&1; then
  echo "error: npm required for dashboard checks" >&2
  exit 1
fi
npm --prefix dashboard ci --no-audit --no-fund
npm --prefix dashboard run typecheck
npm --prefix dashboard run check
npm --prefix dashboard test
npm --prefix dashboard run build
test -f dashboard/dist/index.html || { echo "dashboard build missing index.html" >&2; exit 1; }
# Strict CSP compatibility: no inline scripts in the built shell.
if grep -qE '<script(\s[^>]*)?>[^[:space:]<]' dashboard/dist/index.html; then
  echo "dashboard: inline script detected" >&2
  exit 1
fi
echo "dashboard: no inline scripts"

echo "== container build =="
if command -v podman >/dev/null 2>&1; then
  podman build -f deploy/Containerfile -t moonlight-cloud:check .
else
  docker build -f deploy/Containerfile -t moonlight-cloud:check .
fi

echo "== clean tree for generated output =="
# Untracked files ignored: on a committed tree this catches stale regeneration.
test -z "$(git status --porcelain --untracked-files=no -- internal/ db/)" || { echo "generated output dirty" >&2; exit 1; }

echo "check.sh: PASS"
