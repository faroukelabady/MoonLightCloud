-- name: ClaimProcessing :exec
-- Insert the processing row (first claimant wins; losers get no row back
-- but proceed to lock the existing row). The caller then inspects status:
-- processed/blocked/not-due rows are skipped by whoever lost the race,
-- keeping multi-instance projection safe without external locks.
INSERT INTO sync_event_processing AS p (event_id, processor, status)
VALUES ($1, $2, 'pending')
ON CONFLICT (event_id, processor) DO NOTHING;

-- name: LockProcessing :one
SELECT event_id, status, attempt_count, next_attempt_at
FROM sync_event_processing
WHERE event_id = $1 AND processor = $2
FOR UPDATE;

-- name: MarkProcessing :exec
UPDATE sync_event_processing
SET status = $3, attempt_count = $4, last_attempt_at = $5, next_attempt_at = $6,
    processed_at = $7, last_error_code = $8, last_error_message = $9, updated_at = now()
WHERE event_id = $1 AND processor = $2;

-- name: ProcessingStatus :many
SELECT status, count(*)::bigint AS total,
    COALESCE(max(last_error_code), '')::text AS err, max(updated_at)::timestamptz AS at
FROM sync_event_processing WHERE processor = $1
GROUP BY status;

-- name: OldestPendingAge :one
SELECT count(*)::bigint AS total, min(created_at)::timestamptz AS oldest
FROM sync_event_processing
WHERE processor = $1 AND status IN ('pending', 'retry');

-- name: LastProcessingError :one
SELECT event_id, last_error_code, last_error_message, updated_at
FROM sync_event_processing
WHERE processor = $1 AND status IN ('retry', 'blocked')
ORDER BY updated_at DESC LIMIT 1;

-- name: ResetProcessing :exec
UPDATE sync_event_processing
SET status = 'pending', next_attempt_at = NULL, updated_at = now()
WHERE event_id = $1 AND processor = $2 AND status IN ('retry', 'blocked');

-- name: PendingSaleEvents :many
-- Durable discovery: accepted sale events with no processing row (missing),
-- a pending row, or a due retry. Blocked and processed rows are never
-- returned; retry rows are returned only when next_attempt_at <= now (NULL
-- counts as due). Survivor of restarts; no in-memory signal required.
SELECT e.event_id
FROM sync_events e
LEFT JOIN sync_event_processing p
  ON p.event_id = e.event_id AND p.processor = $1
WHERE e.event_type = 'sale.finalized.v1'
  AND (p.event_id IS NULL
       OR p.status = 'pending'
       OR (p.status = 'retry' AND (p.next_attempt_at IS NULL OR p.next_attempt_at <= now())))
ORDER BY e.received_at
LIMIT $2;
