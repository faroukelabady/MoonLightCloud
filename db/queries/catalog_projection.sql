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
-- Phase 9B: store_id carries the event's own ingress Store context
-- (NULL for legacy events). COALESCE preserves proven ownership: a
-- legacy event never wipes an adopted Store, and a scoped event adopts
-- a NULL row or reaffirms its own Store (cross-Store writes are fenced
-- in Go before this upsert runs).
-- Phase 9-R2: default_algorithm records which identity scheme wrote the
-- row (0 = raw seeded ID, 1 = Store-scoped canonical ID) so an existing
-- pre-R2 annotation can be detected and recovered.
INSERT INTO catalog_categories (
    category_id, status, name_ar, name_en,
    source_revision, source_event_id, source_device_id,
    source_payload_hash, source_received_at, store_id, default_algorithm
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (category_id) DO UPDATE SET
    status = excluded.status, name_ar = excluded.name_ar, name_en = excluded.name_en,
    source_revision = excluded.source_revision, source_event_id = excluded.source_event_id,
    source_device_id = excluded.source_device_id, source_payload_hash = excluded.source_payload_hash,
    source_received_at = excluded.source_received_at,
    store_id = COALESCE(excluded.store_id, catalog_categories.store_id),
    default_algorithm = excluded.default_algorithm,
    projected_at = now();

-- name: CatalogCategoryByID :one
SELECT category_id, status, name_ar, name_en, source_revision,
    source_event_id, source_device_id, source_payload_hash, store_id
FROM catalog_categories WHERE category_id = $1;

-- name: DeleteCatalogCategoryEdges :exec
DELETE FROM catalog_category_edges WHERE child_id = $1;

-- name: InsertCatalogCategoryEdge :exec
INSERT INTO catalog_category_edges (parent_id, child_id, position)
VALUES ($1, $2, $3)
ON CONFLICT (parent_id, child_id) DO UPDATE SET position = excluded.position;

-- name: CatalogCategoryParents :many
SELECT parent_id FROM catalog_category_edges WHERE child_id = $1 ORDER BY position, parent_id;

-- name: CatalogDefaultCategoryAlgorithm :one
-- Phase 9-R2 recovery: the canonical-identity algorithm version of a
-- shared default category's existing row for one Store (0 = none).
SELECT COALESCE(default_algorithm, 0) FROM catalog_categories
WHERE category_id = $1 AND store_id = $2;

-- name: AllCatalogCategoryEdges :many
SELECT parent_id, child_id FROM catalog_category_edges ORDER BY parent_id, child_id;

-- name: UpsertCatalogTag :exec
-- Phase 9B store ownership: see UpsertCatalogCategory.
INSERT INTO catalog_tags (
    tag_id, slug, is_active, name_ar, name_en,
    source_revision, source_event_id, source_device_id,
    source_payload_hash, source_received_at, store_id, default_algorithm
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (tag_id) DO UPDATE SET
    slug = excluded.slug, is_active = excluded.is_active,
    name_ar = excluded.name_ar, name_en = excluded.name_en,
    source_revision = excluded.source_revision, source_event_id = excluded.source_event_id,
    source_device_id = excluded.source_device_id, source_payload_hash = excluded.source_payload_hash,
    source_received_at = excluded.source_received_at,
    store_id = COALESCE(excluded.store_id, catalog_tags.store_id),
    default_algorithm = excluded.default_algorithm,
    projected_at = now();

-- name: CatalogDefaultTagAlgorithm :one
-- Phase 9-R2 recovery: see CatalogDefaultCategoryAlgorithm.
SELECT COALESCE(default_algorithm, 0) FROM catalog_tags
WHERE tag_id = $1 AND store_id = $2;

-- name: RecoverDefaultCatalogBlocked :execrows
-- Phase 9-R2 F08 upgrade recovery. An event that was permanently blocked
-- by a pre-R2 catalog identity/scope defect becomes pending again once,
-- so it re-projects under the current identity model. Bounded to the
-- durable catalog projectors and the exact terminal codes the R1/R2
-- identity changes made obsolete; it never resets validation, cycle,
-- depth, graph-conflict, inventory/policy, sale, or return failures.
UPDATE sync_event_processing SET status = 'pending', next_attempt_at = NULL,
    processed_at = NULL, last_error_code = NULL, last_error_message = NULL
WHERE status = 'blocked'
  AND processor IN ('catalog_category_projection.v1', 'catalog_tag_projection.v1',
                    'catalog_product_projection.v1')
  AND last_error_code IN ('STORE_SCOPE_CONFLICT', 'CATALOG_REVISION_CONFLICT');

-- name: CatalogTagByID :one
SELECT tag_id, slug, is_active, name_ar, name_en, source_revision,
    source_event_id, source_device_id, source_payload_hash, store_id
FROM catalog_tags WHERE tag_id = $1;

-- name: UpsertCatalogProduct :exec
-- Phase 9B store ownership: see UpsertCatalogCategory.
INSERT INTO catalog_products (
    product_id, sku, name, description, top_category_id, width_cm, height_cm,
    is_active, source_revision, source_event_id, source_device_id,
    source_payload_hash, source_received_at, store_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT (product_id) DO UPDATE SET
    sku = excluded.sku, name = excluded.name, description = excluded.description,
    top_category_id = excluded.top_category_id, width_cm = excluded.width_cm,
    height_cm = excluded.height_cm, is_active = excluded.is_active,
    source_revision = excluded.source_revision, source_event_id = excluded.source_event_id,
    source_device_id = excluded.source_device_id, source_payload_hash = excluded.source_payload_hash,
    source_received_at = excluded.source_received_at,
    store_id = COALESCE(excluded.store_id, catalog_products.store_id),
    projected_at = now();

-- name: CatalogProductByID :one
SELECT product_id, sku, name, description, top_category_id, width_cm, height_cm,
    is_active, source_revision, source_event_id, source_device_id, source_payload_hash, store_id
FROM catalog_products WHERE product_id = $1;

-- name: CatalogProductBySKU :one
-- Phase 9B: SKU resolves only within one Store scope (NULL scope matches
-- legacy rows). A bare global SKU lookup could cross Store ownership.
SELECT product_id, sku, name, description, top_category_id, width_cm, height_cm,
    is_active, source_revision, source_event_id, source_device_id, source_payload_hash, store_id
FROM catalog_products WHERE sku = $1 AND store_id IS NOT DISTINCT FROM $2;

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
-- The revision key is a parameter because the policy stream versions on
-- sales_policy_revision while category/tag/product version on
-- catalog_revision.
SELECT e.event_id,
    (e.payload->>($5::text))::bigint AS revision,
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

-- Phase 5A-R2 durable re-evaluation fallback (M01): graph-dependent
-- blocked Product events become eligible again when the relevant graph
-- has advanced since the block decision. The predicate is strictly
-- monotonic (involved category projected_at strictly newer than the block
-- updated_at), so a re-blocked event can never hot-loop: each re-arm
-- requires strictly newer graph advancement. Superseded events (a newer
-- accepted revision exists) are never re-armed. Terminally invalid events
-- under a static graph stay blocked forever. This is the correctness path;
-- the post-commit reset is only a fast wake-up.

-- name: RearmBlockedCatalogProducts :exec
UPDATE sync_event_processing AS p SET status = 'pending', next_attempt_at = NULL,
    processed_at = NULL, last_error_code = NULL, last_error_message = NULL
FROM sync_events e
WHERE p.event_id = e.event_id
  AND p.processor = 'catalog_product_projection.v1'
  AND p.status = 'blocked'
  AND p.last_error_code = 'CATALOG_INVALID_RELATION'
  AND e.event_type = 'catalog.product.snapshot.v1'
  AND NOT EXISTS (
    SELECT 1 FROM sync_events e2
    WHERE e2.event_type = 'catalog.product.snapshot.v1'
      AND e2.payload->>'product_id' = e.payload->>'product_id'
      AND (e2.payload->>'catalog_revision')::bigint > (e.payload->>'catalog_revision')::bigint
  )
  AND EXISTS (
    SELECT 1 FROM catalog_categories c
    WHERE (c.category_id::text = e.payload->>'top_category_id'
           OR c.category_id::text IN (SELECT jsonb_array_elements_text(e.payload->'subcategory_ids')))
      AND c.projected_at > p.updated_at
  );

-- Phase 9-R1 F02: complete ownership-relationship validation before a
-- current-state aggregate may adopt a Store. Each query returns the
-- proven stores of the durable dependents/edges that must agree with the
-- adopting Store; NULL (legacy) dependents are wildcards and omitted.

-- name: CatalogProductDependentStores :many
-- Proven Store ownership of a product's inventory, sales policy, and
-- provider mapping. Adopting a product into a Store that already has a
-- dependent owned by another proven Store is rejected instead of
-- committing a contradictory durable relationship.
SELECT i.store_id FROM catalog_product_inventory i
WHERE i.product_id = $1 AND i.store_id IS NOT NULL
UNION
SELECT pol.store_id FROM catalog_product_sales_policies pol
WHERE pol.product_id = $1 AND pol.store_id IS NOT NULL
UNION
SELECT m.store_id FROM commerce_product_mappings m
WHERE m.product_id = $1 AND m.store_id IS NOT NULL;

-- name: CatalogCategoryChildStores :many
-- Proven Store ownership of a category's existing children edges.
SELECT c.store_id
FROM catalog_category_edges e
JOIN catalog_categories c ON c.category_id = e.child_id
WHERE e.parent_id = $1 AND c.store_id IS NOT NULL;

-- name: CatalogCategoryProductStores :many
-- Proven Store ownership of products referencing a category as top or
-- subcategory.
SELECT p.store_id FROM catalog_products p
WHERE p.top_category_id = $1 AND p.store_id IS NOT NULL
UNION
SELECT p.store_id
FROM catalog_product_subcategories s
JOIN catalog_products p ON p.product_id = s.product_id
WHERE s.category_id = $1 AND p.store_id IS NOT NULL;

-- name: CatalogTagProductStores :many
-- Proven Store ownership of products attached to a tag.
SELECT p.store_id
FROM catalog_product_tags t
JOIN catalog_products p ON p.product_id = t.product_id
WHERE t.tag_id = $1 AND p.store_id IS NOT NULL;
