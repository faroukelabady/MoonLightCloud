-- Scheduled business reports (Phase 7B): recipients, schedules,
-- runs, and per-recipient deliveries. One statement per query: sqlc
-- :exec drops trailing statements silently.

-- name: CreateRecipient :one
INSERT INTO business_report_recipients (id, label, provider_key, recipient, locale, enabled)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id;

-- name: GetRecipient :one
SELECT id, label, provider_key, recipient, locale, enabled
FROM business_report_recipients
WHERE id = $1;

-- name: GetRecipientForUpdate :one
SELECT id, label, provider_key, recipient, locale, enabled
FROM business_report_recipients
WHERE id = $1
FOR UPDATE;

-- name: ListRecipients :many
SELECT id, label, provider_key, recipient, locale, enabled
FROM business_report_recipients
ORDER BY created_at, id;

-- name: SetRecipientEnabled :exec
UPDATE business_report_recipients
SET enabled = $2, updated_at = now()
WHERE id = $1;

-- name: CreateSchedule :exec
INSERT INTO business_report_schedules (
    id, name, report_kind, timezone, local_time, anchor_local_date,
    revision, next_run_local_date, next_run_at
) VALUES ($1, $2, $3, $4, $5, $6, 1, $7, $8);

-- name: GetSchedule :one
SELECT id, name, report_kind, timezone, local_time, anchor_local_date,
    enabled, revision, next_run_local_date, next_run_at
FROM business_report_schedules
WHERE id = $1;

-- name: ListSchedules :many
SELECT id, name, report_kind, timezone, local_time, anchor_local_date,
    enabled, revision, next_run_local_date, next_run_at
FROM business_report_schedules
ORDER BY created_at, id;

-- name: UpdateScheduleEnablement :exec
UPDATE business_report_schedules
SET enabled = $2, revision = revision + 1,
    next_run_local_date = $3, next_run_at = $4, updated_at = now()
WHERE id = $1;

-- name: EnableScheduleIfDisabled :one
UPDATE business_report_schedules
SET enabled = TRUE, revision = revision + 1,
    next_run_local_date = $2, next_run_at = $3, updated_at = now()
WHERE id = $1 AND enabled = FALSE
RETURNING id, name, report_kind, timezone, local_time, anchor_local_date,
    enabled, revision, next_run_local_date, next_run_at;

-- name: GetScheduleForUpdate :one
SELECT id, name, report_kind, timezone, local_time, anchor_local_date,
    enabled, revision, next_run_local_date, next_run_at
FROM business_report_schedules
WHERE id = $1
FOR UPDATE;

-- name: ClaimDueSchedules :many
WITH candidate AS (
    SELECT id
    FROM business_report_schedules
    WHERE enabled AND next_run_at <= now()
    ORDER BY next_run_at, id
    LIMIT $1
    FOR UPDATE SKIP LOCKED
)
SELECT id, name, report_kind, timezone, local_time, anchor_local_date,
    enabled, revision, next_run_local_date, next_run_at
FROM business_report_schedules
WHERE id IN (SELECT id FROM candidate);

-- name: AdvanceScheduleNextRun :exec
UPDATE business_report_schedules
SET next_run_local_date = $2, next_run_at = $3, updated_at = now()
WHERE id = $1;

