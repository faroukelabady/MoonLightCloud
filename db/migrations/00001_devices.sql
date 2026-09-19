-- +goose Up
-- Phase 1A foundation: device identity only. Fresh MoonLightCloud history;
-- unrelated to any MoonLightRetail (desktop/SQLite) schema.
CREATE TABLE devices (
    id          UUID PRIMARY KEY,
    name        TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    status      TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
    secret_hash BYTEA NOT NULL,
    secret_salt BYTEA NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ,
    revoked_at  TIMESTAMPTZ
);
CREATE INDEX idx_devices_status ON devices (status);

-- +goose Down
DROP TABLE IF EXISTS devices;
