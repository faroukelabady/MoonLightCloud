-- Dashboard read queries (Phase 3B). Read-only aggregates over the frozen
-- sale.finalized.v1 projection. Historical FX normalization converts each
-- USD Sale with ITS OWN fx_rate_microrate snapshot (never a current rate):
-- normalized = EGP native + SUM(USD total * microrate / 1e6). Rounding is
-- PostgreSQL numeric-to-bigint conversion (half away from zero), exact
-- integer math throughout, no floats; overflow fails explicitly via the
-- bigint casts.
-- bigint casts so overflow fails instead of wrapping. All windows are
-- half-open [start, end) on sales_projection.occurred_at. No dynamic SQL.

-- name: DashboardNormalizedSummary :one
SELECT
    count(*)::bigint AS transactions,
    COALESCE(SUM(l.units), 0)::bigint AS units,
    COALESCE(SUM(CASE WHEN s.currency = 'EGP' THEN s.total_minor::numeric
        ELSE (s.total_minor::numeric * s.fx_rate_microrate) / 1000000 END), 0)::bigint AS normalized_total,
    COUNT(*) FILTER (WHERE s.currency = 'USD')::bigint AS usd_sales,
    COUNT(*) FILTER (WHERE s.currency = 'USD' AND s.fx_rate_microrate IS NULL)::bigint AS usd_missing_fx
FROM sales_projection s
LEFT JOIN (
    SELECT sale_id, COALESCE(SUM(quantity), 0)::bigint AS units
    FROM sale_lines_projection
    GROUP BY sale_id
) l ON l.sale_id = s.sale_id
WHERE s.occurred_at >= @start_utc AND s.occurred_at < @end_utc;

-- name: DashboardNormalizedDaily :many
SELECT day.day AS day,
    COUNT(*)::bigint AS transactions,
    COALESCE(SUM(day.normalized), 0)::bigint AS normalized_total
FROM (
    SELECT ((s.occurred_at AT TIME ZONE @timezone::text)::date)::text AS day,
        CASE WHEN s.currency = 'EGP' THEN s.total_minor::numeric
        ELSE (s.total_minor::numeric * s.fx_rate_microrate) / 1000000 END AS normalized
    FROM sales_projection s
    WHERE s.occurred_at >= @start_utc AND s.occurred_at < @end_utc
) day
GROUP BY day.day
ORDER BY day.day;

-- name: DashboardLatestFx :one
SELECT
    (SELECT s.fx_rate FROM sales_projection s
        WHERE s.currency = 'USD' AND s.fx_rate_microrate IS NOT NULL
          AND s.occurred_at >= @start_utc AND s.occurred_at < @end_utc
        ORDER BY s.occurred_at DESC, s.sale_id DESC LIMIT 1) AS latest_rate,
    (SELECT s.fx_rate_microrate FROM sales_projection s
        WHERE s.currency = 'USD' AND s.fx_rate_microrate IS NOT NULL
          AND s.occurred_at >= @start_utc AND s.occurred_at < @end_utc
        ORDER BY s.occurred_at DESC, s.sale_id DESC LIMIT 1) AS latest_microrate,
    (SELECT s.occurred_at FROM sales_projection s
        WHERE s.currency = 'USD' AND s.fx_rate_microrate IS NOT NULL
          AND s.occurred_at >= @start_utc AND s.occurred_at < @end_utc
        ORDER BY s.occurred_at DESC, s.sale_id DESC LIMIT 1) AS latest_occurred,
    (SELECT COUNT(DISTINCT s.fx_rate_microrate) FROM sales_projection s
        WHERE s.currency = 'USD'
          AND s.occurred_at >= @start_utc AND s.occurred_at < @end_utc) AS distinct_rates,
    (SELECT COALESCE(MIN(s.fx_rate_microrate), -1)::bigint FROM sales_projection s
        WHERE s.currency = 'USD'
          AND s.occurred_at >= @start_utc AND s.occurred_at < @end_utc) AS min_microrate,
    (SELECT COALESCE(MAX(s.fx_rate_microrate), -1)::bigint FROM sales_projection s
        WHERE s.currency = 'USD'
          AND s.occurred_at >= @start_utc AND s.occurred_at < @end_utc) AS max_microrate,
    (SELECT COUNT(*) FROM sales_projection s
        WHERE s.currency = 'USD'
          AND s.occurred_at >= @start_utc AND s.occurred_at < @end_utc) AS usd_sales;

