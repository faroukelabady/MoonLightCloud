-- +goose Up
-- Phase 7B: durable scheduled business reports. Recipients, schedules,
-- runs, and per-recipient deliveries are Cloud-only durable state:
-- back them up; no catalog/policy/inventory/order/sale rebuild may
-- delete them, and there is no automatic retention purge. All
-- financial calculations stay in the frozen reporting domain: this
-- migration stores only configuration, slot/run identity, immutable
-- snapshots, and delivery records. No destructive FKs to rebuildable
-- projections; notification linkage is a logical UUID reference.

CREATE TABLE business_report_recipients (
    id            UUID PRIMARY KEY,
    label         TEXT NOT NULL CHECK (char_length(label) BETWEEN 1 AND 64),
    provider_key  TEXT NOT NULL CHECK (provider_key ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    recipient     TEXT NOT NULL CHECK (char_length(recipient) BETWEEN 1 AND 32),
    locale        TEXT NOT NULL CHECK (locale IN ('ar', 'en')),
    enabled       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE business_report_schedules (
    id                  UUID PRIMARY KEY,
    name                TEXT NOT NULL UNIQUE CHECK (char_length(name) BETWEEN 1 AND 64),
    report_kind         TEXT NOT NULL CHECK (report_kind IN ('DAILY', 'TEN_DAY')),
    timezone            TEXT NOT NULL CHECK (timezone = 'Africa/Cairo'),
    local_time          TEXT NOT NULL CHECK (local_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    anchor_local_date   DATE,
    enabled             BOOLEAN NOT NULL DEFAULT TRUE,
    revision            BIGINT NOT NULL DEFAULT 1 CHECK (revision >= 1),
    next_run_local_date DATE NOT NULL,
    next_run_at         TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((report_kind = 'TEN_DAY' AND anchor_local_date IS NOT NULL) OR
           (report_kind = 'DAILY'))
);
CREATE INDEX idx_report_schedules_due ON business_report_schedules (enabled, next_run_at);

CREATE TABLE business_report_schedule_recipients (
    schedule_id  UUID NOT NULL REFERENCES business_report_schedules(id) ON DELETE CASCADE,
    recipient_id UUID NOT NULL REFERENCES business_report_recipients(id),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (schedule_id, recipient_id)
);

CREATE TABLE business_report_runs (
    id                     UUID PRIMARY KEY,
    schedule_id            UUID NOT NULL REFERENCES business_report_schedules(id),
    run_kind               TEXT NOT NULL CHECK (run_kind IN ('scheduled', 'manual')),
    slot_local_date        DATE,
    manual_idempotency_key TEXT CHECK (manual_idempotency_key IS NULL OR char_length(manual_idempotency_key) BETWEEN 1 AND 128),
    scheduled_for          TIMESTAMPTZ NOT NULL,
    period_start           TIMESTAMPTZ NOT NULL,
    period_end             TIMESTAMPTZ NOT NULL,
    schedule_revision      BIGINT NOT NULL CHECK (schedule_revision >= 1),
    status                 TEXT NOT NULL DEFAULT 'pending'
                           CHECK (status IN ('pending', 'retry', 'completed', 'blocked')),
    attempt_count          INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at        TIMESTAMPTZ,
    lease_owner            TEXT,
    lease_until            TIMESTAMPTZ,
    lease_generation       BIGINT NOT NULL DEFAULT 0 CHECK (lease_generation >= 0),
    last_error_code        TEXT,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((run_kind = 'scheduled' AND slot_local_date IS NOT NULL AND manual_idempotency_key IS NULL) OR
           (run_kind = 'manual' AND slot_local_date IS NULL AND manual_idempotency_key IS NOT NULL))
);
CREATE UNIQUE INDEX idx_report_runs_slot ON business_report_runs (schedule_id, slot_local_date)
    WHERE run_kind = 'scheduled';
CREATE UNIQUE INDEX idx_report_runs_manual ON business_report_runs (schedule_id, manual_idempotency_key)
    WHERE run_kind = 'manual';
CREATE INDEX idx_report_runs_due ON business_report_runs (status, next_attempt_at, created_at);

CREATE TABLE business_report_deliveries (
    id                          UUID PRIMARY KEY,
    run_id                      UUID NOT NULL REFERENCES business_report_runs(id) ON DELETE CASCADE,
    recipient_id                UUID NOT NULL,
    provider_key                TEXT NOT NULL,
    recipient_snapshot          TEXT NOT NULL,
    locale_snapshot             TEXT NOT NULL,
    template_key                TEXT NOT NULL,
    report_body_snapshot        TEXT,
    report_fingerprint          BYTEA,
    notification_idempotency_key TEXT NOT NULL,
    notification_id             UUID,
    status                      TEXT NOT NULL DEFAULT 'pending'
                                CHECK (status IN ('pending', 'enqueued', 'blocked')),
    last_error_code             TEXT,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (run_id, recipient_id),
    UNIQUE (notification_idempotency_key)
);
CREATE INDEX idx_report_deliveries_run ON business_report_deliveries (run_id);

-- +goose Down
-- Development/test repair only: report scheduler state is durable
-- business state. Never roll back in production.
DROP TABLE IF EXISTS business_report_deliveries;
DROP TABLE IF EXISTS business_report_runs;
DROP TABLE IF EXISTS business_report_schedule_recipients;
DROP TABLE IF EXISTS business_report_schedules;
DROP TABLE IF EXISTS business_report_recipients;
