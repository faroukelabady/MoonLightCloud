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
