-- +goose Up
-- 00030: Phase 15 ONLINE product options (frame configurations).
--
-- catalog_product_configurations is the projection of Retail-authored
-- configuration state (the canonical Product remains the only stock and
-- identity authority — configurations carry no SKU and no inventory).
-- commerce_product_configuration_mappings stores durable, Store-scoped,
-- ownership-keyed provider configuration identity (never label-matched).
-- commerce_online_order_lines gains immutable selection snapshots.

ALTER TABLE catalog_products ADD COLUMN configuration_revision BIGINT NOT NULL DEFAULT 0
    CHECK (configuration_revision >= 0);

CREATE TABLE catalog_product_configurations (
    configuration_id UUID PRIMARY KEY,
    product_id UUID NOT NULL REFERENCES catalog_products (product_id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind = 'frame'),
    style_code TEXT NOT NULL CHECK (length(btrim(style_code)) BETWEEN 1 AND 32),
    style_name_ar TEXT NOT NULL CHECK (length(btrim(style_name_ar)) BETWEEN 1 AND 100),
    style_name_en TEXT NULL CHECK (style_name_en IS NULL OR length(btrim(style_name_en)) BETWEEN 1 AND 100),
    color_code TEXT NOT NULL CHECK (length(btrim(color_code)) BETWEEN 1 AND 32),
    color_name_ar TEXT NOT NULL CHECK (length(btrim(color_name_ar)) BETWEEN 1 AND 100),
    color_name_en TEXT NULL CHECK (color_name_en IS NULL OR length(btrim(color_name_en)) BETWEEN 1 AND 100),
    price_delta_egp_minor BIGINT NOT NULL DEFAULT 0 CHECK (price_delta_egp_minor >= 0),
    price_delta_usd_minor BIGINT NULL CHECK (price_delta_usd_minor IS NULL OR price_delta_usd_minor >= 0),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    position INTEGER NOT NULL DEFAULT 0 CHECK (position >= 0),
    configuration_revision BIGINT NOT NULL CHECK (configuration_revision >= 1),
    source_revision BIGINT NOT NULL CHECK (source_revision >= 1),
    source_event_id UUID NOT NULL REFERENCES sync_events (event_id),
    source_device_id UUID NOT NULL REFERENCES devices (id),
    source_payload_hash BYTEA NOT NULL,
    source_received_at TIMESTAMPTZ NOT NULL,
    projected_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (product_id, style_code, color_code)
);

CREATE INDEX idx_catalog_product_configurations_product
    ON catalog_product_configurations (product_id, position, configuration_id);

-- +goose StatementBegin
-- Durable provider configuration identity (Phase 15 §61/§62). Ownership
-- is proven by (provider_key, product_id, configuration_id) — labels and
-- SKUs never establish ownership. configuration_id '00000000-0000-0000-
-- 0000-000000000000' is the documented sentinel for the NO-FRAME choice
-- (a mapping-key convention — no MoonLight configuration row exists for
-- it). Rows are retained across disable/enable for recovery (§63).
CREATE TABLE commerce_product_configuration_mappings (
    provider_key TEXT NOT NULL CHECK (provider_key ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    product_id UUID NOT NULL,
    configuration_id UUID NOT NULL,
    external_product_id TEXT NOT NULL CHECK (length(external_product_id) BETWEEN 1 AND 200),
    external_configuration_id TEXT NOT NULL CHECK (length(external_configuration_id) BETWEEN 1 AND 200),
    store_id UUID NULL REFERENCES stores (id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider_key, product_id, configuration_id),
    UNIQUE (provider_key, external_product_id, external_configuration_id)
);

CREATE INDEX idx_commerce_product_configuration_mappings_external
    ON commerce_product_configuration_mappings (provider_key, external_product_id, external_configuration_id);
-- +goose StatementEnd

-- Immutable online-order selection snapshots (§55/§56). Current
-- configuration rows are never authority for past purchases; unknown
-- provider identities are preserved raw and marked unresolved (§60).
ALTER TABLE commerce_online_order_lines ADD COLUMN configuration_id UUID NULL;
ALTER TABLE commerce_online_order_lines ADD COLUMN frame_style_code TEXT NULL;
ALTER TABLE commerce_online_order_lines ADD COLUMN frame_style_name_ar TEXT NULL;
ALTER TABLE commerce_online_order_lines ADD COLUMN frame_style_name_en TEXT NULL;
ALTER TABLE commerce_online_order_lines ADD COLUMN frame_color_code TEXT NULL;
ALTER TABLE commerce_online_order_lines ADD COLUMN frame_color_name_ar TEXT NULL;
ALTER TABLE commerce_online_order_lines ADD COLUMN frame_color_name_en TEXT NULL;
ALTER TABLE commerce_online_order_lines ADD COLUMN configuration_price_delta_minor BIGINT NULL
    CHECK (configuration_price_delta_minor IS NULL OR configuration_price_delta_minor >= 0);
ALTER TABLE commerce_online_order_lines ADD COLUMN provider_configuration_id TEXT NULL
    CHECK (provider_configuration_id IS NULL OR length(provider_configuration_id) <= 200);
ALTER TABLE commerce_online_order_lines ADD COLUMN configuration_unresolved BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX idx_commerce_online_order_lines_configuration
    ON commerce_online_order_lines (provider_key, external_order_id, configuration_id);

-- +goose Down
DROP INDEX IF EXISTS idx_commerce_online_order_lines_configuration;
ALTER TABLE commerce_online_order_lines DROP COLUMN configuration_unresolved;
ALTER TABLE catalog_products DROP COLUMN configuration_revision;
ALTER TABLE commerce_online_order_lines DROP COLUMN provider_configuration_id;
ALTER TABLE commerce_online_order_lines DROP COLUMN configuration_price_delta_minor;
ALTER TABLE commerce_online_order_lines DROP COLUMN frame_color_name_en;
ALTER TABLE commerce_online_order_lines DROP COLUMN frame_color_name_ar;
ALTER TABLE commerce_online_order_lines DROP COLUMN frame_color_code;
ALTER TABLE commerce_online_order_lines DROP COLUMN frame_style_name_en;
ALTER TABLE commerce_online_order_lines DROP COLUMN frame_style_name_ar;
ALTER TABLE commerce_online_order_lines DROP COLUMN frame_style_code;
ALTER TABLE commerce_online_order_lines DROP COLUMN configuration_id;
DROP TABLE IF EXISTS commerce_product_configuration_mappings;
DROP TABLE IF EXISTS catalog_product_configurations;
