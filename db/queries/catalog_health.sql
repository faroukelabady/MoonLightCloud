-- Phase 12 — Catalog Health + online-order operational analytics.
--
-- Everything here is READ-ONLY durable MoonLight state. No provider is
-- contacted live; no barrier is settled, cleared or modified; no Store
-- adoption or reconciliation ever runs from these reads.

-- name: DashboardOrderAnalytics :many
-- Operational provider-order aggregation (Phase 12). This is NOT
-- financial revenue: provider online orders are operational commerce
-- truth and are never added to canonical finalized Retail Sales. Groups
-- durable online-order rows by provider, canonical status and currency
-- over the report window; currencies are never summed together. The
-- optional provider filter ('' = all providers) is a narrowing filter
-- only — historical rows stay queryable when a provider is disabled.
SELECT
    provider_key,
    canonical_status,
    currency,
    count(*)::bigint AS orders_count,
    COALESCE(sum(total_minor), 0)::bigint AS value_minor
FROM commerce_online_orders
WHERE created_at >= @start_utc
  AND created_at < @end_utc
  AND (@provider_key::text = '' OR provider_key = @provider_key::text)
  AND (@store_id::text = '' OR store_id = @store_id::uuid)
  AND (@currency::text = '' OR currency = @currency::text)
GROUP BY provider_key, canonical_status, currency;

