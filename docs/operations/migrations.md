# Migration and Rollback Policy

Migrations are append-only (`db/migrations`, goose, embedded). Never edit
applied history. Startup verifies the schema version and refuses to boot on
mismatch (readiness fails).

## Downgrade policy

Downgrades are guarded, not silent:

- `00006` Down refuses while any `sync_events.payload_hash_version = 2` row
  exists (v2 exact-hash rows would become unreadable to old code). A v1-only
  database may roll back.
- `00007` Down refuses while any `sale_event_ownership` decision exists
  (durable arbitration must not be casually destroyed). An empty ownership
  table may roll back.
- Guard failures surface as SQL errors (`division by zero` from the
  single-statement guard form — goose splits files on semicolons, so no DO
  blocks are used) and leave schema and data intact.

Production rollback otherwise requires restoring the application version
plus a database backup from before the newer semantics were accepted:

- pre-v2 backup for a 00006 downgrade with v2 rows;
- pre-ownership backup for a 00007 downgrade with decisions.

## Tested paths

`internal/migrate/migrate_test.go` covers fresh→latest, v5→latest,
v6→latest, projection→ownership backfill (winner + loser), v1-only Down,
v2 Down refusal with intact schema/data, and ownership Down policy.

## Telegram recipient bound (00026)

Schema 26 expands recipient checks from 32 to 33 characters in notification
messages, business-report recipients and operational-alert recipients.
Existing values and delivery snapshots are not rewritten. Startup requires
schema 26 for the R1 binary.

Down reinstates the old constraints in one transaction and refuses while
any affected value exceeds 32 characters. A refusal leaves schema version
26 and all data intact. There is no truncation. Rollback requires a compatible
backup/application pair or explicit operator handling of the longer durable
values; do not silently change queued destinations or historical snapshots.
