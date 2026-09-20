-- name: InsertSyncEvent :one
INSERT INTO sync_events
    (event_id, device_id, credential_id, event_type, occurred_at, received_at, payload, payload_hash)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (event_id) DO NOTHING
RETURNING event_id;

-- name: SyncEventByID :one
SELECT event_id, device_id, payload_hash
FROM sync_events WHERE event_id = $1;
