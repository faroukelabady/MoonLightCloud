-- +goose Up
-- MoonLightCloud pre-production baseline (ADR-0055).
--
-- One migration replaces the historical goose chain 00001-00045; each section
-- below is that migration's Up body, in order. Before the first production
-- install this file changes in place and its version number increases with
-- each change, so a database built from an older revision fails closed: the
-- guard below refuses to run on any existing MoonLight schema. After the first
-- production install the file is frozen and append-only migrations resume.

-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('public.sync_events') IS NOT NULL THEN
        RAISE EXCEPTION 'MoonLightCloud baseline refuses an existing schema: recreate this pre-production database (ADR-0055)';
    END IF;
END
$$;
-- +goose StatementEnd

-- ==== 00001_devices ====
-- Phase 1A foundation: device identity only. Fresh MoonLightCloud history;
-- unrelated to any MoonLightRetail (desktop/SQLite) schema.
CREATE TABLE devices (
    id          UUID PRIMARY KEY,
    name        TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
    status      TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
    secret_hash BYTEA NOT NULL,
    secret_salt BYTEA NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ,
    revoked_at  TIMESTAMPTZ
);
CREATE INDEX idx_devices_status ON devices (status);

-- ==== 00002_device_credentials ====
-- Phase 1B: separate device credentials from devices for lifecycle
-- (provisioning, rotation, per-credential revocation). Existing Phase-1A
-- device verifiers migrate losslessly as verifier_version=0 rows verified
-- with the exact legacy construction; no new v0 row is ever created.
CREATE TABLE device_credentials (
    id                UUID PRIMARY KEY,
    device_id         UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    salt              BYTEA NOT NULL,
    verifier          BYTEA NOT NULL,
    verifier_version  INT NOT NULL DEFAULT 1 CHECK (verifier_version IN (0, 1)),
    pepper_version    INT NOT NULL DEFAULT 1 CHECK (pepper_version >= 1),
    status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    activated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at        TIMESTAMPTZ,
    last_used_at      TIMESTAMPTZ
);
CREATE INDEX idx_device_credentials_device ON device_credentials (device_id, status);

INSERT INTO device_credentials
    (id, device_id, salt, verifier, verifier_version, pepper_version,
     status, created_at, activated_at, revoked_at)
SELECT gen_random_uuid(), id, secret_salt, secret_hash, 0, 1,
    CASE status WHEN 'active' THEN 'active' ELSE 'revoked' END,
    created_at, created_at,
    CASE status WHEN 'active' THEN NULL ELSE revoked_at END
FROM devices;

ALTER TABLE devices DROP COLUMN secret_hash;
ALTER TABLE devices DROP COLUMN secret_salt;

-- ==== 00003_sync_events ====
-- Phase 1B: durable sync event ingestion. Accepted events are immutable
-- business-integration history; no projection tables yet. Globally unique
-- event_id enforced by PRIMARY KEY so concurrent duplicates stay safe;
-- device ownership recorded but never trusted from client input.
CREATE TABLE sync_events (
    event_id      UUID PRIMARY KEY,
    device_id     UUID NOT NULL REFERENCES devices(id) ON DELETE RESTRICT,
    credential_id UUID REFERENCES device_credentials(id) ON DELETE SET NULL,
    event_type    TEXT NOT NULL CHECK (event_type ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)*\.v[0-9]+$'),
    occurred_at   TIMESTAMPTZ NOT NULL,
    received_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    payload       JSONB NOT NULL,
    payload_hash  BYTEA NOT NULL
);
CREATE INDEX idx_sync_events_device_received ON sync_events (device_id, received_at);
CREATE INDEX idx_sync_events_type ON sync_events (event_type);

-- ==== 00004_sale_projection ====
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
    currency           TEXT NOT NULL CHECK (currency IN ('EGP', 'USD')),
    subtotal_minor     BIGINT NOT NULL CHECK (subtotal_minor >= 0),
    discount_minor     BIGINT NOT NULL CHECK (discount_minor >= 0),
    tax_minor          BIGINT NOT NULL CHECK (tax_minor >= 0),
    total_minor        BIGINT NOT NULL CHECK (total_minor >= 0),
    fx_base            TEXT,
    fx_quote           TEXT,
    fx_rate_microrate  BIGINT CHECK (fx_rate_microrate IS NULL OR fx_rate_microrate > 0),
    received_at        TIMESTAMPTZ NOT NULL,
    projected_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_sales_projection_occurred ON sales_projection (occurred_at);
CREATE INDEX idx_sales_projection_channel_occurred ON sales_projection (channel, occurred_at);
CREATE INDEX idx_sales_projection_device_occurred ON sales_projection (source_device_id, occurred_at);

-- ADR-0055 step 2d: one currency per document. Line and payment rows carry
-- amounts only; the header `currency` applies (validators reject any
-- money whose currency differs from the header).
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
    cost_minor       BIGINT CHECK (cost_minor IS NULL OR cost_minor >= 0),
    line_total_minor BIGINT NOT NULL CHECK (line_total_minor >= 0),
    PRIMARY KEY (sale_id, sale_item_id)
);
CREATE INDEX idx_sale_lines_product ON sale_lines_projection (product_id);

CREATE TABLE sale_payments_projection (
    sale_id          UUID NOT NULL REFERENCES sales_projection(sale_id) ON DELETE CASCADE,
    position         INT NOT NULL,
    method           TEXT NOT NULL CHECK (method IN ('cash', 'card')),
    amount_minor     BIGINT NOT NULL CHECK (amount_minor >= 0),
    change_minor     BIGINT NOT NULL CHECK (change_minor >= 0),
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

-- ==== 00005_sync_event_processing ====
-- Phase 2B: durable projector processing state. Small status model:
-- pending (claimed work), retry (transient failure, due at next_attempt_at),
-- blocked (deterministic integrity failure, needs operator), processed.
-- Rows are created lazily by the projector's durable discovery scan, so no
-- ingestion-transaction coupling is required for crash safety.
CREATE TABLE sync_event_processing (
    event_id         UUID NOT NULL REFERENCES sync_events(event_id) ON DELETE CASCADE,
    processor        TEXT NOT NULL,
    status           TEXT NOT NULL CHECK (status IN ('pending', 'retry', 'blocked', 'processed')),
    attempt_count    INT NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    last_attempt_at  TIMESTAMPTZ,
    next_attempt_at  TIMESTAMPTZ,
    processed_at     TIMESTAMPTZ,
    last_error_code  TEXT,
    last_error_message TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, processor)
);
CREATE INDEX idx_processing_due ON sync_event_processing (processor, status, next_attempt_at);

-- ==== 00006_sync_event_hash_version ====
-- ADR-0055: payload_hash_version and the float64 v1 canonicalizer were
-- removed; every payload_hash is the exact canonical hash.

