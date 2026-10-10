#!/usr/bin/env bash
# MoonLightCloud PostgreSQL restore.
#
# Controlled recovery: verify first, restore into an explicitly named
# database, then validate. This script refuses to overwrite a database
# that already contains data unless --force is passed: an accidental
# restore must not silently destroy a newer production database.
#
# The restore is destructive for the target database. Take a fresh
# backup of whatever is there BEFORE using --force.
set -euo pipefail

die() { echo "pg-restore: FAIL: $*" >&2; exit 1; }

usage() {
  cat >&2 <<'USAGE'
Usage: pg-restore.sh --url <target-postgres-url> --dump <file> [--force] [--verify-only]

  --url          Target connection string. The database named here is
                 the one restored into.
  --dump         Path to the pg_dump custom-format artifact.
  --force        Required when the target already contains data.
  --verify-only  Verify the artifact and exit without restoring.

Post-restore validation runs automatically and reports the result.
USAGE
  exit 2
}

URL=""
DUMP=""
FORCE=0
VERIFY_ONLY=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --url) URL="$2"; shift 2 ;;
    --dump) DUMP="$2"; shift 2 ;;
    --force) FORCE=1; shift ;;
    --verify-only) VERIFY_ONLY=1; shift ;;
    -h|--help) usage ;;
    *) echo "unknown argument: $1" >&2; usage ;;
  esac
done

[[ -n "$URL" ]] || { echo "missing --url" >&2; usage; }
[[ -n "$DUMP" ]] || { echo "missing --dump" >&2; usage; }
[[ -f "$DUMP" ]] || { echo "dump not found: $DUMP" >&2; exit 3; }
command -v pg_restore >/dev/null || { echo "pg_restore not found" >&2; exit 3; }
command -v psql >/dev/null || { echo "psql not found" >&2; exit 3; }

# 1. Integrity: the artifact must open and enumerate. No restore is
# attempted on a dump that cannot be read.
echo "pg-restore: verifying $DUMP"
if ! pg_restore --list "$DUMP" >/dev/null; then
  echo "pg-restore: REFUSED - artifact does not open as a pg_dump archive" >&2
  exit 4
fi

# 2. Manifest digest check when a sibling manifest exists.
MANIFEST="${DUMP%.dump}.manifest.json"
if [[ -f "$MANIFEST" ]]; then
  EXPECTED=$(sed -n 's/.*"sha256": "\([0-9a-f]\{64\}\)".*/\1/p' "$MANIFEST" | head -1)
  ACTUAL=$(sha256sum "$DUMP" | cut -d' ' -f1)
  if [[ -n "$EXPECTED" ]] && [[ "$EXPECTED" != "$ACTUAL" ]]; then
    echo "pg-restore: REFUSED - digest mismatch (backup corrupted or tampered)" >&2
    echo "  manifest=$EXPECTED" >&2
    echo "  actual  =$ACTUAL" >&2
    exit 5
  fi
  echo "pg-restore: manifest digest OK"
else
  echo "pg-restore: WARNING - no sibling manifest; provenance cannot be verified" >&2
fi

if [[ "$VERIFY_ONLY" == "1" ]]; then
  echo "pg-restore: verify-only OK"
  exit 0
fi

# 3. Refuse to overwrite a database that already has data.
EXISTING=$(psql "$URL" -Atqc "SELECT count(*) FROM pg_catalog.pg_tables WHERE schemaname NOT IN ('pg_catalog','information_schema')" 2>/dev/null || echo "0")
if [[ "$EXISTING" != "0" ]] && [[ "$FORCE" != "1" ]]; then
  echo "pg-restore: REFUSED - target database already contains $EXISTING tables." >&2
  echo "  Pass --force only after taking a fresh backup of the current state." >&2
  exit 6
fi

# 4. Drop-and-recreate cleanly so no stale objects survive the restore.
echo "pg-restore: restoring into target"
pg_restore --clean --if-exists --no-owner --no-privileges --dbname "$URL" "$DUMP"

# 5. Post-restore validation. A restore that cannot prove its state is
# reported as a failure, never as success.
echo "pg-restore: validating"
fail=0

check() {
  local label="$1" sql="$2" expect="$3"
  local got
  got=$(psql "$URL" -Atqc "$sql" 2>/dev/null || echo "ERR")
  if [[ "$got" == "$expect" ]]; then
    echo "  OK   $label = $got"
  else
    echo "  FAIL $label = $got (expected $expect)" >&2
    fail=1
  fi
}

