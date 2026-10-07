package commerce

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

// CatalogReader is the narrow frozen-state boundary the commerce
// assembler reads through. Adapters never touch sqlc, PostgreSQL, or
// projection tables directly; catalog.Service satisfies this interface.
type CatalogReader interface {
	GetProduct(ctx context.Context, id string) (catalog.Product, error)
	GetProductOnlinePolicy(ctx context.Context, id string) (catalog.ProductOnlinePolicy, error)
	GetCategory(ctx context.Context, id string) (catalog.Category, error)
	GetTag(ctx context.Context, id string) (catalog.Tag, error)
	GetProductSalesPolicy(ctx context.Context, id string) (catalog.ProductSalesPolicy, error)
	GetProductConfigurations(ctx context.Context, id string) ([]catalog.ProductConfiguration, error)
	GetProductAvailability(ctx context.Context, id string) (catalog.ProductAvailability, error)
}

// VariantCatalogReader is the OPTIONAL Phase 17 variant read capability
// (detected by type assertion exactly like the provider order/variant
// capabilities). catalog.Service satisfies it. A reader without it
// yields no variant desired state and products keep the frozen
// product-level behavior.
type VariantCatalogReader interface {
	ListProductVariants(ctx context.Context, productID string) ([]catalog.Variant, error)
	GetVariantAvailability(ctx context.Context, variantID string) (catalog.VariantAvailability, error)
}

