-- +goose Up
-- Phase 2B: durable projector processing state. Small status model:
-- pending (claimed work), retry (transient failure, due at next_attempt_at),
-- blocked (deterministic integrity failure, needs operator), processed.
-- Rows are created lazily by the projector's durable discovery scan, so no
-- ingestion-transaction coupling is required for crash safety.
CREATE TABLE sync_event_processing (
    event_id         UUID NOT NULL REFERENCES sync_events(event_id) ON DELETE CASCADE,
    processor        TEXT NOT NULL,
    status           TEXT NOT NULL CHECK (status IN ('pending', 'retry', 'blocked', 'processed')),
    attempt_count    INT NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    last_attempt_at  TIMESTAMPTZ,
    next_attempt_at  TIMESTAMPTZ,
    processed_at     TIMESTAMPTZ,
    last_error_code  TEXT,
    last_error_message TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, processor)
);
CREATE INDEX idx_processing_due ON sync_event_processing (processor, status, next_attempt_at);

-- +goose Down
DROP TABLE IF EXISTS sync_event_processing;
