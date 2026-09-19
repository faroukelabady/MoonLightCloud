#!/usr/bin/env bash
# Canonical: stop local dev environment (keeps the pgdata volume).
set -euo pipefail
source "$(dirname "$0")/lib.sh"
$(compose_cmd) -f "$COMPOSE_FILE" down
