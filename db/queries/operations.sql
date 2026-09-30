-- Phase 7D operational incidents, alert deliveries, recovery actions.

-- name: CreateAlertRecipient :one
INSERT INTO operational_alert_recipients (id, label, provider_key, recipient, locale, enabled, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, TRUE, $6, $6)
RETURNING id, label, provider_key, recipient, locale, enabled, created_at, updated_at;

-- name: ListAlertRecipients :many
SELECT id, label, provider_key, recipient, locale, enabled, created_at, updated_at
FROM operational_alert_recipients
ORDER BY created_at, id;

-- name: ListEnabledAlertRecipients :many
SELECT id, label, provider_key, recipient, locale, enabled, created_at, updated_at
FROM operational_alert_recipients
WHERE enabled
ORDER BY created_at, id;

-- name: DisableAlertRecipient :execrows
UPDATE operational_alert_recipients SET enabled = FALSE, updated_at = $2 WHERE id = $1 AND enabled;

-- name: OpenIncident :one
INSERT INTO operational_incidents (id, rule_key, subject_type, subject_id, severity, state, episode, source_event_key, opened_at, last_observed_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, 'open', $6, $7, $8, $8, $8, $8)
ON CONFLICT (rule_key, subject_type, subject_id) WHERE state IN ('open', 'acknowledged') DO NOTHING
RETURNING id, rule_key, subject_type, subject_id, severity, state, episode, source_event_key,
    opened_at, last_observed_at, acknowledged_at, resolved_at, resolution_code, created_at, updated_at, open_intent_materialized, resolved_intent_materialized;

-- name: OpenEventIncident :one
INSERT INTO operational_incidents (id, rule_key, subject_type, subject_id, severity, state, episode, source_event_key, opened_at, last_observed_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, 'open', 1, $6, $7, $7, $7, $7)
ON CONFLICT (source_event_key) WHERE source_event_key IS NOT NULL DO NOTHING
RETURNING id, rule_key, subject_type, subject_id, severity, state, episode, source_event_key,
    opened_at, last_observed_at, acknowledged_at, resolved_at, resolution_code, created_at, updated_at, open_intent_materialized, resolved_intent_materialized;

-- name: GetActiveIncident :one
SELECT id, rule_key, subject_type, subject_id, severity, state, episode, source_event_key,
    opened_at, last_observed_at, acknowledged_at, resolved_at, resolution_code, created_at, updated_at, open_intent_materialized, resolved_intent_materialized
FROM operational_incidents
WHERE rule_key = $1 AND subject_type = $2 AND subject_id = $3
  AND state IN ('open', 'acknowledged')
ORDER BY opened_at DESC, id DESC
LIMIT 1;

-- name: GetIncidentByEventKey :one
SELECT id, rule_key, subject_type, subject_id, severity, state, episode, source_event_key,
    opened_at, last_observed_at, acknowledged_at, resolved_at, resolution_code, created_at, updated_at, open_intent_materialized, resolved_intent_materialized
FROM operational_incidents
WHERE source_event_key = $1
ORDER BY opened_at DESC, id DESC
LIMIT 1;

-- name: GetIncident :one
SELECT id, rule_key, subject_type, subject_id, severity, state, episode, source_event_key,
    opened_at, last_observed_at, acknowledged_at, resolved_at, resolution_code, created_at, updated_at, open_intent_materialized, resolved_intent_materialized
FROM operational_incidents
WHERE id = $1;

-- name: TouchIncidentObserved :execrows
UPDATE operational_incidents SET last_observed_at = $2, updated_at = $2
WHERE id = $1 AND state IN ('open', 'acknowledged');

-- name: AcknowledgeIncident :one
UPDATE operational_incidents
SET state = 'acknowledged', acknowledged_at = COALESCE(acknowledged_at, $2), updated_at = $2
WHERE id = $1 AND state = 'open'
RETURNING id, rule_key, subject_type, subject_id, severity, state, episode, source_event_key,
    opened_at, last_observed_at, acknowledged_at, resolved_at, resolution_code, created_at, updated_at, open_intent_materialized, resolved_intent_materialized;

-- name: ResolveIncident :one
UPDATE operational_incidents
SET state = 'resolved', resolved_at = $2, resolution_code = $3, updated_at = $2
WHERE id = $1 AND state IN ('open', 'acknowledged')
RETURNING id, rule_key, subject_type, subject_id, severity, state, episode, source_event_key,
    opened_at, last_observed_at, acknowledged_at, resolved_at, resolution_code, created_at, updated_at, open_intent_materialized, resolved_intent_materialized;

