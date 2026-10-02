-- +goose Up
-- Phase 9-R2: fail-safe schema safety for the Store-scoped default catalog
-- identity model. Current-state category/tag roots now carry one row per
-- (identity, Store); this index enforces that safety property so a
-- regression cannot re-collapse independent Stores onto one physical row.
-- It permits the pre-existing legacy (Store NULL) rows and every raw or
-- canonical identity. default_algorithm is a non-unique marker column (no
-- index needed) recording which identity scheme last wrote a row.
CREATE UNIQUE INDEX idx_catalog_categories_identity_store
    ON catalog_categories (category_id, store_id);
CREATE UNIQUE INDEX idx_catalog_tags_identity_store
    ON catalog_tags (tag_id, store_id);

ALTER TABLE catalog_categories ADD COLUMN default_algorithm SMALLINT NOT NULL DEFAULT 0;
ALTER TABLE catalog_tags ADD COLUMN default_algorithm SMALLINT NOT NULL DEFAULT 0;

-- +goose Down
-- Honest reversal: dropping the marker and safety indexes restores the
-- previous schema shape but does NOT undo the Store-scoped default rows
-- that may have been written. Rows written under the canonical identity
-- keep their UUIDs; a downgrade therefore loses the per-Store default
-- mapping and cannot be considered lossless.
DROP INDEX IF EXISTS idx_catalog_tags_identity_store;
DROP INDEX IF EXISTS idx_catalog_categories_identity_store;
ALTER TABLE catalog_tags DROP COLUMN IF EXISTS default_algorithm;
ALTER TABLE catalog_categories DROP COLUMN IF EXISTS default_algorithm;
