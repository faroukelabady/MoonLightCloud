-- +goose Up
-- Phase 2B: sale.finalized.v1 projection tables. Derived historical state
-- only: the immutable sync_events row stays authoritative and rebuildable.
-- Money is BIGINT minor units; microrate BIGINT; no floats anywhere.
-- Child identities are deterministic (event stable IDs + ordinals), so
-- replaying the same event yields identical rows, never duplicates.
CREATE TABLE sales_projection (
    sale_id            UUID PRIMARY KEY,
    source_event_id    UUID NOT NULL UNIQUE REFERENCES sync_events(event_id),
    source_device_id   UUID NOT NULL REFERENCES devices(id),
    sale_number        TEXT NOT NULL,
    channel            TEXT NOT NULL CHECK (channel = 'STORE'),
    occurred_at        TIMESTAMPTZ NOT NULL,
    paid_at            TIMESTAMPTZ NOT NULL,
    shop_name_ar       TEXT NOT NULL,
    shop_name_en       TEXT NOT NULL,
    shop_address_ar    TEXT NOT NULL,
    shop_address_en    TEXT NOT NULL,
    shop_phone         TEXT NOT NULL,
    shop_receipt_footer_ar TEXT NOT NULL,
    shop_receipt_footer_en TEXT NOT NULL,
    cashier_id         TEXT,
    cashier_name       TEXT,
    currency           TEXT NOT NULL,
    subtotal_minor     BIGINT NOT NULL CHECK (subtotal_minor >= 0),
    discount_minor     BIGINT NOT NULL CHECK (discount_minor >= 0),
    tax_minor          BIGINT NOT NULL CHECK (tax_minor >= 0),
    total_minor        BIGINT NOT NULL CHECK (total_minor >= 0),
    fx_base            TEXT,
    fx_quote           TEXT,
    fx_rate            TEXT,
    fx_rate_microrate  BIGINT CHECK (fx_rate_microrate IS NULL OR fx_rate_microrate > 0),
    received_at        TIMESTAMPTZ NOT NULL,
    projected_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_sales_projection_occurred ON sales_projection (occurred_at);
CREATE INDEX idx_sales_projection_channel_occurred ON sales_projection (channel, occurred_at);
CREATE INDEX idx_sales_projection_device_occurred ON sales_projection (source_device_id, occurred_at);

CREATE TABLE sale_lines_projection (
    sale_id          UUID NOT NULL REFERENCES sales_projection(sale_id) ON DELETE CASCADE,
    sale_item_id     UUID NOT NULL,
    position         INT NOT NULL,
    product_id       UUID,
    variant_id       UUID,
    sku              TEXT NOT NULL,
    product_name     TEXT NOT NULL,
    width_cm         INT CHECK (width_cm IS NULL OR width_cm >= 1),
    height_cm        INT CHECK (height_cm IS NULL OR height_cm >= 1),
    quantity         INT NOT NULL CHECK (quantity >= 1),
    unit_price_minor BIGINT NOT NULL CHECK (unit_price_minor >= 0),
    unit_currency    TEXT NOT NULL,
    cost_minor       BIGINT CHECK (cost_minor IS NULL OR cost_minor >= 0),
    cost_currency    TEXT,
    line_total_minor BIGINT NOT NULL CHECK (line_total_minor >= 0),
    line_currency    TEXT NOT NULL,
    PRIMARY KEY (sale_id, sale_item_id)
);
CREATE INDEX idx_sale_lines_product ON sale_lines_projection (product_id);

CREATE TABLE sale_payments_projection (
    sale_id          UUID NOT NULL REFERENCES sales_projection(sale_id) ON DELETE CASCADE,
    position         INT NOT NULL,
    method           TEXT NOT NULL CHECK (method IN ('cash', 'card')),
    amount_minor     BIGINT NOT NULL CHECK (amount_minor >= 0),
    amount_currency  TEXT NOT NULL,
    change_minor     BIGINT NOT NULL CHECK (change_minor >= 0),
    change_currency  TEXT NOT NULL,
    transaction_ref  TEXT,
    PRIMARY KEY (sale_id, position)
);

-- No historical tags in v1: this table carries roots + subcategories only.
CREATE TABLE sale_line_classifications_projection (
    sale_id            UUID NOT NULL REFERENCES sales_projection(sale_id) ON DELETE CASCADE,
    sale_item_id       UUID NOT NULL,
    classification_kind TEXT NOT NULL CHECK (classification_kind IN ('root', 'subcategory')),
    classification_id  UUID NOT NULL,
    name_ar            TEXT NOT NULL,
    name_en            TEXT NOT NULL,
    position           INT NOT NULL,
    PRIMARY KEY (sale_id, sale_item_id, classification_kind, classification_id),
    FOREIGN KEY (sale_id, sale_item_id)
        REFERENCES sale_lines_projection (sale_id, sale_item_id) ON DELETE CASCADE
);
CREATE INDEX idx_sale_class_kind ON sale_line_classifications_projection (classification_kind, classification_id);

-- +goose Down
DROP TABLE IF EXISTS sale_line_classifications_projection;
DROP TABLE IF EXISTS sale_payments_projection;
DROP TABLE IF EXISTS sale_lines_projection;
DROP TABLE IF EXISTS sales_projection;
