-- name: InsertSyncEvent :one
INSERT INTO sync_events
    (event_id, device_id, credential_id, event_type, occurred_at, received_at, payload, payload_hash, payload_hash_version)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (event_id) DO NOTHING
RETURNING event_id;

-- name: SyncEventByID :one
-- Full immutable identity for idempotency: device, type, instant, payload.
-- Credential identity and batch correlation are never part of event
-- identity. The stored immutable payload (not the v1 hash) is the
-- authoritative duplicate proof for pre-fix rows (fail-closed, ADR-0017+).
SELECT event_id, device_id, event_type, occurred_at, payload, payload_hash, payload_hash_version
FROM sync_events WHERE event_id = $1;

-- name: UpgradeSyncEventHash :exec
-- Opportunistic v1→v2 convergence after an exact stored-payload match:
-- only hash metadata changes, never the immutable payload. Guarded to v1
-- rows so concurrent upgrades write identical values idempotently.
UPDATE sync_events
SET payload_hash = $2, payload_hash_version = 2
WHERE event_id = $1 AND payload_hash_version = 1;

-- name: SaleEventByID :one
SELECT event_id, device_id, credential_id, event_type, occurred_at, received_at, payload
FROM sync_events WHERE event_id = $1;
