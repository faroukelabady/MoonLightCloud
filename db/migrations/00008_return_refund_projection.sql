-- +goose Up
-- Phase 4B: return/refund projection tables plus business arbitration.
-- Derived historical state only: the immutable sync_events rows stay
-- authoritative and rebuildable, and business arbitration lives in
-- return_refund_ownership (never deleted on rebuild). Money is BIGINT
-- minor units; microrate BIGINT; no floats. Return line cost is the
-- EXTENDED historical cost for the returned quantity (unit cost x qty) —
-- never re-multiplied. Child identities are deterministic (business IDs +
-- ordinals), so replaying the same event yields identical rows, never
-- duplicates. Rebuild order: clear return tables first, then sale tables
-- (CASCADE also enforces this); never touch sync_events or ownership.
CREATE TABLE return_refund_projection (
    return_refund_id   UUID PRIMARY KEY,
    source_event_id    UUID NOT NULL UNIQUE REFERENCES sync_events(event_id),
    source_device_id   UUID NOT NULL REFERENCES devices(id),
    return_number      TEXT NOT NULL,
    kind               TEXT NOT NULL CHECK (kind IN ('return', 'void')),
    reason             TEXT NOT NULL,
    note               TEXT,
    sale_id            UUID NOT NULL REFERENCES sales_projection(sale_id) ON DELETE CASCADE,
    sale_number        TEXT NOT NULL,
    sale_event_id      UUID,
    channel            TEXT NOT NULL CHECK (channel = 'STORE'),
    occurred_at        TIMESTAMPTZ NOT NULL,
    currency           TEXT NOT NULL,
    gross_refunded_minor    BIGINT NOT NULL CHECK (gross_refunded_minor >= 0),
    discount_refunded_minor BIGINT NOT NULL CHECK (discount_refunded_minor >= 0),
    tax_refunded_minor      BIGINT NOT NULL CHECK (tax_refunded_minor >= 0),
    refund_total_minor      BIGINT NOT NULL CHECK (refund_total_minor >= 0),
    fx_base            TEXT,
    fx_quote           TEXT,
    fx_rate            TEXT,
    fx_rate_microrate  BIGINT CHECK (fx_rate_microrate IS NULL OR fx_rate_microrate > 0),
    shop_name_ar       TEXT NOT NULL,
    shop_name_en       TEXT NOT NULL,
    shop_address_ar    TEXT NOT NULL,
    shop_address_en    TEXT NOT NULL,
    shop_phone         TEXT NOT NULL,
    shop_receipt_footer_ar TEXT NOT NULL,
    shop_receipt_footer_en TEXT NOT NULL,
    actor_user_id      UUID,
    actor_user_name    TEXT,
    received_at        TIMESTAMPTZ NOT NULL,
    projected_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_return_refund_occurred ON return_refund_projection (occurred_at);
CREATE INDEX idx_return_refund_sale ON return_refund_projection (sale_id);
CREATE INDEX idx_return_refund_channel_occurred ON return_refund_projection (channel, occurred_at);

CREATE TABLE return_refund_lines_projection (
    return_refund_id      UUID NOT NULL REFERENCES return_refund_projection(return_refund_id) ON DELETE CASCADE,
    sale_id               UUID NOT NULL,
    original_sale_line_id UUID NOT NULL,
    position              INT NOT NULL,
    product_id            UUID,
    quantity              INT NOT NULL CHECK (quantity >= 1),
    restocked             BOOLEAN NOT NULL,
    gross_minor           BIGINT NOT NULL CHECK (gross_minor >= 0),
    gross_currency        TEXT NOT NULL,
    discount_minor        BIGINT NOT NULL CHECK (discount_minor >= 0),
    discount_currency     TEXT NOT NULL,
    tax_minor             BIGINT NOT NULL CHECK (tax_minor >= 0),
    tax_currency          TEXT NOT NULL,
    refund_minor          BIGINT NOT NULL CHECK (refund_minor >= 0),
    refund_currency       TEXT NOT NULL,
    cost_minor            BIGINT CHECK (cost_minor IS NULL OR cost_minor >= 0),
    cost_currency         TEXT,
    PRIMARY KEY (return_refund_id, original_sale_line_id),
    FOREIGN KEY (sale_id, original_sale_line_id)
        REFERENCES sale_lines_projection (sale_id, sale_item_id) ON DELETE CASCADE
);
CREATE INDEX idx_return_refund_lines_sale_line ON return_refund_lines_projection (sale_id, original_sale_line_id);

CREATE TABLE return_refund_payments_projection (
    return_refund_id UUID NOT NULL REFERENCES return_refund_projection(return_refund_id) ON DELETE CASCADE,
    position         INT NOT NULL,
    method           TEXT NOT NULL CHECK (method IN ('cash', 'card', 'other')),
    amount_minor     BIGINT NOT NULL CHECK (amount_minor >= 0),
    amount_currency  TEXT NOT NULL,
    transaction_ref  TEXT,
    PRIMARY KEY (return_refund_id, position)
);

-- Durable business arbitration: exactly one accepted event owns each
-- return_refund_id for projection. Multiple distinct returns per sale
-- remain valid; the key is the return transaction, never the sale.
CREATE TABLE return_refund_ownership (
    return_refund_id   UUID PRIMARY KEY,
    winning_event_id   UUID NOT NULL UNIQUE REFERENCES sync_events(event_id) ON DELETE RESTRICT,
    winning_device_id  UUID NOT NULL REFERENCES devices(id) ON DELETE RESTRICT,
    decided_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
-- Downgrade policy (same as sale ownership): return_refund_ownership is
-- durable correctness history. Refuse to drop it while any arbitration
-- decision exists; an empty table may roll back. (Single-statement guard:
-- goose splits on semicolons, so no DO blocks. The inner aggregate defeats
-- constant folding so the error fires only when the guard trips.)
SELECT CASE WHEN (SELECT count(*) FROM return_refund_ownership) > 0
    THEN ((SELECT count(*)/0 FROM return_refund_ownership)) ELSE 0 END;
DROP TABLE IF EXISTS return_refund_payments_projection;
DROP TABLE IF EXISTS return_refund_lines_projection;
DROP TABLE IF EXISTS return_refund_ownership;
DROP TABLE IF EXISTS return_refund_projection;
