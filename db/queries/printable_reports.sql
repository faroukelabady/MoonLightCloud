-- Printable reports (dashboard Reports page). These reads return facts at
-- sale-line grain; internal/salesreport converts currencies and aggregates
-- in Go with exact integer rounding, so every section shares one rule.
--
-- Scope: sqlc.narg('store_id') NULL means every Store (the HTTP boundary
-- only allows that for all-Stores principals); otherwise exactly one Store.
-- The "sale set" is every Sale paid in the period plus every Sale that a
-- return in the period refers to, so returned lines keep their category,
-- tags and cost even when the Sale itself falls outside the period.

-- name: PrintableReportSales :many
WITH sale_set AS (
    SELECT s.sale_id FROM sales_projection s
    WHERE s.occurred_at >= @start_utc AND s.occurred_at < @end_utc
      AND (sqlc.narg('store_id')::uuid IS NULL OR s.store_id = sqlc.narg('store_id')::uuid)
    UNION
    SELECT r.sale_id FROM return_refund_projection r
    WHERE r.occurred_at >= @start_utc AND r.occurred_at < @end_utc
      AND (sqlc.narg('store_id')::uuid IS NULL OR r.store_id = sqlc.narg('store_id')::uuid)
)
SELECT s.sale_id, s.sale_number, s.occurred_at, s.currency,
    s.subtotal_minor, s.discount_minor, s.tax_minor, s.total_minor,
    s.fx_rate_microrate,
    (s.occurred_at >= @start_utc AND s.occurred_at < @end_utc)::boolean AS in_period,
    -- USD->EGP rate for showing an EGP Sale in USD: the Cloud rate in
    -- effect at the Sale (else the earliest Cloud rate); without Cloud
    -- rates, the latest USD Sale snapshot of the same Store at or before
    -- the Sale, else the earliest after it. 0 when none exists.
    COALESCE(CASE WHEN @with_usd_rates::boolean AND s.currency = 'EGP' THEN COALESCE(
        (SELECT x.rate_microrate FROM exchange_rates x
         WHERE x.base = 'USD' AND x.quote = 'EGP'
         ORDER BY (x.effective_at <= s.occurred_at) DESC,
                  CASE WHEN x.effective_at <= s.occurred_at THEN x.effective_at END DESC,
                  x.effective_at, x.id
         LIMIT 1),
        (SELECT r.fx_rate_microrate FROM sales_projection r
         WHERE r.store_id = s.store_id AND r.currency = 'USD' AND r.fx_rate_microrate IS NOT NULL
         ORDER BY (r.occurred_at <= s.occurred_at) DESC,
                  abs(extract(epoch FROM r.occurred_at - s.occurred_at)), r.sale_id
         LIMIT 1)) END, 0)::bigint AS usd_rate_microrate
FROM sales_projection s
JOIN sale_set USING (sale_id)
ORDER BY s.occurred_at DESC, s.sale_id;

-- name: PrintableReportLatestUSDRate :many
-- The USD->EGP rate in effect at @as_of, for the report's rate note: the
-- newest Cloud rate effective by then, else the newest USD Sale snapshot
-- in scope paid by then. No row when neither exists.
SELECT rate_microrate, effective_at, source FROM (
    (SELECT x.rate_microrate, x.effective_at, 'cloud'::text AS source, 1 AS priority
     FROM exchange_rates x
     WHERE x.base = 'USD' AND x.quote = 'EGP' AND x.effective_at <= @as_of
     ORDER BY x.effective_at DESC, x.id
     LIMIT 1)
    UNION ALL
    (SELECT s.fx_rate_microrate::bigint, s.occurred_at, 'sale'::text, 2
     FROM sales_projection s
     WHERE s.currency = 'USD' AND s.fx_rate_microrate IS NOT NULL AND s.occurred_at <= @as_of
       AND (sqlc.narg('store_id')::uuid IS NULL OR s.store_id = sqlc.narg('store_id')::uuid)
     ORDER BY s.occurred_at DESC, s.sale_id DESC
     LIMIT 1)
) rates
ORDER BY priority
LIMIT 1;

