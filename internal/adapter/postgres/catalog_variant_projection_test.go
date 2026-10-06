package postgres

// Phase 17 variant projection integration tests (real PostgreSQL):
// revision gates (stale = terminal no-op), equal-revision semantic
// convergence, strict Store isolation incl. legacy NULL rules, tombstone
// retention, duplicate-combination tolerance for unscoped rows, the
// variant inventory stream's dependency wait, and the variant catalog
// health codes.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

func variantEventID(n int) string {
	return fmt.Sprintf("e%07x-0000-4000-8000-%012x", n, n)
}

func variantAttr(definitionCode, valueCode string, position int) map[string]any {
	return map[string]any{
		"definition_code": definitionCode, "value_code": valueCode,
		"name_ar": "أزرق", "name_en": "Blue",
		"definition_name_ar": "اللون", "definition_name_en": "Color",
		"position": position,
	}
}

func variantPayload(variantID, productID, sku string, active, deleted bool, combinationKey string, attrs []any, variantRevision int64) string {
	raw, _ := json.Marshal(map[string]any{
		"variant_id": variantID, "product_id": productID, "sku": sku,
		"is_active": active, "deleted": deleted,
		"price_egp_cents": 123456, "price_usd_cents": nil,
		"stock_quantity": 4, "inventory_revision": 7,
		"variant_revision": variantRevision, "position": 0,
		"combination_key": combinationKey,
		"attributes":      attrs, "catalog_revision": 9,
	})
	return string(raw)
}

func variantInventoryPayload(variantID, productID, sku string, stock int, invRev int64, sellOnline bool, allocation *int) string {
	raw, _ := json.Marshal(map[string]any{
		"variant_id": variantID, "product_id": productID, "sku": sku,
		"stock_quantity": stock, "inventory_revision": invRev,
		"ready": true, "sell_online": sellOnline,
		"online_allocation_limit": allocation, "policy_revision": 2, "catalog_revision": 9,
	})
	return string(raw)
}

// seedCatalogProduct projects one root category and one product under
// the unscoped fixture and returns their IDs.
func seedCatalogProduct(t *testing.T, env *saleEnv, base int, sku string) (string, string) {
	t.Helper()
	ids := catalogIDs(t, base, "cat", "prod")
	catEvent := variantEventID(base + 1)
	ingestCatalog(t, env, catEvent, catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
		categoryPayload(ids["cat"], "active", map[string]string{"ar": "قسم"}, nil, 1))
	if res := projectCatalogOnce(t, env, catEvent); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("category: %+v", res)
	}
	prodEvent := variantEventID(base + 2)
	ingestCatalog(t, env, prodEvent, catalog.EventProductSnapshotV1, "2026-09-20T10:00:00Z",
		productPayload(ids["prod"], sku, "منتج", ids["cat"], nil, nil, 1))
	if res := projectCatalogOnce(t, env, prodEvent); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("product: %+v", res)
	}
	return ids["cat"], ids["prod"]
}

