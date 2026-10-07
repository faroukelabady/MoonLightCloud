package woocommerce

// Phase 17 §33-§37: WooCommerce ProductVariant mapping tests. Physical
// inventory is VARIANT-specific and must never multiply: Blue 4 / Gold 2
// publishes 4 and 2; frame choices share the one variant pool and never
// add stock; unsafe variant+frame layering fails with the stable
// capability code instead of multiplying stock.

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

func testVariant(value, sku string, quantity int64) commerce.CommerceVariant {
	name := value
	return commerce.CommerceVariant{
		VariantID: map[string]string{"blue": p17BlueVariantID, "gold": p17GoldVariantID}[value],
		SKU:       sku,
		Attributes: []commerce.CommerceVariantAttribute{{
			DefinitionCode: "color", ValueCode: value, NameAR: name,
			DefinitionNameAR: "اللون",
		}},
		Active: true, AvailableQuantity: quantity, Ready: true, InventoryRevision: 9,
	}
}

func testVariantProduct(productID string, variants ...commerce.CommerceVariant) commerce.CommerceProduct {
	product := testProduct(productID)
	product.Variants = variants
	return product
}

func testVariantsUpsertReq(product commerce.CommerceProduct, existing map[string]string) commerce.ProductVariantsUpsertRequest {
	return commerce.ProductVariantsUpsertRequest{
		ProviderKey: testProviderKey, ProductID: product.ProductID,
		ExistingVariants: existing, Product: product, Published: true,
		CatalogRevision: product.CatalogRevision, PolicyRevision: product.PolicyRevision,
		OperationKey: commerce.VariantOperationKey(testProviderKey, product.ProductID,
			product.CatalogRevision, product.PolicyRevision, true, "vf", "vv"),
	}
}

func testVariantInventoryReq(productID, externalProduct, variantID, externalVariant string, quantity int64) commerce.VariantInventoryUpdateRequest {
	return commerce.VariantInventoryUpdateRequest{
		ProviderKey: testProviderKey, ProductID: productID, VariantID: variantID,
		ExternalProductID: externalProduct, ExternalVariantID: externalVariant,
		AvailableQuantity: quantity, InventoryRevision: 9,
		CatalogRevision: 12, PolicyRevision: 4, Ready: true,
		OperationKey: commerce.VariantInventoryOperationKey(testProviderKey, productID, variantID,
			12, 4, 9, quantity, true, true, "vf", "vv"),
	}
}

func variationState(t *testing.T, harness *wooHarness, productID, variationID int64) map[string]any {
	t.Helper()
	harness.mu.Lock()
	defer harness.mu.Unlock()
	if variations := harness.variations[productID]; variations != nil {
		if variation, ok := variations[variationID]; ok {
			return variation
		}
	}
	return nil
}

func variationCount(t *testing.T, harness *wooHarness, productID int64) int {
	t.Helper()
	harness.mu.Lock()
	defer harness.mu.Unlock()
	return len(harness.variations[productID])
}