// DesiredProduct is the provider-neutral desired state for one MoonLight
// product: the assembled product snapshot, whether it should be actively
// published online (is_active AND sell_online), and the Phase 5C derived
// availability with source revisions.
type DesiredProduct struct {
	Product CommerceProduct
	// StoreID is the proven Store of the authoritative catalog product,
	// nil for legacy products. Never sent to providers; used only for
	// mapping/order ownership gates.
	StoreID           *string
	Published         bool
	Availability      catalog.ProductAvailability
	CatalogRevision   int64
	PolicyRevision    int64
	InventoryRevision int64
	// Configurations carries the complete current option set and its
	// deterministic identity (Phase 15 §48) for provider operation keys.
	Configurations            []CommerceConfiguration
	ConfigurationsFingerprint string
	ConfigurationsVersion     string
	// VariantsFingerprint/VariantsVersion are the deterministic identity
	// of the ProductVariant set (Phase 17) for provider operation keys.
	// The fingerprint covers sellable semantics (ids, SKUs, option
	// identity, prices, active); the version additionally rotates on
	// label/position bookkeeping so renames never masquerade as remote
	// variant changes. Variants themselves ride in Product.Variants.
	VariantsFingerprint string
	VariantsVersion     string
	// CategoryPolicyFingerprint is the deterministic ELIGIBILITY identity
	// of the Product's relevant Category ONLINE policy context (Phase 13),
	// and CategoryPolicyVersion its revision-digest generation marker.
	// Both participate in provider operation identity so category
	// disable→enable transitions can never reuse a stale idempotency key.
	CategoryPolicyFingerprint string
	CategoryPolicyVersion     string
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
		StoreID:           product.StoreID,
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
	// Phase 13 §65: publication requires lifecycle consent, Product
	// policy consent, AND the Category hierarchy to allow ONLINE. A
	// missing Product policy or missing/incomplete Category policy state
	// can never publish: unknown online state defaults to disabled
	// (§66/§67 fail-safe).
	onlinePolicy, err := s.reader.GetProductOnlinePolicy(ctx, productID)
	if err != nil {
		return DesiredProduct{}, err
	}
	desired.Published = product.IsActive && policyFound && policy.SellOnline && onlinePolicy.Allowed
	desired.CategoryPolicyFingerprint = onlinePolicy.PolicyFingerprint
	desired.CategoryPolicyVersion = onlinePolicy.PolicyVersion
	// Phase 15: complete option set (enabled and disabled) with its
	// deterministic identity. Publication of every choice remains gated
	// by the Product's effective eligibility above (§33-§36).
	configurationState, err := s.reader.GetProductConfigurations(ctx, productID)
	if err != nil {
		return DesiredProduct{}, err
	}
	desired.Configurations = make([]CommerceConfiguration, 0, len(configurationState))
	for _, configuration := range configurationState {
		desired.Configurations = append(desired.Configurations, CommerceConfiguration{
			ConfigurationID: configuration.ID, Kind: configuration.Kind,
			StyleCode: configuration.StyleCode, StyleNameAR: configuration.StyleNameAR, StyleNameEN: configuration.StyleNameEN,
			ColorCode: configuration.ColorCode, ColorNameAR: configuration.ColorNameAR, ColorNameEN: configuration.ColorNameEN,
			PriceDeltaEGPMinor: configuration.PriceDeltaEGPCents, PriceDeltaUSDMinor: configuration.PriceDeltaUSDCents,
			Enabled: configuration.Enabled, Position: configuration.Position,
			ConfigurationRevision: configuration.Revision,
		})
	}
	desired.Product.Configurations = desired.Configurations
	desired.ConfigurationsFingerprint, desired.ConfigurationsVersion = configurationIdentity(configurationState)
	// Phase 17: the complete ProductVariant set (SKU/inventory owners)
	// with per-variant derived availability (ComputeVariantAvailability
	// output). Optional capability: readers without variant projections
	// keep the frozen product-level behavior.
	if variantReader, ok := s.reader.(VariantCatalogReader); ok {
		variants, err := variantReader.ListProductVariants(ctx, productID)
		if err != nil {
			return DesiredProduct{}, err
		}
		desired.Product.Variants = make([]CommerceVariant, 0, len(variants))
		for _, variant := range variants {
			availability, err := variantReader.GetVariantAvailability(ctx, variant.VariantID)
			if err != nil {
				return DesiredProduct{}, err
			}
			quantity := int64(availability.OnlineAvailable)
			if !desired.Published {
				// Not published means zero availability toward the
				// provider per variant, never positive stock (§68).
				quantity = 0
			}
			entry := CommerceVariant{
				VariantID:         variant.VariantID,
				SKU:               variant.SKU,
				PriceEGPMinor:     variant.PriceEGPCents,
				PriceUSDMinor:     variant.PriceUSDCents,
				Active:            variant.IsActive,
				AvailableQuantity: quantity,
				Ready:             availability.Ready,
				InventoryRevision: availability.InventoryRevision,
			}
			for _, attribute := range variant.Attributes {
				entry.Attributes = append(entry.Attributes, CommerceVariantAttribute{
					DefinitionCode:   attribute.DefinitionCode,
					ValueCode:        attribute.ValueCode,
					NameAR:           attribute.NameAR,
					NameEN:           attribute.NameEN,
					DefinitionNameAR: attribute.DefinitionNameAR,
					DefinitionNameEN: attribute.DefinitionNameEN,
					Position:         attribute.Position,
				})
			}
			desired.Product.Variants = append(desired.Product.Variants, entry)
		}
		desired.VariantsFingerprint, desired.VariantsVersion = variantsIdentity(desired.Product.Variants)
		// Phase 17-R0 (ADR-0049): the provider-facing product SKU is
		// DERIVED from variant SKU ownership — the first live variant by
		// position (providers require a product-level SKU). Product
		// itself carries no SKU authority anywhere; a product without
		// variant rows carries no SKU and cannot be published
		// (documented provider limitation, see docs/operations).
		if len(desired.Product.Variants) > 0 {
			desired.Product.SKU = desired.Product.Variants[0].SKU
		}
	}
	if !desired.Published {
		// Provider-facing quantity follows existing disabled-product
		// semantics: not published means zero availability toward the
		// provider, never positive stock on an unpublished product
		// (§68). Retail stock authority is untouched.
		desired.Availability.OnlineAvailable = 0
	}
	return desired, nil
}

func isNotFound(err error) bool {
	var appErr *apperr.Error
	if errors.As(err, &appErr) {
		return appErr.Kind == apperr.NotFound
	}
	return false
}

