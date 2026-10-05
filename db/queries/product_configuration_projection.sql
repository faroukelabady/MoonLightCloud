-- Phase 15: Product configuration projection (Retail-authoritative
-- current state) + durable provider configuration identity.

-- name: UpsertCatalogProductConfiguration :execrows
INSERT INTO catalog_product_configurations (
    configuration_id, product_id, kind,
    style_code, style_name_ar, style_name_en,
    color_code, color_name_ar, color_name_en,
    price_delta_egp_minor, price_delta_usd_minor,
    enabled, position, configuration_revision,
    source_revision, source_event_id, source_device_id,
    source_payload_hash, source_received_at, projected_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, now())
ON CONFLICT (configuration_id) DO UPDATE SET
    kind = excluded.kind,
    style_code = excluded.style_code,
    style_name_ar = excluded.style_name_ar,
    style_name_en = excluded.style_name_en,
    color_code = excluded.color_code,
    color_name_ar = excluded.color_name_ar,
    color_name_en = excluded.color_name_en,
    price_delta_egp_minor = excluded.price_delta_egp_minor,
    price_delta_usd_minor = excluded.price_delta_usd_minor,
    enabled = excluded.enabled,
    position = excluded.position,
    configuration_revision = excluded.configuration_revision,
    source_revision = excluded.source_revision,
    source_event_id = excluded.source_event_id,
    source_device_id = excluded.source_device_id,
    source_payload_hash = excluded.source_payload_hash,
    source_received_at = excluded.source_received_at,
    projected_at = now()
-- Immutable configuration→Product ownership (Phase 15-R1 F17): a
-- configuration ID belonging to another Product/Store is NEVER adopted;
-- the guarded update affects zero rows and the projector blocks.
WHERE catalog_product_configurations.product_id = excluded.product_id;

-- name: DeleteCatalogProductConfigurations :exec
DELETE FROM catalog_product_configurations WHERE product_id = $1;

-- name: CatalogProductConfigurationsByProduct :many
SELECT configuration_id, product_id, kind,
       style_code, style_name_ar, style_name_en,
       color_code, color_name_ar, color_name_en,
       price_delta_egp_minor, price_delta_usd_minor,
       enabled, position, configuration_revision
FROM catalog_product_configurations
WHERE product_id = $1
ORDER BY position, configuration_id;

-- name: GetCommerceProductConfigurationMapping :one
SELECT provider_key, product_id, configuration_id, external_product_id,
       external_configuration_id, store_id, created_at, updated_at
FROM commerce_product_configuration_mappings
WHERE provider_key = $1 AND product_id = $2 AND configuration_id = $3;

-- name: FindCommerceProductConfigurationMappingByExternal :one
SELECT provider_key, product_id, configuration_id, external_product_id,
       external_configuration_id, store_id, created_at, updated_at
FROM commerce_product_configuration_mappings
WHERE provider_key = $1 AND external_product_id = $2 AND external_configuration_id = $3;

-- name: UpsertCommerceProductConfigurationMapping :one
INSERT INTO commerce_product_configuration_mappings (
    provider_key, product_id, configuration_id,
    external_product_id, external_configuration_id, store_id
) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (provider_key, product_id, configuration_id) DO UPDATE SET
    external_product_id = excluded.external_product_id,
    external_configuration_id = excluded.external_configuration_id,
    store_id = COALESCE(excluded.store_id, commerce_product_configuration_mappings.store_id),
    updated_at = now()
RETURNING provider_key, product_id, configuration_id, external_product_id,
       external_configuration_id, store_id, created_at, updated_at;

-- name: ListCommerceProductConfigurationMappings :many
SELECT provider_key, product_id, configuration_id, external_product_id,
       external_configuration_id, store_id, created_at, updated_at
FROM commerce_product_configuration_mappings
WHERE provider_key = $1 AND product_id = $2
ORDER BY configuration_id;

-- name: BumpCatalogProductConfigurationRevision :execrows
UPDATE catalog_products
SET configuration_revision = $2
WHERE product_id = $1;

-- name: CatalogProductConfigurationByID :one
SELECT configuration_id, product_id, kind,
       style_code, style_name_ar, style_name_en,
       color_code, color_name_ar, color_name_en,
       price_delta_egp_minor, price_delta_usd_minor,
       enabled, position, configuration_revision
FROM catalog_product_configurations
WHERE configuration_id = $1;
