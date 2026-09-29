-- +goose Up
-- Phase 7C-L1: replace the vacuous pending/lease CHECK from 00017 (which
-- OR-ed tautologies and enforced nothing) with a named constraint that
-- actually holds the intended invariant: a pending command is never leased.
-- Committed migrations are frozen, so the old CHECK stays untouched; this
-- forward migration adds the real enforcement.
--
-- Fail-closed upgrade: the constraint is added NOT VALID (no scan, no
-- lock rewrite), then VALIDATE CONSTRAINT scans existing rows. If legacy
-- rows violate the invariant, validation aborts with a bounded diagnostic
-- naming this constraint and the whole upgrade rolls back without touching
-- frozen state. Operator handling: reconcile the offending pending
-- commands first (let leases expire and converge, or drive them terminal),
-- then re-run the upgrade. Never delete rows or rewrite terminal history.
-- (A DO-block pre-check would be friendlier, but goose splits statements
-- on semicolons; NOT VALID + VALIDATE keeps the migration parser-safe.)

ALTER TABLE device_control_commands
    ADD CONSTRAINT device_control_commands_pending_lease_null
    CHECK (status <> 'pending' OR lease_until IS NULL) NOT VALID;

ALTER TABLE device_control_commands
    VALIDATE CONSTRAINT device_control_commands_pending_lease_null;

-- +goose Down
ALTER TABLE device_control_commands
    DROP CONSTRAINT IF EXISTS device_control_commands_pending_lease_null;
