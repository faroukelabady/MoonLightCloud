#!/usr/bin/env bash
# Phase 20 R5 — negative tests for pg-restore.sh recovery-marking policy.
# Uses a disposable real PostgreSQL. Checks behaviour, not shell text.
set -uo pipefail

# Every location is overridable so the harness runs outside the original
# developer workstation (defaults preserve the historical layout).
WORK="${PG_RESTORE_TEST_WORK:-/tmp/opencode}"
PGC="${PG_RESTORE_TEST_CONTAINER:-p20r5pg}"
PATH="${PG_RESTORE_TEST_BIN:-$WORK/p20r5-bin}:$PATH"
PG="${PG_RESTORE_TEST_URL:-postgres://postgres:postgres@localhost:5432/tgt?sslmode=disable}"
ADMIN="${PG_RESTORE_TEST_ADMIN_URL:-postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable}"
DUMP=$WORK/p20r5-backup.dump
SCRIPT="$(cd "$(dirname "$0")/.." && pwd)/scripts/pg-restore.sh"

PASS=0; FAIL=0
ok()   { echo "  PASS $1"; PASS=$((PASS+1)); }
bad()  { echo "  FAIL $1 -- $2"; FAIL=$((FAIL+1)); }

reset_target() {
  psql "$ADMIN" -q -c "DROP DATABASE IF EXISTS tgt" >/dev/null 2>&1
  psql "$ADMIN" -q -c "CREATE DATABASE tgt" >/dev/null 2>&1
}

# count of devices lacking a required recovery row
unmarked() {
  psql "$PG" -Atqc "SELECT count(*) FROM devices d
    LEFT JOIN sync_recovery_state r ON r.device_id=d.id AND r.required
    WHERE r.device_id IS NULL"
}
marked() { psql "$PG" -Atqc "SELECT count(*) FROM sync_recovery_state WHERE required"; }

run() { # run pg-restore with given env prefix, capture rc+output
  local envs="$1"; shift
  local args=""
  for a in "$@"; do args="$args $(printf '%q' "$a")"; done
  ( eval "$envs; bash $(printf '%q' "$SCRIPT") --url $(printf '%q' "$PG") --dump $(printf '%q' "$DUMP")$args" ) \
    >$WORK/p20r5-out.txt 2>&1
  echo $?
}

echo "=== A1: PG_RESTORE_MARK_RECOVERY unset -> marking enabled ==="
reset_target
rc=$(run "unset PG_RESTORE_MARK_RECOVERY")
if [[ "$rc" == "0" ]] && [[ "$(unmarked)" == "0" ]] && [[ "$(marked)" == "2" ]]; then
  ok "unset: rc=0, 2/2 devices marked"
else
  bad "unset" "rc=$rc unmarked=$(unmarked) marked=$(marked)"
fi

echo "=== A2: =1 -> marking enabled ==="
reset_target
rc=$(run "export PG_RESTORE_MARK_RECOVERY=1")
if [[ "$rc" == "0" ]] && [[ "$(unmarked)" == "0" ]]; then
  ok "=1: rc=0, all devices marked"
else
  bad "=1" "rc=$rc unmarked=$(unmarked)"
fi

for v in 0 garbage true false yes no; do
  echo "=== A3: PG_RESTORE_MARK_RECOVERY=$v -> must FAIL closed ==="
  reset_target
  rc=$(run "export PG_RESTORE_MARK_RECOVERY='$v'")
  if [[ "$rc" == "8" ]] && grep -q "invalid PG_RESTORE_MARK_RECOVERY" $WORK/p20r5-out.txt \
     && ! grep -q "STORE_RECONCILED             = NOT YET" $WORK/p20r5-out.txt; then
    ok "=$v refused (rc=8) before any restore"
  else
    bad "=$v" "rc=$rc"
  fi
done

echo "=== A4: empty-but-present -> must FAIL closed ==="
reset_target
rc=$(run "export PG_RESTORE_MARK_RECOVERY=''")
if [[ "$rc" == "8" ]]; then ok "empty-but-present refused (rc=8)"; else bad "empty" "rc=$rc"; fi

echo "=== A5: --allow-unmarked-restore without confirmation -> FAIL ==="
reset_target
rc=$(run "unset PG_RESTORE_MARK_RECOVERY" --allow-unmarked-restore)
if [[ "$rc" == "8" ]] && grep -q "requires --confirm-unmarked-restore" $WORK/p20r5-out.txt; then
  ok "unconfirmed break-glass refused (rc=8)"
else
  bad "unconfirmed break-glass" "rc=$rc"
fi

echo "=== A6: full break-glass -> skips marking but never claims STORE_RECONCILED ==="
reset_target
rc=$(run "unset PG_RESTORE_MARK_RECOVERY" --allow-unmarked-restore --confirm-unmarked-restore)
if [[ "$rc" == "0" ]] && [[ "$(marked)" == "0" ]] \
   && grep -q "RECOVERY MARKING DISABLED" $WORK/p20r5-out.txt \
   && grep -q "STORE_RECONCILED             = NOT YET" $WORK/p20r5-out.txt \
   && grep -q "RECOVERY_MARKING_COMPLETE    = NO" $WORK/p20r5-out.txt; then
  ok "break-glass: rc=0, marking skipped, no false reconciliation claim"
else
  bad "break-glass" "rc=$rc marked=$(marked)"
fi

echo "=== A7: break-glass is NOT reachable via environment variable ==="
reset_target
rc=$(run "export PG_RESTORE_MARK_RECOVERY=0")
if [[ "$rc" == "8" ]]; then
  ok "env var cannot disable marking (rc=8)"