-- name: MaxEpisodeForSubject :one
SELECT COALESCE(MAX(episode), 0)::INTEGER AS max_episode
FROM operational_incidents
WHERE rule_key = $1 AND subject_type = $2 AND subject_id = $3;

-- name: ListIncidentsPage :many
SELECT id, rule_key, subject_type, subject_id, severity, state, episode, source_event_key,
    opened_at, last_observed_at, acknowledged_at, resolved_at, resolution_code, created_at, updated_at, open_intent_materialized, resolved_intent_materialized
FROM operational_incidents
WHERE ($1::text = '' OR state = $1::text)
  AND ($2::text = '' OR severity = $2::text)
  AND ($3::text = '' OR rule_key = $3::text)
  AND ($4::timestamptz IS NULL OR opened_at < $4::timestamptz OR (opened_at = $4::timestamptz AND id < $5::uuid))
ORDER BY opened_at DESC, id DESC
LIMIT $6;

-- name: CountActiveIncidentsByDevice :many
SELECT subject_id AS device_id, count(*)::INTEGER AS open_count,
    MAX(CASE severity WHEN 'urgent' THEN 2 ELSE 1 END)::INTEGER AS max_severity
FROM operational_incidents
WHERE subject_type = 'device' AND state IN ('open', 'acknowledged')
GROUP BY subject_id;

-- name: CreateAlertDelivery :one
INSERT INTO operational_alert_deliveries (id, incident_id, recipient_id, event_type, provider_key_snapshot, recipient_snapshot,
    locale_snapshot, template_key, body_snapshot, body_fingerprint, notification_idempotency_key, status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'pending', $12, $12)
RETURNING id, incident_id, recipient_id, event_type, provider_key_snapshot, recipient_snapshot,
    locale_snapshot, template_key, body_snapshot, body_fingerprint, notification_id, notification_idempotency_key,
    status, last_error_code, created_at, updated_at;

-- name: ClaimAlertDelivery :one
SELECT id, incident_id, recipient_id, event_type, provider_key_snapshot, recipient_snapshot,
    locale_snapshot, template_key, body_snapshot, body_fingerprint, notification_id, notification_idempotency_key,
    status, last_error_code, created_at, updated_at
FROM operational_alert_deliveries
WHERE status = 'pending'
ORDER BY created_at, id
LIMIT 1
FOR UPDATE SKIP LOCKED;

-- name: FinishAlertDeliverySent :execrows
UPDATE operational_alert_deliveries
SET status = 'sent', notification_id = $2, updated_at = $3
WHERE id = $1 AND status = 'pending';

-- name: FinishAlertDeliveryBlocked :execrows
UPDATE operational_alert_deliveries
SET status = 'blocked', last_error_code = $2, updated_at = $3
WHERE id = $1 AND status = 'pending';

-- name: ListDeliveriesForIncident :many
SELECT d.id, d.incident_id, d.recipient_id, d.event_type, d.provider_key_snapshot, d.recipient_snapshot,
    d.locale_snapshot, d.template_key, d.body_snapshot, d.body_fingerprint, d.notification_id, d.notification_idempotency_key,
    d.status, d.last_error_code, d.created_at, d.updated_at,
    n.dispatch_status AS notification_dispatch, n.delivery_status AS notification_delivery
FROM operational_alert_deliveries d
LEFT JOIN notification_messages n ON n.id = d.notification_id
WHERE d.incident_id = $1
ORDER BY d.created_at, d.id;

-- name: CreateRecoveryAction :one
INSERT INTO operational_recovery_actions (id, incident_id, action_type, state, idempotency_key, created_at, updated_at)
VALUES ($1, $2, $3, 'pending', $4, $5, $5)
ON CONFLICT (incident_id, action_type) DO NOTHING
RETURNING id, incident_id, action_type, state, idempotency_key, target_entity_id, result_code,
    attempt_count, next_attempt_at, last_error_code, created_at, updated_at;

-- name: GetRecoveryAction :one
SELECT id, incident_id, action_type, state, idempotency_key, target_entity_id, result_code,
    attempt_count, next_attempt_at, last_error_code, created_at, updated_at
FROM operational_recovery_actions
WHERE incident_id = $1 AND action_type = $2;