-- ==== 00007_sale_event_ownership ====
-- Phase 2E: durable Sale ownership arbitration (P2D-HIGH-02). The live
-- projector already arbitrates concurrent events, but ownership lived only
-- in disposable sales_projection rows: a rebuild could elect a different
-- winner and rewrite financial history. This table persists the logical
-- winner outside derived state:
--   sync_events ............ immutable accepted event history
--   sale_event_ownership ... durable conflict-arbitration decision
--   sales_projection + children ... disposable, rebuildable derived state
-- Rebuilds may delete projections (and reset processing) but must never
-- delete sync_events or sale_event_ownership.
CREATE TABLE sale_event_ownership (
    sale_id            UUID PRIMARY KEY,
    winning_event_id   UUID NOT NULL UNIQUE REFERENCES sync_events(event_id) ON DELETE RESTRICT,
    winning_device_id  UUID NOT NULL REFERENCES devices(id) ON DELETE RESTRICT,
    decided_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Backfill from existing projections: the projected source_event_id defines
-- current ownership (never receipt order). The guard fails the migration
-- with division-by-zero if the projection history cannot yield a unique
-- authoritative mapping instead of choosing arbitrary winners.
-- (Single-statement form: goose splits migration files on semicolons, so no
-- DO blocks are used anywhere in this history. The inner aggregate defeats
-- constant folding so the error fires only when the guard trips.)
SELECT CASE WHEN (SELECT count(*) FROM (
    SELECT sale_id FROM sales_projection GROUP BY sale_id HAVING count(*) > 1
) d) > 0 THEN ((SELECT count(*)/0 FROM sales_projection)) ELSE 0 END;
INSERT INTO sale_event_ownership (sale_id, winning_event_id, winning_device_id, decided_at)
SELECT sale_id, source_event_id, source_device_id, projected_at
FROM sales_projection
ON CONFLICT (sale_id) DO NOTHING;

-- ==== 00008_return_refund_projection ====
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
    currency           TEXT NOT NULL CHECK (currency IN ('EGP', 'USD')),
    gross_refunded_minor    BIGINT NOT NULL CHECK (gross_refunded_minor >= 0),
    discount_refunded_minor BIGINT NOT NULL CHECK (discount_refunded_minor >= 0),
    tax_refunded_minor      BIGINT NOT NULL CHECK (tax_refunded_minor >= 0),
    refund_total_minor      BIGINT NOT NULL CHECK (refund_total_minor >= 0),
    fx_base            TEXT,
    fx_quote           TEXT,
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

-- One currency per document (ADR-0055 step 2d): the header currency applies.
CREATE TABLE return_refund_lines_projection (
    return_refund_id      UUID NOT NULL REFERENCES return_refund_projection(return_refund_id) ON DELETE CASCADE,
    sale_id               UUID NOT NULL,
    original_sale_line_id UUID NOT NULL,
    position              INT NOT NULL,
    product_id            UUID,
    quantity              INT NOT NULL CHECK (quantity >= 1),
    restocked             BOOLEAN NOT NULL,
    gross_minor           BIGINT NOT NULL CHECK (gross_minor >= 0),
    discount_minor        BIGINT NOT NULL CHECK (discount_minor >= 0),
    tax_minor             BIGINT NOT NULL CHECK (tax_minor >= 0),
    refund_minor          BIGINT NOT NULL CHECK (refund_minor >= 0),
    cost_minor            BIGINT CHECK (cost_minor IS NULL OR cost_minor >= 0),
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

-- ==== 00009_catalog_projection ====
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
    name_ar             TEXT NOT NULL,
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
    -- ADR-0055 / Retail ADR-049 1f: localized names are columns.
    name_ar             TEXT NOT NULL,
    name_en             TEXT,
    description_ar      TEXT,
    description_en      TEXT,
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

-- ==== 00010_product_sales_policy ====
-- Phase 5B: product sales-policy projection (ADR in docs/decisions).
-- Retail owns channel/allocation policy; Cloud converges to the highest
-- valid per-product revision. No stock, no availability, no reservations,
-- no provider identity anywhere in this projection. Rebuild: clear this
-- table and reset the policy processor; inbox history replays
-- deterministically (highest valid revision wins regardless of order).

CREATE TABLE catalog_product_sales_policies (
    product_id              UUID PRIMARY KEY REFERENCES catalog_products(product_id) ON DELETE CASCADE,
    sell_offline            BOOLEAN NOT NULL,
    sell_online             BOOLEAN NOT NULL,
    online_allocation_limit BIGINT CHECK (online_allocation_limit IS NULL OR online_allocation_limit >= 0),
    source_revision         BIGINT NOT NULL CHECK (source_revision >= 1),
    source_event_id         UUID NOT NULL REFERENCES sync_events(event_id),
    source_device_id        UUID NOT NULL REFERENCES devices(id),
    source_payload_hash     BYTEA NOT NULL,
    source_received_at      TIMESTAMPTZ NOT NULL,
    projected_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (sell_online OR online_allocation_limit IS NULL)
);
CREATE INDEX idx_catalog_policies_online ON catalog_product_sales_policies (sell_online) WHERE sell_online;

-- ==== 00012_commerce_product_mappings ====
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

-- ==== 00013_commerce_orders ====
-- Phase 6C: provider-neutral online-order domain (ADR in docs/decisions).
-- Webhook deliveries are durable reconciliation triggers (minimal
-- metadata + payload hash; never raw bodies or PII). commerce_online_orders and
-- children are the current provider-derived projection with monotonic
-- MoonLight revisions and append-only status history. These tables are
-- durable integration state like provider mappings: no rebuild path may
-- delete them, and they require normal database backup.
-- Order lines reference MoonLight products by stable UUID WITHOUT a
-- foreign key to rebuildable catalog_products, so catalog rebuilds can
-- never cascade historical order identity away.

CREATE TABLE commerce_online_order_webhook_events (
    provider_key      TEXT NOT NULL CHECK (provider_key ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    delivery_id       TEXT NOT NULL CHECK (char_length(delivery_id) BETWEEN 1 AND 200),
    topic             TEXT NOT NULL CHECK (topic IN ('order.created', 'order.updated', 'order.deleted')),
    external_order_id TEXT NOT NULL CHECK (char_length(external_order_id) BETWEEN 1 AND 32),
    payload_hash      BYTEA NOT NULL,
    webhook_id        TEXT CHECK (webhook_id IS NULL OR char_length(webhook_id) BETWEEN 1 AND 64),
    received_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    status            TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending', 'retry', 'processed', 'blocked')),
    attempt_count     INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at   TIMESTAMPTZ,
    processed_at      TIMESTAMPTZ,
    last_error_code   TEXT,
    lease_owner       TEXT,
    lease_until       TIMESTAMPTZ,
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider_key, delivery_id)
);
CREATE INDEX idx_order_webhook_due ON commerce_online_order_webhook_events (status, next_attempt_at, received_at);
CREATE INDEX idx_order_webhook_order ON commerce_online_order_webhook_events (provider_key, external_order_id);

CREATE TABLE commerce_online_orders (
    provider_key       TEXT NOT NULL CHECK (provider_key ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    external_order_id  TEXT NOT NULL CHECK (char_length(external_order_id) BETWEEN 1 AND 32),
    order_number       TEXT NOT NULL DEFAULT '' CHECK (char_length(order_number) <= 64),
    provider_status    TEXT NOT NULL DEFAULT '' CHECK (char_length(provider_status) <= 32),
    canonical_status   TEXT NOT NULL CHECK (canonical_status IN
        ('pending', 'processing', 'on_hold', 'completed', 'cancelled', 'refunded', 'failed', 'unknown', 'deleted')),
    currency           TEXT NOT NULL DEFAULT '' CHECK (char_length(currency) <= 8),
    discount_minor     BIGINT NOT NULL,
    shipping_minor     BIGINT NOT NULL,
    cart_tax_minor     BIGINT NOT NULL,
    total_tax_minor    BIGINT NOT NULL,
    total_minor        BIGINT NOT NULL,
    prices_include_tax BOOLEAN NOT NULL DEFAULT FALSE,
    created_at         TIMESTAMPTZ NOT NULL,
    modified_at        TIMESTAMPTZ NOT NULL,
    paid_at            TIMESTAMPTZ,
    completed_at       TIMESTAMPTZ,
    payment_method     TEXT NOT NULL DEFAULT '' CHECK (char_length(payment_method) <= 64),
    payment_method_title TEXT NOT NULL DEFAULT '' CHECK (char_length(payment_method_title) <= 128),
    customer_first_name TEXT NOT NULL DEFAULT '',
    customer_last_name  TEXT NOT NULL DEFAULT '',
    customer_email      TEXT NOT NULL DEFAULT '',
    customer_phone      TEXT NOT NULL DEFAULT '',
    revision           BIGINT NOT NULL CHECK (revision >= 1),
    fingerprint        BYTEA NOT NULL,
    provider_deleted   BOOLEAN NOT NULL DEFAULT FALSE,
    mapping_complete   BOOLEAN NOT NULL DEFAULT FALSE,
    unmapped_lines     INTEGER NOT NULL DEFAULT 0 CHECK (unmapped_lines >= 0),
    projected_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider_key, external_order_id)
);
CREATE INDEX idx_commerce_orders_created ON commerce_online_orders (created_at DESC, provider_key, external_order_id);
CREATE INDEX idx_commerce_orders_status ON commerce_online_orders (canonical_status, created_at DESC);

CREATE TABLE commerce_online_order_lines (
    provider_key        TEXT NOT NULL,
    external_order_id   TEXT NOT NULL,
    external_line_id    BIGINT NOT NULL CHECK (external_line_id > 0),
    external_product_id TEXT NOT NULL DEFAULT '' CHECK (char_length(external_product_id) <= 32),
    variation_id        BIGINT NOT NULL DEFAULT 0 CHECK (variation_id >= 0),
    sku                 TEXT NOT NULL DEFAULT '',
    name                TEXT NOT NULL DEFAULT '',
    quantity            BIGINT NOT NULL CHECK (quantity > 0),
    subtotal_minor      BIGINT NOT NULL,
    subtotal_tax_minor  BIGINT NOT NULL,
    total_minor         BIGINT NOT NULL,
    total_tax_minor     BIGINT NOT NULL,
    moonlight_product_id UUID,
    mapped              BOOLEAN NOT NULL DEFAULT FALSE,
    unsupported_reason  TEXT NOT NULL DEFAULT '' CHECK (char_length(unsupported_reason) <= 32),
    PRIMARY KEY (provider_key, external_order_id, external_line_id)
);
CREATE INDEX idx_commerce_online_order_lines_order ON commerce_online_order_lines (provider_key, external_order_id);

CREATE TABLE commerce_online_order_addresses (
    provider_key      TEXT NOT NULL,
    external_order_id TEXT NOT NULL,
    kind              TEXT NOT NULL CHECK (kind IN ('billing', 'shipping')),
    first_name        TEXT NOT NULL DEFAULT '',
    last_name         TEXT NOT NULL DEFAULT '',
    company           TEXT NOT NULL DEFAULT '',
    address_1         TEXT NOT NULL DEFAULT '',
    address_2         TEXT NOT NULL DEFAULT '',
    city              TEXT NOT NULL DEFAULT '',
    state             TEXT NOT NULL DEFAULT '',
    postcode          TEXT NOT NULL DEFAULT '',
    country           TEXT NOT NULL DEFAULT '',
    email             TEXT NOT NULL DEFAULT '',
    phone             TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (provider_key, external_order_id, kind)
);

CREATE TABLE commerce_online_order_status_history (
    provider_key       TEXT NOT NULL,
    external_order_id  TEXT NOT NULL,
    order_revision     BIGINT NOT NULL CHECK (order_revision >= 1),
    provider_status    TEXT NOT NULL DEFAULT '' CHECK (char_length(provider_status) <= 32),
    canonical_status   TEXT NOT NULL CHECK (canonical_status IN
        ('pending', 'processing', 'on_hold', 'completed', 'cancelled', 'refunded', 'failed', 'unknown', 'deleted')),
    provider_modified_at TIMESTAMPTZ,
    observed_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider_key, external_order_id, order_revision)
);

-- ==== 00014_order_concurrency_fences ====
-- Phase 6C-R1: order concurrency fencing (F-01/F-02). The reconcile
-- fence is operational integration state: per-order generation tokens
-- that make later-started reconciliations authoritative. It survives
-- restarts and multiple instances, is never deleted by
-- catalog/policy/inventory rebuilds, and needs normal database backup
-- like other commerce integration state. It is NOT business order
-- history. No garbage collection in R1: one tiny row per observed
-- provider order.
-- Webhook deliveries gain a monotonic claim generation so stale
-- workers cannot finalize events owned by newer leases. Existing rows
-- start at 0 and remain fully claimable; terminal rows stay terminal.

CREATE TABLE commerce_online_order_reconcile_fences (
    provider_key      TEXT NOT NULL CHECK (provider_key ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    external_order_id TEXT NOT NULL CHECK (char_length(external_order_id) BETWEEN 1 AND 32),
    generation        BIGINT NOT NULL CHECK (generation >= 1),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider_key, external_order_id)
);

ALTER TABLE commerce_online_order_webhook_events
    ADD COLUMN lease_generation BIGINT NOT NULL DEFAULT 0 CHECK (lease_generation >= 0);

-- ==== 00015_notifications ====
-- Phase 7A: provider-neutral notification domain with WhatsApp as the
-- first adapter. notification_template_mappings resolves logical
-- (template_key, locale) to approved external templates so future
-- business phases never embed Meta names. notification_messages is the
-- durable idempotent outbox with lease fencing and a durable
-- send-start marker: an unknown remote outcome is AMBIGUOUS, never
-- auto-retried. notification_delivery_status_history records provider
-- callbacks idempotently. All three are Cloud-only durable integration
-- state: back them up; no catalog/policy/inventory/order rebuild may
-- delete them, and there is no automatic retention purge.

CREATE TABLE notification_template_mappings (
    provider_key           TEXT NOT NULL CHECK (provider_key ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    template_key           TEXT NOT NULL CHECK (template_key ~ '^[a-z0-9][a-z0-9_]{0,63}$'),
    locale                 TEXT NOT NULL CHECK (char_length(locale) BETWEEN 2 AND 12),
    external_template_name TEXT NOT NULL CHECK (char_length(external_template_name) BETWEEN 1 AND 128),
    external_language_code TEXT NOT NULL CHECK (char_length(external_language_code) BETWEEN 1 AND 16),
    parameter_names        TEXT[] NOT NULL DEFAULT '{}',
    enabled                BOOLEAN NOT NULL DEFAULT TRUE,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider_key, template_key, locale)
);

CREATE TABLE notification_messages (
    id                     UUID PRIMARY KEY,
    provider_key           TEXT NOT NULL CHECK (provider_key ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    idempotency_key        TEXT NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
    semantic_fingerprint   BYTEA NOT NULL,
    recipient              TEXT NOT NULL CHECK (char_length(recipient) BETWEEN 1 AND 32),
    template_key           TEXT NOT NULL CHECK (template_key ~ '^[a-z0-9][a-z0-9_]{0,63}$'),
    locale                 TEXT NOT NULL CHECK (char_length(locale) BETWEEN 2 AND 12),
    parameters             JSONB NOT NULL DEFAULT '{}',
    ext_template_name      TEXT NOT NULL CHECK (char_length(ext_template_name) BETWEEN 1 AND 128),
    ext_language_code      TEXT NOT NULL CHECK (char_length(ext_language_code) BETWEEN 1 AND 16),
    ext_parameter_order    TEXT[] NOT NULL DEFAULT '{}',
    dispatch_status        TEXT NOT NULL DEFAULT 'pending'
                           CHECK (dispatch_status IN ('pending', 'retry', 'accepted', 'blocked', 'ambiguous')),
    attempt_count          INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at        TIMESTAMPTZ,
    lease_owner            TEXT,
    lease_until            TIMESTAMPTZ,
    lease_generation       BIGINT NOT NULL DEFAULT 0 CHECK (lease_generation >= 0),
    send_started_at        TIMESTAMPTZ,
    provider_message_id    TEXT CHECK (provider_message_id IS NULL OR char_length(provider_message_id) BETWEEN 1 AND 256),
    delivery_status        TEXT NOT NULL DEFAULT 'unknown'
                           CHECK (delivery_status IN ('unknown', 'accepted', 'sent', 'delivered', 'read', 'failed')),
    delivery_status_at     TIMESTAMPTZ,
    last_error_code        TEXT,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider_key, idempotency_key),
    UNIQUE (provider_key, provider_message_id)
);
CREATE INDEX idx_notification_due ON notification_messages (dispatch_status, next_attempt_at, created_at);
CREATE INDEX idx_notification_lease ON notification_messages (lease_until) WHERE dispatch_status IN ('pending', 'retry');

CREATE TABLE notification_delivery_status_history (
    notification_id      UUID NOT NULL REFERENCES notification_messages(id),
    provider_key         TEXT NOT NULL,
    provider_message_id  TEXT NOT NULL,
    provider_status_raw  TEXT NOT NULL CHECK (char_length(provider_status_raw) BETWEEN 1 AND 64),
    canonical_status     TEXT NOT NULL
                         CHECK (canonical_status IN ('unknown', 'accepted', 'sent', 'delivered', 'read', 'failed')),
    provider_timestamp   TIMESTAMPTZ,
    event_fingerprint    BYTEA NOT NULL,
    provider_error_code  TEXT CHECK (provider_error_code IS NULL OR char_length(provider_error_code) BETWEEN 1 AND 64),
    observed_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (notification_id, event_fingerprint)
);
CREATE INDEX idx_notification_history_lookup ON notification_delivery_status_history (provider_key, provider_message_id);

-- ==== 00016_business_reports ====
-- Phase 7B: durable scheduled business reports. Recipients, schedules,
-- runs, and per-recipient deliveries are Cloud-only durable state:
-- back them up; no catalog/policy/inventory/order/sale rebuild may
-- delete them, and there is no automatic retention purge. All
-- financial calculations stay in the frozen reporting domain: this
-- migration stores only configuration, slot/run identity, immutable
-- snapshots, and delivery records. No destructive FKs to rebuildable
-- projections; notification linkage is a logical UUID reference.

-- ADR-0055 step 2e: business-report and operational-alert recipients share
-- one table; `purpose` says which feature owns the row. Telegram allows 32
-- username characters plus the @ destination prefix.
CREATE TABLE notification_recipients (
    id            UUID PRIMARY KEY,
    purpose       TEXT NOT NULL CHECK (purpose IN ('business_report', 'operational_alert')),
    label         TEXT NOT NULL CHECK (char_length(label) BETWEEN 1 AND 64),
    provider_key  TEXT NOT NULL CHECK (provider_key ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    recipient     TEXT NOT NULL CHECK (char_length(recipient) BETWEEN 1 AND 33),
    locale        TEXT NOT NULL CHECK (locale IN ('ar', 'en')),
    enabled       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (id, purpose)
);
CREATE INDEX idx_notification_recipients_purpose ON notification_recipients (purpose, created_at, id);

CREATE TABLE business_report_schedules (
    id                  UUID PRIMARY KEY,
    name                TEXT NOT NULL UNIQUE CHECK (char_length(name) BETWEEN 1 AND 64),
    report_kind         TEXT NOT NULL CHECK (report_kind IN ('daily', 'ten_day')),
    timezone            TEXT NOT NULL CHECK (timezone = 'Africa/Cairo'),
    local_time          TEXT NOT NULL CHECK (local_time ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
    anchor_local_date   DATE,
    enabled             BOOLEAN NOT NULL DEFAULT TRUE,
    revision            BIGINT NOT NULL DEFAULT 1 CHECK (revision >= 1),
    next_run_local_date DATE NOT NULL,
    next_run_at         TIMESTAMPTZ NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((report_kind = 'ten_day' AND anchor_local_date IS NOT NULL) OR
           (report_kind = 'daily'))
);
CREATE INDEX idx_report_schedules_due ON business_report_schedules (enabled, next_run_at);

CREATE TABLE business_report_schedule_recipients (
    schedule_id       UUID NOT NULL REFERENCES business_report_schedules(id) ON DELETE CASCADE,
    recipient_id      UUID NOT NULL,
    -- Only business-report recipients can be scheduled.
    recipient_purpose TEXT NOT NULL DEFAULT 'business_report' CHECK (recipient_purpose = 'business_report'),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (schedule_id, recipient_id),
    FOREIGN KEY (recipient_id, recipient_purpose) REFERENCES notification_recipients (id, purpose)
);

CREATE TABLE business_report_runs (
    id                     UUID PRIMARY KEY,
    schedule_id            UUID NOT NULL REFERENCES business_report_schedules(id),
    run_kind               TEXT NOT NULL CHECK (run_kind IN ('scheduled', 'manual')),
    slot_local_date        DATE,
    manual_idempotency_key TEXT CHECK (manual_idempotency_key IS NULL OR char_length(manual_idempotency_key) BETWEEN 1 AND 128),
    scheduled_for          TIMESTAMPTZ NOT NULL,
    period_start           TIMESTAMPTZ NOT NULL,
    period_end             TIMESTAMPTZ NOT NULL,
    schedule_revision      BIGINT NOT NULL CHECK (schedule_revision >= 1),
    status                 TEXT NOT NULL DEFAULT 'pending'
                           CHECK (status IN ('pending', 'retry', 'completed', 'blocked')),
    attempt_count          INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at        TIMESTAMPTZ,
    lease_owner            TEXT,
    lease_until            TIMESTAMPTZ,
    lease_generation       BIGINT NOT NULL DEFAULT 0 CHECK (lease_generation >= 0),
    last_error_code        TEXT,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((run_kind = 'scheduled' AND slot_local_date IS NOT NULL AND manual_idempotency_key IS NULL) OR
           (run_kind = 'manual' AND slot_local_date IS NULL AND manual_idempotency_key IS NOT NULL))
);
CREATE UNIQUE INDEX idx_report_runs_slot ON business_report_runs (schedule_id, slot_local_date)
    WHERE run_kind = 'scheduled';
CREATE UNIQUE INDEX idx_report_runs_manual ON business_report_runs (schedule_id, manual_idempotency_key)
    WHERE run_kind = 'manual';
CREATE INDEX idx_report_runs_due ON business_report_runs (status, next_attempt_at, created_at);

CREATE TABLE business_report_deliveries (
    id                          UUID PRIMARY KEY,
    run_id                      UUID NOT NULL REFERENCES business_report_runs(id) ON DELETE CASCADE,
    recipient_id                UUID NOT NULL,
    provider_key                TEXT NOT NULL,
    recipient_snapshot          TEXT NOT NULL,
    locale_snapshot             TEXT NOT NULL,
    template_key                TEXT NOT NULL,
    report_body_snapshot        TEXT,
    report_fingerprint          BYTEA,
    notification_idempotency_key TEXT NOT NULL,
    notification_id             UUID,
    status                      TEXT NOT NULL DEFAULT 'pending'
                                CHECK (status IN ('pending', 'enqueued', 'blocked')),
    last_error_code             TEXT,
    created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (run_id, recipient_id),
    UNIQUE (notification_idempotency_key)
);
CREATE INDEX idx_report_deliveries_run ON business_report_deliveries (run_id);

-- ==== 00017_device_connectivity_control ====
-- Phase 7C: durable device connectivity + remote sync control plane.
-- Retail initiates every connection (outbound HTTPS polling); Cloud never
-- dials Retail. Presence and commands are Cloud durable operational state:
-- back them up; no projection rebuild may delete them; no retention purge.
-- Commands carry no business payload in v1 (sync_now.v1 only); result codes
-- are bounded machine codes, never raw errors or customer data.

-- Last contact is devices.last_seen_at (ADR-0055 step 2e); presence keeps
-- only the control-plane timestamps.
CREATE TABLE device_control_presence (
    device_id              UUID PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    last_poll_at           TIMESTAMPTZ,
    last_command_accepted_at TIMESTAMPTZ,
    last_command_finished_at TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE device_control_commands (
    id               UUID PRIMARY KEY,
    device_id        UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    command_type     TEXT NOT NULL CHECK (command_type = 'sync_now'),
    command_version  INTEGER NOT NULL CHECK (command_version = 1),
    idempotency_key  TEXT NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128 AND idempotency_key ~ '^[A-Za-z0-9_:.~-]{1,128}$'),
    status           TEXT NOT NULL CHECK (status IN ('pending','leased','accepted','running','completed','failed')),
    requested_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    leased_at        TIMESTAMPTZ,
    lease_until      TIMESTAMPTZ,
    lease_generation BIGINT NOT NULL DEFAULT 0 CHECK (lease_generation >= 0),
    accepted_at      TIMESTAMPTZ,
    running_at       TIMESTAMPTZ,
    finished_at      TIMESTAMPTZ,
    result_code      TEXT CHECK (result_code IS NULL OR (char_length(result_code) BETWEEN 1 AND 64 AND result_code ~ '^[A-Z][A-Z0-9_]{0,63}$')),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((status IN ('pending') AND lease_until IS NULL) OR (status NOT IN ('pending')) OR (status = 'pending')),
    CHECK ((status IN ('completed','failed') AND finished_at IS NOT NULL AND result_code IS NOT NULL) OR (status NOT IN ('completed','failed')))
);

-- One active sync_now per device: correctness enforced by the database,
-- never by dashboard UI alone.
CREATE UNIQUE INDEX idx_device_commands_one_active
    ON device_control_commands (device_id)
    WHERE status IN ('pending','leased','accepted','running');

-- Dashboard idempotency: same device + same key returns the same command.
CREATE UNIQUE INDEX idx_device_commands_idempotency
    ON device_control_commands (device_id, idempotency_key);

-- Poll lookup: oldest due command per device (pending or expired lease).
CREATE INDEX idx_device_commands_poll
    ON device_control_commands (device_id, status, requested_at, id)
    WHERE status IN ('pending','leased');

-- Lease expiry scan.
CREATE INDEX idx_device_commands_lease
    ON device_control_commands (status, lease_until)
    WHERE status = 'leased';

-- Recent history per device.
CREATE INDEX idx_device_commands_history
    ON device_control_commands (device_id, requested_at DESC, id);

-- ==== 00018_device_control_pending_lease_constraint ====
-- Phase 7C-L1: replace the vacuous pending/lease CHECK from 00017 (which
-- OR-ed tautologies and enforced nothing) with a named constraint that
-- actually holds the intended invariant: a pending command is never leased.
-- Committed migrations are frozen, so the old CHECK stays untouched; this
-- forward migration adds the real enforcement.
--
-- Fail-closed upgrade: the constraint is added NOT VALID (no scan, no
-- lock rewrite), then VALIDATE CONSTRAINT scans existing rows. If legacy
-- rows violate the invariant, validation aborts with a bounded diagnostic
-- naming this constraint and the whole upgrade rolls back without touching
-- frozen state. Operator handling: reconcile the offending pending
-- commands first (let leases expire and converge, or drive them terminal),
-- then re-run the upgrade. Never delete rows or rewrite terminal history.
-- (A DO-block pre-check would be friendlier, but goose splits statements
-- on semicolons; NOT VALID + VALIDATE keeps the migration parser-safe.)

ALTER TABLE device_control_commands
    ADD CONSTRAINT device_control_commands_pending_lease_null
    CHECK (status <> 'pending' OR lease_until IS NULL) NOT VALID;

ALTER TABLE device_control_commands
    VALIDATE CONSTRAINT device_control_commands_pending_lease_null;

-- ==== 00019_operational_alerts ====
-- Phase 7D: durable operational incidents, alert deliveries, recovery
-- actions, and alert recipients. All operational audit/history: back them
-- up; no projection rebuild may delete them; no retention purge. Subjects
-- and notifications are logical UUID references (no destructive cascades):
-- incident history survives device revocation, notification changes, and
-- report-projection rebuilds. Bodies carry bounded machine summaries only:
-- no raw errors, customer payloads, provider bodies, or credentials.

-- Alert recipients live in notification_recipients (purpose 'operational_alert').

CREATE TABLE operational_incidents (
    id               UUID PRIMARY KEY,
    rule_key         TEXT NOT NULL CHECK (rule_key ~ '^[A-Z][A-Z0-9_]{0,63}$'),
    subject_type     TEXT NOT NULL CHECK (subject_type IN ('device', 'sync_command', 'report_run', 'notification')),
    subject_id       TEXT NOT NULL CHECK (char_length(subject_id) BETWEEN 1 AND 64),
    severity         TEXT NOT NULL CHECK (severity IN ('warning', 'urgent')),
    state            TEXT NOT NULL CHECK (state IN ('open', 'acknowledged', 'resolved')),
    episode          INTEGER NOT NULL DEFAULT 1 CHECK (episode >= 1),
    source_event_key TEXT CHECK (source_event_key IS NULL OR char_length(source_event_key) BETWEEN 1 AND 128),
    opened_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    acknowledged_at  TIMESTAMPTZ,
    resolved_at      TIMESTAMPTZ,
    resolution_code  TEXT CHECK (resolution_code IS NULL OR (char_length(resolution_code) BETWEEN 1 AND 64 AND resolution_code ~ '^[A-Z][A-Z0-9_]{0,63}$')),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((state = 'resolved' AND resolved_at IS NOT NULL) OR (state <> 'resolved')),
    CHECK ((state = 'acknowledged' AND acknowledged_at IS NOT NULL) OR (state <> 'acknowledged'))
);

-- One active stateful incident per rule+subject; multi-instance open
-- converges here (INSERT ... ON CONFLICT DO NOTHING).
CREATE UNIQUE INDEX idx_operations_incident_active
    ON operational_incidents (rule_key, subject_type, subject_id)
    WHERE state IN ('open', 'acknowledged');

-- Terminal-event dedupe, all-time: resolving never recreates.
CREATE UNIQUE INDEX idx_operations_incident_event
    ON operational_incidents (source_event_key)
    WHERE source_event_key IS NOT NULL;

CREATE INDEX idx_operations_incidents_state ON operational_incidents (state, opened_at DESC, id DESC);
CREATE INDEX idx_operations_incidents_subject ON operational_incidents (subject_type, subject_id, opened_at DESC);

CREATE TABLE operational_alert_deliveries (
    id                         UUID PRIMARY KEY,
    incident_id                UUID NOT NULL,
    recipient_id               UUID NOT NULL,
    event_type                 TEXT NOT NULL CHECK (event_type IN ('opened', 'resolved')),
    provider_key_snapshot      TEXT NOT NULL,
    recipient_snapshot         TEXT NOT NULL,
    locale_snapshot            TEXT NOT NULL CHECK (locale_snapshot IN ('ar', 'en')),
    template_key               TEXT NOT NULL,
    body_snapshot              TEXT NOT NULL CHECK (char_length(body_snapshot) BETWEEN 1 AND 1024),
    body_fingerprint           BYTEA NOT NULL,
    notification_id            UUID,
    notification_idempotency_key TEXT NOT NULL CHECK (char_length(notification_idempotency_key) BETWEEN 1 AND 128),
    status                     TEXT NOT NULL CHECK (status IN ('pending', 'sent', 'blocked')),
    last_error_code            TEXT CHECK (last_error_code IS NULL OR char_length(last_error_code) BETWEEN 1 AND 64),
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (notification_idempotency_key)
);
CREATE INDEX idx_operations_deliveries_incident ON operational_alert_deliveries (incident_id, event_type);
CREATE INDEX idx_operations_deliveries_pending ON operational_alert_deliveries (status, created_at) WHERE status = 'pending';

CREATE TABLE operational_recovery_actions (
    id               UUID PRIMARY KEY,
    incident_id      UUID NOT NULL,
    action_type      TEXT NOT NULL CHECK (action_type = 'DEVICE_RECONNECT_SYNC'),
    state            TEXT NOT NULL CHECK (state IN ('pending', 'completed', 'blocked')),
    idempotency_key  TEXT NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 128),
    target_entity_id TEXT CHECK (target_entity_id IS NULL OR char_length(target_entity_id) BETWEEN 1 AND 64),
    result_code      TEXT CHECK (result_code IS NULL OR (char_length(result_code) BETWEEN 1 AND 64 AND result_code ~ '^[A-Z][A-Z0-9_]{0,63}$')),
    attempt_count    INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at  TIMESTAMPTZ,
    last_error_code  TEXT CHECK (last_error_code IS NULL OR char_length(last_error_code) BETWEEN 1 AND 64),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (incident_id, action_type)
);
CREATE INDEX idx_operations_recovery_pending ON operational_recovery_actions (state, next_attempt_at, created_at) WHERE state = 'pending';

-- Detector scan support on frozen tables (read-only indexes; no
-- semantics change): failed-command and stale-command lookups must not
-- rescan full history every tick.
CREATE INDEX idx_operations_commands_failed ON device_control_commands (status, finished_at, id) WHERE status = 'failed';
CREATE INDEX idx_operations_commands_stale ON device_control_commands (status, requested_at, id) WHERE status IN ('pending', 'accepted', 'running');

-- ==== 00020_operational_integrity ====
-- Phase 7D-R1 operational integrity hardening. Append-only; frozen
-- migrations 00009–00019 are untouched.
--
-- 1. Delivery identity (H01): exactly one durable resolved/opened event
--    per (incident, event, recipient). Concurrent scanners converge here
--    instead of minting duplicate Phase 7A idempotency keys. The plain
--    ADD CONSTRAINT scans and fails closed naming this constraint if
--    duplicate identities already exist (possible only from concurrent
--    pre-fix writers); reconcile by keeping one delivery per identity,
--    then re-run. (NOT VALID is unavailable for UNIQUE constraints.)
-- 2. Intent materialization flags (M01): durable transactional-outbox
--    state distinguishing "intent missing, must repair" (FALSE) from
--    "intentionally empty snapshot" (TRUE with zero deliveries). Rows
--    written before this migration read FALSE and are repaired by the
--    detector without touching existing snapshots.
-- 3. Stale-command index (M04): leased commands join the overall-age
--    stale predicate; the replacement partial index covers them.

ALTER TABLE operational_incidents
    ADD COLUMN open_intent_materialized BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN resolved_intent_materialized BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE operational_alert_deliveries
    ADD CONSTRAINT operational_deliveries_identity_unique
    UNIQUE (incident_id, event_type, recipient_id);

DROP INDEX IF EXISTS idx_operations_commands_stale;
CREATE INDEX idx_operations_commands_stale ON device_control_commands (status, requested_at, id) WHERE status IN ('pending', 'leased', 'accepted', 'running');

-- Resolution round-robin: least-recently-checked active incidents first.
CREATE INDEX idx_operations_incidents_resolve ON operational_incidents (rule_key, last_observed_at, id) WHERE state IN ('open', 'acknowledged');

-- ==== 00021_sale_historical_tags ====
-- Phase 8C historical sale tag snapshots. Sale-time tag membership is
-- immutable history: tag_id is a historical identity with NO foreign key
-- to catalog_tags (renames, hides, and terminal deletes must never touch
-- history). sale_lines_projection rows are append-only under ownership
-- arbitration, so CASCADE there mirrors the classification precedent.
-- Every accepted sale (sale.finalized.v3) captures tags, so no capture
-- marker exists (ADR-0055).
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


-- ==== 00022_store_identity ====
-- Phase 9A store identity foundation. Retail owns its Store ID; Cloud
-- registers and projects it. No multi-store business semantics.
--
-- 1. stores: minimal registry (identity + mutable display metadata +
--    small lifecycle). No tenant/organization complexity.
-- 2. device_store_bindings: at most one authoritative Store per device
--    (PRIMARY KEY on device_id); a Store may have many devices.
--    Immutable after binding: no update path, conflicts stay conflicts.
-- 3. sync_events.store_id: server-derived ingress context from the
--    authenticated device binding; required: unbound devices cannot ingest.
--    Never trusted from payloads; never rewritten after receipt.

CREATE TABLE stores (
    id           UUID PRIMARY KEY,
    display_name TEXT NOT NULL CHECK (char_length(display_name) BETWEEN 1 AND 200),
    timezone     TEXT NOT NULL CHECK (char_length(timezone) BETWEEN 1 AND 64),
    status       TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE device_store_bindings (
    device_id  UUID PRIMARY KEY REFERENCES devices(id) ON DELETE RESTRICT,
    store_id   UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_device_store_bindings_store ON device_store_bindings (store_id, device_id);

ALTER TABLE sync_events ADD COLUMN store_id UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT;
CREATE INDEX idx_sync_events_store ON sync_events (store_id);

-- ==== 00023_store_scoped_projections ====
-- Phase 9B store-scoped projections. Copies the trusted Phase 9A ingress
-- Store context (sync_events.store_id) into projection roots so
-- cross-Store collisions are isolated instead of overwritten.
--
-- NULL = unscoped legacy (truthful: ownership not proven). Non-NULL =
-- proven Store ownership written only from the event's own ingress
-- context, never from current device bindings or payloads. No backfill:
-- every existing row keeps its values; only new scoped writes populate
-- these columns. SKU/slug uniqueness becomes Store-scoped (NULL legacy
-- rows never collide in PostgreSQL: NULL is never equal to NULL).

-- Catalog roots: nullable ownership until one-time legacy adoption.
ALTER TABLE catalog_categories ADD COLUMN store_id UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT;
CREATE INDEX idx_catalog_categories_store ON catalog_categories (store_id);

ALTER TABLE catalog_tags ADD COLUMN store_id UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT;
CREATE INDEX idx_catalog_tags_store ON catalog_tags (store_id);
-- Slug uniqueness is Store-local: same slug may coexist across Stores;
-- duplicates within one Store stay prohibited.
ALTER TABLE catalog_tags DROP CONSTRAINT catalog_tags_slug_key;
ALTER TABLE catalog_tags ADD CONSTRAINT catalog_tags_store_slug_unique UNIQUE (store_id, slug);

ALTER TABLE catalog_products ADD COLUMN store_id UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT;
CREATE INDEX idx_catalog_products_store ON catalog_products (store_id);
-- SKU uniqueness is Store-local (Retail SKU uniqueness is Store-local).
ALTER TABLE catalog_products DROP CONSTRAINT catalog_products_sku_key;
ALTER TABLE catalog_products ADD CONSTRAINT catalog_products_store_sku_unique UNIQUE (store_id, sku);

-- Policy/inventory carry denormalized ownership alongside their product
-- (written in the same projector decision, never looked up later).
ALTER TABLE catalog_product_sales_policies ADD COLUMN store_id UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT;
CREATE INDEX idx_catalog_product_sales_policies_store ON catalog_product_sales_policies (store_id);


-- Historical roots: immutable once written; NULL stays NULL for legacy.
ALTER TABLE sales_projection ADD COLUMN store_id UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT;
CREATE INDEX idx_sales_projection_store_occurred ON sales_projection (store_id, occurred_at);

ALTER TABLE return_refund_projection ADD COLUMN store_id UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT;
CREATE INDEX idx_return_refund_projection_store_occurred ON return_refund_projection (store_id, occurred_at);

-- ==== 00024_store_scoped_commerce ====
-- Phase 9C store-scoped commerce ownership. Closes the integration-state
-- boundary Phase 9B intentionally left unscoped: provider mappings and
-- online-order roots carry the proven Store of their authoritative
-- MoonLight product(s). NULL = unscoped legacy (ownership not proven),
-- never backfilled by migration. Adoption happens only through live
-- deterministic rules (mapping: same product whose Product adopted;
-- orders: unanimous mapped-line ownership at reconciliation).

-- Commerce product mappings: ownership mirrors the authoritative
-- catalog product. Same-pair identity stays (provider_key, product_id);
-- same external identity stays unique per provider.
ALTER TABLE commerce_product_mappings ADD COLUMN store_id UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT;
CREATE INDEX idx_commerce_product_mappings_store ON commerce_product_mappings (store_id);
CREATE INDEX idx_commerce_product_mappings_store_provider ON commerce_product_mappings (store_id, provider_key);

-- Online order roots: ownership derives at reconciliation from unanimous
-- resolved mapping ownership. Children (lines, addresses, history) stay
-- FK-owned by the root; webhook inbox, fences, and status history carry
-- no Store (operational tokens and append-only history, never authority).
-- Online orders arrive from Store-less providers; an order whose lines map
-- to no Product has no provable Store, so this column alone stays nullable
-- (unattributed, not legacy: ADR-0055 exception).
ALTER TABLE commerce_online_orders ADD COLUMN store_id UUID REFERENCES stores(id) ON DELETE RESTRICT;
CREATE INDEX idx_commerce_online_orders_store_created ON commerce_online_orders (store_id, created_at DESC);
CREATE INDEX idx_commerce_online_orders_store_provider_order ON commerce_online_orders (store_id, provider_key, external_order_id);

-- ==== 00025_store_scoped_default_catalog ====
-- Default reference catalog IDs are always stored under their per-Store
-- canonical identity (ADR-0055: every event is Store-scoped).

-- ==== 00026_notification_recipient_username_bound ====
-- Telegram permits 32 username characters plus the @ destination prefix.
-- Existing TEXT values and immutable delivery snapshots are never rewritten.
ALTER TABLE notification_messages DROP CONSTRAINT notification_messages_recipient_check;
ALTER TABLE notification_messages ADD CONSTRAINT notification_messages_recipient_check CHECK (char_length(recipient) BETWEEN 1 AND 33);

-- ==== 00027_commerce_mutation_barriers ====
-- Durable send-before-write evidence. No FK to rebuildable projections.
-- Successful acknowledged requests remove their active barrier; uncertainty
-- survives cancellation/restart and operator resolutions retain audit history.
CREATE TABLE commerce_product_mutation_barriers (
    operation_id UUID PRIMARY KEY,
    provider_key TEXT NOT NULL CHECK (provider_key ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    product_id UUID NOT NULL,
    request_fingerprint TEXT NOT NULL CHECK (request_fingerprint ~ '^[0-9a-f]{64}$'),
    state TEXT NOT NULL CHECK (state IN ('in_flight', 'uncertain', 'resolved')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    resolved_at TIMESTAMPTZ,
    resolution TEXT CHECK (resolution IN ('remote_completed', 'remote_not_applied')),
    CHECK ((state = 'resolved' AND resolved_at IS NOT NULL AND resolution IS NOT NULL)
        OR (state <> 'resolved' AND resolved_at IS NULL AND resolution IS NULL))
);
CREATE UNIQUE INDEX idx_commerce_mutation_barrier_active
    ON commerce_product_mutation_barriers (provider_key, product_id)
    WHERE state IN ('in_flight', 'uncertain');
CREATE INDEX idx_commerce_mutation_barrier_history
    ON commerce_product_mutation_barriers (provider_key, product_id, created_at DESC, operation_id DESC);

-- ==== 00028_category_online_policy ====
-- 00028: provider-neutral ONLINE channel policy for Category nodes
-- (Phase 13), the canonical effective-eligibility view, and the generic
-- durable commerce re-evaluation queue.
--
-- online_enabled mirrors the Retail-authored catalog.category.snapshot.v2
-- field. v1 events are interpreted as online_enabled = TRUE (pre-Phase13
-- semantics). Existing rows default TRUE: deployment never suppresses
-- the existing catalog. Store ownership semantics are unchanged.

ALTER TABLE catalog_categories ADD COLUMN online_enabled BOOLEAN NOT NULL DEFAULT TRUE;

-- +goose StatementBegin
-- Canonical effective-online eligibility for one Product: ONE
-- calculation shared by commerce publication and Catalog Health (never
-- duplicated in Woo/Shopify adapters). A Product is category-eligible
-- iff EVERY category node participating in its classification paths
-- (top category, each subcategory, and all DAG ancestors of both)
-- exists and has online_enabled = true. Conservative suppression: any
-- disabled relevant node suppresses the Product globally from ONLINE.
-- Missing/incomplete hierarchy fails safe (never eligible). Identity is
-- evaluated from IDs and edges only — never names, slugs, SKU or
-- provider state. block_reason is informational:
--   '' = categories allow online
--   'CATEGORY_ONLINE_DISABLED' = intentional policy suppression
--   'CATEGORY_STATE_INCOMPLETE' = required hierarchy state missing
-- blocking_category_id is the deterministic blocker (shallowest depth
-- first, then category_id ascending).
--
-- Identity is split in two (§94-§96 vs §100):
--   policy_fingerprint — the ELIGIBILITY identity: (id, depth,
--     online_enabled) of every relevant node. Pure policy state:
--     invariant to renames/translations (§100/§146), changes on any
--     policy/hierarchy/classification change (§97/§98), unaffected by
--     unrelated categories (§99).
--   policy_version — the revision digest of the same nodes. A POLICY
--     GENERATION marker: an enabled->disabled->enabled cycle advances it
--     (each transition bumps the Retail category revision), so provider
--     operation identity can never reuse a stale idempotency key after
--     an intervening transition (§96/§146). It also rotates on renames,
--     which is truthful because commerce payloads carry Category display
--     names (§100 escape clause: metadata upsert was already required).
CREATE VIEW catalog_product_online_state AS
WITH RECURSIVE classification (product_id, category_id, depth) AS (
    SELECT p.product_id, p.top_category_id, 0
    FROM catalog_products p
    UNION
    SELECT ps.product_id, ps.category_id, 0
    FROM catalog_product_subcategories ps
    UNION
    SELECT c.product_id, e.parent_id, c.depth + 1
    FROM classification c
    JOIN catalog_category_edges e ON e.child_id = c.category_id
), node AS (
    SELECT product_id, category_id, MIN(depth) AS depth
    FROM classification
    GROUP BY product_id, category_id
), eval AS (
    SELECT n.product_id, n.category_id, n.depth,
           c.category_id IS NULL AS missing,
           COALESCE(c.online_enabled, FALSE) AS online_enabled
    FROM node n
    LEFT JOIN catalog_categories c ON c.category_id = n.category_id
)
SELECT p.product_id,
       p.store_id,
       NOT EXISTS (SELECT 1 FROM eval e WHERE e.product_id = p.product_id AND e.missing)
       AND NOT EXISTS (SELECT 1 FROM eval e WHERE e.product_id = p.product_id AND NOT e.online_enabled)
       AS category_allows_online,
       CASE
         WHEN EXISTS (SELECT 1 FROM eval e WHERE e.product_id = p.product_id AND e.missing)
           THEN 'CATEGORY_STATE_INCOMPLETE'
         WHEN EXISTS (SELECT 1 FROM eval e WHERE e.product_id = p.product_id AND NOT e.online_enabled)
           THEN 'CATEGORY_ONLINE_DISABLED'
         ELSE ''
       END AS block_reason,
       (SELECT e.category_id FROM eval e
        WHERE e.product_id = p.product_id AND (e.missing OR NOT e.online_enabled)
        ORDER BY e.depth, e.category_id
        LIMIT 1) AS blocking_category_id,
       (SELECT string_agg(n.category_id::text || ':' || n.depth || ':' ||
                          CASE WHEN c.category_id IS NULL THEN 'x'
                               WHEN c.online_enabled THEN '1' ELSE '0' END, ','
                          ORDER BY n.category_id::text)
        FROM node n
        LEFT JOIN catalog_categories c ON c.category_id = n.category_id
        WHERE n.product_id = p.product_id) AS policy_fingerprint,
       (SELECT string_agg(n.category_id::text || ':' ||
                          CASE WHEN c.category_id IS NULL THEN 'x'
                               ELSE c.source_revision::text END, ','
                          ORDER BY n.category_id::text)
        FROM node n
        LEFT JOIN catalog_categories c ON c.category_id = n.category_id
        WHERE n.product_id = p.product_id) AS policy_version
FROM catalog_products p;
-- +goose StatementEnd

-- +goose StatementBegin
-- Generic durable commerce re-evaluation queue (Phase 13 §83). The
-- category/product projectors enqueue affected Products ATOMICALLY with
-- the projection commit; a worker later re-runs the generic
-- CommerceService.SyncProduct per registered provider. No provider I/O
-- ever happens inside a projection transaction.
--
-- provider-neutral, Product-keyed (PK coalesces duplicate requests —
-- shared-DAG paths collapse to one logical re-evaluation per product),
-- Store-safe (rows carry the product's proven Store), restart-safe
-- (durable rows), multi-instance safe (SKIP LOCKED claiming plus the
-- existing per-product commerce coordination lock).
CREATE TABLE commerce_product_reevaluations (
    product_id      UUID PRIMARY KEY,
    store_id        UUID NOT NULL REFERENCES stores (id),
    reason          TEXT NOT NULL,
    requested_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error_code TEXT
);

CREATE INDEX idx_commerce_reeval_due ON commerce_product_reevaluations (next_attempt_at, product_id);
-- +goose StatementEnd

-- ==== 00029_commerce_reevaluation_fencing ====
-- Request intent, retry schedule and lease ownership are independent.
-- Existing next_attempt_at values are retained: old due-time leases/backoff
-- become safely claimable at their existing horizon, with no work discarded.
ALTER TABLE commerce_product_reevaluations
 ADD COLUMN requested_generation BIGINT NOT NULL DEFAULT 1 CHECK (requested_generation > 0),
 ADD COLUMN claimed_generation BIGINT,
 ADD COLUMN lease_generation BIGINT NOT NULL DEFAULT 0 CHECK (lease_generation >= 0),
 ADD COLUMN lease_token UUID,
 ADD COLUMN lease_until TIMESTAMPTZ,
 ADD CONSTRAINT commerce_reeval_claim CHECK (
  (lease_token IS NULL AND lease_until IS NULL AND claimed_generation IS NULL) OR
  (lease_token IS NOT NULL AND lease_until IS NOT NULL AND claimed_generation IS NOT NULL AND claimed_generation > 0
   AND claimed_generation <= requested_generation AND lease_generation > 0)
 );

-- ==== 00030_product_configurations ====
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
    store_id UUID NOT NULL REFERENCES stores (id),
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

-- ==== 00031_commerce_async_receipts ====
-- Keep acknowledged asynchronous work alongside its original request evidence.
-- No FK to rebuildable catalog state and no changes to historical payloads.
ALTER TABLE commerce_product_mutation_barriers
    ADD COLUMN async_role TEXT CHECK (async_role IN ('bundle_create','bundle_update')),
    ADD COLUMN async_intent TEXT CHECK (async_intent ~ '^[0-9a-f]{64}$'),
    ADD COLUMN provider_operation_id TEXT CHECK (length(provider_operation_id) BETWEEN 1 AND 256),
    ADD COLUMN async_state TEXT CHECK (async_state IN ('pending','completed','failed')),
    ADD COLUMN async_product_id TEXT,
    ADD CONSTRAINT commerce_async_receipt_complete CHECK (
        (async_role IS NULL AND async_intent IS NULL AND provider_operation_id IS NULL AND async_state IS NULL AND async_product_id IS NULL)
        OR (async_role IS NOT NULL AND async_intent IS NOT NULL AND provider_operation_id IS NOT NULL AND async_state IS NOT NULL AND state = 'resolved')
    );
CREATE UNIQUE INDEX idx_commerce_async_pending
    ON commerce_product_mutation_barriers(provider_key, product_id)
    WHERE async_state = 'pending';
CREATE INDEX idx_commerce_async_history
    ON commerce_product_mutation_barriers(provider_key, product_id, async_role, created_at DESC, operation_id DESC)
    WHERE async_role IS NOT NULL;

-- ==== 00032_catalog_admin_commands ====
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
        'catalog.tag.details.update.v1'
    )),
    command_version INT NOT NULL CHECK (command_version = 1),
    entity_id TEXT NOT NULL CHECK (char_length(entity_id) BETWEEN 1 AND 64),
    payload JSONB NOT NULL,
    payload_hash TEXT NOT NULL CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
    expected_revision BIGINT NOT NULL CHECK (expected_revision >= 0),
    actor TEXT NOT NULL CHECK (char_length(actor) BETWEEN 1 AND 128),
    status TEXT NOT NULL CHECK (status IN ('pending','cancelled')) DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE catalog_admin_command_targets (
    id UUID PRIMARY KEY,
    command_id UUID NOT NULL REFERENCES catalog_admin_commands(id) ON DELETE RESTRICT,
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE RESTRICT,
    status TEXT NOT NULL CHECK (status IN ('pending','delivered','applied','conflict','rejected','blocked_capability','skipped_revoked','cancelled')),
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
-- Capability advertisement: one row per capability a device announces
-- (ADR-0055 step 2e). A missing row means incapable: Cloud never delivers
-- unknown commands there and the dashboard shows update-required.
CREATE TABLE device_capabilities (
    device_id  UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    capability TEXT NOT NULL CHECK (capability IN ('catalog_admin_commands_v1',
        'catalog_admin_scope_online_selling_v1', 'catalog_admin_scope_products_v1',
        'catalog_admin_scope_catalog_v1')),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, capability)
);
CREATE INDEX idx_catalog_admin_commands_store ON catalog_admin_commands(store_id, created_at DESC, id);
CREATE INDEX idx_catalog_admin_commands_entity ON catalog_admin_commands(store_id, entity_id, created_at DESC);
CREATE INDEX idx_catalog_admin_targets_device ON catalog_admin_command_targets(device_id, status, created_at, id) WHERE status IN ('pending','delivered');
CREATE INDEX idx_catalog_admin_targets_command ON catalog_admin_command_targets(command_id);

-- ==== 00033_product_variants ====
-- Phase 17 product & physical variant architecture: SKU/inventory
-- ownership moves from Product to ProductVariant (Retail authority).
-- Retail emits catalog.product_variant.snapshot.v1 and
-- inventory.product_variant.snapshot.v1; Cloud projects them with strict
-- Store isolation (store_id is required: ADR-0055).
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
-- independent from the Variant catalog revision stream.

CREATE TABLE catalog_product_variants (
    variant_id          UUID PRIMARY KEY,
    -- Logical reference to catalog_products(product_id, store_id):
    -- no FK on purpose (rebuildable projection, 00012 style).
    product_id          UUID NOT NULL,
    sku                 TEXT NOT NULL CHECK (btrim(sku) <> ''),
    is_active           BOOLEAN NOT NULL,
    -- Tombstone marker: deleted variants keep their rows forever.
    deleted             BOOLEAN NOT NULL DEFAULT FALSE,
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
    store_id            UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT
);
-- ADR-0055 / Retail ADR-049 1c: per-currency Variant overrides of the
-- Product price and cost. NULL inherits catalog_product_prices; the
-- variant projector replaces the set with each accepted variant revision.
CREATE TABLE catalog_product_variant_prices (
    variant_id  UUID NOT NULL REFERENCES catalog_product_variants(variant_id) ON DELETE CASCADE,
    currency    TEXT NOT NULL CHECK (currency IN ('EGP', 'USD')),
    price_minor BIGINT CHECK (price_minor IS NULL OR price_minor >= 0),
    cost_minor  BIGINT CHECK (cost_minor IS NULL OR cost_minor >= 0),
    PRIMARY KEY (variant_id, currency),
    CHECK (price_minor IS NOT NULL OR cost_minor IS NOT NULL)
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
CREATE INDEX idx_catalog_product_variants_store ON catalog_product_variants (store_id);

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
    store_id                UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT
);
CREATE INDEX idx_catalog_product_variant_inventory_product ON catalog_product_variant_inventory (product_id);
CREATE INDEX idx_catalog_product_variant_inventory_store ON catalog_product_variant_inventory (store_id);

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
    store_id            UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider_key, variant_id)
);
CREATE UNIQUE INDEX idx_commerce_variant_mappings_identity
    ON commerce_product_variant_mappings (provider_key, store_id, variant_id);
CREATE UNIQUE INDEX idx_commerce_variant_mappings_external
    ON commerce_product_variant_mappings (provider_key, store_id, external_product_id, external_variant_id);
CREATE INDEX idx_commerce_variant_mappings_store
    ON commerce_product_variant_mappings (store_id);
CREATE INDEX idx_commerce_variant_mappings_store_provider
    ON commerce_product_variant_mappings (store_id, provider_key);

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
    'catalog.product.variants.update.v1',
    'catalog.product.variant.update.v1',
    'catalog.variant.attributes.update.v1'
));

-- ==== 00034_commerce_order_variant_snap ====
-- Phase 17 order-line variant identity snapshot (additive): ONLINE order
-- lines gain the immutable resolved ProductVariant identity captured AT
-- INGESTION, mirroring the Phase 15 configuration selection snapshots
-- (00030). Current catalog rows are NEVER authority for past purchases:
-- the variant SKU and attribute labels/deltas are copied into the line,
-- so later rename/price/disable/deletion can never rewrite history.
--
--   variant_id                  — resolved commerce_product_variant_mappings
--                                 identity (nullable: lines without a
--                                 provider variation, or unresolved
--                                 provider variations, stay truthful NULL).
--   variant_sku                 — SKU snapshot at ingestion.
--   variant_attribute_snapshot — JSONB snapshot of the variant's option
--                                 attributes (definition/value codes and
--                                 bilingual labels) at ingestion. Display
--                                 data only; no provider identity, no money
--                                 math (BIGINT columns own money).
--
-- No foreign keys on purpose (00030 style): snapshots must survive
-- catalog rebuilds and variant tombstones.

ALTER TABLE commerce_online_order_lines ADD COLUMN variant_id UUID NULL;
ALTER TABLE commerce_online_order_lines ADD COLUMN variant_sku TEXT NULL;
ALTER TABLE commerce_online_order_lines ADD COLUMN variant_attribute_snapshot JSONB NULL;

CREATE INDEX idx_commerce_online_order_lines_variant
    ON commerce_online_order_lines (variant_id)
    WHERE variant_id IS NOT NULL;

-- ==== 00035_product_sku_retirement ====
-- Phase 17-R0 Product SKU retirement (ADR-0049): Product carries NO SKU
-- authority anywhere — not in the Cloud projection and not in the API.
-- ProductVariant owns SKU/stock (00033); product stock is derived only
-- (SUM of active variant stock at read time, never stored here).
--
-- catalog_products.sku was product-identity baggage: the column (and the
-- Store-scoped UNIQUE (store_id, sku) from 00023 plus idx_catalog_products_sku
-- from 00009) let a Product claim a SKU identity that collided with
-- variant SKU ownership. Dropping the column drops its constraints and
-- index together.
--
-- Historical sale-line snapshot SKUs (sale_lines_projection.sku and the
-- return/order equivalents) are immutable sale-time history and are
-- deliberately untouched: reporting over past purchases stays truthful.
ALTER TABLE catalog_products DROP COLUMN sku;

-- ==== 00036_sale_line_variant_snapshots ====
-- Phase 17-R0 immutable sale-line variant snapshots (sale.finalized.v3):
-- per-line ProductVariant identity captured AT SALE TIME, mirroring the
-- Phase 8C tag snapshots (00021) and the Phase 15/17 order-line snapshots
-- (00030/00034). Current catalog rows are NEVER authority for past
-- purchases: the variant SKU, its option attribute labels, and the
-- sale-time variant pricing override are copied into the line so later
-- rename/price/disable/deletion can never rewrite history. These columns
-- are sourced ONLY from the event snapshot — no projection or read path
-- may join catalog_product_variant_attribute_values (or any current-state
-- catalog table) to populate or refresh them.
--
--   variant_sku                — variant SKU snapshot at sale time
--                                (nullable: v1/v2 lines and lines without
--                                variant data stay truthful NULL).
--   variant_attributes         — bounded JSONB array of frozen option
--                                attributes: definition_code, value_code,
--                                name_ar, name_en, definition_name_ar,
--                                definition_name_en (labels frozen at sale
--                                time; display data only, never identity).
--   variant_price_egp_minor    — nullable sale-time variant pricing
--                                override (BIGINT minor units; no floats).
--   variant_price_usd_minor    — nullable sale-time variant pricing
--                                override (BIGINT minor units; no floats).
--
-- No foreign keys on purpose (00021/00030 style): snapshots must survive
-- catalog rebuilds, label renames, and variant tombstones.

ALTER TABLE sale_lines_projection ADD COLUMN variant_sku TEXT NULL;
ALTER TABLE sale_lines_projection ADD COLUMN variant_attributes JSONB NULL;
ALTER TABLE sale_lines_projection ADD COLUMN variant_price_egp_minor BIGINT NULL
    CHECK (variant_price_egp_minor IS NULL OR variant_price_egp_minor >= 0);
ALTER TABLE sale_lines_projection ADD COLUMN variant_price_usd_minor BIGINT NULL
    CHECK (variant_price_usd_minor IS NULL OR variant_price_usd_minor >= 0);
ALTER TABLE sale_lines_projection ADD CONSTRAINT sale_lines_variant_attributes_bounded
    CHECK (variant_attributes IS NULL OR (
        jsonb_typeof(variant_attributes) = 'array'
        AND jsonb_array_length(variant_attributes) <= 32));

CREATE INDEX idx_sale_lines_variant_sku
    ON sale_lines_projection (variant_sku)
    WHERE variant_sku IS NOT NULL;

-- ==== 00037_product_types ====
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
    store_id         UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT
);
ALTER TABLE catalog_product_types
    ADD CONSTRAINT catalog_product_types_store_code_unique
    UNIQUE (store_id, code);
