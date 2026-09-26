-- +goose Up
-- Phase 6A: durable Cloud-only provider product mappings (ADR in
-- docs/decisions). One MoonLight product ↔ one external provider
-- product per provider instance. This is integration state, NOT a
-- derived projection: it must survive catalog/policy/inventory rebuilds,
-- so there is deliberately NO foreign key to the rebuildable
-- catalog_products table. Application code validates product existence
-- when creating a mapping. No credentials or secrets ever live here.

CREATE TABLE commerce_product_mappings (
    provider_key        TEXT NOT NULL CHECK (provider_key ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    product_id          UUID NOT NULL,
    external_product_id TEXT NOT NULL CHECK (char_length(external_product_id) BETWEEN 1 AND 200),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider_key, product_id)
);
CREATE UNIQUE INDEX idx_commerce_mappings_external ON commerce_product_mappings (provider_key, external_product_id);

-- +goose Down
-- Development/test repair only: mappings are durable integration state
-- and are NOT rebuildable from MoonLight events. Never roll back in
-- production; provider identities would be unrecoverable.
DROP TABLE IF EXISTS commerce_product_mappings;
