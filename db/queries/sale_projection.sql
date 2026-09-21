-- name: InsertSaleProjection :exec
INSERT INTO sales_projection (
    sale_id, source_event_id, source_device_id, sale_number, channel,
    occurred_at, paid_at,
    shop_name_ar, shop_name_en, shop_address_ar, shop_address_en, shop_phone,
    shop_receipt_footer_ar, shop_receipt_footer_en,
    cashier_id, cashier_name, currency,
    subtotal_minor, discount_minor, tax_minor, total_minor,
    fx_base, fx_quote, fx_rate, fx_rate_microrate,
    received_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
    $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26
)
ON CONFLICT (sale_id) DO NOTHING;

-- name: SaleProjectionBySaleID :one
SELECT sale_id, source_event_id, source_device_id, sale_number, channel,
    occurred_at, paid_at, currency, subtotal_minor, discount_minor, tax_minor, total_minor
FROM sales_projection WHERE sale_id = $1;

-- name: SaleProjectionByEventID :one
SELECT sale_id FROM sales_projection WHERE source_event_id = $1;

-- name: InsertSaleLine :exec
INSERT INTO sale_lines_projection (
    sale_id, sale_item_id, position, product_id, variant_id, sku, product_name,
    width_cm, height_cm, quantity,
    unit_price_minor, unit_currency, cost_minor, cost_currency,
    line_total_minor, line_currency
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16
)
ON CONFLICT (sale_id, sale_item_id) DO NOTHING;

-- name: InsertSalePayment :exec
INSERT INTO sale_payments_projection (
    sale_id, position, method, amount_minor, amount_currency,
    change_minor, change_currency, transaction_ref
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (sale_id, position) DO NOTHING;

-- name: InsertSaleClassification :exec
INSERT INTO sale_line_classifications_projection (
    sale_id, sale_item_id, classification_kind, classification_id,
    name_ar, name_en, position
) VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (sale_id, sale_item_id, classification_kind, classification_id) DO NOTHING;
