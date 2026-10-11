
-- name: UpsertSyncRecoveryState :exec
-- Marking (or re-marking) a device starts a fresh verification generation:
-- a ledger left over from an earlier, completed recovery must never let a
-- re-marked device complete without walking its history again.
INSERT INTO sync_recovery_state (device_id, required, reason, started_at, updated_at)
VALUES ($1, TRUE, $2, now(), now())
ON CONFLICT (device_id) DO UPDATE
SET required = TRUE, reason = EXCLUDED.reason,
    started_at = COALESCE(sync_recovery_state.started_at, now()),
    cleared_at = NULL, updated_at = now(),
    verify_generation = sync_recovery_state.verify_generation + 1,
    verify_last_event_id = '', verify_pages = 0, verify_events = 0,
    verify_complete = FALSE, verify_declared_total = 0, verify_missing = 0;

-- name: ClearSyncRecoveryState :execrows
-- Clearing is fenced to the exact ledger generation read and validated by
-- the service. Every authoritative completion predicate is repeated here,
-- in the same statement as the state transition, so a newer pass or restore
-- mark cannot be cleared by an older in-flight request. Projection
-- convergence remains a separate Store-level status condition.
UPDATE sync_recovery_state
SET required = FALSE, cleared_at = COALESCE(cleared_at, now()),
    verified_events = sqlc.arg(verified_events)::bigint, updated_at = now()
WHERE device_id = sqlc.arg(device_id)::uuid
  AND verify_generation = sqlc.arg(verify_generation)::bigint
  AND verify_pages > 0
  AND verify_complete
  AND verify_declared_total > 0
  AND verify_events >= verify_declared_total
  AND verify_events = sqlc.arg(verified_events)::bigint
  AND verify_missing = 0;

-- name: GetSyncRecoveryState :one
SELECT device_id, required, reason, started_at, cleared_at, verified_events, updated_at
FROM sync_recovery_state
WHERE device_id = $1;

-- name: ListSyncRecoveryRequired :many
SELECT device_id, reason, started_at, verified_events
FROM sync_recovery_state
WHERE required = TRUE
ORDER BY started_at, device_id;

