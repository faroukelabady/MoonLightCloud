-- +goose Up
-- Phase 19 performance: projection backlog discovery ordering.
--
-- Evidence (internal/perfbench cloud harness; docs/performance/
-- phase19-budgets.md): PendingCatalogEvents / PendingSaleEvents /
-- PendingReturnEvents all order by (received_at, event_id) on
-- sync_events. Only idx_sync_events_device_received (device_id,
-- received_at) existed, so the planner sorted the entire matching inbox
-- per scan — the dominant cost of backlog discovery at 10k+ queued
-- events and a contributor to the Phase 17 SERIALIZABLE tail (§28).
--
-- This index lets discovery walk the inbox in order and stop at the
-- batch limit. Additive only: migrations 00001..00042 are unchanged.
--
-- WRITE/STORAGE COST: one b-tree on the append-only inbox; writes are
-- inserts during ingest (already in a batch transaction), so the
-- maintenance cost is negligible against the scan it removes.
CREATE INDEX idx_sync_events_received ON sync_events(received_at, event_id);

-- +goose Down
DROP INDEX IF EXISTS idx_sync_events_received;