// TestCatalogVariantProjectionLifecycle proves the variant catalog
// stream: higher revision replaces, stale is a terminal no-op, equal
// revision converges idempotently on identical state and blocks on
// conflicting state, attributes are replaced per revision.
func TestCatalogVariantProjectionLifecycle(t *testing.T) {
	env := openSaleEnv(t)
	_, prodID := seedCatalogProduct(t, env, 500, "ML-P-1")
	vid := catalogIDs(t, 520, "v")["v"]

	rev1 := variantEventID(530)
	ingestCatalog(t, env, rev1, catalog.EventProductVariantSnapshotV1, "2026-09-20T10:00:00Z",
		variantPayload(vid, prodID, "ML-V-1", true, false, "color=blue", []any{variantAttr("color", "blue", 0)}, 1))
	if res := projectCatalogOnce(t, env, rev1); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("rev1: %+v", res)
	}
	assertVariant := func(sku string, wantAttrs int, revision int64) {
		t.Helper()
		var gotSKU, deleted string
		var gotRev int64
		if err := env.pool.QueryRow(context.Background(),
			`SELECT sku, deleted::text, variant_revision FROM catalog_product_variants WHERE variant_id=$1`,
			vid).Scan(&gotSKU, &deleted, &gotRev); err != nil {
			t.Fatal(err)
		}
		if gotSKU != sku || gotRev != revision {
			t.Fatalf("variant state sku=%q rev=%d, want %q/%d", gotSKU, gotRev, sku, revision)
		}
		var attrs int
		if err := env.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM catalog_product_variant_attribute_values WHERE variant_id=$1`,
			vid).Scan(&attrs); err != nil || attrs != wantAttrs {
			t.Fatalf("attributes: %d (%v), want %d", attrs, err, wantAttrs)
		}
	}
	assertVariant("ML-V-1", 1, 1)

	// Equal revision + different state: conflicting block.
	conflict := variantEventID(532)
	ingestCatalog(t, env, conflict, catalog.EventProductVariantSnapshotV1, "2026-09-20T10:06:00Z",
		variantPayload(vid, prodID, "ML-CONFLICT", false, false, "color=blue", nil, 1))
	if res := projectCatalogOnce(t, env, conflict); res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrCatalogRevisionConflict {
		t.Fatalf("equal conflicting: %+v", res)
	}
	assertVariant("ML-V-1", 1, 1)

	// Equal revision + identical state converges as an idempotent no-op.
	again := variantEventID(533)
	ingestCatalog(t, env, again, catalog.EventProductVariantSnapshotV1, "2026-09-20T10:07:00Z",
		variantPayload(vid, prodID, "ML-V-1", true, false, "color=blue", []any{variantAttr("color", "blue", 0)}, 1))
	if res := projectCatalogOnce(t, env, again); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("equal identical: %+v", res)
	}

	// Higher revision replaces the whole aggregate (sku + attributes).
	rev2 := variantEventID(534)
	ingestCatalog(t, env, rev2, catalog.EventProductVariantSnapshotV1, "2026-09-20T10:08:00Z",
		variantPayload(vid, prodID, "ML-V-2", false, false, "color=red\u001fstyle=classic",
			[]any{variantAttr("color", "red", 0), variantAttr("style", "classic", 1)}, 2))
	if res := projectCatalogOnce(t, env, rev2); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("rev2: %+v", res)
	}
	assertVariant("ML-V-2", 2, 2)

	// Stale revision is a terminal no-op: it never rewinds state.
	stale := variantEventID(535)
	ingestCatalog(t, env, stale, catalog.EventProductVariantSnapshotV1, "2026-09-20T10:09:00Z",
		variantPayload(vid, prodID, "ML-STALE", false, false, "color=blue", nil, 1))
	if res := projectCatalogOnce(t, env, stale); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("stale: %+v", res)
	}
	assertVariant("ML-V-2", 2, 2)
}

// TestCatalogVariantStoreIsolation proves strict Store isolation: a
// scoped event never writes another Store's variant, legacy events never
// wipe proven ownership, and a scoped event adopts a legacy row by
// revision continuity (00023 conventions).
func TestCatalogVariantStoreIsolation(t *testing.T) {
	f := openScopeFixture(t)
	ids := catalogIDs(t, 600, "catA", "prodA", "v1", "v2", "v3", "catL", "prodL")

	// Store A product graph.
	catEvent := variantEventID(610)
	f.ingest(t, f.devA, f.credA, catEvent, catalog.EventCategorySnapshotV1,
		categoryPayload(ids["catA"], "active", map[string]string{"ar": "قسم"}, nil, 1))
	if res := projectCatalogTerminal(t, f, catEvent, catalog.EventCategorySnapshotV1); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("category A: %+v", res)
	}
	prodEvent := variantEventID(611)
	f.ingest(t, f.devA, f.credA, prodEvent, catalog.EventProductSnapshotV1,
		productPayload(ids["prodA"], "ML-A-1", "منتج", ids["catA"], nil, nil, 1))
	if res := projectCatalogTerminal(t, f, prodEvent, catalog.EventProductSnapshotV1); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("product A: %+v", res)
	}

	// Store A variant.
	v1a := variantEventID(612)
	f.ingest(t, f.devA, f.credA, v1a, catalog.EventProductVariantSnapshotV1,
		variantPayload(ids["v1"], ids["prodA"], "ML-A-V1", true, false, "color=blue", nil, 1))
	if res := projectCatalogTerminal(t, f, v1a, catalog.EventProductVariantSnapshotV1); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("variant A: %+v", res)
	}
	if got := f.rowStore(t, "catalog_product_variants", "variant_id", ids["v1"]); got == nil || *got != scopeStoreA {
		t.Fatalf("variant owned by A, got %v", got)
	}

	// Store B can never write Store A's variant (higher revision is no
	// defense against ownership).
	v1b := variantEventID(613)
	f.ingest(t, f.devB, f.credB, v1b, catalog.EventProductVariantSnapshotV1,
		variantPayload(ids["v1"], ids["prodA"], "ML-B-V1", true, false, "color=blue", nil, 2))
	res := projectCatalogTerminal(t, f, v1b, catalog.EventProductVariantSnapshotV1)
	if res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrStoreScopeConflict {
		t.Fatalf("cross-store write: %+v", res)
	}
	if got := f.rowStore(t, "catalog_product_variants", "variant_id", ids["v1"]); got == nil || *got != scopeStoreA {
		t.Fatalf("ownership must stay A, got %v", got)
	}

	// Legacy events apply revision continuity but never claim or wipe
	// Store ownership.
	v1c := variantEventID(614)
	f.ingest(t, f.devC, f.credC, v1c, catalog.EventProductVariantSnapshotV1,
		variantPayload(ids["v1"], ids["prodA"], "ML-L-V1", true, false, "color=blue", nil, 3))
	if res := projectCatalogTerminal(t, f, v1c, catalog.EventProductVariantSnapshotV1); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("legacy write: %+v", res)
	}
	if got := f.rowStore(t, "catalog_product_variants", "variant_id", ids["v1"]); got == nil || *got != scopeStoreA {
		t.Fatalf("legacy must not wipe A, got %v", got)
	}
	var sku string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT sku FROM catalog_product_variants WHERE variant_id=$1`, ids["v1"]).Scan(&sku); err != nil || sku != "ML-L-V1" {
		t.Fatalf("legacy revision continuity: %q (%v)", sku, err)
	}

	// A scoped event adopts a legacy NULL row.
	v2c := variantEventID(615)
	f.ingest(t, f.devC, f.credC, v2c, catalog.EventProductVariantSnapshotV1,
		variantPayload(ids["v2"], ids["prodA"], "ML-L-V2", true, false, "color=red", nil, 1))
	if res := projectCatalogTerminal(t, f, v2c, catalog.EventProductVariantSnapshotV1); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("legacy variant: %+v", res)
	}
	if got := f.rowStore(t, "catalog_product_variants", "variant_id", ids["v2"]); got != nil {
		t.Fatalf("legacy row must start unscoped, got %v", *got)
	}
	v2a := variantEventID(616)
	f.ingest(t, f.devA, f.credA, v2a, catalog.EventProductVariantSnapshotV1,
		variantPayload(ids["v2"], ids["prodA"], "ML-A-V2", true, false, "color=red", nil, 2))
	if res := projectCatalogTerminal(t, f, v2a, catalog.EventProductVariantSnapshotV1); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("adoption: %+v", res)
	}
	if got := f.rowStore(t, "catalog_product_variants", "variant_id", ids["v2"]); got == nil || *got != scopeStoreA {
		t.Fatalf("adoption must claim A, got %v", got)
	}

	// A Store B variant is never adoptable by Store A. Both Stores may
	// hold variant rows of one legacy (unscoped) product, so the fixture
	// product here is projected legacy.
	catL := variantEventID(617)
	f.ingest(t, f.devC, f.credC, catL, catalog.EventCategorySnapshotV1,
		categoryPayload(ids["catL"], "active", map[string]string{"ar": "قسم"}, nil, 1))
	if res := projectCatalogTerminal(t, f, catL, catalog.EventCategorySnapshotV1); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("legacy category: %+v", res)
	}
	prodL := variantEventID(618)
	f.ingest(t, f.devC, f.credC, prodL, catalog.EventProductSnapshotV1,
		productPayload(ids["prodL"], "ML-L-1", "منتج", ids["catL"], nil, nil, 1))
	if res := projectCatalogTerminal(t, f, prodL, catalog.EventProductSnapshotV1); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("legacy product: %+v", res)
	}

	v3b := variantEventID(619)
	f.ingest(t, f.devB, f.credB, v3b, catalog.EventProductVariantSnapshotV1,
		variantPayload(ids["v3"], ids["prodL"], "ML-B-V3", true, false, "color=green", nil, 1))
	if res := projectCatalogTerminal(t, f, v3b, catalog.EventProductVariantSnapshotV1); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("variant B: %+v", res)
	}
	if got := f.rowStore(t, "catalog_product_variants", "variant_id", ids["v3"]); got == nil || *got != scopeStoreB {
		t.Fatalf("variant B ownership, got %v", got)
	}
	v3a := variantEventID(620)
	f.ingest(t, f.devA, f.credA, v3a, catalog.EventProductVariantSnapshotV1,
		variantPayload(ids["v3"], ids["prodL"], "ML-A-V3", true, false, "color=green", nil, 2))
	res = projectCatalogTerminal(t, f, v3a, catalog.EventProductVariantSnapshotV1)
	if res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrStoreScopeConflict {
		t.Fatalf("foreign adoption: %+v", res)
	}
}

