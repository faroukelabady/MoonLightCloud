-- +goose Up
-- Phase 7A: provider-neutral notification domain with WhatsApp as the
-- first adapter. notification_template_mappings resolves logical
-- (template_key, locale) to approved external templates so future
-- business phases never embed Meta names. notification_messages is the
-- durable idempotent outbox with lease fencing and a durable
-- send-start marker: an unknown remote outcome is AMBIGUOUS, never
-- auto-retried. notification_delivery_status_history records provider
-- callbacks idempotently. All three are Cloud-only durable integration
-- state: back them up; no catalog/policy/inventory/order rebuild may
-- delete them, and there is no automatic retention purge.

CREATE TABLE notification_template_mappings (
    provider_key           TEXT NOT NULL CHECK (provider_key ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    template_key           TEXT NOT NULL CHECK (template_key ~ '^[a-z0-9][a-z0-9_]{0,63}$'),
    locale                 TEXT NOT NULL CHECK (char_length(locale) BETWEEN 2 AND 12),
    external_template_name TEXT NOT NULL CHECK (char_length(external_template_name) BETWEEN 1 AND 128),
    external_language_code TEXT NOT NULL CHECK (char_length(external_language_code) BETWEEN 1 AND 16),
    parameter_names        TEXT[] NOT NULL DEFAULT '{}',
    enabled                BOOLEAN NOT NULL DEFAULT TRUE,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider_key, template_key, locale)
);

CREATE TABLE notification_messages (
    id                     UUID PRIMARY KEY,
    provider_key           TEXT NOT NULL CHECK (provider_key ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    idempotency_key        TEXT NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
    semantic_fingerprint   BYTEA NOT NULL,
    recipient              TEXT NOT NULL CHECK (char_length(recipient) BETWEEN 1 AND 32),
    template_key           TEXT NOT NULL CHECK (template_key ~ '^[a-z0-9][a-z0-9_]{0,63}$'),
    locale                 TEXT NOT NULL CHECK (char_length(locale) BETWEEN 2 AND 12),
    parameters             JSONB NOT NULL DEFAULT '{}',
    ext_template_name      TEXT NOT NULL CHECK (char_length(ext_template_name) BETWEEN 1 AND 128),
    ext_language_code      TEXT NOT NULL CHECK (char_length(ext_language_code) BETWEEN 1 AND 16),
    ext_parameter_order    TEXT[] NOT NULL DEFAULT '{}',
    dispatch_status        TEXT NOT NULL DEFAULT 'pending'
                           CHECK (dispatch_status IN ('pending', 'retry', 'accepted', 'blocked', 'ambiguous')),
    attempt_count          INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at        TIMESTAMPTZ,
    lease_owner            TEXT,
    lease_until            TIMESTAMPTZ,
    lease_generation       BIGINT NOT NULL DEFAULT 0 CHECK (lease_generation >= 0),
    send_started_at        TIMESTAMPTZ,
    provider_message_id    TEXT CHECK (provider_message_id IS NULL OR char_length(provider_message_id) BETWEEN 1 AND 256),
    delivery_status        TEXT NOT NULL DEFAULT 'UNKNOWN'
                           CHECK (delivery_status IN ('UNKNOWN', 'ACCEPTED', 'SENT', 'DELIVERED', 'READ', 'FAILED')),
    delivery_status_at     TIMESTAMPTZ,
    last_error_code        TEXT,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider_key, idempotency_key),
    UNIQUE (provider_key, provider_message_id)
);
CREATE INDEX idx_notification_due ON notification_messages (dispatch_status, next_attempt_at, created_at);
CREATE INDEX idx_notification_lease ON notification_messages (lease_until) WHERE dispatch_status IN ('pending', 'retry');

CREATE TABLE notification_delivery_status_history (
    notification_id      UUID NOT NULL REFERENCES notification_messages(id),
    provider_key         TEXT NOT NULL,
    provider_message_id  TEXT NOT NULL,
    provider_status_raw  TEXT NOT NULL CHECK (char_length(provider_status_raw) BETWEEN 1 AND 64),
    canonical_status     TEXT NOT NULL
                         CHECK (canonical_status IN ('UNKNOWN', 'ACCEPTED', 'SENT', 'DELIVERED', 'READ', 'FAILED')),
    provider_timestamp   TIMESTAMPTZ,
    event_fingerprint    BYTEA NOT NULL,
    provider_error_code  TEXT CHECK (provider_error_code IS NULL OR char_length(provider_error_code) BETWEEN 1 AND 64),
    observed_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (notification_id, event_fingerprint)
);
CREATE INDEX idx_notification_history_lookup ON notification_delivery_status_history (provider_key, provider_message_id);

-- +goose Down
-- Development/test repair only: notifications are durable integration
-- state. Never roll back in production.
DROP TABLE IF EXISTS notification_delivery_status_history;
DROP TABLE IF EXISTS notification_messages;
DROP TABLE IF EXISTS notification_template_mappings;