-- name: PrintableReportSaleLines :many
WITH sale_set AS (
    SELECT s.sale_id FROM sales_projection s
    WHERE s.occurred_at >= @start_utc AND s.occurred_at < @end_utc
      AND (sqlc.narg('store_id')::uuid IS NULL OR s.store_id = sqlc.narg('store_id')::uuid)
    UNION
    SELECT r.sale_id FROM return_refund_projection r
    WHERE r.occurred_at >= @start_utc AND r.occurred_at < @end_utc
      AND (sqlc.narg('store_id')::uuid IS NULL OR r.store_id = sqlc.narg('store_id')::uuid)
)
SELECT l.sale_id, l.sale_item_id, l.position, l.product_id, l.sku, l.product_name,
    l.quantity, l.line_total_minor, l.cost_minor, l.unit_price_minor,
    l.variant_attributes,
    l.variant_price_usd_minor, l.variant_price_egp_minor, s.currency AS sale_currency
FROM sale_lines_projection l
JOIN sale_set USING (sale_id)
JOIN sales_projection s ON s.sale_id=l.sale_id
ORDER BY l.sale_id, l.position;

-- name: PrintableReportLineClassifications :many
WITH sale_set AS (
    SELECT s.sale_id FROM sales_projection s
    WHERE s.occurred_at >= @start_utc AND s.occurred_at < @end_utc
      AND (sqlc.narg('store_id')::uuid IS NULL OR s.store_id = sqlc.narg('store_id')::uuid)
    UNION
    SELECT r.sale_id FROM return_refund_projection r
    WHERE r.occurred_at >= @start_utc AND r.occurred_at < @end_utc
      AND (sqlc.narg('store_id')::uuid IS NULL OR r.store_id = sqlc.narg('store_id')::uuid)
)
SELECT c.sale_id, c.sale_item_id, c.classification_kind, c.classification_id, c.name_ar, c.name_en
FROM sale_line_classifications_projection c
JOIN sale_set USING (sale_id)
ORDER BY c.sale_id, c.sale_item_id, c.classification_kind, c.position, c.classification_id;

-- name: PrintableReportLineTags :many
WITH sale_set AS (
    SELECT s.sale_id FROM sales_projection s
    WHERE s.occurred_at >= @start_utc AND s.occurred_at < @end_utc
      AND (sqlc.narg('store_id')::uuid IS NULL OR s.store_id = sqlc.narg('store_id')::uuid)
    UNION
    SELECT r.sale_id FROM return_refund_projection r
    WHERE r.occurred_at >= @start_utc AND r.occurred_at < @end_utc
      AND (sqlc.narg('store_id')::uuid IS NULL OR r.store_id = sqlc.narg('store_id')::uuid)
)
SELECT t.sale_id, t.sale_item_id, t.tag_id, t.name_ar, t.name_en
FROM sale_item_tag_snapshots t
JOIN sale_set USING (sale_id)
ORDER BY t.sale_id, t.sale_item_id, t.tag_id;

-- Payments of the whole sale set: the payment filter also decides which
-- returns of earlier Sales count.
-- name: PrintableReportSalePayments :many
WITH sale_set AS (
    SELECT s.sale_id FROM sales_projection s
    WHERE s.occurred_at >= @start_utc AND s.occurred_at < @end_utc
      AND (sqlc.narg('store_id')::uuid IS NULL OR s.store_id = sqlc.narg('store_id')::uuid)
    UNION
    SELECT r.sale_id FROM return_refund_projection r
    WHERE r.occurred_at >= @start_utc AND r.occurred_at < @end_utc
      AND (sqlc.narg('store_id')::uuid IS NULL OR r.store_id = sqlc.narg('store_id')::uuid)
)
SELECT p.sale_id, p.method, p.amount_minor, p.change_minor
FROM sale_payments_projection p
JOIN sale_set USING (sale_id)
ORDER BY p.sale_id, p.position;

-- name: PrintableReportReturns :many
SELECT r.return_refund_id, r.sale_id, r.occurred_at, r.currency,
    r.tax_refunded_minor, r.refund_total_minor
FROM return_refund_projection r
WHERE r.occurred_at >= @start_utc AND r.occurred_at < @end_utc
  AND (sqlc.narg('store_id')::uuid IS NULL OR r.store_id = sqlc.narg('store_id')::uuid)
