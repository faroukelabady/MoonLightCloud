-- Phase 18 release registry and fleet rollout persistence (ADR-0051/0052).
-- Every scheduling timestamp is an explicit application-clock parameter
-- (@now); no state decision compares against database now().

-- name: InsertRelease :exec
INSERT INTO releases (id, manifest_digest, release_sequence, version, build_commit, min_installed_sequence,
    key_id, envelope, status, imported_by, imported_at, status_changed_by, status_changed_at)
VALUES (@id::uuid, @manifest_digest::text, @release_sequence::bigint, @version::text, @build_commit::text,
    @min_installed_sequence::bigint, @key_id::text, @envelope::text, 'ACTIVE', @actor::text, @now::timestamptz,
    @actor::text, @now::timestamptz);

-- name: InsertReleaseArtifact :exec
INSERT INTO release_artifacts (release_id, os, arch, package, file_name, size, sha256, url)
VALUES (@release_id::uuid, @os::text, @arch::text, @package::text, @file_name::text, @size::bigint, @sha256::text, @url::text);

-- name: GetRelease :one
SELECT id, manifest_digest, release_sequence, version, build_commit, min_installed_sequence, key_id, envelope,
    status, imported_by, imported_at, status_changed_by, status_changed_at
FROM releases WHERE id = @id::uuid;

-- name: GetReleaseByDigest :one
SELECT id, manifest_digest, release_sequence, version, build_commit, min_installed_sequence, key_id, envelope,
    status, imported_by, imported_at, status_changed_by, status_changed_at
FROM releases WHERE manifest_digest = @manifest_digest::text;

-- name: GetReleaseBySequence :one
SELECT id, manifest_digest, release_sequence, version, build_commit, min_installed_sequence, key_id, envelope,
    status, imported_by, imported_at, status_changed_by, status_changed_at
FROM releases WHERE release_sequence = @release_sequence::bigint;

-- name: ListReleases :many
SELECT id, manifest_digest, release_sequence, version, build_commit, min_installed_sequence, key_id,
    status, imported_by, imported_at, status_changed_by, status_changed_at
FROM releases
WHERE (@before_sequence::bigint = 0 OR release_sequence < @before_sequence::bigint)
ORDER BY release_sequence DESC
LIMIT @row_limit::int;

-- name: ListReleaseArtifacts :many
SELECT release_id, os, arch, package, file_name, size, sha256, url
FROM release_artifacts WHERE release_id = @release_id::uuid
ORDER BY os, arch, package;

-- name: SetReleaseStatus :execrows
UPDATE releases SET status = @status::text, status_changed_by = @actor::text, status_changed_at = @now::timestamptz
WHERE id = @id::uuid AND status <> @status::text;

-- name: InsertRollout :exec
INSERT INTO update_rollouts (id, release_id, scope, store_id, device_id, mode, percentage, status, not_before,
    target_count, created_by, created_at, updated_at)
VALUES (@id::uuid, @release_id::uuid, @scope::text, sqlc.narg(store_id)::uuid, sqlc.narg(device_id)::uuid, @mode::text,
    @percentage::int, 'DRAFT', sqlc.narg(not_before)::timestamptz, @target_count::int, @actor::text, @now::timestamptz, @now::timestamptz);

-- name: GetRollout :one
SELECT id, release_id, scope, store_id, device_id, mode, percentage, status, not_before, target_count,
    created_by, created_at, updated_at
FROM update_rollouts WHERE id = @id::uuid;

-- name: LockRollout :one
SELECT id, release_id, scope, store_id, device_id, mode, percentage, status, not_before, target_count,
    created_by, created_at, updated_at
FROM update_rollouts WHERE id = @id::uuid FOR UPDATE;

-- name: ListRollouts :many
SELECT r.id, r.release_id, r.scope, r.store_id, r.device_id, r.mode, r.percentage, r.status, r.not_before,
    r.target_count, r.created_by, r.created_at, r.updated_at, rel.version, rel.release_sequence