CREATE INDEX idx_catalog_product_types_store ON catalog_product_types (store_id);

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

-- ==== 00038_sale_line_product_type_snapshot ====
-- 00038_sale_line_product_type_snapshot: Phase 17-R2 sale-time ProductType
-- snapshot (ADR-039 §26, ADR-0050). Mirrors 00036 (variant snapshots):
-- per-line ProductType identity captured AT SALE TIME from the
-- sale.finalized.v3 event. Current catalog_product_types rows are NEVER
-- authority for past purchases: later type renames or Product
-- reassignments can never rewrite history. Sourced ONLY from the event
-- snapshot — no projection or read path may join current-state catalog
-- tables to populate or refresh these columns.

ALTER TABLE sale_lines_projection
    ADD COLUMN product_type_id UUID,
    ADD COLUMN product_type_code TEXT CHECK (product_type_code IS NULL OR char_length(product_type_code) BETWEEN 1 AND 32),
    ADD COLUMN product_type_name_ar TEXT,
    ADD COLUMN product_type_name_en TEXT;

-- ==== 00039_admin_type_command_vocabulary ====
-- 00039_admin_type_command_vocabulary: Phase 17-R2 ProductType command
-- vocabulary (ADR-0050 §45/§46). Extends the 00032/00033 CHECK list with
-- the six type commands. Append-only vocabulary extension (same pattern
-- as 00033): recreate the CHECK, never edit applied history.

