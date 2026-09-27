-- Phase 6C commerce order webhook inbox, current-order projection, and
-- read models. Webhook rows are durable integration history; order rows
-- are provider-derived current state. Neither is ever cleared by
-- catalog/policy/inventory rebuilds.

-- name: InsertCommerceWebhookEvent :one
INSERT INTO commerce_online_order_webhook_events (
    provider_key, delivery_id, topic, external_order_id,
    payload_hash, webhook_id
) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (provider_key, delivery_id) DO NOTHING
RETURNING provider_key, delivery_id, topic, external_order_id,
    payload_hash, received_at, status, attempt_count;

-- name: GetCommerceWebhookEvent :one
SELECT provider_key, delivery_id, topic, external_order_id,
    payload_hash, webhook_id, received_at, status, attempt_count,
    next_attempt_at, processed_at, last_error_code
FROM commerce_online_order_webhook_events
WHERE provider_key = $1 AND delivery_id = $2;

-- name: ClaimCommerceWebhookEvent :one
-- Single eligible event with row-level claiming: concurrent workers
-- skip each other's locked rows, and crashed claims become eligible
-- again after the lease horizon. No permanent "processing" status.
WITH candidate AS (
    SELECT provider_key, delivery_id
    FROM commerce_online_order_webhook_events
    WHERE status IN ('pending', 'retry')
      AND (next_attempt_at IS NULL OR next_attempt_at <= now())
      AND (lease_until IS NULL OR lease_until <= now())
    ORDER BY received_at, provider_key, delivery_id
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
UPDATE commerce_online_order_webhook_events AS event
SET lease_owner = $1, lease_until = $2,
    lease_generation = event.lease_generation + 1,
    attempt_count = event.attempt_count + 1, updated_at = now()
FROM candidate
WHERE event.provider_key = candidate.provider_key
  AND event.delivery_id = candidate.delivery_id
RETURNING event.provider_key, event.delivery_id, event.topic,
    event.external_order_id, event.payload_hash, event.received_at,
    event.status, event.attempt_count, event.lease_owner, event.lease_generation;

-- name: FinishCommerceWebhookEvent :execrows
-- Fenced completion: only the current lease owner holding the current
-- generation on a non-terminal row may transition it. Stale workers
-- affect zero rows and must treat that as a safe no-op.
UPDATE commerce_online_order_webhook_events
SET status = $5, next_attempt_at = $6, processed_at = $7,
    last_error_code = $8, lease_owner = NULL, lease_until = NULL,
    updated_at = now()
WHERE provider_key = $1 AND delivery_id = $2
  AND lease_owner = $3 AND lease_generation = $4
  AND status IN ('pending', 'retry');

-- name: CommerceWebhookStats :one
SELECT
    count(*) FILTER (WHERE status = 'pending')::bigint AS pending,
    count(*) FILTER (WHERE status = 'retry')::bigint AS retry,
    count(*) FILTER (WHERE status = 'blocked')::bigint AS blocked,
    min(received_at) FILTER (WHERE status IN ('pending', 'retry'))::timestamptz AS oldest_pending
FROM commerce_online_order_webhook_events;;

-- name: UpsertCommerceOrder :exec
INSERT INTO commerce_online_orders (
    provider_key, external_order_id, order_number,
    provider_status, canonical_status, currency,
    discount_minor, shipping_minor, cart_tax_minor, total_tax_minor, total_minor,
    prices_include_tax, created_at, modified_at, paid_at, completed_at,
    payment_method, payment_method_title,
    customer_first_name, customer_last_name, customer_email, customer_phone,
    revision, fingerprint, provider_deleted, mapping_complete, unmapped_lines
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16,
    $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27
)
ON CONFLICT (provider_key, external_order_id) DO UPDATE SET
    order_number = excluded.order_number,
    provider_status = excluded.provider_status, canonical_status = excluded.canonical_status,
    currency = excluded.currency,
    discount_minor = excluded.discount_minor, shipping_minor = excluded.shipping_minor,
    cart_tax_minor = excluded.cart_tax_minor, total_tax_minor = excluded.total_tax_minor,
    total_minor = excluded.total_minor, prices_include_tax = excluded.prices_include_tax,
    created_at = excluded.created_at, modified_at = excluded.modified_at,
    paid_at = excluded.paid_at, completed_at = excluded.completed_at,
    payment_method = excluded.payment_method, payment_method_title = excluded.payment_method_title,
    customer_first_name = excluded.customer_first_name,
    customer_last_name = excluded.customer_last_name,
    customer_email = excluded.customer_email, customer_phone = excluded.customer_phone,
    revision = excluded.revision, fingerprint = excluded.fingerprint,
    provider_deleted = excluded.provider_deleted,
    mapping_complete = excluded.mapping_complete, unmapped_lines = excluded.unmapped_lines,
    projected_at = now(), updated_at = now();

-- name: DeleteCommerceOrderLines :exec
DELETE FROM commerce_online_order_lines WHERE provider_key = $1 AND external_order_id = $2;

-- name: DeleteCommerceOrderAddresses :exec
DELETE FROM commerce_online_order_addresses WHERE provider_key = $1 AND external_order_id = $2;

-- name: InsertCommerceOrderLine :exec
INSERT INTO commerce_online_order_lines (
    provider_key, external_order_id, external_line_id,
    external_product_id, variation_id, sku, name, quantity,
    subtotal_minor, subtotal_tax_minor, total_minor, total_tax_minor,
    moonlight_product_id, mapped, unsupported_reason
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
);

-- name: InsertCommerceOrderAddress :exec
INSERT INTO commerce_online_order_addresses (
    provider_key, external_order_id, kind,
    first_name, last_name, company, address_1, address_2,
    city, state, postcode, country, email, phone
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14
);

-- name: InsertCommerceOrderStatusHistory :exec
INSERT INTO commerce_online_order_status_history (
    provider_key, external_order_id, order_revision,
    provider_status, canonical_status, provider_modified_at
) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (provider_key, external_order_id, order_revision) DO NOTHING;

-- name: GetCommerceOrder :one
SELECT provider_key, external_order_id, order_number,
    provider_status, canonical_status, currency,
    discount_minor, shipping_minor, cart_tax_minor, total_tax_minor, total_minor,
    prices_include_tax, created_at, modified_at, paid_at, completed_at,
    payment_method, payment_method_title,
    customer_first_name, customer_last_name, customer_email, customer_phone,
    revision, fingerprint, provider_deleted, mapping_complete, unmapped_lines,
    projected_at, updated_at
FROM commerce_online_orders
WHERE provider_key = $1 AND external_order_id = $2;

-- name: ListCommerceOrderLines :many
SELECT provider_key, external_order_id, external_line_id,
    external_product_id, variation_id, sku, name, quantity,
    subtotal_minor, subtotal_tax_minor, total_minor, total_tax_minor,
    moonlight_product_id, mapped, unsupported_reason
FROM commerce_online_order_lines
WHERE provider_key = $1 AND external_order_id = $2
ORDER BY external_line_id;

-- name: ListCommerceOrderAddresses :many
SELECT provider_key, external_order_id, kind,
    first_name, last_name, company, address_1, address_2,
    city, state, postcode, country, email, phone
FROM commerce_online_order_addresses
WHERE provider_key = $1 AND external_order_id = $2
ORDER BY kind;

-- name: ListCommerceOrderStatusHistory :many
SELECT provider_key, external_order_id, order_revision,
    provider_status, canonical_status, provider_modified_at, observed_at
FROM commerce_online_order_status_history
WHERE provider_key = $1 AND external_order_id = $2
ORDER BY order_revision;

-- name: ListCommerceOrders :many
SELECT provider_key, external_order_id, order_number,
    provider_status, canonical_status, currency, total_minor,
    created_at, modified_at,
    customer_first_name, customer_last_name,
    mapping_complete, unmapped_lines, provider_deleted, revision
FROM commerce_online_orders
WHERE (NULLIF($1::text, '') IS NULL OR provider_key = $1)
  AND (NULLIF($2::text, '') IS NULL OR canonical_status = $2)
  AND ($3::timestamptz IS NULL OR created_at < $3::timestamptz
    OR (created_at = $3::timestamptz AND (provider_key, external_order_id) > ($4::text, $5::text)))
ORDER BY created_at DESC, provider_key, external_order_id
LIMIT $6;

-- name: CountCommerceOrdersByStatus :many
SELECT canonical_status, count(*)::bigint AS total
FROM commerce_online_orders
WHERE (NULLIF($1::text, '') IS NULL OR provider_key = $1)
GROUP BY canonical_status;

-- name: BeginOrderReconcile :one
-- Per-order reconciliation fence: first starter creates generation 1,
-- every later starter atomically increments exactly once. The increment
-- guard fails safely instead of wrapping BIGINT on overflow.
INSERT INTO commerce_online_order_reconcile_fences AS fence (provider_key, external_order_id, generation)
VALUES ($1, $2, 1)
ON CONFLICT (provider_key, external_order_id) DO UPDATE SET
    generation = fence.generation + 1, updated_at = now()
WHERE fence.generation < 9223372036854775807
RETURNING generation;

-- name: GetOrderReconcileGeneration :one
SELECT generation FROM commerce_online_order_reconcile_fences
WHERE provider_key = $1 AND external_order_id = $2;

-- name: LockOrderReconcileFence :one
-- Fence value under lock: the single ordering point for concurrent
-- reconciliations of one order. Always locked before any order row.
SELECT generation FROM commerce_online_order_reconcile_fences
WHERE provider_key = $1 AND external_order_id = $2
FOR UPDATE;