FROM update_rollouts r JOIN releases rel ON rel.id = r.release_id
WHERE (sqlc.narg(store_id)::uuid IS NULL OR r.store_id = sqlc.narg(store_id)::uuid)
  AND (sqlc.narg(cursor_at)::timestamptz IS NULL
       OR (r.created_at, r.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY r.created_at DESC, r.id DESC
LIMIT @row_limit::int;

-- name: SetRolloutStatus :execrows
UPDATE update_rollouts SET status = @to_status::text, updated_at = @now::timestamptz
WHERE id = @id::uuid AND status = ANY(@from_statuses::text[]);

-- name: SetRolloutPercentage :execrows
UPDATE update_rollouts SET percentage = @percentage::int, updated_at = @now::timestamptz
WHERE id = @id::uuid AND percentage < @percentage::int AND status IN ('DRAFT','ACTIVE','PAUSED');

-- name: EligibleRolloutDevices :many
-- The immutable target snapshot: active, Store-bound devices in scope.
SELECT d.id AS device_id, b.store_id
FROM devices d JOIN device_store_bindings b ON b.device_id = d.id
WHERE d.status = 'active'
  AND (sqlc.narg(store_id)::uuid IS NULL OR b.store_id = sqlc.narg(store_id)::uuid)
  AND (sqlc.narg(device_id)::uuid IS NULL OR d.id = sqlc.narg(device_id)::uuid)
ORDER BY d.id
LIMIT @row_limit::int;

-- name: InsertRolloutTarget :exec
INSERT INTO update_rollout_targets (id, rollout_id, device_id, store_id, bucket, state, created_at, updated_at)
VALUES (@id::uuid, @rollout_id::uuid, @device_id::uuid, @store_id::uuid, @bucket::int, @state::text,
    @now::timestamptz, @now::timestamptz);

-- name: PromoteSelectedTargets :execrows
UPDATE update_rollout_targets SET state = 'PENDING', updated_at = @now::timestamptz
WHERE rollout_id = @rollout_id::uuid AND state = 'NOT_SELECTED' AND bucket < @percentage::int;

-- name: CancelOpenTargets :execrows
-- Cancellation cannot reach targets already in irreversible execution.
UPDATE update_rollout_targets SET state = 'CANCELLED', finished_at = @now::timestamptz, updated_at = @now::timestamptz
WHERE rollout_id = @rollout_id::uuid
  AND state IN ('NOT_SELECTED','PENDING','DELIVERED','DOWNLOADING','VERIFIED','WAITING_SAFE_BOUNDARY');

-- name: CountRolloutTargetStates :many
SELECT state, count(*)::bigint AS n FROM update_rollout_targets
WHERE rollout_id = @rollout_id::uuid GROUP BY state ORDER BY state;

-- name: ListRolloutTargets :many
SELECT t.id, t.rollout_id, t.device_id, t.store_id, t.bucket, t.state, t.attempt_count, t.next_attempt_at,
    t.last_error, t.retryable, t.delivered_at, t.finished_at, t.created_at, t.updated_at, d.name AS device_name
FROM update_rollout_targets t JOIN devices d ON d.id = t.device_id
WHERE t.rollout_id = @rollout_id::uuid
  AND (sqlc.narg(store_id)::uuid IS NULL OR t.store_id = sqlc.narg(store_id)::uuid)
  AND (sqlc.narg(after_device)::uuid IS NULL OR t.device_id > sqlc.narg(after_device)::uuid)
ORDER BY t.device_id
LIMIT @row_limit::int;

-- name: ClaimDeviceUpdateTarget :one
-- At most one deliverable target for an authenticated device: the
-- device's CURRENT binding must equal the target's snapshot Store, the
-- rollout must be ACTIVE (shared-locked so a concurrent pause serializes
-- with delivery), the release ACTIVE, an artifact must exist for the
-- device's reported platform, and the application-clock retry/not-before
-- gates must have passed.
SELECT t.id AS target_id, t.rollout_id, t.state, r.mode, rel.id AS release_id, rel.manifest_digest, rel.envelope,
    rel.status AS release_status, rel.release_sequence, a.url AS artifact_url
FROM update_rollout_targets t
JOIN update_rollouts r ON r.id = t.rollout_id
JOIN releases rel ON rel.id = r.release_id
JOIN device_store_bindings b ON b.device_id = t.device_id AND b.store_id = t.store_id
JOIN release_artifacts a ON a.release_id = rel.id AND a.os = @os::text AND a.arch = @arch::text AND a.package = @package::text
WHERE t.device_id = @device_id::uuid
  AND t.state IN ('PENDING','DELIVERED')
  AND r.status = 'ACTIVE' AND rel.status = 'ACTIVE'
  AND (t.next_attempt_at IS NULL OR t.next_attempt_at <= @now::timestamptz)
  AND (r.not_before IS NULL OR r.not_before <= @now::timestamptz)
ORDER BY rel.release_sequence DESC, t.created_at, t.id
LIMIT 1
FOR UPDATE OF t FOR SHARE OF r;

-- name: MarkTargetDelivered :exec
UPDATE update_rollout_targets SET state = 'DELIVERED', delivered_at = COALESCE(delivered_at, @now::timestamptz),
    updated_at = @now::timestamptz
WHERE id = @id::uuid AND state IN ('PENDING','DELIVERED');

-- name: MarkUnsupportedPlatformTargets :execrows
-- Targets whose release has no artifact for the device platform can never
-- be delivered: terminal UNSUPPORTED instead of permanent retry (§131).
UPDATE update_rollout_targets t SET state = 'UNSUPPORTED', last_error = 'UPDATE_PLATFORM_UNSUPPORTED',
    finished_at = @now::timestamptz, updated_at = @now::timestamptz
FROM update_rollouts r
WHERE r.id = t.rollout_id AND t.device_id = @device_id::uuid AND t.state = 'PENDING'
  AND NOT EXISTS (SELECT 1 FROM release_artifacts a WHERE a.release_id = r.release_id
                  AND a.os = @os::text AND a.arch = @arch::text AND a.package = @package::text);

-- name: MarkIncapableDeviceTargets :execrows
UPDATE update_rollout_targets SET state = 'UNSUPPORTED', last_error = @reason::text,
    finished_at = @now::timestamptz, updated_at = @now::timestamptz
WHERE device_id = @device_id::uuid AND state = 'PENDING';

-- name: MarkNewerDeviceTargets :execrows
-- A device already at or past the target never downgrades (§58-§59).
UPDATE update_rollout_targets t
SET state = CASE WHEN rel.release_sequence = @release_sequence::bigint THEN 'ALREADY_COMPLIANT' ELSE 'SKIPPED_NEWER' END,
    finished_at = @now::timestamptz, updated_at = @now::timestamptz
FROM update_rollouts r JOIN releases rel ON rel.id = r.release_id
WHERE r.id = t.rollout_id AND t.device_id = @device_id::uuid AND t.state = 'PENDING'
  AND (rel.release_sequence < @release_sequence::bigint
       OR (rel.release_sequence = @release_sequence::bigint AND rel.build_commit = @build_commit::text));

-- name: LockDeviceTarget :one
SELECT t.id, t.rollout_id, t.device_id, t.store_id, t.state, t.attempt_count, t.last_error,
    rel.id AS release_id, rel.release_sequence, rel.manifest_digest, rel.status AS release_status, r.status AS rollout_status
FROM update_rollout_targets t
JOIN update_rollouts r ON r.id = t.rollout_id
JOIN releases rel ON rel.id = r.release_id
WHERE t.id = @id::uuid AND t.device_id = @device_id::uuid
FOR UPDATE OF t;

-- name: UpdateTargetState :exec
UPDATE update_rollout_targets SET state = @state::text, last_error = sqlc.narg(last_error)::text,
    retryable = @retryable::bool, attempt_count = @attempt_count::int,
    next_attempt_at = sqlc.narg(next_attempt_at)::timestamptz, finished_at = sqlc.narg(finished_at)::timestamptz,
    updated_at = @now::timestamptz
WHERE id = @id::uuid;

-- name: InsertTargetEvent :exec
INSERT INTO update_target_events (target_id, device_id, reported_state, target_state, error_code, retryable, recorded_at)
VALUES (@target_id::uuid, @device_id::uuid, @reported_state::text, @target_state::text, sqlc.narg(error_code)::text,
    @retryable::bool, @now::timestamptz);

-- name: ListTargetEvents :many
SELECT id, target_id, device_id, reported_state, target_state, error_code, retryable, recorded_at
FROM update_target_events WHERE target_id = @target_id::uuid
ORDER BY id DESC LIMIT @row_limit::int;

-- name: OpenRolloutTargetCount :one
SELECT count(*)::bigint FROM update_rollout_targets
WHERE rollout_id = @rollout_id::uuid
  AND state IN ('NOT_SELECTED','PENDING','DELIVERED','DOWNLOADING','VERIFIED','WAITING_SAFE_BOUNDARY','INSTALLING','AWAITING_HEALTH');

-- name: UpsertDeviceUpdateStatus :exec
INSERT INTO device_update_status (device_id, version, build_commit, release_sequence, os, arch, updater_protocol,
    updater_capable, unsupported_reason, update_state, update_error, reported_at)
VALUES (@device_id::uuid, @version::text, @build_commit::text, @release_sequence::bigint, @os::text, @arch::text,
    @updater_protocol::int, @updater_capable::bool, sqlc.narg(unsupported_reason)::text, @update_state::text,
    sqlc.narg(update_error)::text, @now::timestamptz)
ON CONFLICT (device_id) DO UPDATE SET version = EXCLUDED.version, build_commit = EXCLUDED.build_commit,
    release_sequence = EXCLUDED.release_sequence, os = EXCLUDED.os, arch = EXCLUDED.arch,
    updater_protocol = EXCLUDED.updater_protocol, updater_capable = EXCLUDED.updater_capable,
    unsupported_reason = EXCLUDED.unsupported_reason, update_state = EXCLUDED.update_state,
    update_error = EXCLUDED.update_error, reported_at = EXCLUDED.reported_at;

-- name: GetDeviceUpdateStatus :one
SELECT device_id, version, build_commit, release_sequence, os, arch, updater_protocol, updater_capable,
    unsupported_reason, update_state, update_error, reported_at
FROM device_update_status WHERE device_id = @device_id::uuid;

-- name: ListFleet :many
-- Bounded keyset page of Store-bound devices with their trusted version
-- report and most recent update target (one LATERAL per row, no N+1).
SELECT d.id AS device_id, d.name, d.status AS device_status, d.last_seen_at, b.store_id,
    s.version, s.build_commit, s.release_sequence, s.os, s.arch, s.updater_protocol, s.updater_capable,
    s.unsupported_reason, s.update_state, s.update_error, s.reported_at,
    lt.target_id, COALESCE(lt.target_state, '')::text AS target_state, lt.target_error,
    COALESCE(lt.target_version, '')::text AS target_version, COALESCE(lt.target_sequence, 0)::bigint AS target_sequence, lt.rollout_id
FROM devices d
JOIN device_store_bindings b ON b.device_id = d.id
LEFT JOIN device_update_status s ON s.device_id = d.id
LEFT JOIN LATERAL (
    SELECT t.id AS target_id, t.state AS target_state, t.last_error AS target_error,
        rel.version AS target_version, rel.release_sequence AS target_sequence, t.rollout_id
    FROM update_rollout_targets t
    JOIN update_rollouts r ON r.id = t.rollout_id
    JOIN releases rel ON rel.id = r.release_id
    WHERE t.device_id = d.id AND t.store_id = b.store_id
    ORDER BY t.created_at DESC, t.id DESC LIMIT 1
) lt ON TRUE
WHERE (sqlc.narg(store_id)::uuid IS NULL OR b.store_id = sqlc.narg(store_id)::uuid)
  AND (sqlc.narg(after_device)::uuid IS NULL OR d.id > sqlc.narg(after_device)::uuid)
ORDER BY d.id
LIMIT @row_limit::int;

-- name: InsertUpdateAudit :exec
INSERT INTO update_audit_events (occurred_at, actor_kind, actor, action, release_id, rollout_id, target_id, device_id, store_id, details)
VALUES (@now::timestamptz, @actor_kind::text, @actor::text, @action::text, sqlc.narg(release_id)::uuid,
    sqlc.narg(rollout_id)::uuid, sqlc.narg(target_id)::uuid, sqlc.narg(device_id)::uuid, sqlc.narg(store_id)::uuid,
    @details::jsonb);

-- name: ListUpdateAudit :many
SELECT id, occurred_at, actor_kind, actor, action, release_id, rollout_id, target_id, device_id, store_id, details
FROM update_audit_events
WHERE (sqlc.narg(store_id)::uuid IS NULL OR store_id = sqlc.narg(store_id)::uuid)
  AND (@before_id::bigint = 0 OR id < @before_id::bigint)
ORDER BY id DESC
LIMIT @row_limit::int;
