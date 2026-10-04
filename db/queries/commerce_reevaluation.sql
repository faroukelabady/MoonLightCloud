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
    reason = EXCLUDED.reason,
    next_attempt_at = now(),
    attempts = 0,
    last_error_code = NULL;

-- name: ClaimProductReevaluations :many
-- Multi-instance safe atomic claim-with-lease: SKIP LOCKED hands each
-- worker its own batch and the lease keeps a crashed worker's rows
-- recoverable; the per-product commerce coordination lock serializes
-- remote writes underneath.
UPDATE commerce_product_reevaluations
SET next_attempt_at = $2
WHERE product_id IN (
    SELECT product_id FROM commerce_product_reevaluations
    WHERE next_attempt_at <= now()
    ORDER BY next_attempt_at, product_id
    LIMIT $1
    FOR UPDATE SKIP LOCKED
)
RETURNING product_id, store_id, reason, attempts;

-- name: CompleteProductReevaluation :execrows
DELETE FROM commerce_product_reevaluations WHERE product_id = $1;

-- name: RetryProductReevaluation :execrows
UPDATE commerce_product_reevaluations
SET attempts = attempts + 1,
    next_attempt_at = $2,
    last_error_code = $3
WHERE product_id = $1;

-- name: CountProductReevaluations :one
SELECT count(*) FROM commerce_product_reevaluations;
