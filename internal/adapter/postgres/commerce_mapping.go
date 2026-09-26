package postgres

import (
	"context"
	"errors"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/jackc/pgx/v5"
)

// Commerce mapping repository on Devices (shared pool + timeouts).
// Mappings are durable integration state, never derived projections:
// no rebuild path touches this table.

func commerceMappingFromRow(row sqlcgen.CommerceProductMapping) commerce.ProductMapping {
	return commerce.ProductMapping{
		ProviderKey: commerce.ProviderKey(row.ProviderKey),
		ProductID:   uuidString(row.ProductID), ExternalProductID: row.ExternalProductID,
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
}

// GetCommerceProductMapping returns one provider product mapping.
func (d Devices) GetProductMapping(ctx context.Context, providerKey commerce.ProviderKey, productID string) (commerce.ProductMapping, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	if _, err := commerce.ValidateProviderKey(string(providerKey)); err != nil {
		return commerce.ProductMapping{}, apperr.New(apperr.InvalidInput, err.Error())
	}
	puid, err := parseUUID(productID)
	if err != nil {
		return commerce.ProductMapping{}, apperr.New(apperr.InvalidInput, "product_id must be a UUID")
	}
	row, err := sqlcgen.New(d.pool).GetCommerceProductMapping(ctx, sqlcgen.GetCommerceProductMappingParams{
		ProviderKey: string(providerKey), ProductID: puid,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return commerce.ProductMapping{}, apperr.New(apperr.NotFound, "no commerce mapping")
		}
		return commerce.ProductMapping{}, apperr.Wrap(apperr.Internal, "commerce mapping", redact(err))
	}
	return commerceMappingFromRow(row), nil
}

// FindCommerceProductMappingByExternal returns the mapping holding one
// external product identity.
func (d Devices) FindByExternalProductID(ctx context.Context, providerKey commerce.ProviderKey, externalID string) (commerce.ProductMapping, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	if _, err := commerce.ValidateProviderKey(string(providerKey)); err != nil {
		return commerce.ProductMapping{}, apperr.New(apperr.InvalidInput, err.Error())
	}
	if len(externalID) == 0 || len(externalID) > 200 {
		return commerce.ProductMapping{}, apperr.New(apperr.InvalidInput, "external product id must be 1..200 characters")
	}
	row, err := sqlcgen.New(d.pool).FindCommerceProductMappingByExternal(ctx, sqlcgen.FindCommerceProductMappingByExternalParams{
		ProviderKey: string(providerKey), ExternalProductID: externalID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return commerce.ProductMapping{}, apperr.New(apperr.NotFound, "no commerce mapping")
		}
		return commerce.ProductMapping{}, apperr.Wrap(apperr.Internal, "commerce mapping", redact(err))
	}
	return commerceMappingFromRow(row), nil
}

// CreateCommerceProductMapping persists one mapping. The exact same pair
// is idempotent; a different external ID for the same pair, or the same
// external ID for a different product, is a typed conflict. Uniqueness
// constraints are the backstop under concurrency: the insert wins or the
// read-back classifies, so concurrent same-pair attempts resolve
// idempotently and conflicting attempts never silently overwrite.
func (d Devices) CreateProductMapping(ctx context.Context, providerKey commerce.ProviderKey, productID, externalID string) (commerce.ProductMapping, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	if _, err := commerce.ValidateProviderKey(string(providerKey)); err != nil {
		return commerce.ProductMapping{}, apperr.New(apperr.InvalidInput, err.Error())
	}
	puid, err := parseUUID(productID)
	if err != nil {
		return commerce.ProductMapping{}, apperr.New(apperr.InvalidInput, "product_id must be a UUID")
	}
	if len(externalID) == 0 || len(externalID) > 200 {
		return commerce.ProductMapping{}, apperr.New(apperr.InvalidInput, "external product id must be 1..200 characters")
	}
	q := sqlcgen.New(d.pool)
	row, err := q.CreateCommerceProductMapping(ctx, sqlcgen.CreateCommerceProductMappingParams{
		ProviderKey: string(providerKey), ProductID: puid, ExternalProductID: externalID,
	})
	if err == nil {
		return commerceMappingFromRow(row), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return commerce.ProductMapping{}, apperr.Wrap(apperr.Internal, "commerce mapping", redact(err))
	}
	// A conflicting row won the race (or already existed): classify.
	if existing, readErr := q.GetCommerceProductMapping(ctx, sqlcgen.GetCommerceProductMappingParams{
		ProviderKey: string(providerKey), ProductID: puid,
	}); readErr == nil {
		if existing.ExternalProductID == externalID {
			return commerceMappingFromRow(existing), nil
		}
		return commerce.ProductMapping{}, apperr.New(apperr.Conflict,
			"commerce mapping conflict: product already maps to a different external id")
	} else if !errors.Is(readErr, pgx.ErrNoRows) {
		return commerce.ProductMapping{}, apperr.Wrap(apperr.Internal, "commerce mapping", redact(readErr))
	}
	if existing, readErr := q.FindCommerceProductMappingByExternal(ctx, sqlcgen.FindCommerceProductMappingByExternalParams{
		ProviderKey: string(providerKey), ExternalProductID: externalID,
	}); readErr == nil {
		_ = existing
		return commerce.ProductMapping{}, apperr.New(apperr.Conflict,
			"commerce mapping conflict: external id already maps to a different product")
	} else if !errors.Is(readErr, pgx.ErrNoRows) {
		return commerce.ProductMapping{}, apperr.Wrap(apperr.Internal, "commerce mapping", redact(readErr))
	}
	return commerce.ProductMapping{}, apperr.Wrap(apperr.Internal, "commerce mapping", redact(err))
}

var _ commerce.ProductMappingRepository = Devices{}
