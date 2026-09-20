-- name: LockDevice :one
SELECT id FROM devices WHERE id = $1 FOR UPDATE;

-- name: CreateCredential :exec
INSERT INTO device_credentials
    (id, device_id, salt, verifier, verifier_version, pepper_version,
     status, created_at, activated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: CredentialByID :one
SELECT id, device_id, salt, verifier, verifier_version, pepper_version,
    status, created_at, activated_at, revoked_at, last_used_at
FROM device_credentials WHERE id = $1;

-- name: ActiveCredentialsForDevice :many
SELECT id, device_id, salt, verifier, verifier_version, pepper_version,
    status, created_at, activated_at, revoked_at, last_used_at
FROM device_credentials
WHERE device_id = $1 AND status = 'active'
ORDER BY activated_at;

-- name: CredentialsForDevice :many
SELECT id, device_id, salt, verifier, verifier_version, pepper_version,
    status, created_at, activated_at, revoked_at, last_used_at
FROM device_credentials
WHERE device_id = $1
ORDER BY activated_at;

-- name: TouchCredentialLastUsed :exec
UPDATE device_credentials SET last_used_at = $2 WHERE id = $1;

-- name: RevokeCredential :exec
UPDATE device_credentials SET status = 'revoked', revoked_at = $2 WHERE id = $1;

-- name: RevokeOtherCredentials :exec
UPDATE device_credentials
SET status = 'revoked', revoked_at = $3
WHERE device_id = $1 AND id <> $2 AND status = 'active';

-- name: RevokeDeviceCredentials :exec
UPDATE device_credentials
SET status = 'revoked', revoked_at = $2
WHERE device_id = $1 AND status = 'active';
