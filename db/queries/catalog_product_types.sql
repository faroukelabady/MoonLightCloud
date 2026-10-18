-- Phase 17-R2 ProductType projections (ADR-0050). Current-state replace
-- semantics: one type row carrying the highest valid type_revision;
-- dimensions and capabilities are replaced transactionally per type
-- revision. Store ownership follows the 00023 model (see
-- UpsertCatalogCategory): NULL = unscoped legacy, COALESCE preserves
-- proven ownership, cross-Store writes are fenced in Go before any
-- upsert runs.

-- name: UpsertCatalogProductType :exec
INSERT INTO catalog_product_types (
    type_id, code, name_ar, name_en, description_ar, description_en,
    is_active, position, type_revision,
    source_event_id, source_device_id, source_payload_hash, source_received_at, store_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT (type_id) DO UPDATE SET
    code = excluded.code,
    name_ar = excluded.name_ar, name_en = excluded.name_en,
    description_ar = excluded.description_ar, description_en = excluded.description_en,
    is_active = excluded.is_active, position = excluded.position,
    type_revision = excluded.type_revision,
    source_event_id = excluded.source_event_id, source_device_id = excluded.source_device_id,
    source_payload_hash = excluded.source_payload_hash, source_received_at = excluded.source_received_at,
    store_id = excluded.store_id,
    projected_at = now();

-- name: CatalogProductTypeByID :one
SELECT type_id, code, name_ar, name_en, description_ar, description_en,
    is_active, position, type_revision,
    source_event_id, source_device_id, source_payload_hash, store_id
FROM catalog_product_types WHERE type_id = $1;

-- name: CatalogProductTypeIdentity :one
-- An internal shared-default key is valid only when its authoritative
-- source event proves the original Retail seed identity and same Store.
SELECT t.store_id, COALESCE(e.payload->>'product_type_id', '')::text AS source_type_id,
    e.store_id AS source_store_id
FROM catalog_product_types t
LEFT JOIN sync_events e ON e.event_id = t.source_event_id
    AND e.event_type = 'catalog.product_type.snapshot.v1'
WHERE t.type_id = $1;

-- name: ListCatalogProductTypes :many
SELECT type_id, code, name_ar, name_en, description_ar, description_en,
    is_active, position, type_revision,
    source_event_id, source_device_id, source_payload_hash, store_id
FROM catalog_product_types
WHERE (NULLIF($1::text, '') IS NULL OR store_id::text = $1)
ORDER BY position, code;

-- name: DeleteCatalogProductTypeDimensions :exec
DELETE FROM catalog_product_type_variant_dimensions WHERE type_id = $1;

-- name: InsertCatalogProductTypeDimension :exec
INSERT INTO catalog_product_type_variant_dimensions (type_id, definition_code, position)
VALUES ($1, $2, $3);

-- name: CatalogProductTypeDimensions :many
SELECT definition_code FROM catalog_product_type_variant_dimensions
WHERE type_id = $1 ORDER BY position, definition_code;

-- name: DeleteCatalogProductTypeCapabilities :exec
DELETE FROM catalog_product_type_capabilities WHERE type_id = $1;

-- name: InsertCatalogProductTypeCapability :exec
INSERT INTO catalog_product_type_capabilities (type_id, capability_code)
VALUES ($1, $2);

-- name: CatalogProductTypeCapabilities :many
SELECT capability_code FROM catalog_product_type_capabilities
WHERE type_id = $1 ORDER BY capability_code;

-- name: SetCatalogProductTypeID :exec
UPDATE catalog_products SET product_type_id = $2 WHERE product_id = $1;