// TestCatalogVariantTombstoneKept proves tombstones are stored, never
// deletes: the row survives for historical safety and reads exclude it.
func TestCatalogVariantTombstoneKept(t *testing.T) {
	env := openSaleEnv(t)
	_, prodID := seedCatalogProduct(t, env, 700, "ML-P-7")
	vid := catalogIDs(t, 720, "v")["v"]

	rev1 := variantEventID(730)
	ingestCatalog(t, env, rev1, catalog.EventProductVariantSnapshotV1, "2026-09-20T10:00:00Z",
		variantPayload(vid, prodID, "ML-V-7", true, false, "color=blue", nil, 1))
	if res := projectCatalogOnce(t, env, rev1); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("rev1: %+v", res)
	}
	rev2 := variantEventID(731)
	ingestCatalog(t, env, rev2, catalog.EventProductVariantSnapshotV1, "2026-09-20T10:01:00Z",
		variantPayload(vid, prodID, "ML-V-7", false, true, "color=blue", nil, 2))
	if res := projectCatalogOnce(t, env, rev2); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("tombstone: %+v", res)
	}
	var deleted bool
	if err := env.pool.QueryRow(context.Background(),
		`SELECT deleted FROM catalog_product_variants WHERE variant_id=$1`, vid).Scan(&deleted); err != nil || !deleted {
		t.Fatalf("tombstone row must survive flagged: %v %v", deleted, err)
	}
	variants, err := catalogStore(env).CatalogProductVariants(context.Background(), prodID)
	if err != nil {
		t.Fatal(err)
	}
	if len(variants) != 0 {
		t.Fatalf("reads must exclude tombstones, got %d", len(variants))
	}
}

