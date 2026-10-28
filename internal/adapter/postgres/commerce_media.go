package postgres

import (
	"context"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// ADR-0056 step 2: provider media MoonLight created (commerce_media_mappings).
// Durable integration state like the other commerce mappings: no projection
// rebuild touches it.

// ListProductMediaMappings returns the recorded media of one Product.
func (d CommercePersistence) ListProductMediaMappings(ctx context.Context, providerKey commerce.ProviderKey, productID string) ([]commerce.MediaMapping, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	if _, err := commerce.ValidateProviderKey(string(providerKey)); err != nil {
		return nil, apperr.New(apperr.InvalidInput, err.Error())
	}
	puid, err := parseUUID(productID)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "product_id must be a UUID")
	}
	rows, err := sqlcgen.New(d.pool).ListCommerceMediaMappings(ctx, sqlcgen.ListCommerceMediaMappingsParams{
		ProviderKey: string(providerKey), ProductID: puid,
	})
	if err != nil {
		return nil, reportErr("commerce media mapping", err)
	}
	out := make([]commerce.MediaMapping, 0, len(rows))
	for _, row := range rows {
		out = append(out, commerce.MediaMapping{
			SHA256: row.Sha256, ExternalProductID: row.ExternalProductID, ExternalMediaID: row.ExternalMediaID,
		})
	}
	return out, nil
}

// CreateProductMediaMapping records one created media item. Recording the
// same row again is idempotent; a different media item for the same file is
// a conflict, never a silent remap. The Store is the projected Product's.
func (d CommercePersistence) CreateProductMediaMapping(ctx context.Context, providerKey commerce.ProviderKey, productID string, mapping commerce.MediaMapping) error {
	if _, err := commerce.ValidateProviderKey(string(providerKey)); err != nil {
		return apperr.New(apperr.InvalidInput, err.Error())
	}
	puid, err := parseUUID(productID)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "product_id must be a UUID")
	}
	insertCtx, cancel := d.ctx(ctx)
	rows, err := sqlcgen.New(d.pool).InsertCommerceMediaMapping(insertCtx, sqlcgen.InsertCommerceMediaMappingParams{
		ProviderKey: string(providerKey), ProductID: puid, Sha256: mapping.SHA256,
		ExternalProductID: mapping.ExternalProductID, ExternalMediaID: mapping.ExternalMediaID,
	})
	cancel()
	if err != nil {
		if isUniqueViolation(err) {
			return apperr.New(apperr.Conflict, "commerce media mapping conflict")
		}
		return reportErr("commerce media mapping", err)
	}
	if rows == 1 {
		return nil
	}
	existing, err := d.ListProductMediaMappings(ctx, providerKey, productID)
	if err != nil {
		return err
	}
	for _, row := range existing {
		if row == mapping {
			return nil
		}
	}
	if len(existing) == 0 {
		return apperr.New(apperr.NotFound, "product not projected")
	}
	return apperr.New(apperr.Conflict, "commerce media mapping conflict")
}

// DeleteProductMediaMapping forgets one media item. A row that now names
// other media is left alone; a missing row is already forgotten.
func (d CommercePersistence) DeleteProductMediaMapping(ctx context.Context, providerKey commerce.ProviderKey, productID string, mapping commerce.MediaMapping) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	if _, err := commerce.ValidateProviderKey(string(providerKey)); err != nil {
		return apperr.New(apperr.InvalidInput, err.Error())
	}
	puid, err := parseUUID(productID)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "product_id must be a UUID")
	}
	if _, err := sqlcgen.New(d.pool).DeleteCommerceMediaMapping(ctx, sqlcgen.DeleteCommerceMediaMappingParams{
		ProviderKey: string(providerKey), ProductID: puid, Sha256: mapping.SHA256, ExternalMediaID: mapping.ExternalMediaID,
	}); err != nil {
		return reportErr("commerce media mapping", err)
	}
	return nil
}
