-- Phase 16 catalog-admin command persistence. Commands and targets
-- are immutable once created except for status transitions; audit
-- history is never deleted.

-- name: CreateCatalogAdminCommand :one
-- Phase 17-R3 create identity: target_kind/requested_key distinguish a
-- creation intent (entity_id = explicit absent marker '') from an entity
-- command (entity_id = business UUID). No fake business identity, ever.
INSERT INTO catalog_admin_commands
    (id, store_id, command_type, command_version, entity_id, target_kind, requested_key, payload, payload_hash, expected_revision, actor)
VALUES
    (@id::uuid, @store_id::uuid, @command_type::text, @command_version::int, @entity_id::text,
     @target_kind::text, sqlc.narg(requested_key),
     @payload::jsonb, @payload_hash::text, @expected_revision::bigint, @actor::text)
RETURNING id, store_id, command_type, command_version, entity_id, target_kind, requested_key, result_entity_id, payload, payload_hash,
    expected_revision, actor, status, created_at, updated_at;

-- name: CreateCatalogAdminTarget :one
INSERT INTO catalog_admin_command_targets
    (id, command_id, device_id, status)
VALUES
    (@id::uuid, @command_id::uuid, @device_id::uuid, 'PENDING')
RETURNING id, command_id, device_id, status, result_code, entity_id, pre_revision, post_revision,
    delivered_at, finished_at, created_at, updated_at;

-- name: GetCatalogAdminCommand :one
SELECT id, store_id, command_type, command_version, entity_id, target_kind, requested_key, result_entity_id, payload, payload_hash,
    expected_revision, actor, status, created_at, updated_at
FROM catalog_admin_commands
WHERE id = @id::uuid;

-- name: CancelCatalogAdminCommand :one
UPDATE catalog_admin_commands
SET status = 'CANCELLED', updated_at = now()
WHERE id = @id::uuid AND status = 'PENDING'
RETURNING id, status;

-- name: ListCatalogAdminCommands :many
SELECT id, store_id, command_type, command_version, entity_id, target_kind, requested_key, result_entity_id, payload_hash,
    expected_revision, actor, status, created_at, updated_at
FROM catalog_admin_commands
WHERE store_id = @store_id::uuid
  AND (@command_type::text = '' OR command_type = @command_type::text)
  AND (@entity_id::text = '' OR entity_id = @entity_id::text)
  AND (@status::text = '' OR status = @status::text)
  AND (@cursor_ts::timestamptz IS NULL
    OR (created_at, id) < (@cursor_ts, @cursor_id::uuid))
ORDER BY created_at DESC, id DESC
LIMIT @limit_n::int;

-- name: ListCatalogAdminTargets :many
SELECT id, command_id, device_id, status, result_code, entity_id, pre_revision, post_revision,
    delivered_at, finished_at, created_at, updated_at
FROM catalog_admin_command_targets
WHERE command_id = @command_id::uuid
ORDER BY created_at, id;

-- name: DueCatalogAdminTargets :many
SELECT t.id, t.command_id, t.device_id, t.status,
    c.command_type, c.command_version, c.store_id, c.entity_id, c.payload, c.payload_hash
FROM catalog_admin_command_targets t
JOIN catalog_admin_commands c ON c.id = t.command_id
WHERE t.device_id = @device_id::uuid
  AND t.status IN ('PENDING','DELIVERED')
  AND c.status = 'PENDING'
ORDER BY c.created_at, c.id
LIMIT @limit_n::int;

-- name: FinishCatalogAdminTarget :one
UPDATE catalog_admin_command_targets
SET status = @status::text, result_code = @result_code::text, entity_id = @entity_id::text,
    pre_revision = @pre_revision::bigint, post_revision = @post_revision::bigint,
    finished_at = CASE WHEN @status::text IN ('APPLIED','CONFLICT','REJECTED','BLOCKED_CAPABILITY','SKIPPED_REVOKED','CANCELLED') THEN now() ELSE finished_at END,
    delivered_at = COALESCE(delivered_at, now()),
    updated_at = now()
