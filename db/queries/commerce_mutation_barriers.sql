-- name: GetActiveCommerceMutationBarrier :one
SELECT operation_id, provider_key, product_id, request_fingerprint, state, created_at
FROM commerce_product_mutation_barriers
WHERE provider_key=$1 AND product_id=$2 AND state IN ('in_flight','uncertain');

-- name: BeginCommerceMutationBarrier :execrows
INSERT INTO commerce_product_mutation_barriers
(operation_id,provider_key,product_id,request_fingerprint,state)
VALUES ($1,$2,$3,$4,'in_flight') ON CONFLICT DO NOTHING;

-- name: CompleteCommerceMutationBarrier :execrows
DELETE FROM commerce_product_mutation_barriers
WHERE operation_id=$1 AND provider_key=$2 AND product_id=$3 AND state='in_flight';

-- name: MarkCommerceMutationUncertain :execrows
UPDATE commerce_product_mutation_barriers SET state='uncertain'
WHERE operation_id=$1 AND provider_key=$2 AND product_id=$3 AND state='in_flight';

-- name: ResolveCommerceMutationBarrier :execrows
UPDATE commerce_product_mutation_barriers
SET state='resolved',resolved_at=clock_timestamp(),resolution=$4
WHERE operation_id=$1 AND provider_key=$2 AND product_id=$3 AND state IN ('in_flight','uncertain');

-- name: GetCommerceMutationBarrierResolution :one
SELECT resolution FROM commerce_product_mutation_barriers
WHERE operation_id=$1 AND provider_key=$2 AND product_id=$3 AND state='resolved';
