package commerce

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// SyncOutcome describes what SyncProduct did.
type SyncOutcome string

const (
	// SyncNoOp means nothing was sent: unpublished product with no mapping.
	SyncNoOp SyncOutcome = "noop"
	// SyncProduct means the adapter converged remote product state
	// (create, update, or disable) and inventory was addressed.
	SyncProduct SyncOutcome = "product_synced"
)

// SyncResult is the structured outcome of one SyncProduct call: no
// credentials, no raw provider responses.
type SyncResult struct {
	Outcome               SyncOutcome
	ProviderKey           ProviderKey
	ExternalProductID     string
	MappingCreated        bool
	InventoryUpdated      bool
	ProductOperationKey   string
	InventoryOperationKey string
	// Phase 17 variant reconciliation outcome (zero without variants).
	VariantsSynced          int
	VariantMappingsCreated  int
	VariantInventoryUpdated int
	VariantOperationKey     string
}

// CommerceService is the synchronous orchestration seam Phase 6B
// invokes: resolve provider, assemble desired state, converge the
// remote product, persist the mapping, then set safe availability. No
// scheduler, no background jobs, no polling.
type CommerceService struct {
	providers *Registry
	mappings  ProductMappingRepository
	source    CommerceProductSource
	log       *slog.Logger
}

// NewCommerceService wires orchestration over a provider registry, a
// durable mapping repository, and a desired-state source.
func NewCommerceService(providers *Registry, mappings ProductMappingRepository, source CommerceProductSource, log *slog.Logger) *CommerceService {
	return &CommerceService{providers: providers, mappings: mappings, source: source, log: log}
}

