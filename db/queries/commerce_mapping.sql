-- Phase 6A durable provider product mappings. Integration state, not a
-- projection: never cleared by catalog/policy/inventory rebuilds.

-- name: GetCommerceProductMapping :one
SELECT provider_key, product_id, external_product_id, created_at, updated_at
FROM commerce_product_mappings
WHERE provider_key = $1 AND product_id = $2;

-- name: FindCommerceProductMappingByExternal :one
SELECT provider_key, product_id, external_product_id, created_at, updated_at
FROM commerce_product_mappings
WHERE provider_key = $1 AND external_product_id = $2;

-- name: CreateCommerceProductMapping :one
INSERT INTO commerce_product_mappings (provider_key, product_id, external_product_id)
VALUES ($1, $2, $3)
ON CONFLICT DO NOTHING
RETURNING provider_key, product_id, external_product_id, created_at, updated_at;
