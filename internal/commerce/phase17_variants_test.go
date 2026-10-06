package commerce

// Phase 17 §33-§37: ProductVariant commerce mapping orchestration.
//
// The core invariant under test: physical inventory is VARIANT-specific
// and must NEVER multiply — Blue 4 / Gold 2 publishes 4 and 2, never
// 6/6, never a product aggregate copied per variant, and frame choices
// (Phase 15 configurations) never create additional stock.

import (
	"context"
	"errors"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

const (
	p17BlueID = "11111111-1111-4111-8111-1111111111b1"
	p17GoldID = "11111111-1111-4111-8111-1111111111b2"
)

func p17Variant(id, sku, value string, quantity int64, ready bool) CommerceVariant {
	name := value
	return CommerceVariant{
		VariantID: id, SKU: sku,
		Attributes: []CommerceVariantAttribute{{
			DefinitionCode: "color", ValueCode: value,
			NameAR: name, DefinitionNameAR: "اللون",
		}},
		Active: true, AvailableQuantity: quantity, Ready: ready,
		InventoryRevision: 3,
	}
}

func p17DesiredState(productID string, variants []CommerceVariant, configurations []CommerceConfiguration, published bool) DesiredProduct {
	product := CommerceProduct{
		ProductID: productID, SKU: "PAP-1", IsActive: true, SellOnline: true,
		Configurations: configurations,
	}
	product.Variants = variants
	fingerprint, version := variantsIdentity(variants)
	return DesiredProduct{
		Product: product, Published: published,
		Availability:        catalog.ProductAvailability{ProductID: productID, Ready: true},
		CatalogRevision:     12,
		PolicyRevision:      4,
		InventoryRevision:   27,
		VariantsFingerprint: fingerprint, VariantsVersion: version,
	}
}

// TestPhase17VariantInventoryNotMultiplied is the stock non-multiplication
// proof: Blue stock=4 and Gold stock=2 must publish Blue=4 and Gold=2 —
// never 6/6, never the product aggregate, never a per-frame total.
func TestPhase17VariantInventoryNotMultiplied(t *testing.T) {
	ctx := context.Background()
	provider := NewFakeProvider("primary")
	desired := p17DesiredState("p1", []CommerceVariant{
		p17Variant(p17BlueID, "PAP-BLUE", "blue", 4, true),
		p17Variant(p17GoldID, "PAP-GOLD", "gold", 2, true),
	}, nil, true)
	service, _ := testService(provider, map[string]DesiredProduct{"p1": desired})
	result, err := service.SyncProduct(ctx, "primary", "p1")
	if err != nil {
		t.Fatal(err)
	}
	if result.VariantsSynced != 2 || result.VariantMappingsCreated != 2 || result.VariantInventoryUpdated != 2 {
		t.Fatalf("variant outcome: %+v", result)
	}
	if len(provider.Inventories()) != 0 {
		t.Fatal("variant products never receive product-level inventory writes")
	}
	published := map[string]int64{}
	for _, inventory := range provider.VariantInventories() {
		published[inventory.VariantID] = inventory.AvailableQuantity
		if !inventory.Ready {
			t.Fatal("ready variants must publish as ready")
		}
	}
	if published[p17BlueID] != 4 || published[p17GoldID] != 2 {
		t.Fatalf("per-variant stock multiplied or aggregated: %v", published)
	}
	if len(published) != 2 {
		t.Fatalf("exactly one publish per variant: %v", published)
	}
	// The stable operation keys make an identical retry idempotent.
	retry, err := service.SyncProduct(ctx, "primary", "p1")
	if err != nil {
		t.Fatal(err)
	}
	if retry.VariantMappingsCreated != 0 {
		t.Fatal("retry must not recreate mappings")
	}
	if provider.VariantCreations() != 2 {
		t.Fatalf("retry must not mint remote variants: %d", provider.VariantCreations())
	}
	if retry.VariantOperationKey != result.VariantOperationKey {
		t.Fatal("identical desired state must reuse the variant operation key")
	}
}

// TestPhase17FrameChoicesConsumeVariantStock proves frames never multiply
// variant stock: every frame choice of one variant consumes that one
// variant's stock pool — one publish with the variant's quantity, zero
// per-frame inventory, zero product-aggregate publish.
func TestPhase17FrameChoicesConsumeVariantStock(t *testing.T) {
	ctx := context.Background()
	provider := NewFakeProvider("primary")
	delta := int64(1500)
	frames := []CommerceConfiguration{{
		ConfigurationID: "22222222-2222-4222-8222-222222222222",
		Kind:            "frame", StyleCode: "classic", StyleNameAR: "كلاسيك",
		ColorCode: "gold", ColorNameAR: "ذهبي",
		PriceDeltaEGPMinor: delta, Enabled: true,
	}}
	desired := p17DesiredState("p1", []CommerceVariant{
		p17Variant(p17BlueID, "PAP-BLUE", "blue", 4, true),
	}, frames, true)
	service, _ := testService(provider, map[string]DesiredProduct{"p1": desired})
	result, err := service.SyncProduct(ctx, "primary", "p1")
	if err != nil {
		t.Fatal(err)
	}
	if result.VariantsSynced != 1 || result.VariantInventoryUpdated != 1 {
		t.Fatalf("frame choices must not add variant publishes: %+v", result)
	}
	inventories := provider.VariantInventories()
	if len(inventories) != 1 {
		t.Fatalf("frames must never publish inventory: %d writes", len(inventories))
	}
	if inventories[0].VariantID != p17BlueID || inventories[0].AvailableQuantity != 4 {
		t.Fatalf("the one variant pool must carry the variant stock: %+v", inventories[0])
	}
	if len(provider.Inventories()) != 0 {
		t.Fatal("frame layers never trigger product-aggregate inventory")
	}
}

// TestPhase17VariantMappingLossRecoveryNoDuplicate: losing durable
// variant mappings must never duplicate remote variants — the adapter
// re-adopts the same remote identities and the mappings are recreated.
func TestPhase17VariantMappingLossRecoveryNoDuplicate(t *testing.T) {
	ctx := context.Background()
	provider := NewFakeProvider("primary")
	desired := p17DesiredState("p1", []CommerceVariant{
		p17Variant(p17BlueID, "PAP-BLUE", "blue", 4, true),
		p17Variant(p17GoldID, "PAP-GOLD", "gold", 2, true),
	}, nil, true)
	service, mappings := testService(provider, map[string]DesiredProduct{"p1": desired})
	if _, err := service.SyncProduct(ctx, "primary", "p1"); err != nil {
		t.Fatal(err)
	}
	first := map[string]string{}
	for _, mapping := range mappings.variants {
		first[mapping.VariantID] = mapping.ExternalVariantID
	}
	// Durable mapping loss: the remote variations still exist and are
	// re-adopted (never duplicated, never remapped).
	mappings.variants = map[string]ProductVariantMapping{}
	second, err := service.SyncProduct(ctx, "primary", "p1")
	if err != nil {
		t.Fatal(err)
	}
	if provider.VariantCreations() != 2 {
		t.Fatalf("mapping loss must never duplicate remote variants: %d", provider.VariantCreations())
	}
	if second.VariantMappingsCreated != 2 {
		t.Fatalf("mappings must be recreated: %+v", second)
	}
	for _, mapping := range mappings.variants {
		if first[mapping.VariantID] != mapping.ExternalVariantID {
			t.Fatalf("recovery must re-adopt the same remote variant: %v -> %v",
				first[mapping.VariantID], mapping.ExternalVariantID)
		}
	}
}

// TestPhase17WooShopifyIndependentVariantMappings: the same MoonLight
// product and variant carry independent durable identities per provider
// instance — never shared, never colliding.
func TestPhase17WooShopifyIndependentVariantMappings(t *testing.T) {
	ctx := context.Background()
	woo := NewFakeProvider("website")
	shopify := NewFakeProvider("shopify-main")
	registry := NewRegistry()
	if err := registry.Register("website", woo); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("shopify-main", shopify); err != nil {
		t.Fatal(err)
	}
	desired := p17DesiredState("p1", []CommerceVariant{
		p17Variant(p17BlueID, "PAP-BLUE", "blue", 4, true),
	}, nil, true)
	mappings := &stubMappings{rows: map[string]ProductMapping{}}
	service := NewCommerceService(registry, mappings,
		&stubSource{states: map[string]DesiredProduct{"p1": desired}}, nil)
	if _, err := service.SyncProduct(ctx, "website", "p1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SyncProduct(ctx, "shopify-main", "p1"); err != nil {
		t.Fatal(err)
	}
	wooMapping, err := mappings.GetProductVariantMapping(ctx, "website", p17BlueID)
	if err != nil {
		t.Fatal(err)
	}
	shopifyMapping, err := mappings.GetProductVariantMapping(ctx, "shopify-main", p17BlueID)
	if err != nil {
		t.Fatal(err)
	}
	if wooMapping.ExternalVariantID == shopifyMapping.ExternalVariantID {
		t.Fatalf("providers must keep independent variant identities: %q",
			wooMapping.ExternalVariantID)
	}
	if wooMapping.ProductID != "p1" || shopifyMapping.ProductID != "p1" {
		t.Fatal("both mappings belong to the same MoonLight product")
	}
}

// TestPhase17VariantMappingDisputeStopsInventory: an adapter identity
// that disputes a durable mapping never reaches mapping state and never
// publishes variant inventory.
func TestPhase17VariantMappingDisputeStopsInventory(t *testing.T) {
	ctx := context.Background()
	provider := NewFakeProvider("primary")
	provider.OverrideVariantExternal(p17BlueID, "disputed-identity")
	desired := p17DesiredState("p1", []CommerceVariant{
		p17Variant(p17BlueID, "PAP-BLUE", "blue", 4, true),
	}, nil, true)
	service, mappings := testService(provider, map[string]DesiredProduct{"p1": desired})
	if _, err := mappings.CreateProductVariantMapping(ctx, "primary", "p1", p17BlueID, "ext-p1", "frozen-identity"); err != nil {
		t.Fatal(err)
	}
	_, err := service.SyncProduct(ctx, "primary", "p1")
	if err == nil {
		t.Fatal("disputed variant identity must fail")
	}
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Kind != apperr.Conflict {
		t.Fatalf("dispute must classify as conflict: %v", err)
	}
	if len(provider.VariantInventories()) != 0 {
		t.Fatal("inventory is never addressed while variant identity is disputed")
	}
}

// TestPhase17VariantMappingPersistenceStopsInventory: durable mapping
// failure stops before any variant inventory publish.
func TestPhase17VariantMappingPersistenceStopsInventory(t *testing.T) {
	ctx := context.Background()
	provider := NewFakeProvider("primary")
	desired := p17DesiredState("p1", []CommerceVariant{
		p17Variant(p17BlueID, "PAP-BLUE", "blue", 4, true),
	}, nil, true)
	service, mappings := testService(provider, map[string]DesiredProduct{"p1": desired})
	mappings.failCount = 1
	mappings.failNext = errors.New("injected mapping persistence failure")
	if _, err := service.SyncProduct(ctx, "primary", "p1"); err == nil {
		t.Fatal("mapping persistence failure must surface")
	}
	if len(provider.VariantInventories()) != 0 {
		t.Fatal("no variant inventory before durable mapping")
	}
}

// TestPhase17VariantCapabilityConflictFailsSafely: a provider without
// the variant capability fails with a capability conflict — never a
// product-aggregate inventory fallback that could multiply stock.
func TestPhase17VariantCapabilityConflictFailsSafely(t *testing.T) {
	ctx := context.Background()
	provider := &p17LegacyProvider{key: "primary"}
	registry := NewRegistry()
	if err := registry.Register("primary", provider); err != nil {
		t.Fatal(err)
	}
	desired := p17DesiredState("p1", []CommerceVariant{
		p17Variant(p17BlueID, "PAP-BLUE", "blue", 4, true),
	}, nil, true)
	service := NewCommerceService(registry, &stubMappings{rows: map[string]ProductMapping{}},
		&stubSource{states: map[string]DesiredProduct{"p1": desired}}, nil)
	_, err := service.SyncProduct(ctx, "primary", "p1")
	if err == nil {
		t.Fatal("missing variant capability must fail safely")
	}
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Kind != apperr.Conflict {
		t.Fatalf("capability gap must classify as conflict: %v", err)
	}
	if len(provider.inventories) != 0 {
		t.Fatal("provider limitations never fall back to aggregate inventory")
	}
}

// p17LegacyProvider is a product-only CommerceProvider (no variant
// capability): the fail-safe capability case.
type p17LegacyProvider struct {
	key         ProviderKey
	inventories []InventoryUpdateRequest
}

func (l *p17LegacyProvider) Key() ProviderKey { return l.key }

func (l *p17LegacyProvider) UpsertProduct(_ context.Context, req ProductUpsertRequest) (ProductUpsertResult, error) {
	return ProductUpsertResult{ExternalProductID: "ext-" + req.ProductID}, nil
}

func (l *p17LegacyProvider) SetInventory(_ context.Context, req InventoryUpdateRequest) error {
	l.inventories = append(l.inventories, req)
	return nil
}

// TestPhase17AvailabilityVariantAwarePublish assembles desired variant
// state through the real CatalogCommerceSource and proves per-variant
// publishing follows ComputeVariantAvailability (caps, activity,
// readiness) and the unpublished safe-zero rule.
func TestPhase17AvailabilityVariantAwarePublish(t *testing.T) {
	ctx := context.Background()
	limit := 3
	reader := &p17Reader{
		product: catalog.Product{ProductHeader: catalog.ProductHeader{
			ID: "p1", SKU: "PAP-1", IsActive: true, Revision: 12,
		}},
		policy: catalog.ProductSalesPolicy{ProductID: "p1", SellOnline: true, Revision: 4},
		onlinePolicy: catalog.ProductOnlinePolicy{
			ProductID: "p1", Allowed: true, PolicyFingerprint: "fp", PolicyVersion: "v1",
		},
		availability: catalog.ProductAvailability{ProductID: "p1", Ready: true},
		variants: []catalog.Variant{
			{VariantID: p17BlueID, SKU: "PAP-BLUE", IsActive: true},
			{VariantID: p17GoldID, SKU: "PAP-GOLD", IsActive: true},
		},
		variantAvail: map[string]catalog.VariantAvailability{
			// Blue stock 5 capped by the allocation limit 3.
			p17BlueID: {
				VariantID: p17BlueID, ProductID: "p1", ProductActive: true, VariantActive: true,
				SellOnline: true, OnlineAllocationLimit: &limit, StockQuantity: intPtr(5),
				Ready: true, InventoryRevision: 31,
				OnlineAvailable: catalog.ComputeVariantAvailability(true, true, 5, &limit),
			},
			// Gold inactive: availability must be zero.
			p17GoldID: {
				VariantID: p17GoldID, ProductID: "p1", ProductActive: true, VariantActive: false,
				SellOnline: true, StockQuantity: intPtr(2), Ready: true, InventoryRevision: 32,
				OnlineAvailable: catalog.ComputeVariantAvailability(true, false, 2, nil),
			},
		},
	}
	source := NewCatalogCommerceSource(reader)
	desired, err := source.GetDesiredCommerceProduct(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(desired.Product.Variants) != 2 {
		t.Fatalf("variants assembled: %d", len(desired.Product.Variants))
	}
	quantities := map[string]int64{}
	for _, variant := range desired.Product.Variants {
		quantities[variant.VariantID] = variant.AvailableQuantity
	}
	if quantities[p17BlueID] != 3 {
		t.Fatalf("capped variant availability must publish %d, got %d", 3, quantities[p17BlueID])
	}
	if quantities[p17GoldID] != 0 {
		t.Fatalf("inactive variant must publish zero, got %d", quantities[p17GoldID])
	}
	if desired.VariantsFingerprint == "" || desired.VariantsVersion == "" {
		t.Fatal("variant set identity must be deterministic")
	}
	// Unpublished products publish zero per variant (§68 semantics).
	reader.product.IsActive = false
	reader.onlinePolicy.Allowed = false
	unpublished, err := source.GetDesiredCommerceProduct(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	for _, variant := range unpublished.Product.Variants {
		if variant.AvailableQuantity != 0 {
			t.Fatalf("unpublished product must publish zero per variant, got %d", variant.AvailableQuantity)
		}
	}
}

func intPtr(value int) *int { return &value }

// p17Reader is a CatalogReader + VariantCatalogReader stub for source
// assembly tests.
type p17Reader struct {
	product      catalog.Product
	policy       catalog.ProductSalesPolicy
	onlinePolicy catalog.ProductOnlinePolicy
	availability catalog.ProductAvailability
	variants     []catalog.Variant
	variantAvail map[string]catalog.VariantAvailability
}

func (r *p17Reader) GetProduct(context.Context, string) (catalog.Product, error) {
	return r.product, nil
}
func (r *p17Reader) GetProductOnlinePolicy(context.Context, string) (catalog.ProductOnlinePolicy, error) {
	return r.onlinePolicy, nil
}
func (r *p17Reader) GetCategory(context.Context, string) (catalog.Category, error) {
	return catalog.Category{}, apperr.New(apperr.NotFound, "no category")
}
func (r *p17Reader) GetTag(context.Context, string) (catalog.Tag, error) {
	return catalog.Tag{}, apperr.New(apperr.NotFound, "no tag")
}
func (r *p17Reader) GetProductSalesPolicy(context.Context, string) (catalog.ProductSalesPolicy, error) {
	return r.policy, nil
}
func (r *p17Reader) GetProductConfigurations(context.Context, string) ([]catalog.ProductConfiguration, error) {
	return nil, nil
}
func (r *p17Reader) GetProductAvailability(context.Context, string) (catalog.ProductAvailability, error) {
	return r.availability, nil
}
func (r *p17Reader) ListProductVariants(context.Context, string) ([]catalog.Variant, error) {
	return r.variants, nil
}
func (r *p17Reader) GetVariantAvailability(_ context.Context, id string) (catalog.VariantAvailability, error) {
	availability, ok := r.variantAvail[id]
	if !ok {
		return catalog.VariantAvailability{VariantID: id, MissingVariant: true}, nil
	}
	return availability, nil
}
