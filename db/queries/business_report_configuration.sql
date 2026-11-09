-- name: UpdateReportRecipient :one
UPDATE notification_recipients
SET label=$2, recipient=$3, locale=$4, updated_at=now()
WHERE id=$1 AND purpose='business_report' AND deleted_at IS NULL
RETURNING id, label, provider_key, recipient, locale, enabled;

-- name: DeleteReportRecipient :execrows
UPDATE notification_recipients SET enabled=FALSE, deleted_at=now(), updated_at=now()
WHERE id=$1 AND purpose='business_report' AND deleted_at IS NULL;

-- name: UpdateReportSchedule :execrows
UPDATE business_report_schedules
SET name=$2, report_kind=$3, local_time=$4, anchor_local_date=$5,
    next_run_local_date=$6, next_run_at=$7, revision=revision+1, updated_at=now()
WHERE id=$1 AND revision=$8 AND deleted_at IS NULL;

-- name: ClearReportScheduleRecipients :exec
DELETE FROM business_report_schedule_recipients WHERE schedule_id=$1;

-- name: DeleteReportSchedule :execrows
UPDATE business_report_schedules
SET enabled=FALSE, deleted_at=now(), revision=revision+1, updated_at=now()
WHERE id=$1 AND deleted_at IS NULL;

-- name: ListReportRunPage :many
SELECT id, schedule_id, run_kind, report_kind, schedule_name, slot_local_date, manual_idempotency_key,
       scheduled_for, period_start, period_end, schedule_revision,
       status, attempt_count, lease_owner, lease_generation, last_error_code,
       count(*) OVER ()::bigint AS remaining_count
FROM business_report_runs
WHERE sqlc.narg(before_id)::uuid IS NULL
   OR (created_at,id) < (SELECT created_at,id FROM business_report_runs WHERE id=sqlc.narg(before_id)::uuid)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListReportDeliveryStates :many
-- Read-only status enrichment for one bounded history page. Bodies, full
-- addresses and provider message identities never leave this query.
SELECT d.id, d.run_id, d.provider_key,
       COALESCE(r.label, '')::text AS recipient_label,
       (CASE WHEN length(d.recipient_snapshot) <= 4 THEN '…' ELSE '…' || right(d.recipient_snapshot, 4) END)::text AS recipient_masked,
       d.status::text AS report_status,
       COALESCE(n.dispatch_status, '')::text AS dispatch_status,
       COALESCE(n.delivery_status, '')::text AS delivery_status,
       COALESCE(n.last_error_code, d.last_error_code, '')::text AS last_error_code,
       COALESCE(n.attempt_count, 0)::int AS attempt_count,
       (n.send_started_at IS NOT NULL)::boolean AS send_started,
       COALESCE(n.delivery_status_at, n.updated_at, d.updated_at)::timestamptz AS updated_at
FROM business_report_deliveries d
LEFT JOIN notification_recipients r ON r.id = d.recipient_id
LEFT JOIN notification_messages n ON n.id = d.notification_id AND n.provider_key = d.provider_key
WHERE d.run_id = ANY(sqlc.arg(run_ids)::uuid[])
ORDER BY d.run_id, d.created_at, d.id;