-- name: ClaimRecoveryAction :one
SELECT id, incident_id, action_type, state, idempotency_key, target_entity_id, result_code,
    attempt_count, next_attempt_at, last_error_code, created_at, updated_at
FROM operational_recovery_actions
WHERE state = 'pending' AND (next_attempt_at IS NULL OR next_attempt_at <= $1)
ORDER BY created_at, id
LIMIT 1
FOR UPDATE SKIP LOCKED;

-- name: FinishRecoveryCompleted :execrows
UPDATE operational_recovery_actions
SET state = 'completed', target_entity_id = $2, result_code = $3, updated_at = $4
WHERE id = $1 AND state = 'pending';

-- name: RetryRecoveryLater :execrows
UPDATE operational_recovery_actions
SET attempt_count = attempt_count + 1, next_attempt_at = $2, last_error_code = $3, updated_at = $4
WHERE id = $1 AND state = 'pending';

-- name: FinishRecoveryBlocked :execrows
UPDATE operational_recovery_actions
SET state = 'blocked', last_error_code = $2, updated_at = $3
WHERE id = $1 AND state = 'pending';

-- name: ListRecoveryForIncident :many
SELECT id, incident_id, action_type, state, idempotency_key, target_entity_id, result_code,
    attempt_count, next_attempt_at, last_error_code, created_at, updated_at
FROM operational_recovery_actions
WHERE incident_id = $1
ORDER BY created_at, id;

-- Detector scans: bounded, indexed, server-time based. ----------

-- name: ScanOfflineDevices :many
SELECT d.id, d.name, d.status, p.last_seen_at
FROM devices d
JOIN device_control_presence p ON p.device_id = d.id
WHERE d.status = 'active'
  AND p.last_seen_at IS NOT NULL
  AND p.last_seen_at < $1
  AND NOT EXISTS (
    SELECT 1 FROM operational_incidents i
    WHERE i.rule_key = 'DEVICE_OFFLINE' AND i.subject_type = 'device'
      AND i.subject_id = d.id::text AND i.state IN ('open', 'acknowledged'))
ORDER BY p.last_seen_at, d.id
LIMIT $2;

-- name: ScanFailedCommands :many
SELECT id, device_id, command_type, command_version, result_code, finished_at
FROM device_control_commands
WHERE status = 'failed'
  AND NOT EXISTS (
    SELECT 1 FROM operational_incidents i
    WHERE i.source_event_key = 'sync-command:' || device_control_commands.id::text)
ORDER BY device_control_commands.finished_at, device_control_commands.id
LIMIT $1;

-- name: ScanStaleCommands :many
SELECT id, device_id, command_type, command_version, status, requested_at
FROM device_control_commands
WHERE ((status = 'pending' AND requested_at < $1)
   OR (status IN ('leased', 'accepted', 'running') AND requested_at < $2))
  AND NOT EXISTS (
    SELECT 1 FROM operational_incidents i
    WHERE i.rule_key = 'DEVICE_SYNC_STALE' AND i.subject_type = 'sync_command'
      AND i.subject_id = device_control_commands.id::text AND i.state IN ('open', 'acknowledged'))
ORDER BY device_control_commands.requested_at, device_control_commands.id
LIMIT $3;

-- name: ScanBlockedRuns :many
SELECT id, schedule_id, run_kind, slot_local_date, created_at
FROM business_report_runs
WHERE status = 'blocked'
  AND NOT EXISTS (
    SELECT 1 FROM operational_incidents i
    WHERE i.source_event_key = 'report-blocked:' || business_report_runs.id::text)
ORDER BY business_report_runs.created_at, business_report_runs.id
LIMIT $1;

-- name: ScanStaleRuns :many
SELECT id, schedule_id, run_kind, slot_local_date, status, created_at
FROM business_report_runs
WHERE status IN ('pending', 'retry')
  AND business_report_runs.created_at < $1
  AND NOT EXISTS (
    SELECT 1 FROM operational_incidents i
    WHERE i.rule_key = 'BUSINESS_REPORT_STALE' AND i.subject_type = 'report_run'
      AND i.subject_id = business_report_runs.id::text AND i.state IN ('open', 'acknowledged'))
ORDER BY business_report_runs.created_at, business_report_runs.id
LIMIT $2;

