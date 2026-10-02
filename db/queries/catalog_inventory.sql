-- Phase 5C product inventory projection. Current-state replace semantics
-- per product: one row carrying the highest valid revision. Depends on the
-- core product projection (dependency wait, never terminal for missing
-- products); inactive products and sell_online=false still project rows.

-- name: UpsertCatalogProductInventory :exec
-- Phase 9B store ownership: see UpsertCatalogCategory.
INSERT INTO catalog_product_inventory (
    product_id, stock_quantity,
    source_revision, source_event_id, source_device_id,
    source_payload_hash, source_received_at, store_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (product_id) DO UPDATE SET
    stock_quantity = excluded.stock_quantity,
    source_revision = excluded.source_revision, source_event_id = excluded.source_event_id,
    source_device_id = excluded.source_device_id, source_payload_hash = excluded.source_payload_hash,
    source_received_at = excluded.source_received_at,
    store_id = COALESCE(excluded.store_id, catalog_product_inventory.store_id),
    projected_at = now();

-- name: CatalogProductInventoryByID :one
SELECT product_id, stock_quantity,
    source_revision, source_event_id, source_device_id, source_payload_hash,
    source_received_at, projected_at, store_id
FROM catalog_product_inventory WHERE product_id = $1;

-- name: CatalogAvailabilityByProductID :one
SELECT
    p.product_id AS product_id,
    p.is_active AS product_active,
    p.source_revision AS product_revision,
    pol.sell_offline AS sell_offline,
    pol.sell_online AS sell_online,
    pol.online_allocation_limit AS online_allocation_limit,
    pol.source_revision AS policy_revision,
    inv.stock_quantity AS stock_quantity,
    inv.source_revision AS inventory_revision,
    inv.source_event_id AS inventory_event_id,
    inv.projected_at AS inventory_projected_at
FROM catalog_products p
-- Phase 9-R1 F02: a dependency may back the product only when it is
-- either unscoped legacy (NULL) or owned by the product's exact proven
-- Store. A proven dependency from any other Store is excluded, so a
-- contradictory durable relationship cannot expose foreign stock or
-- policy as positive availability.
-- Phase 9-R2: the same rule applies to the product's referenced
-- categories and tags, which may be Store-scoped canonical rows.
LEFT JOIN catalog_product_sales_policies pol
  ON pol.product_id = p.product_id
 AND (pol.store_id IS NULL OR pol.store_id = p.store_id)
LEFT JOIN catalog_product_inventory inv
  ON inv.product_id = p.product_id
 AND (inv.store_id IS NULL OR inv.store_id = p.store_id)
WHERE p.product_id = $1
  AND EXISTS (
    SELECT 1 FROM catalog_categories c
    WHERE c.category_id = p.top_category_id
      AND (c.store_id IS NULL OR c.store_id = p.store_id))
  AND NOT EXISTS (
    SELECT 1 FROM catalog_product_subcategories s
    JOIN catalog_categories c ON c.category_id = s.category_id
    WHERE s.product_id = p.product_id
      AND c.store_id IS NOT NULL AND c.store_id <> p.store_id)
  AND NOT EXISTS (
    SELECT 1 FROM catalog_product_tags t
    JOIN catalog_tags tg ON tg.tag_id = t.tag_id
    WHERE t.product_id = p.product_id
      AND tg.store_id IS NOT NULL AND tg.store_id <> p.store_id);
