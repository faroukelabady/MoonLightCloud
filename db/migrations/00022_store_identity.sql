-- +goose Up
-- Phase 9A store identity foundation. Retail owns its Store ID; Cloud
-- registers and projects it. No multi-store business semantics.
--
-- 1. stores: minimal registry (identity + mutable display metadata +
--    small lifecycle). No tenant/organization complexity.
-- 2. device_store_bindings: at most one authoritative Store per device
--    (PRIMARY KEY on device_id); a Store may have many devices.
--    Immutable after binding: no update path, conflicts stay conflicts.
-- 3. sync_events.store_id: server-derived ingress context from the
--    authenticated device binding, NULL for legacy/unbound events.
--    Never trusted from payloads; never rewritten after receipt.

CREATE TABLE stores (
    id           UUID PRIMARY KEY,
    display_name TEXT NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 200),
    timezone     TEXT NOT NULL CHECK (char_length(timezone) BETWEEN 1 AND 64),
    status       TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE device_store_bindings (
    device_id  UUID PRIMARY KEY REFERENCES devices(id) ON DELETE RESTRICT,
    store_id   UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_device_store_bindings_store ON device_store_bindings (store_id, device_id);

ALTER TABLE sync_events ADD COLUMN store_id UUID REFERENCES stores(id) ON DELETE RESTRICT;
CREATE INDEX idx_sync_events_store ON sync_events (store_id) WHERE store_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_sync_events_store;
ALTER TABLE sync_events DROP COLUMN IF EXISTS store_id;
DROP INDEX IF EXISTS idx_device_store_bindings_store;
DROP TABLE IF EXISTS device_store_bindings;
DROP TABLE IF EXISTS stores;
