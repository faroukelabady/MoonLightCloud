package woocommerce

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// Woo's SKU namespace includes both products and product_variations.
// The older shared fake intentionally omits this constraint; applying
// it here prevents a passing adapter test from hiding a real rejection.
func enforceWooGlobalSKU(h *wooHarness) {
	h.setIntercept(func(request wooRecordedRequest) (int, any, bool) {
		if request.Method != http.MethodPost && request.Method != http.MethodPut {
			return 0, nil, false
		}
		sku, hasSKU := request.Body["sku"].(string)
		if !hasSKU || sku == "" {
			return 0, nil, false
		}
		rest := strings.TrimPrefix(request.Path, "/wp-json/wc/v3/products")
		parentID, childID := int64(0), int64(0)
		if request.Method == http.MethodPut {
			if match := variationRoute.FindStringSubmatch(rest); match != nil {
				childID, _ = strconv.ParseInt(match[2], 10, 64)
			} else {
				parentID, _ = strconv.ParseInt(strings.Trim(rest, "/"), 10, 64)
			}
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		duplicate := false
		for id, product := range h.products {
			stored, _ := product["sku"].(string)
			if id != parentID && strings.EqualFold(stored, sku) {
				duplicate = true
			}
		}
		for _, variations := range h.variations {
			for id, variation := range variations {
				stored, _ := variation["sku"].(string)
				if id != childID && strings.EqualFold(stored, sku) {
					duplicate = true
				}
			}
		}
		if duplicate {
			return http.StatusBadRequest, map[string]any{
				"code": "product_invalid_sku", "message": "Invalid or duplicated SKU",
				"data": map[string]any{"status": http.StatusBadRequest},
			}, true
		}
		return 0, nil, false
	})
}

func TestWooFreshPhysicalVariantsUseSeparateParentSKU(t *testing.T) {
	h := newWooHarness(t, testConsumerKey, testConsumerSec)
	enforceWooGlobalSKU(h)
	p := testProvider(t, h)
	product := physicalParentProduct("strict-fresh", true)
	parent, mapped := convergePhysicalParent(t, p, product, "", nil)
	parentID, _ := parseWooID(parent)
	if sku := h.productState(parentID)["sku"]; sku != wooPhysicalParentSKU(testProviderKey, product.ProductID) {
		t.Fatalf("provider parent integration identity: %v", sku)
	}
	if len(mapped.Variants) != 2 || variationCount(t, h, parentID) != 2 {
		t.Fatal("exactly two physical child variations must converge under global SKU uniqueness")
	}
	publishPhysicalQuantities(t, p, product, parent, mapped.Variants)
	assertPhysicalQuantity(t, h, parentID, mapped.Variants[p17BlueVariantID], 4)
	assertPhysicalQuantity(t, h, parentID, mapped.Variants[p17GoldVariantID], 2)
}

func TestWooPhysicalParentTransitionRestartAndMappingLoss(t *testing.T) {
	h := newWooHarness(t, testConsumerKey, testConsumerSec)
	enforceWooGlobalSKU(h)
	p := testProvider(t, h)
	single := physicalParentProduct("strict-transition", false)
	parent, first := convergePhysicalParent(t, p, single, "", nil)
	if first.Variants[p17BlueVariantID] != parent {
		t.Fatal("fresh single Variant must use one parent inventory pool")
	}
	publishPhysicalQuantities(t, p, single, parent, first.Variants)
	two := physicalParentProduct(single.ProductID, true)
	_, multiple := convergePhysicalParent(t, p, two, parent, first.Variants)
	if multiple.Transitions[p17BlueVariantID] != parent || multiple.Variants[p17BlueVariantID] == parent {
		t.Fatalf("forward transition must name the expected old parent: %+v", multiple)
	}
	publishPhysicalQuantities(t, p, two, parent, multiple.Variants)
	// A new adapter with lost local mappings must recover the stable
	// parent and exact owned children; no replacement resource is created.
	p = testProvider(t, h)
	recoveredParent, recovered := convergePhysicalParent(t, p, two, "", nil)
	if recoveredParent != parent || recovered.Variants[p17BlueVariantID] != multiple.Variants[p17BlueVariantID] ||
		recovered.Variants[p17GoldVariantID] != multiple.Variants[p17GoldVariantID] {
		t.Fatal("mapping loss after shape transition must recover identical identities")
	}
	_, back := convergePhysicalParent(t, p, single, parent, recovered.Variants)
	if back.Variants[p17BlueVariantID] != multiple.Variants[p17BlueVariantID] || len(back.Transitions) != 0 {
		t.Fatal("multiple-to-single must retain the owned child's identity")
	}
	publishPhysicalQuantities(t, p, single, parent, back.Variants)
	// Mapping loss after N->1 still finds the provider parent by its
	// stable integration SKU, not the physical SKU now owned by a child.
	p = testProvider(t, h)
	afterParent, after := convergePhysicalParent(t, p, single, "", nil)
	if afterParent != parent || after.Variants[p17BlueVariantID] != multiple.Variants[p17BlueVariantID] {
		t.Fatal("single remaining Variant must recover the retained child shape")
	}
	publishPhysicalQuantities(t, p, single, afterParent, after.Variants)
	parentID, _ := parseWooID(parent)
	if len(h.products) != 1 || variationCount(t, h, parentID) != 2 {
		t.Fatal("transitions/restarts must not duplicate or delete provider resources")
	}
	state := h.productState(parentID)
	if state["type"] != wooTypeVariable || state["manage_stock"] != false || state["stock_quantity"] != float64(0) {
		t.Fatalf("retained parent must have no physical pool: %v", state)
	}
	assertPhysicalQuantity(t, h, parentID, multiple.Variants[p17BlueVariantID], 4)
	assertPhysicalQuantity(t, h, parentID, multiple.Variants[p17GoldVariantID], 0)
}

func TestWooRetainedPhysicalChildrenRejectFramesBeforeWrites(t *testing.T) {
	h := newWooHarness(t, testConsumerKey, testConsumerSec)
	enforceWooGlobalSKU(h)
	p := testProvider(t, h)
	two := physicalParentProduct("strict-retained-frames", true)
	parent, _ := convergePhysicalParent(t, p, two, "", nil)
	single := physicalParentProduct(two.ProductID, false)
	single.Configurations = []commerce.CommerceConfiguration{{
		ConfigurationID: "11111111-0000-4000-8000-0000000000c1", Kind: "frame",
		StyleCode: "classic", StyleNameAR: "كلاسيكي", ColorCode: "black", ColorNameAR: "أسود",
		PriceDeltaEGPMinor: 1500, Enabled: true,
	}}
	for _, mapped := range []bool{true, false} {
		before := len(h.recorded())
		req := testUpsertReq(single, true, nil)
		if mapped {
			req.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: parent}
		}
		if _, err := p.UpsertProduct(context.Background(), req); err == nil || !strings.Contains(err.Error(), WooVariantOptionsCapabilityCode) {
			t.Fatalf("retained physical children plus frames must fail explicitly: %v", err)
		}
		for _, request := range h.recorded()[before:] {
			if request.Method != http.MethodGet {
				t.Fatalf("capability refusal must happen before any provider write: %s %s", request.Method, request.Path)
			}
		}
	}
}