-- name: CatalogHealthSummary :many
-- Bounded summary counts per stable health reason code. Exact predicates
-- (documented in docs/decisions/0045 and tested):
--   CATALOG_MISSING_SKU       — projected SKU absent/blank (missing only;
--                               legacy stored SKUs are preserved, never
--                               regex-rejected).
--   CATALOG_MISSING_CATEGORY  — required root/top category absent or
--                               unresolved (optional subcategories and
--                               hidden categories are NOT failures).
--   AVAILABILITY_NOT_READY    — online-eligible product (active +
--                               sell_online) with no projected inventory
--                               row (frozen readiness: product + policy
--                               + inventory), excluding products
--                               INTENTIONALLY Category-suppressed
--                               (Phase 13 §169: deliberate suppression is
--                               never fabricated as not-ready; missing
--                               hierarchy state still reports not-ready
--                               alongside CATALOG_MISSING_CATEGORY).
--   COMMERCE_MAPPING_MISSING  — EFFECTIVELY online-eligible product with
--                               no mapping for a known provider. Uses
--                               the same canonical eligibility rule as
--                               commerce publication (the
--                               catalog_product_online_state view):
--                               intentionally offline/inactive AND
--                               Category-suppressed products are never
--                               flagged (Phase 13 §113/§114).
--   CATEGORY_ONLINE_DISABLED  — INFORMATIONAL (Phase 13 §116): active +
--                               sell_online Product intentionally
--                               suppressed by Category policy. Not a
--                               catalog-health failure — it is deliberate
--                               configuration.
--   COMMERCE_SYNC_AMBIGUOUS   — an unresolved mutation barrier exists for
--                               the product (needs operator settlement;
--                               read-only, never settled here).
--   COMMERCE_STORE_CONFLICT   — durable mapping Store ownership differs
--                               from the catalog product's Store.
-- Provider-scoped reasons are computed against the durable provider
-- universe (provider keys observed in mappings or barriers).
SELECT reason_code, count(*)::bigint AS products
FROM (
    SELECT 'CATALOG_MISSING_SKU' AS reason_code
    FROM catalog_products p
    WHERE (p.sku IS NULL OR btrim(p.sku) = '')
      AND (@store_id::text = '' OR p.store_id = @store_id::uuid)
    UNION ALL
    SELECT 'CATALOG_MISSING_CATEGORY'
    FROM catalog_products p
    WHERE (p.top_category_id IS NULL OR
           NOT EXISTS (SELECT 1 FROM catalog_categories c WHERE c.category_id = p.top_category_id))
      AND (@store_id::text = '' OR p.store_id = @store_id::uuid)
    UNION ALL
    SELECT 'AVAILABILITY_NOT_READY'
    FROM catalog_products p
    JOIN catalog_product_sales_policies pol ON pol.product_id = p.product_id
    JOIN catalog_product_online_state s ON s.product_id = p.product_id
    WHERE p.is_active AND pol.sell_online
      AND s.block_reason <> 'CATEGORY_ONLINE_DISABLED'
      AND NOT EXISTS (SELECT 1 FROM catalog_product_inventory i WHERE i.product_id = p.product_id)
      AND (@store_id::text = '' OR p.store_id = @store_id::uuid)
    UNION ALL
    SELECT 'COMMERCE_MAPPING_MISSING'
    FROM catalog_products p
    JOIN catalog_product_sales_policies pol ON pol.product_id = p.product_id
    JOIN (
        SELECT DISTINCT provider_key FROM commerce_product_mappings
        UNION
        SELECT DISTINCT provider_key FROM commerce_product_mutation_barriers
    ) u ON (@provider_key::text = '' OR u.provider_key = @provider_key::text)
    JOIN catalog_product_online_state s ON s.product_id = p.product_id
    WHERE p.is_active AND pol.sell_online AND s.category_allows_online
      AND (@store_id::text = '' OR p.store_id = @store_id::uuid)
      AND NOT EXISTS (SELECT 1 FROM commerce_product_mappings m
                      WHERE m.provider_key = u.provider_key AND m.product_id = p.product_id)
    UNION ALL
    SELECT 'CATEGORY_ONLINE_DISABLED'
    FROM catalog_products p
    JOIN catalog_product_sales_policies pol ON pol.product_id = p.product_id
    JOIN catalog_product_online_state s ON s.product_id = p.product_id
    WHERE p.is_active AND pol.sell_online
      AND NOT s.category_allows_online AND s.block_reason = 'CATEGORY_ONLINE_DISABLED'
      AND (@store_id::text = '' OR p.store_id = @store_id::uuid)
    UNION ALL
    SELECT 'COMMERCE_SYNC_AMBIGUOUS'
    FROM commerce_product_mutation_barriers b
    JOIN catalog_products p ON p.product_id = b.product_id
    WHERE b.state IN ('in_flight', 'uncertain')
      AND (@provider_key::text = '' OR b.provider_key = @provider_key::text)
      AND (@store_id::text = '' OR p.store_id = @store_id::uuid)
    UNION ALL
    SELECT 'COMMERCE_STORE_CONFLICT'
    FROM commerce_product_mappings m
    JOIN catalog_products p ON p.product_id = m.product_id
    WHERE m.store_id IS NOT NULL AND p.store_id IS NOT NULL
      AND m.store_id <> p.store_id
      AND (@provider_key::text = '' OR m.provider_key = @provider_key::text)
      AND (@store_id::text = '' OR p.store_id = @store_id::uuid)
) reasons
GROUP BY reason_code;

