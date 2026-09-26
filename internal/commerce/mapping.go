package commerce

import (
	"context"
	"time"
)

// ProductMapping is durable Cloud-only integration state: one MoonLight
// product ↔ one external provider product. It is NOT a derived catalog
// projection: it survives catalog/policy/inventory rebuilds and requires
// real database backup. No credentials or secrets ever live here, only
// identifiers.
type ProductMapping struct {
	ProviderKey       ProviderKey
	ProductID         string
	ExternalProductID string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// ProductMappingRepository persists provider product mappings with
// explicit mutation semantics. Same-pair creation is idempotent;
// conflicting remaps fail instead of silently overwriting.
type ProductMappingRepository interface {
	// GetProductMapping returns the mapping for one provider product.
	GetProductMapping(ctx context.Context, providerKey ProviderKey, productID string) (ProductMapping, error)
	// FindByExternalProductID returns the mapping holding one external ID.
	FindByExternalProductID(ctx context.Context, providerKey ProviderKey, externalID string) (ProductMapping, error)
	// CreateProductMapping persists one mapping. Creating the exact same
	// pair is idempotent; a different external ID for the same pair, or
	// the same external ID for a different product, is a mapping
	// conflict. PostgreSQL uniqueness constraints are the backstop.
	CreateProductMapping(ctx context.Context, providerKey ProviderKey, productID, externalID string) (ProductMapping, error)
}
