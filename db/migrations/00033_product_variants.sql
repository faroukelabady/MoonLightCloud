-- +goose Up
-- Phase 17 product & physical variant architecture: SKU/inventory
-- ownership moves from Product to ProductVariant (Retail authority).
-- Retail emits catalog.product_variant.snapshot.v1 and
-- inventory.product_variant.snapshot.v1; Cloud projects them with strict
-- Store isolation (store_id UUID NULL = unscoped legacy, never collides —
-- see 00023 conventions: NULL is never equal to NULL, so legacy rows never
-- collide in the Store-scoped UNIQUE constraints).
--
-- catalog_product_variants is a rebuildable projection root and carries
-- deliberately NO foreign key to catalog_products (00012 style: never tie
-- rebuildable projections to each other destructively). Application code
-- enforces the product dependency (projection dependency wait). Child
-- rows (attribute values, variant inventory) are FK-owned by the variant
-- root like 00009/00011 children. Tombstones are stored, never deleted:
-- a `deleted` variant row is historical safety, not garbage.
--
-- Money is BIGINT minor units; no floats; no provider identity in
-- projection tables. The catalog stream (variant_revision) and the
-- inventory stream (inventory_revision) stay independent revisions,
-- exactly like catalog_products vs catalog_product_inventory.

CREATE TABLE catalog_product_variants (
    variant_id          UUID PRIMARY KEY,
    -- Logical reference to catalog_products(product_id, store_id):
    -- no FK on purpose (rebuildable projection, 00012 style).
    product_id          UUID NOT NULL,
    sku                 TEXT NOT NULL CHECK (btrim(sku) <> ''),
    is_active           BOOLEAN NOT NULL,
    -- Tombstone marker: deleted variants keep their rows forever.
    deleted             BOOLEAN NOT NULL DEFAULT FALSE,
    price_egp_cents     BIGINT CHECK (price_egp_cents IS NULL OR price_egp_cents >= 0),
    price_usd_cents     BIGINT CHECK (price_usd_cents IS NULL OR price_usd_cents >= 0),
    position            INT NOT NULL CHECK (position >= 0),
    -- Normalized stable identity of the option combination; entries are
    -- joined with U+001F (unit separator).
    combination_key     TEXT NOT NULL,
    variant_revision    BIGINT NOT NULL CHECK (variant_revision >= 1),
    catalog_revision    BIGINT NOT NULL CHECK (catalog_revision >= 1),
    source_event_id     UUID NOT NULL REFERENCES sync_events(event_id),
    source_device_id    UUID NOT NULL REFERENCES devices(id),
    source_payload_hash BYTEA NOT NULL,
    source_received_at  TIMESTAMPTZ NOT NULL,
    projected_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    store_id            UUID REFERENCES stores(id) ON DELETE RESTRICT
);
-- Store-local identities (00023): legacy NULL rows never collide.
ALTER TABLE catalog_product_variants
    ADD CONSTRAINT catalog_product_variants_store_combination_unique
    UNIQUE (store_id, product_id, combination_key);
ALTER TABLE catalog_product_variants
    ADD CONSTRAINT catalog_product_variants_store_sku_unique
    UNIQUE (store_id, sku);
CREATE INDEX idx_catalog_product_variants_product ON catalog_product_variants (product_id, position, variant_id);
CREATE INDEX idx_catalog_product_variants_sku ON catalog_product_variants (sku);
CREATE INDEX idx_catalog_product_variants_store ON catalog_product_variants (store_id) WHERE store_id IS NOT NULL;

-- Physical option identity with bilingual display labels (projection
-- only; labels never participate in business identity).
CREATE TABLE catalog_product_variant_attribute_values (
    variant_id          UUID NOT NULL REFERENCES catalog_product_variants(variant_id) ON DELETE CASCADE,
    definition_code     TEXT NOT NULL CHECK (char_length(definition_code) BETWEEN 1 AND 64),
    value_code          TEXT NOT NULL CHECK (char_length(value_code) BETWEEN 1 AND 64),
    name_ar             TEXT NOT NULL,
    name_en             TEXT,
    definition_name_ar  TEXT NOT NULL,
    definition_name_en  TEXT,
    position            INT NOT NULL CHECK (position >= 0),
    PRIMARY KEY (variant_id, definition_code)
);

