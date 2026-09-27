-- +goose Up
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
        ('PENDING', 'PROCESSING', 'ON_HOLD', 'COMPLETED', 'CANCELLED', 'REFUNDED', 'FAILED', 'UNKNOWN', 'DELETED')),
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
        ('PENDING', 'PROCESSING', 'ON_HOLD', 'COMPLETED', 'CANCELLED', 'REFUNDED', 'FAILED', 'UNKNOWN', 'DELETED')),
    provider_modified_at TIMESTAMPTZ,
    observed_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider_key, external_order_id, order_revision)
);

-- +goose Down
-- Development/test repair only: orders, status history, and webhook
-- inbox are durable integration state and are NOT rebuildable from
-- Retail events. Never roll back in production.
DROP TABLE IF EXISTS commerce_online_order_status_history;
DROP TABLE IF EXISTS commerce_online_order_addresses;
DROP TABLE IF EXISTS commerce_online_order_lines;
DROP TABLE IF EXISTS commerce_online_orders;
DROP TABLE IF EXISTS commerce_online_order_webhook_events;