else
  bad "env disable" "rc=$rc"
fi

echo "=== A8: marking SQL failure (CHECK rejects marker) -> operation FAILS ==="
# Build a fixture whose restored schema refuses the marker reason. The
# breakage must live IN THE DUMP so it survives pg_restore.
reset_target
bash "$SCRIPT" --url "$PG" --dump "$DUMP" >/dev/null 2>&1
psql "$PG" -q -c "DELETE FROM sync_recovery_state"
psql "$PG" -q -c "ALTER TABLE sync_recovery_state ADD CONSTRAINT c_reason CHECK (reason <> 'cloud database restored from backup')"
podman exec -e PGPASSWORD=postgres "$PGC" pg_dump --format=custom --no-owner --no-privileges -U postgres tgt > $WORK/p20r5-broken-check.dump 2>/dev/null
podman cp $WORK/p20r5-broken-check.dump "$PGC":/fixture/p20r5-broken-check.dump
DUMP=$WORK/p20r5-broken-check.dump
reset_target
rc=$(run "unset PG_RESTORE_MARK_RECOVERY")
DUMP=$WORK/p20r5-backup.dump
if [[ "$rc" == "9" ]] && grep -q "RECOVERY MARKING FAILED" $WORK/p20r5-out.txt \
   && grep -q "RECOVERY_MARKING_COMPLETE    = NO" $WORK/p20r5-out.txt \
   && grep -q "DATABASE_CONTENT_RESTORED    = yes" $WORK/p20r5-out.txt \
   && grep -q "report this restore as successful" $WORK/p20r5-out.txt \
   && ! grep -q "next steps per docs" $WORK/p20r5-out.txt; then
  ok "marking failure fails operation (rc=9), restored state reported accurately"
else
  bad "marking SQL failure" "rc=$rc"
fi

echo "=== A9: zero rows marked despite write attempt (insert swallowed) ==="
reset_target
bash "$SCRIPT" --url "$PG" --dump "$DUMP" >/dev/null 2>&1
psql "$PG" -q -c "DELETE FROM sync_recovery_state"
psql "$PG" -q -c "CREATE OR REPLACE FUNCTION swallow() RETURNS trigger AS \$\$ BEGIN RETURN NULL; END \$\$ LANGUAGE plpgsql"
psql "$PG" -q -c "CREATE TRIGGER t_swallow BEFORE INSERT ON sync_recovery_state FOR EACH ROW EXECUTE FUNCTION swallow()"
podman exec -e PGPASSWORD=postgres "$PGC" pg_dump --format=custom --no-owner --no-privileges -U postgres tgt > $WORK/p20r5-broken-swallow.dump 2>/dev/null
podman cp $WORK/p20r5-broken-swallow.dump "$PGC":/fixture/p20r5-broken-swallow.dump
DUMP=$WORK/p20r5-broken-swallow.dump
reset_target
rc=$(run "unset PG_RESTORE_MARK_RECOVERY")
DUMP=$WORK/p20r5-backup.dump
if [[ "$rc" == "9" ]] && grep -q "RECOVERY MARKING FAILED" $WORK/p20r5-out.txt; then
  ok "silent no-op write detected by re-verification (rc=9)"
else
  bad "zero rows marked" "rc=$rc"
fi

echo "=== A10: partial marking failure (only first device marks) ==="
reset_target
bash "$SCRIPT" --url "$PG" --dump "$DUMP" >/dev/null 2>&1
psql "$PG" -q -c "DELETE FROM sync_recovery_state"
psql "$PG" -q -c "CREATE OR REPLACE FUNCTION swallow2() RETURNS trigger AS \$\$ BEGIN
  IF (SELECT count(*) FROM sync_recovery_state) >= 1 THEN RETURN NULL; END IF; RETURN NEW; END \$\$ LANGUAGE plpgsql"
psql "$PG" -q -c "CREATE TRIGGER t_swallow2 BEFORE INSERT ON sync_recovery_state FOR EACH ROW EXECUTE FUNCTION swallow2()"
podman exec -e PGPASSWORD=postgres "$PGC" pg_dump --format=custom --no-owner --no-privileges -U postgres tgt > $WORK/p20r5-broken-partial.dump 2>/dev/null
podman cp $WORK/p20r5-broken-partial.dump "$PGC":/fixture/p20r5-broken-partial.dump
DUMP=$WORK/p20r5-broken-partial.dump
reset_target
rc=$(run "unset PG_RESTORE_MARK_RECOVERY")
DUMP=$WORK/p20r5-backup.dump
if [[ "$rc" == "9" ]] && grep -q "RECOVERY MARKING FAILED" $WORK/p20r5-out.txt; then
  ok "partial marking detected (rc=9)"
else
  bad "partial marking" "rc=$rc"
fi

echo "=== A11: marking is durable across process restart ==="
reset_target
rc=$(run "unset PG_RESTORE_MARK_RECOVERY")
BEFORE=$(marked)
# simulate Cloud restart: state is a table, so a new connection observes it
AFTER=$(psql "$PG" -Atqc "SELECT count(*) FROM sync_recovery_state WHERE required")
if [[ "$rc" == "0" ]] && [[ "$BEFORE" == "$AFTER" ]] && [[ "$AFTER" == "2" ]]; then
  ok "marking durable: $AFTER device(s) still required after restart"
else
  bad "durability" "rc=$rc before=$BEFORE after=$AFTER"
fi

echo ""
echo "=== pg-restore policy tests: $PASS passed, $FAIL failed ==="
[[ "$FAIL" == "0" ]]
