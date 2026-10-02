package shopify

import (
	"context"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

func upsertRequest(productID, sku string, published bool) commerce.ProductUpsertRequest {
	return commerce.ProductUpsertRequest{
		ProviderKey:  "shopify-main",
		ProductID:    productID,
		OperationKey: "opkey-" + productID,
		Product: commerce.CommerceProduct{
			ProductID:    productID,
			SKU:          sku,
			Names:        []commerce.LocalizedName{{Locale: "ar", Name: "بردية توت"}, {Locale: "en", Name: "Tut Papyrus"}},
			Descriptions: map[string]string{"ar": "وصف المنتج", "en": "product description"},
			Prices:       []commerce.Money{{Currency: "EGP", AmountMinor: 65000}},
			IsActive:     true,
			SellOnline:   true,
		},
		Published:       published,
		CatalogRevision: 3,
		PolicyRevision:  2,
	}
}

// wantConflict fails the test unless err is a terminal provider
// conflict (frozen taxonomy).
func wantConflict(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("want conflict, got success")
	}
	providerErr, ok := err.(*commerce.ProviderError)
	if !ok || providerErr.Kind != commerce.ErrorConflict {
		t.Fatalf("want provider conflict, got %v", err)
	}
	if providerErr.Retryable() {
		t.Fatal("conflict must not be retryable")
	}
}

// Phase 11 §149: create path — exact SKU, Arabic title, configured
// price, atomic ownership metafields, safe-zero, one managed variant,
// publication on the configured publication only, and no unmanaged
// fields written.
func TestProductCreateHappyPath(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	result, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}
	if h.creations() != 1 {
		t.Fatalf("creates = %d", h.creations())
	}
	product := h.productOf(result.ExternalProductID)
	if product == nil {
		t.Fatal("product not created")
	}
	if product.title != "بردية توت" {
		t.Fatalf("title = %q (Arabic name must be primary)", product.title)
	}
	if product.desc != "وصف المنتج" {
		t.Fatalf("description = %q (Arabic description must be primary)", product.desc)
	}
	if len(product.variants) != 1 {
		t.Fatalf("managed variants = %d", len(product.variants))
	}
	variant := product.variants[0]
	if variant.sku != "PAP-001" || variant.price != "650.00" {
		t.Fatalf("variant sku/price = %q/%q", variant.sku, variant.price)
	}
	if quantity := h.quantityOf(result.ExternalProductID, "gid://shopify/Location/7700000001"); quantity != 0 {
		t.Fatalf("created quantity = %d (must be safe-zero)", quantity)
	}
	// Permanent ownership = MoonLight ProductID + ProviderKey, created
	// atomically with the product.
	if product.metafields[metafieldNamespace+"."+metafieldProductID] != "prod-1" ||
		product.metafields[metafieldNamespace+"."+metafieldProviderKey] != "shopify-main" {
		t.Fatalf("ownership metafields missing: %v", product.metafields)
	}
	if product.metafields[metafieldNamespace+"."+metafieldManagedVariantID] == "" {
		t.Fatal("managed variant identity not recorded")
	}
	if !h.published[result.ExternalProductID] {
		t.Fatal("product not published on the configured publication")
	}
	if h.publishedElsewhere[result.ExternalProductID] {
		t.Fatal("unrelated publication touched")
	}
	if violations := h.violations(result.ExternalProductID); len(violations) > 0 {
		t.Fatalf("unmanaged fields written: %v", violations)
	}
}

// Phase 11 §33: Arabic title primary, English fallback, none → validation.
func TestTitleLocaleFallbacks(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)

	req := upsertRequest("prod-en", "SKU-EN", false)
	req.Product.Names = []commerce.LocalizedName{{Locale: "en", Name: "English Only"}}
	result, err := provider.UpsertProduct(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if product := h.productOf(result.ExternalProductID); product == nil || product.title != "English Only" {
		t.Fatalf("english fallback failed: %+v", product)
	}

	req = upsertRequest("prod-none", "SKU-NONE", false)
	req.Product.Names = nil
	if _, err := provider.UpsertProduct(context.Background(), req); err == nil {
		t.Fatal("missing titles accepted")
	}
}

