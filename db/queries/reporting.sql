-- Reporting queries (Phase 3A). Read-only aggregates over the frozen
-- sale.finalized.v1 projection. Every time filter is the half-open window
-- [start, end) on sales_projection.occurred_at (business time, never
-- transport time). Money aggregates cast numeric SUM to bigint so overflow
-- fails explicitly instead of wrapping or floating. No dynamic SQL: one
-- static query per report need; currency '' means all currencies (bucketed,
-- never summed across currencies).

-- name: ReportSalesSummary :many
SELECT s.currency,
    count(*)::bigint AS transactions,
    COALESCE(SUM(s.subtotal_minor), 0)::bigint AS subtotal,
    COALESCE(SUM(s.discount_minor), 0)::bigint AS discount,
    COALESCE(SUM(s.tax_minor), 0)::bigint AS tax,
    COALESCE(SUM(s.total_minor), 0)::bigint AS sales_total,
    COALESCE(SUM(l.units), 0)::bigint AS units,
    COALESCE(SUM(l.ext_cost), 0)::bigint AS line_cost
FROM sales_projection s
LEFT JOIN (
    SELECT sale_id,
        COALESCE(SUM(quantity), 0)::bigint AS units,
        SUM(cost_minor::numeric * quantity) AS ext_cost
    FROM sale_lines_projection
    GROUP BY sale_id
) l ON l.sale_id = s.sale_id
WHERE s.occurred_at >= @start_utc AND s.occurred_at < @end_utc
  AND (@currency::text = '' OR s.currency = @currency::text)
GROUP BY s.currency
ORDER BY s.currency;

-- name: ReportSalesPayments :many
SELECT p.method, p.amount_currency AS currency,
    COALESCE(SUM(p.amount_minor), 0)::bigint AS amount,
    COALESCE(SUM(p.change_minor), 0)::bigint AS change
FROM sale_payments_projection p
JOIN sales_projection s ON s.sale_id = p.sale_id
WHERE s.occurred_at >= @start_utc AND s.occurred_at < @end_utc
  AND (@currency::text = '' OR p.amount_currency = @currency::text)
GROUP BY p.method, p.amount_currency
ORDER BY p.method, p.amount_currency;

-- name: ReportSalesDaily :many
SELECT ((s.occurred_at AT TIME ZONE @timezone::text)::date)::text AS day,
    s.currency,
    count(*)::bigint AS transactions,
    COALESCE(SUM(s.subtotal_minor), 0)::bigint AS subtotal,
    COALESCE(SUM(s.discount_minor), 0)::bigint AS discount,
    COALESCE(SUM(s.tax_minor), 0)::bigint AS tax,
    COALESCE(SUM(s.total_minor), 0)::bigint AS sales_total,
    COALESCE(SUM(l.units), 0)::bigint AS units,
    COALESCE(SUM(l.ext_cost), 0)::bigint AS line_cost
FROM sales_projection s
LEFT JOIN (
    SELECT sale_id,
        COALESCE(SUM(quantity), 0)::bigint AS units,
        SUM(cost_minor::numeric * quantity) AS ext_cost
    FROM sale_lines_projection
    GROUP BY sale_id
) l ON l.sale_id = s.sale_id
WHERE s.occurred_at >= @start_utc AND s.occurred_at < @end_utc
  AND (@currency::text = '' OR s.currency = @currency::text)
GROUP BY 1, s.currency
ORDER BY 1, s.currency;

-- name: ReportSalesByProduct :many
SELECT l.product_id, l.sku, l.product_name, l.line_currency AS currency,
    COALESCE(SUM(l.quantity), 0)::bigint AS units,
    COALESCE(SUM(l.line_total_minor), 0)::bigint AS line_sales,
    COALESCE(SUM(l.cost_minor::numeric * l.quantity), 0)::bigint AS line_cost
FROM sale_lines_projection l
JOIN sales_projection s ON s.sale_id = l.sale_id
WHERE s.occurred_at >= @start_utc AND s.occurred_at < @end_utc
  AND (@currency::text = '' OR l.line_currency = @currency::text)
GROUP BY l.product_id, l.sku, l.product_name, l.line_currency;

-- name: ReportSalesByCategory :many
SELECT c.classification_kind AS kind, c.classification_id AS id,
    c.name_ar, c.name_en, l.line_currency AS currency,
    COALESCE(SUM(l.quantity), 0)::bigint AS units,
    COALESCE(SUM(l.line_total_minor), 0)::bigint AS sales,
    COALESCE(SUM(l.cost_minor::numeric * l.quantity), 0)::bigint AS cost
FROM sale_line_classifications_projection c
JOIN sale_lines_projection l
  ON l.sale_id = c.sale_id AND l.sale_item_id = c.sale_item_id
JOIN sales_projection s ON s.sale_id = c.sale_id
WHERE s.occurred_at >= @start_utc AND s.occurred_at < @end_utc
  AND c.classification_kind = @kind::text
  AND (@currency::text = '' OR l.line_currency = @currency::text)
