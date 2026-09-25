-- name: InsertReturnProjection :one
INSERT INTO return_refund_projection (
    return_refund_id, source_event_id, source_device_id, return_number, kind,
    reason, note, sale_id, sale_number, sale_event_id, channel,
    occurred_at, currency,
    gross_refunded_minor, discount_refunded_minor, tax_refunded_minor, refund_total_minor,
    fx_base, fx_quote, fx_rate, fx_rate_microrate,
    shop_name_ar, shop_name_en, shop_address_ar, shop_address_en, shop_phone,
    shop_receipt_footer_ar, shop_receipt_footer_en,
    actor_user_id, actor_user_name,
    received_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
    $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25,
    $26, $27, $28, $29, $30, $31
)
ON CONFLICT (return_refund_id) DO NOTHING
RETURNING return_refund_id, source_event_id;

-- name: ReturnProjectionByID :one
SELECT return_refund_id, source_event_id, source_device_id, return_number, kind,
    reason, sale_id, sale_number, channel, occurred_at, currency,
    gross_refunded_minor, discount_refunded_minor, tax_refunded_minor, refund_total_minor
FROM return_refund_projection WHERE return_refund_id = $1;

-- name: ReturnProjectionByEventID :one
SELECT return_refund_id FROM return_refund_projection WHERE source_event_id = $1;

-- name: InsertReturnLine :exec
INSERT INTO return_refund_lines_projection (
    return_refund_id, sale_id, original_sale_line_id, position, product_id,
    quantity, restocked,
    gross_minor, gross_currency, discount_minor, discount_currency,
    tax_minor, tax_currency, refund_minor, refund_currency,
    cost_minor, cost_currency
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17
)
ON CONFLICT (return_refund_id, original_sale_line_id) DO NOTHING;

-- name: InsertReturnPayment :exec
INSERT INTO return_refund_payments_projection (
    return_refund_id, position, method, amount_minor, amount_currency,
    transaction_ref
) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (return_refund_id, position) DO NOTHING;

-- name: LockSaleProjectionForReturn :one
-- Parent-row lock serializing all return projections for one sale, so the
-- cumulative quantity/economic guards below cannot write-skew under
-- concurrency. Returns the authoritative sale economics for cross-checks.
SELECT sale_id, source_event_id, sale_number, channel, occurred_at, paid_at,
    currency, subtotal_minor, discount_minor, tax_minor, total_minor,
    fx_base, fx_quote, fx_rate, fx_rate_microrate
FROM sales_projection WHERE sale_id = $1
FOR UPDATE;

-- name: SaleLineForReturn :one
-- Authoritative sold quantity for one sale line. Missing row means the
-- referenced line is not part of the sale projection.
SELECT sale_id, sale_item_id, quantity, line_total_minor
FROM sale_lines_projection WHERE sale_id = $1 AND sale_item_id = $2;

-- name: CumulativeReturnedQty :one
-- Committed authoritative returns only: projection rows exist solely for
-- ownership winners (rivals are blocked before writing), so no ownership
-- join is needed.
SELECT COALESCE(SUM(quantity), 0)::bigint AS total
FROM return_refund_lines_projection
WHERE sale_id = $1 AND original_sale_line_id = $2;

-- name: CumulativeRefundedForSale :one
-- Sum of authoritative line refunds already committed for one sale, for the
-- cumulative-refund guard (telescoping-exact under frozen allocation:
-- partial sums never exceed the sale total).
SELECT COALESCE(SUM(l.refund_minor), 0)::bigint AS total
FROM return_refund_lines_projection l
JOIN return_refund_projection r ON r.return_refund_id = l.return_refund_id
WHERE r.sale_id = $1;

-- name: PendingReturnEvents :many
-- Durable discovery: accepted return events with no processing row (missing),
-- a pending row, or a due retry. Blocked and processed rows are never
-- returned; retry rows only when next_attempt_at <= now (NULL counts as
-- due). Ordered deterministically by received_at, event_id.
SELECT e.event_id
FROM sync_events e
LEFT JOIN sync_event_processing p
  ON p.event_id = e.event_id AND p.processor = $1
WHERE e.event_type = 'sale.return_refund.finalized.v1'
  AND (p.event_id IS NULL
       OR p.status = 'pending'
       OR (p.status = 'retry' AND (p.next_attempt_at IS NULL OR p.next_attempt_at <= now())))
ORDER BY e.received_at, e.event_id
LIMIT $2;