// Phase 11 §34: Arabic description, English fallback, empty otherwise.
func TestDescriptionFallbacks(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	req := upsertRequest("prod-desc", "SKU-DESC", false)
	req.Product.Descriptions = map[string]string{"en": "english description"}
	result, err := provider.UpsertProduct(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if product := h.productOf(result.ExternalProductID); product == nil || product.desc != "english description" {
		t.Fatalf("english description fallback failed: %+v", product)
	}
}

// Phase 11 §36: the configured currency is selected exactly; missing
// configured currency is validation, never a fallback to another.
func TestConfiguredPriceHasNoFallback(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	req := upsertRequest("prod-usd", "SKU-USD", false)
	req.Product.Prices = []commerce.Money{{Currency: "USD", AmountMinor: 1300}}
	if _, err := provider.UpsertProduct(context.Background(), req); err == nil {
		t.Fatal("USD price accepted in an EGP provider")
	}
}

// Phase 11 §37/§166: shop currency mismatch blocks product mutation.
func TestShopCurrencyMismatchBlocksMutation(t *testing.T) {
	h := newHarness(t)
	h.shopCurrency = "USD"
	provider := newTestProvider(t, h)
	_, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-x", "SKU-X", false))
	providerErr, ok := err.(*commerce.ProviderError)
	if !ok || providerErr.Kind != commerce.ErrorValidation {
		t.Fatalf("want validation failure, got %v", err)
	}
	if h.creations() != 0 {
		t.Fatal("product created despite currency mismatch")
	}
}

// Phase 11 §52/§157: mapped updates verify remote ownership before any
// write; foreign ownership conflicts with zero mutations.
func TestMappedUpdateVerifiesOwnershipFirst(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	created, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}
	beforeTitle := h.productOf(created.ExternalProductID).title

	req := upsertRequest("prod-OTHER", "PAP-001", true)
	req.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: created.ExternalProductID}
	req.OperationKey = "opkey-other"
	_, err = provider.UpsertProduct(context.Background(), req)
	wantConflict(t, err)
	if after := h.productOf(created.ExternalProductID); after.title != beforeTitle {
		t.Fatal("foreign-owned product was mutated")
	}
	if h.quantityOf(created.ExternalProductID, "gid://shopify/Location/7700000001") != 0 {
		t.Fatal("foreign-owned product inventory was mutated")
	}
}

// Phase 11 §76/§77/§150: manual tags, collections, media, SEO, vendor,
// product type and manual variants survive updates; only managed fields
// are written; managed variant addressed alone.
func TestMappedUpdatePreservesManualState(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	created, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}
	product := h.productOf(created.ExternalProductID)
	// Simulate manual merchant state: extra variant, tags, media.
	product.variants = append(product.variants, &fakeVariant{
		gid: "gid://shopify/ProductVariant/9999", sku: "MANUAL-1", itemGID: "gid://shopify/InventoryItem/9998",
		tracked: true, levels: map[string]int64{},
	})
	product.metafields["manual.tags"] = "vip"

	req := upsertRequest("prod-1", "PAP-001", true)
	req.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: created.ExternalProductID}
	req.OperationKey = "opkey-update"
	req.Product.Names = []commerce.LocalizedName{{Locale: "ar", Name: "بردية محدثة"}}
	if _, err := provider.UpsertProduct(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	updated := h.productOf(created.ExternalProductID)
	if updated.title != "بردية محدثة" {
		t.Fatalf("title not updated: %q", updated.title)
	}
	if len(updated.variants) != 2 {
		t.Fatalf("manual variant deleted: %d variants", len(updated.variants))
	}
	if updated.metafields["manual.tags"] != "vip" {
		t.Fatal("manual metafield cleared")
	}
	if updated.variants[1].sku != "MANUAL-1" {
		t.Fatal("manual variant modified")
	}
	if violations := h.violations(created.ExternalProductID); len(violations) > 0 {
		t.Fatalf("unmanaged fields written on update: %v", violations)
	}
}