// TestWooVariantVariationsTrackedPerVariantStock: several MoonLight
// variants map to Woo variations with SKU, effective price and
// manage_stock=true carrying the EXACT per-variant availability. The
// provider must see Blue 4 and Gold 2 — never 6/6, never any aggregate.
func TestWooVariantVariationsTrackedPerVariantStock(t *testing.T) {
	ctx := context.Background()
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	provider := testProvider(t, harness)
	product := testVariantProduct("p1",
		testVariant("blue", "PAP-BLUE", 4),
		testVariant("gold", "PAP-GOLD", 2))

	upserted, err := provider.UpsertProduct(ctx, testUpsertReq(product, true, nil))
	if err != nil {
		t.Fatal(err)
	}
	parent := testVariantsUpsertReq(product, nil)
	parent.ExternalProductID = upserted.ExternalProductID
	result, err := provider.UpsertProductVariants(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Variants) != 2 {
		t.Fatalf("one provider variation per variant: %v", result.Variants)
	}
	wooID, err := parseWooID(upserted.ExternalProductID)
	if err != nil {
		t.Fatal(err)
	}
	blueID, err := parseWooID(result.Variants[p17BlueVariantID])
	if err != nil {
		t.Fatal(err)
	}
	goldID, err := parseWooID(result.Variants[p17GoldVariantID])
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{blueID, goldID} {
		variation := variationState(t, harness, wooID, id)
		if variation == nil {
			t.Fatalf("variation %d missing", id)
		}
		if variation["manage_stock"] != true {
			t.Fatalf("variant variations must manage their own stock: %v", variation["manage_stock"])
		}
		if variation["stock_quantity"] != float64(0) {
			t.Fatalf("metadata writes must stay safe-zero: %v", variation["stock_quantity"])
		}
		if variation["sku"] == "" {
			t.Fatal("variant variation must carry its SKU")
		}
	}
	if variationState(t, harness, wooID, blueID)["sku"] != "PAP-BLUE" ||
		variationState(t, harness, wooID, goldID)["sku"] != "PAP-GOLD" {
		t.Fatal("variation SKU must equal the variant SKU")
	}
	// Per-variant inventory publish: exactly the variant availability.
	if err := provider.SetVariantInventory(ctx, testVariantInventoryReq("p1", upserted.ExternalProductID, p17BlueVariantID, result.Variants[p17BlueVariantID], 4)); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetVariantInventory(ctx, testVariantInventoryReq("p1", upserted.ExternalProductID, p17GoldVariantID, result.Variants[p17GoldVariantID], 2)); err != nil {
		t.Fatal(err)
	}
	blueStock := variationState(t, harness, wooID, blueID)["stock_quantity"]
	goldStock := variationState(t, harness, wooID, goldID)["stock_quantity"]
	if blueStock != float64(4) || goldStock != float64(2) {
		t.Fatalf("per-variant stock must publish 4/2 exactly, got %v/%v", blueStock, goldStock)
	}
	if variationCount(t, harness, wooID) != 2 {
		t.Fatalf("no extra variations: %d", variationCount(t, harness, wooID))
	}
	// The multi-variant parent keeps no pool that could double-count.
	parentState := harness.productState(wooID)
	if parentState["manage_stock"] != false {
		t.Fatalf("multi-variant parents must not manage a shared pool: %v", parentState["manage_stock"])
	}
}