ALTER TABLE catalog_admin_commands DROP CONSTRAINT catalog_admin_commands_command_type_check;
ALTER TABLE catalog_admin_commands ADD CONSTRAINT catalog_admin_commands_command_type_check CHECK (command_type IN (
    'catalog.product.details.update.v1',
    'catalog.product.online-policy.update.v1',
    'catalog.product.classification.update.v1',
    'catalog.category.details.update.v1',
    'catalog.category.parents.update.v1',
    'catalog.category.online-policy.update.v1',
    'catalog.tag.details.update.v1',
    'catalog.product.variants.update.v1',
    'catalog.product.variant.update.v1',
    'catalog.variant.attributes.update.v1',
    'catalog.product-type.create.v1',
    'catalog.product-type.details.update.v1',
    'catalog.product-type.dimensions.update.v1',
    'catalog.product-type.capabilities.update.v1',
    'catalog.product-type.status.update.v1',
    'catalog.product.type.assign.v1'
));

-- ==== 00040_create_command_identity ====
-- 00040_create_command_identity: Phase 17-R3 create-command identity
-- closure (ADR-0050 §create-closure). A creation request must not pretend
-- a placeholder UUID is the business entity's identity: for create-kind
-- commands the target does not exist yet.
--
--   target_kind    'entity' (default, all Phase 16 + update commands) or
--                  'create' (creation intent; the entity is minted
--                  Retail-side at apply time).
--   requested_key  the stable requested key for creates (ProductType code);
--                  NULL for entity commands.
--   result_entity_id the actual Retail-minted entity ID, set when a target
--                  reports APPLIED (first writer wins); NULL until then.
--                  Intent rows are never rewritten: requested_* is immutable.
--   entity_id      keeps NOT NULL. For target_kind='entity' it is the
--                  business UUID (unchanged invariant). For 'create' it is
--                  the EXPLICIT ABSENT marker '' — never a fake business
--                  UUID. No reader may present '' as a ProductType ID.

