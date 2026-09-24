#!/usr/bin/env bash
# Required-file completeness for the Phase 2E baseline (P2D-HIGH-01): every
# implementation-critical path must be Git-tracked. Dirty-only remediation
# is not a baseline. Stage with `git add` before finalizing; the canonical
# check.sh clean-tree gate additionally requires no dirty tracked output.
set -euo pipefail
source "$(dirname "$0")/lib.sh"
cd "$REPO_ROOT"

required=(
  db/migrations/00006_sync_event_hash_version.sql
  db/migrations/00007_sale_event_ownership.sql
  db/queries/sale_ownership.sql
  db/queries/sale_projection.sql
  db/queries/sync_events.sql
  db/queries/sync_processing.sql
  internal/adapter/postgres/sqlcgen/sale_ownership.sql.go
  internal/adapter/postgres/sqlcgen/sale_projection.sql.go
  internal/adapter/postgres/sqlcgen/sync_events.sql.go
  internal/adapter/postgres/sqlcgen/sync_processing.sql.go
  internal/adapter/postgres/sqlcgen/models.go
  internal/adapter/postgres/sale.go
  internal/adapter/postgres/sync.go
  internal/auth/service.go
  internal/migrate/migrate.go
  internal/migrate/migrate_test.go
  internal/sale/sale.go
  internal/sale/projector.go
  internal/sale/sale_phase2d_test.go
  internal/sale/contract_parity_test.go
  internal/sale/openapi_test.go
  internal/returnrefund/returnrefund.go
  internal/returnrefund/returnrefund_test.go
  internal/returnrefund/openapi_test.go
  internal/returnrefund/testdata/return_partial.json
  internal/returnrefund/testdata/return_full.json
  internal/sync/sync.go
  internal/sync/canonical.go
  internal/sync/canonical_test.go
  internal/adapter/postgres/sale_conflict_test.go
  internal/adapter/postgres/ownership_rebuild_test.go
  internal/adapter/postgres/sale_replay_test.go
  internal/adapter/postgres/projection_retry_test.go
  internal/adapter/postgres/ingest_identity_test.go
  internal/adapter/postgres/projection_rebuild_test.go
  internal/adapter/postgres/sale_adversarial_test.go
  internal/adapter/postgres/sale_recovery_test.go
  internal/adapter/postgres/returnrefund_test.go
  internal/app/app.go
  docs/decisions/0017-canonical-hash-compat.md
  docs/decisions/0018-durable-sale-ownership.md
  docs/decisions/0019-fail-closed-legacy-hash.md
  docs/decisions/0026-return-refund-sync.md
  docs/sync/protocol.md
  docs/sync/returns.md
  docs/operations/migrations.md
  docs/operations/baseline-freeze.md
  api/openapi.yaml
)
failed=0
for rel in "${required[@]}"; do
  if [[ ! -f "$REPO_ROOT/$rel" ]]; then
    echo "missing required file: $rel" >&2
    failed=1
    continue
  fi
  if ! git ls-files --error-unmatch "$rel" >/dev/null 2>&1; then
    echo "required file not Git-tracked (run git add): $rel" >&2
    failed=1
  fi
done
if [[ "$failed" -ne 0 ]]; then
  echo "check-required-files: FAIL" >&2
  exit 1
fi
echo "check-required-files: PASS"
