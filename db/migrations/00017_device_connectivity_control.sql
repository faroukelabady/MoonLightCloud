-- +goose Up
-- Phase 7C: durable device connectivity + remote sync control plane.
-- Retail initiates every connection (outbound HTTPS polling); Cloud never
-- dials Retail. Presence and commands are Cloud durable operational state:
-- back them up; no projection rebuild may delete them; no retention purge.
-- Commands carry no business payload in v1 (sync_now.v1 only); result codes
-- are bounded machine codes, never raw errors or customer data.

CREATE TABLE device_control_presence (
    device_id              UUID PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    last_seen_at           TIMESTAMPTZ,
    last_poll_at           TIMESTAMPTZ,
    last_command_accepted_at TIMESTAMPTZ,
    last_command_finished_at TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE device_control_commands (
    id               UUID PRIMARY KEY,
    device_id        UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    command_type     TEXT NOT NULL CHECK (command_type = 'sync_now'),
    command_version  INTEGER NOT NULL CHECK (command_version = 1),
    idempotency_key  TEXT NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128 AND idempotency_key ~ '^[A-Za-z0-9_:.~-]{1,128}$'),
    status           TEXT NOT NULL CHECK (status IN ('pending','leased','accepted','running','completed','failed')),
    requested_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    leased_at        TIMESTAMPTZ,
    lease_until      TIMESTAMPTZ,
    lease_generation BIGINT NOT NULL DEFAULT 0 CHECK (lease_generation >= 0),
    accepted_at      TIMESTAMPTZ,
    running_at       TIMESTAMPTZ,
    finished_at      TIMESTAMPTZ,
    result_code      TEXT CHECK (result_code IS NULL OR (char_length(result_code) BETWEEN 1 AND 64 AND result_code ~ '^[A-Z][A-Z0-9_]{0,63}$')),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((status IN ('pending') AND lease_until IS NULL) OR (status NOT IN ('pending')) OR (status = 'pending')),
    CHECK ((status IN ('completed','failed') AND finished_at IS NOT NULL AND result_code IS NOT NULL) OR (status NOT IN ('completed','failed')))
);

-- One active sync_now per device: correctness enforced by the database,
-- never by dashboard UI alone.
CREATE UNIQUE INDEX idx_device_commands_one_active
    ON device_control_commands (device_id)
    WHERE status IN ('pending','leased','accepted','running');

-- Dashboard idempotency: same device + same key returns the same command.
CREATE UNIQUE INDEX idx_device_commands_idempotency
    ON device_control_commands (device_id, idempotency_key);

-- Poll lookup: oldest due command per device (pending or expired lease).
CREATE INDEX idx_device_commands_poll
    ON device_control_commands (device_id, status, requested_at, id)
    WHERE status IN ('pending','leased');

-- Lease expiry scan.
CREATE INDEX idx_device_commands_lease
    ON device_control_commands (status, lease_until)
    WHERE status = 'leased';

-- Recent history per device.
CREATE INDEX idx_device_commands_history
    ON device_control_commands (device_id, requested_at DESC, id);

-- +goose Down
DROP TABLE IF EXISTS device_control_commands;
DROP TABLE IF EXISTS device_control_presence;
