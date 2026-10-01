-- Phase 6A durable provider product mappings. Integration state, not a
-- projection: never cleared by catalog/policy/inventory rebuilds.

-- name: GetCommerceProductMapping :one
SELECT provider_key, product_id, external_product_id, store_id, created_at, updated_at
FROM commerce_product_mappings
WHERE provider_key = $1 AND product_id = $2;

-- name: FindCommerceProductMappingByExternal :one
SELECT provider_key, product_id, external_product_id, store_id, created_at, updated_at
FROM commerce_product_mappings
WHERE provider_key = $1 AND external_product_id = $2;

-- name: CreateCommerceProductMapping :one
-- Phase 9C: store_id mirrors the authoritative catalog product at
-- creation (NULL for legacy products). ON CONFLICT DO NOTHING keeps the
-- frozen same-pair idempotency; adoption and cross-Store conflicts are
-- decided in Go before insert.
INSERT INTO commerce_product_mappings (provider_key, product_id, external_product_id, store_id)
VALUES ($1, $2, $3, $4)
ON CONFLICT DO NOTHING
RETURNING provider_key, product_id, external_product_id, store_id, created_at, updated_at;

-- name: AdoptCommerceProductMappingStore :one
-- Phase 9C one-time legacy adoption: a NULL mapping adopts the proven
-- Store of its own product. The WHERE clause makes concurrent adoption
-- deterministic (same product implies same Store); a lost race reads
-- back the winner instead of overwriting.
UPDATE commerce_product_mappings SET store_id = $3, updated_at = now()
WHERE provider_key = $1 AND product_id = $2 AND store_id IS NULL
RETURNING provider_key, product_id, external_product_id, store_id, created_at, updated_at;

-- name: ListCommerceProductMappingsForStore :many
-- Phase 9C Store-scoped mapping enumeration. No global list exists, so
-- there is no legacy behavior to preserve: scoped reads only.
SELECT provider_key, product_id, external_product_id, store_id, created_at, updated_at
FROM commerce_product_mappings
WHERE provider_key = $1 AND store_id = $2
ORDER BY product_id;
