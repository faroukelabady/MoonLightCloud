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
LEFT JOIN catalog_product_sales_policies pol ON pol.product_id = p.product_id
LEFT JOIN catalog_product_inventory inv ON inv.product_id = p.product_id
WHERE p.product_id = $1;
