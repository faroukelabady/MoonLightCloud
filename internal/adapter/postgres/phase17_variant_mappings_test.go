package postgres

// Phase 17 §33-§37: durable ProductVariant commerce mappings and the
// immutable ONLINE order-line variant snapshot (00034).

import (
	"context"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
)

// TestCommerceProductVariantMappingLifecycle proves the durable variant
// mapping contract: same-pair idempotency, conflicting remap refusal,
// external-identity reuse refusal, and the read surfaces.
func TestCommerceProductVariantMappingLifecycle(t *testing.T) {
	env := openSaleEnv(t)
	store := catalogStore(env)
	ctx := context.Background()
	_, prodID := seedCatalogProduct(t, env, 600, "ML-P-6")
	vid := catalogIDs(t, 620, "v")["v"]

	created, err := store.CreateProductVariantMapping(ctx, "website", prodID, vid, "500", "77")
	if err != nil {
		t.Fatal(err)
	}
	if created.VariantID != vid || created.ExternalProductID != "500" || created.ExternalVariantID != "77" {
		t.Fatalf("created: %+v", created)
	}
	// Same pair is idempotent.
	again, err := store.CreateProductVariantMapping(ctx, "website", prodID, vid, "500", "77")
	if err != nil || again.ExternalVariantID != "77" {
		t.Fatalf("idempotent replay: %+v %v", again, err)
	}
	// Same variant, different external variation: conflict (never remap).
	if _, err := store.CreateProductVariantMapping(ctx, "website", prodID, vid, "500", "78"); !commerce.IsMappingConflict(err) {
		t.Fatalf("remap must conflict: %v", err)
	}
	// Same external pair, different variant: conflict (never merge).
	other := catalogIDs(t, 621, "v")["v"]
	if _, err := store.CreateProductVariantMapping(ctx, "website", prodID, other, "500", "77"); !commerce.IsMappingConflict(err) {
		t.Fatalf("external reuse must conflict: %v", err)
	}
	// Independent per provider instance (Woo + Shopify same product).
	shopify, err := store.CreateProductVariantMapping(ctx, "shopify-main", prodID, vid, "9000000001", "9100000001")
	if err != nil || shopify.ExternalVariantID != "9100000001" {
		t.Fatalf("independent provider mapping: %+v %v", shopify, err)
	}
	// Read surfaces.
	got, err := store.GetProductVariantMapping(ctx, "website", vid)
	if err != nil || got.ExternalVariantID != "77" {
		t.Fatalf("get: %+v %v", got, err)
	}
	list, err := store.ListProductVariantMappings(ctx, "website", prodID)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %+v %v", list, err)
	}
	found, err := store.FindProductVariantMappingByExternal(ctx, "website", "500", "77")
	if err != nil || found.VariantID != vid {
		t.Fatalf("find by external: %+v %v", found, err)
	}
	if _, err := store.FindProductVariantMappingByExternal(ctx, "website", "500", "nope"); err == nil {
		t.Fatal("unknown external variation must not resolve")
	}
}

// TestOrderLineVariantSnapshot proves the Phase 17 order ingestion
// contract: provider variation → durable variant mapping → immutable
// MoonLight variant identity snapshot captured at ingestion (00034).
func TestOrderLineVariantSnapshot(t *testing.T) {
	env := openSaleEnv(t)
	store := catalogStore(env)
	ctx := context.Background()
	_, prodID := seedCatalogProduct(t, env, 630, "ML-P-7")
	vid := catalogIDs(t, 650, "v")["v"]
	rev1 := variantEventID(660)
	ingestCatalog(t, env, rev1, catalog.EventProductVariantSnapshotV1, "2026-09-20T10:00:00Z",
		variantPayload(vid, prodID, "ML-V-7", true, false, "color=blue",
			[]any{variantAttr("color", "blue", 0)}, 1))
	if res := projectCatalogOnce(t, env, rev1); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("variant: %+v", res)
	}
	if _, err := store.CreateProductMapping(ctx, "website", prodID, "500"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProductVariantMapping(ctx, "website", prodID, vid, "500", "77"); err != nil {
		t.Fatal(err)
	}

	snapshot := orderTestSnapshot("website", "300", orders.StatusPending, "pending", 10000)
	snapshot.Lines = []orders.OrderLine{
		{ExternalLineID: 1, ExternalProductID: "500", VariationID: 77, ProviderVariantID: "77",
			SKU: "ML-V-7", Name: "X", Quantity: 1, TotalMinor: 10000},
	}
	if _, changed := reconcileSnapshot(t, env, snapshot); !changed {
		t.Fatal("initial projection must change")
	}
	stored, _, found, err := store.LoadProjectedOrder(ctx, "website", "300")
	if err != nil || !found {
		t.Fatal("stored")
	}
	line := stored.Lines[0]
	if line.VariantID == nil || *line.VariantID != vid {
		t.Fatalf("resolved variant identity: %+v", line)
	}
	if line.VariantSKU == nil || *line.VariantSKU != "ML-V-7" {
		t.Fatalf("variant SKU snapshot: %+v", line)
	}
	if len(line.VariantAttributeSnapshot) != 1 ||
		line.VariantAttributeSnapshot[0].ValueCode != "blue" ||
		line.VariantAttributeSnapshot[0].DefinitionCode != "color" {
		t.Fatalf("attribute snapshot: %+v", line.VariantAttributeSnapshot)
	}
	// The snapshot is immutable: a later catalog rename never rewrites
	// the purchase-time capture.
	rev2 := variantEventID(661)
	ingestCatalog(t, env, rev2, catalog.EventProductVariantSnapshotV1, "2026-09-20T11:00:00Z",
		variantPayload(vid, prodID, "ML-V-7-RENAMED", true, false, "color=blue",
			[]any{variantAttr("color", "blue", 0)}, 2))
	if res := projectCatalogOnce(t, env, rev2); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("rename: %+v", res)
	}
	snapshot.Lines[0].Name = "X renamed"
	if _, changed := reconcileSnapshot(t, env, snapshot); !changed {
		t.Fatal("line change must project")
	}
	stored, _, found, err = store.LoadProjectedOrder(ctx, "website", "300")
	if err != nil || !found {
		t.Fatal("stored")
	}
	if *stored.Lines[0].VariantSKU != "ML-V-7" {
		t.Fatalf("history must keep the purchase-time SKU, got %q", *stored.Lines[0].VariantSKU)
	}
}

