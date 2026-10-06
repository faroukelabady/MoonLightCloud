package shopify

// Phase 17 §33-§37: Shopify ProductVariant mapping tests. Physical
// inventory is VARIANT-specific and must never multiply: Blue 4 / Gold 2
// publishes 4 and 2; frame choices (bundle options) consume the SAME
// variant stock pool; unsafe variant+frame layering fails with the
// stable capability code instead of multiplying stock.

import (
	"context"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

const (
	p17BlueVariantID = "11111111-1111-4111-8111-1111111111b1"
	p17GoldVariantID = "11111111-1111-4111-8111-1111111111b2"
)

func p17Variant(value, sku string, quantity int64, priceOverride *int64) commerce.CommerceVariant {
	name := value
	return commerce.CommerceVariant{
		VariantID: map[string]string{"blue": p17BlueVariantID, "gold": p17GoldVariantID}[value],
		SKU:       sku,
		Attributes: []commerce.CommerceVariantAttribute{{
			DefinitionCode: "color", ValueCode: value, NameAR: name,
			DefinitionNameAR: "اللون",
		}},
		PriceEGPMinor: priceOverride,
		Active:        true, AvailableQuantity: quantity, Ready: true, InventoryRevision: 9,
	}
}

func p17Product(productID string, variants ...commerce.CommerceVariant) commerce.CommerceProduct {
	return commerce.CommerceProduct{
		ProductID: productID, SKU: "PAP-001", IsActive: true, SellOnline: true,
		Names:           []commerce.LocalizedName{{Locale: "ar", Name: "بردية"}, {Locale: "en", Name: "Papyrus"}},
		Prices:          []commerce.Money{{Currency: "EGP", AmountMinor: 65000}},
		Variants:        variants,
		CatalogRevision: 12, PolicyRevision: 4,
	}
}

func p17VariantsUpsertReq(product commerce.CommerceProduct, externalProduct string, existing map[string]string) commerce.ProductVariantsUpsertRequest {
	return commerce.ProductVariantsUpsertRequest{
		ProviderKey: "shopify-main", ProductID: product.ProductID,
		ExternalProductID: externalProduct, ExistingVariants: existing,
		Product: product, Published: true,
		CatalogRevision: product.CatalogRevision, PolicyRevision: product.PolicyRevision,
		OperationKey: commerce.VariantOperationKey("shopify-main", product.ProductID,
			product.CatalogRevision, product.PolicyRevision, true, "vf", "vv"),
	}
}

func p17VariantInventoryReq(productID, externalProduct, variantID, externalVariant string, quantity int64) commerce.VariantInventoryUpdateRequest {
	return commerce.VariantInventoryUpdateRequest{
		ProviderKey: "shopify-main", ProductID: productID, VariantID: variantID,
		ExternalProductID: externalProduct, ExternalVariantID: externalVariant,
		AvailableQuantity: quantity, InventoryRevision: 9,
		CatalogRevision: 12, PolicyRevision: 4, Ready: true,
		OperationKey: commerce.VariantInventoryOperationKey("shopify-main", productID, variantID,
			12, 4, 9, quantity, true, true, "vf", "vv"),
	}
}

// TestShopifyVariantTrackedPerVariantStock is the stock
// non-multiplication proof on Shopify: one tracked variant per MoonLight
// variant, each at its exact availability. The provider must see Blue 4
// and Gold 2 — never 6/6, never any aggregate.
func TestShopifyVariantTrackedPerVariantStock(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	provider := newTestProvider(t, h)
	goldPrice := int64(70000)
	product := p17Product("p1",
		p17Variant("blue", "PAP-BLUE", 4, nil),
		p17Variant("gold", "PAP-GOLD", 2, &goldPrice))

	upserted, err := provider.UpsertProduct(ctx, commerce.ProductUpsertRequest{
		ProviderKey: "shopify-main", ProductID: "p1",
		Product: product, Published: true,
		CatalogRevision: 12, PolicyRevision: 4, OperationKey: "op-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.UpsertProductVariants(ctx, p17VariantsUpsertReq(product, upserted.ExternalProductID, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Variants) != 2 {
		t.Fatalf("one provider variant per variant: %v", result.Variants)
	}
	blueGID := "gid://shopify/ProductVariant/" + result.Variants[p17BlueVariantID]
	goldGID := "gid://shopify/ProductVariant/" + result.Variants[p17GoldVariantID]
	blueQuantity, goldQuantity := 4, 2
	if err := provider.SetVariantInventory(ctx, p17VariantInventoryReq("p1", upserted.ExternalProductID, p17BlueVariantID, result.Variants[p17BlueVariantID], int64(blueQuantity))); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetVariantInventory(ctx, p17VariantInventoryReq("p1", upserted.ExternalProductID, p17GoldVariantID, result.Variants[p17GoldVariantID], int64(goldQuantity))); err != nil {
		t.Fatal(err)
	}
	if got := h.variantQuantityOf(upserted.ExternalProductID, blueGID, h.locationGID); got != 4 {
		t.Fatalf("Blue must publish exactly 4, got %d", got)
	}
	if got := h.variantQuantityOf(upserted.ExternalProductID, goldGID, h.locationGID); got != 2 {
		t.Fatalf("Gold must publish exactly 2, got %d", got)
	}
	if h.casViolations != 0 {
		t.Fatal("compare-and-set must never be violated")
	}
	// SKU/price per variant (price override respected).
	for _, variant := range h.productOf(upserted.ExternalProductID).variants {
		switch variant.gid {
		case blueGID:
			if variant.sku != "PAP-BLUE" || variant.price != "650.00" {
				t.Fatalf("blue variant content: %q %q", variant.sku, variant.price)
			}
		case goldGID:
			if variant.sku != "PAP-GOLD" || variant.price != "700.00" {
				t.Fatalf("gold variant content: %q %q", variant.sku, variant.price)
			}
		default:
			t.Fatalf("unexpected variant %q", variant.gid)
		}
		if !variant.tracked {
			t.Fatal("variant variants must be inventory tracked")
		}
	}
	// A full re-sync keeps the exact per-variant quantities (idempotent).
	if _, err := provider.UpsertProduct(ctx, commerce.ProductUpsertRequest{
		ProviderKey: "shopify-main", ProductID: "p1",
		ExistingExternal: &commerce.ProviderProductRef{ExternalProductID: upserted.ExternalProductID},
		Product:          product, Published: true,
		CatalogRevision: 12, PolicyRevision: 4, OperationKey: "op-2",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpsertProductVariants(ctx, p17VariantsUpsertReq(product, upserted.ExternalProductID, result.Variants)); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetVariantInventory(ctx, p17VariantInventoryReq("p1", upserted.ExternalProductID, p17BlueVariantID, result.Variants[p17BlueVariantID], 4)); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetVariantInventory(ctx, p17VariantInventoryReq("p1", upserted.ExternalProductID, p17GoldVariantID, result.Variants[p17GoldVariantID], 2)); err != nil {
		t.Fatal(err)
	}
	if got := h.variantQuantityOf(upserted.ExternalProductID, blueGID, h.locationGID); got != 4 {
		t.Fatalf("re-sync must keep Blue at 4, got %d", got)
	}
	if got := h.variantQuantityOf(upserted.ExternalProductID, goldGID, h.locationGID); got != 2 {
		t.Fatalf("re-sync must keep Gold at 2, got %d", got)
	}
	if h.creations() != 1 {
		t.Fatalf("re-sync must never create a second product: %d", h.creations())
	}
}

// TestShopifyVariantFrameChoicesShareVariantPool: the Phase 15 bundle
// architecture keeps working — the single MoonLight variant maps to the
// tracked base-papyrus managed variant, and every frame choice (bundle
// option) consumes that SAME variant pool. Frame variants stay untracked
// and never carry stock.
func TestShopifyVariantFrameChoicesShareVariantPool(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	provider := newTestProvider(t, h)
	// Seed the Phase 15 representation: a tracked base component with the
	// managed variant, and the sellable bundle parent whose variants are
	// untracked frame choices.
	baseGID := "gid://shopify/Product/8800000000"
	managedGID := "gid://shopify/ProductVariant/9100000001"
	managedDecimal := "9100000001"
	bundleGID := "gid://shopify/Product/9000000001"
	frameVariantGID := "gid://shopify/ProductVariant/9200000001"
	h.seedProduct(&fakeProduct{
		gid: baseGID, status: "DRAFT",
		metafields: map[string]string{
			"moonlight.product_id":         "p1",
			"moonlight.provider_key":       "shopify-main",
			"moonlight.role":               roleBaseComponent,
			"moonlight.managed_variant_id": managedDecimal,
		},
		variants: []*fakeVariant{{
			gid: managedGID, sku: "PAP-BLUE", price: "650.00",
			itemGID: "gid://shopify/InventoryItem/9300000001", tracked: true,
			levels: map[string]int64{}, metafields: map[string]string{},
		}},
	})
	h.seedProduct(&fakeProduct{
		gid: bundleGID, status: "ACTIVE",
		metafields: map[string]string{
			"moonlight.product_id":        "p1",
			"moonlight.provider_key":      "shopify-main",
			"moonlight.role":              roleBundleParent,
			"moonlight.base_component_id": baseGID,
		},
		variants: []*fakeVariant{{
			gid: frameVariantGID, sku: "PAP-FRAME", price: "800.00",
			itemGID: "gid://shopify/InventoryItem/9300000002", tracked: false,
			levels: map[string]int64{}, metafields: map[string]string{},
		}},
	})

	product := p17Product("p1", p17Variant("blue", "PAP-BLUE", 4, nil))
	result, err := provider.UpsertProductVariants(ctx, p17VariantsUpsertReq(product, "9000000001", nil))
	if err != nil {
		t.Fatal(err)
	}
	// The variant maps to the tracked base managed variant (the pool).
	if result.Variants[p17BlueVariantID] != managedDecimal {
		t.Fatalf("single variant must map to the tracked base pool, got %v", result.Variants)
	}
	if err := provider.SetVariantInventory(ctx, p17VariantInventoryReq("p1", "9000000001", p17BlueVariantID, managedDecimal, 4)); err != nil {
		t.Fatal(err)
	}
	// One pool carries exactly the variant stock.
	if got := h.variantQuantityOf("8800000000", managedGID, h.locationGID); got != 4 {
		t.Fatalf("the variant pool must carry exactly 4, got %d", got)
	}
	// Frame choices stay untracked and stockless: they consume the SAME
	// pool through bundle derivation and never add stock.
	for _, variant := range h.productOf("9000000001").variants {
		if variant.tracked {
			t.Fatal("frame choices must stay untracked")
		}
		if got := h.variantQuantityOf("9000000001", variant.gid, h.locationGID); got != 0 {
			t.Fatalf("frame choices must never carry stock, got %d", got)
		}
	}
}

// TestShopifyVariantFrameCapabilityConflict: several physical variants
// AND frame options cannot layer safely — the adapter must fail with the
// stable capability code and never multiply stock.
func TestShopifyVariantFrameCapabilityConflict(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	provider := newTestProvider(t, h)
	product := p17Product("p1",
		p17Variant("blue", "PAP-BLUE", 4, nil),
		p17Variant("gold", "PAP-GOLD", 2, nil))
	product.Configurations = []commerce.CommerceConfiguration{{
		ConfigurationID: "22222222-2222-4222-8222-222222222222", Kind: "frame",
		StyleCode: "classic", StyleNameAR: "كلاسيك", ColorCode: "gold", ColorNameAR: "ذهبي",
		PriceDeltaEGPMinor: 1500, Enabled: true,
	}}
	_, err := provider.UpsertProductVariants(ctx, p17VariantsUpsertReq(product, "1000", nil))
	if err == nil {
		t.Fatal("variant + frame options must fail safely")
	}
	if !strings.Contains(err.Error(), CapabilityCode) {
		t.Fatalf("expected the stable capability code, got: %v", err)
	}
	for _, record := range h.recorded() {
		if strings.Contains(record.Query, "MoonlightVariantSet") || strings.Contains(record.Query, "MoonlightProductCreate") {
			t.Fatal("capability refusal must happen before any remote variant write")
		}
	}
}

// TestShopifyVariantMappingLossRecoveryNoDuplicate: losing durable
// variant mappings must never duplicate remote variants — ownership
// metadata re-adopts the same provider variants.
func TestShopifyVariantMappingLossRecoveryNoDuplicate(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	provider := newTestProvider(t, h)
	product := p17Product("p1",
		p17Variant("blue", "PAP-BLUE", 4, nil),
		p17Variant("gold", "PAP-GOLD", 2, nil))

	upserted, err := provider.UpsertProduct(ctx, commerce.ProductUpsertRequest{
		ProviderKey: "shopify-main", ProductID: "p1",
		Product: product, Published: true,
		CatalogRevision: 12, PolicyRevision: 4, OperationKey: "op-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := provider.UpsertProductVariants(ctx, p17VariantsUpsertReq(product, upserted.ExternalProductID, nil))
	if err != nil {
		t.Fatal(err)
	}
	// Durable mapping loss: the remote variants keep their ownership.
	second, err := provider.UpsertProductVariants(ctx, p17VariantsUpsertReq(product, upserted.ExternalProductID, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, variantID := range []string{p17BlueVariantID, p17GoldVariantID} {
		if first.Variants[variantID] != second.Variants[variantID] {
			t.Fatalf("mapping loss must re-adopt the same remote variant: %v -> %v",
				first.Variants[variantID], second.Variants[variantID])
		}
	}
	if got := len(h.productOf(upserted.ExternalProductID).variants); got != 2 {
		t.Fatalf("recovery must never duplicate remote variants: %d", got)
	}
	if h.creations() != 1 {
		t.Fatalf("recovery must never create products: %d", h.creations())
	}
}
