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
Usage: pg-restore.sh --url <target-postgres-url> --dump <file>
                     [--force] [--verify-only]
                     [--allow-unmarked-restore --confirm-unmarked-restore]

  --url          Target connection string. The database named here is
                 the one restored into.
  --dump         Path to the pg_dump custom-format artifact.
  --force        Required when the target already contains data.
  --verify-only  Verify the artifact and exit without restoring.

  --allow-unmarked-restore
                 BREAK-GLASS. Skip recovery marking. Requires
                 --confirm-unmarked-restore. Never use in production:
                 without marking, a restored Cloud reports healthy while
                 acknowledged business events may be missing.
  --confirm-unmarked-restore
                 Second, explicit confirmation for the break-glass path.

Post-restore validation runs automatically and reports the result.

Recovery marking is ENABLED by default and cannot be disabled through an
environment variable. PG_RESTORE_MARK_RECOVERY is accepted only when
unset or exactly 1; every other value is a configuration error.
USAGE
  exit 2
}

URL=""
DUMP=""
FORCE=0
VERIFY_ONLY=0
ALLOW_UNMARKED=0
CONFIRM_UNMARKED=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --url) URL="${2:-}"; shift 2 ;;
    --dump) DUMP="${2:-}"; shift 2 ;;
    --force) FORCE=1; shift ;;
    --verify-only) VERIFY_ONLY=1; shift ;;
    --allow-unmarked-restore) ALLOW_UNMARKED=1; shift ;;
    --confirm-unmarked-restore) CONFIRM_UNMARKED=1; shift ;;
    -h|--help) usage ;;
    *) echo "unknown argument: $1" >&2; usage ;;
  esac
done

[[ -n "$URL" ]] || { echo "missing --url" >&2; usage; }
[[ -n "$DUMP" ]] || { echo "missing --dump" >&2; usage; }
[[ -f "$DUMP" ]] || { echo "dump not found: $DUMP" >&2; exit 3; }
command -v pg_restore >/dev/null || { echo "pg_restore not found" >&2; exit 3; }
command -v psql >/dev/null || { echo "psql not found" >&2; exit 3; }

# --- Recovery marking policy: FAIL CLOSED (Phase 20 R5) ---------------
# Marking is a security-critical disaster-recovery control. It is enabled
# by default and is NOT configurable through an inherited environment
# variable, so an accidental or hostile variable can never silently turn
# it off.
#
# Accepted contract for PG_RESTORE_MARK_RECOVERY:
#   unset      -> marking enabled (default)
#   1          -> marking enabled (explicit)
#   anything else (0, true, false, yes, no, garbage, empty) -> ERROR
MARKING=1
if [[ -n "${PG_RESTORE_MARK_RECOVERY+x}" ]]; then
  case "${PG_RESTORE_MARK_RECOVERY}" in
    1) MARKING=1 ;;
    *)
      echo "pg-restore: FAIL - invalid PG_RESTORE_MARK_RECOVERY='${PG_RESTORE_MARK_RECOVERY}'" >&2
      echo "  Recovery marking is a fail-closed security control." >&2
      echo "  Accepted values: unset, or exactly 1 (enabled)." >&2
      echo "  To skip recovery marking deliberately, use the break-glass" >&2
      echo "  flags --allow-unmarked-restore --confirm-unmarked-restore." >&2
      exit 8
      ;;
  esac
fi

# Break-glass: skipping recovery marking requires TWO explicit CLI
# flags. Neither is read from the environment, so an inherited variable
# can never enable it by accident.
if [[ "$ALLOW_UNMARKED" == "1" ]]; then
  if [[ "$CONFIRM_UNMARKED" != "1" ]]; then
    echo "pg-restore: FAIL - --allow-unmarked-restore requires --confirm-unmarked-restore" >&2
    echo "  Skipping recovery marking leaves a restored Cloud able to report" >&2
    echo "  healthy synchronization while acknowledged business events are" >&2
    echo "  missing. This is never a routine operation." >&2
    exit 8
  fi
  MARKING=0
