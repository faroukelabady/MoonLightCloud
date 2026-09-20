-- +goose Up
-- Phase 1B: durable sync event ingestion. Accepted events are immutable
-- business-integration history; no projection tables yet. Globally unique
-- event_id enforced by PRIMARY KEY so concurrent duplicates stay safe;
-- device ownership recorded but never trusted from client input.
CREATE TABLE sync_events (
    event_id      UUID PRIMARY KEY,
    device_id     UUID NOT NULL REFERENCES devices(id) ON DELETE RESTRICT,
    credential_id UUID REFERENCES device_credentials(id) ON DELETE SET NULL,
    event_type    TEXT NOT NULL CHECK (event_type ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*\.v[0-9]+$'),
    occurred_at   TIMESTAMPTZ NOT NULL,
    received_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    payload       JSONB NOT NULL,
    payload_hash  BYTEA NOT NULL
);
CREATE INDEX idx_sync_events_device_received ON sync_events (device_id, received_at);
CREATE INDEX idx_sync_events_type ON sync_events (event_type);

-- +goose Down
DROP TABLE IF EXISTS sync_events;
