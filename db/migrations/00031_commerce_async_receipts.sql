-- +goose Up
-- Keep acknowledged asynchronous work alongside its original request evidence.
-- No FK to rebuildable catalog state and no changes to historical payloads.
ALTER TABLE commerce_product_mutation_barriers
    ADD COLUMN async_role TEXT CHECK (async_role IN ('bundle_create','bundle_update')),
    ADD COLUMN async_intent TEXT CHECK (async_intent ~ '^[0-9a-f]{64}$'),
    ADD COLUMN provider_operation_id TEXT CHECK (length(provider_operation_id) BETWEEN 1 AND 256),
    ADD COLUMN async_state TEXT CHECK (async_state IN ('pending','completed','failed')),
    ADD COLUMN async_product_id TEXT,
    ADD CONSTRAINT commerce_async_receipt_complete CHECK (
        (async_role IS NULL AND async_intent IS NULL AND provider_operation_id IS NULL AND async_state IS NULL AND async_product_id IS NULL)
        OR (async_role IS NOT NULL AND async_intent IS NOT NULL AND provider_operation_id IS NOT NULL AND async_state IS NOT NULL AND state = 'resolved')
    );
CREATE UNIQUE INDEX idx_commerce_async_pending
    ON commerce_product_mutation_barriers(provider_key, product_id)
    WHERE async_state = 'pending';
CREATE INDEX idx_commerce_async_history
    ON commerce_product_mutation_barriers(provider_key, product_id, async_role, created_at DESC, operation_id DESC)
    WHERE async_role IS NOT NULL;

-- +goose Down
-- Downgrade must not destroy receipts, including completed adoption provenance.
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM commerce_product_mutation_barriers WHERE async_role IS NOT NULL) THEN
        RAISE EXCEPTION 'asynchronous receipt history prevents rollback';
    END IF;
END $$;
-- +goose StatementEnd
DROP INDEX idx_commerce_async_pending;
DROP INDEX idx_commerce_async_history;
ALTER TABLE commerce_product_mutation_barriers
    DROP CONSTRAINT commerce_async_receipt_complete,
    DROP COLUMN async_role, DROP COLUMN async_intent,
    DROP COLUMN provider_operation_id, DROP COLUMN async_state, DROP COLUMN async_product_id;
