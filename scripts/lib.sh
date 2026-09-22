#!/usr/bin/env bash
# Shared helpers. Source from other scripts: `source "$(dirname "$0")/lib.sh"`.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# compose_cmd prints the container compose command (podman first, docker fallback).
compose_cmd() {
  if command -v podman >/dev/null 2>&1; then
    echo "podman compose"
  elif command -v docker >/dev/null 2>&1; then
    echo "docker compose"
  else
    echo "error: need podman or docker" >&2
    exit 1
  fi
}

COMPOSE_FILE="$REPO_ROOT/deploy/compose.yaml"

# load_env sources .env.local when present (local overrides, never committed).
load_env() {
  if [[ -f "$REPO_ROOT/.env.local" ]]; then
    set -a
    # shellcheck disable=SC1091
    source "$REPO_ROOT/.env.local"
    set +a
  fi
}

# ensure_dev_reporting_env defaults the explicit dev-open reporting flag
# for local development scripts only: when the effective environment is
# development, no reporting token is configured, and the flag is unset, it
# opts into the documented development behavior so `go run`/`dbprobe` paths
# pass the fail-closed startup gate. Explicit values (including an explicit
# false, or any token) are never overridden. Production application
# behavior is unchanged: the raw image without this env still fails closed.
ensure_dev_reporting_env() {
  if [[ -z "${ALLOW_UNAUTHENTICATED_REPORTING:-}" && -z "${REPORTING_API_TOKEN:-}" && "${ENVIRONMENT:-development}" == "development" ]]; then
    export ALLOW_UNAUTHENTICATED_REPORTING=true
  fi
}
require_dev() {
  local env="${ENVIRONMENT:-development}"
  if [[ "$env" != "development" ]]; then
    echo "refusing: ENVIRONMENT=$env (destructive op requires development)" >&2
    exit 1
  fi
}

# require_clean_db_target aborts unless DATABASE_URL points at the known
# local development database. Never drop anything else.
require_local_dev_db() {
  require_dev
  local url="${DATABASE_URL:-}"
  if [[ -z "$url" ]]; then
    echo "refusing: DATABASE_URL is not set" >&2
    exit 1
  fi
  case "$url" in
    *"@localhost"*"/moonlight_dev"*|*"@127.0.0.1"*"/moonlight_dev"*) ;;
    *)
      echo "refusing: DATABASE_URL does not look like the local dev database: $(redact_url "$url")" >&2
      exit 1
      ;;
  esac
}

redact_url() {
  echo "$1" | sed -E 's#(://[^:/]+:)[^@]+@#\1***@#'
}

# wait_db_ready polls a real PostgreSQL handshake (not just TCP accept).
# Builds the app binary once to /tmp so loop iterations stay fast.
wait_db_ready() {
  local url="${1:?usage: wait_db_ready DATABASE_URL}"
  (cd "$REPO_ROOT" && go build -o /tmp/moonlight-cloud-probe ./cmd/moonlight-cloud) >/dev/null 2>&1
  for _ in $(seq 1 60); do
    if DATABASE_URL="$url" ENVIRONMENT=development ALLOW_UNAUTHENTICATED_REPORTING=true /tmp/moonlight-cloud-probe dbprobe >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "postgres did not become ready: $(redact_url "$url")" >&2
  return 1
}

# wait_postgres polls until postgres accepts connections (no blind sleep).
pg_isready_check() {
  # Pure-Go readiness via the app binary probe path would need HTTP;
  # for postgres use a minimal connection attempt with psql when present,
  # else a TCP check on the URL host:port.
  if command -v psql >/dev/null 2>&1; then
    PGCONNECT_TIMEOUT=2 psql "$1" -c 'select 1' >/dev/null 2>&1
    return $?
  fi
  python3 - "$1" <<'EOF' >/dev/null 2>&1
import socket, sys, urllib.parse
u = urllib.parse.urlparse(sys.argv[1])
s = socket.create_connection((u.hostname or 'localhost', u.port or 5432), timeout=2)
s.close()
EOF
}
