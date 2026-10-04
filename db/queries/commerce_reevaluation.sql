-- Phase 13 §83: generic durable commerce re-evaluation. Provider-neutral
-- and Product-keyed: the category/product projectors enqueue affected
-- Products atomically with their projection commit; a worker later runs
-- the existing generic CommerceService.SyncProduct per registered
-- provider. No provider I/O ever happens inside projection transactions.
--
-- Dedupe/coalescing is the primary key: repeated requests for one
-- Product collapse into one row (shared-DAG paths never fan out twice),
-- and the worker always re-reads CURRENT desired state, so rapid
-- on/off/on toggles converge to the latest committed policy.

-- name: EnqueueProductReevaluation :exec
INSERT INTO commerce_product_reevaluations (product_id, store_id, reason, requested_at, next_attempt_at)
VALUES ($1, $2, $3, now(), now())
ON CONFLICT (product_id) DO UPDATE SET
    requested_at = now(),
    requested_generation = commerce_product_reevaluations.requested_generation + 1,
    reason = EXCLUDED.reason,
    next_attempt_at = now(),
    attempts = 0,
    last_error_code = NULL;

-- name: EnqueueCategoryAffectedReevaluations :execrows
-- Set-based descendant fan-out (§85): every Product whose classification
-- reaches the Category or any DAG descendant of it. UNION deduplicates
-- multi-parent paths (§86/§182); Store scope matches proven ownership
-- only (legacy NULL never fans out Store-owned Products, §107).
WITH RECURSIVE descendants (category_id) AS (
    SELECT $1::uuid
    UNION
    SELECT e.child_id
    FROM catalog_category_edges e
    JOIN descendants d ON e.parent_id = d.category_id
)
INSERT INTO commerce_product_reevaluations (product_id, store_id, reason, requested_at, next_attempt_at)
SELECT DISTINCT p.product_id, p.store_id, $2::text, now(), now()
FROM catalog_products p
WHERE p.store_id IS NOT DISTINCT FROM $3::uuid
  AND (
    p.top_category_id = $1::uuid
    OR EXISTS (
      SELECT 1
      FROM catalog_product_subcategories ps
      JOIN descendants reachable ON reachable.category_id = ps.category_id
      WHERE ps.product_id = p.product_id
    )
  )
ON CONFLICT (product_id) DO UPDATE SET
    requested_at = now(),
    requested_generation = commerce_product_reevaluations.requested_generation + 1,
    reason = EXCLUDED.reason,
    next_attempt_at = now(),
    attempts = 0,
    last_error_code = NULL;

-- name: ClaimProductReevaluations :many
-- Scheduling never changes an active lease. A fresh opaque token also fences
-- deletion/reinsertion (generation counters alone could suffer an ABA race).
WITH due AS (
 SELECT product_id FROM commerce_product_reevaluations
 WHERE next_attempt_at <= now() AND (lease_until IS NULL OR lease_until <= now())
 ORDER BY next_attempt_at, product_id
 LIMIT @batch_size::int FOR UPDATE SKIP LOCKED
)
UPDATE commerce_product_reevaluations q
SET lease_until = @lease_until::timestamptz, lease_token = @lease_token::uuid,
    lease_generation = lease_generation + 1, claimed_generation = requested_generation
FROM due WHERE q.product_id = due.product_id
RETURNING q.product_id, q.store_id, q.reason, q.attempts,
 q.claimed_generation, q.lease_generation, q.lease_token, q.lease_until;

-- name: CompleteProductReevaluation :one
-- Only this live claim can acknowledge its captured intent. Newer intent is
-- released for another pass, rather than being deleted by an older send.
WITH finished AS (
 DELETE FROM commerce_product_reevaluations
 WHERE product_id = @product_id::uuid AND lease_token = @lease_token::uuid
 AND lease_generation = @lease_generation::bigint AND claimed_generation = @claimed_generation::bigint
 AND lease_until > now() AND requested_generation = claimed_generation
 RETURNING product_id
), newer AS (
 UPDATE commerce_product_reevaluations SET lease_token=NULL, lease_until=NULL,
 claimed_generation=NULL, next_attempt_at=now()
 WHERE product_id = @product_id::uuid AND lease_token = @lease_token::uuid
 AND lease_generation = @lease_generation::bigint AND claimed_generation = @claimed_generation::bigint
 AND lease_until > now() AND requested_generation > claimed_generation
 RETURNING product_id
)
SELECT EXISTS(SELECT 1 FROM finished) OR EXISTS(SELECT 1 FROM newer) AS acknowledged;

-- name: RetryProductReevaluation :execrows
UPDATE commerce_product_reevaluations
SET attempts = CASE WHEN requested_generation > claimed_generation THEN attempts ELSE attempts+1 END,
 next_attempt_at = CASE WHEN requested_generation > claimed_generation THEN next_attempt_at ELSE @next_attempt_at::timestamptz END,
 last_error_code = CASE WHEN requested_generation > claimed_generation THEN last_error_code ELSE @last_error_code::text END,
 lease_token=NULL, lease_until=NULL, claimed_generation=NULL
WHERE product_id = @product_id::uuid AND lease_token = @lease_token::uuid
 AND lease_generation = @lease_generation::bigint AND claimed_generation = @claimed_generation::bigint
 AND lease_until > now();

-- name: CountProductReevaluations :one
SELECT count(*) FROM commerce_product_reevaluations;
