-- +goose Up
-- Phase 1B: separate device credentials from devices for lifecycle
-- (provisioning, rotation, per-credential revocation). Existing Phase-1A
-- device verifiers migrate losslessly as verifier_version=0 rows verified
-- with the exact legacy construction; no new v0 row is ever created.
CREATE TABLE device_credentials (
    id                UUID PRIMARY KEY,
    device_id         UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    salt              BYTEA NOT NULL,
    verifier          BYTEA NOT NULL,
    verifier_version  INT NOT NULL DEFAULT 1 CHECK (verifier_version IN (0, 1)),
    pepper_version    INT NOT NULL DEFAULT 1 CHECK (pepper_version >= 1),
    status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    activated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at        TIMESTAMPTZ,
    last_used_at      TIMESTAMPTZ
);
CREATE INDEX idx_device_credentials_device ON device_credentials (device_id, status);

INSERT INTO device_credentials
    (id, device_id, salt, verifier, verifier_version, pepper_version,
     status, created_at, activated_at, revoked_at)
SELECT gen_random_uuid(), id, secret_salt, secret_hash, 0, 1,
    CASE status WHEN 'active' THEN 'active' ELSE 'revoked' END,
    created_at, created_at,
    CASE status WHEN 'active' THEN NULL ELSE revoked_at END
FROM devices;

ALTER TABLE devices DROP COLUMN secret_hash;
ALTER TABLE devices DROP COLUMN secret_salt;

-- +goose Down
ALTER TABLE devices ADD COLUMN secret_hash BYTEA;
ALTER TABLE devices ADD COLUMN secret_salt BYTEA;
-- Best effort: restore the single active credential per device. Devices
-- with several credentials keep only the most recently activated one;
-- pre-production documented limitation.
UPDATE devices d SET
    secret_hash = c.verifier,
    secret_salt = c.salt
FROM device_credentials c
WHERE c.device_id = d.id AND c.status = 'active'
  AND c.activated_at = (
      SELECT MAX(activated_at) FROM device_credentials WHERE device_id = d.id AND status = 'active'
  );
DROP TABLE device_credentials;
