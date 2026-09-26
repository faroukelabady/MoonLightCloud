package commerce

import (
	"context"
	"errors"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

// CatalogReader is the narrow frozen-state boundary the commerce
// assembler reads through. Adapters never touch sqlc, PostgreSQL, or
// projection tables directly; catalog.Service satisfies this interface.
type CatalogReader interface {
	GetProduct(ctx context.Context, id string) (catalog.Product, error)
	GetCategory(ctx context.Context, id string) (catalog.Category, error)
	GetTag(ctx context.Context, id string) (catalog.Tag, error)
	GetProductSalesPolicy(ctx context.Context, id string) (catalog.ProductSalesPolicy, error)
	GetProductAvailability(ctx context.Context, id string) (catalog.ProductAvailability, error)
}

// DesiredProduct is the provider-neutral desired state for one MoonLight
// product: the assembled product snapshot, whether it should be actively
// published online (is_active AND sell_online), and the Phase 5C derived
// availability with source revisions.
type DesiredProduct struct {
	Product           CommerceProduct
	Published         bool
	Availability      catalog.ProductAvailability
	CatalogRevision   int64
	PolicyRevision    int64
	InventoryRevision int64
}

// CommerceProductSource assembles desired provider-neutral state from
// frozen catalog, policy, and availability services. A missing core
// product is a typed not-ready failure; the caller retains any mapping.
type CommerceProductSource interface {
	GetDesiredCommerceProduct(ctx context.Context, productID string) (DesiredProduct, error)
}

// CatalogCommerceSource implements CommerceProductSource over a
// CatalogReader. It calls the frozen Phase 5C availability service for
// quantities; the availability formula lives in exactly one place.
type CatalogCommerceSource struct {
	reader CatalogReader
}

// NewCatalogCommerceSource wires the assembler over frozen reads.
func NewCatalogCommerceSource(reader CatalogReader) *CatalogCommerceSource {
	return &CatalogCommerceSource{reader: reader}
}

// GetDesiredCommerceProduct composes product, policy, and availability
// into one provider-neutral desired state.
func (s *CatalogCommerceSource) GetDesiredCommerceProduct(ctx context.Context, productID string) (DesiredProduct, error) {
	product, err := s.reader.GetProduct(ctx, productID)
	if err != nil {
		return DesiredProduct{}, err
	}
	policy, policyErr := s.reader.GetProductSalesPolicy(ctx, productID)
	if policyErr != nil && !isNotFound(policyErr) {
		return DesiredProduct{}, policyErr
	}
	policyFound := policyErr == nil
	availability, err := s.reader.GetProductAvailability(ctx, productID)
	if err != nil {
		return DesiredProduct{}, err
	}
	assembled := CommerceProduct{
		ProductID:       product.ID,
		SKU:             product.SKU,
		IsActive:        product.IsActive,
		WidthCM:         product.WidthCM,
		HeightCM:        product.HeightCM,
		CatalogRevision: product.Revision,
	}
	for _, translation := range product.Translations {
		assembled.Names = append(assembled.Names, LocalizedName{Locale: translation.Locale, Name: translation.Name})
		if translation.Description != nil {
			if assembled.Descriptions == nil {
				assembled.Descriptions = map[string]string{}
			}
			assembled.Descriptions[translation.Locale] = *translation.Description
		}
	}
	for _, price := range product.Prices {
		assembled.Prices = append(assembled.Prices, Money{
			Currency: price.Currency, AmountMinor: price.PriceCents, CostMinor: price.CostCents,
		})
	}
	if category, err := s.reader.GetCategory(ctx, product.TopCategoryID); err == nil {
		assembled.TopCategory = CategoryRef{ID: category.ID, NameAR: category.NameAR}
		if category.NameEN != nil {
			assembled.TopCategory.NameEN = *category.NameEN
		}
	} else if !isNotFound(err) {
		return DesiredProduct{}, err
	}
	for _, subID := range product.SubcategoryIDs {
		ref := CategoryRef{ID: subID}
		if category, err := s.reader.GetCategory(ctx, subID); err == nil {
			ref.NameAR = category.NameAR
			if category.NameEN != nil {
				ref.NameEN = *category.NameEN
			}
		} else if !isNotFound(err) {
			return DesiredProduct{}, err
		}
		assembled.Subcategories = append(assembled.Subcategories, ref)
	}
	for _, tagID := range product.TagIDs {
		ref := TagRef{ID: tagID}
		if tag, err := s.reader.GetTag(ctx, tagID); err == nil {
			ref.Slug = tag.Slug
			if tag.NameAR != nil {
				ref.NameAR = *tag.NameAR
			}
			if tag.NameEN != nil {
				ref.NameEN = *tag.NameEN
			}
		} else if !isNotFound(err) {
			return DesiredProduct{}, err
		}
		assembled.Tags = append(assembled.Tags, ref)
	}
	desired := DesiredProduct{
		Product:           assembled,
		Availability:      availability,
		CatalogRevision:   product.Revision,
		InventoryRevision: availability.InventoryRevision,
	}
	if policyFound {
		assembled.SellOnline = policy.SellOnline
		assembled.AllocationLimit = policy.OnlineAllocationLimit
		assembled.PolicyRevision = policy.Revision
		desired.PolicyRevision = policy.Revision
		desired.Product = assembled
	}
	// Publication requires both lifecycle and policy consent. A missing
	// policy can never publish: unknown online state defaults to disabled.
	desired.Published = product.IsActive && policyFound && policy.SellOnline
	return desired, nil
}

func isNotFound(err error) bool {
	var appErr *apperr.Error
	if errors.As(err, &appErr) {
		return appErr.Kind == apperr.NotFound
	}
	return false
}
