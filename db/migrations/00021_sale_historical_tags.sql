-- +goose Up
-- Phase 8C historical sale tag snapshots. Sale-time tag membership is
-- immutable history: tag_id is a historical identity with NO foreign key
-- to catalog_tags (renames, hides, and terminal deletes must never touch
-- history). sale_lines_projection rows are append-only under ownership
-- arbitration, so CASCADE there mirrors the classification precedent.
-- sales_projection.tag_capture distinguishes capture states: NULL means
-- the sale projected from v1 (unknown, never backfilled), TRUE means a
-- v2 projection captured tags (possibly zero rows for untagged lines).
CREATE TABLE sale_item_tag_snapshots (
    sale_id      UUID NOT NULL REFERENCES sales_projection(sale_id) ON DELETE CASCADE,
    sale_item_id UUID NOT NULL,
    tag_id       UUID NOT NULL,
    slug         TEXT NOT NULL CHECK (char_length(slug) BETWEEN 1 AND 64),
    name_ar      TEXT NOT NULL CHECK (char_length(name_ar) BETWEEN 1 AND 200),
    name_en      TEXT NOT NULL CHECK (char_length(name_en) BETWEEN 1 AND 200),
    PRIMARY KEY (sale_id, sale_item_id, tag_id)
);
CREATE INDEX idx_sale_item_tag_snapshots_tag ON sale_item_tag_snapshots (tag_id, sale_id, sale_item_id);
CREATE INDEX idx_sale_item_tag_snapshots_item ON sale_item_tag_snapshots (sale_id, sale_item_id);

ALTER TABLE sales_projection ADD COLUMN tag_capture BOOLEAN;

-- +goose Down
DROP TABLE sale_item_tag_snapshots;
ALTER TABLE sales_projection DROP COLUMN tag_capture;