func physicalParentProduct(id string, multiple bool) commerce.CommerceProduct {
	product := testVariantProduct(id, testVariant("blue", "PAP-BLUE", 4))
	if multiple {
		product.Variants = append(product.Variants, testVariant("gold", "PAP-GOLD", 2))
	}
	// Match the production CatalogCommerceSource exactly. Its provider
	// presentation field mirrors the first physical Variant SKU.
	product.SKU = product.Variants[0].SKU
	return product
}

func convergePhysicalParent(t *testing.T, provider *WooCommerceProvider, product commerce.CommerceProduct, parent string, existing map[string]string) (string, commerce.ProductVariantsUpsertResult) {
	t.Helper()
	req := testUpsertReq(product, true, nil)
	if parent != "" {
		req.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: parent}
	}
	upserted, err := provider.UpsertProduct(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	variants := testVariantsUpsertReq(product, existing)
	variants.ExternalProductID = upserted.ExternalProductID
	mapped, err := provider.UpsertProductVariants(context.Background(), variants)
	if err != nil {
		t.Fatal(err)
	}
	return upserted.ExternalProductID, mapped
}

func publishPhysicalQuantities(t *testing.T, provider *WooCommerceProvider, product commerce.CommerceProduct, parent string, mapped map[string]string) {
	t.Helper()
	for _, variant := range product.Variants {
		if err := provider.SetVariantInventory(context.Background(), testVariantInventoryReq(product.ProductID, parent,
			variant.VariantID, mapped[variant.VariantID], variant.AvailableQuantity)); err != nil {
			t.Fatal(err)
		}
	}
}

func assertPhysicalQuantity(t *testing.T, h *wooHarness, parentID int64, child string, quantity int64) {
	t.Helper()
	childID, _ := parseWooID(child)
	state := variationState(t, h, parentID, childID)
	if state == nil || state["stock_quantity"] != float64(quantity) {
		t.Fatalf("child %s quantity: %v, want %d", child, state, quantity)
	}
}
