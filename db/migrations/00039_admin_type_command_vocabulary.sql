-- +goose Up
-- 00039_admin_type_command_vocabulary: Phase 17-R2 ProductType command
-- vocabulary (ADR-0050 §45/§46). Extends the 00032/00033 CHECK list with
-- the six type commands. Append-only vocabulary extension (same pattern
-- as 00033): recreate the CHECK, never edit applied history.

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

-- +goose Down
-- Development/test repair only. Never roll back in production.
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
    'catalog.variant.attributes.update.v1'
));
