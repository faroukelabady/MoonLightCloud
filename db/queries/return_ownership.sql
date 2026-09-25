-- name: ClaimReturnOwnership :one
-- Durable arbitration (ADR-0027): the INSERT decides the permanent winner
-- for a return_refund_id. The bare ON CONFLICT absorbs either uniqueness
-- race — business-ID rivals on return_refund_id, and same-event replays on
-- winning_event_id — returning no row in both cases. Callers then read
-- explicit state instead of assuming failure. Never delete this table on
-- rebuild: projections are disposable, ownership is not.
INSERT INTO return_refund_ownership (return_refund_id, winning_event_id, winning_device_id, decided_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT DO NOTHING
RETURNING winning_event_id;

-- name: ReturnOwnershipByID :one
-- Read the durable winner inside the arbitration transaction (row lock keeps
-- concurrent arbiters serialized on the same return_refund_id).
SELECT return_refund_id, winning_event_id, winning_device_id, decided_at
FROM return_refund_ownership WHERE return_refund_id = $1
FOR UPDATE;

-- name: ReturnOwnershipByWinner :one
-- Reverse lookup for the impossible-invariant check: a winning event may
-- own exactly one return_refund_id. A hit here with a different business
-- ID is a deterministic integrity failure, never idempotent success.
SELECT return_refund_id, winning_event_id, winning_device_id, decided_at
FROM return_refund_ownership WHERE winning_event_id = $1;

-- name: ReturnOwnershipIntegrityMismatches :many
-- Operational integrity check: any projected return whose source event
-- differs from the durable winner is an integrity failure.
SELECT r.return_refund_id, r.source_event_id AS projected_source, o.winning_event_id
FROM return_refund_projection r
JOIN return_refund_ownership o USING (return_refund_id)
WHERE r.source_event_id <> o.winning_event_id;
