-- name: CreateDevice :exec
INSERT INTO devices (id, name, status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5);

-- name: DeviceByID :one
SELECT id, name, status, created_at, updated_at, last_seen_at, revoked_at
FROM devices WHERE id = $1;

-- name: ListDevices :many
SELECT id, name, status, created_at, updated_at, last_seen_at, revoked_at
FROM devices ORDER BY created_at;

-- name: TouchDeviceLastSeen :exec
UPDATE devices SET last_seen_at = $2, updated_at = $2 WHERE id = $1;

-- name: RevokeDeviceRow :exec
UPDATE devices SET status = 'revoked', revoked_at = $2, updated_at = $2 WHERE id = $1;