// SyncProduct converges one provider's remote product to the current
// MoonLight desired state.
//
//   - Unpublished product with no mapping: successful no-op, zero
//     provider calls (there is nothing remote to disable).
//   - Otherwise UpsertProduct converges remote metadata/publication
//     state, the mapping persists (idempotent same-pair, conflict on
//     remap), then SetInventory addresses the external product with
//     Phase 5C derived availability (safe-zero when not ready).
//
// Mapping persistence failure returns before SetInventory: Cloud cannot
// reliably address the remote product later without the mapping. Stable
// operation keys make every step safely retryable.
func (s *CommerceService) SyncProduct(ctx context.Context, providerKey, productID string) (SyncResult, error) {
	key, err := ValidateProviderKey(providerKey)
	if err != nil {
		return SyncResult{}, apperr.New(apperr.InvalidInput, err.Error())
	}
	if productID == "" {
		return SyncResult{}, apperr.New(apperr.InvalidInput, "product id must not be empty")
	}
	provider, err := s.providers.Get(key)
	if err != nil {
		return SyncResult{}, err
	}
	if coordinator, ok := s.mappings.(ProductSyncCoordinator); ok && !ProductSyncHeld(ctx, key, productID) {
		var result SyncResult
		err := coordinator.WithProductSync(ctx, key, productID, func(held context.Context) error {
			var err error
			result, err = s.SyncProduct(held, providerKey, productID)
			return err
		})
		return result, err
	}
	desired, err := s.source.GetDesiredCommerceProduct(ctx, productID)
	if err != nil {
		return SyncResult{}, err
	}
	mapping, mappingErr := s.mappings.GetProductMapping(ctx, key, productID)
	mapped := mappingErr == nil
	if mappingErr != nil && !isNotFound(mappingErr) {
		return SyncResult{}, mappingErr
	}

	// Phase 9C mapping ownership: the mapping mirrors its authoritative
	// product. A legacy NULL mapping adopts the proven product Store
	// through the idempotent same-pair path before any provider call; a
	// mapping owned by another proven Store can never serve this
	// product (unreachable via product immutability, fenced explicitly).
	if mapped && mapping.StoreID == nil && desired.StoreID != nil {
		adopted, err := s.mappings.CreateProductMapping(ctx, key, productID, mapping.ExternalProductID)
		if err != nil {
			return SyncResult{}, err
		}
		mapping = adopted
	}
	if mapped && mapping.StoreID != nil && desired.StoreID != nil &&
		*mapping.StoreID != *desired.StoreID {
		return SyncResult{}, apperr.New(apperr.Conflict,
			"STORE_SCOPE_CONFLICT: commerce mapping owned by another store")
	}

	if !desired.Published && !mapped {
		s.logInfo("commerce sync noop", "provider", string(key), "product", productID)
		return SyncResult{Outcome: SyncNoOp, ProviderKey: key}, nil
	}

	var existing *ProviderProductRef
	if mapped {
		existing = &ProviderProductRef{ExternalProductID: mapping.ExternalProductID}
	}
	productKey := ProductOperationKey(key, productID, desired.CatalogRevision, desired.PolicyRevision, desired.Published, desired.CategoryPolicyFingerprint, desired.CategoryPolicyVersion,
		desired.ConfigurationsFingerprint, desired.ConfigurationsVersion)
	upserted, err := provider.UpsertProduct(ctx, ProductUpsertRequest{
		ProviderKey: key, ProductID: productID, ExistingExternal: existing,
		Product: desired.Product, Published: desired.Published,
		CatalogRevision: desired.CatalogRevision, PolicyRevision: desired.PolicyRevision,
		OperationKey: productKey,
	})
	if err != nil {
		return SyncResult{}, err
	}
	if upserted.ExternalProductID == "" {
		return SyncResult{}, apperr.New(apperr.Internal, "provider returned empty external product id")
	}
	if len(upserted.ExternalProductID) > 200 {
		return SyncResult{}, apperr.New(apperr.Internal, "provider returned overlong external product id")
	}
	if mapped && mapping.ExternalProductID != upserted.ExternalProductID && !upserted.SellableTransition {
		// The adapter claims a different remote identity WITHOUT the
		// authorized-transition marker: never silently remap (generic
		// protection, F07). Inventory is not addressed while identity is
		// disputed.
		return SyncResult{}, apperr.New(apperr.Conflict, fmt.Sprintf(
			"provider mapping conflict for %s/%s: mapped %q, adapter returned %q",
			key, productID, mapping.ExternalProductID, upserted.ExternalProductID))
	}

	result := SyncResult{
		Outcome: SyncProduct, ProviderKey: key,
		ExternalProductID:   upserted.ExternalProductID,
		ProductOperationKey: productKey,
	}
	if !mapped {
		if _, err := s.mappings.CreateProductMapping(ctx, key, productID, upserted.ExternalProductID); err != nil {
			// Mapping persistence failed: stop before SetInventory.
			// The stable operation key lets a retry recover without
			// duplicating the remote product.
			return SyncResult{}, err
		}
		result.MappingCreated = true
	}

	// Phase 15-R1 F07/§116: the sellable remote identity may transition
	// (simple→framed bundle parent). The transition is a compare-and-set
	// on the existing mapping — a concurrent different identity is never
	// clobbered and no abandoned conflicting mapping is created.
	if mapped && upserted.ExternalProductID != "" {
		if _, err := s.mappings.UpdateProductMappingExternal(ctx, key, productID,
			mapping.ExternalProductID, upserted.ExternalProductID); err != nil {
			return SyncResult{}, err
		}
	}

	// Phase 15 §64: persist provider configuration identity durably; a
	// failure here stops before SetInventory and a retry re-adopts the
	// owned remote representation (never a duplicate variation).
	for configurationID, externalConfigurationID := range upserted.Configurations {
		if _, err := s.mappings.UpsertProductConfigurationMapping(ctx, key, productID, configurationID,
			upserted.ExternalProductID, externalConfigurationID); err != nil {
			return SyncResult{}, err
		}
	}

	result.VariantOperationKey, err = s.syncVariants(ctx, provider, key, productID, desired, upserted.ExternalProductID, &result)
	if err != nil {
		return SyncResult{}, err
	}
	if len(desired.Product.Variants) > 0 {
		// Phase 17: variant-owning products publish per-variant
		// availability ONLY. A product-level aggregate publish would
		// misrepresent variant stock (and multiply it across frame
		// choices), so it never runs alongside the variant path.
		s.logInfo("commerce sync variants", "provider", string(key), "product", productID,
			"external", upserted.ExternalProductID, "variants", result.VariantsSynced,
			"published", desired.Published, "mapped", mapped)
		return result, nil
	}

	quantity := int64(desired.Availability.OnlineAvailable)
	inventoryKey := InventoryOperationKey(key, productID,
		desired.CatalogRevision, desired.PolicyRevision, desired.InventoryRevision,
		quantity, desired.Published, desired.CategoryPolicyFingerprint, desired.CategoryPolicyVersion,
		desired.ConfigurationsFingerprint, desired.ConfigurationsVersion)
	if err := provider.SetInventory(ctx, InventoryUpdateRequest{
		ProviderKey: key, ProductID: productID, ExternalProductID: upserted.ExternalProductID,
		AvailableQuantity: quantity,
		InventoryRevision: desired.InventoryRevision,
		CatalogRevision:   desired.CatalogRevision, PolicyRevision: desired.PolicyRevision,
		Ready: desired.Availability.Ready, OperationKey: inventoryKey,
	}); err != nil {
		return SyncResult{}, err
	}
	result.InventoryUpdated = true
	result.InventoryOperationKey = inventoryKey
	s.logInfo("commerce sync", "provider", string(key), "product", productID,
		"external", upserted.ExternalProductID, "published", desired.Published,
		"quantity", quantity, "mapped", mapped)
	return result, nil
}

func (s *CommerceService) logInfo(msg string, args ...any) {
	if s.log != nil {
		s.log.Info(msg, args...)
	}
}