ALTER TABLE catalog_admin_commands ADD COLUMN target_kind TEXT NOT NULL DEFAULT 'entity';
ALTER TABLE catalog_admin_commands ADD COLUMN requested_key TEXT NULL;
ALTER TABLE catalog_admin_commands ADD COLUMN result_entity_id TEXT NULL;

-- Coherence: entity commands carry a business UUID and no requested key;
-- creates carry the absent marker, a requested key, and (once applied) the
-- real result identity.
ALTER TABLE catalog_admin_commands DROP CONSTRAINT catalog_admin_commands_command_type_check;
ALTER TABLE catalog_admin_commands ADD CONSTRAINT catalog_admin_commands_command_type_check CHECK (command_type IN (
    'catalog.product.details.update.v1',
    'catalog.product.online-policy.update.v1',
    'catalog.product.classification.update.v1',
    'catalog.category.details.update.v1',
    'catalog.category.parents.update.v1',
    'catalog.category.online-policy.update.v1',
    'catalog.tag.details.update.v1',
    'catalog.product.variants.update.v1',
    'catalog.product.variant.update.v1',
    'catalog.variant.attributes.update.v1',
    'catalog.product-type.create.v1',
    'catalog.product-type.details.update.v1',
    'catalog.product-type.dimensions.update.v1',
    'catalog.product-type.capabilities.update.v1',
    'catalog.product-type.status.update.v1',
    'catalog.product.type.assign.v1'
));
-- Replace the inline entity_id 1..64 check (auto-named
-- catalog_admin_commands_entity_id_check) with the coherence rule below:
-- entity commands keep the 1..64 business-UUID invariant; creates carry
-- the explicit absent marker '' (never a fake business UUID).
ALTER TABLE catalog_admin_commands DROP CONSTRAINT catalog_admin_commands_entity_id_check;
ALTER TABLE catalog_admin_commands ADD CONSTRAINT catalog_admin_commands_create_identity_check CHECK (
    (target_kind = 'entity' AND char_length(entity_id) BETWEEN 1 AND 64 AND requested_key IS NULL)
    OR (target_kind = 'create' AND entity_id = '' AND requested_key IS NOT NULL
        AND char_length(requested_key) BETWEEN 1 AND 64
        AND (result_entity_id IS NULL OR char_length(result_entity_id) BETWEEN 1 AND 64))
);
ALTER TABLE catalog_admin_commands DROP CONSTRAINT IF EXISTS catalog_admin_commands_target_kind_check;
ALTER TABLE catalog_admin_commands ADD CONSTRAINT catalog_admin_commands_target_kind_check CHECK (
    target_kind IN ('entity', 'create')
);

