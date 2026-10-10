
-- name: UpsertSyncRecoveryState :exec
INSERT INTO sync_recovery_state (device_id, required, reason, started_at, updated_at)
VALUES ($1, TRUE, $2, now(), now())
ON CONFLICT (device_id) DO UPDATE
SET required = TRUE, reason = EXCLUDED.reason,
    started_at = COALESCE(sync_recovery_state.started_at, now()),
    cleared_at = NULL, updated_at = now();

-- name: ClearSyncRecoveryState :exec
UPDATE sync_recovery_state
SET required = FALSE, cleared_at = now(), verified_events = $2, updated_at = now()
WHERE device_id = $1;

-- name: GetSyncRecoveryState :one
SELECT device_id, required, reason, started_at, cleared_at, verified_events, updated_at
FROM sync_recovery_state
WHERE device_id = $1;

-- name: ListSyncRecoveryRequired :many
SELECT device_id, reason, started_at, verified_events
FROM sync_recovery_state
WHERE required = TRUE
ORDER BY started_at, device_id;

-- name: CountSyncRecoveryRequired :one
SELECT count(*) FROM sync_recovery_state WHERE required = TRUE;

-- Continuity verification: which of the reported event identities does
-- Cloud already hold? Identity is (event_id, payload_hash) - never a
-- fabricated sequence, and never count-only equality.
-- name: MissingSyncEvents :many
SELECT e.event_id
FROM unnest($1::uuid[]) AS q(event_id)
LEFT JOIN sync_events e ON e.event_id = q.event_id
WHERE e.event_id IS NULL;

-- name: CountSyncEventsForDevice :one
SELECT count(*) FROM sync_events WHERE device_id = $1;

-- name: SyncEventPayloadHash :one
SELECT payload_hash FROM sync_events WHERE event_id = $1;

-- Phase 20 R3: verification coverage ledger. Pages must ADVANCE the
-- keyset cursor; skipping a page or revisiting one is rejected, so an
-- incomplete walk can never look complete.

-- name: SyncRecoveryVerifyProgress :one
SELECT verify_generation, verify_last_event_id, verify_pages, verify_events, verify_complete, verify_declared_total
FROM sync_recovery_state WHERE device_id = $1;

-- name: SyncRecoveryBeginVerify :exec
UPDATE sync_recovery_state
SET verify_generation = verify_generation + 1,
    verify_last_event_id = '',
    verify_pages = 0, verify_events = 0, verify_complete = FALSE,
    verify_declared_total = $2,
    updated_at = now()
WHERE device_id = $1;

-- name: SyncRecoveryRecordVerifyPage :execrows
UPDATE sync_recovery_state
SET verify_last_event_id = $2,
    verify_pages = verify_pages + 1,
    verify_events = verify_events + $3,
    verify_complete = (verify_complete OR $4::bool),
    updated_at = now()
WHERE device_id = $1 AND verify_last_event_id < $2;

-- name: SyncRecoveryResetVerify :exec
UPDATE sync_recovery_state
SET verify_last_event_id = '', verify_pages = 0, verify_events = 0,
    verify_complete = FALSE, updated_at = now()
WHERE device_id = $1;

-- name: CountPendingProjections :one
-- §5: ingest is not projection. This counts processor rows still
-- retrying or blocked, so recovery completion can be qualified rather
-- than implying business projections have converged.
SELECT count(*) FROM sync_event_processing WHERE status IN ('retry', 'blocked');

-- name: CountDeviceSyncEvents :one
-- Phase 20 R5: independent lower bound on a device's retained history.
-- Every event Cloud accepted from this device necessarily came from that
-- device's durable outbox, so this count is a lower bound for the number
-- of retained rows the device must be able to enumerate. A verification
-- pass that declares fewer retained events than this is refused.
SELECT count(*) FROM sync_events WHERE device_id = $1;