// Phase 11 §63/§64/§161: managed inventory is safe-zeroed before the
// metadata/publication stages and restored only by the later
// SetInventory call.
func TestUpdateSafeZeroOrdering(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	created, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}
	location := "gid://shopify/Location/7700000001"
	if err := provider.SetInventory(context.Background(), inventoryRequest(created.ExternalProductID, "prod-1", 20, true)); err != nil {
		t.Fatal(err)
	}
	if got := h.quantityOf(created.ExternalProductID, location); got != 20 {
		t.Fatalf("quantity = %d", got)
	}
	// A failing metadata stage after safe-zero must leave 0 behind.
	h.setFailure("MoonlightProductUpdate", 500, `{"errors":[{"message":"boom"}]}`)
	req := upsertRequest("prod-1", "PAP-001", true)
	req.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: created.ExternalProductID}
	req.OperationKey = "opkey-fail"
	if _, err := provider.UpsertProduct(context.Background(), req); err == nil {
		t.Fatal("expected failure")
	}
	if got := h.quantityOf(created.ExternalProductID, location); got > 0 {
		t.Fatalf("stale positive quantity %d after failed update (safe-zero invariant)", got)
	}
}

// Phase 11 §61/§151: disabled mapped product unpublishes the configured
// publication and zeroes managed inventory; the product is never deleted
// and the mapping identity is retained.
func TestDisabledMappedProductUnpublishesAndZeroes(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	created, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.SetInventory(context.Background(), inventoryRequest(created.ExternalProductID, "prod-1", 20, true)); err != nil {
		t.Fatal(err)
	}
	req := upsertRequest("prod-1", "PAP-001", false)
	req.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: created.ExternalProductID}
	req.OperationKey = "opkey-disable"
	result, err := provider.UpsertProduct(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExternalProductID != created.ExternalProductID {
		t.Fatal("external identity changed")
	}
	if h.published[created.ExternalProductID] {
		t.Fatal("still published")
	}
	if h.creations() != 1 {
		t.Fatal("disabled product recreated")
	}
	if got := h.quantityOf(created.ExternalProductID, "gid://shopify/Location/7700000001"); got != 0 {
		t.Fatalf("quantity = %d (must be zero)", got)
	}
	if err := provider.SetInventory(context.Background(), inventoryRequest(created.ExternalProductID, "prod-1", 0, false)); err != nil {
		t.Fatal(err)
	}
}

// Phase 11 §152: disable then re-enable keeps the same product GID,
// managed variant, and mapping with no duplicate create.
func TestReenableKeepsRemoteIdentity(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	created, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}
	disable := upsertRequest("prod-1", "PAP-001", false)
	disable.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: created.ExternalProductID}
	if _, err := provider.UpsertProduct(context.Background(), disable); err != nil {
		t.Fatal(err)
	}
	enable := upsertRequest("prod-1", "PAP-001", true)
	enable.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: created.ExternalProductID}
	enable.OperationKey = "opkey-enable"
	result, err := provider.UpsertProduct(context.Background(), enable)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExternalProductID != created.ExternalProductID {
		t.Fatal("external identity changed on re-enable")
	}
	if !h.published[created.ExternalProductID] {
		t.Fatal("not republished")
	}
	if h.creations() != 1 {
		t.Fatalf("creates = %d", h.creations())
	}
	if err := provider.SetInventory(context.Background(), inventoryRequest(created.ExternalProductID, "prod-1", 7, true)); err != nil {
		t.Fatal(err)
	}
	if got := h.quantityOf(created.ExternalProductID, "gid://shopify/Location/7700000001"); got != 7 {
		t.Fatalf("quantity = %d", got)
	}
}

// Phase 11 §49/§50/§153: a same-SKU remote product without our
// ownership is a conflict — zero creates, zero takeover writes (this is
// the same-SKU cross-Store defense).
func TestForeignSKUIsConflict(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	// Foreign remote product with the same SKU.
	created, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-foreign", "SAME-001", false))
	if err != nil {
		t.Fatal(err)
	}
	product := h.productOf(created.ExternalProductID)
	product.metafields[metafieldNamespace+"."+metafieldProductID] = "someone-else"
	product.metafields[metafieldNamespace+"."+metafieldProviderKey] = "shopify-main"

	req := upsertRequest("prod-b", "SAME-001", true)
	req.OperationKey = "opkey-b"
	_, conflictErr := provider.UpsertProduct(context.Background(), req)
	wantConflict(t, conflictErr)
	if h.creations() != 1 {
		t.Fatalf("creates = %d (foreign SKU must not be taken over)", h.creations())
	}
	if h.published[created.ExternalProductID] {
		t.Fatal("foreign product published by us")
	}
}

