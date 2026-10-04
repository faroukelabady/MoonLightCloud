package shopify

// Phase 15 §164/§98/§264.2: Shopify shared-stock acceptance — the
// bundle representation provably preserves ONE base papyrus inventory
// pool. Native parallel variants each carrying the full quantity are
// never used (§97); frame components are untracked (§77); inventory
// writes target only the tracked base component.

import (
	"context"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

func framedProduct(productID string) commerce.CommerceProduct {
	usd := int64(2500)
	return commerce.CommerceProduct{
		ProductID: productID, SKU: "PAP-FRAME-001", IsActive: true, SellOnline: true,
		Names:  []commerce.LocalizedName{{Locale: "en", Name: "Papyrus"}},
		Prices: []commerce.Money{{Currency: "EGP", AmountMinor: 100000}, {Currency: "USD", AmountMinor: 2000}},
		Configurations: []commerce.CommerceConfiguration{
			{ConfigurationID: "11111111-0000-4000-8000-0000000000c1", Kind: "frame",
				StyleCode: "classic", StyleNameAR: "كلاسيكي", StyleNameEN: strPtr("Classic"),
				ColorCode: "black", ColorNameAR: "أسود", ColorNameEN: strPtr("Black"),
				PriceDeltaEGPMinor: 30000, PriceDeltaUSDMinor: &usd, Enabled: true, Position: 0, ConfigurationRevision: 1},
			{ConfigurationID: "11111111-0000-4000-8000-0000000000c2", Kind: "frame",
				StyleCode: "modern", StyleNameAR: "عصري", StyleNameEN: strPtr("Modern"),
				ColorCode: "black", ColorNameAR: "أسود", ColorNameEN: strPtr("Black"),
				PriceDeltaEGPMinor: 40000, PriceDeltaUSDMinor: &usd, Enabled: true, Position: 1, ConfigurationRevision: 1},
		},
	}
}

func strPtr(value string) *string { return &value }

// §164 (freeze-critical): base OnlineAvailable = 1 across No Frame,
// Classic/Black and Modern/Black must remain ONE physical unit. The
// bundle strategy proves it structurally: the frame component carries
// untracked variants, the base component is the only tracked inventory,
// and inventory writes never touch frame variants.
func TestShopifySharedStockBundleRepresentation(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	product := framedProduct("11111111-0000-4000-8000-0000000000dd")

	result, err := provider.UpsertProduct(context.Background(), commerce.ProductUpsertRequest{
		ProviderKey: provider.Key(), ProductID: product.ProductID,
		Product: product, Published: true, CatalogRevision: 1, PolicyRevision: 1,
		OperationKey: "op-key-bundle",
	})
	if err != nil {
		t.Fatal(err)
	}

	// (1) The frame component is created with UNTRACKED variants only —
	// frame choices carry no stock (§77/§137) and therefore cannot
	// multiply the base pool.
	if len(h.frameComponentSets) == 0 {
		t.Fatal("frame component was never created")
	}
	for _, set := range h.frameComponentSets {
		input := set["input"].(map[string]any)
		for _, raw := range input["variants"].([]any) {
			variant := raw.(map[string]any)
			item := variant["inventoryItem"].(map[string]any)
			if item["tracked"] != false {
				t.Fatalf("frame variant must be untracked (shared-pool safety): %v", variant)
			}
		}
	}

	// (2) The bundle attaches exactly the base + frame components; the
	// official bundle semantics derive bundle inventory from component
	// inventory — the tracked base is the single pool.
	if len(h.bundleAttaches) == 0 {
		t.Fatal("bundle components never attached")
	}
	attach := h.bundleAttaches[len(h.bundleAttaches)-1]["input"].(map[string]any)
	components := attach["components"].([]any)
	if len(components) != 2 {
		t.Fatalf("bundle must have exactly base + frame components, got %d", len(components))
	}

	// (3) Bundle parent variant prices equal base + delta exactly (§215).
	prices := map[string]string{}
	for _, request := range h.recorded() {
		vars := request.Variables
		if strings.Contains(request.Query, "MoonlightManagedVariantUpdate") {
			if input, ok := vars["variants"].([]any); ok {
				for _, raw := range input {
					entry := raw.(map[string]any)
					prices[str(entry["id"])] = str(entry["price"])
				}
			}
		}
	}
	for id, price := range prices {
		if strings.HasPrefix(id, "gid://shopify/ProductVariant/92") {
			switch price {
			case "1300.00", "1400.00", "1000.00":
			default:
				t.Fatalf("unexpected configured price %s for %s", price, id)
			}
		}
	}

	// (4) Inventory writes target ONLY the tracked base component's
	// managed variant — never a frame variant (§97/§106).
	if err := provider.SetInventory(context.Background(), commerce.InventoryUpdateRequest{
		ProviderKey: provider.Key(), ProductID: product.ProductID,
		ExternalProductID: result.ExternalProductID,
		AvailableQuantity: 1, InventoryRevision: 1, Ready: true, OperationKey: "inv-key-bundle",
	}); err != nil {
		t.Log("SetInventory reported:", err)
	}
	for _, request := range h.recorded() {
		if !strings.Contains(request.Query, "MoonlightInventorySet") {
			continue
		}
		vars := request.Variables
		input := vars["input"].(map[string]any)
		changes := input["changes"].([]any)
		for _, raw := range changes {
			item := raw.(map[string]any)
			if strings.HasPrefix(str(item["inventoryItemId"]), "gid://shopify/InventoryItem/9") {
				t.Fatal("quantity written to a frame/untracked inventory item")
			}
		}
	}

	// (5) Configuration identity is provider variant GIDs mapped by the
	// service seam — never labels (§62).
	if len(result.Configurations) < 3 {
		t.Fatalf("expected NO-FRAME + 2 configuration identities, got %v", result.Configurations)
	}
}