ORDER BY r.occurred_at, r.return_refund_id;

-- name: PrintableReportReturnLines :many
SELECT l.return_refund_id, l.sale_id, l.original_sale_line_id, l.quantity,
    l.restocked, l.refund_minor, l.cost_minor
FROM return_refund_lines_projection l
JOIN return_refund_projection r ON r.return_refund_id = l.return_refund_id
WHERE r.occurred_at >= @start_utc AND r.occurred_at < @end_utc
  AND (sqlc.narg('store_id')::uuid IS NULL OR r.store_id = sqlc.narg('store_id')::uuid)
ORDER BY l.return_refund_id, l.position;

-- name: PrintableReportReturnPayments :many
SELECT p.return_refund_id, p.method, p.amount_minor
FROM return_refund_payments_projection p
JOIN return_refund_projection r ON r.return_refund_id = p.return_refund_id
WHERE r.occurred_at >= @start_utc AND r.occurred_at < @end_utc
  AND (sqlc.narg('store_id')::uuid IS NULL OR r.store_id = sqlc.narg('store_id')::uuid)
ORDER BY p.return_refund_id, p.position;

-- The printed letterhead: the shop profile on the newest Sale in scope.
-- name: PrintableReportShop :many
SELECT s.shop_name_ar, s.shop_name_en, s.shop_address_ar, s.shop_address_en, s.shop_phone
FROM sales_projection s
WHERE sqlc.narg('store_id')::uuid IS NULL OR s.store_id = sqlc.narg('store_id')::uuid
ORDER BY s.occurred_at DESC, s.sale_id
LIMIT 1;

-- Stocktaking: one row per Variant that is sellable or still holds stock,
-- valued at its EGP cost and price (the Variant override, else the
-- Product's). Variants without a ready inventory snapshot count as zero.
-- name: PrintableStockItems :many
SELECT v.variant_id, v.sku, p.product_id, p.name_ar AS product_name_ar,
    COALESCE(p.name_en, '')::text AS product_name_en,
    COALESCE((SELECT string_agg(a.name_ar, ' / ' ORDER BY a.position, a.definition_code)
        FROM catalog_product_variant_attribute_values a WHERE a.variant_id = v.variant_id), '')::text AS variant_name_ar,
    COALESCE((SELECT string_agg(COALESCE(a.name_en, a.name_ar), ' / ' ORDER BY a.position, a.definition_code)
        FROM catalog_product_variant_attribute_values a WHERE a.variant_id = v.variant_id), '')::text AS variant_name_en,
    c.category_id, c.name_ar AS category_name_ar, COALESCE(c.name_en, '')::text AS category_name_en,
    (p.is_active AND v.is_active)::boolean AS active,
    COALESCE(i.stock_quantity, 0)::bigint AS quantity,
    COALESCE(vp.cost_minor, pp.cost_minor) AS unit_cost_minor,
    COALESCE(vp.price_minor, pp.price_minor, 0)::bigint AS unit_price_minor,
    (COALESCE(vp.price_minor, pp.price_minor) IS NOT NULL)::boolean AS has_price
FROM catalog_product_variants v
JOIN catalog_products p ON p.product_id = v.product_id AND p.store_id = v.store_id
JOIN catalog_categories c ON c.category_id = p.top_category_id
LEFT JOIN catalog_product_variant_inventory i
  ON i.variant_id = v.variant_id AND i.store_id = v.store_id AND i.ready
LEFT JOIN catalog_product_prices pp ON pp.product_id = p.product_id AND pp.currency = 'EGP'
LEFT JOIN catalog_product_variant_prices vp ON vp.variant_id = v.variant_id AND vp.currency = 'EGP'
WHERE NOT v.deleted
  AND ((p.is_active AND v.is_active) OR COALESCE(i.stock_quantity, 0) > 0)
  AND (sqlc.narg('store_id')::uuid IS NULL OR v.store_id = sqlc.narg('store_id')::uuid)
ORDER BY c.name_ar, c.category_id, p.name_ar, p.product_id, v.position, v.sku;
