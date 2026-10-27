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
SELECT id, rate_microrate, effective_at, note, created_at
FROM exchange_rates
WHERE base = 'USD' AND quote = 'EGP'
ORDER BY effective_at DESC, id
LIMIT @row_limit;

-- name: InsertExchangeRate :exec
INSERT INTO exchange_rates (id, base, quote, rate_microrate, effective_at, note, created_by)
VALUES (@id, 'USD', 'EGP', @rate_microrate, @effective_at, sqlc.narg('note'), sqlc.narg('created_by')::uuid);

-- name: DeleteExchangeRate :execrows
DELETE FROM exchange_rates WHERE id = @id;