// syncVariants reconciles the provider variation set to the desired
// MoonLight ProductVariant set (Phase 17) and publishes per-variant
// availability. Durable per-variant mappings persist BEFORE any variant
// inventory is published; a mapping-loss retry re-adopts the owned
// remote variation (adapters recover by SKU + ownership metadata), never
// a duplicate. Provider limitations fail safely: a provider without the
// variant capability, or a returned identity that disputes a durable
// mapping, is a capability/mapping conflict — never an inventory
// fallback that could multiply stock.
func (s *CommerceService) syncVariants(ctx context.Context, provider CommerceProvider, key ProviderKey, productID string, desired DesiredProduct, externalProductID string, result *SyncResult) (string, error) {
	variants := desired.Product.Variants
	if len(variants) == 0 {
		return "", nil
	}
	variantProvider, err := AsVariantProvider(provider)
	if err != nil {
		return "", apperr.New(apperr.Conflict,
			"VARIANT_CAPABILITY_UNAVAILABLE: provider cannot represent product variants without multiplying stock")
	}
	existingRows, err := s.mappings.ListProductVariantMappings(ctx, key, productID)
	if err != nil {
		return "", err
	}
	existing := make(map[string]string, len(existingRows))
	for _, row := range existingRows {
		existing[row.VariantID] = row.ExternalVariantID
	}
	variantKey := VariantOperationKey(key, productID,
		desired.CatalogRevision, desired.PolicyRevision, desired.Published,
		desired.VariantsFingerprint, desired.VariantsVersion)
	upserted, err := variantProvider.UpsertProductVariants(ctx, ProductVariantsUpsertRequest{
		ProviderKey: key, ProductID: productID, ExternalProductID: externalProductID,
		ExistingVariants: existing, Product: desired.Product, Published: desired.Published,
		CatalogRevision: desired.CatalogRevision, PolicyRevision: desired.PolicyRevision,
		OperationKey: variantKey,
	})
	if err != nil {
		return "", err
	}
	// The complete returned set is validated BEFORE anything is
	// persisted: partial or disputed identity never reaches durable
	// mapping state.
	desiredIDs := make(map[string]bool, len(variants))
	for _, variant := range variants {
		desiredIDs[variant.VariantID] = true
		externalVariantID := upserted.Variants[variant.VariantID]
		if externalVariantID == "" {
			return "", apperr.New(apperr.Internal, "provider returned no external variant id for one variant")
		}
		if len(externalVariantID) > 200 {
			return "", apperr.New(apperr.Internal, "provider returned overlong external variant id")
		}
		if mapped, ok := existing[variant.VariantID]; ok && mapped != externalVariantID {
			// The adapter claims a different remote variation for an
			// already-mapped variant: never silently remap (generic
			// protection). Variant inventory is not addressed while
			// identity is disputed.
			return "", apperr.New(apperr.Conflict, fmt.Sprintf(
				"provider variant mapping conflict for %s/%s: mapped %q, adapter returned %q",
				key, variant.VariantID, mapped, externalVariantID))
		}
	}
	for variantID := range upserted.Variants {
		if !desiredIDs[variantID] {
			return "", apperr.New(apperr.Internal, "provider returned external variant id for an unknown variant")
		}
	}
	for _, variant := range variants {
		if _, ok := existing[variant.VariantID]; ok {
			continue
		}
		if _, err := s.mappings.CreateProductVariantMapping(ctx, key, productID, variant.VariantID,
			externalProductID, upserted.Variants[variant.VariantID]); err != nil {
			// Mapping persistence failed: stop before variant inventory.
			// The stable operation key lets a retry recover without
			// duplicating the remote variation.
			return "", err
		}
		result.VariantMappingsCreated++
	}
	for _, variant := range variants {
		quantity := variant.AvailableQuantity
		inventoryKey := VariantInventoryOperationKey(key, productID, variant.VariantID,
			desired.CatalogRevision, desired.PolicyRevision, variant.InventoryRevision,
			quantity, variant.Ready, desired.Published,
			desired.VariantsFingerprint, desired.VariantsVersion)
		if err := variantProvider.SetVariantInventory(ctx, VariantInventoryUpdateRequest{
			ProviderKey: key, ProductID: productID, VariantID: variant.VariantID,
			ExternalProductID: externalProductID, ExternalVariantID: upserted.Variants[variant.VariantID],
			AvailableQuantity: quantity, InventoryRevision: variant.InventoryRevision,
			CatalogRevision: desired.CatalogRevision, PolicyRevision: desired.PolicyRevision,
			Ready: variant.Ready, OperationKey: inventoryKey,
		}); err != nil {
			return "", err
		}
		result.VariantInventoryUpdated++
	}
	result.VariantsSynced = len(variants)
	return variantKey, nil
}
