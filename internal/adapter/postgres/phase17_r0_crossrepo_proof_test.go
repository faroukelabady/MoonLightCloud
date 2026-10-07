package postgres

// Phase 17-R0 (R13) cross-repository proof, consumer half: the REAL
// MoonLightRetail-emitted fixture payloads (committed in testdata, pinned
// byte-for-byte by MoonLightRetail's internal/domain/sync
// TestWireParityFixtures) driven through the real Cloud ingest + variant
// projection path against real PostgreSQL. The emitter half is proven in
// MoonLightRetail (variant_sync_test.go + admincommand
// variant_commands_test.go); full two-process E2E is replaced by these
// paired emitter/consumer integration tests (see the Phase 17-R0 report).
//
// Proven here: (a) product + 3 Nefertiti variants converge with exact
// per-variant stock (Blue 4 / Gold 2 preserved — never a product
// aggregate), (c) stale/equal-revision conflicts never overwrite,
// (d) out-of-order arrival (inventory before its variant — the
// offline-queue reconnect shape) waits then converges, (e) duplicate
// delivery is idempotent. Store isolation (f) is proven by
// TestCatalogVariantStoreIsolation in catalog_variant_projection_test.go.

import (
	"context"
	"encoding/json"

	"os"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

const (
	proofProductID     = "77777777-0000-4000-8000-000000000001"
	proofBlueVariantID = "77777777-0000-4000-8000-000000000002"
	proofGoldVariantID = "77777777-0000-4000-8000-000000000003"
	proofModernVarID   = "77777777-0000-4000-8000-000000000004"
	proofRootCatID     = "00000000-0000-4000-8000-000000000101"
)

func proofFixture(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// proofSiblingVariant builds a same-shape sibling variant payload (the
// committed fixture set pins one variant; the siblings are the exact
// Nefertiti shape MoonLightRetail emits for the other two units).
func proofSiblingVariant(t *testing.T, variantID, sku, combinationKey string, priceEGP int64, stockMirror int, revision int64, attrs []any) string {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"variant_id": variantID, "product_id": proofProductID, "sku": sku,
		"is_active": true, "deleted": false,
		"price_egp_cents": priceEGP, "price_usd_cents": nil,
		"stock_quantity": stockMirror, "inventory_revision": 1,
		"variant_revision": revision, "position": 0,
		"combination_key": combinationKey,
		"attributes":      attrs, "catalog_revision": 1,
	})
	return string(raw)
}

func proofSiblingInventory(t *testing.T, variantID, sku string, stock int) string {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{
		"variant_id": variantID, "product_id": proofProductID, "sku": sku,
		"stock_quantity": stock, "inventory_revision": 1,
		"ready": true, "sell_online": false,
		"online_allocation_limit": nil, "policy_revision": 1, "catalog_revision": 1,
	})
	return string(raw)
}

func proofAttr(definitionCode, valueCode, nameAR, nameEN, definitionNameAR, definitionNameEN string, position int) map[string]any {
	return map[string]any{
		"definition_code": definitionCode, "value_code": valueCode,
		"name_ar": nameAR, "name_en": nameEN,
		"definition_name_ar": definitionNameAR, "definition_name_en": definitionNameEN,
		"position": position,
	}
}

func proofProject(t *testing.T, env *saleEnv, eventID string) catalog.ProjectResult {
	t.Helper()
	res := projectCatalogOnce(t, env, eventID)
	return res
}

