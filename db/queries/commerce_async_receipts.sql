-- name: AcknowledgeCommerceAsyncReceipt :execrows
UPDATE commerce_product_mutation_barriers
SET state = 'resolved', resolution = 'remote_completed', resolved_at = clock_timestamp(),
    async_role = $4, async_intent = $5, provider_operation_id = $6, async_state = 'pending'
WHERE operation_id = $1 AND provider_key = $2 AND product_id = $3 AND state = 'in_flight';

-- name: GetCommerceAsyncReceipt :one
SELECT async_role, async_intent, provider_operation_id, async_state, async_product_id
FROM commerce_product_mutation_barriers
WHERE provider_key = $1 AND product_id = $2 AND async_role = $3
ORDER BY created_at DESC, operation_id DESC LIMIT 1;

-- name: FinishCommerceAsyncReceipt :execrows
UPDATE commerce_product_mutation_barriers
SET async_state = $4, async_product_id = $5
WHERE provider_key = $1 AND product_id = $2 AND provider_operation_id = $3
    AND async_state = 'pending';
