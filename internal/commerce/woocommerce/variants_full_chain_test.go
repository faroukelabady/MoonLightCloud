package woocommerce

// Phase 17-R0 (R14) full-chain proof on the fake WooCommerce harness:
// Product + Blue variant (stock 4) + Gold variant (stock 2) → variant
// sync (UpsertProductVariants + SetVariantInventory) → provider
// resources at EXACT per-variant stock (4/2 — never 6/6, never
// multiplied) → provider ORDER ingestion → normalized MoonLight online
// order snapshot carrying the variant identity → the order must NEVER
// mutate provider/Retail inventory (Blue still 4 / Gold still 2) and no
// frame choice may multiply stock. The multi-variant × frame publish
// combination stays refused (TestWooVariantFrameCapabilityConflict,
// retained limitation — ADR 0049).

import (
	"context"
	"strconv"
	"strings"
	"testing"
)

func TestWooVariantFullChainOrderNeverMutatesStock(t *testing.T) {
	ctx := context.Background()
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	provider := testProvider(t, harness)
	product := testVariantProduct("p1",
		testVariant("blue", "PAP-BLUE", 4),
		testVariant("gold", "PAP-GOLD", 2))

	// syncVariants leg: parent + per-variant provider resources.
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
	if err := provider.SetVariantInventory(ctx, testVariantInventoryReq("p1", upserted.ExternalProductID,
		p17BlueVariantID, result.Variants[p17BlueVariantID], 4)); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetVariantInventory(ctx, testVariantInventoryReq("p1", upserted.ExternalProductID,
		p17GoldVariantID, result.Variants[p17GoldVariantID], 2)); err != nil {
		t.Fatal(err)
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
	assertStocks := func(what string) {
		t.Helper()
		blueStock := variationState(t, harness, wooID, blueID)["stock_quantity"]
		goldStock := variationState(t, harness, wooID, goldID)["stock_quantity"]
		if blueStock != float64(4) || goldStock != float64(2) {
			t.Fatalf("%s: per-variant stock must stay exactly 4/2, got %v/%v", what, blueStock, goldStock)
		}
	}
	assertStocks("after publish")

	// Provider ORDER leg: a customer bought the Blue variation. The
	// adapter normalizes it into the MoonLight online order snapshot
	// carrying the variant identity — and must never write inventory.
	order := wooOrderFixture(100, "processing")
	lines := order["line_items"].([]any)
	lines[0].(map[string]any)["variation_id"] = float64(blueID)
	lines[0].(map[string]any)["product_id"] = float64(wooID)
	lines[0].(map[string]any)["sku"] = "PAP-BLUE"
	harness.preloadOrder(100, order)
	snapshot, err := provider.GetOrder(ctx, "100")
	if err != nil {
		t.Fatalf("order ingestion: %v", err)
	}
	if len(snapshot.Lines) != 1 {
		t.Fatalf("order lines: %+v", snapshot.Lines)
	}
	line := snapshot.Lines[0]
	if line.ProviderVariantID != strconv.FormatInt(blueID, 10) || line.VariationID != blueID {
		t.Fatalf("order line must carry the variant identity snapshot: %+v", line)
	}
	if line.SKU != "PAP-BLUE" {
		t.Fatalf("order line must carry the variant SKU snapshot: %+v", line)
	}

	// Orders NEVER mutate inventory: the exact per-variant stock stands.
	assertStocks("after order ingestion")
	inventoryWrites := 0
	for _, record := range harness.recorded() {
		if record.Method == "PUT" && strings.Contains(record.Path, "/variations/") {
			if _, ok := record.Body["stock_quantity"]; ok {
				inventoryWrites++
			}
		}
	}
	if inventoryWrites != 2 {
		t.Fatalf("exactly one stock write per variant (never from orders), got %d", inventoryWrites)
	}
}
