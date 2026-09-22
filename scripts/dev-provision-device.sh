#!/usr/bin/env bash
# DEVELOPMENT ONLY provisioning helper. Prints one device credential for
# local auth testing. Never runs outside development.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
load_env
ensure_dev_reporting_env

export ENVIRONMENT="${ENVIRONMENT:-development}"
export DATABASE_URL="${DATABASE_URL:-postgres://moonlight:moonlight@localhost:5432/moonlight_dev?sslmode=disable}"
require_dev

name="${1:-shop-dev}"
echo "DEVELOPMENT ONLY — provisioning device '$name' in local dev DB"
go run ./cmd/moonlight-cloud device create --name "$name"
