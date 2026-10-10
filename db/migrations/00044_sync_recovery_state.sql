-- +goose Up
-- Phase 20 R2: durable Cloud-restore recovery state.
--
-- WHY: a successful pg_restore can leave Cloud with a database that is
-- infrastructurally healthy (/health/ready 200) while its business
-- projections are historically incomplete - missing events Retail
-- already delivered and acknowledged. Nothing in the sync protocol
-- detected that condition, and the R1 ReconcileCloud primitive had no
-- trigger and no reachable call path.
--
-- This table makes the condition explicit and durable:
--   - the supported restore workflow marks every device's Store as
--     RECOVERY_RECONCILIATION_REQUIRED;
--   - Retail reads the flag over its normal control channel and refuses
--     to report full synchronization while it is set;
--   - reconciliation verifies continuity against retained Retail
--     history, resends what Cloud is missing, and clears the flag only
--     when verification reports zero missing events.
--
-- The state survives Cloud restarts (it is a table, not memory) and is
-- scoped per device so a Store with several devices cannot be marked
-- reconciled while one device is still unresolved.
CREATE TABLE sync_recovery_state (
    device_id UUID PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    required BOOLEAN NOT NULL DEFAULT FALSE,
    reason TEXT NOT NULL DEFAULT '' CHECK (length(reason) <= 200),
    started_at TIMESTAMPTZ,
    cleared_at TIMESTAMPTZ,
    verified_events BIGINT NOT NULL DEFAULT 0 CHECK (verified_events >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Phase 20 R3: server-side verification coverage ledger.
    --
    -- A device must not be able to clear RECOVERY_RECONCILIATION_REQUIRED
    -- by verifying only a subset of its retained history. The server
    -- therefore tracks the verification pass itself:
    --   verify_generation   increments whenever a pass restarts, so an
    --                       interrupted reconciliation resumes cleanly
    --                       instead of corrupting prior progress;
    --   verify_last_event_id the highest keyset position covered so far.
    --                       Pages MUST advance past it, so page 1 then
    --                       page 3 (skipping page 2) is rejected;
    --   verify_pages/events  bounded counters for operator evidence;
    --   verify_complete     set only when a page is submitted as
    --                       end_of_history. Complete() refuses to clear
    --                       the flag unless this is TRUE - a client can
    --                       never simply claim missing=0 and finish.
    verify_generation    BIGINT NOT NULL DEFAULT 0 CHECK (verify_generation >= 0),
    verify_last_event_id TEXT NOT NULL DEFAULT '' CHECK (length(verify_last_event_id) <= 64),
    verify_pages         INT NOT NULL DEFAULT 0 CHECK (verify_pages >= 0),
    verify_events        BIGINT NOT NULL DEFAULT 0 CHECK (verify_events >= 0),
    verify_complete      BOOLEAN NOT NULL DEFAULT FALSE,
    -- Phase 20 R3: the device declares its retained-history size when a
    -- verification pass begins. Complete() requires verify_events to
    -- reach that total, so skipping pages cannot look exhaustive.
    verify_declared_total BIGINT NOT NULL DEFAULT 0 CHECK (verify_declared_total >= 0)
);

CREATE INDEX idx_sync_recovery_required ON sync_recovery_state(required) WHERE required;

-- +goose Down
DROP TABLE IF EXISTS sync_recovery_state;
