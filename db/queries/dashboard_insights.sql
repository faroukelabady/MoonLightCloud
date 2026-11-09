-- Sync page reads: what arrived in the selected period, one sale with its
-- lines and costs, and the product image store. A NULL store_id reads every
-- Store; the HTTP boundary has already authorized the scope.

-- name: DashboardSyncSummary :many
-- Events Cloud received in [start, end) per kind, and the newest arrival of
-- each kind at any time.
SELECT CASE
        WHEN e.event_type LIKE 'sale.return_refund.%' THEN 'returns'
        WHEN e.event_type LIKE 'sale.%' THEN 'sales'
        WHEN e.event_type LIKE 'catalog.%' THEN 'catalog'
        WHEN e.event_type LIKE 'inventory.%' THEN 'stock'
        ELSE 'other'
    END::text AS category,
    count(*) FILTER (WHERE e.received_at >= @start_utc AND e.received_at < @end_utc)::bigint AS synced,
    count(*) FILTER (WHERE e.received_at >= @recent_start AND e.received_at < @recent_end)::bigint AS received_last_7_days,
    count(*)::bigint AS total_received,
    max(e.received_at)::timestamptz AS last_received_at
FROM sync_events e
WHERE sqlc.narg(store_id)::uuid IS NULL OR e.store_id = sqlc.narg(store_id)::uuid
GROUP BY 1;

-- name: DashboardSaleDetail :one
SELECT s.sale_id, s.sale_number, s.channel, s.occurred_at, s.currency,
    s.subtotal_minor, s.discount_minor, s.tax_minor, s.total_minor,
    s.cashier_name, s.fx_rate_microrate, s.shop_name_ar, s.shop_name_en
FROM sales_projection s
WHERE s.sale_id = @sale_id
  AND (sqlc.narg(store_id)::uuid IS NULL OR s.store_id = sqlc.narg(store_id)::uuid);

-- name: DashboardSaleDetailLines :many
-- cost_minor is the unit cost Retail recorded at sale time (NULL: unknown).
SELECT l.position, l.sku, l.product_name, l.width_cm, l.height_cm, l.quantity,
    l.unit_price_minor, l.cost_minor, l.line_total_minor, l.variant_attributes,
    l.variant_price_usd_minor, l.variant_price_egp_minor, s.currency AS sale_currency
FROM sale_lines_projection l JOIN sales_projection s ON s.sale_id=l.sale_id
WHERE l.sale_id = $1
ORDER BY l.position, l.sale_item_id;

-- name: DashboardMediaStatus :one
-- Stored files are the ones a gallery of the Store names (every file when
-- no Store is selected).
SELECT count(*)::bigint AS stored_files,
    COALESCE(sum(b.byte_size), 0)::bigint AS stored_bytes,
    count(*) FILTER (WHERE b.created_at >= @start_utc AND b.created_at < @end_utc)::bigint AS uploaded_in_range,
    max(b.created_at)::timestamptz AS last_upload_at,
    COALESCE((array_agg(b.sha256 ORDER BY b.created_at DESC, b.sha256))[1], '')::text AS latest_sha256
FROM media_blobs b
WHERE sqlc.narg(store_id)::uuid IS NULL OR EXISTS (
    SELECT 1 FROM catalog_product_media m
    JOIN catalog_products p ON p.product_id = m.product_id
    WHERE m.sha256 = b.sha256 AND p.store_id = sqlc.narg(store_id)::uuid);

-- name: DashboardGalleryStatus :one
-- Gallery entries, and those whose file has not arrived yet.
SELECT count(*)::bigint AS gallery_images,
    count(*) FILTER (WHERE b.sha256 IS NULL)::bigint AS waiting_images
FROM catalog_product_media m
JOIN catalog_products p ON p.product_id = m.product_id
LEFT JOIN media_blobs b ON b.sha256 = m.sha256
WHERE sqlc.narg(store_id)::uuid IS NULL OR p.store_id = sqlc.narg(store_id)::uuid;
