-- +goose Up
-- Phase 17 order-line variant identity snapshot (additive): ONLINE order
-- lines gain the immutable resolved ProductVariant identity captured AT
-- INGESTION, mirroring the Phase 15 configuration selection snapshots
-- (00030). Current catalog rows are NEVER authority for past purchases:
-- the variant SKU and attribute labels/deltas are copied into the line,
-- so later rename/price/disable/deletion can never rewrite history.
--
--   variant_id                  — resolved commerce_product_variant_mappings
--                                 identity (nullable: lines without a
--                                 provider variation, or unresolved
--                                 provider variations, stay truthful NULL).
--   variant_sku                 — SKU snapshot at ingestion.
--   variant_attribute_snapshot — JSONB snapshot of the variant's option
--                                 attributes (definition/value codes and
--                                 bilingual labels) at ingestion. Display
--                                 data only; no provider identity, no money
--                                 math (BIGINT columns own money).
--
-- No foreign keys on purpose (00030 style): snapshots must survive
-- catalog rebuilds and variant tombstones.

ALTER TABLE commerce_online_order_lines ADD COLUMN variant_id UUID NULL;
ALTER TABLE commerce_online_order_lines ADD COLUMN variant_sku TEXT NULL;
ALTER TABLE commerce_online_order_lines ADD COLUMN variant_attribute_snapshot JSONB NULL;

CREATE INDEX idx_commerce_online_order_lines_variant
    ON commerce_online_order_lines (variant_id)
    WHERE variant_id IS NOT NULL;

-- +goose Down
-- Development/test repair only: captured variant snapshots are immutable
-- history. Never roll back in production.
DROP INDEX IF EXISTS idx_commerce_online_order_lines_variant;
ALTER TABLE commerce_online_order_lines DROP COLUMN IF EXISTS variant_attribute_snapshot;
ALTER TABLE commerce_online_order_lines DROP COLUMN IF EXISTS variant_sku;
ALTER TABLE commerce_online_order_lines DROP COLUMN IF EXISTS variant_id;
