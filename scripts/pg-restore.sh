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

# Recovery marking writes the Phase 20 ledger columns, which exist from
# this schema version on (00045_sync_recovery_missing_ledger).
MARKING_SCHEMA=45

die() { echo "pg-restore: FAIL: $*" >&2; exit 1; }

usage() {
  cat >&2 <<'USAGE'
Usage: pg-restore.sh --url <target-postgres-url> --dump <file>
                     [--force] [--verify-only] [--cloud-binary <path>]
                     [--allow-unmarked-restore --confirm-unmarked-restore]

  --url          Target connection string. The database named here is
                 the one restored into.
  --dump         Path to the pg_dump custom-format artifact.
  --force        Required when the target already contains data.
  --verify-only  Verify the artifact (integrity, digest, schema) and exit
                 without restoring.
  --cloud-binary Path to the moonlight-cloud binary of the release you
                 will run. Required when the backup predates schema 45
                 (any backup taken before Phase 20): the restored database
                 is migrated with `<binary> migrate up` BEFORE devices are
                 marked. Run this script with that release's production
                 environment, exactly as you would run `migrate up`.

  --allow-unmarked-restore
                 BREAK-GLASS. Skip recovery marking. Requires
                 --confirm-unmarked-restore. Never use in production:
                 without marking, a restored Cloud reports healthy while
                 acknowledged business events may be missing.
  --confirm-unmarked-restore
                 Second, explicit confirmation for the break-glass path.
                 It is an error on its own.

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
CLOUD_BINARY=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --url) URL="${2:-}"; shift 2 ;;
    --dump) DUMP="${2:-}"; shift 2 ;;
    --cloud-binary) CLOUD_BINARY="${2:-}"; shift 2 ;;
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
if [[ -n "$CLOUD_BINARY" ]] && [[ ! -x "$CLOUD_BINARY" ]]; then
  echo "pg-restore: FAIL - --cloud-binary is not an executable file: $CLOUD_BINARY" >&2
  exit 3
fi

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
# can never enable it by accident. Either flag alone is an error.
if [[ "$ALLOW_UNMARKED" == "1" ]] && [[ "$CONFIRM_UNMARKED" != "1" ]]; then
  echo "pg-restore: FAIL - --allow-unmarked-restore requires --confirm-unmarked-restore" >&2
  echo "  Skipping recovery marking leaves a restored Cloud able to report" >&2
  echo "  healthy synchronization while acknowledged business events are" >&2
  echo "  missing. This is never a routine operation." >&2
  exit 8
fi
if [[ "$CONFIRM_UNMARKED" == "1" ]] && [[ "$ALLOW_UNMARKED" != "1" ]]; then
  echo "pg-restore: FAIL - --confirm-unmarked-restore is only valid together with --allow-unmarked-restore" >&2
  exit 8
fi
if [[ "$ALLOW_UNMARKED" == "1" ]]; then
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

# report_state prints the three outcomes separately and honestly. It is
# used on every path that ends after destructive work has started.
report_state() {
  local restored="$1" marked="$2"
  echo "" >&2
  echo "pg-restore: DATABASE_CONTENT_RESTORED    = $restored" >&2
  echo "pg-restore: RECOVERY_MARKING_COMPLETE    = $marked" >&2
  echo "pg-restore: STORE_RECONCILED             = NO" >&2
}

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