fi

if [[ "$MARKING" == "0" ]]; then
  echo "pg-restore: ==========================================================" >&2
  echo "pg-restore: SECURITY: RECOVERY MARKING DISABLED (break-glass)" >&2
  echo "pg-restore: The restored Cloud will NOT flag devices for" >&2
  echo "pg-restore: reconciliation. Acknowledged business events may be" >&2
  echo "pg-restore: missing and NOTHING will signal it." >&2
  echo "pg-restore: This is only defensible for a disposable test/lab" >&2
  echo "pg-restore: restore that you will verify by other means." >&2
  echo "pg-restore: requested flags: --allow-unmarked-restore --confirm-unmarked-restore" >&2
  echo "pg-restore: ==========================================================" >&2
fi

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
  case "$expect" in
    ">="*)
      local floor="${expect#>=}"
      if [[ "$got" =~ ^[0-9]+$ ]] && [[ "$got" -ge "$floor" ]]; then
        echo "  OK   $label = $got"
      else
        echo "  FAIL $label = $got (expected >= $floor)" >&2
        fail=1
      fi
      ;;
    *)
      if [[ "$got" == "$expect" ]]; then
        echo "  OK   $label = $got"
      else
        echo "  FAIL $label = $got (expected $expect)" >&2
        fail=1
      fi
      ;;
  esac
}

check "goose_db_version present" "SELECT count(*) FROM information_schema.tables WHERE table_name='goose_db_version'" "1"
check "schema version recorded" "SELECT count(*) FROM goose_db_version" ">=1"
check "devices table" "SELECT count(*) FROM information_schema.tables WHERE table_name='devices'" "1"
check "stores table" "SELECT count(*) FROM information_schema.tables WHERE table_name='stores'" "1"
check "sync_events table" "SELECT count(*) FROM information_schema.tables WHERE table_name='sync_events'" "1"
check "catalog_products table" "SELECT count(*) FROM information_schema.tables WHERE table_name='catalog_products'" "1"
check "sales_projection table" "SELECT count(*) FROM information_schema.tables WHERE table_name='sales_projection'" "1"
check "sync_recovery_state table" "SELECT count(*) FROM information_schema.tables WHERE table_name='sync_recovery_state'" "1"
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

echo "pg-restore: OK - post-restore validation passed"