WHERE id = @id::uuid AND device_id = @device_id::uuid
  AND status IN ('PENDING','DELIVERED')
RETURNING id, command_id, device_id, status, result_code;

-- name: MarkCatalogAdminTargetDelivered :one
UPDATE catalog_admin_command_targets
SET status = 'DELIVERED', delivered_at = COALESCE(delivered_at, now()), updated_at = now()
WHERE id = @id::uuid AND device_id = @device_id::uuid AND status = 'PENDING'
RETURNING id, status;

-- name: CancelPendingCatalogAdminTargets :exec
UPDATE catalog_admin_command_targets
SET status = 'CANCELLED', updated_at = now()
WHERE command_id = @command_id::uuid AND status IN ('PENDING','DELIVERED');

-- name: UpsertCatalogAdminCapability :one
INSERT INTO catalog_admin_device_capabilities (device_id, catalog_admin_commands_v1, updated_at)
VALUES (@device_id::uuid, @capable::bool, now())
ON CONFLICT (device_id) DO UPDATE
SET catalog_admin_commands_v1 = EXCLUDED.catalog_admin_commands_v1, updated_at = now()
RETURNING device_id, catalog_admin_commands_v1, updated_at;

-- name: GetCatalogAdminCapability :one
SELECT device_id, catalog_admin_commands_v1, updated_at
FROM catalog_admin_device_capabilities
WHERE device_id = @device_id::uuid;

-- name: ListStoreBoundDevices :many
SELECT b.device_id, d.name
FROM device_store_bindings b
JOIN devices d ON d.id = b.device_id
WHERE b.store_id = @store_id::uuid
ORDER BY b.device_id;

-- Projection ownership + convergence evidence. source_revision is the
-- Retail authoritative revision of the last projected snapshot for
-- that stream; store_id pins ownership (legacy NULL never matches a
-- specific Store).
-- name: AdminProductOwnership :one
SELECT product_id, store_id, source_revision
FROM catalog_products
WHERE product_id = @product_id::uuid;

-- name: AdminProductPolicyRevision :one
SELECT product_id, source_revision
FROM catalog_product_sales_policies
WHERE product_id = @product_id::uuid;

-- name: AdminProductConfigurationRevision :one
SELECT product_id, configuration_revision
FROM catalog_products
WHERE product_id = @product_id::uuid;

-- name: AdminCategoryOwnership :one
SELECT category_id, store_id, source_revision
FROM catalog_categories
WHERE category_id = @category_id::uuid;

-- name: AdminTagOwnership :one
SELECT tag_id, store_id, source_revision
FROM catalog_tags
WHERE tag_id = @tag_id::uuid;

-- Phase 17-R2 type ownership (type_revision is the type stream gate).
-- name: AdminProductTypeOwnership :one
SELECT type_id, store_id, type_revision
FROM catalog_product_types
WHERE type_id = @type_id::uuid;

-- name: AdminProductTypeByCode :one
SELECT type_id, store_id, type_revision
FROM catalog_product_types
WHERE code = @code::text AND ((store_id::text = @store_id::text) OR (@store_id::text = '' AND store_id IS NULL));

-- Phase 17-R2 type reads (Store-scoped, projection only).
-- name: AdminProductTypeList :many
SELECT t.type_id, t.code, t.name_ar, t.name_en,
    t.description_ar, t.description_en, t.is_active, t.position,
    t.type_revision,
    COALESCE(source.payload->>'product_type_id', '')::text AS source_type_id,
    source.store_id AS source_store_id
FROM catalog_product_types t
LEFT JOIN sync_events source ON source.event_id = t.source_event_id
    AND source.event_type = 'catalog.product_type.snapshot.v1'
WHERE (t.store_id::text = @store_id::text OR (@store_id::text = '' AND t.store_id IS NULL))
ORDER BY t.position, t.code;

-- name: AdminProductTypeDetail :one
SELECT t.type_id, t.code, t.name_ar, t.name_en,
    t.description_ar, t.description_en,
    t.is_active, t.position, t.type_revision,
    COALESCE(source.payload->>'product_type_id', '')::text AS source_type_id,
    source.store_id AS source_store_id
