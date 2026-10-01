-- +goose Up
-- Phase 9B store-scoped projections. Copies the trusted Phase 9A ingress
-- Store context (sync_events.store_id) into projection roots so
-- cross-Store collisions are isolated instead of overwritten.
--
-- NULL = unscoped legacy (truthful: ownership not proven). Non-NULL =
-- proven Store ownership written only from the event's own ingress
-- context, never from current device bindings or payloads. No backfill:
-- every existing row keeps its values; only new scoped writes populate
-- these columns. SKU/slug uniqueness becomes Store-scoped (NULL legacy
-- rows never collide in PostgreSQL: NULL is never equal to NULL).

-- Catalog roots: nullable ownership until one-time legacy adoption.
ALTER TABLE catalog_categories ADD COLUMN store_id UUID REFERENCES stores(id) ON DELETE RESTRICT;
CREATE INDEX idx_catalog_categories_store ON catalog_categories (store_id) WHERE store_id IS NOT NULL;

ALTER TABLE catalog_tags ADD COLUMN store_id UUID REFERENCES stores(id) ON DELETE RESTRICT;
CREATE INDEX idx_catalog_tags_store ON catalog_tags (store_id) WHERE store_id IS NOT NULL;
-- Slug uniqueness is Store-local: same slug may coexist across Stores;
-- duplicates within one Store stay prohibited.
ALTER TABLE catalog_tags DROP CONSTRAINT catalog_tags_slug_key;
ALTER TABLE catalog_tags ADD CONSTRAINT catalog_tags_store_slug_unique UNIQUE (store_id, slug);

ALTER TABLE catalog_products ADD COLUMN store_id UUID REFERENCES stores(id) ON DELETE RESTRICT;
CREATE INDEX idx_catalog_products_store ON catalog_products (store_id) WHERE store_id IS NOT NULL;
-- SKU uniqueness is Store-local (Retail SKU uniqueness is Store-local).
ALTER TABLE catalog_products DROP CONSTRAINT catalog_products_sku_key;
ALTER TABLE catalog_products ADD CONSTRAINT catalog_products_store_sku_unique UNIQUE (store_id, sku);

-- Policy/inventory carry denormalized ownership alongside their product
-- (written in the same projector decision, never looked up later).
ALTER TABLE catalog_product_sales_policies ADD COLUMN store_id UUID REFERENCES stores(id) ON DELETE RESTRICT;
CREATE INDEX idx_catalog_product_sales_policies_store ON catalog_product_sales_policies (store_id) WHERE store_id IS NOT NULL;

ALTER TABLE catalog_product_inventory ADD COLUMN store_id UUID REFERENCES stores(id) ON DELETE RESTRICT;
CREATE INDEX idx_catalog_product_inventory_store ON catalog_product_inventory (store_id) WHERE store_id IS NOT NULL;

-- Historical roots: immutable once written; NULL stays NULL for legacy.
ALTER TABLE sales_projection ADD COLUMN store_id UUID REFERENCES stores(id) ON DELETE RESTRICT;
CREATE INDEX idx_sales_projection_store_occurred ON sales_projection (store_id, occurred_at) WHERE store_id IS NOT NULL;

ALTER TABLE return_refund_projection ADD COLUMN store_id UUID REFERENCES stores(id) ON DELETE RESTRICT;
CREATE INDEX idx_return_refund_projection_store_occurred ON return_refund_projection (store_id, occurred_at) WHERE store_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_return_refund_projection_store_occurred;
ALTER TABLE return_refund_projection DROP COLUMN IF EXISTS store_id;
DROP INDEX IF EXISTS idx_sales_projection_store_occurred;
ALTER TABLE sales_projection DROP COLUMN IF EXISTS store_id;
DROP INDEX IF EXISTS idx_catalog_product_inventory_store;
ALTER TABLE catalog_product_inventory DROP COLUMN IF EXISTS store_id;
DROP INDEX IF EXISTS idx_catalog_product_sales_policies_store;
ALTER TABLE catalog_product_sales_policies DROP COLUMN IF EXISTS store_id;
ALTER TABLE catalog_products DROP CONSTRAINT IF EXISTS catalog_products_store_sku_unique;
ALTER TABLE catalog_products ADD CONSTRAINT catalog_products_sku_key UNIQUE (sku);
DROP INDEX IF EXISTS idx_catalog_products_store;
ALTER TABLE catalog_products DROP COLUMN IF EXISTS store_id;
ALTER TABLE catalog_tags DROP CONSTRAINT IF EXISTS catalog_tags_store_slug_unique;
ALTER TABLE catalog_tags ADD CONSTRAINT catalog_tags_slug_key UNIQUE (slug);
DROP INDEX IF EXISTS idx_catalog_tags_store;
ALTER TABLE catalog_tags DROP COLUMN IF EXISTS store_id;
DROP INDEX IF EXISTS idx_catalog_categories_store;
ALTER TABLE catalog_categories DROP COLUMN IF EXISTS store_id;
