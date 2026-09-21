-- +goose Up
-- Phase 2E: durable Sale ownership arbitration (P2D-HIGH-02). The live
-- projector already arbitrates concurrent events, but ownership lived only
-- in disposable sales_projection rows: a rebuild could elect a different
-- winner and rewrite financial history. This table persists the logical
-- winner outside derived state:
--   sync_events ............ immutable accepted event history
--   sale_event_ownership ... durable conflict-arbitration decision
--   sales_projection + children ... disposable, rebuildable derived state
-- Rebuilds may delete projections (and reset processing) but must never
-- delete sync_events or sale_event_ownership.
CREATE TABLE sale_event_ownership (
    sale_id            UUID PRIMARY KEY,
    winning_event_id   UUID NOT NULL UNIQUE REFERENCES sync_events(event_id) ON DELETE RESTRICT,
    winning_device_id  UUID NOT NULL REFERENCES devices(id) ON DELETE RESTRICT,
    decided_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Backfill from existing projections: the projected source_event_id defines
-- current ownership (never receipt order). The guard fails the migration
-- with division-by-zero if the projection history cannot yield a unique
-- authoritative mapping instead of choosing arbitrary winners.
-- (Single-statement form: goose splits migration files on semicolons, so no
-- DO blocks are used anywhere in this history. The inner aggregate defeats
-- constant folding so the error fires only when the guard trips.)
SELECT CASE WHEN (SELECT count(*) FROM (
    SELECT sale_id FROM sales_projection GROUP BY sale_id HAVING count(*) > 1
) d) > 0 THEN ((SELECT count(*)/0 FROM sales_projection)) ELSE 0 END;
INSERT INTO sale_event_ownership (sale_id, winning_event_id, winning_device_id, decided_at)
SELECT sale_id, source_event_id, source_device_id, projected_at
FROM sales_projection
ON CONFLICT (sale_id) DO NOTHING;

-- +goose Down
-- Downgrade policy: ownership is durable correctness history. Refuse to drop
-- it while any arbitration decision exists; an empty table may roll back.
-- Production rollback otherwise requires restoring the application version
-- plus a database backup from before ownership-gated projection.
SELECT CASE WHEN (SELECT count(*) FROM sale_event_ownership) > 0
    THEN ((SELECT count(*)/0 FROM sale_event_ownership)) ELSE 0 END;
DROP TABLE IF EXISTS sale_event_ownership;