-- name: CountStoreSyncRecoveryRequired :one
-- Phase 20 R6: unresolved devices of the REQUESTING device's Store only
-- (never another Store's), counting active devices. A revoked device can
-- never reconcile; revocation is the operator's explicit resolution for a
-- retired or replaced workstation (its retained history travels with the
-- restored Retail database to the replacement device).
SELECT count(*)
FROM sync_recovery_state r
JOIN devices d ON d.id = r.device_id AND d.status = 'active'
WHERE r.required
  AND (r.device_id = sqlc.arg(device_id)::uuid
       OR r.device_id IN (
           SELECT b.device_id FROM device_store_bindings b
           WHERE b.store_id = (SELECT s.store_id FROM device_store_bindings s
                               WHERE s.device_id = sqlc.arg(device_id)::uuid)));

-- Continuity verification: the stored identity of each reported event.
-- Identity is (event_id, payload_hash); an absent row or a different hash
-- is reported missing. One bounded query per page (no N+1).
-- name: SyncEventHashes :many
-- The authenticated device's binding is resolved server-side. All event
-- rows belonging to that Store remain visible for supported replacement
-- devices. Legacy rows without a Store are matchable only by their original
-- device; unbound callers therefore receive no identity matches.
SELECT e.event_id, e.payload_hash
FROM sync_events e
WHERE e.event_id = ANY(sqlc.arg(event_ids)::uuid[])
  AND EXISTS (
      SELECT 1
      FROM device_store_bindings b
      WHERE b.device_id = sqlc.arg(device_id)::uuid
        AND (e.store_id = b.store_id
             OR (e.store_id IS NULL AND e.device_id = b.device_id))
  );

-- name: CountSyncEventsForDevice :one
SELECT count(*) FROM sync_events WHERE device_id = $1;

-- Phase 20 R3/R6: verification coverage ledger. Pages must ADVANCE the
-- keyset cursor in BYTE order (COLLATE "C"), the same order Retail's
-- SQLite BINARY collation and Go string comparison use, so the cursor can
-- never disagree with the client's keyset because of a database locale.

-- name: SyncRecoveryVerifyProgress :one
SELECT verify_generation, verify_last_event_id, verify_pages, verify_events, verify_complete,
       verify_declared_total, verify_missing
FROM sync_recovery_state WHERE device_id = $1;

-- name: SyncRecoveryBeginVerify :one
UPDATE sync_recovery_state AS recovery
SET verify_generation = verify_generation + 1,
    verify_last_event_id = '',
    verify_pages = 0, verify_events = 0, verify_complete = FALSE,
    verify_declared_total = GREATEST(
      sqlc.arg(verify_declared_total)::bigint,
      (SELECT count(*) FROM sync_events e WHERE e.device_id = recovery.device_id)
    ), verify_missing = 0,
    updated_at = now()
WHERE recovery.device_id = sqlc.arg(device_id)::uuid
RETURNING verify_generation;

-- name: SyncRecoveryRecordVerifyPage :execrows
UPDATE sync_recovery_state
SET verify_last_event_id = sqlc.arg(last_event_id)::text,
    verify_pages = verify_pages + 1,
    verify_events = verify_events + sqlc.arg(events)::bigint,
    verify_missing = verify_missing + sqlc.arg(missing)::bigint,
    verify_complete = (verify_complete OR sqlc.arg(end_of_history)::bool),
    updated_at = now()
WHERE device_id = sqlc.arg(device_id)::uuid
  AND verify_generation = sqlc.arg(verify_generation)::bigint
  AND verify_last_event_id COLLATE "C" < sqlc.arg(first_event_id)::text COLLATE "C"
  AND sqlc.arg(first_event_id)::text COLLATE "C" <= sqlc.arg(last_event_id)::text COLLATE "C";

-- name: CountStorePendingProjections :one
-- §5: ingest is not projection. Count every accepted event's canonical
-- required processor when it has no row or is not processed. Event/version
-- mapping prevents an unrelated processor from implying convergence.
WITH required_processor(event_type, processor) AS (VALUES
    ('sale.finalized.v1', 'sale_projection.v1'),
    ('sale.finalized.v2', 'sale_projection.v2'),
    ('sale.finalized.v3', 'sale_projection.v3'),
    ('sale.return_refund.finalized.v1', 'return_refund_projection.v1'),
    ('catalog.category.snapshot.v1', 'catalog_category_projection.v1'),
    ('catalog.category.snapshot.v2', 'catalog_category_projection.v1'),
    ('catalog.tag.snapshot.v1', 'catalog_tag_projection.v1'),
    ('catalog.product.snapshot.v1', 'catalog_product_projection.v1'),
    ('catalog.product.snapshot.v2', 'catalog_product_projection.v1'),
    ('catalog.product.sales_policy.snapshot.v1', 'catalog_product_sales_policy_projection.v1'),
    ('inventory.product.snapshot.v1', 'inventory_product_projection.v1'),
    ('catalog.product.configuration.snapshot.v1', 'catalog_product_configuration_projection.v1'),
    ('catalog.product_variant.snapshot.v1', 'catalog_product_variant_projection.v1'),
    ('catalog.product_type.snapshot.v1', 'catalog_product_type_projection.v1'),
    ('inventory.product_variant.snapshot.v1', 'inventory_product_variant_projection.v1')
)
SELECT count(*)::bigint
FROM sync_events e
JOIN required_processor r ON r.event_type = e.event_type
LEFT JOIN sync_event_processing p
  ON p.event_id = e.event_id AND p.processor = r.processor
WHERE p.status IS DISTINCT FROM 'processed'
  AND (e.device_id = sqlc.arg(device_id)::uuid
       OR e.store_id = (SELECT s.store_id FROM device_store_bindings s
                        WHERE s.device_id = sqlc.arg(device_id)::uuid));
