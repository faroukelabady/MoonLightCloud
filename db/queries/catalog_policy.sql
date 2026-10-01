-- Phase 5B product sales-policy projection. Current-state replace
-- semantics per product: one row carrying the highest valid revision.
-- Depends on the core product projection (dependency wait, never terminal
-- for missing products); inactive products still project policy rows.

-- name: UpsertCatalogProductSalesPolicy :exec
-- Phase 9B store ownership: see UpsertCatalogCategory.
INSERT INTO catalog_product_sales_policies (
    product_id, sell_offline, sell_online, online_allocation_limit,
    source_revision, source_event_id, source_device_id,
    source_payload_hash, source_received_at, store_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (product_id) DO UPDATE SET
    sell_offline = excluded.sell_offline, sell_online = excluded.sell_online,
    online_allocation_limit = excluded.online_allocation_limit,
    source_revision = excluded.source_revision, source_event_id = excluded.source_event_id,
    source_device_id = excluded.source_device_id, source_payload_hash = excluded.source_payload_hash,
    source_received_at = excluded.source_received_at,
    store_id = COALESCE(excluded.store_id, catalog_product_sales_policies.store_id),
    projected_at = now();

-- name: CatalogProductSalesPolicyByID :one
SELECT product_id, sell_offline, sell_online, online_allocation_limit,
    source_revision, source_event_id, source_device_id, source_payload_hash, store_id
FROM catalog_product_sales_policies WHERE product_id = $1;

-- name: CatalogOnlineConfiguredProducts :many
SELECT product_id, sell_offline, sell_online, online_allocation_limit, source_revision
FROM catalog_product_sales_policies
WHERE sell_online ORDER BY product_id
LIMIT $1;
