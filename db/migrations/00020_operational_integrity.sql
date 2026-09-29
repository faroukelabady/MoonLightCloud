-- +goose Up
-- Phase 7D-R1 operational integrity hardening. Append-only; frozen
-- migrations 00009–00019 are untouched.
--
-- 1. Delivery identity (H01): exactly one durable resolved/opened event
--    per (incident, event, recipient). Concurrent scanners converge here
--    instead of minting duplicate Phase 7A idempotency keys. The plain
--    ADD CONSTRAINT scans and fails closed naming this constraint if
--    duplicate identities already exist (possible only from concurrent
--    pre-fix writers); reconcile by keeping one delivery per identity,
--    then re-run. (NOT VALID is unavailable for UNIQUE constraints.)
-- 2. Intent materialization flags (M01): durable transactional-outbox
--    state distinguishing "intent missing, must repair" (FALSE) from
--    "intentionally empty snapshot" (TRUE with zero deliveries). Rows
--    written before this migration read FALSE and are repaired by the
--    detector without touching existing snapshots.
-- 3. Stale-command index (M04): leased commands join the overall-age
--    stale predicate; the replacement partial index covers them.

ALTER TABLE operational_incidents
    ADD COLUMN open_intent_materialized BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN resolved_intent_materialized BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE operational_alert_deliveries
    ADD CONSTRAINT operational_deliveries_identity_unique
    UNIQUE (incident_id, event_type, recipient_id);

DROP INDEX IF EXISTS idx_operations_commands_stale;
CREATE INDEX idx_operations_commands_stale ON device_control_commands (status, requested_at, id) WHERE status IN ('pending', 'leased', 'accepted', 'running');

-- Resolution round-robin: least-recently-checked active incidents first.
CREATE INDEX idx_operations_incidents_resolve ON operational_incidents (rule_key, last_observed_at, id) WHERE state IN ('open', 'acknowledged');

-- +goose Down
DROP INDEX IF EXISTS idx_operations_incidents_resolve;
DROP INDEX IF EXISTS idx_operations_commands_stale;
CREATE INDEX idx_operations_commands_stale ON device_control_commands (status, requested_at, id) WHERE status IN ('pending', 'accepted', 'running');
ALTER TABLE operational_alert_deliveries
    DROP CONSTRAINT IF EXISTS operational_deliveries_identity_unique;
ALTER TABLE operational_incidents
    DROP COLUMN IF EXISTS open_intent_materialized,
    DROP COLUMN IF EXISTS resolved_intent_materialized;
