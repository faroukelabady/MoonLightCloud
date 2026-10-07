-- +goose Up
-- 00037_product_types: Phase 17-R2 ProductType projection (ADR-0050).
-- Retail-authoritative structural types: identity, code, translations,
-- status, position, revision + allowed dimensions + capabilities.
-- Products reference their type (logical ref, no destructive FK —
-- 00012 style: rebuilds must never destroy catalog identity).
-- Store-scoped per 00023 conventions (NULL = unscoped legacy).

CREATE TABLE catalog_product_types (
    type_id          UUID PRIMARY KEY,
    code             TEXT NOT NULL CHECK (code ~ '^[a-z0-9_]{1,32}$'),
    name_ar          TEXT NOT NULL,
    name_en          TEXT NOT NULL,
    description_ar   TEXT,
    description_en   TEXT,
    is_active        BOOLEAN NOT NULL DEFAULT TRUE,
    position         INT NOT NULL DEFAULT 0 CHECK (position >= 0),
    type_revision    BIGINT NOT NULL CHECK (type_revision >= 1),
    source_event_id  UUID NOT NULL REFERENCES sync_events(event_id),
    source_device_id UUID NOT NULL REFERENCES devices(id),
    source_payload_hash BYTEA NOT NULL,
    source_received_at TIMESTAMPTZ NOT NULL,
    projected_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    store_id         UUID REFERENCES stores(id) ON DELETE RESTRICT
);
ALTER TABLE catalog_product_types
    ADD CONSTRAINT catalog_product_types_store_code_unique
    UNIQUE (store_id, code);
CREATE INDEX idx_catalog_product_types_store ON catalog_product_types (store_id) WHERE store_id IS NOT NULL;

CREATE TABLE catalog_product_type_variant_dimensions (
    type_id         UUID NOT NULL REFERENCES catalog_product_types(type_id) ON DELETE CASCADE,
    definition_code TEXT NOT NULL CHECK (char_length(definition_code) BETWEEN 1 AND 64),
    position        INT NOT NULL DEFAULT 0 CHECK (position >= 0),
    PRIMARY KEY (type_id, definition_code)
);

CREATE TABLE catalog_product_type_capabilities (
    type_id         UUID NOT NULL REFERENCES catalog_product_types(type_id) ON DELETE CASCADE,
    capability_code TEXT NOT NULL CHECK (char_length(capability_code) BETWEEN 1 AND 64),
    PRIMARY KEY (type_id, capability_code)
);

-- Product → type relation on the product projection (logical ref only).
ALTER TABLE catalog_products ADD COLUMN product_type_id UUID;
CREATE INDEX idx_catalog_products_type ON catalog_products (product_type_id);

-- +goose Down
-- Development/test repair only. Never roll back in production.
ALTER TABLE catalog_products DROP COLUMN IF EXISTS product_type_id;
DROP TABLE IF EXISTS catalog_product_type_capabilities;
DROP TABLE IF EXISTS catalog_product_type_variant_dimensions;
DROP TABLE IF EXISTS catalog_product_types;