# --- Phase 20 R2/R5: DATABASE_RESTORED is NOT STORE_RECONCILED --------
# A restored database is infrastructurally healthy while its projections
# may be historically incomplete. Mark every device as needing
# reconciliation so Retail refuses to report full synchronization until
# continuity is verified. The state is a table: it survives restarts.
#
# Three distinct outcomes are reported separately and are NOT equivalent:
#   DATABASE_CONTENT_RESTORED    pg_restore + schema/structural checks
#   RECOVERY_MARKING_COMPLETE    every device flagged for reconciliation
#   STORE_RECONCILED             reconciliation + projections converged
#
# This script can reach the first two. It can NEVER reach the third:
# STORE_RECONCILED requires the Retail-side reconciliation protocol.
mark_devices() {
  # "Applicable existing devices" are rows in devices. The FK on
  # sync_recovery_state.device_id is ON DELETE CASCADE, so an anti-join
  # is an exact, race-free proof that every device is flagged.
  local unmarked
  unmarked=$(psql "$URL" -Atqc "SELECT count(*) FROM devices d
    LEFT JOIN sync_recovery_state r ON r.device_id = d.id AND r.required
    WHERE r.device_id IS NULL" 2>/dev/null) || return 1
  if [[ "$unmarked" == "0" ]]; then
    return 0
  fi

  local devices dev
  devices=$(psql "$URL" -Atqc "SELECT id FROM devices") || return 1
  for dev in $devices; do
    [ -z "$dev" ] && continue
    psql "$URL" -q -c "INSERT INTO sync_recovery_state
        (device_id, required, reason, started_at, updated_at)
      VALUES ('$dev', TRUE, 'cloud database restored from backup', now(), now())
      ON CONFLICT (device_id) DO UPDATE SET required = TRUE,
        reason = EXCLUDED.reason, cleared_at = NULL, updated_at = now()" >/dev/null \
      || return 1
  done

  # Re-verify: the write must actually have taken effect.
  unmarked=$(psql "$URL" -Atqc "SELECT count(*) FROM devices d
    LEFT JOIN sync_recovery_state r ON r.device_id = d.id AND r.required
    WHERE r.device_id IS NULL" 2>/dev/null) || return 1
  [[ "$unmarked" == "0" ]] || return 1
  return 0
}

if [[ "$MARKING" == "1" ]]; then
  DEVICE_COUNT=$(psql "$URL" -Atqc "SELECT count(*) FROM devices" 2>/dev/null || echo -1)
  echo "pg-restore: marking devices RECOVERY_RECONCILIATION_REQUIRED ($DEVICE_COUNT device(s))"
  if ! mark_devices; then
    # The database content is already restored and is NOT atomically
    # undoable. Report that precisely: never dress this up as success.
    echo "" >&2
    echo "pg-restore: ===============================================" >&2
    echo "pg-restore: FAIL - RECOVERY MARKING FAILED" >&2
    echo "pg-restore: ===============================================" >&2
    echo "pg-restore: DATABASE_CONTENT_RESTORED = yes (this is NOT undoable" >&2
    echo "pg-restore:   by this script; the target already contains the" >&2
    echo "pg-restore:   restored content)" >&2
    echo "pg-restore: RECOVERY_MARKING_COMPLETE = NO" >&2
    echo "pg-restore: STORE_RECONCILED           = NO" >&2
    echo "" >&2
    echo "pg-restore: The restored Cloud may be missing acknowledged business" >&2
    echo "pg-restore: events and devices are NOT flagged to say so. Do NOT" >&2
    echo "pg-restore: report this restore as successful." >&2
    echo "" >&2
    echo "pg-restore: Action required: mark every device manually, then run" >&2
    echo "pg-restore: reconciliation. See docs/operations/disaster-recovery.md" >&2
    echo "pg-restore: section 5.2 for the manual marking SQL." >&2
    exit 9
  fi
  MARKED=$(psql "$URL" -Atqc "SELECT count(*) FROM sync_recovery_state WHERE required" 2>/dev/null || echo -1)
  echo "pg-restore: RECOVERY_MARKING_COMPLETE - $MARKED device(s) now require reconciliation"
else
  echo "pg-restore: RECOVERY_MARKING_COMPLETE = SKIPPED (break-glass)" >&2
fi

echo ""
echo "pg-restore: ======================================================="
echo "pg-restore: DATABASE_CONTENT_RESTORED    = yes"
if [[ "$MARKING" == "1" ]]; then
  echo "pg-restore: RECOVERY_MARKING_COMPLETE    = yes"
else
  echo "pg-restore: RECOVERY_MARKING_COMPLETE    = NO (break-glass skipped)"
fi
echo "pg-restore: STORE_RECONCILED             = NOT YET"
echo "pg-restore: ======================================================="
echo "pg-restore: Cloud serves traffic but business projections may be"
echo "pg-restore: historically incomplete. Do NOT report this Store as"
echo "pg-restore: synchronized until reconciliation completes."
echo ""
echo "pg-restore: next steps per docs/operations/disaster-recovery.md:"
echo "  1. confirm migrate status matches the release you are running"
echo "  2. verify /health/ready on the Cloud binary (infrastructure only)"
echo "  3. on EACH Retail device open Settings -> Cloud Sync and run"
echo "     the Reconcile action (see the Retail Cloud Sync panel)"
echo "  4. confirm the Cloud recovery status reports required=false,"
echo "     devices_unresolved=0 and projections_pending=0 for the Store"
echo "  5. only then report the Store as reconciled"