check "schema_migrations present" "SELECT count(*) FROM information_schema.tables WHERE table_name='schema_migrations'" "1"
check "devices table" "SELECT count(*) FROM information_schema.tables WHERE table_name='devices'" "1"
check "stores table" "SELECT count(*) FROM information_schema.tables WHERE table_name='stores'" "1"
check "sync_events table" "SELECT count(*) FROM information_schema.tables WHERE table_name='sync_events'" "1"
check "catalog_products table" "SELECT count(*) FROM information_schema.tables WHERE table_name='catalog_products'" "1"
check "sales_projection table" "SELECT count(*) FROM information_schema.tables WHERE table_name='sales_projection'" "1"
check "admin_users table" "SELECT count(*) FROM information_schema.tables WHERE table_name='admin_users'" "1"
check "releases table" "SELECT count(*) FROM information_schema.tables WHERE table_name='releases'" "1"
check "update_rollouts table" "SELECT count(*) FROM information_schema.tables WHERE table_name='update_rollouts'" "1"

# Store/device scope integrity: every device binding must reference an
# existing Store. A restore that breaks scope is unusable.
check "orphan device bindings" "SELECT count(*) FROM device_store_bindings b LEFT JOIN stores s ON s.id=b.store_id WHERE s.id IS NULL" "0"

# Durable command identity: no duplicate command ids can exist by PK,
# but confirm the constraint is live after restore.
check "catalog_admin_commands PK" "SELECT count(*) FROM pg_indexes WHERE indexname='catalog_admin_commands_pkey'" "1"

if [[ "$fail" != "0" ]]; then
  echo "pg-restore: FAILED post-restore validation" >&2
  exit 7
fi

echo "pg-restore: OK - restore complete and validated"

# Phase 20 R2: DATABASE_RESTORED is NOT STORE_RECONCILED. A restored
# database is infrastructurally healthy while its projections may be
# historically incomplete. Mark every device as needing reconciliation
# so Retail refuses to report full synchronization until continuity is
# verified. The state is durable and survives Cloud restarts.
MARK="${PG_RESTORE_MARK_RECOVERY:-1}"
if [[ "$MARK" == "1" ]]; then
  echo "pg-restore: marking devices RECOVERY_RECONCILIATION_REQUIRED"
  # The device key column is id (not device_id): a wrong column name
  # makes this SELECT fail silently and leave every device unmarked,
  # after which the script would still announce success. Read the list
  # first and fail loudly if marking does not take effect.
  DEVICES=$(psql "$URL" -Atqc "SELECT id FROM devices") || die "cannot read devices for recovery marking"
  MARKED=0
  for dev in $DEVICES; do
    [ -z "$dev" ] && continue
    psql "$URL" -q -c "INSERT INTO sync_recovery_state (device_id, required, reason, started_at, updated_at)
      VALUES ('$dev', TRUE, 'cloud database restored from backup', now(), now())
      ON CONFLICT (device_id) DO UPDATE SET required = TRUE,
        reason = EXCLUDED.reason, cleared_at = NULL, updated_at = now()" >/dev/null \
      || die "recovery marking failed for device $dev"
    MARKED=$((MARKED + 1))
  done
  COUNT=$(psql "$URL" -Atqc "SELECT count(*) FROM sync_recovery_state WHERE required" 2>/dev/null || echo 0)
  if [[ "$COUNT" -lt "$MARKED" ]]; then
    die "recovery marking incomplete: $MARKED device(s) processed but only $COUNT marked required"
  fi
  echo "pg-restore: $COUNT device(s) now require reconciliation"
else
  echo "pg-restore: WARNING - recovery marking disabled (PG_RESTORE_MARK_RECOVERY=0)"
fi

echo ""
echo "pg-restore: === DATABASE_RESTORED (not STORE_RECONCILED) ==="
echo "pg-restore: Cloud serves traffic but business projections may be"
echo "pg-restore: historically incomplete. Do NOT report this Store as"
echo "pg-restore: synchronized until reconciliation completes."
echo ""
echo "pg-restore: next steps per docs/operations/disaster-recovery.md:"
echo "  1. confirm migrate status matches the release you are running"
echo "  2. verify /health/ready on the Cloud binary (infrastructure only)"
echo "  3. run the reconciliation on each Retail device:"
echo "       Retail -> Settings -> Cloud Sync -> Reconcile"
echo "       (CLI: moonlight-retail sync reconcile)"
echo "  4. verify recovery/status reports required=false for every device"
echo "  5. only then report the Store as reconciled"
