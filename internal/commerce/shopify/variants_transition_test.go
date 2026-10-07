package shopify

// Phase 17-R0 R08: 1↔N↔1 variant transitions must leave NO inert stale
// managed_variant_id metafield and never duplicate provider resources.
// The multi-variant shape durably tombstones the product-level
// managed-variant identity (empty metafieldsSet value); the
// single-variant shape (re)stamps it. Variant-scoped moonlight.variant_id
// stamps are the ONLY adoption proof in variant paths — a stale
// managed_variant_id can never drive adoption or mutation when N > 1.

import (
	"context"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

const (
	managedKey = metafieldNamespace + "." + metafieldManagedVariantID
	stampKey   = metafieldNamespace + "." + metafieldVariantID
)

func p17ManagedOf(t *testing.T, h *shopifyHarness, externalProduct string) string {
	t.Helper()
	product := h.productOf(externalProduct)
	if product == nil {
		t.Fatalf("product %s missing", externalProduct)
	}
	return product.metafields[managedKey]
}

// p17TransitionBase creates the single-variant shape (product flow plus
// variant reconciliation) and returns the external identity and the
// durable-style variant map.
func p17TransitionBase(t *testing.T, h *shopifyHarness, provider *ShopifyProvider, productID string) (string, map[string]string) {
	t.Helper()
	ctx := context.Background()
	single := p17Product(productID, p17Variant("blue", "PAP-BLUE", 4, nil))
	upserted, err := provider.UpsertProduct(ctx, commerce.ProductUpsertRequest{
		ProviderKey: "shopify-main", ProductID: productID,
		Product: single, Published: true,
		CatalogRevision: 12, PolicyRevision: 4, OperationKey: "op-" + productID,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.UpsertProductVariants(ctx, p17VariantsUpsertReq(single, upserted.ExternalProductID, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := p17ManagedOf(t, h, upserted.ExternalProductID); got == "" {
		t.Fatal("single-variant shape must record the managed variant identity")
	}
	return upserted.ExternalProductID, result.Variants
}

func p17GID(decimal string) string { return "gid://shopify/ProductVariant/" + decimal }

// TestShopifyTransitionOneToNTombstonesManagedVariant: the 1→N shape
// change durably clears the managed-variant identity and stamps every
// variant; repeats stay idempotent (no duplicates, no extra writes of a
// non-empty managed id).
func TestShopifyTransitionOneToNTombstonesManagedVariant(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	external, single := p17TransitionBase(t, h, provider, "t1")

	two := p17Product("t1",
		p17Variant("blue", "PAP-BLUE", 4, nil),
		p17Variant("gold", "PAP-GOLD", 2, nil))
	result, err := provider.UpsertProductVariants(context.Background(), p17VariantsUpsertReq(two, external, single))
	if err != nil {
		t.Fatal(err)
	}
	if got := p17ManagedOf(t, h, external); got != "" {
		t.Fatalf("managed_variant_id must be tombstoned after 1→N, got %q", got)
	}
	if len(result.Variants) != 2 {
		t.Fatalf("two mappings: %v", result.Variants)
	}
	product := h.productOf(external)
	if len(product.variants) != 2 {
		t.Fatalf("no duplicate provider resources: %d variants", len(product.variants))
	}
	for _, variant := range product.variants {
		if variant.metafields[stampKey] == "" {
			t.Fatalf("every owned variant carries the variant-scoped stamp: %+v", variant)
		}
	}
	// Repeat converge: still two variants, tombstone intact.
	if _, err := provider.UpsertProductVariants(context.Background(), p17VariantsUpsertReq(two, external, result.Variants)); err != nil {
		t.Fatal(err)
	}
	if got := p17ManagedOf(t, h, external); got != "" {
		t.Fatalf("repeat must keep the tombstone, got %q", got)
	}
	if len(h.productOf(external).variants) != 2 {
		t.Fatal("repeat duplicated provider resources")
	}
}

// TestShopifyTransitionNToOneRepairsManagedVariant: the N→1 shape change
// re-adopts the exact-SKU variant (never a duplicate) and (re)stamps the
// managed-variant identity so product-level managed flows stay coherent.
func TestShopifyTransitionNToOneRepairsManagedVariant(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	external, single := p17TransitionBase(t, h, provider, "t1")

	two := p17Product("t1",
		p17Variant("blue", "PAP-BLUE", 4, nil),
		p17Variant("gold", "PAP-GOLD", 2, nil))
	nResult, err := provider.UpsertProductVariants(context.Background(), p17VariantsUpsertReq(two, external, single))
	if err != nil {
		t.Fatal(err)
	}
	back := p17Product("t1", p17Variant("blue", "PAP-BLUE", 4, nil))
	oneResult, err := provider.UpsertProductVariants(context.Background(), p17VariantsUpsertReq(back, external, nResult.Variants))
	if err != nil {
		t.Fatal(err)
	}
	blueDecimal := oneResult.Variants[p17BlueVariantID]
	if blueDecimal != nResult.Variants[p17BlueVariantID] {
		t.Fatalf("N→1 must keep the same remote variant: %q vs %q", blueDecimal, nResult.Variants[p17BlueVariantID])
	}
	if got := p17ManagedOf(t, h, external); got != blueDecimal {
		t.Fatalf("managed_variant_id repaired to %q want %q", got, blueDecimal)
	}
	if len(h.productOf(external).variants) != 2 {
		t.Fatal("N→1 must never delete remote variants (tombstone semantics are Cloud-side)")
	}
}

// TestShopifyTransitionOneNOneWithRestart: full 1→N→1 with a FRESH
// adapter instance between every step (restart between transitions) —
// identity survives only through durable remote metadata, and no step
// duplicates a provider resource.
func TestShopifyTransitionOneNOneWithRestart(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	external, single := p17TransitionBase(t, h, provider, "t1")

	// Restart before 1→N.
	provider = newTestProvider(t, h)
	two := p17Product("t1",
		p17Variant("blue", "PAP-BLUE", 4, nil),
		p17Variant("gold", "PAP-GOLD", 2, nil))
	nResult, err := provider.UpsertProductVariants(context.Background(), p17VariantsUpsertReq(two, external, single))
	if err != nil {
		t.Fatal(err)
	}
	if got := p17ManagedOf(t, h, external); got != "" {
		t.Fatalf("tombstone after restart 1→N: %q", got)
	}

	// Restart before N→1.
	provider = newTestProvider(t, h)
	back := p17Product("t1", p17Variant("blue", "PAP-BLUE", 4, nil))
	oneResult, err := provider.UpsertProductVariants(context.Background(), p17VariantsUpsertReq(back, external, nResult.Variants))
	if err != nil {
		t.Fatal(err)
	}
	if oneResult.Variants[p17BlueVariantID] != nResult.Variants[p17BlueVariantID] {
		t.Fatal("restart across transitions duplicated the provider variant")
	}
	if got := p17ManagedOf(t, h, external); got != oneResult.Variants[p17BlueVariantID] {
		t.Fatalf("managed identity after 1→N→1: %q", got)
	}
	if len(h.productOf(external).variants) != 2 {
		t.Fatal("transitions duplicated provider resources")
	}
	if h.casViolations != 0 {
		t.Fatal("compare-and-set must never be violated")
	}
}

// TestShopifyTransitionCrashBeforeTombstoneIsRepaired: a stale
// managed_variant_id left by a crash between the shape change and its
// cleanup is durably tombstoned by the next multi-variant reconcile, and
// product-level inventory never drives the stale variant.
func TestShopifyTransitionCrashBeforeTombstoneIsRepaired(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	external, single := p17TransitionBase(t, h, provider, "t1")
	blueGID := p17GID(single[p17BlueVariantID])

	// Simulate crash-after-shape-change-before-cleanup: the stale
	// managed identity still points at the old managed variant.
	stale := h.productOf(external)
	stale.metafields[managedKey] = single[p17BlueVariantID]

	two := p17Product("t1",
		p17Variant("blue", "PAP-BLUE", 4, nil),
		p17Variant("gold", "PAP-GOLD", 2, nil))
	if _, err := provider.UpsertProductVariants(context.Background(), p17VariantsUpsertReq(two, external, single)); err != nil {
		t.Fatal(err)
	}
	if got := p17ManagedOf(t, h, external); got != "" {
		t.Fatalf("stale managed identity must be tombstoned on reconcile, got %q", got)
	}
	// Product-level inventory refuses the multi-variant shape BEFORE any
	// mutation: the stale identity is never read, nothing moves.
	err := provider.SetInventory(context.Background(), inventoryRequest(external, "t1", 9, true))
	if err == nil {
		t.Fatal("product-level inventory must refuse the multi-variant shape")
	}
	if got := h.variantQuantityOf(external, blueGID, testLocationGID); got != 0 {
		t.Fatalf("stale managed variant quantity moved: %d", got)
	}
	for _, variant := range h.productOf(external).variants {
		if variant.gid != blueGID {
			if got := h.variantQuantityOf(external, variant.gid, testLocationGID); got != 0 {
				t.Fatalf("foreign variant quantity moved: %d", got)
			}
		}
	}
}

// TestShopifyMappingLossNeverAdoptsViaManagedVariant: with N > 1, lost
// durable mappings are recovered through the variant-scoped
// moonlight.variant_id stamps (and exact-SKU ownership) — a stale
// managed_variant_id pointing at a DIFFERENT variant can never hijack
// adoption, and recovery never duplicates a provider variant.
func TestShopifyMappingLossNeverAdoptsViaManagedVariant(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	external, single := p17TransitionBase(t, h, provider, "t1")

	two := p17Product("t1",
		p17Variant("blue", "PAP-BLUE", 4, nil),
		p17Variant("gold", "PAP-GOLD", 2, nil))
	nResult, err := provider.UpsertProductVariants(context.Background(), p17VariantsUpsertReq(two, external, single))
	if err != nil {
		t.Fatal(err)
	}
	// Stale managed identity points at GOLD while the desired BLUE must
	// resolve through its own variant-scoped stamp.
	h.productOf(external).metafields[managedKey] = nResult.Variants[p17GoldVariantID]

	recovered, err := provider.UpsertProductVariants(context.Background(), p17VariantsUpsertReq(two, external, nil))
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Variants[p17BlueVariantID] != nResult.Variants[p17BlueVariantID] {
		t.Fatalf("blue must adopt its stamped variant, got %q want %q",
			recovered.Variants[p17BlueVariantID], nResult.Variants[p17BlueVariantID])
	}
	if recovered.Variants[p17GoldVariantID] != nResult.Variants[p17GoldVariantID] {
		t.Fatalf("gold must adopt its stamped variant, got %q", recovered.Variants[p17GoldVariantID])
	}
	if len(h.productOf(external).variants) != 2 {
		t.Fatal("mapping-loss recovery duplicated provider resources")
	}
	if got := p17ManagedOf(t, h, external); got != "" {
		t.Fatalf("stale managed identity must be tombstoned by recovery, got %q", got)
	}
}
