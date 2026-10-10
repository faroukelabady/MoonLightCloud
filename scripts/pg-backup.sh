#!/usr/bin/env bash
# MoonLightCloud PostgreSQL backup.
#
# Layer 2 of the documented 4-layer model (docs/operations/backup.md):
# a verified logical backup. This is NOT point-in-time recovery - a
# pg_dump can only ever restore to the moment of the dump. RPO for
# disaster recovery is therefore "the last successful backup", which is
# why the runbook requires a schedule tighter than the RPO target and a
# provider-level WAL/snapshot layer for anything stricter.
#
# The backup is written as pg_dump custom format (compressed,
# parallel-restore capable) plus a manifest recording provenance and a
# SHA-256 of the dump. Verification is a real pg_restore --list read of
# the produced artifact: a successful write is not evidence of a valid
# backup.
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage: pg-backup.sh --url <postgres-url> --out <directory> [--label <name>]

  --url    PostgreSQL connection string (superuser or backup role).
  --out    Destination directory. Must not be the database volume.
  --label  Optional backup label (default: UTC timestamp).

Environment:
  PG_BACKUP_RETENTION   Number of backups to retain in --out (default 7).

The backup contains the full database INCLUDING credentials hashes and
business data. Store it outside this host. Never commit backups.
USAGE
  exit 2
}

URL=""
OUT=""
LABEL=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --url) URL="$2"; shift 2 ;;
    --out) OUT="$2"; shift 2 ;;
    --label) LABEL="$2"; shift 2 ;;
    -h|--help) usage ;;
    *) echo "unknown argument: $1" >&2; usage ;;
  esac
done

[[ -n "$URL" ]] || { echo "missing --url" >&2; usage; }
[[ -n "$OUT" ]] || { echo "missing --out" >&2; usage; }
command -v pg_dump >/dev/null || { echo "pg_dump not found in PATH" >&2; exit 3; }
command -v pg_restore >/dev/null || { echo "pg_restore not found in PATH" >&2; exit 3; }

if [[ -z "$LABEL" ]]; then
  LABEL="$(date -u +%Y%m%dT%H%M%SZ)"
fi

mkdir -p "$OUT"
chmod 700 "$OUT" 2>/dev/null || true

DUMP="$OUT/moonlight-cloud-$LABEL.dump"
MANIFEST="$OUT/moonlight-cloud-$LABEL.manifest.json"
SCHEMA_ONLY="$OUT/moonlight-cloud-$LABEL.schema.sql"
STARTED="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
STARTED_S=$(date +%s)

echo "pg-backup: writing $DUMP"
# Custom format: compressed, consistent snapshot (single transaction),
# restoreable with pg_restore. Never a raw file copy of the data dir.
pg_dump --format=custom --no-owner --no-privileges --file "$DUMP" "$URL"
chmod 600 "$DUMP"

# Schema-only companion: lets an operator inspect or partially restore
# structure without loading the full dump.
pg_dump --schema-only --no-owner --no-privileges --file "$SCHEMA_ONLY" "$URL"
chmod 600 "$SCHEMA_ONLY"

# Verification: the artifact must actually open as a pg_dump archive.
# A truncated or corrupt file fails here and is removed - it is never
# reported as a successful backup.
if ! pg_restore --list "$DUMP" >/dev/null 2>"$OUT/.verify-$LABEL.err"; then
  echo "pg-backup: FAILED verification - removing unusable artifact" >&2
  cat "$OUT/.verify-$LABEL.err" >&2 || true
  rm -f "$OUT/.verify-$LABEL.err" "$DUMP" "$SCHEMA_ONLY"
  exit 4
fi
rm -f "$OUT/.verify-$LABEL.err"

SIZE=$(wc -c <"$DUMP" | tr -d ' ')
DIGEST=$(sha256sum "$DUMP" | cut -d' ' -f1)
ENDED="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
ELAPSED=$(( $(date +%s) - STARTED_S ))

SCHEMA_VERSION="$(pg_restore --schema-only --file - "$DUMP" 2>/dev/null | grep -oE "INSERT INTO schema_migrations[^;]*" | head -1 || true)"

cat >"$MANIFEST" <<MANIFEST
{
  "backup_id": "$LABEL",
  "created_at": "$ENDED",
  "started_at": "$STARTED",
  "duration_seconds": $ELAPSED,
  "tool": "pg_dump",
  "format": "custom",
  "file": "$(basename "$DUMP")",
  "size_bytes": $SIZE,
  "sha256": "$DIGEST",
  "schema_only": "$(basename "$SCHEMA_ONLY")",
  "verified": "pg_restore --list",
  "point_in_time_recovery": false,
  "note": "Logical dump: restores to the dump moment only. PITR requires provider WAL archiving."
}
MANIFEST
chmod 600 "$MANIFEST"

# Bounded retention: oldest backups removed first. Only files this
# script created are considered, so nothing else in --out is touched.
RETENTION="${PG_BACKUP_RETENTION:-7}"
if [[ "$RETENTION" =~ ^[0-9]+$ ]] && [[ "$RETENTION" -gt 0 ]]; then
  mapfile -t CANDIDATES < <(ls -1t "$OUT"/moonlight-cloud-*.dump 2>/dev/null || true)
  if [[ ${#CANDIDATES[@]} -gt "$RETENTION" ]]; then
    for old in "${CANDIDATES[@]:$RETENTION}"; do
      base="${old%.dump}"
      echo "pg-backup: retention removing $(basename "$old")"
      rm -f "$old" "$base.manifest.json" "$base.schema.sql"
    done
  fi
fi

echo "pg-backup: OK label=$LABEL size=$SIZE sha256=$DIGEST duration=${ELAPSED}s"
echo "pg-backup: manifest=$MANIFEST"
echo "pg-backup: WARNING - copy $OUT off this host; a backup on the same disk is not disaster recovery."