FROM catalog_product_types t
LEFT JOIN sync_events source ON source.event_id = t.source_event_id
    AND source.event_type = 'catalog.product_type.snapshot.v1'
WHERE t.type_id = @type_id::uuid
  AND (t.store_id::text = @store_id::text OR (@store_id::text = '' AND t.store_id IS NULL));

-- Admin catalog reads: Store-scoped projection lists for the operator.
-- Never query Retail directly. Search is bounded ILIKE on variant SKU/name
-- with keyset pagination; pending flags come from a single EXISTS
-- per row (no per-row status queries from the frontend).
-- Phase 17-R0 (ADR-0049): product rows carry NO sku/stock — they carry
-- variant_count and derived_stock (SUM of active, non-tombstoned variant
-- stock; product stock is derived, never authoritative). Variant rows own
-- the SKU.

-- name: AdminProductList :many
SELECT p.product_id, p.name AS name_ar,
    COALESCE((SELECT t.name FROM catalog_product_translations t WHERE t.product_id = p.product_id AND t.locale = 'en'), '') AS name_en,
    p.is_active, p.product_type_id,
    COALESCE(type_source.payload->>'product_type_id', '')::text AS source_type_id,
    type_source.store_id AS source_store_id,
    p.source_revision AS catalog_revision,
    COALESCE(s.sell_online, FALSE) AS sell_online,
    (SELECT count(*) FROM catalog_product_variants v
     WHERE v.product_id = p.product_id AND NOT v.deleted)::bigint AS variant_count,
    COALESCE((SELECT sum(vi.stock_quantity)
     FROM catalog_product_variant_inventory vi
     JOIN catalog_product_variants v ON v.variant_id = vi.variant_id
     WHERE v.product_id = p.product_id AND v.is_active AND NOT v.deleted), 0)::bigint AS derived_stock,
    p.configuration_revision,
    EXISTS (
        SELECT 1 FROM catalog_admin_commands c
        JOIN catalog_admin_command_targets t ON t.command_id = c.id
        WHERE c.store_id = @store_id::uuid AND c.entity_id = p.product_id::text
          AND c.status = 'PENDING' AND t.status IN ('PENDING','DELIVERED')
    ) AS has_pending
FROM catalog_products p
LEFT JOIN catalog_product_sales_policies s ON s.product_id = p.product_id
LEFT JOIN catalog_product_types product_type ON product_type.type_id = p.product_type_id
    AND product_type.store_id = p.store_id
LEFT JOIN sync_events type_source ON type_source.event_id = product_type.source_event_id
    AND type_source.event_type = 'catalog.product_type.snapshot.v1'
WHERE p.store_id = @store_id::uuid
  AND (sqlc.arg(search)::text = '' OR p.name ILIKE '%'||sqlc.arg(search)::text||'%'
       OR EXISTS (SELECT 1 FROM catalog_product_variants v
                  WHERE v.product_id = p.product_id
                    AND v.sku ILIKE '%'||sqlc.arg(search)::text||'%'))
  AND (p.product_id::text < @cursor::text OR @cursor::text = '')
ORDER BY p.product_id DESC
LIMIT @limit_n::int;

-- name: AdminProductDetail :one
SELECT p.product_id, p.name AS name_ar,
    COALESCE((SELECT t.name FROM catalog_product_translations t WHERE t.product_id = p.product_id AND t.locale = 'en'), '') AS name_en,
    p.description AS description_ar,
    COALESCE((SELECT t.description FROM catalog_product_translations t WHERE t.product_id = p.product_id AND t.locale = 'en'), '') AS description_en,
    p.width_cm, p.height_cm, p.top_category_id, p.is_active, p.product_type_id,
    COALESCE(type_source.payload->>'product_type_id', '')::text AS source_type_id,
    type_source.store_id AS source_store_id,
    p.source_revision AS catalog_revision, p.configuration_revision,
    COALESCE(s.sell_online, FALSE) AS sell_online,
    COALESCE(s.sell_offline, TRUE) AS sell_offline,
    (SELECT count(*) FROM catalog_product_variants v
     WHERE v.product_id = p.product_id AND NOT v.deleted)::bigint AS variant_count,
    COALESCE((SELECT sum(vi.stock_quantity)
     FROM catalog_product_variant_inventory vi
     JOIN catalog_product_variants v ON v.variant_id = vi.variant_id
     WHERE v.product_id = p.product_id AND v.is_active AND NOT v.deleted), 0)::bigint AS derived_stock
