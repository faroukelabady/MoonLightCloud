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