# 3. Archive schema version, read from the archive itself (goose_db_version
# data), BEFORE anything destructive happens.
ARCHIVE_SCHEMA=$(pg_restore --data-only --table=goose_db_version --file - "$DUMP" 2>/dev/null | awk -F'\t' '
  /^COPY .*goose_db_version/ { inblock = 1; next }
  inblock && /^\\\.$/ { inblock = 0; next }
  inblock && $3 == "t" && $2 + 0 > max { max = $2 + 0 }
  END { print max + 0 }')
echo "pg-restore: archive schema version $ARCHIVE_SCHEMA"
if [[ "$MARKING" == "1" ]] && [[ "$ARCHIVE_SCHEMA" -lt "$MARKING_SCHEMA" ]] && [[ -z "$CLOUD_BINARY" ]]; then
  echo "pg-restore: REFUSED - the backup predates recovery schema $MARKING_SCHEMA (archive schema $ARCHIVE_SCHEMA)." >&2
  echo "  Devices cannot be marked for reconciliation until the restored database" >&2
  echo "  is migrated. Re-run with --cloud-binary <moonlight-cloud binary of the" >&2
  echo "  release you will run>, using that release's production environment, so" >&2
  echo "  the script migrates and then marks every device. Nothing was changed." >&2
  exit 10
fi

if [[ "$VERIFY_ONLY" == "1" ]]; then
  echo "pg-restore: verify-only OK"
  exit 0
fi

# 4. Refuse to overwrite a database that already has data. A target that
# cannot even be inspected is refused: "unknown" is never "empty".
if ! EXISTING=$(psql "$URL" -Atqc "SELECT count(*) FROM pg_catalog.pg_tables WHERE schemaname NOT IN ('pg_catalog','information_schema')" 2>/dev/null); then
  echo "pg-restore: REFUSED - cannot inspect the target database (connection, credentials or permissions). Nothing was changed." >&2
  exit 3
fi
if [[ "$EXISTING" != "0" ]] && [[ "$FORCE" != "1" ]]; then
  echo "pg-restore: REFUSED - target database already contains $EXISTING tables." >&2
  echo "  Pass --force only after taking a fresh backup of the current state." >&2
  exit 6
fi

# 5. Drop-and-recreate cleanly so no stale objects survive the restore.
# From here on the target is being changed: every failure reports the
# partial state instead of a bare error.
echo "pg-restore: restoring into target"
set +e
pg_restore --clean --if-exists --no-owner --no-privileges --dbname "$URL" "$DUMP"
PG_RESTORE_RC=$?
set -e
if [[ "$PG_RESTORE_RC" != "0" ]]; then
  echo "pg-restore: FAIL - pg_restore exited $PG_RESTORE_RC; the target may be PARTIALLY restored." >&2
  report_state "PARTIAL / UNKNOWN (not undoable by this script)" "NO"
  echo "pg-restore: Do NOT serve traffic from this database. Fix the cause and re-run the restore with --force." >&2
  exit 11
fi

# 6. Older backup: migrate the restored database with the release binary
# before validation and marking.
if [[ -n "$CLOUD_BINARY" ]]; then
  echo "pg-restore: migrating restored database with $CLOUD_BINARY"
  if ! DATABASE_URL="$URL" "$CLOUD_BINARY" migrate up; then
    echo "pg-restore: FAIL - migration of the restored database failed." >&2
    report_state "yes (not undoable by this script)" "NO"
    echo "pg-restore: Devices are NOT flagged. Fix the migration, then re-run the restore." >&2
    exit 12
  fi
fi

# 7. Post-restore validation. A restore that cannot prove its state is
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
check "admin_users table" "SELECT count(*) FROM information_schema.tables WHERE table_name='admin_users'" "1"
check "releases table" "SELECT count(*) FROM information_schema.tables WHERE table_name='releases'" "1"
check "update_rollouts table" "SELECT count(*) FROM information_schema.tables WHERE table_name='update_rollouts'" "1"
if [[ "$MARKING" == "1" ]]; then
  check "schema supports recovery marking" "SELECT coalesce(max(version_id),0) FROM goose_db_version WHERE is_applied" ">=$MARKING_SCHEMA"
fi

# Store/device scope integrity: every device binding must reference an
# existing Store. A restore that breaks scope is unusable.
check "orphan device bindings" "SELECT count(*) FROM device_store_bindings b LEFT JOIN stores s ON s.id=b.store_id WHERE s.id IS NULL" "0"

# Durable command identity: no duplicate command ids can exist by PK,
# but confirm the constraint is live after restore.
check "catalog_admin_commands PK" "SELECT count(*) FROM pg_indexes WHERE indexname='catalog_admin_commands_pkey'" "1"

if [[ "$fail" != "0" ]]; then
  echo "pg-restore: FAILED post-restore validation" >&2
  report_state "yes (not undoable by this script)" "NO"
  echo "pg-restore: Devices are NOT flagged and the restored database is not proven usable. Do NOT serve traffic from it." >&2
  exit 7
fi

echo "pg-restore: OK - post-restore validation passed"

# --- Phase 20 R2/R5/R6: DATABASE_RESTORED is NOT STORE_RECONCILED -----
# A restored database is infrastructurally healthy while its projections
# may be historically incomplete. Mark every device as needing
# reconciliation so Retail refuses to report full synchronization until
# continuity is verified. The state is a table: it survives restarts.
# Marking also starts a fresh verification generation: a ledger restored
# from the backup (e.g. an earlier completed recovery) is never reused.
#
# Three distinct outcomes are reported separately and are NOT equivalent:
#   DATABASE_CONTENT_RESTORED    pg_restore + schema/structural checks
#   RECOVERY_MARKING_COMPLETE    every device flagged for reconciliation
#   STORE_RECONCILED             reconciliation + projections converged
#
# This script can reach the first two. It can NEVER reach the third:
# STORE_RECONCILED requires the Retail-side reconciliation protocol.
mark_devices() {
  local devices dev
  devices=$(psql "$URL" -Atqc "SELECT id FROM devices") || return 1
  for dev in $devices; do
    [ -z "$dev" ] && continue
    psql "$URL" -q -c "INSERT INTO sync_recovery_state
        (device_id, required, reason, started_at, updated_at)
      VALUES ('$dev', TRUE, 'cloud database restored from backup', now(), now())
      ON CONFLICT (device_id) DO UPDATE SET required = TRUE,
        reason = EXCLUDED.reason, cleared_at = NULL, updated_at = now(),
        verify_generation = sync_recovery_state.verify_generation + 1,
        verify_last_event_id = '', verify_pages = 0, verify_events = 0,
        verify_complete = FALSE, verify_declared_total = 0, verify_missing = 0" >/dev/null \
      || return 1
  done

  # Re-verify: the write must actually have taken effect, ledger included.
  # The FK on sync_recovery_state.device_id is ON DELETE CASCADE, so an
  # anti-join is an exact proof that every device is flagged.
  local unmarked
  unmarked=$(psql "$URL" -Atqc "SELECT count(*) FROM devices d
    LEFT JOIN sync_recovery_state r ON r.device_id = d.id AND r.required
      AND NOT r.verify_complete AND r.verify_pages = 0
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
    report_state "yes (this is NOT undoable by this script; the target already contains the restored content)" "NO"
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