FROM catalog_products p
LEFT JOIN catalog_product_sales_policies s ON s.product_id = p.product_id
LEFT JOIN catalog_product_types product_type ON product_type.type_id = p.product_type_id
    AND product_type.store_id = p.store_id
LEFT JOIN sync_events type_source ON type_source.event_id = product_type.source_event_id
    AND type_source.event_type = 'catalog.product_type.snapshot.v1'
WHERE p.product_id = @product_id::uuid AND p.store_id = @store_id::uuid;

-- name: AdminProductPrices :many
SELECT product_id, currency, price_minor, cost_minor
FROM catalog_product_prices
WHERE product_id = @product_id::uuid;

-- name: AdminProductSubcategories :many
SELECT category_id
FROM catalog_product_subcategories
WHERE product_id = @product_id::uuid;

-- name: AdminProductTags :many
SELECT tag_id
FROM catalog_product_tags
WHERE product_id = @product_id::uuid;

-- name: AdminCategoryList :many
SELECT c.category_id, c.name_ar, c.name_en, c.status, c.online_enabled,
    c.source_revision AS catalog_revision,
    COALESCE((SELECT array_agg(e.parent_id::text ORDER BY e.position) FROM catalog_category_edges e WHERE e.child_id = c.category_id), '{}') AS parent_ids,
    EXISTS (
        SELECT 1 FROM catalog_admin_commands cmd
        JOIN catalog_admin_command_targets t ON t.command_id = cmd.id
        WHERE cmd.store_id = @store_id::uuid AND cmd.entity_id = c.category_id::text
          AND cmd.status = 'PENDING' AND t.status IN ('PENDING','DELIVERED')
    ) AS has_pending
FROM catalog_categories c
WHERE c.store_id = @store_id::uuid
ORDER BY c.category_id;

-- name: AdminTagList :many
SELECT t.tag_id, t.slug, t.name_ar, t.name_en, t.is_active,
    t.source_revision AS catalog_revision,
    EXISTS (
        SELECT 1 FROM catalog_admin_commands c
        JOIN catalog_admin_command_targets tgt ON tgt.command_id = c.id
        WHERE c.store_id = @store_id::uuid AND c.entity_id = t.tag_id::text
          AND c.status = 'PENDING' AND tgt.status IN ('PENDING','DELIVERED')
    ) AS has_pending
FROM catalog_tags t
WHERE t.store_id = @store_id::uuid
ORDER BY t.tag_id;

-- name: AdminProductHasPending :one
SELECT EXISTS (
    SELECT 1 FROM catalog_admin_commands c
    JOIN catalog_admin_command_targets t ON t.command_id = c.id
    WHERE c.store_id = @store_id::uuid AND c.entity_id = (@product_id::uuid)::text
      AND c.status = 'PENDING' AND t.status IN ('PENDING','DELIVERED')
)::bool AS has_pending;

-- name: AdminProductConfigurations :many
SELECT configuration_id, style_code, style_name_ar, style_name_en,
    color_code, color_name_ar, color_name_en,
    price_delta_egp_minor, price_delta_usd_minor, enabled, position
FROM catalog_product_configurations
WHERE product_id = @product_id::uuid
ORDER BY position, configuration_id;

-- name: ListCatalogAdminTargetsBatch :many
SELECT id, command_id, device_id, status, result_code, entity_id, pre_revision, post_revision,
    delivered_at, finished_at, created_at, updated_at
FROM catalog_admin_command_targets
WHERE command_id = ANY(@command_ids::uuid[])
ORDER BY command_id, created_at, id;

