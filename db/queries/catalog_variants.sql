-- Phase 17 product & physical variant projections (SKU/inventory
-- ownership on ProductVariant). Current-state replace semantics per
-- stream: one variant row carrying the highest valid variant_revision
-- (catalog stream), one inventory row carrying the highest valid
-- inventory_revision (inventory stream); attribute values are replaced
-- transactionally per variant revision. Store ownership follows the 00023
-- model (see UpsertCatalogCategory): NULL = unscoped legacy, COALESCE
-- preserves proven ownership, cross-Store writes are fenced in Go before
-- any upsert runs.

-- name: UpsertCatalogProductVariant :exec
-- Phase 9B store ownership semantics: see UpsertCatalogCategory.
INSERT INTO catalog_product_variants (
    variant_id, product_id, sku, is_active, deleted,
    price_egp_cents, price_usd_cents, position, combination_key,
    variant_revision, catalog_revision,
    source_event_id, source_device_id, source_payload_hash, source_received_at, store_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
ON CONFLICT (variant_id) DO UPDATE SET
    product_id = excluded.product_id, sku = excluded.sku,
    is_active = excluded.is_active, deleted = excluded.deleted,
    price_egp_cents = excluded.price_egp_cents, price_usd_cents = excluded.price_usd_cents,
    position = excluded.position, combination_key = excluded.combination_key,
    variant_revision = excluded.variant_revision, catalog_revision = excluded.catalog_revision,
    source_event_id = excluded.source_event_id, source_device_id = excluded.source_device_id,
    source_payload_hash = excluded.source_payload_hash, source_received_at = excluded.source_received_at,
    store_id = COALESCE(excluded.store_id, catalog_product_variants.store_id),
    projected_at = now();

-- name: CatalogProductVariantByID :one
SELECT variant_id, product_id, sku, is_active, deleted,
    price_egp_cents, price_usd_cents, position, combination_key,
    variant_revision, catalog_revision,
    source_event_id, source_device_id, source_payload_hash, store_id
FROM catalog_product_variants WHERE variant_id = $1;

-- name: CatalogProductVariantsByProduct :many
-- Tombstones stay queryable for historical safety (callers decide).
SELECT variant_id, product_id, sku, is_active, deleted,
    price_egp_cents, price_usd_cents, position, combination_key,
    variant_revision, catalog_revision,
    source_event_id, source_device_id, source_payload_hash, store_id
FROM catalog_product_variants WHERE product_id = $1
ORDER BY position, variant_id;

-- name: DeleteCatalogProductVariantAttributes :exec
DELETE FROM catalog_product_variant_attribute_values WHERE variant_id = $1;

-- name: InsertCatalogProductVariantAttribute :exec
INSERT INTO catalog_product_variant_attribute_values (
    variant_id, definition_code, value_code,
    name_ar, name_en, definition_name_ar, definition_name_en, position
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (variant_id, definition_code) DO UPDATE SET
    value_code = excluded.value_code, name_ar = excluded.name_ar, name_en = excluded.name_en,
    definition_name_ar = excluded.definition_name_ar, definition_name_en = excluded.definition_name_en,
    position = excluded.position;

-- name: CatalogProductVariantAttributes :many
SELECT definition_code, value_code, name_ar, name_en, definition_name_ar, definition_name_en, position
FROM catalog_product_variant_attribute_values WHERE variant_id = $1
ORDER BY position, definition_code;

-- name: UpsertCatalogProductVariantInventory :exec
-- Phase 9B store ownership semantics: see UpsertCatalogCategory. The
-- policy/catalog mirror columns are display + effective-cap context
-- only; source_revision is this stream's inventory_revision gate.
INSERT INTO catalog_product_variant_inventory (
    variant_id, product_id, sku, stock_quantity,
    ready, sell_online, online_allocation_limit,
    policy_revision, catalog_revision, source_revision,
    source_event_id, source_device_id, source_payload_hash, source_received_at, store_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
ON CONFLICT (variant_id) DO UPDATE SET
    product_id = excluded.product_id, sku = excluded.sku,
    stock_quantity = excluded.stock_quantity,
    ready = excluded.ready, sell_online = excluded.sell_online,
    online_allocation_limit = excluded.online_allocation_limit,
    policy_revision = excluded.policy_revision, catalog_revision = excluded.catalog_revision,
    source_revision = excluded.source_revision,
    source_event_id = excluded.source_event_id, source_device_id = excluded.source_device_id,
    source_payload_hash = excluded.source_payload_hash, source_received_at = excluded.source_received_at,
    store_id = COALESCE(excluded.store_id, catalog_product_variant_inventory.store_id),
    projected_at = now();

-- name: CatalogProductVariantInventoryByID :one
SELECT variant_id, product_id, sku, stock_quantity,
    ready, sell_online, online_allocation_limit,
    policy_revision, catalog_revision, source_revision,
    source_event_id, source_device_id, source_payload_hash,
    source_received_at, projected_at, store_id
FROM catalog_product_variant_inventory WHERE variant_id = $1;

-- Phase 9-R1 F02 mirror: proven Store ownership of a variant's durable
-- dependents (inventory + provider mappings). Adopting a variant into a
-- Store must never leave a dependent owned by another proven Store.

-- name: CatalogProductVariantDependentStores :many
SELECT i.store_id FROM catalog_product_variant_inventory i
WHERE i.variant_id = $1 AND i.store_id IS NOT NULL
UNION
SELECT m.store_id FROM commerce_product_variant_mappings m
WHERE m.variant_id = $1 AND m.store_id IS NOT NULL;

-- name: CatalogVariantAvailabilityByVariantID :one
-- Per-variant ONLINE availability inputs (Phase 17). Centered on the
-- variant: a dependency may back the variant only when it is unscoped
-- legacy (NULL) or owned by the variant's exact proven Store, mirroring
-- CatalogAvailabilityByProductID (a contradictory durable relationship
-- can never expose foreign stock or policy as positive availability).
SELECT
    v.variant_id AS variant_id,
    v.product_id AS product_id,
    v.is_active AS variant_active,
    v.deleted AS variant_deleted,
    v.variant_revision AS variant_revision,
    v.sku AS variant_sku,
    p.is_active AS product_active,
    p.source_revision AS product_revision,
    pol.sell_online AS sell_online,
    pol.online_allocation_limit AS online_allocation_limit,
    pol.source_revision AS policy_revision,
    inv.stock_quantity AS stock_quantity,
    inv.sell_online AS variant_sell_online,
    inv.online_allocation_limit AS variant_allocation_limit,
    inv.ready AS variant_ready,
    inv.source_revision AS inventory_revision,
    inv.source_event_id AS inventory_event_id,
    inv.projected_at AS inventory_projected_at
FROM catalog_product_variants v
LEFT JOIN catalog_products p
  ON p.product_id = v.product_id
 AND (p.store_id IS NULL OR p.store_id = v.store_id)
LEFT JOIN catalog_product_sales_policies pol
  ON pol.product_id = v.product_id
 AND (pol.store_id IS NULL OR pol.store_id = v.store_id)
LEFT JOIN catalog_product_variant_inventory inv
  ON inv.variant_id = v.variant_id
 AND (inv.store_id IS NULL OR inv.store_id = v.store_id)
WHERE v.variant_id = $1;
