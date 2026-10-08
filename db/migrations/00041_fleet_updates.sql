-- +goose Up
-- Phase 18 secure Retail self-update control (ADR-0051, ADR-0052).
--
-- Releases are GLOBAL infrastructure (one signed artifact set serves every
-- Store); rollout targeting, device state and history are Store/device
-- scoped. Cloud stores signed release metadata and artifact LOCATIONS,
-- never artifact bytes, and never anything executable on Retail: the
-- signed envelope is relayed to Retail, which re-verifies it against its
-- own embedded keys. All scheduling timestamps are written from one
-- application clock passed by the service; nothing here compares now().

CREATE TABLE releases (
    id UUID PRIMARY KEY,
    manifest_digest TEXT NOT NULL UNIQUE CHECK (manifest_digest ~ '^[0-9a-f]{64}$'),
    release_sequence BIGINT NOT NULL UNIQUE CHECK (release_sequence > 0 AND release_sequence <= 9007199254740992),
    version TEXT NOT NULL CHECK (char_length(version) BETWEEN 1 AND 64),
    build_commit TEXT NOT NULL CHECK (build_commit ~ '^[0-9a-f]{40}$'),
    min_installed_sequence BIGINT NOT NULL CHECK (min_installed_sequence >= 0 AND min_installed_sequence < release_sequence),
    key_id TEXT NOT NULL CHECK (key_id ~ '^[0-9a-f]{16}$'),
    envelope TEXT NOT NULL CHECK (char_length(envelope) BETWEEN 1 AND 65536),
    status TEXT NOT NULL CHECK (status IN ('ACTIVE','REVOKED')),
    imported_by TEXT NOT NULL CHECK (char_length(imported_by) BETWEEN 1 AND 128),
    imported_at TIMESTAMPTZ NOT NULL,
    status_changed_by TEXT NOT NULL CHECK (char_length(status_changed_by) BETWEEN 1 AND 128),
    status_changed_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE release_artifacts (
    release_id UUID NOT NULL REFERENCES releases(id) ON DELETE RESTRICT,
    os TEXT NOT NULL CHECK (os IN ('linux','windows','darwin')),
    arch TEXT NOT NULL CHECK (arch IN ('amd64','arm64')),
    package TEXT NOT NULL CHECK (package ~ '^[a-z0-9][a-z0-9.-]{0,31}$'),
    file_name TEXT NOT NULL CHECK (file_name ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    size BIGINT NOT NULL CHECK (size > 0 AND size <= 1073741824),
    sha256 TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    url TEXT NOT NULL CHECK (char_length(url) BETWEEN 8 AND 2048),
    PRIMARY KEY (release_id, os, arch, package)
);

-- Release identity is immutable after import; only lifecycle status may
-- change. Material changes require a new signed release (prompt §14).
-- +goose StatementBegin
CREATE FUNCTION releases_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'releases are never deleted; revoke instead' USING ERRCODE = 'restrict_violation';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id OR NEW.manifest_digest IS DISTINCT FROM OLD.manifest_digest
       OR NEW.release_sequence IS DISTINCT FROM OLD.release_sequence OR NEW.version IS DISTINCT FROM OLD.version
       OR NEW.build_commit IS DISTINCT FROM OLD.build_commit OR NEW.min_installed_sequence IS DISTINCT FROM OLD.min_installed_sequence
       OR NEW.key_id IS DISTINCT FROM OLD.key_id OR NEW.envelope IS DISTINCT FROM OLD.envelope
       OR NEW.imported_by IS DISTINCT FROM OLD.imported_by OR NEW.imported_at IS DISTINCT FROM OLD.imported_at THEN
        RAISE EXCEPTION 'release identity is immutable' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER releases_immutable BEFORE UPDATE OR DELETE ON releases
    FOR EACH ROW EXECUTE FUNCTION releases_immutable();

-- +goose StatementBegin
CREATE FUNCTION release_artifacts_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'release artifacts are immutable' USING ERRCODE = 'check_violation';
END $$;
-- +goose StatementEnd
CREATE TRIGGER release_artifacts_immutable BEFORE UPDATE OR DELETE ON release_artifacts
    FOR EACH ROW EXECUTE FUNCTION release_artifacts_immutable();

CREATE TABLE update_rollouts (
    id UUID PRIMARY KEY,
    release_id UUID NOT NULL REFERENCES releases(id) ON DELETE RESTRICT,
    scope TEXT NOT NULL CHECK (scope IN ('DEVICE','STORE','ALL')),
    store_id UUID REFERENCES stores(id) ON DELETE RESTRICT,
    device_id UUID REFERENCES devices(id) ON DELETE RESTRICT,
    mode TEXT NOT NULL CHECK (mode IN ('OPTIONAL','MANDATORY')),
    percentage INT NOT NULL CHECK (percentage BETWEEN 1 AND 100),
    status TEXT NOT NULL CHECK (status IN ('DRAFT','ACTIVE','PAUSED','COMPLETED','CANCELLED')),
    not_before TIMESTAMPTZ,
    target_count INT NOT NULL CHECK (target_count >= 0),
    created_by TEXT NOT NULL CHECK (char_length(created_by) BETWEEN 1 AND 128),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CHECK ((scope = 'DEVICE' AND device_id IS NOT NULL AND store_id IS NOT NULL)
        OR (scope = 'STORE' AND store_id IS NOT NULL AND device_id IS NULL)
        OR (scope = 'ALL' AND store_id IS NULL AND device_id IS NULL))
);
CREATE INDEX idx_update_rollouts_created ON update_rollouts (created_at DESC, id DESC);

-- Immutable per-rollout target snapshot (prompt §51, §94): devices are
-- captured at creation with their Store binding; later enrollment never
-- joins an existing rollout. bucket is the deterministic percentage slot.
CREATE TABLE update_rollout_targets (
    id UUID PRIMARY KEY,
    rollout_id UUID NOT NULL REFERENCES update_rollouts(id) ON DELETE RESTRICT,
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE RESTRICT,
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    bucket INT NOT NULL CHECK (bucket BETWEEN 0 AND 99),
    state TEXT NOT NULL CHECK (state IN ('NOT_SELECTED','PENDING','DELIVERED','DOWNLOADING','VERIFIED',
        'WAITING_SAFE_BOUNDARY','INSTALLING','AWAITING_HEALTH','SUCCEEDED','ALREADY_COMPLIANT','FAILED',
        'ROLLED_BACK','MANUAL_ACTION_REQUIRED','SKIPPED_NEWER','UNSUPPORTED','CANCELLED')),
    attempt_count INT NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at TIMESTAMPTZ,
    last_error TEXT CHECK (last_error IS NULL OR last_error ~ '^[A-Z][A-Z0-9_]{0,63}$'),
    retryable BOOLEAN NOT NULL DEFAULT FALSE,
    delivered_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (rollout_id, device_id)
);
CREATE INDEX idx_update_targets_device_open ON update_rollout_targets (device_id, next_attempt_at)
    WHERE state IN ('PENDING','DELIVERED');
CREATE INDEX idx_update_targets_rollout ON update_rollout_targets (rollout_id, store_id, device_id);

-- Trusted per-device version/updater report (authenticated device only).
CREATE TABLE device_update_status (
    device_id UUID PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    version TEXT NOT NULL CHECK (char_length(version) BETWEEN 1 AND 64),
    build_commit TEXT NOT NULL CHECK (char_length(build_commit) BETWEEN 1 AND 64),
    release_sequence BIGINT NOT NULL CHECK (release_sequence >= 0),
    os TEXT NOT NULL CHECK (char_length(os) BETWEEN 1 AND 16),
    arch TEXT NOT NULL CHECK (char_length(arch) BETWEEN 1 AND 16),
    updater_protocol INT NOT NULL CHECK (updater_protocol BETWEEN 0 AND 100),
    updater_capable BOOLEAN NOT NULL,
    unsupported_reason TEXT CHECK (unsupported_reason IS NULL OR unsupported_reason ~ '^[A-Z][A-Z0-9_]{0,63}$'),
    update_state TEXT NOT NULL CHECK (update_state ~ '^[A-Z][A-Z_]{0,63}$'),
    update_error TEXT CHECK (update_error IS NULL OR update_error ~ '^[A-Z][A-Z0-9_]{0,63}$'),
    reported_at TIMESTAMPTZ NOT NULL
);

-- Append-only attempt history (prompt §105): never overwritten.
CREATE TABLE update_target_events (
    id BIGSERIAL PRIMARY KEY,
    target_id UUID NOT NULL REFERENCES update_rollout_targets(id) ON DELETE RESTRICT,
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE RESTRICT,
    reported_state TEXT NOT NULL CHECK (reported_state ~ '^[A-Z][A-Z_]{0,63}$'),
    target_state TEXT NOT NULL,
    error_code TEXT CHECK (error_code IS NULL OR error_code ~ '^[A-Z][A-Z0-9_]{0,63}$'),
    retryable BOOLEAN NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_update_target_events_target ON update_target_events (target_id, id DESC);

-- Operator/device audit (prompt §63). Identity is server-derived.
CREATE TABLE update_audit_events (
    id BIGSERIAL PRIMARY KEY,
    occurred_at TIMESTAMPTZ NOT NULL,
    actor_kind TEXT NOT NULL CHECK (actor_kind IN ('operator','device','system')),
    actor TEXT NOT NULL CHECK (char_length(actor) BETWEEN 1 AND 128),
    action TEXT NOT NULL CHECK (action ~ '^[a-z][a-z_.]{0,63}$'),
    release_id UUID REFERENCES releases(id) ON DELETE RESTRICT,
    rollout_id UUID REFERENCES update_rollouts(id) ON DELETE RESTRICT,
    target_id UUID REFERENCES update_rollout_targets(id) ON DELETE RESTRICT,
    device_id UUID REFERENCES devices(id) ON DELETE RESTRICT,
    store_id UUID REFERENCES stores(id) ON DELETE RESTRICT,
    details JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX idx_update_audit_events_time ON update_audit_events (occurred_at DESC, id DESC);

-- +goose Down
-- Development/test only. Never roll back in production.
DROP TABLE IF EXISTS update_audit_events;
DROP TABLE IF EXISTS update_target_events;
DROP TABLE IF EXISTS device_update_status;
DROP TABLE IF EXISTS update_rollout_targets;
DROP TABLE IF EXISTS update_rollouts;
DROP TRIGGER IF EXISTS release_artifacts_immutable ON release_artifacts;
DROP FUNCTION IF EXISTS release_artifacts_immutable();
DROP TABLE IF EXISTS release_artifacts;
DROP TRIGGER IF EXISTS releases_immutable ON releases;
DROP FUNCTION IF EXISTS releases_immutable();
DROP TABLE IF EXISTS releases;
