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

-- name: UpdateCommerceProductMappingExternal :one
-- Phase 15-R1 F07 (§116): compare-and-set sellable-identity transition.
-- Only the currently recorded external identity may be replaced; a
-- concurrent different mapping is never clobbered.
UPDATE commerce_product_mappings
SET external_product_id = $4, updated_at = now()
WHERE provider_key = $1 AND product_id = $2 AND external_product_id = $3
RETURNING provider_key, product_id, external_product_id, store_id, created_at, updated_at;

-- Phase 17 durable ProductVariant mappings (00033): one MoonLight
-- variant ↔ one provider variation per provider instance. Integration
-- state, not a projection: never cleared by rebuilds, no FK to
-- rebuildable projections.

-- name: GetCommerceProductVariantMapping :one
SELECT provider_key, product_id, variant_id, external_product_id, external_variant_id, store_id, created_at, updated_at
FROM commerce_product_variant_mappings
WHERE provider_key = $1 AND variant_id = $2;

-- name: ListCommerceProductVariantMappings :many
SELECT provider_key, product_id, variant_id, external_product_id, external_variant_id, store_id, created_at, updated_at
FROM commerce_product_variant_mappings
WHERE provider_key = $1 AND product_id = $2
ORDER BY variant_id;

-- name: FindCommerceProductVariantMappingByExternal :one
SELECT provider_key, product_id, variant_id, external_product_id, external_variant_id, store_id, created_at, updated_at
FROM commerce_product_variant_mappings
WHERE provider_key = $1 AND external_product_id = $2 AND external_variant_id = $3;

-- name: CreateCommerceProductVariantMapping :one
-- store_id mirrors the authoritative catalog product at creation (NULL
-- for legacy/unprojected products). ON CONFLICT DO NOTHING keeps
-- same-pair idempotency; remaps and cross-variant external reuse are
-- classified in Go before insert.
INSERT INTO commerce_product_variant_mappings (provider_key, product_id, variant_id, external_product_id, external_variant_id, store_id)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT DO NOTHING
RETURNING provider_key, product_id, variant_id, external_product_id, external_variant_id, store_id, created_at, updated_at;

-- name: UpdateCommerceProductVariantMappingExternal :one
UPDATE commerce_product_variant_mappings m
SET external_variant_id = sqlc.arg(new_external_variant_id), updated_at = now()
WHERE m.provider_key = sqlc.arg(provider_key) AND m.product_id = sqlc.arg(product_id)
 AND m.variant_id = sqlc.arg(variant_id) AND m.external_product_id = sqlc.arg(external_product_id)
 AND m.external_variant_id = sqlc.arg(expected_external_variant_id)
 AND m.store_id IS NOT NULL
 AND EXISTS (SELECT 1 FROM catalog_products p WHERE p.product_id=m.product_id AND p.store_id=m.store_id)
RETURNING m.provider_key, m.product_id, m.variant_id, m.external_product_id, m.external_variant_id, m.store_id, m.created_at, m.updated_at;