-- ==== 00041_fleet_updates ====
-- Phase 18 secure Retail self-update control (ADR-0051, ADR-0052).
--
-- Releases are GLOBAL infrastructure (one signed artifact set serves every
-- Store); rollout targeting, device state and history are Store/device
-- scoped. Cloud stores signed release metadata and artifact LOCATIONS,
-- never artifact bytes, and never anything executable on Retail: the
-- signed envelope is relayed to Retail, which re-verifies it against its
-- own embedded keys. All scheduling timestamps are written from one
-- application clock passed by the service; nothing here compares now().

CREATE TABLE releases (
    id UUID PRIMARY KEY,
    manifest_digest TEXT NOT NULL UNIQUE CHECK (manifest_digest ~ '^[0-9a-f]{64}$'),
    release_sequence BIGINT NOT NULL UNIQUE CHECK (release_sequence > 0 AND release_sequence <= 9007199254740992),
    version TEXT NOT NULL CHECK (char_length(version) BETWEEN 1 AND 64),
    build_commit TEXT NOT NULL CHECK (build_commit ~ '^[0-9a-f]{40}$'),
    min_installed_sequence BIGINT NOT NULL CHECK (min_installed_sequence >= 0 AND min_installed_sequence < release_sequence),
    key_id TEXT NOT NULL CHECK (key_id ~ '^[0-9a-f]{16}$'),
    envelope TEXT NOT NULL CHECK (char_length(envelope) BETWEEN 1 AND 65536),
    status TEXT NOT NULL CHECK (status IN ('active','revoked')),
    imported_by TEXT NOT NULL CHECK (char_length(imported_by) BETWEEN 1 AND 128),
    imported_at TIMESTAMPTZ NOT NULL,
    status_changed_by TEXT NOT NULL CHECK (char_length(status_changed_by) BETWEEN 1 AND 128),
    status_changed_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE release_artifacts (
    release_id UUID NOT NULL REFERENCES releases(id) ON DELETE RESTRICT,
    os TEXT NOT NULL CHECK (os IN ('linux','windows','darwin')),
    arch TEXT NOT NULL CHECK (arch IN ('amd64','arm64')),
    package TEXT NOT NULL CHECK (package ~ '^[a-z0-9][a-z0-9.-]{0,31}$'),
    file_name TEXT NOT NULL CHECK (file_name ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$'),
    size BIGINT NOT NULL CHECK (size > 0 AND size <= 1073741824),
    sha256 TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    url TEXT NOT NULL CHECK (char_length(url) BETWEEN 8 AND 2048),
    PRIMARY KEY (release_id, os, arch, package)
);

-- Release identity is immutable after import; only lifecycle status may
-- change. Material changes require a new signed release (prompt §14).
-- +goose StatementBegin
CREATE FUNCTION releases_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'releases are never deleted; revoke instead' USING ERRCODE = 'restrict_violation';
    END IF;
    IF NEW.id IS DISTINCT FROM OLD.id OR NEW.manifest_digest IS DISTINCT FROM OLD.manifest_digest
       OR NEW.release_sequence IS DISTINCT FROM OLD.release_sequence OR NEW.version IS DISTINCT FROM OLD.version
       OR NEW.build_commit IS DISTINCT FROM OLD.build_commit OR NEW.min_installed_sequence IS DISTINCT FROM OLD.min_installed_sequence
       OR NEW.key_id IS DISTINCT FROM OLD.key_id OR NEW.envelope IS DISTINCT FROM OLD.envelope
       OR NEW.imported_by IS DISTINCT FROM OLD.imported_by OR NEW.imported_at IS DISTINCT FROM OLD.imported_at THEN
        RAISE EXCEPTION 'release identity is immutable' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER releases_immutable BEFORE UPDATE OR DELETE ON releases
    FOR EACH ROW EXECUTE FUNCTION releases_immutable();

-- +goose StatementBegin
CREATE FUNCTION release_artifacts_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'release artifacts are immutable' USING ERRCODE = 'check_violation';
END $$;
-- +goose StatementEnd
CREATE TRIGGER release_artifacts_immutable BEFORE UPDATE OR DELETE ON release_artifacts
    FOR EACH ROW EXECUTE FUNCTION release_artifacts_immutable();

CREATE TABLE update_rollouts (
    id UUID PRIMARY KEY,
    release_id UUID NOT NULL REFERENCES releases(id) ON DELETE RESTRICT,
    scope TEXT NOT NULL CHECK (scope IN ('device','store','all')),
    store_id UUID REFERENCES stores(id) ON DELETE RESTRICT,
    device_id UUID REFERENCES devices(id) ON DELETE RESTRICT,
    mode TEXT NOT NULL CHECK (mode IN ('optional','mandatory')),
    percentage INT NOT NULL CHECK (percentage BETWEEN 1 AND 100),
    status TEXT NOT NULL CHECK (status IN ('draft','active','paused','completed','cancelled')),
    not_before TIMESTAMPTZ,
    target_count INT NOT NULL CHECK (target_count >= 0),
    created_by TEXT NOT NULL CHECK (char_length(created_by) BETWEEN 1 AND 128),
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CHECK ((scope = 'device' AND device_id IS NOT NULL AND store_id IS NOT NULL)
        OR (scope = 'store' AND store_id IS NOT NULL AND device_id IS NULL)
        OR (scope = 'all' AND store_id IS NULL AND device_id IS NULL))
);
CREATE INDEX idx_update_rollouts_created ON update_rollouts (created_at DESC, id DESC);

-- Immutable per-rollout target snapshot (prompt §51, §94): devices are
-- captured at creation with their Store binding; later enrollment never
-- joins an existing rollout. bucket is the deterministic percentage slot.
CREATE TABLE update_rollout_targets (
    id UUID PRIMARY KEY,
    rollout_id UUID NOT NULL REFERENCES update_rollouts(id) ON DELETE RESTRICT,
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE RESTRICT,
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    bucket INT NOT NULL CHECK (bucket BETWEEN 0 AND 99),
    state TEXT NOT NULL CHECK (state IN ('not_selected','pending','delivered','downloading','verified',
        'waiting_safe_boundary','installing','awaiting_health','succeeded','already_compliant','failed',
        'rolled_back','manual_action_required','skipped_newer','unsupported','cancelled')),
    attempt_count INT NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_attempt_at TIMESTAMPTZ,
    last_error TEXT CHECK (last_error IS NULL OR last_error ~ '^[A-Z][A-Z0-9_]{0,63}$'),
    retryable BOOLEAN NOT NULL DEFAULT FALSE,
    delivered_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (rollout_id, device_id)
);
CREATE INDEX idx_update_targets_device_open ON update_rollout_targets (device_id, next_attempt_at)
    WHERE state IN ('pending','delivered');
CREATE INDEX idx_update_targets_rollout ON update_rollout_targets (rollout_id, store_id, device_id);

-- Trusted per-device version/updater report (authenticated device only).
CREATE TABLE device_update_status (
    device_id UUID PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    version TEXT NOT NULL CHECK (char_length(version) BETWEEN 1 AND 64),
    build_commit TEXT NOT NULL CHECK (char_length(build_commit) BETWEEN 1 AND 64),
    release_sequence BIGINT NOT NULL CHECK (release_sequence >= 0),
    os TEXT NOT NULL CHECK (char_length(os) BETWEEN 1 AND 16),
    arch TEXT NOT NULL CHECK (char_length(arch) BETWEEN 1 AND 16),
    updater_protocol INT NOT NULL CHECK (updater_protocol BETWEEN 0 AND 100),
    updater_capable BOOLEAN NOT NULL,
    unsupported_reason TEXT CHECK (unsupported_reason IS NULL OR unsupported_reason ~ '^[A-Z][A-Z0-9_]{0,63}$'),
    update_state TEXT NOT NULL CHECK (update_state ~ '^[a-z][a-z_]{0,63}$'),
    update_error TEXT CHECK (update_error IS NULL OR update_error ~ '^[A-Z][A-Z0-9_]{0,63}$'),
    reported_at TIMESTAMPTZ NOT NULL
);

-- Append-only attempt history (prompt §105): never overwritten.
CREATE TABLE update_target_events (
    id BIGSERIAL PRIMARY KEY,
    target_id UUID NOT NULL REFERENCES update_rollout_targets(id) ON DELETE RESTRICT,
    device_id UUID NOT NULL REFERENCES devices(id) ON DELETE RESTRICT,
    reported_state TEXT NOT NULL CHECK (reported_state ~ '^[a-z][a-z_]{0,63}$'),
    target_state TEXT NOT NULL,
    error_code TEXT CHECK (error_code IS NULL OR error_code ~ '^[A-Z][A-Z0-9_]{0,63}$'),
    retryable BOOLEAN NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX idx_update_target_events_target ON update_target_events (target_id, id DESC);

-- Operator/device audit (prompt §63). Identity is server-derived.
CREATE TABLE update_audit_events (
    id BIGSERIAL PRIMARY KEY,
    occurred_at TIMESTAMPTZ NOT NULL,
    actor_kind TEXT NOT NULL CHECK (actor_kind IN ('operator','device','system')),
    actor TEXT NOT NULL CHECK (char_length(actor) BETWEEN 1 AND 128),
    action TEXT NOT NULL CHECK (action ~ '^[a-z][a-z_.]{0,63}$'),
    release_id UUID REFERENCES releases(id) ON DELETE RESTRICT,
    rollout_id UUID REFERENCES update_rollouts(id) ON DELETE RESTRICT,
    target_id UUID REFERENCES update_rollout_targets(id) ON DELETE RESTRICT,
    device_id UUID REFERENCES devices(id) ON DELETE RESTRICT,
    store_id UUID REFERENCES stores(id) ON DELETE RESTRICT,
    details JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX idx_update_audit_events_time ON update_audit_events (occurred_at DESC, id DESC);

-- ==== 00042_admin_auth ====
-- Phase 18 R1 Cloud human authentication & authorization (ADR-0053).
--
-- Human identity is distinct from Store and from Retail device identity:
-- admin_users never share rows, credentials or sessions with devices.
-- Only explicitly provisioned humans exist (first OWNER via the server-side
-- bootstrap CLI; ADMINs created by an OWNER). There is no self-registration
-- and no default account.
--
-- Secrets are never stored in usable form: passwords are Argon2id PHC
-- strings, session/activation tokens and recovery codes are SHA-256
-- digests of high-entropy random values, and TOTP secrets are AES-256-GCM
-- ciphertext under a key that never enters the database
-- (AUTH_MFA_ENCRYPTION_KEY). Every timestamp is written from the single
-- application clock passed by the service; nothing here compares now().

CREATE TABLE admin_users (
    id UUID PRIMARY KEY,
    -- Normalized (trimmed, lower-case) login identifier.
    login TEXT NOT NULL UNIQUE
        CHECK (login = lower(login) AND char_length(login) BETWEEN 3 AND 254 AND login ~ '^[a-z0-9][a-z0-9._@+-]*$'),
    display_name TEXT NOT NULL CHECK (char_length(btrim(display_name)) BETWEEN 1 AND 100),
    role TEXT NOT NULL CHECK (role IN ('owner', 'admin')),
    status TEXT NOT NULL CHECK (status IN ('pending_setup', 'active', 'disabled')),
    -- Access to every Store (present and future) instead of explicit
    -- memberships. Restricted users list their Stores below.
    all_stores BOOLEAN NOT NULL DEFAULT false,
    password_hash TEXT CHECK (password_hash IS NULL OR password_hash LIKE '$argon2id$%'),
    password_changed_at TIMESTAMPTZ,
    -- Bumped on every password/MFA/role/status/membership change; a
    -- session minted under an older version is no longer accepted.
    security_version BIGINT NOT NULL DEFAULT 1 CHECK (security_version >= 1),
    created_by UUID REFERENCES admin_users(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CHECK (status = 'pending_setup' OR password_hash IS NOT NULL)
);

-- First-owner bootstrap happens exactly once in the life of a database:
-- the singleton primary key makes a concurrent second bootstrap fail.
CREATE TABLE admin_bootstrap_state (
    singleton BOOLEAN PRIMARY KEY DEFAULT true CHECK (singleton),
    owner_id UUID NOT NULL REFERENCES admin_users(id) ON DELETE RESTRICT,
    bootstrapped_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE admin_user_store_memberships (
    user_id UUID NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    store_id UUID NOT NULL REFERENCES stores(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, store_id)
);
CREATE INDEX idx_admin_memberships_store ON admin_user_store_memberships (store_id);

-- One TOTP credential per user. enabled_at NULL means enrollment started
-- but not yet verified. last_used_step rejects TOTP replay.
CREATE TABLE admin_mfa_credentials (
    user_id UUID PRIMARY KEY REFERENCES admin_users(id) ON DELETE CASCADE,
    secret_ciphertext BYTEA NOT NULL CHECK (octet_length(secret_ciphertext) BETWEEN 28 AND 256),
    key_version INTEGER NOT NULL CHECK (key_version >= 1),
    enabled_at TIMESTAMPTZ,
    last_used_step BIGINT NOT NULL DEFAULT 0 CHECK (last_used_step >= 0),
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE admin_recovery_codes (
    id BIGSERIAL PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    code_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(code_hash) = 32),
    created_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ
);
CREATE INDEX idx_admin_recovery_codes_user ON admin_recovery_codes (user_id) WHERE used_at IS NULL;

CREATE TABLE admin_activation_tokens (
    token_hash BYTEA PRIMARY KEY CHECK (octet_length(token_hash) = 32),
    user_id UUID NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    created_by UUID REFERENCES admin_users(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at TIMESTAMPTZ,
    CHECK (expires_at > created_at)
);
CREATE INDEX idx_admin_activation_user ON admin_activation_tokens (user_id);

-- Opaque server-side sessions: the browser holds a 256-bit random token;
-- only its digest is stored. stage gates what the session may do.
CREATE TABLE admin_sessions (
    id UUID PRIMARY KEY,
    token_hash BYTEA NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    csrf_hash BYTEA NOT NULL CHECK (octet_length(csrf_hash) = 32),
    user_id UUID NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
    stage TEXT NOT NULL CHECK (stage IN ('mfa_pending', 'mfa_setup', 'full')),
    security_version BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    last_seen_at TIMESTAMPTZ NOT NULL,
    absolute_expires_at TIMESTAMPTZ NOT NULL,
    auth_time TIMESTAMPTZ NOT NULL,
    mfa_verified_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    revoke_reason TEXT CHECK (revoke_reason IS NULL OR revoke_reason ~ '^[a-z_]{1,32}$'),
    CHECK (absolute_expires_at > created_at),
    CHECK (stage <> 'full' OR mfa_verified_at IS NOT NULL)
);
CREATE INDEX idx_admin_sessions_user ON admin_sessions (user_id) WHERE revoked_at IS NULL;

-- Bounded brute-force throttling for both the account and the network
-- identity. Locks are temporary (locked_until); nothing is permanent.
CREATE TABLE admin_login_throttle (
    key_kind TEXT NOT NULL CHECK (key_kind IN ('account', 'ip')),
    key TEXT NOT NULL CHECK (char_length(key) BETWEEN 1 AND 254),
    failures INTEGER NOT NULL CHECK (failures >= 0),
    window_started_at TIMESTAMPTZ NOT NULL,
    locked_until TIMESTAMPTZ,
    lock_count INTEGER NOT NULL DEFAULT 0 CHECK (lock_count >= 0),
    updated_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (key_kind, key)
);
CREATE INDEX idx_admin_login_throttle_updated ON admin_login_throttle (updated_at);

-- Append-only human-auth/security audit, separate from update audit.
-- Never stores passwords, TOTP secrets, recovery codes or tokens.
CREATE TABLE auth_audit_events (
    id BIGSERIAL PRIMARY KEY,
    occurred_at TIMESTAMPTZ NOT NULL,
    actor_user_id UUID REFERENCES admin_users(id) ON DELETE RESTRICT,
    target_user_id UUID REFERENCES admin_users(id) ON DELETE RESTRICT,
    action TEXT NOT NULL CHECK (action ~ '^[a-z][a-z_.]{0,63}$'),
    outcome TEXT NOT NULL CHECK (outcome IN ('success', 'failure', 'denied')),
    reason TEXT CHECK (reason IS NULL OR reason ~ '^[A-Z][A-Z0-9_]{0,63}$'),
    client_ip TEXT CHECK (client_ip IS NULL OR char_length(client_ip) <= 64),
    details JSONB NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX idx_auth_audit_time ON auth_audit_events (occurred_at DESC, id DESC);

-- +goose StatementBegin
CREATE FUNCTION auth_audit_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'auth audit events are append-only' USING ERRCODE = 'restrict_violation';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER auth_audit_append_only BEFORE UPDATE OR DELETE ON auth_audit_events
    FOR EACH ROW EXECUTE FUNCTION auth_audit_append_only();

-- Backstop for the service's transactional OWNER protection: once any
-- human exists, at least one ACTIVE OWNER must remain.
-- +goose StatementBegin
CREATE FUNCTION admin_users_keep_owner() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM admin_users)
       AND NOT EXISTS (SELECT 1 FROM admin_users WHERE role = 'owner' AND status = 'active') THEN
        RAISE EXCEPTION 'at least one active OWNER must remain' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER admin_users_keep_owner AFTER UPDATE OR DELETE ON admin_users
    FOR EACH STATEMENT EXECUTE FUNCTION admin_users_keep_owner();

-- ==== 00043_projection_discovery_index ====
-- Phase 19 performance: projection backlog discovery ordering.
--
-- Evidence (internal/perfbench cloud harness; docs/performance/
-- phase19-budgets.md): PendingCatalogEvents / PendingSaleEvents /
-- PendingReturnEvents all order by (received_at, event_id) on
-- sync_events. Only idx_sync_events_device_received (device_id,
-- received_at) existed, so the planner sorted the entire matching inbox
-- per scan — the dominant cost of backlog discovery at 10k+ queued
-- events and a contributor to the Phase 17 SERIALIZABLE tail (§28).
--
-- This index lets discovery walk the inbox in order and stop at the
-- batch limit. Additive only: migrations 00001..00042 are unchanged.
--
-- WRITE/STORAGE COST: one b-tree on the append-only inbox; writes are
-- inserts during ingest (already in a batch transaction), so the
-- maintenance cost is negligible against the scan it removes.
CREATE INDEX idx_sync_events_received ON sync_events(received_at, event_id);

-- ==== 00044_sync_recovery_state ====
-- Phase 20 R2: durable Cloud-restore recovery state.
--
-- WHY: a successful pg_restore can leave Cloud with a database that is
-- infrastructurally healthy (/health/ready 200) while its business
-- projections are historically incomplete - missing events Retail
-- already delivered and acknowledged. Nothing in the sync protocol
-- detected that condition, and the R1 ReconcileCloud primitive had no
-- trigger and no reachable call path.
--
-- This table makes the condition explicit and durable:
--   - the supported restore workflow marks every device's Store as
--     RECOVERY_RECONCILIATION_REQUIRED;
--   - Retail reads the flag over its normal control channel and refuses
--     to report full synchronization while it is set;
--   - reconciliation verifies continuity against retained Retail
--     history, resends what Cloud is missing, and clears the flag only
--     when verification reports zero missing events.
--
-- The state survives Cloud restarts (it is a table, not memory) and is
-- scoped per device so a Store with several devices cannot be marked
-- reconciled while one device is still unresolved.
CREATE TABLE sync_recovery_state (
    device_id UUID PRIMARY KEY REFERENCES devices(id) ON DELETE CASCADE,
    required BOOLEAN NOT NULL DEFAULT FALSE,
    reason TEXT NOT NULL DEFAULT '' CHECK (length(reason) <= 200),
    started_at TIMESTAMPTZ,
    cleared_at TIMESTAMPTZ,
    verified_events BIGINT NOT NULL DEFAULT 0 CHECK (verified_events >= 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Phase 20 R3: server-side verification coverage ledger.
    --
    -- A device must not be able to clear RECOVERY_RECONCILIATION_REQUIRED
    -- by verifying only a subset of its retained history. The server
    -- therefore tracks the verification pass itself:
    --   verify_generation   increments whenever a pass restarts, so an
    --                       interrupted reconciliation resumes cleanly
    --                       instead of corrupting prior progress;
    --   verify_last_event_id the highest keyset position covered so far.
    --                       Pages MUST advance past it, so page 1 then
    --                       page 3 (skipping page 2) is rejected;
    --   verify_pages/events  bounded counters for operator evidence;
    --   verify_complete     set only when a page is submitted as
    --                       end_of_history. Complete() refuses to clear
    --                       the flag unless this is TRUE - a client can
    --                       never simply claim missing=0 and finish.
    verify_generation    BIGINT NOT NULL DEFAULT 0 CHECK (verify_generation >= 0),
    verify_last_event_id TEXT NOT NULL DEFAULT '' CHECK (length(verify_last_event_id) <= 64),
    verify_pages         INT NOT NULL DEFAULT 0 CHECK (verify_pages >= 0),
    verify_events        BIGINT NOT NULL DEFAULT 0 CHECK (verify_events >= 0),
    verify_complete      BOOLEAN NOT NULL DEFAULT FALSE,
    -- Phase 20 R3: the device declares its retained-history size when a
    -- verification pass begins. Complete() requires verify_events to
    -- reach that total, so skipping pages cannot look exhaustive.
    verify_declared_total BIGINT NOT NULL DEFAULT 0 CHECK (verify_declared_total >= 0)
);

CREATE INDEX idx_sync_recovery_required ON sync_recovery_state(required) WHERE required;

-- ==== 00045_sync_recovery_missing_ledger ====
-- Phase 20 R6: server-owned missing-identity ledger.
--
-- WHY: Complete() previously trusted the client's last_missing claim, so a
-- device could finish a pass that the server itself had reported missing
-- identities for. verify_missing accumulates, per verification
-- generation, how many submitted identities Cloud did not hold (or held
-- with a different payload hash). Complete() refuses unless it is zero,
-- so the completeness proof no longer depends on a client assertion.
ALTER TABLE sync_recovery_state
    ADD COLUMN verify_missing BIGINT NOT NULL DEFAULT 0 CHECK (verify_missing >= 0);

-- ==== 00046_business_settings ====
-- Business-wide dashboard settings and Cloud-entered exchange rates. Both
-- apply to every Store and are Cloud-only durable state (back them up; no
-- projection rebuild touches them).

-- One row: which sales features the business uses. The Reports page shows
-- tax, discount and refund rows when the feature is on, or when the period
-- still holds such amounts. Every change bumps revision so concurrent
-- editors cannot overwrite each other silently.
CREATE TABLE business_settings (
    id               BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    tax_enabled      BOOLEAN NOT NULL DEFAULT FALSE,
    discount_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    returns_enabled  BOOLEAN NOT NULL DEFAULT FALSE,
    revision         BIGINT NOT NULL DEFAULT 1 CHECK (revision >= 1),
    updated_by       UUID REFERENCES admin_users(id) ON DELETE SET NULL,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO business_settings (id) VALUES (TRUE);

-- USD->EGP rates entered in Cloud. A report showing an EGP Sale in USD
-- uses the rate in effect when the Sale was made (else the earliest one).
-- USD Sales always keep the rate saved with them in Retail.
CREATE TABLE exchange_rates (
    id             UUID PRIMARY KEY,
    base           TEXT NOT NULL CHECK (base = 'USD'),
    quote          TEXT NOT NULL CHECK (quote = 'EGP'),
    rate_microrate BIGINT NOT NULL CHECK (rate_microrate > 0),
    effective_at   TIMESTAMPTZ NOT NULL,
    note           TEXT CHECK (note IS NULL OR char_length(note) BETWEEN 1 AND 120),
    created_by     UUID REFERENCES admin_users(id) ON DELETE SET NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (base, quote, effective_at)
);

-- ==== 00047_product_media ====
-- ADR-0056: Retail Product and Variant gallery images. The gallery state is
-- a rebuildable catalog projection (catalog.product.media.snapshot.v1); the
-- bytes are content-addressed uploads that arrive independently, so
-- catalog_product_media.sha256 is deliberately not a foreign key.
ALTER TABLE catalog_products ADD COLUMN media_revision BIGINT NOT NULL DEFAULT 0
    CHECK (media_revision >= 0);

CREATE TABLE catalog_product_media (
    image_id            UUID PRIMARY KEY,
    product_id          UUID NOT NULL REFERENCES catalog_products (product_id) ON DELETE CASCADE,
    -- NULL is the Product gallery. Logical reference to
    -- catalog_product_variants (no FK: rebuildable projection).
    variant_id          UUID,
    position            INT NOT NULL CHECK (position >= 0),
    is_primary          BOOLEAN NOT NULL,
    sha256              TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    content_type        TEXT NOT NULL CHECK (content_type IN ('image/jpeg', 'image/png')),
    byte_size           INT NOT NULL CHECK (byte_size BETWEEN 1 AND 5242880),
    width               INT NOT NULL CHECK (width > 0),
    height              INT NOT NULL CHECK (height > 0),
    source_revision     BIGINT NOT NULL CHECK (source_revision >= 1),
    source_event_id     UUID NOT NULL REFERENCES sync_events (event_id),
    source_device_id    UUID NOT NULL REFERENCES devices (id),
    source_received_at  TIMESTAMPTZ NOT NULL,
    projected_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX catalog_product_media_gallery_position
    ON catalog_product_media (product_id, COALESCE(variant_id, '00000000-0000-0000-0000-000000000000'::uuid), position);
CREATE UNIQUE INDEX catalog_product_media_one_primary
    ON catalog_product_media (product_id, COALESCE(variant_id, '00000000-0000-0000-0000-000000000000'::uuid))
    WHERE is_primary;
CREATE INDEX idx_catalog_product_media_sha256 ON catalog_product_media (sha256);

-- Uploaded image files, keyed by their SHA-256 (ADR-0057). The bytes live in
-- object storage under that hash; a row means the file is stored there.
-- Durable Cloud state (not a projection): a rebuild keeps it.
CREATE TABLE media_blobs (
    sha256             TEXT PRIMARY KEY CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    content_type       TEXT NOT NULL CHECK (content_type IN ('image/jpeg', 'image/png')),
    byte_size          INT NOT NULL CHECK (byte_size BETWEEN 1 AND 5242880),
    width              INT NOT NULL CHECK (width > 0),
    height             INT NOT NULL CHECK (height > 0),
    uploaded_by_device UUID NOT NULL REFERENCES devices (id),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ==== 00048_commerce_media_mappings ====
-- ADR-0056 step 2: provider media that MoonLight created, one row per
-- provider, Product and image file. Only media named here is ever reordered,
-- attached to variants or deleted on the provider; media added by hand in
-- the shop never appears in this table and is never touched.
CREATE TABLE commerce_media_mappings (
    provider_key        TEXT NOT NULL CHECK (provider_key ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    product_id          UUID NOT NULL,
    sha256              TEXT NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    store_id            UUID NOT NULL REFERENCES stores (id) ON DELETE RESTRICT,
    external_product_id TEXT NOT NULL CHECK (char_length(external_product_id) BETWEEN 1 AND 200),
    external_media_id   TEXT NOT NULL CHECK (char_length(external_media_id) BETWEEN 1 AND 200),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider_key, product_id, sha256)
);
CREATE UNIQUE INDEX idx_commerce_media_mappings_external
    ON commerce_media_mappings (provider_key, external_media_id);
CREATE INDEX idx_commerce_media_mappings_store ON commerce_media_mappings (store_id);

-- Product stock is a read-only aggregate. Independent Variant revisions are
-- never collapsed into an invented Product inventory revision. Incomplete or
-- contradictory inventory cannot expose partial positive availability.
CREATE VIEW catalog_product_stock AS
SELECT p.product_id, p.store_id,
    CASE WHEN count(v.variant_id) > 0
          AND count(i.variant_id) FILTER (WHERE i.ready) = count(v.variant_id)
         THEN sum(i.stock_quantity)::bigint
         ELSE NULL::bigint END AS stock_quantity
FROM catalog_products p
LEFT JOIN catalog_product_variants v
  ON v.product_id = p.product_id AND v.is_active AND NOT v.deleted
LEFT JOIN catalog_product_variant_inventory i
  ON i.variant_id = v.variant_id AND i.product_id = v.product_id
 AND i.sku = v.sku
 AND v.store_id = p.store_id
 AND i.store_id = p.store_id
GROUP BY p.product_id, p.store_id;

-- +goose Down
DROP TABLE IF EXISTS commerce_media_mappings CASCADE;
DROP TABLE IF EXISTS media_blobs CASCADE;
DROP TABLE IF EXISTS catalog_product_media CASCADE;
DROP TABLE IF EXISTS exchange_rates CASCADE;
DROP TABLE IF EXISTS business_settings CASCADE;
DROP VIEW IF EXISTS catalog_product_stock;
-- Dev/test only: removes every baseline object.
DROP VIEW IF EXISTS catalog_product_online_state;
DROP TABLE IF EXISTS admin_activation_tokens CASCADE;
DROP TABLE IF EXISTS admin_bootstrap_state CASCADE;
DROP TABLE IF EXISTS admin_login_throttle CASCADE;
DROP TABLE IF EXISTS admin_mfa_credentials CASCADE;
DROP TABLE IF EXISTS admin_recovery_codes CASCADE;
DROP TABLE IF EXISTS admin_sessions CASCADE;
DROP TABLE IF EXISTS admin_user_store_memberships CASCADE;
DROP TABLE IF EXISTS admin_users CASCADE;
DROP TABLE IF EXISTS auth_audit_events CASCADE;
DROP TABLE IF EXISTS business_report_deliveries CASCADE;
DROP TABLE IF EXISTS notification_recipients CASCADE;
DROP TABLE IF EXISTS business_report_runs CASCADE;
DROP TABLE IF EXISTS business_report_schedule_recipients CASCADE;
DROP TABLE IF EXISTS business_report_schedules CASCADE;
DROP TABLE IF EXISTS catalog_admin_command_targets CASCADE;
DROP TABLE IF EXISTS catalog_admin_commands CASCADE;
DROP TABLE IF EXISTS device_capabilities CASCADE;
DROP TABLE IF EXISTS catalog_categories CASCADE;
DROP TABLE IF EXISTS catalog_category_edges CASCADE;
DROP TABLE IF EXISTS catalog_product_configurations CASCADE;
DROP TABLE IF EXISTS catalog_product_prices CASCADE;
DROP TABLE IF EXISTS catalog_product_variant_prices CASCADE;
DROP TABLE IF EXISTS catalog_product_sales_policies CASCADE;
DROP TABLE IF EXISTS catalog_product_subcategories CASCADE;
DROP TABLE IF EXISTS catalog_product_tags CASCADE;
DROP TABLE IF EXISTS catalog_product_type_capabilities CASCADE;
DROP TABLE IF EXISTS catalog_product_type_variant_dimensions CASCADE;
DROP TABLE IF EXISTS catalog_product_types CASCADE;
DROP TABLE IF EXISTS catalog_product_variant_attribute_values CASCADE;
DROP TABLE IF EXISTS catalog_product_variant_inventory CASCADE;
DROP TABLE IF EXISTS catalog_product_variants CASCADE;
DROP TABLE IF EXISTS catalog_products CASCADE;
DROP TABLE IF EXISTS catalog_tags CASCADE;
DROP TABLE IF EXISTS commerce_online_order_addresses CASCADE;
DROP TABLE IF EXISTS commerce_online_order_lines CASCADE;
DROP TABLE IF EXISTS commerce_online_order_reconcile_fences CASCADE;
DROP TABLE IF EXISTS commerce_online_order_status_history CASCADE;
DROP TABLE IF EXISTS commerce_online_order_webhook_events CASCADE;
DROP TABLE IF EXISTS commerce_online_orders CASCADE;
DROP TABLE IF EXISTS commerce_product_configuration_mappings CASCADE;
DROP TABLE IF EXISTS commerce_product_mappings CASCADE;
DROP TABLE IF EXISTS commerce_product_mutation_barriers CASCADE;
DROP TABLE IF EXISTS commerce_product_reevaluations CASCADE;
DROP TABLE IF EXISTS commerce_product_variant_mappings CASCADE;
DROP TABLE IF EXISTS device_control_commands CASCADE;
DROP TABLE IF EXISTS device_control_presence CASCADE;
DROP TABLE IF EXISTS device_credentials CASCADE;
DROP TABLE IF EXISTS device_store_bindings CASCADE;
DROP TABLE IF EXISTS device_update_status CASCADE;
DROP TABLE IF EXISTS devices CASCADE;
DROP TABLE IF EXISTS notification_delivery_status_history CASCADE;
DROP TABLE IF EXISTS notification_messages CASCADE;
DROP TABLE IF EXISTS notification_template_mappings CASCADE;
DROP TABLE IF EXISTS operational_alert_deliveries CASCADE;
DROP TABLE IF EXISTS operational_incidents CASCADE;
DROP TABLE IF EXISTS operational_recovery_actions CASCADE;
DROP TABLE IF EXISTS release_artifacts CASCADE;
DROP TABLE IF EXISTS releases CASCADE;
DROP TABLE IF EXISTS return_refund_lines_projection CASCADE;
DROP TABLE IF EXISTS return_refund_ownership CASCADE;
DROP TABLE IF EXISTS return_refund_payments_projection CASCADE;
DROP TABLE IF EXISTS return_refund_projection CASCADE;
DROP TABLE IF EXISTS sale_event_ownership CASCADE;
DROP TABLE IF EXISTS sale_item_tag_snapshots CASCADE;
DROP TABLE IF EXISTS sale_line_classifications_projection CASCADE;
DROP TABLE IF EXISTS sale_lines_projection CASCADE;
DROP TABLE IF EXISTS sale_payments_projection CASCADE;
DROP TABLE IF EXISTS sales_projection CASCADE;
DROP TABLE IF EXISTS stores CASCADE;
DROP TABLE IF EXISTS sync_event_processing CASCADE;
DROP TABLE IF EXISTS sync_events CASCADE;
DROP TABLE IF EXISTS sync_recovery_state CASCADE;
DROP TABLE IF EXISTS update_audit_events CASCADE;
DROP TABLE IF EXISTS update_rollout_targets CASCADE;
DROP TABLE IF EXISTS update_rollouts CASCADE;
DROP TABLE IF EXISTS update_target_events CASCADE;
DROP FUNCTION IF EXISTS admin_users_keep_owner();
DROP FUNCTION IF EXISTS auth_audit_append_only();
DROP FUNCTION IF EXISTS release_artifacts_immutable();
DROP FUNCTION IF EXISTS releases_immutable();
