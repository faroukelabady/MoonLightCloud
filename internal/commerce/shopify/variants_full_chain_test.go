package shopify

// Phase 17-R0 (R14) full-chain proof on the fake Shopify harness:
// Product + Blue variant (stock 4) + Gold variant (stock 2) → variant
// sync → provider resources at EXACT per-variant stock (4/2 — never 6/6)
// → provider ORDER ingestion → normalized MoonLight online order snapshot
// carrying product + variant + frame selection identity → the order must
// NEVER mutate provider/Retail inventory (Blue still 4 / Gold still 2)
// and the frame choice must never multiply stock. The multi-variant ×
// frame publish combination stays refused
// (TestShopifyVariantFrameCapabilityConflict, retained limitation —
// ADR 0049).

import (
	"context"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

func TestShopifyVariantFullChainOrderNeverMutatesStock(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	provider := newTestProvider(t, h)
	product := p17Product("p1",
		p17Variant("blue", "PAP-BLUE", 4, nil),
		p17Variant("gold", "PAP-GOLD", 2, nil))

	// syncVariants leg.
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
	if err := provider.SetVariantInventory(ctx, p17VariantInventoryReq("p1", upserted.ExternalProductID, p17BlueVariantID, result.Variants[p17BlueVariantID], 4)); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetVariantInventory(ctx, p17VariantInventoryReq("p1", upserted.ExternalProductID, p17GoldVariantID, result.Variants[p17GoldVariantID], 2)); err != nil {
		t.Fatal(err)
	}
	blueGID := "gid://shopify/ProductVariant/" + result.Variants[p17BlueVariantID]
	goldGID := "gid://shopify/ProductVariant/" + result.Variants[p17GoldVariantID]
	assertStocks := func(what string) {
		t.Helper()
		if got := h.variantQuantityOf(upserted.ExternalProductID, blueGID, h.locationGID); got != 4 {
			t.Fatalf("%s: Blue must stay exactly 4, got %d", what, got)
		}
		if got := h.variantQuantityOf(upserted.ExternalProductID, goldGID, h.locationGID); got != 2 {
			t.Fatalf("%s: Gold must stay exactly 2, got %d", what, got)
		}
	}
	assertStocks("after publish")

	// Provider ORDER leg: the customer bought the Blue variant WITH a
	// frame selection (the provider records the choice on the line's
	// variant identity). Normalization must carry product + variant +
	// frame selection identity — and never write inventory.
	order := sampleOrder()
	lines := order["lineItems"].(map[string]any)["nodes"].([]any)
	line := lines[0].(map[string]any)
	line["sku"] = "PAP-BLUE"
	line["variant"] = map[string]any{"id": blueGID}
	line["product"] = map[string]any{"id": "gid://shopify/Product/" + upserted.ExternalProductID}
	h.preloadOrder("5231234567890", order)
	snapshot, err := provider.GetOrder(ctx, "5231234567890")
	if err != nil {
		t.Fatalf("order ingestion: %v", err)
	}
	if len(snapshot.Lines) != 1 {
		t.Fatalf("order lines: %+v", snapshot.Lines)
	}
	got := snapshot.Lines[0]
	if got.ProviderVariantID != result.Variants[p17BlueVariantID] {
		t.Fatalf("order line must carry the variant identity snapshot: %+v", got)
	}
	if got.ProviderConfigurationID != blueGID {
		t.Fatalf("order line must carry the frame/selection identity snapshot: %+v", got)
	}
	if got.ExternalProductID != upserted.ExternalProductID {
		t.Fatalf("order line must carry the product identity snapshot: %+v", got)
	}
	if got.SKU != "PAP-BLUE" {
		t.Fatalf("order line must carry the variant SKU snapshot: %+v", got)
	}
	if snapshot.MappingComplete {
		t.Fatal("resolution runs at projection time; adapter must not claim mapping completeness")
	}

	// Orders NEVER mutate inventory; frame choices never multiply stock.
	assertStocks("after order ingestion")
	if h.casViolations != 0 {
		t.Fatal("compare-and-set must never be violated")
	}
}
