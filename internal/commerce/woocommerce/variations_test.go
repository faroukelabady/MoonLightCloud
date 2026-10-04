package woocommerce

// Phase 15 §163/§91/§92/§264.1-5: WooCommerce shared-stock and
// variation-ownership proofs against the recorded-request harness.

import (
	"context"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

func framedProduct(productID string) commerce.CommerceProduct {
	product := testProduct(productID)
	usd := int64(2500)
	product.Configurations = []commerce.CommerceConfiguration{
		{ConfigurationID: "11111111-0000-4000-8000-0000000000c1", Kind: "frame",
			StyleCode: "classic", StyleNameAR: "كلاسيكي", StyleNameEN: stringPtr("Classic"),
			ColorCode: "black", ColorNameAR: "أسود", ColorNameEN: stringPtr("Black"),
			PriceDeltaEGPMinor: 30000, PriceDeltaUSDMinor: &usd, Enabled: true, Position: 0, ConfigurationRevision: 1},
		{ConfigurationID: "11111111-0000-4000-8000-0000000000c2", Kind: "frame",
			StyleCode: "classic", StyleNameAR: "كلاسيكي", StyleNameEN: stringPtr("Classic"),
			ColorCode: "gold", ColorNameAR: "ذهبي", ColorNameEN: stringPtr("Gold"),
			PriceDeltaEGPMinor: 35000, PriceDeltaUSDMinor: &usd, Enabled: true, Position: 1, ConfigurationRevision: 1},
		{ConfigurationID: "11111111-0000-4000-8000-0000000000c3", Kind: "frame",
			StyleCode: "modern", StyleNameAR: "عصري", StyleNameEN: stringPtr("Modern"),
			ColorCode: "black", ColorNameAR: "أسود", ColorNameEN: stringPtr("Black"),
			PriceDeltaEGPMinor: 40000, PriceDeltaUSDMinor: &usd, Enabled: false, Position: 2, ConfigurationRevision: 2},
	}
	return product
}

func stringPtr(value string) *string { return &value }

// §163 (freeze-critical): base OnlineAvailable = 1 across No Frame and
// every frame choice must remain ONE physical unit. The Woo
// representation proves it structurally: the parent carries the only
// managed stock; every variation is manage_stock=false with NO quantity.
func TestWooSharedStockAcrossFrameChoices(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	provider := testProvider(t, harness)
	product := framedProduct("11111111-0000-4000-8000-0000000000aa")

	result, err := provider.UpsertProduct(context.Background(), commerce.ProductUpsertRequest{
		ProviderKey: provider.Key(), ProductID: product.ProductID,
		Product: product, Published: true, CatalogRevision: 1, PolicyRevision: 1,
		OperationKey: "op-key-shared-stock",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExternalProductID == "" {
		t.Fatal("parent identity missing")
	}

	// Parent is the ONE shared pool (official Woo semantics): the only
	// product-level payload manages stock as a variable product.
	sawParentPool := false
	for _, request := range harness.recorded() {
		if request.Method == "POST" && strings.HasSuffix(request.Path, "/products") {
			payload := request.Body
			if payload["manage_stock"] == true && payload["type"] == wooTypeVariable {
				sawParentPool = true
			}
		}
	}
	if !sawParentPool {
		t.Fatal("parent variable product must carry the single managed stock pool")
	}

	// Variations never carry quantities and never manage stock.
	for _, request := range harness.recorded() {
		if request.Method != "POST" || !strings.HasSuffix(request.Path, "/variations") {
			continue
		}
		payload := request.Body
		if payload["manage_stock"] != false {
			t.Fatalf("variation must not manage stock (would multiply the pool): %v", payload)
		}
		if _, exists := payload["stock_quantity"]; exists {
			t.Fatalf("variation must never carry a quantity: %v", payload)
		}
	}
	// 3 configurations + the implicit NO-FRAME choice (§18/§86).
	if len(result.Configurations) != 4 {
		t.Fatalf("expected identities for 3 configurations plus NO-FRAME, got %d", len(result.Configurations))
	}

	// The disabled configuration is hidden by withholding its price —
	// never deleted (§88/§63): mapping identity is retained.
	// A later SetInventory touches ONLY the parent.
	if err := provider.SetInventory(context.Background(), commerce.InventoryUpdateRequest{
		ProviderKey: provider.Key(), ProductID: product.ProductID,
		ExternalProductID: result.ExternalProductID,
		AvailableQuantity: 1, InventoryRevision: 1, Ready: true, OperationKey: "inv-key",
	}); err != nil {
		t.Fatal(err)
	}
	for _, request := range harness.recorded() {
		if request.Method == "PUT" && strings.Contains(request.Path, "/variations/") {
			t.Fatal("inventory must never target a variation (shared parent pool only)")
		}
	}
}

// §91/§92 (mandatory): a create whose response is lost must recover the
// same owned remote variation on retry — no duplicate, no label-based
// takeover.
func TestWooVariationMappingLossRecovery(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	provider := testProvider(t, harness)
	product := framedProduct("11111111-0000-4000-8000-0000000000bb")

	// First pass: variation creates succeed but responses are dropped
	// (ambiguous create with lost response, §92).
	harness.setDropCreate(true)
	_, err := provider.UpsertProduct(context.Background(), commerce.ProductUpsertRequest{
		ProviderKey: provider.Key(), ProductID: product.ProductID,
		Product: product, Published: true, CatalogRevision: 1, PolicyRevision: 1,
		OperationKey: "op-key-lost-response",
	})
	if err != nil {
		// The parent create is also dropped; adoption recovery may still
		// succeed. Either way no duplicate may exist afterwards.
		t.Logf("first pass reported: %v", err)
	}
	harness.setDropCreate(false)

	// Retry converges on the SAME owned variations.
	result, err := provider.UpsertProduct(context.Background(), commerce.ProductUpsertRequest{
		ProviderKey: provider.Key(), ProductID: product.ProductID,
		Product: product, Published: true, CatalogRevision: 1, PolicyRevision: 1,
		OperationKey: "op-key-lost-response",
	})
	if err != nil {
		t.Fatal(err)
	}
	creates := 0
	for _, request := range harness.recorded() {
		if request.Method == "POST" && strings.HasSuffix(request.Path, "/variations") {
			creates++
		}
	}
	// Ownership metadata proves identity: exactly one creation attempt
	// per configuration (plus NO-FRAME) across both passes combined.
	if creates > 4 {
		t.Fatalf("duplicate remote variations created: %d creates", creates)
	}
	if len(result.Configurations) != 4 {
		t.Fatalf("recovered identities missing: %v", result.Configurations)
	}
}
