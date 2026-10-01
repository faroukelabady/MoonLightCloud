-- +goose Up
-- Phase 9C store-scoped commerce ownership. Closes the integration-state
-- boundary Phase 9B intentionally left unscoped: provider mappings and
-- online-order roots carry the proven Store of their authoritative
-- MoonLight product(s). NULL = unscoped legacy (ownership not proven),
-- never backfilled by migration. Adoption happens only through live
-- deterministic rules (mapping: same product whose Product adopted;
-- orders: unanimous mapped-line ownership at reconciliation).

-- Commerce product mappings: ownership mirrors the authoritative
-- catalog product. Same-pair identity stays (provider_key, product_id);
-- same external identity stays unique per provider.
ALTER TABLE commerce_product_mappings ADD COLUMN store_id UUID REFERENCES stores(id) ON DELETE RESTRICT;
CREATE INDEX idx_commerce_product_mappings_store ON commerce_product_mappings (store_id) WHERE store_id IS NOT NULL;
CREATE INDEX idx_commerce_product_mappings_store_provider ON commerce_product_mappings (store_id, provider_key) WHERE store_id IS NOT NULL;

-- Online order roots: ownership derives at reconciliation from unanimous
-- resolved mapping ownership. Children (lines, addresses, history) stay
-- FK-owned by the root; webhook inbox, fences, and status history carry
-- no Store (operational tokens and append-only history, never authority).
ALTER TABLE commerce_online_orders ADD COLUMN store_id UUID REFERENCES stores(id) ON DELETE RESTRICT;
CREATE INDEX idx_commerce_online_orders_store_created ON commerce_online_orders (store_id, created_at DESC) WHERE store_id IS NOT NULL;
CREATE INDEX idx_commerce_online_orders_store_provider_order ON commerce_online_orders (store_id, provider_key, external_order_id) WHERE store_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_commerce_online_orders_store_provider_order;
DROP INDEX IF EXISTS idx_commerce_online_orders_store_created;
ALTER TABLE commerce_online_orders DROP COLUMN IF EXISTS store_id;
DROP INDEX IF EXISTS idx_commerce_product_mappings_store_provider;
DROP INDEX IF EXISTS idx_commerce_product_mappings_store;
ALTER TABLE commerce_product_mappings DROP COLUMN IF EXISTS store_id;
