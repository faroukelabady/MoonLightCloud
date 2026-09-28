-- Notification template mappings, idempotent outbox, and delivery
-- history (Phase 7A). One statement per query: sqlc :exec drops
-- trailing statements silently.

-- name: UpsertTemplateMapping :exec
INSERT INTO notification_template_mappings AS mapping (
    provider_key, template_key, locale,
    external_template_name, external_language_code, parameter_names, enabled, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, now())
ON CONFLICT (provider_key, template_key, locale) DO UPDATE SET
    external_template_name = EXCLUDED.external_template_name,
    external_language_code = EXCLUDED.external_language_code,
    parameter_names = EXCLUDED.parameter_names,
    enabled = EXCLUDED.enabled,
    updated_at = now();

-- name: GetTemplateMapping :one
SELECT provider_key, template_key, locale,
    external_template_name, external_language_code, parameter_names, enabled
FROM notification_template_mappings
WHERE provider_key = $1 AND template_key = $2 AND locale = $3;

-- name: ListTemplateMappings :many
SELECT provider_key, template_key, locale,
    external_template_name, external_language_code, parameter_names, enabled
FROM notification_template_mappings
WHERE (NULLIF($1::text, '') IS NULL OR provider_key = $1)
ORDER BY provider_key, template_key, locale;

-- name: InsertNotification :one
INSERT INTO notification_messages (
    id, provider_key, idempotency_key, semantic_fingerprint,
    recipient, template_key, locale, parameters,
    ext_template_name, ext_language_code, ext_parameter_order
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (provider_key, idempotency_key) DO NOTHING
RETURNING id;

-- name: GetNotificationByIdempotency :one
SELECT id, provider_key, idempotency_key, semantic_fingerprint,
    recipient, template_key, locale, parameters,
    ext_template_name, ext_language_code, ext_parameter_order,
    dispatch_status, attempt_count, provider_message_id, delivery_status
FROM notification_messages
WHERE provider_key = $1 AND idempotency_key = $2;

-- name: GetNotificationByID :one
SELECT id, provider_key, idempotency_key, semantic_fingerprint,
    recipient, template_key, locale, parameters,
    ext_template_name, ext_language_code, ext_parameter_order,
    dispatch_status, attempt_count, next_attempt_at,
    lease_owner, lease_until, lease_generation, send_started_at,
    provider_message_id, delivery_status, delivery_status_at,
    last_error_code, created_at, updated_at
FROM notification_messages
WHERE id = $1;

-- name: GetNotificationByIDForUpdate :one
SELECT id, provider_key, idempotency_key, semantic_fingerprint,
    recipient, template_key, locale, parameters,
    ext_template_name, ext_language_code, ext_parameter_order,
    dispatch_status, attempt_count, next_attempt_at,
    lease_owner, lease_until, lease_generation, send_started_at,
    provider_message_id, delivery_status, delivery_status_at,
    last_error_code, created_at, updated_at
FROM notification_messages
WHERE id = $1
FOR UPDATE;

-- name: GetNotificationByProviderMessage :one
SELECT id, provider_key, idempotency_key,
    dispatch_status, delivery_status, delivery_status_at
FROM notification_messages
WHERE provider_key = $1 AND provider_message_id = $2;

-- name: ClaimNotification :one
WITH candidate AS (
    SELECT id
    FROM notification_messages
    WHERE dispatch_status IN ('pending', 'retry')
      AND (next_attempt_at IS NULL OR next_attempt_at <= now())
      AND (lease_until IS NULL OR lease_until <= now())
    ORDER BY next_attempt_at NULLS FIRST, created_at, id
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
UPDATE notification_messages AS message
SET lease_owner = $1, lease_until = $2,
    lease_generation = message.lease_generation + 1,
    attempt_count = message.attempt_count + 1, updated_at = now()
FROM candidate
WHERE message.id = candidate.id
RETURNING message.id, message.provider_key, message.idempotency_key,
    message.recipient, message.template_key, message.locale, message.parameters,
    message.ext_template_name, message.ext_language_code, message.ext_parameter_order,
    message.dispatch_status, message.attempt_count, message.send_started_at,
    message.lease_owner, message.lease_generation;

-- name: MarkNotificationSendStarted :execrows
UPDATE notification_messages
SET send_started_at = now(), updated_at = now()
WHERE id = $1 AND lease_owner = $2 AND lease_generation = $3
  AND dispatch_status IN ('pending', 'retry');

-- name: FinishNotificationAccepted :execrows
UPDATE notification_messages
SET dispatch_status = 'accepted', provider_message_id = $4,
    delivery_status = 'ACCEPTED', delivery_status_at = now(),
    last_error_code = NULL, lease_owner = NULL, lease_until = NULL,
    updated_at = now()
WHERE id = $1 AND lease_owner = $2 AND lease_generation = $3
  AND dispatch_status IN ('pending', 'retry');

-- name: FinishNotificationRetry :execrows
UPDATE notification_messages
SET dispatch_status = 'retry', next_attempt_at = $4,
    last_error_code = $5, send_started_at = NULL,
    lease_owner = NULL, lease_until = NULL, updated_at = now()
WHERE id = $1 AND lease_owner = $2 AND lease_generation = $3
  AND dispatch_status IN ('pending', 'retry');

-- name: FinishNotificationBlocked :execrows
UPDATE notification_messages
SET dispatch_status = 'blocked',
    last_error_code = $4, lease_owner = NULL, lease_until = NULL,
    updated_at = now()
WHERE id = $1 AND lease_owner = $2 AND lease_generation = $3
  AND dispatch_status IN ('pending', 'retry');

-- name: FinishNotificationAmbiguous :execrows
UPDATE notification_messages
SET dispatch_status = 'ambiguous',
    last_error_code = $4, lease_owner = NULL, lease_until = NULL,
    updated_at = now()
WHERE id = $1 AND lease_owner = $2 AND lease_generation = $3
  AND dispatch_status IN ('pending', 'retry');

-- name: InsertDeliveryStatusHistory :one
INSERT INTO notification_delivery_status_history (
    notification_id, provider_key, provider_message_id,
    provider_status_raw, canonical_status, provider_timestamp,
    event_fingerprint, provider_error_code
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (notification_id, event_fingerprint) DO NOTHING
RETURNING notification_id;

-- name: UpdateNotificationDelivery :execrows
UPDATE notification_messages
SET delivery_status = $2, delivery_status_at = $3, updated_at = now()
WHERE id = $1;

-- name: NotificationQueueStats :one
SELECT
    count(*) FILTER (WHERE dispatch_status = 'pending')::bigint AS pending,
    count(*) FILTER (WHERE dispatch_status = 'retry')::bigint AS retry,
    count(*) FILTER (WHERE dispatch_status = 'accepted')::bigint AS accepted,
    count(*) FILTER (WHERE dispatch_status = 'blocked')::bigint AS blocked,
    count(*) FILTER (WHERE dispatch_status = 'ambiguous')::bigint AS ambiguous,
    min(created_at) FILTER (WHERE dispatch_status IN ('pending', 'retry'))::timestamptz AS oldest_pending
FROM notification_messages;
