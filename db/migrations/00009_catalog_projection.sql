-- +goose Up
-- Phase 5A: current-state catalog projection (ADR-0028). Retail is the
-- authority; these tables converge to the highest valid per-entity
-- revision. Historical Sale/Return projections never join these tables.
-- Money is BIGINT minor units; no floats; no provider columns; no stock
-- columns. Rebuild order: clear product relations, products, edges,
-- categories, tags (CASCADE also enforces this); never touch sync_events.

CREATE TABLE catalog_categories (
    category_id         UUID PRIMARY KEY,
    status              TEXT NOT NULL CHECK (status IN ('active', 'hidden', 'archived')),
    name_ar             TEXT NOT NULL,
    name_en             TEXT,
    source_revision     BIGINT NOT NULL CHECK (source_revision >= 1),
    source_event_id     UUID NOT NULL REFERENCES sync_events(event_id),
    source_device_id    UUID NOT NULL REFERENCES devices(id),
    source_payload_hash BYTEA NOT NULL,
    source_received_at  TIMESTAMPTZ NOT NULL,
    projected_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE catalog_category_edges (
    parent_id UUID NOT NULL REFERENCES catalog_categories(category_id) ON DELETE CASCADE,
    child_id  UUID NOT NULL REFERENCES catalog_categories(category_id) ON DELETE CASCADE,
    position  INT NOT NULL CHECK (position >= 0),
    PRIMARY KEY (parent_id, child_id),
    CHECK (parent_id <> child_id)
);
CREATE INDEX idx_catalog_category_edges_child ON catalog_category_edges (child_id);

CREATE TABLE catalog_tags (
    tag_id              UUID PRIMARY KEY,
    slug                TEXT NOT NULL UNIQUE,
    is_active           BOOLEAN NOT NULL,
    name_ar             TEXT,
    name_en             TEXT,
    source_revision     BIGINT NOT NULL CHECK (source_revision >= 1),
    source_event_id     UUID NOT NULL REFERENCES sync_events(event_id),
    source_device_id    UUID NOT NULL REFERENCES devices(id),
    source_payload_hash BYTEA NOT NULL,
    source_received_at  TIMESTAMPTZ NOT NULL,
    projected_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE catalog_products (
    product_id          UUID PRIMARY KEY,
    sku                 TEXT NOT NULL UNIQUE,
    name                TEXT NOT NULL,
    description         TEXT,
    top_category_id     UUID NOT NULL REFERENCES catalog_categories(category_id) ON DELETE RESTRICT,
    width_cm            INT CHECK (width_cm IS NULL OR width_cm > 0),
    height_cm           INT CHECK (height_cm IS NULL OR height_cm > 0),
    is_active           BOOLEAN NOT NULL,
    source_revision     BIGINT NOT NULL CHECK (source_revision >= 1),
    source_event_id     UUID NOT NULL REFERENCES sync_events(event_id),
    source_device_id    UUID NOT NULL REFERENCES devices(id),
    source_payload_hash BYTEA NOT NULL,
    source_received_at  TIMESTAMPTZ NOT NULL,
    projected_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_catalog_products_sku ON catalog_products (sku);
CREATE INDEX idx_catalog_products_active ON catalog_products (is_active) WHERE is_active;

CREATE TABLE catalog_product_prices (
    product_id  UUID NOT NULL REFERENCES catalog_products(product_id) ON DELETE CASCADE,
    currency    TEXT NOT NULL CHECK (currency IN ('EGP', 'USD')),
    price_minor BIGINT NOT NULL CHECK (price_minor >= 0),
    cost_minor  BIGINT CHECK (cost_minor IS NULL OR cost_minor >= 0),
    PRIMARY KEY (product_id, currency)
);

CREATE TABLE catalog_product_translations (
    product_id  UUID NOT NULL REFERENCES catalog_products(product_id) ON DELETE CASCADE,
    locale      TEXT NOT NULL CHECK (locale IN ('ar', 'en')),
    name        TEXT NOT NULL,
    description TEXT,
    PRIMARY KEY (product_id, locale)
);

CREATE TABLE catalog_product_subcategories (
    product_id  UUID NOT NULL REFERENCES catalog_products(product_id) ON DELETE CASCADE,
    category_id UUID NOT NULL REFERENCES catalog_categories(category_id) ON DELETE CASCADE,
    position    INT NOT NULL CHECK (position >= 0),
    PRIMARY KEY (product_id, category_id)
);
CREATE INDEX idx_catalog_product_subcategories_category ON catalog_product_subcategories (category_id);

CREATE TABLE catalog_product_tags (
    product_id UUID NOT NULL REFERENCES catalog_products(product_id) ON DELETE CASCADE,
    tag_id     UUID NOT NULL REFERENCES catalog_tags(tag_id) ON DELETE CASCADE,
    PRIMARY KEY (product_id, tag_id)
);
CREATE INDEX idx_catalog_product_tags_tag ON catalog_product_tags (tag_id);

-- +goose Down
DROP TABLE IF EXISTS catalog_product_tags;
DROP TABLE IF EXISTS catalog_product_subcategories;
DROP TABLE IF EXISTS catalog_product_translations;
DROP TABLE IF EXISTS catalog_product_prices;
DROP TABLE IF EXISTS catalog_products;
DROP TABLE IF EXISTS catalog_category_edges;
DROP TABLE IF EXISTS catalog_tags;
DROP TABLE IF EXISTS catalog_categories;
