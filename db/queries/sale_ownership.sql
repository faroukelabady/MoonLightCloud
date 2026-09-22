-- name: ClaimSaleOwnership :one
-- Durable arbitration (P2D-HIGH-02, P2E-MED-01): the INSERT decides the
-- permanent winner for a sale_id. The bare ON CONFLICT absorbs either
-- uniqueness race — same-sale rivals on sale_id, and same-event replays on
-- winning_event_id — returning no row in both cases. Callers must then
-- read explicit state (by sale, then by winner) instead of assuming
-- failure. Never delete this table on rebuild: projections are disposable,
-- ownership is not.
INSERT INTO sale_event_ownership (sale_id, winning_event_id, winning_device_id, decided_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT DO NOTHING
RETURNING winning_event_id;

-- name: SaleOwnershipBySaleID :one
-- Read the durable winner inside the arbitration transaction (row lock keeps
-- concurrent arbiters serialized on the same sale_id).
SELECT sale_id, winning_event_id, winning_device_id, decided_at
FROM sale_event_ownership WHERE sale_id = $1
FOR UPDATE;

-- name: SaleOwnershipByWinner :one
-- Reverse lookup for the impossible-invariant check: a winning event may
-- own exactly one sale_id. A hit here with a different sale_id is a
-- deterministic integrity failure, never idempotent success.
SELECT sale_id, winning_event_id, winning_device_id, decided_at
FROM sale_event_ownership WHERE winning_event_id = $1;

-- name: OwnershipIntegrityMismatches :many
-- Operational integrity check (§53): any projected sale whose source event
-- differs from the durable winner is an integrity failure.
SELECT s.sale_id, s.source_event_id AS projected_source, o.winning_event_id
FROM sales_projection s
JOIN sale_event_ownership o USING (sale_id)
WHERE s.source_event_id <> o.winning_event_id;
