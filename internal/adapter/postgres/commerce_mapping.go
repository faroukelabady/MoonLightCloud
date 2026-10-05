package postgres

import (
	"context"
	"errors"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Commerce mapping repository on Devices (shared pool + timeouts).
// Mappings are durable integration state, never derived projections:
// no rebuild path touches this table.

func commerceMappingFromRow(providerKey string, productID pgtype.UUID, external string, store pgtype.UUID, createdAt, updatedAt pgtype.Timestamptz) commerce.ProductMapping {
	return commerce.ProductMapping{
		ProviderKey: commerce.ProviderKey(providerKey),
		ProductID:   uuidString(productID), ExternalProductID: external,
		StoreID:   storeString(store),
		CreatedAt: createdAt.Time, UpdatedAt: updatedAt.Time,
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
	return commerceMappingFromRow(row.ProviderKey, row.ProductID, row.ExternalProductID, row.StoreID, row.CreatedAt, row.UpdatedAt), nil
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
	return commerceMappingFromRow(row.ProviderKey, row.ProductID, row.ExternalProductID, row.StoreID, row.CreatedAt, row.UpdatedAt), nil
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
	// Phase 9C authority: the mapping mirrors its authoritative catalog
	// product. A missing product yields a legacy NULL mapping (mappings
	// may predate catalog projection); ownership is never manufactured.
	var productStore pgtype.UUID
	if prow, err := q.CatalogProductByID(ctx, puid); err == nil {
		productStore = prow.StoreID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return commerce.ProductMapping{}, apperr.Wrap(apperr.Internal, "commerce mapping", redact(err))
	}
	row, err := q.CreateCommerceProductMapping(ctx, sqlcgen.CreateCommerceProductMappingParams{
		ProviderKey: string(providerKey), ProductID: puid, ExternalProductID: externalID,
		StoreID: productStore,
	})
	if err == nil {
		return commerceMappingFromRow(row.ProviderKey, row.ProductID, row.ExternalProductID, row.StoreID, row.CreatedAt, row.UpdatedAt), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return commerce.ProductMapping{}, apperr.Wrap(apperr.Internal, "commerce mapping", redact(err))
	}
	// A conflicting row won the race (or already existed): classify.
	if existing, readErr := q.GetCommerceProductMapping(ctx, sqlcgen.GetCommerceProductMappingParams{
		ProviderKey: string(providerKey), ProductID: puid,
	}); readErr == nil {
		if existing.ExternalProductID == externalID {
			// Same-pair idempotent replay. A legacy NULL mapping adopts
			// the proven product Store atomically; an owned mapping is
			// returned as-is (adoption is one-way and deterministic).
			if !existing.StoreID.Valid && productStore.Valid {
				if adopted, aerr := q.AdoptCommerceProductMappingStore(ctx, sqlcgen.AdoptCommerceProductMappingStoreParams{
					ProviderKey: string(providerKey), ProductID: puid, StoreID: productStore,
				}); aerr == nil {
					return commerceMappingFromRow(adopted.ProviderKey, adopted.ProductID, adopted.ExternalProductID, adopted.StoreID, adopted.CreatedAt, adopted.UpdatedAt), nil
				}
				// Lost a concurrent adoption (or the row changed under
				// us): re-read the winner instead of returning stale
				// state; conflicts below still apply.
				if fresh, ferr := q.GetCommerceProductMapping(ctx, sqlcgen.GetCommerceProductMappingParams{
					ProviderKey: string(providerKey), ProductID: puid,
				}); ferr == nil {
					existing = fresh
				} else if !errors.Is(ferr, pgx.ErrNoRows) {
					return commerce.ProductMapping{}, apperr.Wrap(apperr.Internal, "commerce mapping", redact(ferr))
				}
				if existing.ExternalProductID != externalID {
					return commerce.ProductMapping{}, apperr.New(apperr.Conflict,
						"commerce mapping conflict: product already maps to a different external id")
				}
			}
			if existing.StoreID.Valid && productStore.Valid &&
				uuidString(existing.StoreID) != uuidString(productStore) {
				return commerce.ProductMapping{}, apperr.New(apperr.Conflict,
					"STORE_SCOPE_CONFLICT: commerce mapping owned by another store")
			}
			return commerceMappingFromRow(existing.ProviderKey, existing.ProductID, existing.ExternalProductID, existing.StoreID, existing.CreatedAt, existing.UpdatedAt), nil
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

// ListProductMappingsForStore enumerates one Store's mappings for one
// provider. No global list exists, so there is no legacy behavior to
// preserve: administration uses provider-keyed point reads.
func (d Devices) ListProductMappingsForStore(ctx context.Context, providerKey commerce.ProviderKey, storeID string) ([]commerce.ProductMapping, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	if _, err := commerce.ValidateProviderKey(string(providerKey)); err != nil {
		return nil, apperr.New(apperr.InvalidInput, err.Error())
	}
	suid, err := parseUUID(storeID)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "store_id must be a UUID")
	}
	rows, err := sqlcgen.New(d.pool).ListCommerceProductMappingsForStore(ctx, sqlcgen.ListCommerceProductMappingsForStoreParams{
		ProviderKey: string(providerKey), StoreID: suid,
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "commerce mapping", redact(err))
	}
	out := make([]commerce.ProductMapping, 0, len(rows))
	for _, row := range rows {
		out = append(out, commerceMappingFromRow(row.ProviderKey, row.ProductID, row.ExternalProductID, row.StoreID, row.CreatedAt, row.UpdatedAt))
	}
	return out, nil
}

// Phase 15 configuration identity (§61/§62): durable, Store-scoped,
// ownership-keyed by (provider, product, configuration). Never matched
// by labels or SKU. Rows survive disable/re-enable (§63).

func (d Devices) GetProductConfigurationMapping(ctx context.Context, providerKey commerce.ProviderKey, productID, configurationID string) (commerce.ProductConfigurationMapping, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	puid, err := parseUUID(productID)
	if err != nil {
		return commerce.ProductConfigurationMapping{}, err
	}
	cuid, err := parseUUID(configurationID)
	if err != nil {
		return commerce.ProductConfigurationMapping{}, err
	}
	row, err := sqlcgen.New(d.pool).GetCommerceProductConfigurationMapping(ctx, sqlcgen.GetCommerceProductConfigurationMappingParams{
		ProviderKey: string(providerKey), ProductID: puid, ConfigurationID: cuid,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return commerce.ProductConfigurationMapping{}, apperr.New(apperr.NotFound, "no commerce configuration mapping")
		}
		return commerce.ProductConfigurationMapping{}, apperr.Wrap(apperr.Internal, "commerce configuration mapping", redact(err))
	}
	return configurationMappingFromRow(row), nil
}

func (d Devices) FindConfigurationByExternal(ctx context.Context, providerKey commerce.ProviderKey, externalProductID, externalConfigurationID string) (commerce.ProductConfigurationMapping, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	row, err := sqlcgen.New(d.pool).FindCommerceProductConfigurationMappingByExternal(ctx, sqlcgen.FindCommerceProductConfigurationMappingByExternalParams{
		ProviderKey: string(providerKey), ExternalProductID: externalProductID, ExternalConfigurationID: externalConfigurationID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return commerce.ProductConfigurationMapping{}, apperr.New(apperr.NotFound, "no commerce configuration mapping")
		}
		return commerce.ProductConfigurationMapping{}, apperr.Wrap(apperr.Internal, "commerce configuration mapping", redact(err))
	}
	return configurationMappingFromRow(row), nil
}

func (d Devices) UpsertProductConfigurationMapping(ctx context.Context, providerKey commerce.ProviderKey, productID, configurationID, externalProductID, externalConfigurationID string) (commerce.ProductConfigurationMapping, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	puid, err := parseUUID(productID)
	if err != nil {
		return commerce.ProductConfigurationMapping{}, err
	}
	cuid, err := parseUUID(configurationID)
	if err != nil {
		return commerce.ProductConfigurationMapping{}, err
	}
	row, err := sqlcgen.New(d.pool).UpsertCommerceProductConfigurationMapping(ctx, sqlcgen.UpsertCommerceProductConfigurationMappingParams{
		ProviderKey: string(providerKey), ProductID: puid, ConfigurationID: cuid,
		ExternalProductID: externalProductID, ExternalConfigurationID: externalConfigurationID,
	})
	if err != nil {
		return commerce.ProductConfigurationMapping{}, redact(err)
	}
	return configurationMappingFromRow(row), nil
}

func configurationMappingFromRow(row sqlcgen.CommerceProductConfigurationMapping) commerce.ProductConfigurationMapping {
	mapping := commerce.ProductConfigurationMapping{
		ProviderKey: commerce.ProviderKey(row.ProviderKey),
		ProductID:   uuidString(row.ProductID), ConfigurationID: uuidString(row.ConfigurationID),
		ExternalProductID: row.ExternalProductID, ExternalConfigurationID: row.ExternalConfigurationID,
	}
	if row.StoreID.Valid {
		value := uuidString(row.StoreID)
		mapping.StoreID = &value
	}
	return mapping
}

// UpdateProductMappingExternal is the Phase 15-R1 F07 (§116) documented
// simple→framed sellable-identity transition: compare-and-set, never a
// blind overwrite.
func (d Devices) UpdateProductMappingExternal(ctx context.Context, providerKey commerce.ProviderKey, productID, expectedExternalID, newExternalID string) (commerce.ProductMapping, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	if expectedExternalID == newExternalID {
		return d.GetProductMapping(ctx, providerKey, productID)
	}
	puid, err := parseUUID(productID)
	if err != nil {
		return commerce.ProductMapping{}, apperr.New(apperr.InvalidInput, "product_id must be a UUID")
	}
	row, err := sqlcgen.New(d.pool).UpdateCommerceProductMappingExternal(ctx, sqlcgen.UpdateCommerceProductMappingExternalParams{
		ProviderKey: string(providerKey), ProductID: puid,
		ExternalProductID: expectedExternalID, ExternalProductID_2: newExternalID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return commerce.ProductMapping{}, apperr.New(apperr.Conflict, "commerce mapping changed concurrently")
		}
		return commerce.ProductMapping{}, apperr.Wrap(apperr.Internal, "commerce mapping transition", redact(err))
	}
	return commerceMappingFromRow(row.ProviderKey, row.ProductID, row.ExternalProductID, row.StoreID, row.CreatedAt, row.UpdatedAt), nil
}
