-- ADR-0056: Product media projection and content-addressed image blobs.

-- name: CatalogProductMediaByProduct :many
SELECT image_id, product_id, variant_id, position, is_primary, sha256,
       content_type, byte_size, width, height
FROM catalog_product_media
WHERE product_id = $1
ORDER BY variant_id NULLS FIRST, position;

-- name: CatalogProductMediaOwners :many
-- Current owners of the given image IDs (identity never moves Products).
SELECT image_id, product_id
FROM catalog_product_media
WHERE image_id = ANY(sqlc.arg(image_ids)::uuid[]);

-- name: DeleteCatalogProductMedia :exec
DELETE FROM catalog_product_media WHERE product_id = $1;

-- name: InsertCatalogProductMedia :exec
INSERT INTO catalog_product_media (
    image_id, product_id, variant_id, position, is_primary, sha256,
    content_type, byte_size, width, height,
    source_revision, source_event_id, source_device_id, source_received_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14);

-- name: SetCatalogProductMediaRevision :execrows
UPDATE catalog_products SET media_revision = $2 WHERE product_id = $1;

-- name: CatalogProductMediaRevision :one
SELECT media_revision FROM catalog_products WHERE product_id = $1;

-- name: InsertMediaBlob :execrows
-- First upload wins; a repeat of the same hash is the same bytes.
INSERT INTO media_blobs (sha256, content_type, byte_size, width, height, content, uploaded_by_device)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (sha256) DO NOTHING;

-- name: MediaBlobContent :one
SELECT sha256, content_type, byte_size, width, height, content
FROM media_blobs WHERE sha256 = $1;

-- name: MediaBlobsPresent :many
SELECT sha256 FROM media_blobs WHERE sha256 = ANY(sqlc.arg(hashes)::text[]);

-- name: CatalogProductMediaForCommerce :many
-- Commerce desired state: every gallery entry and whether its file has
-- arrived. media_blobs is joined by key only; its content is not read here.
SELECT m.image_id, m.variant_id, m.position, m.is_primary, m.sha256,
       m.content_type, m.byte_size, m.width, m.height,
       (b.sha256 IS NOT NULL)::boolean AS available
FROM catalog_product_media m
LEFT JOIN media_blobs b ON b.sha256 = m.sha256
WHERE m.product_id = $1
ORDER BY m.variant_id NULLS FIRST, m.position;

-- name: EnqueueMediaBlobReevaluations :execrows
-- A newly stored file re-evaluates every Product whose gallery names it, so
-- online channels pick up images whose bytes arrived after the snapshot.
INSERT INTO commerce_product_reevaluations (product_id, store_id, reason, requested_at, next_attempt_at)
SELECT DISTINCT p.product_id, p.store_id, 'product_media', now(), now()
FROM catalog_product_media m
JOIN catalog_products p ON p.product_id = m.product_id
WHERE m.sha256 = $1
ON CONFLICT (product_id) DO UPDATE SET
    requested_at = now(),
    requested_generation = commerce_product_reevaluations.requested_generation + 1,
    reason = EXCLUDED.reason,
    next_attempt_at = now(),
    attempts = 0,
    last_error_code = NULL;

-- name: AdminStoreMediaBlob :one
-- Dashboard image read: the file must be named by a gallery of a Product
-- the Store owns, so an operator never reads another Store's files.
SELECT b.sha256, b.content_type, b.content
FROM media_blobs b
WHERE b.sha256 = @sha256
  AND EXISTS (SELECT 1 FROM catalog_product_media m
              JOIN catalog_products p ON p.product_id = m.product_id
              WHERE m.sha256 = b.sha256 AND p.store_id = @store_id::uuid);
