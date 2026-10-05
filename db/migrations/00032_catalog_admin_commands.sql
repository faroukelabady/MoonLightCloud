-- +goose Up
-- Phase 16 Cloud-admin catalog control: durable operator intent.
-- Cloud stores typed immutable commands plus immutable per-device
-- targets. Cloud NEVER writes catalog projections from these tables:
-- Retail pulls targets over authenticated outbound device control,
-- applies them through canonical Retail services, and resulting state
-- returns via normal Retail catalog sync events only.
CREATE TABLE catalog_admin_commands (
    id UUID PRIMARY KEY,
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    command_type TEXT NOT NULL CHECK (command_type IN (
        'catalog.product.details.update.v1',
        'catalog.product.online-policy.update.v1',
        'catalog.product.classification.update.v1',
        'catalog.category.details.update.v1',
        'catalog.category.parents.update.v1',
        'catalog.category.online-policy.update.v1',
        'catalog.tag.details.update.v1',
        'catalog.product.configurations.update.v1'
    )),
    command_version INT NOT NULL CHECK (command_version = 1),
    entity_id TEXT NOT NULL CHECK (char_length(entity_id) BETWEEN 1 AND 64),
    payload JSONB NOT NULL,
    payload_hash TEXT NOT NULL CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
    expected_revision BIGINT NOT NULL CHECK (expected_revision >= 0),
    actor TEXT NOT NULL CHECK (char_length(actor) BETWEEN 1 AND 128),
    status TEXT NOT NULL CHECK (status IN ('PENDING','CANCELLED')) DEFAULT 'PENDING',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE catalog_admin_command_targets (
    id UUID PRIMARY KEY,
    command_id UUID NOT NULL REFERENCES catalog_admin_commands(id) ON DELETE RESTRICT,
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE RESTRICT,
    status TEXT NOT NULL CHECK (status IN ('PENDING','DELIVERED','APPLIED','CONFLICT','REJECTED','BLOCKED_CAPABILITY','SKIPPED_REVOKED','CANCELLED')),
    result_code TEXT CHECK (result_code IS NULL OR result_code ~ '^[A-Z][A-Z0-9_]{0,63}$'),
    entity_id TEXT NOT NULL DEFAULT '',
    pre_revision BIGINT NOT NULL DEFAULT 0,
    post_revision BIGINT NOT NULL DEFAULT 0,
    delivered_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (command_id, device_id)
);
-- Capability advertisement: Retail announces catalog_admin_commands_v1.
-- A missing row means incapable (old Retail): Cloud never delivers
-- unknown commands there and the dashboard shows update-required.
CREATE TABLE catalog_admin_device_capabilities (
    device_id UUID PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    catalog_admin_commands_v1 BOOLEAN NOT NULL DEFAULT FALSE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_catalog_admin_commands_store ON catalog_admin_commands(store_id, created_at DESC, id);
CREATE INDEX idx_catalog_admin_commands_entity ON catalog_admin_commands(store_id, entity_id, created_at DESC);
CREATE INDEX idx_catalog_admin_targets_device ON catalog_admin_command_targets(device_id, status, created_at, id) WHERE status IN ('PENDING','DELIVERED');
CREATE INDEX idx_catalog_admin_targets_command ON catalog_admin_command_targets(command_id);

-- +goose Down
DROP TABLE IF EXISTS catalog_admin_device_capabilities;
DROP TABLE IF EXISTS catalog_admin_command_targets;
DROP TABLE IF EXISTS catalog_admin_commands;
