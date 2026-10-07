package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestWireParityMoonLightRetailFixtures is the Cloud side of the Phase
// 17-R0 cross-repository wire-parity pin (R13): the committed fixtures
// were captured from MoonLightRetail's REAL marshal functions
// (internal/domain/sync/wire_parity_test.go — the Nefertiti shape) and
// must decode AND validate through the real ingestion decoders here.
// Retail asserts byte-stability against the same files; any drift in
// field names, bounds, or shapes breaks one side's suite.
func loadWireFixture(t *testing.T, name string) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return json.RawMessage(raw)
}

func TestWireParityProductV2(t *testing.T) {
	raw := loadWireFixture(t, "wire_product_v2.json")
	decoded, err := DecodeProductSnapshotV2(raw)
	if err != nil {
		t.Fatalf("decode product v2 fixture: %v", err)
	}
	valid, err := ValidateProductSnapshotV2(decoded)
	if err != nil {
		t.Fatalf("validate product v2 fixture: %v", err)
	}
	if valid.ProductID != "77777777-0000-4000-8000-000000000001" || valid.SKU != "" {
		t.Fatalf("product v2 parity broken: id=%q sku=%q", valid.ProductID, valid.SKU)
	}
	if valid.Name != "نفرتيتي" || valid.CatalogRevision != 1 {
		t.Fatalf("product v2 identity parity broken: name=%q rev=%d", valid.Name, valid.CatalogRevision)
	}
	if len(valid.Prices) != 2 || len(valid.Translations) != 2 {
		t.Fatalf("product v2 relations parity broken: prices=%d translations=%d", len(valid.Prices), len(valid.Translations))
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if _, present := wire["sku"]; present {
		t.Fatal("wire fixture must carry no sku field (Phase 17-R0)")
	}
	if _, present := wire["primary_variant_sku"]; present {
		t.Fatal("wire fixture must omit the retired primary_variant_sku mirror")
	}
}

func TestWireParityProductVariantV1(t *testing.T) {
	raw := loadWireFixture(t, "wire_product_variant_v1.json")
	decoded, err := DecodeProductVariantSnapshot(raw)
	if err != nil {
		t.Fatalf("decode variant fixture: %v", err)
	}
	valid, err := ValidateProductVariantSnapshot(decoded)
	if err != nil {
		t.Fatalf("validate variant fixture: %v", err)
	}
	if valid.VariantID != "77777777-0000-4000-8000-000000000002" || valid.SKU != "NEF-BLU-TRA" {
		t.Fatalf("variant identity parity broken: id=%q sku=%q", valid.VariantID, valid.SKU)
	}
	if valid.StockQuantity != 4 || valid.VariantRevision != 1 || valid.CatalogRevision != 1 {
		t.Fatalf("variant state parity broken: stock=%d variant_rev=%d catalog_rev=%d",
			valid.StockQuantity, valid.VariantRevision, valid.CatalogRevision)
	}
	if valid.PriceEGPCents == nil || *valid.PriceEGPCents != 100000 || valid.PriceUSDCents != nil {
		t.Fatalf("variant money parity broken: egp=%v usd=%v", valid.PriceEGPCents, valid.PriceUSDCents)
	}
	if len(valid.Attributes) != 2 || valid.Attributes[0].DefinitionCode != "color" || valid.Attributes[1].DefinitionCode != "painting_style" {
		t.Fatalf("variant attribute parity broken: %+v", valid.Attributes)
	}
	normalized := NormalizeProductVariantSnapshot(valid)
	if normalized.CombinationKey != "color=blue\u001fpainting_style=traditional" {
		t.Fatalf("combination key parity broken: %q", normalized.CombinationKey)
	}
}

func TestWireParityProductVariantInventoryV1(t *testing.T) {
	raw := loadWireFixture(t, "wire_product_variant_inventory_v1.json")
	decoded, err := DecodeProductVariantInventorySnapshot(raw)
	if err != nil {
		t.Fatalf("decode variant inventory fixture: %v", err)
	}
	valid, err := ValidateProductVariantInventorySnapshot(decoded)
	if err != nil {
		t.Fatalf("validate variant inventory fixture: %v", err)
	}
	if valid.VariantID != "77777777-0000-4000-8000-000000000002" || valid.SKU != "NEF-BLU-TRA" {
		t.Fatalf("variant inventory identity parity broken: id=%q sku=%q", valid.VariantID, valid.SKU)
	}
	// Blue stock is 4 — the cross-repo invariant (never 6, never a
	// product aggregate).
	if valid.StockQuantity != 4 || valid.InventoryRevision != 1 {
		t.Fatalf("variant inventory parity broken: stock=%d inv_rev=%d", valid.StockQuantity, valid.InventoryRevision)
	}
	if !valid.Ready || valid.SellOnline || valid.OnlineAllocationLimit != nil {
		t.Fatalf("variant inventory mirror parity broken: %+v", valid)
	}
	if valid.PolicyRevision != 1 || valid.CatalogRevision != 1 {
		t.Fatalf("variant inventory context parity broken: policy=%d catalog=%d", valid.PolicyRevision, valid.CatalogRevision)
	}
}
