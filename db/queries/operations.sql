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
    opened_at, last_observed_at, acknowledged_at, resolved_at, resolution_code, created_at, updated_at;

-- name: OpenEventIncident :one
INSERT INTO operational_incidents (id, rule_key, subject_type, subject_id, severity, state, episode, source_event_key, opened_at, last_observed_at, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, 'open', 1, $6, $7, $7, $7, $7)
ON CONFLICT (source_event_key) WHERE source_event_key IS NOT NULL DO NOTHING
RETURNING id, rule_key, subject_type, subject_id, severity, state, episode, source_event_key,
    opened_at, last_observed_at, acknowledged_at, resolved_at, resolution_code, created_at, updated_at;

-- name: GetActiveIncident :one
SELECT id, rule_key, subject_type, subject_id, severity, state, episode, source_event_key,
    opened_at, last_observed_at, acknowledged_at, resolved_at, resolution_code, created_at, updated_at
FROM operational_incidents
WHERE rule_key = $1 AND subject_type = $2 AND subject_id = $3
  AND state IN ('open', 'acknowledged')
ORDER BY opened_at DESC, id DESC
LIMIT 1;

-- name: GetIncidentByEventKey :one
SELECT id, rule_key, subject_type, subject_id, severity, state, episode, source_event_key,
    opened_at, last_observed_at, acknowledged_at, resolved_at, resolution_code, created_at, updated_at
FROM operational_incidents
WHERE source_event_key = $1
ORDER BY opened_at DESC, id DESC
LIMIT 1;

-- name: GetIncident :one
SELECT id, rule_key, subject_type, subject_id, severity, state, episode, source_event_key,
    opened_at, last_observed_at, acknowledged_at, resolved_at, resolution_code, created_at, updated_at
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
    opened_at, last_observed_at, acknowledged_at, resolved_at, resolution_code, created_at, updated_at;

-- name: ResolveIncident :one
UPDATE operational_incidents
SET state = 'resolved', resolved_at = $2, resolution_code = $3, updated_at = $2
WHERE id = $1 AND state IN ('open', 'acknowledged')
RETURNING id, rule_key, subject_type, subject_id, severity, state, episode, source_event_key,
    opened_at, last_observed_at, acknowledged_at, resolved_at, resolution_code, created_at, updated_at;

-- name: MaxEpisodeForSubject :one
SELECT COALESCE(MAX(episode), 0)::INTEGER AS max_episode
FROM operational_incidents
WHERE rule_key = $1 AND subject_type = $2 AND subject_id = $3;

-- name: ListIncidentsPage :many
SELECT id, rule_key, subject_type, subject_id, severity, state, episode, source_event_key,
    opened_at, last_observed_at, acknowledged_at, resolved_at, resolution_code, created_at, updated_at
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
ORDER BY p.last_seen_at, d.id
LIMIT $2;

-- name: ScanReconnectedDevices :many
SELECT d.id, d.name, d.status, p.last_seen_at
FROM devices d
JOIN device_control_presence p ON p.device_id = d.id
WHERE d.status = 'active'
  AND p.last_seen_at IS NOT NULL
  AND p.last_seen_at >= $1
ORDER BY p.last_seen_at, d.id
LIMIT $2;

-- name: ScanRevokedDevices :many
SELECT d.id, d.name, d.status
FROM devices d
WHERE d.status <> 'active'
ORDER BY d.id
LIMIT $1;

-- name: ScanFailedCommands :many
SELECT id, device_id, command_type, command_version, result_code, finished_at
FROM device_control_commands
WHERE status = 'failed'
ORDER BY finished_at, id
LIMIT $1;

-- name: ScanStaleCommands :many
SELECT id, device_id, command_type, command_version, status, requested_at
FROM device_control_commands
WHERE status IN ('pending', 'accepted', 'running')
  AND requested_at < $1
ORDER BY requested_at, id
LIMIT $2;

-- name: ScanBlockedRuns :many
SELECT id, schedule_id, run_kind, slot_local_date, created_at
FROM business_report_runs
WHERE status = 'blocked'
ORDER BY created_at, id
LIMIT $1;

-- name: ScanStaleRuns :many
SELECT id, schedule_id, run_kind, slot_local_date, status, created_at
FROM business_report_runs
WHERE status IN ('pending', 'retry')
  AND created_at < $1
ORDER BY created_at, id
LIMIT $2;

-- name: ScanBlockedNotifications :many
SELECT id, template_key, locale, created_at
FROM notification_messages
WHERE dispatch_status = 'blocked'
  AND idempotency_key NOT LIKE 'ops-alert:%'
  AND template_key NOT LIKE 'operational\_alert\_%'
ORDER BY created_at, id
LIMIT $1;

-- name: ScanAmbiguousNotifications :many
SELECT id, template_key, locale, created_at
FROM notification_messages
WHERE dispatch_status = 'ambiguous'
  AND idempotency_key NOT LIKE 'ops-alert:%'
  AND template_key NOT LIKE 'operational\_alert\_%'
ORDER BY created_at, id
LIMIT $1;

-- name: ScanRetryStaleNotifications :many
SELECT id, template_key, locale, created_at
FROM notification_messages
WHERE dispatch_status = 'retry'
  AND created_at < $1
  AND idempotency_key NOT LIKE 'ops-alert:%'
  AND template_key NOT LIKE 'operational\_alert\_%'
ORDER BY created_at, id
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
    opened_at, last_observed_at, acknowledged_at, resolved_at, resolution_code, created_at, updated_at
FROM operational_incidents
WHERE rule_key = $1 AND state IN ('open', 'acknowledged')
ORDER BY opened_at, id
LIMIT $2;
