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
WHERE (e.event_type = $2
       OR ($2 IN ('catalog.category.snapshot.v1','catalog.category.snapshot.v2')
           AND e.event_type IN ('catalog.category.snapshot.v1','catalog.category.snapshot.v2'))
       OR ($2 IN ('catalog.product.snapshot.v1','catalog.product.snapshot.v2')
           AND e.event_type IN ('catalog.product.snapshot.v1','catalog.product.snapshot.v2')))
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
    source_payload_hash, source_received_at, store_id, default_algorithm,
    online_enabled
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (category_id) DO UPDATE SET
    status = excluded.status, name_ar = excluded.name_ar, name_en = excluded.name_en,
    source_revision = excluded.source_revision, source_event_id = excluded.source_event_id,
    source_device_id = excluded.source_device_id, source_payload_hash = excluded.source_payload_hash,
    source_received_at = excluded.source_received_at,
    store_id = COALESCE(excluded.store_id, catalog_categories.store_id),
    default_algorithm = excluded.default_algorithm,
    online_enabled = excluded.online_enabled,
    projected_at = now();

-- name: CatalogCategoryByID :one
SELECT category_id, status, name_ar, name_en, online_enabled, source_revision,
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

-- name: CatalogTagByID :one
SELECT tag_id, slug, is_active, name_ar, name_en, source_revision,
    source_event_id, source_device_id, source_payload_hash, store_id
FROM catalog_tags WHERE tag_id = $1;

