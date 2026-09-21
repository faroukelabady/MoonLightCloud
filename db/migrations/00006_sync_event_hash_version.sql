-- +goose Up
-- Phase 2D HIGH-03: payload hash canonicalizer version. Pre-fix rows were
-- hashed with the float64 canonicalizer (v1); all new rows use the exact
-- decimal canonicalizer (v2). The version lets ingestion retry-compare with
-- the matching algorithm so legitimate pre-fix events still deduplicate
-- after upgrade without perpetuating float weakness for new events.
ALTER TABLE sync_events
    ADD COLUMN payload_hash_version INT NOT NULL DEFAULT 1
        CHECK (payload_hash_version IN (1, 2));
-- Existing rows keep v1 (their stored hash is a v1 hash). Duplicate
-- verification compares the stored immutable payload with the exact
-- canonicalizer (fail-closed); v1 hashes are version metadata only
-- (see ADR-0017 and its Phase 2E successor).

-- +goose Down
-- Downgrade policy (P2D-MED-04): dropping the version column after v2 rows
-- exist would silently make exact-hash rows unreadable to old code. Refuse
-- when any v2 row exists (integer division by zero fails the migration); a
-- v1-only database may roll back. Production rollback otherwise requires
-- restoring the application version plus a database backup from before v2
-- event acceptance. (Single-statement form: goose splits files on
-- semicolons, so no DO blocks are used. The inner aggregate defeats constant
-- folding so division-by-zero fires only when the guard trips.)
SELECT CASE WHEN (SELECT count(*) FROM sync_events WHERE payload_hash_version = 2) > 0
    THEN ((SELECT count(*)/0 FROM sync_events)) ELSE 0 END;
ALTER TABLE sync_events DROP COLUMN payload_hash_version;
