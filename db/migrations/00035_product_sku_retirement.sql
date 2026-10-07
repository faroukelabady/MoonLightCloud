-- +goose Up
-- Phase 17-R0 Product SKU retirement (ADR-0049): Product carries NO SKU
-- authority anywhere — not in the Cloud projection and not in the API.
-- ProductVariant owns SKU/stock (00033); product stock is derived only
-- (SUM of active variant stock at read time, never stored here).
--
-- catalog_products.sku was product-identity baggage: the column (and the
-- Store-scoped UNIQUE (store_id, sku) from 00023 plus idx_catalog_products_sku
-- from 00009) let a Product claim a SKU identity that collided with
-- variant SKU ownership. Dropping the column drops its constraints and
-- index together.
--
-- Historical sale-line snapshot SKUs (sale_lines_projection.sku and the
-- return/order equivalents) are immutable sale-time history and are
-- deliberately untouched: reporting over past purchases stays truthful.
ALTER TABLE catalog_products DROP COLUMN sku;

-- +goose Down
-- Development/test repair only. Retired SKU values and their Store-scoped
-- uniqueness are NOT recoverable (projections are rebuildable from the
-- immutable sync_events stream; the event payloads still carry the value).
-- The column therefore comes back NULLABLE and NON-UNIQUE — a re-projection
-- may repopulate display values, but uniqueness is never re-asserted here.
ALTER TABLE catalog_products ADD COLUMN sku TEXT;