-- name: ScanBlockedNotifications :many
SELECT id, template_key, locale, created_at
FROM notification_messages
WHERE dispatch_status = 'blocked'
  AND idempotency_key NOT LIKE 'ops-alert:%'
  AND template_key NOT LIKE 'operational\_alert\_%'
  AND NOT EXISTS (
    SELECT 1 FROM operational_incidents i
    WHERE i.source_event_key = 'notification-blocked:' || notification_messages.id::text)
ORDER BY notification_messages.created_at, notification_messages.id
LIMIT $1;

-- name: ScanAmbiguousNotifications :many
SELECT id, template_key, locale, created_at
FROM notification_messages
WHERE dispatch_status = 'ambiguous'
  AND idempotency_key NOT LIKE 'ops-alert:%'
  AND template_key NOT LIKE 'operational\_alert\_%'
  AND NOT EXISTS (
    SELECT 1 FROM operational_incidents i
    WHERE i.source_event_key = 'notification-ambiguous:' || notification_messages.id::text)
ORDER BY notification_messages.created_at, notification_messages.id
LIMIT $1;

-- name: ScanRetryStaleNotifications :many
SELECT id, template_key, locale, created_at
FROM notification_messages
WHERE dispatch_status = 'retry'
  AND notification_messages.created_at < $1
  AND idempotency_key NOT LIKE 'ops-alert:%'
  AND template_key NOT LIKE 'operational\_alert\_%'
  AND NOT EXISTS (
    SELECT 1 FROM operational_incidents i
    WHERE i.rule_key = 'NOTIFICATION_RETRY_STALE' AND i.subject_type = 'notification'
      AND i.subject_id = notification_messages.id::text AND i.state IN ('open', 'acknowledged'))
ORDER BY notification_messages.created_at, notification_messages.id
LIMIT $2;

-- name: GetCommandTerminal :one
SELECT status
FROM device_control_commands
WHERE id = $1;

-- name: GetRunTerminal :one
SELECT status
FROM business_report_runs
WHERE id = $1;

-- name: GetNotificationDispatch :one
SELECT dispatch_status
FROM notification_messages
WHERE id = $1;

-- name: CheckDeviceActive :one
SELECT status
FROM devices
WHERE id = $1;

-- name: OpsPresence :one
SELECT last_seen_at
FROM device_control_presence
WHERE device_id = $1;

-- name: ListActiveByRule :many
SELECT id, rule_key, subject_type, subject_id, severity, state, episode, source_event_key,
    opened_at, last_observed_at, acknowledged_at, resolved_at, resolution_code, created_at, updated_at, open_intent_materialized, resolved_intent_materialized
FROM operational_incidents
WHERE rule_key = $1 AND state IN ('open', 'acknowledged')
-- Least-recently-checked first: resolution loops touch every checked
-- row, so no head-of-line batch can starve later rows indefinitely.
ORDER BY last_observed_at, id
LIMIT $2;

-- name: MarkOpenIntentMaterialized :execrows
UPDATE operational_incidents SET open_intent_materialized = TRUE, updated_at = $2 WHERE id = $1;

-- name: MarkResolvedIntentMaterialized :execrows
UPDATE operational_incidents SET resolved_intent_materialized = TRUE, updated_at = $2 WHERE id = $1;

-- name: GetDeliveryByIdentity :one
SELECT id, incident_id, recipient_id, event_type, provider_key_snapshot, recipient_snapshot,
    locale_snapshot, template_key, body_snapshot, body_fingerprint, notification_id, notification_idempotency_key,
    status, last_error_code, created_at, updated_at
FROM operational_alert_deliveries
WHERE incident_id = $1 AND event_type = $2 AND recipient_id = $3;

-- name: CreateAlertDeliveryIdempotent :one
INSERT INTO operational_alert_deliveries (id, incident_id, recipient_id, event_type, provider_key_snapshot, recipient_snapshot,
    locale_snapshot, template_key, body_snapshot, body_fingerprint, notification_idempotency_key, status, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'pending', $12, $12)
ON CONFLICT (incident_id, event_type, recipient_id) DO NOTHING
RETURNING id, incident_id, recipient_id, event_type, provider_key_snapshot, recipient_snapshot,
    locale_snapshot, template_key, body_snapshot, body_fingerprint, notification_id, notification_idempotency_key,
    status, last_error_code, created_at, updated_at;

-- Manual-resolution guarded transitions (single statement each): the
-- resolve commits only when the stateful predicate currently reads
-- clear, closing the check/write race. Callers distinguish "already
-- resolved" (no-op success) from "still active" (409) by re-reading.
-- Subject IDs are UUIDs; callers pre-validate and fail closed otherwise.

