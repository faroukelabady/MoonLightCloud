-- +goose Up
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
    store_id        UUID REFERENCES stores (id),
    reason          TEXT NOT NULL,
    requested_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error_code TEXT
);

CREATE INDEX idx_commerce_reeval_due ON commerce_product_reevaluations (next_attempt_at, product_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Rollback policy: the Phase 11-13 commerce durability stack (mutation
-- barriers + durable re-evaluation) rolls back as ONE unit. Refusing
-- here keeps a failed rollback from partially unwinding the stack and
-- leaving barrier semantics unrecoverable (frozen invariant: a refused
-- rollback must leave the schema untouched).
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM commerce_product_mutation_barriers) THEN
        RAISE EXCEPTION 'refusing rollback: commerce_product_mutation_barriers holds mutation evidence';
    END IF;
    IF EXISTS (SELECT 1 FROM commerce_product_reevaluations) THEN
        RAISE EXCEPTION 'refusing rollback: commerce_product_reevaluations holds durable re-evaluation work';
    END IF;
    IF EXISTS (SELECT 1 FROM catalog_categories WHERE online_enabled = FALSE) THEN
        RAISE EXCEPTION 'refusing rollback: catalog_categories holds online policy configuration';
    END IF;
END
$$;
-- +goose StatementEnd
DROP TABLE IF EXISTS commerce_product_reevaluations;
DROP VIEW IF EXISTS catalog_product_online_state;
ALTER TABLE catalog_categories DROP COLUMN online_enabled;
