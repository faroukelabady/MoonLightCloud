-- +goose Up
-- Phase 7D: durable operational incidents, alert deliveries, recovery
-- actions, and alert recipients. All operational audit/history: back them
-- up; no projection rebuild may delete them; no retention purge. Subjects
-- and notifications are logical UUID references (no destructive cascades):
-- incident history survives device revocation, notification changes, and
-- report-projection rebuilds. Bodies carry bounded machine summaries only:
-- no raw errors, customer payloads, provider bodies, or credentials.

CREATE TABLE operational_alert_recipients (
    id            UUID PRIMARY KEY,
    label         TEXT NOT NULL CHECK (char_length(label) BETWEEN 1 AND 64),
    provider_key  TEXT NOT NULL CHECK (provider_key ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    recipient     TEXT NOT NULL CHECK (char_length(recipient) BETWEEN 1 AND 32),
    locale        TEXT NOT NULL CHECK (locale IN ('ar', 'en')),
    enabled       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE operational_incidents (
    id               UUID PRIMARY KEY,
    rule_key         TEXT NOT NULL CHECK (rule_key ~ '^[A-Z][A-Z0-9_]{0,63}$'),
    subject_type     TEXT NOT NULL CHECK (subject_type IN ('device', 'sync_command', 'report_run', 'notification')),
    subject_id       TEXT NOT NULL CHECK (char_length(subject_id) BETWEEN 1 AND 64),
    severity         TEXT NOT NULL CHECK (severity IN ('warning', 'urgent')),
    state            TEXT NOT NULL CHECK (state IN ('open', 'acknowledged', 'resolved')),
    episode          INTEGER NOT NULL DEFAULT 1 CHECK (episode >= 1),
    source_event_key TEXT CHECK (source_event_key IS NULL OR char_length(source_event_key) BETWEEN 1 AND 128),
    opened_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    acknowledged_at  TIMESTAMPTZ,
    resolved_at      TIMESTAMPTZ,
    resolution_code  TEXT CHECK (resolution_code IS NULL OR (char_length(resolution_code) BETWEEN 1 AND 64 AND resolution_code ~ '^[A-Z][A-Z0-9_]{0,63}$')),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((state = 'resolved' AND resolved_at IS NOT NULL) OR (state <> 'resolved')),
    CHECK ((state = 'acknowledged' AND acknowledged_at IS NOT NULL) OR (state <> 'acknowledged'))
);

-- One active stateful incident per rule+subject; multi-instance open
-- converges here (INSERT ... ON CONFLICT DO NOTHING).
CREATE UNIQUE INDEX idx_operations_incident_active
    ON operational_incidents (rule_key, subject_type, subject_id)
    WHERE state IN ('open', 'acknowledged');

-- Terminal-event dedupe, all-time: resolving never recreates.
CREATE UNIQUE INDEX idx_operations_incident_event
    ON operational_incidents (source_event_key)
    WHERE source_event_key IS NOT NULL;

CREATE INDEX idx_operations_incidents_state ON operational_incidents (state, opened_at DESC, id DESC);
CREATE INDEX idx_operations_incidents_subject ON operational_incidents (subject_type, subject_id, opened_at DESC);

CREATE TABLE operational_alert_deliveries (
    id                         UUID PRIMARY KEY,
    incident_id                UUID NOT NULL,
    recipient_id               UUID NOT NULL,
    event_type                 TEXT NOT NULL CHECK (event_type IN ('opened', 'resolved')),
    provider_key_snapshot      TEXT NOT NULL,
    recipient_snapshot         TEXT NOT NULL,
    locale_snapshot            TEXT NOT NULL CHECK (locale_snapshot IN ('ar', 'en')),
    template_key               TEXT NOT NULL,
    body_snapshot              TEXT NOT NULL CHECK (char_length(body_snapshot) BETWEEN 1 AND 1024),
    body_fingerprint           BYTEA NOT NULL,
    notification_id            UUID,
    notification_idempotency_key TEXT NOT NULL CHECK (char_length(notification_idempotency_key) BETWEEN 1 AND 128),
    status                     TEXT NOT NULL CHECK (status IN ('pending', 'sent', 'blocked')),
    last_error_code            TEXT CHECK (last_error_code IS NULL OR char_length(last_error_code) BETWEEN 1 AND 64),
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (notification_idempotency_key)
);
CREATE INDEX idx_operations_deliveries_incident ON operational_alert_deliveries (incident_id, event_type);
CREATE INDEX idx_operations_deliveries_pending ON operational_alert_deliveries (status, created_at) WHERE status = 'pending';

CREATE TABLE operational_recovery_actions (
    id               UUID PRIMARY KEY,
    incident_id      UUID NOT NULL,
    action_type      TEXT NOT NULL CHECK (action_type = 'DEVICE_RECONNECT_SYNC'),
    state            TEXT NOT NULL CHECK (state IN ('pending', 'completed', 'blocked')),
    idempotency_key  TEXT NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
    target_entity_id TEXT CHECK (target_entity_id IS NULL OR char_length(target_entity_id) BETWEEN 1 AND 64),
    result_code      TEXT CHECK (result_code IS NULL OR (char_length(result_code) BETWEEN 1 AND 64 AND result_code ~ '^[A-Z][A-Z0-9_]{0,63}$')),
    attempt_count    INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at  TIMESTAMPTZ,
    last_error_code  TEXT CHECK (last_error_code IS NULL OR char_length(last_error_code) BETWEEN 1 AND 64),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (incident_id, action_type)
);
CREATE INDEX idx_operations_recovery_pending ON operational_recovery_actions (state, next_attempt_at, created_at) WHERE state = 'pending';

-- Detector scan support on frozen tables (read-only indexes; no
-- semantics change): failed-command and stale-command lookups must not
-- rescan full history every tick.
CREATE INDEX idx_operations_commands_failed ON device_control_commands (status, finished_at, id) WHERE status = 'failed';
CREATE INDEX idx_operations_commands_stale ON device_control_commands (status, requested_at, id) WHERE status IN ('pending', 'accepted', 'running');

-- +goose Down
DROP TABLE IF EXISTS operational_recovery_actions;
DROP TABLE IF EXISTS operational_alert_deliveries;
DROP TABLE IF EXISTS operational_incidents;
DROP TABLE IF EXISTS operational_alert_recipients;
