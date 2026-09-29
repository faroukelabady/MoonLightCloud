-- Phase 7C device control plane: presence + durable sync_now commands.

-- name: UpsertPresenceSeen :exec
INSERT INTO device_control_presence (device_id, last_seen_at, last_poll_at, created_at, updated_at)
VALUES ($1, $2, $3, $2, $2)
ON CONFLICT (device_id) DO UPDATE SET
    last_seen_at = GREATEST(device_control_presence.last_seen_at, EXCLUDED.last_seen_at),
    last_poll_at = EXCLUDED.last_poll_at,
    updated_at = EXCLUDED.updated_at;

-- name: TouchPresenceAccepted :exec
UPDATE device_control_presence
SET last_seen_at = GREATEST(last_seen_at, $2),
    last_command_accepted_at = $2,
    updated_at = $2
WHERE device_id = $1;

-- name: TouchPresenceFinished :exec
UPDATE device_control_presence
SET last_seen_at = GREATEST(last_seen_at, $2),
    last_command_finished_at = $2,
    updated_at = $2
WHERE device_id = $1;

-- name: GetPresence :one
SELECT device_id, last_seen_at, last_poll_at, last_command_accepted_at,
    last_command_finished_at, created_at, updated_at
FROM device_control_presence
WHERE device_id = $1;

-- name: ListPresence :many
SELECT device_id, last_seen_at, last_poll_at, last_command_accepted_at,
    last_command_finished_at, created_at, updated_at
FROM device_control_presence
ORDER BY device_id;

-- name: CreateControlCommand :one
INSERT INTO device_control_commands (id, device_id, command_type, command_version, idempotency_key, status, requested_at, created_at, updated_at)
VALUES ($1, $2, 'sync_now', 1, $3, 'pending', $4, $4, $4)
RETURNING id, device_id, command_type, command_version, idempotency_key, status,
    requested_at, leased_at, lease_until, lease_generation,
    accepted_at, running_at, finished_at, result_code, created_at, updated_at;

-- name: GetControlCommand :one
SELECT id, device_id, command_type, command_version, idempotency_key, status,
    requested_at, leased_at, lease_until, lease_generation,
    accepted_at, running_at, finished_at, result_code, created_at, updated_at
FROM device_control_commands
WHERE id = $1;

-- name: GetControlCommandForDevice :one
SELECT id, device_id, command_type, command_version, idempotency_key, status,
    requested_at, leased_at, lease_until, lease_generation,
    accepted_at, running_at, finished_at, result_code, created_at, updated_at
FROM device_control_commands
WHERE id = $1 AND device_id = $2;

-- name: GetCommandByIdempotency :one
SELECT id, device_id, command_type, command_version, idempotency_key, status,
    requested_at, leased_at, lease_until, lease_generation,
    accepted_at, running_at, finished_at, result_code, created_at, updated_at
FROM device_control_commands
WHERE device_id = $1 AND idempotency_key = $2;

-- name: GetActiveControlCommand :one
SELECT id, device_id, command_type, command_version, idempotency_key, status,
    requested_at, leased_at, lease_until, lease_generation,
    accepted_at, running_at, finished_at, result_code, created_at, updated_at
FROM device_control_commands
WHERE device_id = $1 AND status IN ('pending','leased','accepted','running')
ORDER BY requested_at, id
LIMIT 1;

-- name: ClaimControlCommand :one
SELECT id, device_id, command_type, command_version, idempotency_key, status,
    requested_at, leased_at, lease_until, lease_generation,
    accepted_at, running_at, finished_at, result_code, created_at, updated_at
FROM device_control_commands
WHERE device_id = $1
  AND (status = 'pending' OR (status = 'leased' AND lease_until <= $2))
ORDER BY requested_at, id
LIMIT 1
FOR UPDATE SKIP LOCKED;

-- name: LeaseControlCommand :one
UPDATE device_control_commands
SET status = 'leased', leased_at = $3, lease_until = $4,
    lease_generation = lease_generation + 1, updated_at = $3
WHERE id = $1 AND device_id = $2
  AND (status = 'pending' OR (status = 'leased' AND lease_until <= $3))
RETURNING id, device_id, command_type, command_version, idempotency_key, status,
    requested_at, leased_at, lease_until, lease_generation,
    accepted_at, running_at, finished_at, result_code, created_at, updated_at;

-- name: AcceptControlCommand :one
UPDATE device_control_commands
SET status = 'accepted', accepted_at = COALESCE(accepted_at, $3), updated_at = $3
WHERE id = $1 AND device_id = $2
  AND status IN ('pending','leased','accepted')
RETURNING id, device_id, command_type, command_version, idempotency_key, status,
    requested_at, leased_at, lease_until, lease_generation,
    accepted_at, running_at, finished_at, result_code, created_at, updated_at;

-- name: MarkControlRunning :one
UPDATE device_control_commands
SET status = 'running', running_at = COALESCE(running_at, $3), updated_at = $3
WHERE id = $1 AND device_id = $2
  AND status IN ('pending','leased','accepted','running')
RETURNING id, device_id, command_type, command_version, idempotency_key, status,
    requested_at, leased_at, lease_until, lease_generation,
    accepted_at, running_at, finished_at, result_code, created_at, updated_at;

-- name: FinishControlCommand :one
UPDATE device_control_commands
SET status = $3, finished_at = $4, result_code = $5, updated_at = $4
WHERE id = $1 AND device_id = $2
  AND status IN ('pending','leased','accepted','running')
RETURNING id, device_id, command_type, command_version, idempotency_key, status,
    requested_at, leased_at, lease_until, lease_generation,
    accepted_at, running_at, finished_at, result_code, created_at, updated_at;

-- name: ListRecentControlCommands :many
SELECT id, device_id, command_type, command_version, idempotency_key, status,
    requested_at, leased_at, lease_until, lease_generation,
    accepted_at, running_at, finished_at, result_code, created_at, updated_at
FROM device_control_commands
WHERE device_id = $1
ORDER BY requested_at DESC, id DESC
LIMIT $2;
