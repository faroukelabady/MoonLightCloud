-- +goose Up
-- Phase 18 R1 Cloud human authentication & authorization (ADR-0053).
--
-- Human identity is distinct from Store and from Retail device identity:
-- admin_users never share rows, credentials or sessions with devices.
-- Only explicitly provisioned humans exist (first OWNER via the server-side
-- bootstrap CLI; ADMINs created by an OWNER). There is no self-registration
-- and no default account.
--
-- Secrets are never stored in usable form: passwords are Argon2id PHC
-- strings, session/activation tokens and recovery codes are SHA-256
-- digests of high-entropy random values, and TOTP secrets are AES-256-GCM
-- ciphertext under a key that never enters the database
-- (AUTH_MFA_ENCRYPTION_KEY). Every timestamp is written from the single
-- application clock passed by the service; nothing here compares now().

CREATE TABLE admin_users (
    id UUID PRIMARY KEY,
    -- Normalized (trimmed, lower-case) login identifier.
    login TEXT NOT NULL UNIQUE
        CHECK (login = lower(login) AND char_length(login) BETWEEN 3 AND 254 AND login ~ '^[a-z0-9][a-z0-9._@+-]*$'),
    display_name TEXT NOT NULL CHECK (char_length(btrim(display_name)) BETWEEN 1 AND 100),
    role TEXT NOT NULL CHECK (role IN ('OWNER', 'ADMIN')),
    status TEXT NOT NULL CHECK (status IN ('PENDING_SETUP', 'ACTIVE', 'DISABLED')),
    -- Access to every Store (present and future) instead of explicit
    -- memberships. Restricted users list their Stores below.
    all_stores BOOLEAN NOT NULL DEFAULT false,
    password_hash TEXT CHECK (password_hash IS NULL OR password_hash LIKE '$argon2id$%'),
    password_changed_at TIMESTAMPTZ,
    -- Bumped on every password/MFA/role/status/membership change; a
    -- session minted under an older version is no longer accepted.
    security_version BIGINT NOT NULL DEFAULT 1 CHECK (security_version >= 1),
    created_by UUID REFERENCES admin_users(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CHECK (status = 'PENDING_SETUP' OR password_hash IS NOT NULL)
);

-- First-owner bootstrap happens exactly once in the life of a database:
-- the singleton primary key makes a concurrent second bootstrap fail.
CREATE TABLE admin_bootstrap_state (
    singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
    owner_id UUID NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
    bootstrapped_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE admin_user_store_memberships (
    user_id UUID NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, store_id)
);
CREATE INDEX idx_admin_memberships_store ON admin_user_store_memberships (store_id);

-- One TOTP credential per user. enabled_at NULL means enrollment started
-- but not yet verified. last_used_step rejects TOTP replay.
CREATE TABLE admin_mfa_credentials (
    user_id UUID PRIMARY KEY REFERENCES admin_users(id) ON DELETE CASCADE,
    secret_ciphertext BYTEA NOT NULL CHECK (octet_length(secret_ciphertext) BETWEEN 28 AND 256),
    key_version INTEGER NOT NULL CHECK (key_version >= 1),
    enabled_at TIMESTAMPTZ,
    last_used_step BIGINT NOT NULL DEFAULT 0 CHECK (last_used_step >= 0),
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE admin_recovery_codes (
    id BIGSERIAL PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    code_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(code_hash) = 32),
    created_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ
);
CREATE INDEX idx_admin_recovery_codes_user ON admin_recovery_codes (user_id) WHERE used_at IS NULL;

CREATE TABLE admin_activation_tokens (
    token_hash BYTEA PRIMARY KEY CHECK (octet_length(token_hash) = 32),
    user_id UUID NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    created_by UUID REFERENCES admin_users(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    CHECK (expires_at > created_at)
);
CREATE INDEX idx_admin_activation_user ON admin_activation_tokens (user_id);

-- Opaque server-side sessions: the browser holds a 256-bit random token;
-- only its digest is stored. stage gates what the session may do.
CREATE TABLE admin_sessions (
    id UUID PRIMARY KEY,
    token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    csrf_hash BYTEA NOT NULL CHECK (octet_length(csrf_hash) = 32),
    user_id UUID NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    stage TEXT NOT NULL CHECK (stage IN ('MFA_PENDING', 'MFA_SETUP', 'FULL')),
    security_version BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    absolute_expires_at TIMESTAMPTZ NOT NULL,
    auth_time TIMESTAMPTZ NOT NULL,
    mfa_verified_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    revoke_reason TEXT CHECK (revoke_reason IS NULL OR revoke_reason ~ '^[a-z_]{1,32}$'),
    CHECK (absolute_expires_at > created_at),
    CHECK (stage <> 'FULL' OR mfa_verified_at IS NOT NULL)
);
CREATE INDEX idx_admin_sessions_user ON admin_sessions (user_id) WHERE revoked_at IS NULL;

-- Bounded brute-force throttling for both the account and the network
-- identity. Locks are temporary (locked_until); nothing is permanent.
CREATE TABLE admin_login_throttle (
    key_kind TEXT NOT NULL CHECK (key_kind IN ('account', 'ip')),
    key TEXT NOT NULL CHECK (char_length(key) BETWEEN 1 AND 254),
    failures INTEGER NOT NULL CHECK (failures >= 0),
    window_started_at TIMESTAMPTZ NOT NULL,
    locked_until TIMESTAMPTZ,
    lock_count INTEGER NOT NULL DEFAULT 0 CHECK (lock_count >= 0),
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (key_kind, key)
);
CREATE INDEX idx_admin_login_throttle_updated ON admin_login_throttle (updated_at);

-- Append-only human-auth/security audit, separate from update audit.
-- Never stores passwords, TOTP secrets, recovery codes or tokens.
CREATE TABLE auth_audit_events (
    id BIGSERIAL PRIMARY KEY,
    occurred_at TIMESTAMPTZ NOT NULL,
    actor_user_id UUID REFERENCES admin_users(id) ON DELETE RESTRICT,
    target_user_id UUID REFERENCES admin_users(id) ON DELETE RESTRICT,
    action TEXT NOT NULL CHECK (action ~ '^[a-z][a-z_.]{0,63}$'),
    outcome TEXT NOT NULL CHECK (outcome IN ('success', 'failure', 'denied')),
    reason TEXT CHECK (reason IS NULL OR reason ~ '^[A-Z][A-Z0-9_]{0,63}$'),
    client_ip TEXT CHECK (client_ip IS NULL OR char_length(client_ip) <= 64),
    details JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX idx_auth_audit_time ON auth_audit_events (occurred_at DESC, id DESC);

-- +goose StatementBegin
CREATE FUNCTION auth_audit_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'auth audit events are append-only' USING ERRCODE = 'restrict_violation';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER auth_audit_append_only BEFORE UPDATE OR DELETE ON auth_audit_events
    FOR EACH ROW EXECUTE FUNCTION auth_audit_append_only();

-- Backstop for the service's transactional OWNER protection: once any
-- human exists, at least one ACTIVE OWNER must remain.
-- +goose StatementBegin
CREATE FUNCTION admin_users_keep_owner() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM admin_users)
       AND NOT EXISTS (SELECT 1 FROM admin_users WHERE role = 'OWNER' AND status = 'ACTIVE') THEN
        RAISE EXCEPTION 'at least one active OWNER must remain' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER admin_users_keep_owner AFTER UPDATE OR DELETE ON admin_users
    FOR EACH STATEMENT EXECUTE FUNCTION admin_users_keep_owner();

-- +goose Down
-- Development/test only. Never roll back in production.
DROP TRIGGER IF EXISTS admin_users_keep_owner ON admin_users;
DROP FUNCTION IF EXISTS admin_users_keep_owner();
DROP TRIGGER IF EXISTS auth_audit_append_only ON auth_audit_events;
DROP FUNCTION IF EXISTS auth_audit_append_only();
DROP TABLE IF EXISTS auth_audit_events;
DROP TABLE IF EXISTS admin_login_throttle;
DROP TABLE IF EXISTS admin_sessions;
DROP TABLE IF EXISTS admin_activation_tokens;
DROP TABLE IF EXISTS admin_recovery_codes;
DROP TABLE IF EXISTS admin_mfa_credentials;
DROP TABLE IF EXISTS admin_user_store_memberships;
DROP TABLE IF EXISTS admin_bootstrap_state;
DROP TABLE IF EXISTS admin_users;