-- name: DashboardBranches :many
SELECT s.channel,
    s.shop_name_ar, s.shop_name_en, s.shop_address_ar, s.shop_address_en,
    s.shop_phone, s.shop_receipt_footer_ar, s.shop_receipt_footer_en,
    s.currency,
    count(*)::bigint AS transactions,
    COALESCE(SUM(l.units), 0)::bigint AS units,
    COALESCE(SUM(s.subtotal_minor), 0)::bigint AS subtotal,
    COALESCE(SUM(s.discount_minor), 0)::bigint AS discount,
    COALESCE(SUM(s.tax_minor), 0)::bigint AS tax,
    COALESCE(SUM(s.total_minor), 0)::bigint AS sales_total
FROM sales_projection s
LEFT JOIN (
    SELECT sale_id, COALESCE(SUM(quantity), 0)::bigint AS units
    FROM sale_lines_projection
    GROUP BY sale_id
) l ON l.sale_id = s.sale_id
WHERE s.occurred_at >= @start_utc AND s.occurred_at < @end_utc
  AND (@currency::text = '' OR s.currency = @currency::text)
GROUP BY s.channel,
    s.shop_name_ar, s.shop_name_en, s.shop_address_ar, s.shop_address_en,
    s.shop_phone, s.shop_receipt_footer_ar, s.shop_receipt_footer_en,
    s.currency;

-- name: DashboardProductsNormalized :many
SELECT l.product_id, l.sku, l.product_name,
    COALESCE(SUM(l.quantity), 0)::bigint AS units,
    COALESCE(SUM(CASE WHEN l.line_currency = 'EGP' THEN l.line_total_minor::numeric
        ELSE (l.line_total_minor::numeric * s.fx_rate_microrate) / 1000000 END), 0)::bigint AS normalized,
    COUNT(*) FILTER (WHERE l.line_currency = 'USD' AND s.fx_rate_microrate IS NULL)::bigint AS missing_fx
FROM sale_lines_projection l
JOIN sales_projection s ON s.sale_id = l.sale_id
WHERE s.occurred_at >= @start_utc AND s.occurred_at < @end_utc
GROUP BY l.product_id, l.sku, l.product_name;

-- name: DashboardCategoriesNormalized :many
SELECT c.classification_kind AS kind, c.classification_id AS id,
    c.name_ar, c.name_en,
    COALESCE(SUM(l.quantity), 0)::bigint AS units,
    COALESCE(SUM(CASE WHEN l.line_currency = 'EGP' THEN l.line_total_minor::numeric
        ELSE (l.line_total_minor::numeric * s.fx_rate_microrate) / 1000000 END), 0)::bigint AS normalized,
    COUNT(*) FILTER (WHERE l.line_currency = 'USD' AND s.fx_rate_microrate IS NULL)::bigint AS missing_fx
FROM sale_line_classifications_projection c
JOIN sale_lines_projection l
  ON l.sale_id = c.sale_id AND l.sale_item_id = c.sale_item_id
JOIN sales_projection s ON s.sale_id = c.sale_id
WHERE s.occurred_at >= @start_utc AND s.occurred_at < @end_utc
  AND c.classification_kind = @kind::text
GROUP BY c.classification_kind, c.classification_id, c.name_ar, c.name_en;

-- name: DashboardRecentActivity :many
SELECT kind, event_id, event_type, ts, device_name, detail FROM (
    (SELECT 'accepted'::text AS kind, e.event_id, e.event_type, e.received_at AS ts,
        d.name AS device_name, NULL::text AS detail
    FROM sync_events e
    LEFT JOIN devices d ON d.id = e.device_id
    WHERE e.event_type = 'sale.finalized.v1'
    ORDER BY e.received_at DESC
    LIMIT @limit_n::int)
    UNION ALL
    (SELECT CASE WHEN p.status = 'blocked' THEN 'blocked'::text ELSE 'projected'::text END AS kind,
        p.event_id, e.event_type, COALESCE(p.processed_at, p.updated_at) AS ts,
        d.name AS device_name, p.last_error_code AS detail
    FROM sync_event_processing p
    JOIN sync_events e ON e.event_id = p.event_id
    LEFT JOIN devices d ON d.id = e.device_id
    WHERE p.processor = 'sale_projection.v1' AND p.status IN ('processed', 'blocked')
    ORDER BY ts DESC
    LIMIT @limit_n::int)
) feed
ORDER BY ts DESC
LIMIT @limit_n::int;

-- name: DashboardLatestSales :many
SELECT sale_id, sale_number, channel, occurred_at, currency, total_minor,
    cashier_id, cashier_name
FROM sales_projection
ORDER BY occurred_at DESC, sale_id DESC
LIMIT @limit_n::int;