-- name: UpsertCatalogProduct :exec
-- Phase 9B store ownership: see UpsertCatalogCategory.
-- Phase 17-R0 (ADR-0049): Product carries NO SKU — SKU/stock identity
-- lives on ProductVariant; the column was retired by 00035.
INSERT INTO catalog_products (
    product_id, name, description, top_category_id, width_cm, height_cm,
    is_active, product_type_id,
    source_revision, source_event_id, source_device_id,
    source_payload_hash, source_received_at, store_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT (product_id) DO UPDATE SET
    name = excluded.name, description = excluded.description,
    top_category_id = excluded.top_category_id, width_cm = excluded.width_cm,
    height_cm = excluded.height_cm, is_active = excluded.is_active,
    product_type_id = excluded.product_type_id,
    source_revision = excluded.source_revision, source_event_id = excluded.source_event_id,
    source_device_id = excluded.source_device_id, source_payload_hash = excluded.source_payload_hash,
    source_received_at = excluded.source_received_at,
    store_id = COALESCE(excluded.store_id, catalog_products.store_id),
    projected_at = now();

-- name: CatalogProductByID :one
SELECT product_id, name, description, top_category_id, width_cm, height_cm,
    is_active, product_type_id, source_revision, source_event_id, source_device_id, source_payload_hash, store_id,
    configuration_revision
FROM catalog_products WHERE product_id = $1;

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
-- Phase 17-R0: Product has no SKU (ADR-0049); deterministic product_id
-- ordering replaces the retired SKU ordering.
SELECT product_id, name, source_revision, source_event_id
FROM catalog_products
WHERE is_active ORDER BY product_id
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
WHERE (e.event_type = $1
       OR ($1 IN ('catalog.category.snapshot.v1','catalog.category.snapshot.v2')
           AND e.event_type IN ('catalog.category.snapshot.v1','catalog.category.snapshot.v2'))
       OR ($1 IN ('catalog.product.snapshot.v1','catalog.product.snapshot.v2')
           AND e.event_type IN ('catalog.product.snapshot.v1','catalog.product.snapshot.v2')))
  AND e.payload->>($2::text) = ($3::text)
  AND (CASE WHEN sqlc.arg(store_scoped_default)::boolean
       THEN e.store_id IS NOT DISTINCT FROM sqlc.narg(store_id)::uuid
       ELSE sqlc.narg(store_id)::uuid IS NULL OR e.store_id IS NULL
            OR e.store_id = sqlc.narg(store_id)::uuid END);

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
    JOIN catalog_categories c ON c.category_id = $1::uuid
    JOIN sync_events source ON source.event_id = c.source_event_id
    WHERE e.event_type IN ('catalog.product.snapshot.v1','catalog.product.snapshot.v2')
      AND ((c.default_algorithm = 1 AND e.store_id = c.store_id)
        OR (c.default_algorithm <> 1 AND (e.store_id IS NULL OR c.store_id IS NULL OR e.store_id = c.store_id)))
      AND (e.payload->>'top_category_id' IN (c.category_id::text, source.payload->>'category_id')
           OR e.payload->'subcategory_ids' @> to_jsonb(c.category_id::text)
           OR e.payload->'subcategory_ids' @> to_jsonb(source.payload->>'category_id'))
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
  AND e.event_type IN ('catalog.product.snapshot.v1','catalog.product.snapshot.v2')
  AND NOT EXISTS (
    SELECT 1 FROM sync_events e2
    WHERE e2.event_type IN ('catalog.product.snapshot.v1','catalog.product.snapshot.v2')
      AND e2.payload->>'product_id' = e.payload->>'product_id'
      AND (e.store_id IS NULL OR e2.store_id IS NULL OR e2.store_id = e.store_id)
      AND (e2.payload->>'catalog_revision')::bigint > (e.payload->>'catalog_revision')::bigint
  )
  AND EXISTS (
    SELECT 1 FROM catalog_categories c JOIN sync_events source ON source.event_id = c.source_event_id
    WHERE ((c.default_algorithm = 1 AND e.store_id = c.store_id)
       OR (c.default_algorithm <> 1 AND (e.store_id IS NULL OR c.store_id IS NULL OR e.store_id = c.store_id)))
      AND (c.category_id::text = e.payload->>'top_category_id'
           OR source.payload->>'category_id' = e.payload->>'top_category_id'
           OR c.category_id::text IN (SELECT jsonb_array_elements_text(e.payload->'subcategory_ids'))
           OR source.payload->>'category_id' IN (SELECT jsonb_array_elements_text(e.payload->'subcategory_ids')))
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
-- Phase 17: variant rows and their inventory/mappings are dependents of
-- the product too (SKU/inventory ownership moved to variant).
SELECT i.store_id FROM catalog_product_inventory i
WHERE i.product_id = $1 AND i.store_id IS NOT NULL
UNION
SELECT pol.store_id FROM catalog_product_sales_policies pol
WHERE pol.product_id = $1 AND pol.store_id IS NOT NULL
UNION
SELECT m.store_id FROM commerce_product_mappings m
WHERE m.product_id = $1 AND m.store_id IS NOT NULL
UNION
SELECT v.store_id FROM catalog_product_variants v
WHERE v.product_id = $1 AND v.store_id IS NOT NULL
UNION
SELECT vi.store_id FROM catalog_product_variant_inventory vi
WHERE vi.product_id = $1 AND vi.store_id IS NOT NULL
UNION
SELECT vm.store_id FROM commerce_product_variant_mappings vm
WHERE vm.product_id = $1 AND vm.store_id IS NOT NULL;

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

-- name: RetireRawDefaultCategory :execrows
-- Keep the raw compatibility snapshot and its source provenance. It no
-- longer claims a Store once that Store transitions to a canonical row.
UPDATE catalog_categories c SET store_id = NULL, default_algorithm = 2
WHERE c.category_id = $1 AND c.default_algorithm = 0
  AND (c.store_id = $2 OR c.store_id IS NULL)
  AND EXISTS (SELECT 1 FROM sync_events e WHERE e.event_id=c.source_event_id
    AND e.store_id=$2 AND e.event_type IN ('catalog.category.snapshot.v1','catalog.category.snapshot.v2')
    AND e.payload->>'category_id'=c.category_id::text);

-- name: RetireRawDefaultTag :execrows
-- Also releases the obsolete (Store, slug) claim before canonical replay.
UPDATE catalog_tags t SET store_id = NULL, default_algorithm = 2
WHERE t.tag_id = $1 AND t.default_algorithm = 0
  AND (t.store_id = $2 OR t.store_id IS NULL)
  AND EXISTS (SELECT 1 FROM sync_events e WHERE e.event_id=t.source_event_id
    AND e.store_id=$2 AND e.event_type='catalog.tag.snapshot.v1'
    AND e.payload->>'tag_id'=t.tag_id::text);

-- name: EstablishedDefaultCatalog :one
-- A no-op may retire compatibility state only for a proven canonical row.
SELECT EXISTS (
 SELECT 1 FROM catalog_categories c JOIN sync_events e ON e.event_id=c.source_event_id
 WHERE sqlc.arg(kind)::text='category' AND c.category_id=sqlc.arg(canonical_id)::uuid
 AND c.store_id=sqlc.arg(store_id)::uuid AND e.store_id=c.store_id
 AND c.default_algorithm=1 AND e.payload->>'category_id'=sqlc.arg(raw_id)::text
) OR EXISTS (
 SELECT 1 FROM catalog_tags t JOIN sync_events e ON e.event_id=t.source_event_id
 WHERE sqlc.arg(kind)::text='tag' AND t.tag_id=sqlc.arg(canonical_id)::uuid
 AND t.store_id=sqlc.arg(store_id)::uuid AND e.store_id=t.store_id
 AND t.default_algorithm=1 AND e.payload->>'tag_id'=sqlc.arg(raw_id)::text
) AS established;

-- name: DefaultCatalogRecoveryCandidates :many
-- Only authoritative scoped events are replayed. Never reset genuine
-- equal-revision/ownership conflicts, or unbound historical processing.
WITH defaults AS (
 SELECT DISTINCT ON (e.store_id,CASE WHEN e.event_type IN ('catalog.category.snapshot.v1','catalog.category.snapshot.v2') THEN 'catalog.category.snapshot' ELSE e.event_type END,COALESCE(e.payload->>'category_id',e.payload->>'tag_id'))
        e.event_id,e.event_type,e.store_id,e.payload,p.processor,e.received_at
 FROM sync_events e JOIN sync_event_processing p ON p.event_id=e.event_id
 WHERE e.store_id IS NOT NULL AND (
  (e.event_type IN ('catalog.category.snapshot.v1','catalog.category.snapshot.v2') AND p.processor='catalog_category_projection.v1'
   AND e.payload->>'category_id'=ANY(sqlc.arg(category_ids)::text[])) OR
  (e.event_type='catalog.tag.snapshot.v1' AND p.processor='catalog_tag_projection.v1'
   AND e.payload->>'tag_id'=ANY(sqlc.arg(tag_ids)::text[])))
 AND (p.status='processed' OR (p.status='blocked'
  AND p.last_error_code IN ('STORE_SCOPE_CONFLICT','CATALOG_REVISION_CONFLICT')
  AND p.last_error_message IN ('category owned by another store','tag owned by another store','equal revision with conflicting state')
  AND NOT EXISTS (SELECT 1 FROM sync_events own JOIN sync_event_processing settled
       ON settled.event_id=own.event_id AND settled.processor=p.processor AND settled.status='processed'
       WHERE own.store_id=e.store_id AND (own.event_type=e.event_type OR (e.event_type IN ('catalog.category.snapshot.v1','catalog.category.snapshot.v2') AND own.event_type IN ('catalog.category.snapshot.v1','catalog.category.snapshot.v2')))
         AND COALESCE(own.payload->>'category_id',own.payload->>'tag_id')=COALESCE(e.payload->>'category_id',e.payload->>'tag_id')
         AND (own.payload->>'catalog_revision')::bigint >= (e.payload->>'catalog_revision')::bigint)
  AND (EXISTS(SELECT 1 FROM catalog_categories c JOIN sync_events s ON s.event_id=c.source_event_id
        WHERE c.category_id::text=e.payload->>'category_id' AND s.store_id IS NOT NULL AND s.store_id<>e.store_id)
    OR EXISTS(SELECT 1 FROM catalog_tags t JOIN sync_events s ON s.event_id=t.source_event_id
        WHERE t.tag_id::text=e.payload->>'tag_id' AND s.store_id IS NOT NULL AND s.store_id<>e.store_id))
  AND NOT EXISTS(SELECT 1 FROM catalog_categories c JOIN sync_events s ON s.event_id=c.source_event_id
        WHERE c.default_algorithm=1 AND c.store_id=e.store_id AND s.payload->>'category_id'=e.payload->>'category_id')
  AND NOT EXISTS(SELECT 1 FROM catalog_tags t JOIN sync_events s ON s.event_id=t.source_event_id
        WHERE t.default_algorithm=1 AND t.store_id=e.store_id AND s.payload->>'tag_id'=e.payload->>'tag_id')))
 ORDER BY e.store_id,CASE WHEN e.event_type IN ('catalog.category.snapshot.v1','catalog.category.snapshot.v2') THEN 'catalog.category.snapshot' ELSE e.event_type END,COALESCE(e.payload->>'category_id',e.payload->>'tag_id'),
          (e.payload->>'catalog_revision')::bigint DESC,e.received_at DESC,e.event_id DESC
), candidates AS (
 SELECT d.event_id,d.processor,d.received_at FROM defaults d WHERE
  (d.event_type IN ('catalog.category.snapshot.v1','catalog.category.snapshot.v2') AND (
   NOT EXISTS(SELECT 1 FROM catalog_categories c JOIN sync_events s ON s.event_id=c.source_event_id
     WHERE c.default_algorithm=1 AND c.store_id=d.store_id AND s.payload->>'category_id'=d.payload->>'category_id'
       AND c.source_revision >= (d.payload->>'catalog_revision')::bigint)
   OR EXISTS(SELECT 1 FROM catalog_categories c WHERE c.category_id::text=d.payload->>'category_id' AND c.store_id=d.store_id AND c.default_algorithm=0)))
  OR (d.event_type='catalog.tag.snapshot.v1' AND (
   NOT EXISTS(SELECT 1 FROM catalog_tags t JOIN sync_events s ON s.event_id=t.source_event_id
     WHERE t.default_algorithm=1 AND t.store_id=d.store_id AND s.payload->>'tag_id'=d.payload->>'tag_id'
       AND t.source_revision >= (d.payload->>'catalog_revision')::bigint)
   OR EXISTS(SELECT 1 FROM catalog_tags t WHERE t.tag_id::text=d.payload->>'tag_id' AND t.store_id=d.store_id AND t.default_algorithm=0)))
 UNION
 -- R2 tried to insert the canonical Tag while the same Store's older raw
 -- default still occupied its slug. The retained raw source proves the
 -- obsolete collision even after retirement (algorithm 2). Other slug
 -- owners, equal revisions and unbound/custom identities never qualify.
 SELECT e.event_id,p.processor,e.received_at
 FROM sync_events e JOIN sync_event_processing p ON p.event_id=e.event_id
 JOIN catalog_tags raw ON raw.tag_id::text=e.payload->>'tag_id'
 JOIN sync_events source ON source.event_id=raw.source_event_id
 JOIN sync_event_processing settled ON settled.event_id=source.event_id
   AND settled.processor='catalog_tag_projection.v1' AND settled.status='processed'
 WHERE e.event_type='catalog.tag.snapshot.v1' AND e.store_id IS NOT NULL
 AND p.processor='catalog_tag_projection.v1' AND p.status='blocked'
 AND p.last_error_code='CATALOG_REVISION_CONFLICT' AND p.last_error_message='tag identity collision'
 AND e.payload->>'tag_id'=ANY(sqlc.arg(tag_ids)::text[])
 AND source.event_type=e.event_type AND source.store_id=e.store_id
 AND source.payload->>'tag_id'=e.payload->>'tag_id'
 AND raw.default_algorithm IN (0,2)
 AND ((raw.default_algorithm=0 AND raw.store_id=e.store_id)
   OR (raw.default_algorithm=2 AND raw.store_id IS NULL))
 AND raw.slug=e.payload->>'slug' AND source.payload->>'slug'=raw.slug
 AND raw.source_revision=(source.payload->>'catalog_revision')::bigint
 AND raw.source_revision < (e.payload->>'catalog_revision')::bigint
 AND NOT EXISTS (
  SELECT 1 FROM catalog_tags other WHERE other.store_id=e.store_id AND other.slug=raw.slug
    AND other.tag_id<>raw.tag_id AND NOT (other.default_algorithm=1 AND EXISTS (
      SELECT 1 FROM sync_events canonical WHERE canonical.event_id=other.source_event_id
      AND canonical.store_id=e.store_id AND canonical.event_type=e.event_type
      AND canonical.payload->>'tag_id'=e.payload->>'tag_id')))
 AND NOT EXISTS (
  SELECT 1 FROM sync_events own JOIN sync_event_processing terminal ON terminal.event_id=own.event_id
  AND terminal.processor=p.processor AND terminal.status='processed'
  WHERE own.store_id=e.store_id AND (own.event_type=e.event_type OR (e.event_type IN ('catalog.category.snapshot.v1','catalog.category.snapshot.v2') AND own.event_type IN ('catalog.category.snapshot.v1','catalog.category.snapshot.v2')))
  AND own.payload->>'tag_id'=e.payload->>'tag_id'
  AND own.payload->>'catalog_revision'=e.payload->>'catalog_revision'
  AND own.payload<>e.payload)
 UNION
 SELECT e.event_id,p.processor,e.received_at FROM catalog_categories c
 JOIN sync_events e ON e.event_id=c.source_event_id AND e.store_id=c.store_id
 JOIN sync_event_processing p ON p.event_id=e.event_id AND p.processor='catalog_category_projection.v1'
 WHERE p.status='processed' AND EXISTS(SELECT 1 FROM catalog_category_edges edge
   WHERE edge.child_id=c.category_id AND edge.parent_id::text=ANY(sqlc.arg(category_ids)::text[]))
 UNION
 SELECT e.event_id,p.processor,e.received_at FROM catalog_products product
 JOIN sync_events e ON e.event_id=product.source_event_id AND e.store_id=product.store_id
 JOIN sync_event_processing p ON p.event_id=e.event_id AND p.processor='catalog_product_projection.v1'
 WHERE p.status='processed' AND (product.top_category_id::text=ANY(sqlc.arg(category_ids)::text[])
   OR EXISTS(SELECT 1 FROM catalog_product_subcategories sub WHERE sub.product_id=product.product_id AND sub.category_id::text=ANY(sqlc.arg(category_ids)::text[]))
   OR EXISTS(SELECT 1 FROM catalog_product_tags tag WHERE tag.product_id=product.product_id AND tag.tag_id::text=ANY(sqlc.arg(tag_ids)::text[])))
)
SELECT event_id,processor FROM candidates ORDER BY received_at,event_id LIMIT 100;

-- name: RearmDefaultCatalogRecovery :execrows
UPDATE sync_event_processing SET status='pending',next_attempt_at=NULL,processed_at=NULL,
       last_error_code=NULL,last_error_message=NULL
WHERE event_id=$1 AND processor=$2 AND status IN ('processed','blocked');
