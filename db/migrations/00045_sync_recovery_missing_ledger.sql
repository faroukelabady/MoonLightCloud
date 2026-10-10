-- +goose Up
-- Phase 20 R6: server-owned missing-identity ledger.
--
-- WHY: Complete() previously trusted the client's last_missing claim, so a
-- device could finish a pass that the server itself had reported missing
-- identities for. verify_missing accumulates, per verification
-- generation, how many submitted identities Cloud did not hold (or held
-- with a different payload hash). Complete() refuses unless it is zero,
-- so the completeness proof no longer depends on a client assertion.
ALTER TABLE sync_recovery_state
    ADD COLUMN verify_missing BIGINT NOT NULL DEFAULT 0 CHECK (verify_missing >= 0);

-- +goose Down
ALTER TABLE sync_recovery_state DROP COLUMN IF EXISTS verify_missing;