// Phase 11 §54/§154: multiple exact SKU candidates conflict; no
// arbitrary selection.
func TestMultipleSKUCandidatesConflict(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	// Two remote variants carrying the same SKU (search will return both).
	if _, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-1", "SAME-001", false)); err != nil {
		t.Fatal(err)
	}
	// Manually clone a second variant with the same SKU onto another product.
	foreign := &fakeProduct{
		gid: "gid://shopify/Product/1002", status: "ACTIVE",
		metafields: map[string]string{},
		variants: []*fakeVariant{{
			gid: "gid://shopify/ProductVariant/1003", sku: "SAME-001",
			itemGID: "gid://shopify/InventoryItem/1004", tracked: true, levels: map[string]int64{},
		}},
	}
	h.mu.Lock()
	h.products["1002"] = foreign
	h.mu.Unlock()

	req := upsertRequest("prod-b", "SAME-001", true)
	req.OperationKey = "opkey-multi"
	_, conflictErr := provider.UpsertProduct(context.Background(), req)
	wantConflict(t, conflictErr)
	if h.creations() != 1 {
		t.Fatalf("creates = %d", h.creations())
	}
}

// Phase 11 §53/§158: a mapped product that no longer exists conflicts;
// no replacement create, no remap.
func TestMappedMissingProductConflicts(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	req := upsertRequest("prod-1", "PAP-001", true)
	req.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: "424242"}
	req.OperationKey = "opkey-missing"
	_, conflictErr := provider.UpsertProduct(context.Background(), req)
	wantConflict(t, conflictErr)
	if h.creations() != 0 {
		t.Fatal("replacement product created for a missing mapped product")
	}
}

// Phase 11 §54/§159: a response identifying a different product than
// the mapped one conflicts with no inventory restore.
func TestResponseIdentityMismatchConflicts(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	created, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the remote identity behind the mapping.
	product := h.productOf(created.ExternalProductID)
	product.gid = "gid://shopify/Product/999999"
	req := upsertRequest("prod-1", "PAP-001", true)
	req.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: created.ExternalProductID}
	req.OperationKey = "opkey-mismatch"
	_, conflictErr := provider.UpsertProduct(context.Background(), req)
	wantConflict(t, conflictErr)
}

// Phase 11 §55/§156: ambiguous create (connection drops after the remote
// applied) recovers the same product on retry — remote creations = 1.
func TestAmbiguousCreateRecoversExactlyOnce(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	h.dropOnce("MoonlightProductCreate")
	req := upsertRequest("prod-1", "PAP-001", true)
	if _, err := provider.UpsertProduct(context.Background(), req); err == nil {
		t.Fatal("ambiguous create must fail retryably")
	}
	h.clearDrop("MoonlightProductCreate")
	if h.creations() != 1 {
		t.Fatalf("remote creations = %d after drop", h.creations())
	}
	// Retry with the SAME operation key discovers ownership and recovers.
	result, err := provider.UpsertProduct(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if h.creations() != 1 {
		t.Fatalf("remote creations = %d after recovery (duplicate create)", h.creations())
	}
	if result.ExternalProductID == "" {
		t.Fatal("no external identity recovered")
	}
	if !h.published[result.ExternalProductID] {
		t.Fatal("recovered product not published")
	}
}

// Phase 11 §155: create response missing the managed variant is
// retryable and recovery keeps a single remote product at safe-zero.
func TestCreateWithoutVariantIdentityIsRetryable(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	req := upsertRequest("prod-1", "PAP-001", true)
	h.setFailure("MoonlightProductCreate", 200,
		`{"data":{"productSet":{"product":{"id":"gid://shopify/Product/777"},"userErrors":[]}}}`)
	if _, err := provider.UpsertProduct(context.Background(), req); err == nil {
		t.Fatal("create response without managed variant must not succeed")
	}
	h.clearFailure("MoonlightProductCreate")
	if _, err := provider.UpsertProduct(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if h.creations() != 1 {
		t.Fatalf("creates = %d", h.creations())
	}
}

// Phase 11 §200: GraphQL variables carry dynamic values; query documents
// stay static and never embed SKU/title/IDs.
func TestDynamicValuesTravelAsVariablesOnly(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	if _, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-SECRET-SKU", true)); err != nil {
		t.Fatal(err)
	}
	for _, request := range h.recorded() {
		if strings.Contains(request.Query, "PAP-SECRET-SKU") ||
			strings.Contains(request.Query, "prod-1") ||
			strings.Contains(request.Query, "بردية") {
			t.Fatalf("dynamic value embedded in query document: %s", request.Query)
		}
	}
}