// configurationIdentity derives the deterministic option-state identity
// (Phase 15 §48/§50): the fingerprint covers published option semantics
// (ids, codes, published labels, deltas, enabled, position) while the
// version digest covers per-configuration revisions — a pure label
// rename rotates the version but NOT the fingerprint, so Retail-only
// bookkeeping never masquerades as a remote option change (§50).
func configurationIdentity(configurations []catalog.ProductConfiguration) (fingerprint, version string) {
	parts := make([]string, 0, len(configurations))
	versions := make([]string, 0, len(configurations))
	for _, configuration := range configurations {
		labelEN := ""
		if configuration.StyleNameEN != nil {
			labelEN = *configuration.StyleNameEN
		}
		colorEN := ""
		if configuration.ColorNameEN != nil {
			colorEN = *configuration.ColorNameEN
		}
		usd := "-"
		if configuration.PriceDeltaUSDCents != nil {
			usd = strconv.FormatInt(*configuration.PriceDeltaUSDCents, 10)
		}
		parts = append(parts, strings.Join([]string{
			configuration.ID, configuration.Kind, configuration.StyleCode,
			configuration.StyleNameAR, labelEN, configuration.ColorCode,
			configuration.ColorNameAR, colorEN,
			strconv.FormatInt(configuration.PriceDeltaEGPCents, 10), usd,
			strconv.FormatBool(configuration.Enabled),
			strconv.Itoa(configuration.Position),
		}, ":"))
		versions = append(versions, configuration.ID+":"+strconv.FormatInt(configuration.Revision, 10))
	}
	sort.Strings(parts)
	sort.Strings(versions)
	return strings.Join(parts, "|"), strings.Join(versions, "|")
}

// variantsIdentity derives the deterministic ProductVariant-set identity
// (Phase 17), mirroring configurationIdentity: the fingerprint covers
// sellable semantics (variant ids, SKUs, option identity codes, price
// overrides, active) while the version additionally covers display
// labels, positions and per-variant revisions — a pure label rename
// rotates the version but NOT the fingerprint.
func variantsIdentity(variants []CommerceVariant) (fingerprint, version string) {
	parts := make([]string, 0, len(variants))
	versions := make([]string, 0, len(variants))
	for _, variant := range variants {
		usd := "-"
		if variant.PriceUSDMinor != nil {
			usd = strconv.FormatInt(*variant.PriceUSDMinor, 10)
		}
		egp := "-"
		if variant.PriceEGPMinor != nil {
			egp = strconv.FormatInt(*variant.PriceEGPMinor, 10)
		}
		codes := make([]string, 0, len(variant.Attributes))
		labels := make([]string, 0, len(variant.Attributes))
		for _, attribute := range variant.Attributes {
			codes = append(codes, attribute.DefinitionCode+"="+attribute.ValueCode)
			nameEN := ""
			if attribute.NameEN != nil {
				nameEN = *attribute.NameEN
			}
			definitionEN := ""
			if attribute.DefinitionNameEN != nil {
				definitionEN = *attribute.DefinitionNameEN
			}
			labels = append(labels, strings.Join([]string{
				attribute.DefinitionCode, attribute.NameAR, nameEN,
				attribute.DefinitionNameAR, definitionEN,
				strconv.Itoa(attribute.Position),
			}, ":"))
		}
		sort.Strings(codes)
		sort.Strings(labels)
		parts = append(parts, strings.Join([]string{
			variant.VariantID, variant.SKU, egp, usd,
			strconv.FormatBool(variant.Active), strings.Join(codes, ","),
		}, ":"))
		versions = append(versions, strings.Join([]string{
			variant.VariantID, strings.Join(labels, ","),
		}, ":"))
	}
	sort.Strings(parts)
	sort.Strings(versions)
	return strings.Join(parts, "|"), strings.Join(versions, "|")
}

// ConfiguredPrice computes the customer-facing configured price
// (§38-§40): exact int64 minor units, base + delta with overflow
// rejection (never wraparound; never float; never FX).
func ConfiguredPrice(base, delta int64) (int64, error) {
	if delta < 0 {
		return 0, fmt.Errorf("configured price: negative delta")
	}
	total := base + delta
	if total < base {
		return 0, fmt.Errorf("configured price: overflow")
	}
	return total, nil
}
