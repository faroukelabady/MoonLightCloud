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
	// StoreID is the proven Store of the authoritative MoonLight
	// product, nil for legacy mappings. Never sourced from provider
	// payloads; adopted only from product authority.
	StoreID   *string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ProductMappingRepository persists provider product mappings with
// explicit mutation semantics. Same-pair creation is idempotent;
// conflicting remaps fail instead of silently overwriting.
type ProductMappingRepository interface {
	// GetProductMapping returns the mapping for one provider product.
	GetProductMapping(ctx context.Context, providerKey ProviderKey, productID string) (ProductMapping, error)
	// Phase 15 configuration identity (§61/§62).
	GetProductConfigurationMapping(ctx context.Context, providerKey ProviderKey, productID, configurationID string) (ProductConfigurationMapping, error)
	FindConfigurationByExternal(ctx context.Context, providerKey ProviderKey, externalProductID, externalConfigurationID string) (ProductConfigurationMapping, error)
	UpsertProductConfigurationMapping(ctx context.Context, providerKey ProviderKey, productID, configurationID, externalProductID, externalConfigurationID string) (ProductConfigurationMapping, error)
	// FindByExternalProductID returns the mapping holding one external ID.
	FindByExternalProductID(ctx context.Context, providerKey ProviderKey, externalID string) (ProductMapping, error)
	// CreateProductMapping persists one mapping. Creating the exact same
	// pair is idempotent; a different external ID for the same pair, or
	// the same external ID for a different product, is a mapping
	// conflict. PostgreSQL uniqueness constraints are the backstop.
	CreateProductMapping(ctx context.Context, providerKey ProviderKey, productID, externalID string) (ProductMapping, error)
	// UpdateProductMappingExternal performs the documented Phase 15
	// simple→framed sellable-identity transition (§116): a
	// compare-and-set replace of the external identity that never
	// clobbers a concurrent different mapping.
	UpdateProductMappingExternal(ctx context.Context, providerKey ProviderKey, productID, expectedExternalID, newExternalID string) (ProductMapping, error)
	// Phase 17 ProductVariant mappings (commerce_product_variant_mappings).
	// GetProductVariantMapping returns the mapping for one variant.
	GetProductVariantMapping(ctx context.Context, providerKey ProviderKey, variantID string) (ProductVariantMapping, error)
	// ListProductVariantMappings returns every mapped variant of one
	// product for one provider instance (adapter reconciliation input).
	ListProductVariantMappings(ctx context.Context, providerKey ProviderKey, productID string) ([]ProductVariantMapping, error)
	// FindProductVariantMappingByExternal returns the mapping holding one
	// provider variation identity (order-line variant resolution and
	// mapping-loss recovery evidence).
	FindProductVariantMappingByExternal(ctx context.Context, providerKey ProviderKey, externalProductID, externalVariantID string) (ProductVariantMapping, error)
	// CreateProductVariantMapping persists one variant mapping. Same-pair
	// creation is idempotent; a different external variant for the same
	// variant, or the same external pair for a different variant, is a
	// mapping conflict (never a silent remap).
	CreateProductVariantMapping(ctx context.Context, providerKey ProviderKey, productID, variantID, externalProductID, externalVariantID string) (ProductVariantMapping, error)
}

// ProductConfigurationMapping is durable provider identity for one
// Product configuration choice (Phase 15 §61/§62). Ownership is proven
// by (provider, product, configuration) — labels and SKUs never do.
type ProductConfigurationMapping struct {
	ProviderKey             ProviderKey
	ProductID               string
	ConfigurationID         string
	ExternalProductID       string
	ExternalConfigurationID string
	StoreID                 *string
}

// Configuration mappings are durable through disable/re-enable (§63)
// and are the recovery evidence for mapping-loss and ambiguous-create
// handling (§64/§65).

// ProductVariantMapping is durable Cloud-only integration state
// (Phase 17): one MoonLight ProductVariant ↔ one provider
// variation/variant per provider instance, with the parent external
// product identity denormalized for provider round-trips. It survives
// catalog/policy/inventory rebuilds (no FK to rebuildable projections)
// and requires real database backup. No credentials ever live here.
type ProductVariantMapping struct {
	ProviderKey       ProviderKey
	ProductID         string
	VariantID         string
	ExternalProductID string
	ExternalVariantID string
	// StoreID is the proven Store of the authoritative MoonLight
	// product, nil for legacy/unproven mappings. Never sourced from
	// provider payloads; adopted only from product authority.
	StoreID   *string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Variant mappings survive disable/re-enable and are the recovery
// evidence for mapping-loss handling: an adapter re-adopts the owned
// remote variation by SKU + ownership metadata — never a duplicate.
