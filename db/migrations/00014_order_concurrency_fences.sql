-- +goose Up
-- Phase 6C-R1: order concurrency fencing (F-01/F-02). The reconcile
-- fence is operational integration state: per-order generation tokens
-- that make later-started reconciliations authoritative. It survives
-- restarts and multiple instances, is never deleted by
-- catalog/policy/inventory rebuilds, and needs normal database backup
-- like other commerce integration state. It is NOT business order
-- history. No garbage collection in R1: one tiny row per observed
-- provider order.
-- Webhook deliveries gain a monotonic claim generation so stale
-- workers cannot finalize events owned by newer leases. Existing rows
-- start at 0 and remain fully claimable; terminal rows stay terminal.

CREATE TABLE commerce_online_order_reconcile_fences (
    provider_key      TEXT NOT NULL CHECK (provider_key ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    external_order_id TEXT NOT NULL CHECK (char_length(external_order_id) BETWEEN 1 AND 32),
    generation        BIGINT NOT NULL CHECK (generation >= 1),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider_key, external_order_id)
);

ALTER TABLE commerce_online_order_webhook_events
    ADD COLUMN lease_generation BIGINT NOT NULL DEFAULT 0 CHECK (lease_generation >= 0);

-- +goose Down
-- Development/test repair only: fences and lease generations are
-- operational integration state. Never roll back in production.
ALTER TABLE commerce_online_order_webhook_events DROP COLUMN IF EXISTS lease_generation;
DROP TABLE IF EXISTS commerce_online_order_reconcile_fences;
