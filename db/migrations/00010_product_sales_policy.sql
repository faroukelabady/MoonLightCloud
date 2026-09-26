-- +goose Up
-- Phase 5B: product sales-policy projection (ADR in docs/decisions).
-- Retail owns channel/allocation policy; Cloud converges to the highest
-- valid per-product revision. No stock, no availability, no reservations,
-- no provider identity anywhere in this projection. Rebuild: clear this
-- table and reset the policy processor; inbox history replays
-- deterministically (highest valid revision wins regardless of order).

CREATE TABLE catalog_product_sales_policies (
    product_id              UUID PRIMARY KEY REFERENCES catalog_products(product_id) ON DELETE CASCADE,
    sell_offline            BOOLEAN NOT NULL,
    sell_online             BOOLEAN NOT NULL,
    online_allocation_limit BIGINT CHECK (online_allocation_limit IS NULL OR online_allocation_limit >= 0),
    source_revision         BIGINT NOT NULL CHECK (source_revision >= 1),
    source_event_id         UUID NOT NULL REFERENCES sync_events(event_id),
    source_device_id        UUID NOT NULL REFERENCES devices(id),
    source_payload_hash     BYTEA NOT NULL,
    source_received_at      TIMESTAMPTZ NOT NULL,
    projected_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (sell_online OR online_allocation_limit IS NULL)
);
CREATE INDEX idx_catalog_policies_online ON catalog_product_sales_policies (sell_online) WHERE sell_online;

-- +goose Down
DROP TABLE IF EXISTS catalog_product_sales_policies;
