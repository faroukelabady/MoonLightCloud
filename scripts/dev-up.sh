#!/usr/bin/env bash
# Canonical: start local dev environment (postgres + cloud in Compose),
# wait for readiness, run explicit migrations.
# Mirrors production (both services in OCI) while keeping `go run` available
# for fast iteration (see README).
set -euo pipefail
source "$(dirname "$0")/lib.sh"
load_env

export ENVIRONMENT="${ENVIRONMENT:-development}"
export DATABASE_URL="${DATABASE_URL:-postgres://moonlight:moonlight@localhost:5432/moonlight_dev?sslmode=disable}"

$(compose_cmd) -f "$COMPOSE_FILE" up --build -d

echo "waiting for postgres..."
wait_db_ready "$DATABASE_URL"

echo "running migrations..."
"$(dirname "$0")/migrate.sh" up

echo "waiting for cloud readiness..."
for _ in $(seq 1 60); do
  if curl -fsS http://localhost:8080/health/ready >/dev/null 2>&1; then
    echo "MoonLightCloud ready at http://localhost:8080"
    exit 0
  fi
  sleep 1
done
echo "cloud did not become ready" >&2
exit 1
