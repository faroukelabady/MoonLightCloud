-- +goose Up
-- Request intent, retry schedule and lease ownership are independent.
-- Existing next_attempt_at values are retained: old due-time leases/backoff
-- become safely claimable at their existing horizon, with no work discarded.
ALTER TABLE commerce_product_reevaluations
 ADD COLUMN requested_generation BIGINT NOT NULL DEFAULT 1 CHECK (requested_generation > 0),
 ADD COLUMN claimed_generation BIGINT,
 ADD COLUMN lease_generation BIGINT NOT NULL DEFAULT 0 CHECK (lease_generation >= 0),
 ADD COLUMN lease_token UUID,
 ADD COLUMN lease_until TIMESTAMPTZ,
 ADD CONSTRAINT commerce_reeval_claim CHECK (
  (lease_token IS NULL AND lease_until IS NULL AND claimed_generation IS NULL) OR
  (lease_token IS NOT NULL AND lease_until IS NOT NULL AND claimed_generation IS NOT NULL AND claimed_generation > 0
   AND claimed_generation <= requested_generation AND lease_generation > 0)
 );

-- +goose Down
-- Downgrading with durable work would reintroduce unfenced acknowledgments.
-- +goose StatementBegin
DO $$ BEGIN
 LOCK TABLE commerce_product_reevaluations IN ACCESS EXCLUSIVE MODE;
 IF EXISTS (SELECT 1 FROM commerce_product_reevaluations) THEN
  RAISE EXCEPTION 'refusing rollback: commerce reevaluation work requires fenced claims';
 END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE commerce_product_reevaluations
 DROP CONSTRAINT commerce_reeval_claim,
 DROP COLUMN lease_until, DROP COLUMN lease_token, DROP COLUMN lease_generation,
 DROP COLUMN claimed_generation, DROP COLUMN requested_generation;