-- name: ResolveIfOfflineClear :one
UPDATE operational_incidents i
SET state = 'resolved', resolved_at = $3, resolution_code = $4, updated_at = $3
WHERE i.id = $1 AND i.state IN ('open', 'acknowledged')
  AND NOT EXISTS (
    SELECT 1 FROM device_control_presence p
    WHERE p.device_id = i.subject_id::uuid AND p.last_seen_at <= $2)
RETURNING id, rule_key, subject_type, subject_id, severity, state, episode, source_event_key,
    opened_at, last_observed_at, acknowledged_at, resolved_at, resolution_code, created_at, updated_at, open_intent_materialized, resolved_intent_materialized;

-- name: ResolveIfSyncClear :one
UPDATE operational_incidents i
SET state = 'resolved', resolved_at = $2, resolution_code = $3, updated_at = $2
WHERE i.id = $1 AND i.state IN ('open', 'acknowledged')
  AND NOT EXISTS (
    SELECT 1 FROM device_control_commands c
    WHERE c.id = i.subject_id::uuid AND c.status NOT IN ('completed', 'failed'))
RETURNING id, rule_key, subject_type, subject_id, severity, state, episode, source_event_key,
    opened_at, last_observed_at, acknowledged_at, resolved_at, resolution_code, created_at, updated_at, open_intent_materialized, resolved_intent_materialized;

-- name: ResolveIfReportClear :one
UPDATE operational_incidents i
SET state = 'resolved', resolved_at = $2, resolution_code = $3, updated_at = $2
WHERE i.id = $1 AND i.state IN ('open', 'acknowledged')
  AND NOT EXISTS (
    SELECT 1 FROM business_report_runs r
    WHERE r.id = i.subject_id::uuid AND r.status IN ('pending', 'retry'))
RETURNING id, rule_key, subject_type, subject_id, severity, state, episode, source_event_key,
    opened_at, last_observed_at, acknowledged_at, resolved_at, resolution_code, created_at, updated_at, open_intent_materialized, resolved_intent_materialized;

-- name: ResolveIfNotificationClear :one
UPDATE operational_incidents i
SET state = 'resolved', resolved_at = $2, resolution_code = $3, updated_at = $2
WHERE i.id = $1 AND i.state IN ('open', 'acknowledged')
  AND NOT EXISTS (
    SELECT 1 FROM notification_messages n
    WHERE n.id = i.subject_id::uuid AND n.dispatch_status IN ('pending', 'retry'))
RETURNING id, rule_key, subject_type, subject_id, severity, state, episode, source_event_key,
    opened_at, last_observed_at, acknowledged_at, resolved_at, resolution_code, created_at, updated_at, open_intent_materialized, resolved_intent_materialized;

-- Manual-resolution predicate locks: held for the duration of the
-- guarded-resolve transaction so a concurrent predicate write cannot
-- commit between validation and resolution. EvalPlanQual only rechecks
-- when the incident row itself changes, so without these locks a
-- predicate flip landing mid-flight would be invisible. Missing subject
-- rows lock nothing (matching StillActive: absent reads as clear).
-- Writers always touch these same rows, so lock ordering is consistent
-- (predicate row, then incident row) and cannot deadlock against
-- detector paths, which never take explicit row locks.

-- name: LockPresenceRow :one
SELECT device_id FROM device_control_presence WHERE device_id = $1 FOR UPDATE;

-- name: LockCommandRow :one
SELECT id FROM device_control_commands WHERE id = $1 FOR UPDATE;

-- name: LockRunRow :one
SELECT id FROM business_report_runs WHERE id = $1 FOR UPDATE;

-- name: LockNotificationRow :one
SELECT id FROM notification_messages WHERE id = $1 FOR UPDATE;

-- Incident-row lock for guarded manual resolution. Lock ordering across
-- the codebase is incident row first, predicate row second; no other
-- path holds these locks in reverse order, so this cannot deadlock.

-- name: LockIncidentRow :one
SELECT id, rule_key, subject_type, subject_id, severity, state, episode, source_event_key,
    opened_at, last_observed_at, acknowledged_at, resolved_at, resolution_code, created_at, updated_at, open_intent_materialized, resolved_intent_materialized
FROM operational_incidents
WHERE id = $1 FOR UPDATE;