-- Phase 17 variant admin reads: Store-scoped projection rows for the
-- operator (variant identity, prices, stock, option attributes). Never
-- query Retail directly; tombstones are shown but flagged.

-- name: AdminVariantOwnership :one
SELECT variant_id, store_id, variant_revision
FROM catalog_product_variants
WHERE variant_id = @variant_id::uuid;

-- name: AdminProductVariants :many
SELECT v.variant_id, v.product_id, v.sku, v.is_active, v.deleted,
    v.price_egp_cents, v.price_usd_cents, v.position, v.combination_key,
    v.variant_revision, v.catalog_revision,
    COALESCE(inv.stock_quantity, 0)::bigint AS stock_quantity,
    COALESCE(inv.source_revision, 0)::bigint AS inventory_revision,
    EXISTS (
        SELECT 1 FROM catalog_admin_commands c
        JOIN catalog_admin_command_targets t ON t.command_id = c.id
        WHERE c.store_id = @store_id::uuid
          AND (c.entity_id = v.variant_id::text OR c.entity_id = v.product_id::text)
          AND c.status = 'PENDING' AND t.status IN ('PENDING','DELIVERED')
    ) AS has_pending
FROM catalog_product_variants v
LEFT JOIN catalog_product_variant_inventory inv ON inv.variant_id = v.variant_id
WHERE v.product_id = @product_id::uuid AND v.store_id = @store_id::uuid
ORDER BY v.position, v.variant_id;

-- name: AdminProductVariantByVariantID :one
SELECT v.variant_id, v.product_id, v.sku, v.is_active, v.deleted,
    v.price_egp_cents, v.price_usd_cents, v.position, v.combination_key,
    v.variant_revision, v.catalog_revision,
    COALESCE(inv.stock_quantity, 0)::bigint AS stock_quantity,
    COALESCE(inv.source_revision, 0)::bigint AS inventory_revision,
    EXISTS (
        SELECT 1 FROM catalog_admin_commands c
        JOIN catalog_admin_command_targets t ON t.command_id = c.id
        WHERE c.store_id = @store_id::uuid
          AND (c.entity_id = v.variant_id::text OR c.entity_id = v.product_id::text)
          AND c.status = 'PENDING' AND t.status IN ('PENDING','DELIVERED')
    ) AS has_pending
FROM catalog_product_variants v
LEFT JOIN catalog_product_variant_inventory inv ON inv.variant_id = v.variant_id
WHERE v.variant_id = @variant_id::uuid AND v.store_id = @store_id::uuid;

-- name: AdminProductVariantAttributes :many
SELECT definition_code, value_code, name_ar, name_en, definition_name_ar, definition_name_en, position
FROM catalog_product_variant_attribute_values
WHERE variant_id = @variant_id::uuid
ORDER BY position, definition_code;

-- Phase 17-R3 result identity: the actual Retail-minted entity ID for a
-- command, set when one of its targets reports APPLIED. First writer wins
-- (at most one APPLIED per create intent exists); intent rows are never
-- rewritten, only the result evidence is appended. Keyed by target so the
-- outcome path (which binds targets, not commands) needs no extra lookup.
-- name: SetCommandResultEntity :execrows
UPDATE catalog_admin_commands
SET result_entity_id = @result_entity_id::text, updated_at = now()
WHERE id IN (SELECT command_id FROM catalog_admin_command_targets WHERE id = @target_id::uuid)
  AND result_entity_id IS NULL;

-- Phase 19: batched device state for command target annotation. Replaces
-- per-target DeviceActive + BindingStore + Capability + Name lookups
-- (N+1: up to 5 statements per target) with one set-based read.
-- name: CatalogAdminDeviceStates :many
SELECT d.id, d.name, d.status,
       b.store_id,
       COALESCE(cap.catalog_admin_commands_v1, FALSE) AS catalog_admin_commands_v1
FROM devices d
LEFT JOIN device_store_bindings b ON b.device_id = d.id
LEFT JOIN catalog_admin_device_capabilities cap ON cap.device_id = d.id
WHERE d.id = ANY(@device_ids::uuid[]);
