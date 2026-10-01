-- Phase 9A store registry + device bindings. Bindings are immutable:
-- inserts only, conflicts stay conflicts; enforced by PRIMARY KEY on
-- device_id (at most one binding per device).

-- name: InsertStore :execrows
INSERT INTO stores (id, display_name, timezone)
VALUES ($1, $2, $3)
ON CONFLICT (id) DO NOTHING;

-- name: UpdateStoreMetadata :exec
UPDATE stores
SET display_name = $2, timezone = $3, updated_at = now()
WHERE id = $1;

-- name: StoreByID :one
SELECT id, display_name, timezone, status, created_at, updated_at
FROM stores
WHERE id = $1;

-- name: ListStores :many
SELECT s.id, s.display_name, s.timezone, s.status, s.created_at, s.updated_at,
    COUNT(b.device_id)::bigint AS device_count
FROM stores s
LEFT JOIN device_store_bindings b ON b.store_id = s.id
GROUP BY s.id
ORDER BY s.created_at, s.id;

-- name: InsertDeviceBinding :exec
INSERT INTO device_store_bindings (device_id, store_id)
VALUES ($1, $2)
ON CONFLICT (device_id) DO NOTHING;

-- name: BindingByDevice :one
SELECT device_id, store_id, created_at
FROM device_store_bindings
WHERE device_id = $1;

-- name: DevicesByStore :many
SELECT device_id
FROM device_store_bindings
WHERE store_id = $1
ORDER BY device_id;

-- name: ListBindingsWithStores :many
SELECT b.device_id, b.store_id, s.display_name, s.status
FROM device_store_bindings b
JOIN stores s ON s.id = b.store_id
ORDER BY b.device_id;

-- name: LockStoreRegistrationDevice :one
SELECT status FROM devices WHERE id = $1 FOR UPDATE;
