-- Phase 5A catalog projection queries (ADR-0028). Current-state replace
-- semantics: one entity row per business ID carrying its highest valid
-- revision; relations replaced transactionally per revision.

-- name: PendingCatalogEvents :many
-- Lazy backfill discovery shared by the three catalog processors: accepted
-- events with no processing row (missing), a pending row, or a due retry.
-- Blocked and processed rows never return; ordering is deterministic.
SELECT e.event_id
FROM sync_events e
LEFT JOIN sync_event_processing p
  ON p.event_id = e.event_id AND p.processor = $1
WHERE e.event_type = $2
  AND (p.event_id IS NULL
       OR p.status = 'pending'
       OR (p.status = 'retry' AND (p.next_attempt_at IS NULL OR p.next_attempt_at <= now())))
ORDER BY e.received_at, e.event_id
LIMIT $3;

-- name: UpsertCatalogCategory :exec
INSERT INTO catalog_categories (
    category_id, status, name_ar, name_en,
    source_revision, source_event_id, source_device_id,
    source_payload_hash, source_received_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (category_id) DO UPDATE SET
    status = excluded.status, name_ar = excluded.name_ar, name_en = excluded.name_en,
    source_revision = excluded.source_revision, source_event_id = excluded.source_event_id,
    source_device_id = excluded.source_device_id, source_payload_hash = excluded.source_payload_hash,
    source_received_at = excluded.source_received_at, projected_at = now();

-- name: CatalogCategoryByID :one
SELECT category_id, status, name_ar, name_en, source_revision,
    source_event_id, source_device_id, source_payload_hash
FROM catalog_categories WHERE category_id = $1;

-- name: DeleteCatalogCategoryEdges :exec
DELETE FROM catalog_category_edges WHERE child_id = $1;

-- name: InsertCatalogCategoryEdge :exec
INSERT INTO catalog_category_edges (parent_id, child_id, position)
VALUES ($1, $2, $3)
ON CONFLICT (parent_id, child_id) DO UPDATE SET position = excluded.position;

-- name: CatalogCategoryParents :many
SELECT parent_id FROM catalog_category_edges WHERE child_id = $1 ORDER BY position, parent_id;

-- name: AllCatalogCategoryEdges :many
SELECT parent_id, child_id FROM catalog_category_edges ORDER BY parent_id, child_id;

-- name: UpsertCatalogTag :exec
INSERT INTO catalog_tags (
    tag_id, slug, is_active, name_ar, name_en,
    source_revision, source_event_id, source_device_id,
    source_payload_hash, source_received_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (tag_id) DO UPDATE SET
    slug = excluded.slug, is_active = excluded.is_active,
    name_ar = excluded.name_ar, name_en = excluded.name_en,
    source_revision = excluded.source_revision, source_event_id = excluded.source_event_id,
    source_device_id = excluded.source_device_id, source_payload_hash = excluded.source_payload_hash,
    source_received_at = excluded.source_received_at, projected_at = now();

-- name: CatalogTagByID :one
SELECT tag_id, slug, is_active, name_ar, name_en, source_revision,
    source_event_id, source_device_id, source_payload_hash
FROM catalog_tags WHERE tag_id = $1;

-- name: UpsertCatalogProduct :exec
INSERT INTO catalog_products (
    product_id, sku, name, description, top_category_id, width_cm, height_cm,
    is_active, source_revision, source_event_id, source_device_id,
    source_payload_hash, source_received_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (product_id) DO UPDATE SET
    sku = excluded.sku, name = excluded.name, description = excluded.description,
    top_category_id = excluded.top_category_id, width_cm = excluded.width_cm,
    height_cm = excluded.height_cm, is_active = excluded.is_active,
    source_revision = excluded.source_revision, source_event_id = excluded.source_event_id,
    source_device_id = excluded.source_device_id, source_payload_hash = excluded.source_payload_hash,
    source_received_at = excluded.source_received_at, projected_at = now();

-- name: CatalogProductByID :one
SELECT product_id, sku, name, description, top_category_id, width_cm, height_cm,
    is_active, source_revision, source_event_id, source_device_id, source_payload_hash
FROM catalog_products WHERE product_id = $1;

-- name: CatalogProductBySKU :one
SELECT product_id, sku, name, description, top_category_id, width_cm, height_cm,
    is_active, source_revision, source_event_id, source_device_id, source_payload_hash
FROM catalog_products WHERE sku = $1;

-- name: DeleteCatalogProductPrices :exec
DELETE FROM catalog_product_prices WHERE product_id = $1;

-- name: DeleteCatalogProductTranslations :exec
DELETE FROM catalog_product_translations WHERE product_id = $1;

-- name: DeleteCatalogProductSubcategories :exec
DELETE FROM catalog_product_subcategories WHERE product_id = $1;

-- name: DeleteCatalogProductTags :exec
DELETE FROM catalog_product_tags WHERE product_id = $1;

-- name: InsertCatalogProductPrice :exec
INSERT INTO catalog_product_prices (product_id, currency, price_minor, cost_minor)
VALUES ($1, $2, $3, $4)
ON CONFLICT (product_id, currency) DO UPDATE SET
    price_minor = excluded.price_minor, cost_minor = excluded.cost_minor;

-- name: InsertCatalogProductTranslation :exec
INSERT INTO catalog_product_translations (product_id, locale, name, description)
VALUES ($1, $2, $3, $4)
ON CONFLICT (product_id, locale) DO UPDATE SET
    name = excluded.name, description = excluded.description;

-- name: InsertCatalogProductSubcategory :exec
INSERT INTO catalog_product_subcategories (product_id, category_id, position)
VALUES ($1, $2, $3)
ON CONFLICT (product_id, category_id) DO UPDATE SET position = excluded.position;

-- name: InsertCatalogProductTag :exec
INSERT INTO catalog_product_tags (product_id, tag_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: CatalogProductPrices :many
SELECT currency, price_minor, cost_minor
FROM catalog_product_prices WHERE product_id = $1 ORDER BY currency;

-- name: CatalogProductTranslations :many
SELECT locale, name, description
FROM catalog_product_translations WHERE product_id = $1 ORDER BY locale;

-- name: CatalogProductSubcategories :many
SELECT category_id FROM catalog_product_subcategories
WHERE product_id = $1 ORDER BY position, category_id;

-- name: CatalogProductTags :many
SELECT tag_id FROM catalog_product_tags WHERE product_id = $1 ORDER BY tag_id;

-- name: CatalogActiveProducts :many
SELECT product_id, sku, name, source_revision, source_event_id
FROM catalog_products
WHERE is_active ORDER BY sku, product_id
LIMIT $1;

-- Phase 5A-R1 cross-aggregate convergence (ADR-0028): accepted catalog
-- events per entity with their processing state, projected products
-- referencing a category, and graph-change product resets. All bounded by
-- entity ID (never full-table scans beyond the small catalog event set).

-- name: CatalogEntityEventRevisions :many
-- Accepted events for one catalog entity (type + JSON identity key +
-- identity value) with revision and processing state. Missing processing
-- rows report 'missing'; callers treat missing/pending/retry as unsettled.
SELECT e.event_id,
    (e.payload->>'catalog_revision')::bigint AS revision,
    COALESCE(p.status, 'missing') AS processing_status,
    COALESCE(p.last_error_code, '') AS last_error_code
FROM sync_events e
LEFT JOIN sync_event_processing p
  ON p.event_id = e.event_id AND p.processor = $4
WHERE e.event_type = $1
  AND e.payload->>($2::text) = ($3::text);

-- name: CatalogProductsReferencingCategory :many
-- Projected products whose top or subcategory set references a category.
-- Indexed both sides (top FK index is implicit via PK lookups; the
-- subcategory side uses idx_catalog_product_subcategories_category).
SELECT product_id FROM catalog_products WHERE top_category_id = $1
UNION
SELECT product_id FROM catalog_product_subcategories WHERE category_id = $1;

-- name: ResetCatalogProductRetriesForGraph :exec
-- Re-evaluation trigger: after a category graph commit, waiting or
-- graph-blocked product events referencing the changed category become
-- pending again so they re-validate against the new graph with no operator
-- retry and no Retail resend. Other error codes are never touched.
UPDATE sync_event_processing SET status = 'pending', next_attempt_at = NULL,
    processed_at = NULL, last_error_code = NULL, last_error_message = NULL
WHERE processor = 'catalog_product_projection.v1'
  AND status IN ('retry', 'blocked')
  AND last_error_code IN ('CATALOG_DEPENDENCY_WAIT', 'CATALOG_INVALID_RELATION')
  AND event_id IN (
    SELECT e.event_id FROM sync_events e
    WHERE e.event_type = 'catalog.product.snapshot.v1'
      AND (e.payload->>'top_category_id' = ($1::text)
           OR e.payload->'subcategory_ids' @> to_jsonb(($2::text)))
  );

-- name: LockCatalogEntity :exec
-- PostgreSQL-enforced entity serialization (R04): one advisory
-- transaction-scoped lock per (entity_type, entity_id), always acquired in
-- globally sorted key order (see lockCatalogEntities). Concurrent
-- revisions of the same entity serialize; different entities proceed in
-- parallel. Released automatically at commit/rollback.
SELECT pg_advisory_xact_lock(hashtext('catalog:' || $1::text || ':' || $2::text));
