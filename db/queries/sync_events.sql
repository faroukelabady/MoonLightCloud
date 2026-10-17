-- name: InsertSyncEvent :one
INSERT INTO sync_events
    (event_id, device_id, credential_id, event_type, occurred_at, received_at, payload, payload_hash, store_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (event_id) DO NOTHING
RETURNING event_id;

-- name: SyncEventByID :one
-- Full immutable identity for idempotency: device, type, instant, payload.
-- Credential identity and batch correlation are never part of event
-- identity. payload_hash is the exact canonical hash (ADR-0055).
SELECT event_id, device_id, event_type, occurred_at, payload, payload_hash
FROM sync_events WHERE event_id = $1;

-- name: SaleEventByID :one
-- Phase 9B: store_id is the trusted ingress context (NULL for legacy or
-- unbound events). Projectors copy it verbatim; they never re-derive it.
SELECT event_id, device_id, credential_id, event_type, occurred_at, received_at, payload, store_id
FROM sync_events WHERE event_id = $1;
