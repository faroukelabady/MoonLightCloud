#!/usr/bin/env bash
# Canonical DESTRUCTIVE reset of the local dev database only.
# Refuses anything but ENVIRONMENT=development + the known local dev DB.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
load_env

export ENVIRONMENT="${ENVIRONMENT:-development}"
export DATABASE_URL="${DATABASE_URL:-postgres://moonlight:moonlight@localhost:5432/moonlight_dev?sslmode=disable}"
require_local_dev_db

echo "WARNING: destroying local dev volume moonlightcloud_pgdata (DEVELOPMENT ONLY)."
$(compose_cmd) -f "$COMPOSE_FILE" down
volume="$(podman volume ls --format '{{.Name}}' 2>/dev/null | grep -x 'moonlightcloud_pgdata' || true)"
if [[ -n "$volume" ]]; then
  podman volume rm "$volume" || docker volume rm "$volume"
fi
echo "dev database destroyed. Run ./scripts/dev-up.sh for a fresh environment."
