-- +goose Up
-- Durable send-before-write evidence. No FK to rebuildable projections.
-- Successful acknowledged requests remove their active barrier; uncertainty
-- survives cancellation/restart and operator resolutions retain audit history.
CREATE TABLE commerce_product_mutation_barriers (
    operation_id UUID PRIMARY KEY,
    provider_key TEXT NOT NULL CHECK (provider_key ~ '^[a-z0-9][a-z0-9_-]{0,63}$'),
    product_id UUID NOT NULL,
    request_fingerprint TEXT NOT NULL CHECK (request_fingerprint ~ '^[0-9a-f]{64}$'),
    state TEXT NOT NULL CHECK (state IN ('in_flight', 'uncertain', 'resolved')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    resolved_at TIMESTAMPTZ,
    resolution TEXT CHECK (resolution IN ('remote_completed', 'remote_not_applied')),
    CHECK ((state = 'resolved' AND resolved_at IS NOT NULL AND resolution IS NOT NULL)
        OR (state <> 'resolved' AND resolved_at IS NULL AND resolution IS NULL))
);
CREATE UNIQUE INDEX idx_commerce_mutation_barrier_active
    ON commerce_product_mutation_barriers (provider_key, product_id)
    WHERE state IN ('in_flight', 'uncertain');
CREATE INDEX idx_commerce_mutation_barrier_history
    ON commerce_product_mutation_barriers (provider_key, product_id, created_at DESC, operation_id DESC);

-- +goose Down
-- Never destroy active uncertainty evidence during rollback. Resolved audit
-- history must also be preserved; archive/restore explicitly before downgrade.
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM commerce_product_mutation_barriers) THEN
        RAISE EXCEPTION 'commerce mutation barrier history prevents rollback';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE commerce_product_mutation_barriers;
