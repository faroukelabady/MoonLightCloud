#!/usr/bin/env bash
# Canonical: stop local dev environment (keeps the pgdata volume).
set -euo pipefail
source "$(dirname "$0")/lib.sh"
load_env
allow_compose_down
$(compose_cmd) -f "$COMPOSE_FILE" down