GROUP BY c.classification_kind, c.classification_id, c.name_ar, c.name_en, l.line_currency;

-- name: ReportSalesByCashier :many
SELECT s.cashier_id, s.cashier_name, s.currency,
    count(*)::bigint AS transactions,
    COALESCE(SUM(s.subtotal_minor), 0)::bigint AS subtotal,
    COALESCE(SUM(s.discount_minor), 0)::bigint AS discount,
    COALESCE(SUM(s.tax_minor), 0)::bigint AS tax,
    COALESCE(SUM(s.total_minor), 0)::bigint AS sales_total,
    COALESCE(SUM(l.units), 0)::bigint AS units
FROM sales_projection s
LEFT JOIN (
    SELECT sale_id, COALESCE(SUM(quantity), 0)::bigint AS units
    FROM sale_lines_projection
    GROUP BY sale_id
) l ON l.sale_id = s.sale_id
WHERE s.occurred_at >= @start_utc AND s.occurred_at < @end_utc
  AND (@currency::text = '' OR s.currency = @currency::text)
GROUP BY s.cashier_id, s.cashier_name, s.currency;

-- name: ReportSalesByChannel :many
SELECT s.channel, s.currency,
    count(*)::bigint AS transactions,
    COALESCE(SUM(s.subtotal_minor), 0)::bigint AS subtotal,
    COALESCE(SUM(s.discount_minor), 0)::bigint AS discount,
    COALESCE(SUM(s.tax_minor), 0)::bigint AS tax,
    COALESCE(SUM(s.total_minor), 0)::bigint AS sales_total,
    COALESCE(SUM(l.units), 0)::bigint AS units
FROM sales_projection s
LEFT JOIN (
    SELECT sale_id, COALESCE(SUM(quantity), 0)::bigint AS units
    FROM sale_lines_projection
    GROUP BY sale_id
) l ON l.sale_id = s.sale_id
WHERE s.occurred_at >= @start_utc AND s.occurred_at < @end_utc
  AND (@currency::text = '' OR s.currency = @currency::text)
GROUP BY s.channel, s.currency;

-- name: ReportFreshness :one
SELECT
    (SELECT max(received_at)::timestamptz FROM sync_events WHERE event_type = 'sale.finalized.v1') AS latest_received,
    (SELECT max(occurred_at)::timestamptz FROM sales_projection) AS latest_occurred,
    (SELECT count(*) FROM sync_events e WHERE e.event_type = 'sale.finalized.v1'
        AND NOT EXISTS (SELECT 1 FROM sync_event_processing p
            WHERE p.event_id = e.event_id AND p.processor = @processor::text
              AND p.status IN ('processed', 'blocked'))) AS backlog,
    (SELECT count(*) FROM sync_event_processing
        WHERE processor = @processor::text AND status = 'blocked') AS blocked;

-- Phase 4B return/refund aggregates. Same conventions as the frozen sale
-- queries: half-open [start, end) on the RETURN occurred_at (business
-- time, never transport), numeric SUM with bigint overflow failure, no
-- dynamic SQL, currency '' means all (rows stay bucketed, never summed).
-- Refunds expose reversal economics; net is derived by merging with the
-- frozen gross queries in Go (checked integer arithmetic, never clamped).

-- name: ReportRefundsSummary :many
SELECT r.currency, count(*)::bigint AS transactions,
 COALESCE(SUM(l.units), 0)::bigint AS units,
 COALESCE(SUM(r.gross_refunded_minor), 0)::bigint AS gross_refunded,
 COALESCE(SUM(r.discount_refunded_minor), 0)::bigint AS discount_refunded,
 COALESCE(SUM(r.tax_refunded_minor), 0)::bigint AS tax_refunded,
 COALESCE(SUM(r.refund_total_minor), 0)::bigint AS refund_total,
 COALESCE(SUM(l.ext_cost), 0)::bigint AS returned_cost
FROM return_refund_projection r
LEFT JOIN (
    SELECT return_refund_id, COALESCE(SUM(quantity), 0)::bigint AS units,
        SUM(cost_minor) AS ext_cost
    FROM return_refund_lines_projection
    GROUP BY return_refund_id
) l ON l.return_refund_id = r.return_refund_id
WHERE r.occurred_at >= @start_utc AND r.occurred_at < @end_utc
 AND (@currency::text = '' OR r.currency = @currency::text)
GROUP BY r.currency ORDER BY r.currency;

-- name: ReportRefundsDaily :many
SELECT ((r.occurred_at AT TIME ZONE @timezone::text)::date)::text AS day,
 r.currency, count(*)::bigint AS transactions,
 COALESCE(SUM(l.units), 0)::bigint AS units,
 COALESCE(SUM(r.refund_total_minor), 0)::bigint AS refund_total,
 COALESCE(SUM(l.ext_cost), 0)::bigint AS returned_cost