-- name: CatalogHealthDetail :many
-- Bounded diagnostic detail rows for one stable reason code (or all).
-- Row identity only: no raw provider errors, no credentials, no SQL.
SELECT reason_code, provider_key, product_id, sku, name, store_id
FROM (
    SELECT 'CATALOG_MISSING_SKU' AS reason_code, ''::text AS provider_key,
           p.product_id, p.sku, p.name, p.store_id
    FROM catalog_products p
    WHERE (p.sku IS NULL OR btrim(p.sku) = '')
      AND (@store_id::text = '' OR p.store_id = @store_id::uuid)
    UNION ALL
    SELECT 'CATALOG_MISSING_CATEGORY', ''::text,
           p.product_id, p.sku, p.name, p.store_id
    FROM catalog_products p
    WHERE (p.top_category_id IS NULL OR
           NOT EXISTS (SELECT 1 FROM catalog_categories c WHERE c.category_id = p.top_category_id))
      AND (@store_id::text = '' OR p.store_id = @store_id::uuid)
    UNION ALL
    SELECT 'AVAILABILITY_NOT_READY', ''::text,
           p.product_id, p.sku, p.name, p.store_id
    FROM catalog_products p
    JOIN catalog_product_sales_policies pol ON pol.product_id = p.product_id
    JOIN catalog_product_online_state s ON s.product_id = p.product_id
    WHERE p.is_active AND pol.sell_online
      AND s.block_reason <> 'CATEGORY_ONLINE_DISABLED'
      AND NOT EXISTS (SELECT 1 FROM catalog_product_inventory i WHERE i.product_id = p.product_id)
      AND (@store_id::text = '' OR p.store_id = @store_id::uuid)
    UNION ALL
    SELECT 'COMMERCE_MAPPING_MISSING', u.provider_key,
           p.product_id, p.sku, p.name, p.store_id
    FROM catalog_products p
    JOIN catalog_product_sales_policies pol ON pol.product_id = p.product_id
    JOIN (
        SELECT DISTINCT provider_key FROM commerce_product_mappings
        UNION
        SELECT DISTINCT provider_key FROM commerce_product_mutation_barriers
    ) u ON (@provider_key::text = '' OR u.provider_key = @provider_key::text)
    JOIN catalog_product_online_state s ON s.product_id = p.product_id
    WHERE p.is_active AND pol.sell_online AND s.category_allows_online
      AND (@store_id::text = '' OR p.store_id = @store_id::uuid)
      AND NOT EXISTS (SELECT 1 FROM commerce_product_mappings m
                      WHERE m.provider_key = u.provider_key AND m.product_id = p.product_id)
    UNION ALL
    SELECT 'CATEGORY_ONLINE_DISABLED', ''::text,
           p.product_id, p.sku, p.name, p.store_id
    FROM catalog_products p
    JOIN catalog_product_sales_policies pol ON pol.product_id = p.product_id
    JOIN catalog_product_online_state s ON s.product_id = p.product_id
    WHERE p.is_active AND pol.sell_online
      AND NOT s.category_allows_online AND s.block_reason = 'CATEGORY_ONLINE_DISABLED'
      AND (@store_id::text = '' OR p.store_id = @store_id::uuid)
    UNION ALL
    SELECT 'COMMERCE_SYNC_AMBIGUOUS', b.provider_key,
           p.product_id, p.sku, p.name, p.store_id
    FROM commerce_product_mutation_barriers b
    JOIN catalog_products p ON p.product_id = b.product_id
    WHERE b.state IN ('in_flight', 'uncertain')
      AND (@provider_key::text = '' OR b.provider_key = @provider_key::text)
      AND (@store_id::text = '' OR p.store_id = @store_id::uuid)
    UNION ALL
    SELECT 'COMMERCE_STORE_CONFLICT', m.provider_key,
           p.product_id, p.sku, p.name, p.store_id
    FROM commerce_product_mappings m
    JOIN catalog_products p ON p.product_id = m.product_id
    WHERE m.store_id IS NOT NULL AND p.store_id IS NOT NULL
      AND m.store_id <> p.store_id
      AND (@provider_key::text = '' OR m.provider_key = @provider_key::text)
      AND (@store_id::text = '' OR p.store_id = @store_id::uuid)
) details
WHERE (@reason::text = '' OR reason_code = @reason::text)
ORDER BY reason_code, provider_key, sku, product_id
LIMIT @limit_n::int;

-- name: CatalogHealthProviders :many
-- Same complete durable provider universe as mapping-health predicates.
-- A specific Store must have projected Products; unknown Store never falls
-- back to a global list. Choices ignore provider selection/detail truncation.
SELECT provider_key FROM (
 SELECT provider_key FROM commerce_product_mappings
 UNION
 SELECT provider_key FROM commerce_product_mutation_barriers
) providers
WHERE (@store_id::text = '' OR EXISTS (
 SELECT 1 FROM catalog_products p WHERE p.store_id = @store_id::uuid
))
ORDER BY provider_key;