// TestWooFrameVariationsShareVariantPool: a single-variant product's
// stock pool IS the parent pool — frame variations stay untracked
// shared-pool choices (§85) and never add stock. One pool, one publish.
func TestWooFrameVariationsShareVariantPool(t *testing.T) {
	ctx := context.Background()
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	provider := testProvider(t, harness)
	product := testVariantProduct("p1", testVariant("blue", "PAP-BLUE", 4))
	product.Configurations = []commerce.CommerceConfiguration{{
		ConfigurationID: "22222222-2222-4222-8222-222222222222", Kind: "frame",
		StyleCode: "classic", StyleNameAR: "كلاسيك", ColorCode: "gold", ColorNameAR: "ذهبي",
		PriceDeltaEGPMinor: 1500, Enabled: true,
	}}

	upserted, err := provider.UpsertProduct(ctx, testUpsertReq(product, true, nil))
	if err != nil {
		t.Fatal(err)
	}
	// Frame variations stay untracked shared-pool choices.
	frameWrite := false
	for _, record := range harness.recorded() {
		if record.Method == "POST" && strings.HasSuffix(record.Path, "/variations") {
			frameWrite = true
			if record.Body["manage_stock"] != false {
				t.Fatalf("frame variations must never manage stock: %v", record.Body["manage_stock"])
			}
			if _, tracked := record.Body["stock_quantity"]; tracked {
				t.Fatal("frame variations must never carry quantities")
			}
		}
	}
	if !frameWrite {
		t.Fatal("expected frame variation writes")
	}

	req := testVariantsUpsertReq(product, nil)
	req.ExternalProductID = upserted.ExternalProductID
	result, err := provider.UpsertProductVariants(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	// The single variant's pool is the parent pool.
	if result.Variants[p17BlueVariantID] != upserted.ExternalProductID {
		t.Fatalf("single variant maps to the shared pool: %v", result.Variants)
	}
	if err := provider.SetVariantInventory(ctx, testVariantInventoryReq("p1", upserted.ExternalProductID,
		p17BlueVariantID, result.Variants[p17BlueVariantID], 4)); err != nil {
		t.Fatal(err)
	}
	wooID, err := parseWooID(upserted.ExternalProductID)
	if err != nil {
		t.Fatal(err)
	}
	if stock := harness.productState(wooID)["stock_quantity"]; stock != float64(4) {
		t.Fatalf("the one variant pool must carry exactly the variant stock, got %v", stock)
	}
	// The frame choice adds no stock anywhere.
	stockWrites := 0
	for _, record := range harness.recorded() {
		if record.Method == "PUT" && record.Path == "/wp-json/wc/v3/products/"+upserted.ExternalProductID {
			if _, ok := record.Body["stock_quantity"]; ok {
				stockWrites++
			}
		}
	}
	if stockWrites != 1 {
		t.Fatalf("exactly one stock write (the variant pool), got %d", stockWrites)
	}
}

// TestWooVariantFrameCapabilityConflict: several physical variants AND
// frame options cannot layer safely in Woo — the adapter must fail with
// the stable capability code and never multiply stock.
func TestWooVariantFrameCapabilityConflict(t *testing.T) {
	ctx := context.Background()
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	provider := testProvider(t, harness)
	product := testVariantProduct("p1",
		testVariant("blue", "PAP-BLUE", 4),
		testVariant("gold", "PAP-GOLD", 2))
	product.Configurations = []commerce.CommerceConfiguration{{
		ConfigurationID: "22222222-2222-4222-8222-222222222222", Kind: "frame",
		StyleCode: "classic", StyleNameAR: "كلاسيك", ColorCode: "gold", ColorNameAR: "ذهبي",
		PriceDeltaEGPMinor: 1500, Enabled: true,
	}}

	req := testVariantsUpsertReq(product, nil)
	req.ExternalProductID = "500"
	_, err := provider.UpsertProductVariants(ctx, req)
	if err == nil {
		t.Fatal("variant + frame options must fail safely")
	}
	if !strings.Contains(err.Error(), WooVariantOptionsCapabilityCode) {
		t.Fatalf("expected the stable capability code, got: %v", err)
	}
	if count := variationCount(t, harness, 500); count != 0 {
		t.Fatalf("capability refusal must leave zero remote variations: %d", count)
	}
	// R14: the capability refusal yields ZERO provider mutations — no
	// write request of any kind reaches the provider.
	for _, record := range harness.recorded() {
		if record.Method != "GET" {
			t.Fatalf("capability refusal must yield zero provider mutations, saw %s %s", record.Method, record.Path)
		}
	}
	// The product path fails the same way before any remote write.
	if _, err := provider.UpsertProduct(ctx, testUpsertReq(product, true, nil)); err == nil ||
		!strings.Contains(err.Error(), WooVariantOptionsCapabilityCode) {
		t.Fatalf("product path must refuse the same unsafe layering: %v", err)
	}
	if len(harness.productState(500)) != 0 && variationCount(t, harness, 500) != 0 {
		t.Fatal("no remote state may be written for an unrepresentable product")
	}
}

// TestWooVariantMappingLossRecoveryNoDuplicate: a lost durable mapping
// re-adopts the owned remote variation by SKU + ownership metadata —
// never a duplicate create.
func TestWooVariantMappingLossRecoveryNoDuplicate(t *testing.T) {
	ctx := context.Background()
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	provider := testProvider(t, harness)
	product := testVariantProduct("p1",
		testVariant("blue", "PAP-BLUE", 4),
		testVariant("gold", "PAP-GOLD", 2))
	// The remote Blue variation exists with full ownership metadata (the
	// durable Cloud mapping was lost).
	harness.preload(500, map[string]any{
		"sku": "PAP-001", "type": wooTypeVariable,
		"meta_data": []any{
			map[string]any{"key": metaProductID, "value": "p1"},
			map[string]any{"key": metaProviderKey, "value": testProviderKey},
		},
	})
	harness.preloadVariation(500, 77, map[string]any{
		"sku": "PAP-BLUE", "manage_stock": true,
		"meta_data": []any{
			map[string]any{"key": metaVariantID, "value": p17BlueVariantID},
			map[string]any{"key": metaProductID, "value": "p1"},
			map[string]any{"key": metaProviderKey, "value": testProviderKey},
		},
	})

	req := testVariantsUpsertReq(product, nil)
	req.ExternalProductID = "500"
	result, err := provider.UpsertProductVariants(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Variants[p17BlueVariantID] != "77" {
		t.Fatalf("mapping loss must re-adopt the owned variation, got %v", result.Variants)
	}
	if count := variationCount(t, harness, 500); count != 2 {
		t.Fatalf("recovery must never duplicate remote variations: %d", count)
	}
	// A second pass keeps the same identities.
	again, err := provider.UpsertProductVariants(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if again.Variants[p17BlueVariantID] != "77" || variationCount(t, harness, 500) != 2 {
		t.Fatal("idempotent re-adoption")
	}
}

// TestWooTombstonedVariantVariationHidden: a remote variation of a
// removed/tombstoned MoonLight variant stops being sellable — hidden by
// withholding its price and safe-zeroed (mapping state is retained).
func TestWooTombstonedVariantVariationHidden(t *testing.T) {
	ctx := context.Background()
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	provider := testProvider(t, harness)
	product := testVariantProduct("p1",
		testVariant("blue", "PAP-BLUE", 4),
		testVariant("gold", "PAP-GOLD", 2))
	harness.preload(500, map[string]any{
		"sku": "PAP-001", "type": wooTypeVariable,
		"meta_data": []any{
			map[string]any{"key": metaProductID, "value": "p1"},
			map[string]any{"key": metaProviderKey, "value": testProviderKey},
		},
	})
	harness.preloadVariation(500, 79, map[string]any{
		"sku": "PAP-DEAD", "manage_stock": true, "stock_quantity": float64(9),
		"regular_price": "650.00",
		"meta_data": []any{
			map[string]any{"key": metaVariantID, "value": "99999999-9999-4999-8999-999999999999"},
			map[string]any{"key": metaProductID, "value": "p1"},
			map[string]any{"key": metaProviderKey, "value": testProviderKey},
		},
	})
	req := testVariantsUpsertReq(product, nil)
	req.ExternalProductID = "500"
	if _, err := provider.UpsertProductVariants(ctx, req); err != nil {
		t.Fatal(err)
	}
	dead := variationState(t, harness, 500, 79)
	if dead["regular_price"] != "" {
		t.Fatalf("dead variations must be hidden (price withheld): %q", dead["regular_price"])
	}
	if dead["stock_quantity"] != float64(0) {
		t.Fatalf("dead variations must be safe-zeroed: %v", dead["stock_quantity"])
	}
	if count := variationCount(t, harness, 500); count != 3 {
		t.Fatalf("mapping state is retained, never deleted: %d", count)
	}
}

// TestWooVariantForeignSKUNeverAdopted: a foreign same-SKU variation is
// never adopted and never overwritten — the adapter conflicts instead of
// creating an unsafe duplicate.
func TestWooVariantForeignSKUNeverAdopted(t *testing.T) {
	ctx := context.Background()
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	provider := testProvider(t, harness)
	product := testVariantProduct("p1",
		testVariant("blue", "PAP-BLUE", 4),
		testVariant("gold", "PAP-GOLD", 2))
	harness.preload(500, map[string]any{
		"sku": "PAP-001", "type": wooTypeVariable,
		"meta_data": []any{
			map[string]any{"key": metaProductID, "value": "p1"},
			map[string]any{"key": metaProviderKey, "value": testProviderKey},
		},
	})
	harness.preloadVariation(500, 78, map[string]any{
		"sku": "PAP-BLUE",
		"meta_data": []any{
			map[string]any{"key": metaProductID, "value": "someone-else"},
			map[string]any{"key": metaProviderKey, "value": testProviderKey},
		},
	})
	req := testVariantsUpsertReq(product, nil)
	req.ExternalProductID = "500"
	_, err := provider.UpsertProductVariants(ctx, req)
	if err == nil {
		t.Fatal("foreign same-SKU variations must never be adopted")
	}
	if count := variationCount(t, harness, 500); count != 1 {
		t.Fatalf("conflict must leave the foreign variation untouched: %d", count)
	}
}
