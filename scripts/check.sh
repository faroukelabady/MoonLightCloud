#!/usr/bin/env bash
# Canonical quality gate: format, generation freshness, migration validity,
# tests with -race, vet, vulnerability audit, container build, clean tree.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
cd "$REPO_ROOT"
check_root="$(mktemp -d "${TMPDIR:-/tmp}/moonlight-check.XXXXXXXX")"
cname=""
container_engine=""
image_tag="moonlight-cloud:check-${check_root##*.}"
cleanup() {
  if [[ -n "$container_engine" ]]; then
    if [[ -n "$cname" ]]; then
      "$container_engine" rm -f "$cname" >/dev/null 2>&1 || true
    fi
    "$container_engine" image rm "$image_tag" >/dev/null 2>&1 || true
  fi
  rm -rf -- "$check_root"
}
trap cleanup EXIT
load_env
ensure_dev_reporting_env
ensure_dev_auth_env

echo "== gofmt =="
test -z "$(gofmt -l cmd/ internal/ db/)" || { echo "unformatted files above" >&2; exit 1; }

echo "== openapi strict gate =="
./scripts/check-openapi.sh

echo "== openapi fixture parity gate =="
./scripts/check-openapi-fixtures.sh

echo "== sqlc freshness =="
cp -r internal/adapter/postgres/sqlcgen "$check_root/sqlcgen.before"
run_sqlc generate
diff -r "$check_root/sqlcgen.before" internal/adapter/postgres/sqlcgen || { echo "stale generated code: run ./scripts/generate.sh" >&2; exit 1; }

echo "== required files (baseline completeness) =="
./scripts/check-required-files.sh

echo "== migration validity (throwaway postgres:18) =="
cname="moonlight-check-pg-${check_root##*.}"
if command -v podman >/dev/null 2>&1; then
  container_engine=podman
elif command -v docker >/dev/null 2>&1; then
  container_engine=docker
else
  echo "error: podman or docker required" >&2
  exit 1
fi
"$container_engine" run -d --name "$cname" -e POSTGRES_PASSWORD=postgres -p 127.0.0.1::5432 docker.io/library/postgres:18.1-bookworm >/dev/null
mapping="$("$container_engine" port "$cname" 5432/tcp)"
db_port="${mapping##*:}"
if [[ ! "$db_port" =~ ^[0-9]+$ ]] || ((db_port < 1 || db_port > 65535)); then
  echo "error: could not resolve disposable PostgreSQL port" >&2
  exit 1
fi
echo "disposable PostgreSQL: $cname at 127.0.0.1:$db_port"
export DATABASE_URL="postgres://postgres:postgres@127.0.0.1:${db_port}/postgres?sslmode=disable"
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
# Always the pinned scanner (GOVULNCHECK_VERSION, lib.sh), never a govulncheck
# found on PATH: a stale global binary or shim must not change this gate.
# Offline/unavailable fails the script; there is no skip and no ID filter.
go run "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}" ./...

echo "== frontend (typecheck, unit tests, build) =="
if ! command -v pnpm >/dev/null 2>&1; then
  echo "error: pnpm 12 required for dashboard checks (mise or Corepack)" >&2
  exit 1
fi
pnpm --dir dashboard install --frozen-lockfile
pnpm --dir dashboard run typecheck
pnpm --dir dashboard run check
pnpm --dir dashboard test
pnpm --dir dashboard run build
pnpm --dir dashboard audit --audit-level high
test -f dashboard/dist/index.html || { echo "dashboard build missing index.html" >&2; exit 1; }
# Strict CSP compatibility: no inline scripts in the built shell.
if grep -qE '<script(\s[^>]*)?>[^[:space:]<]' dashboard/dist/index.html; then
  echo "dashboard: inline script detected" >&2
  exit 1
fi
echo "dashboard: no inline scripts"

echo "== container build =="
"$container_engine" build -f deploy/Containerfile -t "$image_tag" .

echo "== clean tree for generated output =="
# Untracked files ignored: on a committed tree this catches stale regeneration.
test -z "$(git status --porcelain --untracked-files=no -- internal/ db/)" || { echo "generated output dirty" >&2; exit 1; }

echo "check.sh: PASS"
