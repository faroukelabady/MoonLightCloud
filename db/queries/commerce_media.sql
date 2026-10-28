-- ADR-0056 step 2: provider media that MoonLight created. The adapter only
-- ever changes or deletes media listed here.

-- name: ListCommerceMediaMappings :many
SELECT provider_key, product_id, sha256, store_id, external_product_id, external_media_id, created_at
FROM commerce_media_mappings
WHERE provider_key = $1 AND product_id = $2
ORDER BY created_at, sha256;

-- name: InsertCommerceMediaMapping :execrows
-- The Store comes from the projected Product, never from the caller.
INSERT INTO commerce_media_mappings (provider_key, product_id, sha256, store_id, external_product_id, external_media_id)
SELECT sqlc.arg(provider_key)::text, p.product_id, sqlc.arg(sha256)::text, p.store_id,
       sqlc.arg(external_product_id)::text, sqlc.arg(external_media_id)::text
FROM catalog_products p
WHERE p.product_id = sqlc.arg(product_id)::uuid
ON CONFLICT DO NOTHING;

-- name: DeleteCommerceMediaMapping :execrows
-- Compare-and-delete: a row that now names other media is left alone.
DELETE FROM commerce_media_mappings
WHERE provider_key = $1 AND product_id = $2 AND sha256 = $3 AND external_media_id = $4;
