-- name: GetBusinessSettings :one
SELECT tax_enabled, discount_enabled, returns_enabled, revision, updated_at
FROM business_settings
WHERE id;

-- Optimistic update: no row comes back when the revision moved on.
-- name: UpdateBusinessSettings :many
UPDATE business_settings
SET tax_enabled = @tax_enabled, discount_enabled = @discount_enabled,
    returns_enabled = @returns_enabled, revision = revision + 1,
    updated_by = sqlc.narg('updated_by')::uuid, updated_at = now()
WHERE id AND revision = @revision
RETURNING tax_enabled, discount_enabled, returns_enabled, revision, updated_at;

-- name: ListExchangeRates :many
SELECT id, rate_microrate, effective_at, note, source, created_at
FROM exchange_rates
WHERE base = 'USD' AND quote = 'EGP'
ORDER BY effective_at DESC, id
LIMIT @row_limit;

-- name: InsertExchangeRate :exec
INSERT INTO exchange_rates (id, base, quote, rate_microrate, effective_at, note, source, created_by)
VALUES (@id, 'USD', 'EGP', @rate_microrate, @effective_at, sqlc.narg('note'), @source, sqlc.narg('created_by')::uuid);

-- One Cloud instance at a time records a fetched rate (ADR-0058).
-- name: LockExchangeRateFetch :exec
SELECT pg_advisory_xact_lock(hashtext('exchange_rates.fetch'));

-- A fetched rate starts at the database clock read after the lock, so
-- instances whose own clocks disagree still record one row per change. No
-- row is added when the rate in effect then has the same value.
-- name: InsertFetchedExchangeRate :execrows
INSERT INTO exchange_rates (id, base, quote, rate_microrate, effective_at, source)
SELECT @id, 'USD', 'EGP', @rate_microrate::bigint, clock_timestamp(), @source
WHERE @rate_microrate::bigint IS DISTINCT FROM (
    SELECT x.rate_microrate FROM exchange_rates x
    WHERE x.base = 'USD' AND x.quote = 'EGP' AND x.effective_at <= clock_timestamp()
    ORDER BY x.effective_at DESC, x.id
    LIMIT 1);

-- name: DeleteExchangeRate :execrows
DELETE FROM exchange_rates WHERE id = @id;
