-- +goose Up
-- Phase 17-R0 immutable sale-line variant snapshots (sale.finalized.v3):
-- per-line ProductVariant identity captured AT SALE TIME, mirroring the
-- Phase 8C tag snapshots (00021) and the Phase 15/17 order-line snapshots
-- (00030/00034). Current catalog rows are NEVER authority for past
-- purchases: the variant SKU, its option attribute labels, and the
-- sale-time variant pricing override are copied into the line so later
-- rename/price/disable/deletion can never rewrite history. These columns
-- are sourced ONLY from the event snapshot — no projection or read path
-- may join catalog_product_variant_attribute_values (or any current-state
-- catalog table) to populate or refresh them.
--
--   variant_sku                — variant SKU snapshot at sale time
--                                (nullable: v1/v2 lines and lines without
--                                variant data stay truthful NULL).
--   variant_attributes         — bounded JSONB array of frozen option
--                                attributes: definition_code, value_code,
--                                name_ar, name_en, definition_name_ar,
--                                definition_name_en (labels frozen at sale
--                                time; display data only, never identity).
--   variant_price_egp_cents    — nullable sale-time variant pricing
--                                override (BIGINT minor units; no floats).
--   variant_price_usd_cents    — nullable sale-time variant pricing
--                                override (BIGINT minor units; no floats).
--
-- No foreign keys on purpose (00021/00030 style): snapshots must survive
-- catalog rebuilds, label renames, and variant tombstones.

ALTER TABLE sale_lines_projection ADD COLUMN variant_sku TEXT NULL;
ALTER TABLE sale_lines_projection ADD COLUMN variant_attributes JSONB NULL;
ALTER TABLE sale_lines_projection ADD COLUMN variant_price_egp_cents BIGINT NULL
    CHECK (variant_price_egp_cents IS NULL OR variant_price_egp_cents >= 0);
ALTER TABLE sale_lines_projection ADD COLUMN variant_price_usd_cents BIGINT NULL
    CHECK (variant_price_usd_cents IS NULL OR variant_price_usd_cents >= 0);
ALTER TABLE sale_lines_projection ADD CONSTRAINT sale_lines_variant_attributes_bounded
    CHECK (variant_attributes IS NULL OR (
        jsonb_typeof(variant_attributes) = 'array'
        AND jsonb_array_length(variant_attributes) <= 32));

CREATE INDEX idx_sale_lines_variant_sku
    ON sale_lines_projection (variant_sku)
    WHERE variant_sku IS NOT NULL;

-- +goose Down
-- Development/test repair only: captured variant snapshots are immutable
-- history. Never roll back in production.
DROP INDEX IF EXISTS idx_sale_lines_variant_sku;
ALTER TABLE sale_lines_projection DROP CONSTRAINT IF EXISTS sale_lines_variant_attributes_bounded;
ALTER TABLE sale_lines_projection DROP COLUMN IF EXISTS variant_price_usd_cents;
ALTER TABLE sale_lines_projection DROP COLUMN IF EXISTS variant_price_egp_cents;
ALTER TABLE sale_lines_projection DROP COLUMN IF EXISTS variant_attributes;
ALTER TABLE sale_lines_projection DROP COLUMN IF EXISTS variant_sku;
