-- +goose Up
-- 00040_create_command_identity: Phase 17-R3 create-command identity
-- closure (ADR-0050 §create-closure). A creation request must not pretend
-- a placeholder UUID is the business entity's identity: for create-kind
-- commands the target does not exist yet.
--
--   target_kind    'entity' (default, all Phase 16 + update commands) or
--                  'create' (creation intent; the entity is minted
--                  Retail-side at apply time).
--   requested_key  the stable requested key for creates (ProductType code);
--                  NULL for entity commands.
--   result_entity_id the actual Retail-minted entity ID, set when a target
--                  reports APPLIED (first writer wins); NULL until then.
--                  Intent rows are never rewritten: requested_* is immutable.
--   entity_id      keeps NOT NULL. For target_kind='entity' it is the
--                  business UUID (unchanged invariant). For 'create' it is
--                  the EXPLICIT ABSENT marker '' — never a fake business
--                  UUID. No reader may present '' as a ProductType ID.

ALTER TABLE catalog_admin_commands ADD COLUMN target_kind TEXT NOT NULL DEFAULT 'entity';
ALTER TABLE catalog_admin_commands ADD COLUMN requested_key TEXT NULL;
ALTER TABLE catalog_admin_commands ADD COLUMN result_entity_id TEXT NULL;

-- Coherence: entity commands carry a business UUID and no requested key;
-- creates carry the absent marker, a requested key, and (once applied) the
-- real result identity.
ALTER TABLE catalog_admin_commands DROP CONSTRAINT catalog_admin_commands_command_type_check;
ALTER TABLE catalog_admin_commands ADD CONSTRAINT catalog_admin_commands_command_type_check CHECK (command_type IN (
    'catalog.product.details.update.v1',
    'catalog.product.online-policy.update.v1',
    'catalog.product.classification.update.v1',
    'catalog.category.details.update.v1',
    'catalog.category.parents.update.v1',
    'catalog.category.online-policy.update.v1',
    'catalog.tag.details.update.v1',
    'catalog.product.configurations.update.v1',
    'catalog.product.variants.update.v1',
    'catalog.product.variant.update.v1',
    'catalog.variant.attributes.update.v1',
    'catalog.product-type.create.v1',
    'catalog.product-type.details.update.v1',
    'catalog.product-type.dimensions.update.v1',
    'catalog.product-type.capabilities.update.v1',
    'catalog.product-type.status.update.v1',
    'catalog.product.type.assign.v1'
));
-- Replace the inline entity_id 1..64 check (auto-named
-- catalog_admin_commands_entity_id_check) with the coherence rule below:
-- entity commands keep the 1..64 business-UUID invariant; creates carry
-- the explicit absent marker '' (never a fake business UUID).
ALTER TABLE catalog_admin_commands DROP CONSTRAINT catalog_admin_commands_entity_id_check;
ALTER TABLE catalog_admin_commands ADD CONSTRAINT catalog_admin_commands_create_identity_check CHECK (
    (target_kind = 'entity' AND char_length(entity_id) BETWEEN 1 AND 64 AND requested_key IS NULL)
    OR (target_kind = 'create' AND entity_id = '' AND requested_key IS NOT NULL
        AND char_length(requested_key) BETWEEN 1 AND 64
        AND (result_entity_id IS NULL OR char_length(result_entity_id) BETWEEN 1 AND 64))
);
ALTER TABLE catalog_admin_commands DROP CONSTRAINT IF EXISTS catalog_admin_commands_target_kind_check;
ALTER TABLE catalog_admin_commands ADD CONSTRAINT catalog_admin_commands_target_kind_check CHECK (
    target_kind IN ('entity', 'create')
);

-- +goose Down
-- Development/test repair only. Never roll back in production.
-- Create-kind rows cannot survive the downgrade (their '' entity marker
-- violates the restored invariant), so they are removed first; entity
-- commands are preserved.
DELETE FROM catalog_admin_command_targets WHERE command_id IN (
    SELECT id FROM catalog_admin_commands WHERE target_kind = 'create');
DELETE FROM catalog_admin_commands WHERE target_kind = 'create';
ALTER TABLE catalog_admin_commands DROP CONSTRAINT IF EXISTS catalog_admin_commands_create_identity_check;
ALTER TABLE catalog_admin_commands DROP CONSTRAINT IF EXISTS catalog_admin_commands_target_kind_check;
ALTER TABLE catalog_admin_commands DROP CONSTRAINT catalog_admin_commands_command_type_check;
ALTER TABLE catalog_admin_commands ADD CONSTRAINT catalog_admin_commands_command_type_check CHECK (command_type IN (
    'catalog.product.details.update.v1',
    'catalog.product.online-policy.update.v1',
    'catalog.product.classification.update.v1',
    'catalog.category.details.update.v1',
    'catalog.category.parents.update.v1',
    'catalog.category.online-policy.update.v1',
    'catalog.tag.details.update.v1',
    'catalog.product.configurations.update.v1',
    'catalog.product.variants.update.v1',
    'catalog.product.variant.update.v1',
    'catalog.variant.attributes.update.v1',
    'catalog.product-type.create.v1',
    'catalog.product-type.details.update.v1',
    'catalog.product-type.dimensions.update.v1',
    'catalog.product-type.capabilities.update.v1',
    'catalog.product-type.status.update.v1',
    'catalog.product.type.assign.v1'
));
ALTER TABLE catalog_admin_commands ADD CONSTRAINT catalog_admin_commands_entity_check CHECK (
    char_length(entity_id) BETWEEN 1 AND 64
);
ALTER TABLE catalog_admin_commands DROP COLUMN IF EXISTS result_entity_id;
ALTER TABLE catalog_admin_commands DROP COLUMN IF EXISTS requested_key;
ALTER TABLE catalog_admin_commands DROP COLUMN IF EXISTS target_kind;