-- Variant-scoped authoritative stock (000011 pattern: one row per
-- variant carrying the highest valid inventory_revision; no movements,
-- no reservations). The extra columns mirror the
-- inventory.product_variant.snapshot.v1 policy/catalog context Retail
-- sends with each snapshot: they are display/availability context, never
-- independent revision gates (policy_revision/catalog_revision never
-- overlap inventory_revision).
CREATE TABLE catalog_product_variant_inventory (
    variant_id              UUID PRIMARY KEY REFERENCES catalog_product_variants(variant_id) ON DELETE CASCADE,
    product_id              UUID NOT NULL,
    sku                     TEXT NOT NULL CHECK (btrim(sku) <> ''),
    stock_quantity          BIGINT NOT NULL CHECK (stock_quantity >= 0),
    ready                   BOOLEAN NOT NULL DEFAULT FALSE,
    sell_online             BOOLEAN NOT NULL DEFAULT FALSE,
    online_allocation_limit BIGINT CHECK (online_allocation_limit IS NULL OR online_allocation_limit >= 0),
    policy_revision         BIGINT NOT NULL CHECK (policy_revision >= 0),
    catalog_revision        BIGINT NOT NULL CHECK (catalog_revision >= 1),
    source_revision         BIGINT NOT NULL CHECK (source_revision >= 1),
    source_event_id         UUID NOT NULL REFERENCES sync_events(event_id),
    source_device_id        UUID NOT NULL REFERENCES devices(id),
    source_payload_hash     BYTEA NOT NULL,
    source_received_at      TIMESTAMPTZ NOT NULL,
    projected_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    store_id                UUID REFERENCES stores(id) ON DELETE RESTRICT
);
CREATE INDEX idx_catalog_product_variant_inventory_product ON catalog_product_variant_inventory (product_id);
CREATE INDEX idx_catalog_product_variant_inventory_store ON catalog_product_variant_inventory (store_id) WHERE store_id IS NOT NULL;

-- Durable Cloud-only provider variant mappings (00012/00024 style:
-- integration state, NOT a rebuildable projection — deliberately NO
-- foreign key to catalog_product_variants; rebuilds must never destroy
-- provider identities). One MoonLight variant ↔ one external provider
-- variant per provider instance; the parent external product identity is
-- kept denormalized for provider round-trips. No credentials ever here.
CREATE TABLE commerce_product_variant_mappings (
    provider_key        TEXT NOT NULL CHECK (provider_key ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    product_id          UUID NOT NULL,
    variant_id          UUID NOT NULL,
    external_product_id TEXT NOT NULL CHECK (char_length(external_product_id) BETWEEN 1 AND 200),
    external_variant_id TEXT NOT NULL CHECK (char_length(external_variant_id) BETWEEN 1 AND 200),
    store_id            UUID REFERENCES stores(id) ON DELETE RESTRICT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider_key, variant_id)
);
CREATE UNIQUE INDEX idx_commerce_variant_mappings_identity
    ON commerce_product_variant_mappings (provider_key, store_id, variant_id);
CREATE UNIQUE INDEX idx_commerce_variant_mappings_external
    ON commerce_product_variant_mappings (provider_key, store_id, external_product_id, external_variant_id);
CREATE INDEX idx_commerce_variant_mappings_store
    ON commerce_product_variant_mappings (store_id) WHERE store_id IS NOT NULL;
CREATE INDEX idx_commerce_variant_mappings_store_provider
    ON commerce_product_variant_mappings (store_id, provider_key) WHERE store_id IS NOT NULL;

-- Phase 17 Cloud-admin variant command vocabulary (00032 CHECK list).
-- PostgreSQL: recreate the CHECK constraint with the extended list.
ALTER TABLE catalog_admin_commands DROP CONSTRAINT catalog_admin_commands_command_type_check;
ALTER TABLE catalog_admin_commands ADD CONSTRAINT catalog_admin_commands_command_type_check CHECK (command_type IN (
    'catalog.product.details.update.v1',
    'catalog.product.online-policy.update.v1',
    'catalog.product.classification.update.v1',
    'catalog.category.details.update.v1',
    'catalog.category.parents.update.v1',
    'catalog.category.online-policy.update.v1',
    'catalog.tag.details.update.v1',
    'catalog.product.configurations.update.v1',
    'catalog.product.variants.update.v1',
    'catalog.product.variant.update.v1',
    'catalog.variant.attributes.update.v1'
));

-- +goose Down
-- Development/test repair only: variant mappings are durable integration
-- state and are NOT rebuildable from MoonLight events. Never roll back in
-- production; provider identities would be unrecoverable.
ALTER TABLE catalog_admin_commands DROP CONSTRAINT catalog_admin_commands_command_type_check;
ALTER TABLE catalog_admin_commands ADD CONSTRAINT catalog_admin_commands_command_type_check CHECK (command_type IN (
    'catalog.product.details.update.v1',
    'catalog.product.online-policy.update.v1',
    'catalog.product.classification.update.v1',
    'catalog.category.details.update.v1',
    'catalog.category.parents.update.v1',
    'catalog.category.online-policy.update.v1',
    'catalog.tag.details.update.v1',
    'catalog.product.configurations.update.v1'
));
DROP TABLE IF EXISTS commerce_product_variant_mappings;
DROP TABLE IF EXISTS catalog_product_variant_inventory;
DROP TABLE IF EXISTS catalog_product_variant_attribute_values;
DROP TABLE IF EXISTS catalog_product_variants;