-- name: LinkScheduleRecipient :exec
INSERT INTO business_report_schedule_recipients (schedule_id, recipient_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: ListScheduleRecipients :many
SELECT r.id, r.label, r.provider_key, r.recipient, r.locale, r.enabled
FROM business_report_recipients AS r
JOIN business_report_schedule_recipients AS link ON link.recipient_id = r.id
WHERE link.schedule_id = $1
ORDER BY r.created_at, r.id;

-- name: InsertScheduledRun :one
INSERT INTO business_report_runs (
    id, schedule_id, run_kind, slot_local_date, scheduled_for,
    period_start, period_end, schedule_revision
) VALUES ($1, $2, 'scheduled', $3, $4, $5, $6, $7)
ON CONFLICT DO NOTHING
RETURNING id;

-- name: GetScheduledRun :one
SELECT id, schedule_id, run_kind, slot_local_date, manual_idempotency_key,
    scheduled_for, period_start, period_end, schedule_revision,
    status, attempt_count, next_attempt_at,
    lease_owner, lease_until, lease_generation, last_error_code
FROM business_report_runs
WHERE schedule_id = $1 AND slot_local_date = $2 AND run_kind = 'scheduled';

-- name: InsertManualRun :one
INSERT INTO business_report_runs (
    id, schedule_id, run_kind, manual_idempotency_key, scheduled_for,
    period_start, period_end, schedule_revision
) VALUES ($1, $2, 'manual', $3, $4, $5, $6, $7)
ON CONFLICT DO NOTHING
RETURNING id;

-- name: GetManualRun :one
SELECT id, schedule_id, run_kind, slot_local_date, manual_idempotency_key,
    scheduled_for, period_start, period_end, schedule_revision,
    status, attempt_count, next_attempt_at,
    lease_owner, lease_until, lease_generation, last_error_code
FROM business_report_runs
WHERE schedule_id = $1 AND manual_idempotency_key = $2 AND run_kind = 'manual';

-- name: GetRun :one
SELECT id, schedule_id, run_kind, slot_local_date, manual_idempotency_key,
    scheduled_for, period_start, period_end, schedule_revision,
    status, attempt_count, next_attempt_at,
    lease_owner, lease_until, lease_generation, last_error_code
FROM business_report_runs
WHERE id = $1;

-- name: ListRuns :many
SELECT id, schedule_id, run_kind, slot_local_date, manual_idempotency_key,
    scheduled_for, period_start, period_end, schedule_revision,
    status, attempt_count, next_attempt_at,
    lease_owner, lease_until, lease_generation, last_error_code
FROM business_report_runs
ORDER BY created_at DESC, id DESC
LIMIT $1;

-- name: ClaimRun :one
WITH candidate AS (
    SELECT id
    FROM business_report_runs
    WHERE status IN ('pending', 'retry')
      AND (next_attempt_at IS NULL OR next_attempt_at <= now())
      AND (lease_until IS NULL OR lease_until <= now())
    ORDER BY next_attempt_at NULLS FIRST, created_at, id
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
UPDATE business_report_runs AS run
SET lease_owner = $1, lease_until = $2,
    lease_generation = run.lease_generation + 1,
    attempt_count = run.attempt_count + 1, updated_at = now()
FROM candidate
WHERE run.id = candidate.id
RETURNING run.id, run.schedule_id, run.run_kind, run.slot_local_date,
    run.manual_idempotency_key, run.scheduled_for, run.period_start,
    run.period_end, run.schedule_revision, run.status, run.attempt_count,
    run.lease_owner, run.lease_generation;

-- name: FinishRunCompleted :execrows
UPDATE business_report_runs
SET status = 'completed',
    last_error_code = NULL, lease_owner = NULL, lease_until = NULL,
    updated_at = now()
WHERE id = $1 AND lease_owner = $2 AND lease_generation = $3
  AND status IN ('pending', 'retry');

-- name: FinishRunRetry :execrows
UPDATE business_report_runs
SET status = 'retry', next_attempt_at = $4,
    last_error_code = $5, lease_owner = NULL, lease_until = NULL,
    updated_at = now()
WHERE id = $1 AND lease_owner = $2 AND lease_generation = $3
  AND status IN ('pending', 'retry');

-- name: FinishRunBlocked :execrows
UPDATE business_report_runs
SET status = 'blocked',
    last_error_code = $4, lease_owner = NULL, lease_until = NULL,
    updated_at = now()
WHERE id = $1 AND lease_owner = $2 AND lease_generation = $3
  AND status IN ('pending', 'retry');

-- name: InsertDelivery :exec
INSERT INTO business_report_deliveries (
    id, run_id, recipient_id, provider_key, recipient_snapshot,
    locale_snapshot, template_key, notification_idempotency_key
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT DO NOTHING;

-- name: ListRunDeliveries :many
SELECT id, run_id, recipient_id, provider_key, recipient_snapshot,
    locale_snapshot, template_key, report_body_snapshot, report_fingerprint,
    notification_idempotency_key, notification_id, status, last_error_code
FROM business_report_deliveries
WHERE run_id = $1
ORDER BY created_at, id;

-- name: PersistDeliverySnapshot :execrows
UPDATE business_report_deliveries AS delivery
SET report_body_snapshot = $2, report_fingerprint = $3, updated_at = now()
WHERE delivery.id = $1 AND delivery.report_body_snapshot IS NULL
  AND EXISTS (SELECT 1 FROM business_report_runs AS run
    WHERE run.id = delivery.run_id AND run.id = $4
      AND run.lease_owner = $5 AND run.lease_generation = $6
      AND run.status IN ('pending', 'retry')
      AND run.lease_until > clock_timestamp());

-- name: GetRunForUpdate :one
SELECT id, schedule_id, run_kind, slot_local_date, manual_idempotency_key,
    scheduled_for, period_start, period_end, schedule_revision,
    status, attempt_count, next_attempt_at,
    lease_owner, lease_until, lease_generation, last_error_code
FROM business_report_runs
WHERE id = $1
FOR UPDATE;

-- name: GetDeliveryForUpdate :one
SELECT id, run_id, recipient_id, provider_key, recipient_snapshot,
    locale_snapshot, template_key, report_body_snapshot, report_fingerprint,
    notification_idempotency_key, notification_id, status, last_error_code
FROM business_report_deliveries
WHERE id = $1
FOR UPDATE;

-- name: FinishDeliveryEnqueued :execrows
UPDATE business_report_deliveries AS delivery
SET status = 'enqueued', notification_id = $2, last_error_code = NULL,
    updated_at = now()
FROM business_report_runs AS run
WHERE delivery.id = $1 AND delivery.status = 'pending'
  AND run.id = delivery.run_id AND run.id = $3
  AND run.lease_owner = $4 AND run.lease_generation = $5
  AND run.lease_until > clock_timestamp()
  AND run.status IN ('pending', 'retry');

-- name: FinishDeliveryBlocked :execrows
UPDATE business_report_deliveries AS delivery
SET status = 'blocked', last_error_code = $2, updated_at = now()
FROM business_report_runs AS run
WHERE delivery.id = $1 AND delivery.status = 'pending'
  AND run.id = delivery.run_id AND run.id = $3
  AND run.lease_owner = $4 AND run.lease_generation = $5
  AND run.lease_until > clock_timestamp()
  AND run.status IN ('pending', 'retry');

-- name: BusinessReportStats :one
SELECT
    count(*) FILTER (WHERE status = 'pending')::bigint AS pending,
    count(*) FILTER (WHERE status = 'retry')::bigint AS retry,
    count(*) FILTER (WHERE status = 'completed')::bigint AS completed,
    count(*) FILTER (WHERE status = 'blocked')::bigint AS blocked,
    min(created_at) FILTER (WHERE status IN ('pending', 'retry'))::timestamptz AS oldest_pending
FROM business_report_runs;
