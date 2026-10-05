-- Phase 16 catalog-admin command persistence. Commands and targets
-- are immutable once created except for status transitions; audit
-- history is never deleted.

-- name: CreateCatalogAdminCommand :one
INSERT INTO catalog_admin_commands
    (id, store_id, command_type, command_version, entity_id, payload, payload_hash, expected_revision, actor)
VALUES
    (@id::uuid, @store_id::uuid, @command_type::text, @command_version::int, @entity_id::text,
     @payload::jsonb, @payload_hash::text, @expected_revision::bigint, @actor::text)
RETURNING id, store_id, command_type, command_version, entity_id, payload, payload_hash,
    expected_revision, actor, status, created_at, updated_at;

-- name: CreateCatalogAdminTarget :one
INSERT INTO catalog_admin_command_targets
    (id, command_id, device_id, status)
VALUES
    (@id::uuid, @command_id::uuid, @device_id::uuid, 'PENDING')
RETURNING id, command_id, device_id, status, result_code, entity_id, pre_revision, post_revision,
    delivered_at, finished_at, created_at, updated_at;

-- name: GetCatalogAdminCommand :one
SELECT id, store_id, command_type, command_version, entity_id, payload, payload_hash,
    expected_revision, actor, status, created_at, updated_at
FROM catalog_admin_commands
WHERE id = @id::uuid;

-- name: CancelCatalogAdminCommand :one
UPDATE catalog_admin_commands
SET status = 'CANCELLED', updated_at = now()
WHERE id = @id::uuid AND status = 'PENDING'
RETURNING id, status;

-- name: ListCatalogAdminCommands :many
SELECT id, store_id, command_type, command_version, entity_id, payload_hash,
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

-- Admin catalog reads: Store-scoped projection lists for the operator.
-- Never query Retail directly. Search is bounded ILIKE on SKU/name
-- with keyset pagination; pending flags come from a single EXISTS
-- per row (no per-row status queries from the frontend).

-- name: AdminProductList :many
SELECT p.product_id, p.sku, p.name AS name_ar,
    COALESCE((SELECT t.name FROM catalog_product_translations t WHERE t.product_id = p.product_id AND t.locale = 'en'), '') AS name_en,
    p.is_active,
    p.source_revision AS catalog_revision,
    COALESCE(s.sell_online, FALSE) AS sell_online,
    COALESCE(inv.stock_quantity, 0)::bigint AS stock_quantity,
    p.configuration_revision,
    EXISTS (
        SELECT 1 FROM catalog_admin_commands c
        JOIN catalog_admin_command_targets t ON t.command_id = c.id
        WHERE c.store_id = @store_id::uuid AND c.entity_id = p.product_id::text
          AND c.status = 'PENDING' AND t.status IN ('PENDING','DELIVERED')
    ) AS has_pending
FROM catalog_products p
LEFT JOIN catalog_product_sales_policies s ON s.product_id = p.product_id
LEFT JOIN catalog_product_inventory inv ON inv.product_id = p.product_id
WHERE p.store_id = @store_id::uuid
  AND (@search::text = '' OR p.sku ILIKE '%'||@search::text||'%' OR p.name ILIKE '%'||@search::text||'%')
  AND (p.product_id::text < @cursor::text OR @cursor::text = '')
ORDER BY p.product_id DESC
LIMIT @limit_n::int;

-- name: AdminProductDetail :one
SELECT p.product_id, p.sku, p.name AS name_ar,
    COALESCE((SELECT t.name FROM catalog_product_translations t WHERE t.product_id = p.product_id AND t.locale = 'en'), '') AS name_en,
    p.description AS description_ar,
    COALESCE((SELECT t.description FROM catalog_product_translations t WHERE t.product_id = p.product_id AND t.locale = 'en'), '') AS description_en,
    p.width_cm, p.height_cm, p.top_category_id, p.is_active,
    p.source_revision AS catalog_revision, p.configuration_revision,
    COALESCE(s.sell_online, FALSE) AS sell_online,
    COALESCE(s.sell_offline, TRUE) AS sell_offline,
    COALESCE(inv.stock_quantity, 0)::bigint AS stock_quantity
FROM catalog_products p
LEFT JOIN catalog_product_sales_policies s ON s.product_id = p.product_id
LEFT JOIN catalog_product_inventory inv ON inv.product_id = p.product_id
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
    WHERE c.store_id = @store_id::uuid AND c.entity_id = @product_id::uuid
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
