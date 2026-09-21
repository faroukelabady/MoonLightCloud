-- name: ClaimSaleOwnership :one
-- Durable arbitration (P2D-HIGH-02): the INSERT decides the permanent
-- winner for a sale_id. Exactly one event ever gets a row back; losers read
-- the existing winner in the same transaction. Never delete this table on
-- rebuild: projections are disposable, ownership is not.
INSERT INTO sale_event_ownership (sale_id, winning_event_id, winning_device_id, decided_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (sale_id) DO NOTHING
RETURNING winning_event_id;

-- name: SaleOwnershipBySaleID :one
-- Read the durable winner inside the arbitration transaction (row lock keeps
-- concurrent arbiters serialized on the same sale_id).
SELECT sale_id, winning_event_id, winning_device_id, decided_at
FROM sale_event_ownership WHERE sale_id = $1
FOR UPDATE;

-- name: OwnershipIntegrityMismatches :many
-- Operational integrity check (§53): any projected sale whose source event
-- differs from the durable winner is an integrity failure.
SELECT s.sale_id, s.source_event_id AS projected_source, o.winning_event_id
FROM sales_projection s
JOIN sale_event_ownership o USING (sale_id)
WHERE s.source_event_id <> o.winning_event_id;