// TestOrderLineVariantFrameLayerFallback: a frame-layer selection (an
// unresolved provider variation) on a product with exactly ONE mapped
// variant adopts that variant's identity — every frame choice consumes
// that one variant's stock pool. Products with several variants never
// guess: the unresolved variation stays truthful NULL.
func TestOrderLineVariantFrameLayerFallback(t *testing.T) {
	env := openSaleEnv(t)
	store := catalogStore(env)
	ctx := context.Background()
	_, prodID := seedCatalogProduct(t, env, 670, "ML-P-8")
	vid := catalogIDs(t, 690, "v")["v"]
	rev1 := variantEventID(700)
	ingestCatalog(t, env, rev1, catalog.EventProductVariantSnapshotV1, "2026-09-20T10:00:00Z",
		variantPayload(vid, prodID, "ML-V-8", true, false, "color=blue", nil, 1))
	if res := projectCatalogOnce(t, env, rev1); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("variant: %+v", res)
	}
	if _, err := store.CreateProductMapping(ctx, "website", prodID, "500"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProductVariantMapping(ctx, "website", prodID, vid, "500", "77"); err != nil {
		t.Fatal(err)
	}
	// Frame-layer variation 88 has no variant mapping.
	snapshot := orderTestSnapshot("website", "301", orders.StatusPending, "pending", 10000)
	snapshot.Lines = []orders.OrderLine{
		{ExternalLineID: 1, ExternalProductID: "500", VariationID: 88, ProviderVariantID: "88",
			SKU: "PAP-FRAME", Name: "X", Quantity: 1, TotalMinor: 10000},
	}
	if _, changed := reconcileSnapshot(t, env, snapshot); !changed {
		t.Fatal("initial projection must change")
	}
	stored, _, found, err := store.LoadProjectedOrder(ctx, "website", "301")
	if err != nil || !found {
		t.Fatal("stored")
	}
	if stored.Lines[0].VariantID == nil || *stored.Lines[0].VariantID != vid {
		t.Fatalf("single-variant frame layer must resolve the variant pool: %+v", stored.Lines[0])
	}

	// A second variant makes provider variations ambiguous: nothing is
	// guessed.
	vid2 := catalogIDs(t, 691, "v")["v"]
	rev2 := variantEventID(701)
	ingestCatalog(t, env, rev2, catalog.EventProductVariantSnapshotV1, "2026-09-20T10:00:00Z",
		variantPayload(vid2, prodID, "ML-V-8B", true, false, "color=gold", nil, 1))
	if res := projectCatalogOnce(t, env, rev2); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("variant2: %+v", res)
	}
	if _, err := store.CreateProductVariantMapping(ctx, "website", prodID, vid2, "500", "78"); err != nil {
		t.Fatal(err)
	}
	snapshot = orderTestSnapshot("website", "302", orders.StatusPending, "pending", 10000)
	snapshot.Lines = []orders.OrderLine{
		{ExternalLineID: 1, ExternalProductID: "500", VariationID: 89, ProviderVariantID: "89",
			SKU: "PAP-FRAME", Name: "X", Quantity: 1, TotalMinor: 10000},
	}
	if _, changed := reconcileSnapshot(t, env, snapshot); !changed {
		t.Fatal("initial projection must change")
	}
	stored, _, found, err = store.LoadProjectedOrder(ctx, "website", "302")
	if err != nil || !found {
		t.Fatal("stored")
	}
	if stored.Lines[0].VariantID != nil {
		t.Fatalf("multi-variant products must never guess: %+v", stored.Lines[0])
	}
}
