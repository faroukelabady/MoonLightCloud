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

-- name: CatalogOnlineConfiguredProductsForStore :many
-- Phase 9C Store-aware publication enumeration: online-configured
-- products of exactly one proven Store through canonical projected
-- state. Legacy NULL rows never match. The global variant above stays
-- for administration/diagnostics and must not feed Store-specific
-- provider writes.
-- Phase 9-R1 F02: the policy's Store must also be the owning product's
-- Store, so a contradictory product/policy pair can never be enumerated
-- (and published) as another Store's resource.
SELECT pol.product_id, pol.sell_offline, pol.sell_online, pol.online_allocation_limit, pol.source_revision
FROM catalog_product_sales_policies pol
JOIN catalog_products p ON p.product_id = pol.product_id
WHERE pol.sell_online AND pol.store_id = $1
  AND p.store_id = pol.store_id
ORDER BY pol.product_id
LIMIT $2;

-- Phase 13 §62: canonical effective-online policy reads. The eligibility
-- rule lives in ONE place — the catalog_product_online_state view
-- (migration 00028) — and is consumed by commerce publication and
-- Catalog Health alike; no provider adapter ever traverses the DAG.

-- name: CatalogProductOnlineState :one
SELECT product_id, store_id, category_allows_online, block_reason, blocking_category_id, policy_fingerprint, policy_version
FROM catalog_product_online_state
WHERE product_id = $1;

-- name: CatalogProductsOnlineState :many
SELECT product_id, store_id, category_allows_online, block_reason, blocking_category_id, policy_fingerprint, policy_version
FROM catalog_product_online_state
WHERE product_id = ANY($1::uuid[]);
