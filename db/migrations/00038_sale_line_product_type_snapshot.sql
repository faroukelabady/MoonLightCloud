-- +goose Up
-- 00038_sale_line_product_type_snapshot: Phase 17-R2 sale-time ProductType
-- snapshot (ADR-039 §26, ADR-0050). Mirrors 00036 (variant snapshots):
-- per-line ProductType identity captured AT SALE TIME from the
-- sale.finalized.v3 event. Current catalog_product_types rows are NEVER
-- authority for past purchases: later type renames or Product
-- reassignments can never rewrite history. Sourced ONLY from the event
-- snapshot — no projection or read path may join current-state catalog
-- tables to populate or refresh these columns.

ALTER TABLE sale_lines_projection
    ADD COLUMN product_type_id TEXT,
    ADD COLUMN product_type_code TEXT CHECK (product_type_code IS NULL OR char_length(product_type_code) BETWEEN 1 AND 32),
    ADD COLUMN product_type_name_ar TEXT,
    ADD COLUMN product_type_name_en TEXT;

-- +goose Down
-- Development/test repair only. Never roll back in production.
ALTER TABLE sale_lines_projection
    DROP COLUMN IF EXISTS product_type_id,
    DROP COLUMN IF EXISTS product_type_code,
    DROP COLUMN IF EXISTS product_type_name_ar,
    DROP COLUMN IF EXISTS product_type_name_en;