FROM return_refund_projection r
LEFT JOIN (
    SELECT return_refund_id, COALESCE(SUM(quantity), 0)::bigint AS units,
        SUM(cost_minor) AS ext_cost
    FROM return_refund_lines_projection
    GROUP BY return_refund_id
) l ON l.return_refund_id = r.return_refund_id
WHERE r.occurred_at >= @start_utc AND r.occurred_at < @end_utc
 AND (@currency::text = '' OR r.currency = @currency::text)
GROUP BY 1, r.currency ORDER BY 1, r.currency;

-- name: ReportRefundsByProduct :many
SELECT sl.product_id, sl.sku, sl.product_name, l.refund_currency AS currency,
 COALESCE(SUM(l.quantity), 0)::bigint AS units,
 COALESCE(SUM(l.refund_minor), 0)::bigint AS refund,
 COALESCE(SUM(l.cost_minor), 0)::bigint AS returned_cost
FROM return_refund_lines_projection l
JOIN sale_lines_projection sl
  ON sl.sale_id = l.sale_id AND sl.sale_item_id = l.original_sale_line_id
JOIN return_refund_projection r ON r.return_refund_id = l.return_refund_id
WHERE r.occurred_at >= @start_utc AND r.occurred_at < @end_utc
 AND (@currency::text = '' OR l.refund_currency = @currency::text)
GROUP BY sl.product_id, sl.sku, sl.product_name, l.refund_currency;

-- name: ReportRefundsByCategory :many
SELECT c.classification_kind AS kind, c.classification_id AS id, c.name_ar, c.name_en,
 l.refund_currency AS currency, COALESCE(SUM(l.quantity), 0)::bigint AS units,
 COALESCE(SUM(l.refund_minor), 0)::bigint AS refund,
 COALESCE(SUM(l.cost_minor), 0)::bigint AS returned_cost
FROM sale_line_classifications_projection c
JOIN return_refund_lines_projection l
  ON l.sale_id = c.sale_id AND l.original_sale_line_id = c.sale_item_id
JOIN return_refund_projection r ON r.return_refund_id = l.return_refund_id
WHERE r.occurred_at >= @start_utc AND r.occurred_at < @end_utc
 AND c.classification_kind = @kind::text
 AND (@currency::text = '' OR l.refund_currency = @currency::text)
GROUP BY c.classification_kind, c.classification_id, c.name_ar, c.name_en, l.refund_currency;

-- name: ReportRefundsByCashier :many
SELECT s.cashier_id, s.cashier_name, r.currency, count(*)::bigint AS transactions,
 COALESCE(SUM(l.units), 0)::bigint AS units,
 COALESCE(SUM(r.refund_total_minor), 0)::bigint AS refund_total
FROM return_refund_projection r
JOIN sales_projection s ON s.sale_id = r.sale_id
LEFT JOIN (
    SELECT return_refund_id, COALESCE(SUM(quantity), 0)::bigint AS units
    FROM return_refund_lines_projection
    GROUP BY return_refund_id
) l ON l.return_refund_id = r.return_refund_id
WHERE r.occurred_at >= @start_utc AND r.occurred_at < @end_utc
 AND (@currency::text = '' OR r.currency = @currency::text)
GROUP BY s.cashier_id, s.cashier_name, r.currency;

-- name: ReportRefundsByChannel :many
SELECT s.channel, r.currency, count(*)::bigint AS transactions,
 COALESCE(SUM(l.units), 0)::bigint AS units,
 COALESCE(SUM(r.refund_total_minor), 0)::bigint AS refund_total
FROM return_refund_projection r
JOIN sales_projection s ON s.sale_id = r.sale_id
LEFT JOIN (
    SELECT return_refund_id, COALESCE(SUM(quantity), 0)::bigint AS units
    FROM return_refund_lines_projection
    GROUP BY return_refund_id
) l ON l.return_refund_id = r.return_refund_id
WHERE r.occurred_at >= @start_utc AND r.occurred_at < @end_utc
 AND (@currency::text = '' OR r.currency = @currency::text)
GROUP BY s.channel, r.currency;

-- name: ReportReturnFreshness :one
SELECT (SELECT max(received_at)::timestamptz FROM sync_events WHERE event_type = 'sale.return_refund.finalized.v1') AS latest_received,
 (SELECT max(occurred_at)::timestamptz FROM return_refund_projection) AS latest_occurred,
 (SELECT count(*) FROM sync_events e WHERE e.event_type = 'sale.return_refund.finalized.v1' AND NOT EXISTS (SELECT 1 FROM sync_event_processing p WHERE p.event_id = e.event_id AND p.processor = 'return_refund_projection.v1' AND p.status IN ('processed', 'blocked'))) AS backlog,
 (SELECT count(*) FROM sync_event_processing WHERE processor = 'return_refund_projection.v1' AND status = 'blocked') AS blocked;