func proofStock(t *testing.T, env *saleEnv, variantID string) int {
	t.Helper()
	var stock int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT stock_quantity FROM catalog_product_variant_inventory WHERE variant_id=$1`, variantID).Scan(&stock); err != nil {
		t.Fatalf("variant inventory row %s: %v", variantID, err)
	}
	return stock
}

func TestPhase17R0CrossRepoVariantConvergence(t *testing.T) {
	env := openSaleEnv(t)

	// Category root at the fixture's exact identity.
	catEvent := variantEventID(900)
	ingestCatalog(t, env, catEvent, catalog.EventCategorySnapshotV1, "2026-10-06T10:00:00Z",
		categoryPayload(proofRootCatID, "active", map[string]string{"ar": "المتحف المصري", "en": "Egyptian Museum"}, nil, 1))
	if res := proofProject(t, env, catEvent); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("category: %+v", res)
	}

	// (a) Product from the REAL Retail v2 fixture bytes. The fixture now
	// carries product_type_id (Phase 17-R2), so the papyrus type projects
	// first — the same dependency order the live sync leg proves.
	typeEvent := variantEventID(899)
	ingestCatalog(t, env, typeEvent, catalog.EventProductTypeSnapshotV1, "2026-10-06T10:00:00Z",
		typePayload("10000000-0000-4000-8000-000000000001", "papyrus", 1))
	if res := proofProject(t, env, typeEvent); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("papyrus type: %+v", res)
	}
	prodEvent := variantEventID(901)
	ingestCatalog(t, env, prodEvent, catalog.EventProductSnapshotV2, "2026-10-06T10:00:01Z",
		proofFixture(t, "../../catalog/testdata/wire_product_v2.json"))
	if res := proofProject(t, env, prodEvent); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("product v2 fixture: %+v", res)
	}

	// (d) Out-of-order leg FIRST: Gold's inventory arrives before its
	// variant (offline queue drained out of order on reconnect). It must
	// WAIT (retryable), never fabricate a row.
	goldInventoryEvent := variantEventID(902)
	ingestCatalog(t, env, goldInventoryEvent, catalog.EventInventoryProductVariantSnapshotV1, "2026-10-06T10:00:02Z",
		proofSiblingInventory(t, proofGoldVariantID, "NEF-GOL-TRA", 2))
	res := proofProject(t, env, goldInventoryEvent)
	if res.Outcome == catalog.OutcomeProcessed {
		t.Fatalf("gold inventory must wait for its variant, got %+v", res)
	}
	var waiting int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM catalog_product_variant_inventory WHERE variant_id=$1`, proofGoldVariantID).Scan(&waiting); err != nil {
		t.Fatal(err)
	}
	if waiting != 0 {
		t.Fatal("dependency wait must never fabricate an inventory row")
	}

	// (a) Variants: Blue from the REAL Retail fixture bytes; Gold and
	// Blue/Modern as exact same-shape siblings.
	blueEvent := variantEventID(903)
	ingestCatalog(t, env, blueEvent, catalog.EventProductVariantSnapshotV1, "2026-10-06T10:00:03Z",
		proofFixture(t, "../../catalog/testdata/wire_product_variant_v1.json"))
	if res := proofProject(t, env, blueEvent); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("blue variant fixture: %+v", res)
	}
	goldEvent := variantEventID(904)
	ingestCatalog(t, env, goldEvent, catalog.EventProductVariantSnapshotV1, "2026-10-06T10:00:04Z",
		proofSiblingVariant(t, proofGoldVariantID, "NEF-GOL-TRA", "color=gold\u001fpainting_style=traditional", 110000, 2, 1, []any{
			proofAttr("color", "gold", "ذهبي", "Gold", "اللون", "Color", 0),
			proofAttr("painting_style", "traditional", "تقليدي", "Traditional", "أسلوب الرسم", "Painting Style", 1),
		}))
	if res := proofProject(t, env, goldEvent); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("gold variant: %+v", res)
	}
	modernEvent := variantEventID(905)
	ingestCatalog(t, env, modernEvent, catalog.EventProductVariantSnapshotV1, "2026-10-06T10:00:05Z",
		proofSiblingVariant(t, proofModernVarID, "NEF-BLU-MOD", "color=blue\u001fpainting_style=modern", 120000, 1, 1, []any{
			proofAttr("color", "blue", "أزرق", "Blue", "اللون", "Color", 0),
			proofAttr("painting_style", "modern", "عصري", "Modern", "أسلوب الرسم", "Painting Style", 1),
		}))
	if res := proofProject(t, env, modernEvent); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("modern variant: %+v", res)
	}

	// (d) Gold inventory converges now that its variant projected.
	forceCatalogDue(t, env, catalog.ProcessorProductVariantInventoryProjectionV1, goldInventoryEvent)
	if res := proofProject(t, env, goldInventoryEvent); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("gold inventory after dependency: %+v", res)
	}

	// (a) Per-variant inventory from the REAL Retail fixture + siblings.
	blueInventoryEvent := variantEventID(906)
	ingestCatalog(t, env, blueInventoryEvent, catalog.EventInventoryProductVariantSnapshotV1, "2026-10-06T10:00:06Z",
		proofFixture(t, "../../catalog/testdata/wire_product_variant_inventory_v1.json"))
	if res := proofProject(t, env, blueInventoryEvent); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("blue inventory fixture: %+v", res)
	}
	modernInventoryEvent := variantEventID(907)
	ingestCatalog(t, env, modernInventoryEvent, catalog.EventInventoryProductVariantSnapshotV1, "2026-10-06T10:00:07Z",
		proofSiblingInventory(t, proofModernVarID, "NEF-BLU-MOD", 1))
	if res := proofProject(t, env, modernInventoryEvent); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("modern inventory: %+v", res)
	}

	// Blue 4 / Gold 2 preserved EXACTLY — never 6/6, never multiplied.
	if got := proofStock(t, env, proofBlueVariantID); got != 4 {
		t.Fatalf("Blue stock = %d, want exactly 4", got)
	}
	if got := proofStock(t, env, proofGoldVariantID); got != 2 {
		t.Fatalf("Gold stock = %d, want exactly 2", got)
	}
	if got := proofStock(t, env, proofModernVarID); got != 1 {
		t.Fatalf("Blue/Modern stock = %d, want exactly 1", got)
	}
	var variants, attributes int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM catalog_product_variants WHERE product_id=$1`, proofProductID).Scan(&variants); err != nil || variants != 3 {
		t.Fatalf("projected variants = %d (%v), want 3", variants, err)
	}
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM catalog_product_variant_attribute_values WHERE variant_id=$1`, proofBlueVariantID).Scan(&attributes); err != nil || attributes != 2 {
		t.Fatalf("blue attributes = %d (%v), want 2", attributes, err)
	}
	var blueSKU string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT sku FROM catalog_product_variants WHERE variant_id=$1`, proofBlueVariantID).Scan(&blueSKU); err != nil || blueSKU != "NEF-BLU-TRA" {
		t.Fatalf("blue sku = %q (%v)", blueSKU, err)
	}

	// (e) Duplicate delivery: the same event_id is already accepted, and
	// re-projection is a stored no-op with state untouched.
	duplicate := variantEventID(908)
	ingestCatalog(t, env, duplicate, catalog.EventProductVariantSnapshotV1, "2026-10-06T10:00:08Z",
		proofFixture(t, "../../catalog/testdata/wire_product_variant_v1.json"))
	if res := proofProject(t, env, duplicate); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("first delivery of duplicate payload: %+v", res)
	}
	if res := proofProject(t, env, duplicate); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("re-projection of a committed event must replay stored outcome: %+v", res)
	}
	if got := proofStock(t, env, proofBlueVariantID); got != 4 {
		t.Fatalf("duplicate delivery mutated stock: %d", got)
	}

	// (c) Equal-revision conflicting payload blocks and never overwrites.
	conflict := variantEventID(909)
	ingestCatalog(t, env, conflict, catalog.EventProductVariantSnapshotV1, "2026-10-06T10:00:09Z",
		proofSiblingVariant(t, proofBlueVariantID, "NEF-CONFLICT", "color=blue\u001fpainting_style=traditional", 999999, 4, 1, []any{
			proofAttr("color", "blue", "أزرق", "Blue", "اللون", "Color", 0),
			proofAttr("painting_style", "traditional", "تقليدي", "Traditional", "أسلوب الرسم", "Painting Style", 1),
		}))
	res = proofProject(t, env, conflict)
	if res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrCatalogRevisionConflict {
		t.Fatalf("equal-revision conflict: %+v", res)
	}
	if err := env.pool.QueryRow(context.Background(),
		`SELECT sku FROM catalog_product_variants WHERE variant_id=$1`, proofBlueVariantID).Scan(&blueSKU); err != nil || blueSKU != "NEF-BLU-TRA" {
		t.Fatalf("conflict overwrote state: %q (%v)", blueSKU, err)
	}

	// (c) Advance Blue to revision 2, then a stale revision-1 payload is
	// a terminal no-op that never rewinds state.
	advance := variantEventID(910)
	ingestCatalog(t, env, advance, catalog.EventProductVariantSnapshotV1, "2026-10-06T10:00:10Z",
		proofSiblingVariant(t, proofBlueVariantID, "NEF-BLU-TRA", "color=blue\u001fpainting_style=traditional", 100001, 4, 2, []any{
			proofAttr("color", "blue", "أزرق", "Blue", "اللون", "Color", 0),
			proofAttr("painting_style", "traditional", "تقليدي", "Traditional", "أسلوب الرسم", "Painting Style", 1),
		}))
	if res := proofProject(t, env, advance); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("rev2 advance: %+v", res)
	}
	stale := variantEventID(911)
	ingestCatalog(t, env, stale, catalog.EventProductVariantSnapshotV1, "2026-10-06T10:00:11Z",
		proofSiblingVariant(t, proofBlueVariantID, "NEF-STALE", "color=blue\u001fpainting_style=traditional", 1, 4, 1, nil))
	if res := proofProject(t, env, stale); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("stale no-op: %+v", res)
	}
	if err := env.pool.QueryRow(context.Background(),
		`SELECT sku FROM catalog_product_variants WHERE variant_id=$1`, proofBlueVariantID).Scan(&blueSKU); err != nil || blueSKU != "NEF-BLU-TRA" {
		t.Fatalf("stale revision overwrote state: %q (%v)", blueSKU, err)
	}
	var priceEGP int64
	if err := env.pool.QueryRow(context.Background(),
		`SELECT price_egp_cents FROM catalog_product_variants WHERE variant_id=$1`, proofBlueVariantID).Scan(&priceEGP); err != nil || priceEGP != 100001 {
		t.Fatalf("stale revision rewound price: %d (%v)", priceEGP, err)
	}
}

// TestPhase17R0CrossRepoCommandPayloadParity lives in
// internal/catalogadmin/variant_command_parity_test.go (same literals).
