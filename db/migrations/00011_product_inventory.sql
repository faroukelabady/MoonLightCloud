-- +goose Up
-- Phase 5C: product inventory projection (ADR in docs/decisions).
-- Retail owns physical stock; Cloud converges to the highest valid
-- per-product revision as last-known inventory. No movements, no
-- reservations, no availability writes, no provider identity anywhere in
-- this projection. Rebuild: clear this table and reset the inventory
-- processor; inbox history replays deterministically (highest valid
-- revision wins regardless of order).

CREATE TABLE catalog_product_inventory (
    product_id              UUID PRIMARY KEY REFERENCES catalog_products(product_id) ON DELETE CASCADE,
    stock_quantity          BIGINT NOT NULL CHECK (stock_quantity >= 0),
    source_revision         BIGINT NOT NULL CHECK (source_revision >= 1),
    source_event_id         UUID NOT NULL REFERENCES sync_events(event_id),
    source_device_id        UUID NOT NULL REFERENCES devices(id),
    source_payload_hash     BYTEA NOT NULL,
    source_received_at      TIMESTAMPTZ NOT NULL,
    projected_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS catalog_product_inventory;