// TestCatalogVariantDuplicateCombinationTolerated proves duplicate
// combination data is tolerated at the projection layer for unscoped
// legacy rows (NULL ownership never collides), while proven Store rows
// reject identity collisions deterministically.
func TestCatalogVariantDuplicateCombinationTolerated(t *testing.T) {
	env := openSaleEnv(t)
	_, prodID := seedCatalogProduct(t, env, 800, "ML-P-8")
	ids := catalogIDs(t, 820, "v1", "v2")

	for i, vid := range []string{ids["v1"], ids["v2"]} {
		event := variantEventID(830 + i)
		ingestCatalog(t, env, event, catalog.EventProductVariantSnapshotV1, "2026-09-20T10:00:00Z",
			variantPayload(vid, prodID, fmt.Sprintf("ML-V-8%d", i), true, false, "color=blue",
				[]any{variantAttr("color", "blue", 0)}, 1))
		if res := projectCatalogOnce(t, env, event); res.Outcome != catalog.OutcomeProcessed {
			t.Fatalf("legacy duplicate %d: %+v", i, res)
		}
	}
	var rows int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM catalog_product_variants WHERE product_id=$1 AND combination_key='color=blue'`,
		prodID).Scan(&rows); err != nil || rows != 2 {
		t.Fatalf("legacy duplicates must coexist: %d (%v)", rows, err)
	}

	// Proven Store rows still enforce their Store-local identity.
	f := openScopeFixture(t)
	ids2 := catalogIDs(t, 860, "catA", "prodA", "w1", "w2")
	catEvent := variantEventID(870)
	f.ingest(t, f.devA, f.credA, catEvent, catalog.EventCategorySnapshotV1,
		categoryPayload(ids2["catA"], "active", map[string]string{"ar": "قسم"}, nil, 1))
	projectCatalogTerminal(t, f, catEvent, catalog.EventCategorySnapshotV1)
	prodEvent := variantEventID(871)
	f.ingest(t, f.devA, f.credA, prodEvent, catalog.EventProductSnapshotV1,
		productPayload(ids2["prodA"], "ML-A-8", "منتج", ids2["catA"], nil, nil, 1))
	projectCatalogTerminal(t, f, prodEvent, catalog.EventProductSnapshotV1)

	w1 := variantEventID(872)
	f.ingest(t, f.devA, f.credA, w1, catalog.EventProductVariantSnapshotV1,
		variantPayload(ids2["w1"], ids2["prodA"], "ML-A-W1", true, false, "color=blue", nil, 1))
	if res := projectCatalogTerminal(t, f, w1, catalog.EventProductVariantSnapshotV1); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("w1: %+v", res)
	}
	w2 := variantEventID(873)
	f.ingest(t, f.devA, f.credA, w2, catalog.EventProductVariantSnapshotV1,
		variantPayload(ids2["w2"], ids2["prodA"], "ML-A-W2", true, false, "color=blue", nil, 1))
	res := projectCatalogTerminal(t, f, w2, catalog.EventProductVariantSnapshotV1)
	if res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrCatalogRevisionConflict {
		t.Fatalf("scoped duplicate combination: %+v", res)
	}
}

// TestCatalogVariantInventoryStream proves the variant inventory stream:
// dependency wait before the variant projects, revision gates, and the
// policy/catalog mirrors stored as non-semantic context.
func TestCatalogVariantInventoryStream(t *testing.T) {
	env := openSaleEnv(t)
	_, prodID := seedCatalogProduct(t, env, 900, "ML-P-9")
	vid := catalogIDs(t, 920, "v")["v"]

	invEvent := variantEventID(930)
	ingestCatalog(t, env, invEvent, catalog.EventInventoryProductVariantSnapshotV1, "2026-09-20T10:00:00Z",
		variantInventoryPayload(vid, prodID, "ML-V-9", 7, 1, true, nil))
	res := projectCatalogOnce(t, env, invEvent)
	if res.Outcome != catalog.OutcomeRetryable || res.ErrorCode != ErrCatalogDependencyWait {
		t.Fatalf("dependency wait: %+v", res)
	}
	if n := saleCount(t, env.pool, "catalog_product_variant_inventory"); n != 0 {
		t.Fatalf("zero rows while waiting, got %d", n)
	}

	varEvent := variantEventID(931)
	ingestCatalog(t, env, varEvent, catalog.EventProductVariantSnapshotV1, "2026-09-20T10:01:00Z",
		variantPayload(vid, prodID, "ML-V-9", true, false, "color=blue", nil, 1))
	if res := projectCatalogOnce(t, env, varEvent); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("variant: %+v", res)
	}
	forceCatalogDue(t, env, catalog.ProcessorProductVariantInventoryProjectionV1, invEvent)
	if res := projectCatalogOnce(t, env, invEvent); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("inventory converges: %+v", res)
	}
	var stock, policyRev, catalogRev int64
	var sellOnline bool
	if err := env.pool.QueryRow(context.Background(),
		`SELECT stock_quantity, policy_revision, catalog_revision, sell_online
		 FROM catalog_product_variant_inventory WHERE variant_id=$1`,
		vid).Scan(&stock, &policyRev, &catalogRev, &sellOnline); err != nil {
		t.Fatal(err)
	}
	if stock != 7 || policyRev != 2 || catalogRev != 9 || !sellOnline {
		t.Fatalf("inventory row: stock=%d policy=%d catalog=%d sell=%v", stock, policyRev, catalogRev, sellOnline)
	}

	// Stale inventory revision: terminal no-op. Equal revision with
	// different stock: conflicting block. Higher revision replaces.
	invStale := variantEventID(932)
	ingestCatalog(t, env, invStale, catalog.EventInventoryProductVariantSnapshotV1, "2026-09-20T10:02:00Z",
		variantInventoryPayload(vid, prodID, "ML-V-9", 1, 1, true, nil))
	if res := projectCatalogOnce(t, env, invStale); res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrCatalogRevisionConflict {
		t.Fatalf("equal conflicting inventory: %+v", res)
	}
	invNew := variantEventID(933)
	ingestCatalog(t, env, invNew, catalog.EventInventoryProductVariantSnapshotV1, "2026-09-20T10:03:00Z",
		variantInventoryPayload(vid, prodID, "ML-V-9", 3, 2, true, nil))
	if res := projectCatalogOnce(t, env, invNew); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("inventory rev2: %+v", res)
	}
	if err := env.pool.QueryRow(context.Background(),
		`SELECT stock_quantity FROM catalog_product_variant_inventory WHERE variant_id=$1`,
		vid).Scan(&stock); err != nil || stock != 3 {
		t.Fatalf("stock replaced: %d (%v)", stock, err)
	}
	invOld := variantEventID(934)
	ingestCatalog(t, env, invOld, catalog.EventInventoryProductVariantSnapshotV1, "2026-09-20T10:04:00Z",
		variantInventoryPayload(vid, prodID, "ML-V-9", 99, 1, true, nil))
	if res := projectCatalogOnce(t, env, invOld); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("stale inventory: %+v", res)
	}
	if err := env.pool.QueryRow(context.Background(),
		`SELECT stock_quantity FROM catalog_product_variant_inventory WHERE variant_id=$1`,
		vid).Scan(&stock); err != nil || stock != 3 {
		t.Fatalf("stale must not rewind: %d (%v)", stock, err)
	}
}

// TestCatalogVariantAvailabilityRead proves the per-variant ONLINE
// availability derivation: product eligibility, variant activity, and
// the allocation cap (mirroring the product formula).
func TestCatalogVariantAvailabilityRead(t *testing.T) {
	env := openSaleEnv(t)
	_, prodID := seedCatalogProduct(t, env, 1000, "ML-P-10")
	vid := catalogIDs(t, 1020, "v")["v"]

	policyEvent := variantEventID(1030)
	ingestCatalog(t, env, policyEvent, catalog.EventProductSalesPolicySnapshotV1, "2026-09-20T10:00:00Z",
		`{"product_id":"`+prodID+`","sales_policy_revision":1,"sell_offline":true,"sell_online":true,"online_allocation_limit":3}`)
	if res := projectCatalogOnce(t, env, policyEvent); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("policy: %+v", res)
	}
	varEvent := variantEventID(1031)
	ingestCatalog(t, env, varEvent, catalog.EventProductVariantSnapshotV1, "2026-09-20T10:01:00Z",
		variantPayload(vid, prodID, "ML-V-10", true, false, "color=blue", nil, 1))
	if res := projectCatalogOnce(t, env, varEvent); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("variant: %+v", res)
	}
	invEvent := variantEventID(1032)
	ingestCatalog(t, env, invEvent, catalog.EventInventoryProductVariantSnapshotV1, "2026-09-20T10:02:00Z",
		variantInventoryPayload(vid, prodID, "ML-V-10", 7, 1, true, nil))
	if res := projectCatalogOnce(t, env, invEvent); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("inventory: %+v", res)
	}

	store := catalogStore(env)
	avail, err := store.CatalogProductVariantAvailability(context.Background(), vid)
	if err != nil {
		t.Fatal(err)
	}
	if !avail.Ready || avail.OnlineAvailable != 3 {
		t.Fatalf("capped availability: %+v", avail)
	}

	// Inactive variant sells nothing.
	rev2 := variantEventID(1033)
	ingestCatalog(t, env, rev2, catalog.EventProductVariantSnapshotV1, "2026-09-20T10:03:00Z",
		variantPayload(vid, prodID, "ML-V-10", false, false, "color=blue", nil, 2))
	if res := projectCatalogOnce(t, env, rev2); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("rev2: %+v", res)
	}
	avail, err = store.CatalogProductVariantAvailability(context.Background(), vid)
	if err != nil {
		t.Fatal(err)
	}
	if avail.OnlineAvailable != 0 || !avail.Ready {
		t.Fatalf("inactive variant: %+v", avail)
	}
}

// TestCatalogHealthVariantCodes proves the Phase 17 health vocabulary
// over durable state: product-level variant defects, variant-level
// defects, and the informational intentionally-offline code.
func TestCatalogHealthVariantCodes(t *testing.T) {
	env := openSaleEnv(t)
	devID := env.devID
	exec := func(query string) {
		t.Helper()
		if _, err := env.pool.Exec(context.Background(), query); err != nil {
			t.Fatalf("seed: %v\n%s", err, query)
		}
	}
	exec(fmt.Sprintf(`INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, payload, payload_hash)
		VALUES ('f0000000-0000-4000-8000-000000000001', '%s', 'catalog.product.snapshot.v1', now(), '{}', '\x00')`, devID))
	exec(`INSERT INTO catalog_categories (category_id, status, name_ar, online_enabled, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
		VALUES ('f0000000-0000-4000-8000-000000000002', 'active', 'cat', true, 1, 'f0000000-0000-4000-8000-000000000001', '` + devID + `', '\x00', now())`)
	// p1: active product, no variants at all.
	// p2: online-eligible, one live variant with zero stock.
	// p3: online-eligible, one inactive variant (informational).
	// p4: online-eligible, one live variant with stock but no mapping.
	for i := 1; i <= 4; i++ {
		p := fmt.Sprintf("f0000000-0000-4000-8000-00000000000%d", 2+i)
		exec(fmt.Sprintf(`INSERT INTO catalog_products (product_id, sku, name, top_category_id, is_active, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
			VALUES ('%s', 'SKU-%d', 'p%d', 'f0000000-0000-4000-8000-000000000002', true, 1, 'f0000000-0000-4000-8000-000000000001', '%s', '\x00', now())`, p, i, i, devID))
		exec(fmt.Sprintf(`INSERT INTO catalog_product_sales_policies (product_id, sell_offline, sell_online, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
			VALUES ('%s', true, true, 1, 'f0000000-0000-4000-8000-000000000001', '%s', '\x00', now())`, p, devID))
	}
	mkVariant := func(id, product, sku, combination string, active bool) {
		exec(fmt.Sprintf(`INSERT INTO catalog_product_variants (variant_id, product_id, sku, is_active, deleted, price_egp_cents, position, combination_key, variant_revision, catalog_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
			VALUES ('%s', '%s', '%s', %v, false, 100, 0, '%s', 1, 1, 'f0000000-0000-4000-8000-000000000001', '%s', '\x00', now())`, id, product, sku, active, combination, devID))
	}
	mkStock := func(variant, product, sku string, stock int64) {
		exec(fmt.Sprintf(`INSERT INTO catalog_product_variant_inventory (variant_id, product_id, sku, stock_quantity, ready, sell_online, policy_revision, catalog_revision, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
			VALUES ('%s', '%s', '%s', %d, true, true, 1, 1, 1, 'f0000000-0000-4000-8000-000000000001', '%s', '\x00', now())`, variant, product, sku, stock, devID))
	}
	mkVariant("f0000000-0000-4000-8000-000000000011", "f0000000-0000-4000-8000-000000000004", "V-2", "color=blue", true)
	mkStock("f0000000-0000-4000-8000-000000000011", "f0000000-0000-4000-8000-000000000004", "V-2", 0)
	mkVariant("f0000000-0000-4000-8000-000000000012", "f0000000-0000-4000-8000-000000000005", "V-3", "color=blue", false)
	mkVariant("f0000000-0000-4000-8000-000000000013", "f0000000-0000-4000-8000-000000000006", "V-4", "color=blue", true)
	mkStock("f0000000-0000-4000-8000-000000000013", "f0000000-0000-4000-8000-000000000006", "V-4", 5)

	devices := catalogStore(env)
	counts, err := devices.CatalogHealthSummaryRows(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	byCode := map[string]int64{}
	for _, c := range counts {
		byCode[c.ReasonCode] = c.Products
	}
	want := map[string]int64{
		// p1 (no variants) + p3 (only an inactive variant).
		"PRODUCT_NO_ACTIVE_VARIANTS": 2,
		// p1 (none), p2 (zero stock), p3 (inactive).
		"PRODUCT_NO_SELLABLE_VARIANT": 3,
		// p3's deliberately inactive variant on an online-eligible product.
		"VARIANT_INTENTIONALLY_OFFLINE": 1,
		// Provider universe is empty (no mappings/barriers): the
		// provider-scoped variant mapping reason reports nothing.
		"VARIANT_MAPPING_MISSING": 0,
	}
	for code, wantCount := range want {
		if got := byCode[code]; got != wantCount {
			t.Fatalf("%s: got %d want %d (all: %v)", code, got, wantCount, byCode)
		}
	}

	// Detail rows carry variant/product identity only.
	detail, err := devices.CatalogHealthDetailRows(context.Background(), "", "", "PRODUCT_NO_ACTIVE_VARIANTS", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail) != 2 || detail[0].SKU != "SKU-1" {
		t.Fatalf("detail: %+v", detail)
	}
}
