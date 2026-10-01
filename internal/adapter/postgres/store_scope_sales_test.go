package postgres

// Phase 9B store scoping, part 2: inventory/policy isolation, historical
// sale/return ownership, and cross-Store attack rejection on real
// PostgreSQL. Same fixture and ingress-trust rules as store_scope_test.go.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/returnrefund"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
)

// scopedSalePayload returns the EGP sale fixture with a chosen sale ID.
func scopedSalePayload(t *testing.T, saleID string) string {
	t.Helper()
	raw := fixture(t, "sale_egp.json")
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	m["sale_id"] = saleID
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// scaledSalePayload multiplies every amount_minor by factor (uniform
// scaling preserves sale/payment/total agreement exactly).
func scaledSalePayload(t *testing.T, saleID string, factor int64) string {
	t.Helper()
	raw := fixture(t, "sale_egp.json")
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	m["sale_id"] = saleID
	var walk func(v any)
	walk = func(v any) {
		switch node := v.(type) {
		case map[string]any:
			for key, val := range node {
				if key == "amount_minor" {
					if f, ok := val.(float64); ok {
						node[key] = int64(f) * factor
					}
					continue
				}
				walk(val)
			}
		case []any:
			for _, item := range node {
				walk(item)
			}
		}
	}
	walk(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// bigSalePayload sets every amount_minor beyond 2^53 (int64-exact, never
// float) while preserving agreement: single-unit quantities keep
// line_total == quantity × unit_price, and totals/payments stay mutually
// consistent by construction.
func bigSalePayload(t *testing.T, saleID string) string {
	t.Helper()
	const big = int64(9007199254740993)
	raw := fixture(t, "sale_egp.json")
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	m["sale_id"] = saleID
	var walk func(v any)
	walk = func(v any) {
		switch node := v.(type) {
		case map[string]any:
			for key, val := range node {
				switch key {
				case "amount_minor":
					node[key] = big
				case "quantity":
					node[key] = 1
				default:
					walk(val)
				}
			}
		case []any:
			for _, item := range node {
				walk(item)
			}
		}
	}
	walk(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func saleStore(t *testing.T, f *scopeFixture, saleID string) *string {
	t.Helper()
	return f.rowStore(t, "sales_projection", "sale_id", saleID)
}

// TestScope_InventoryIsolation proves stock belongs to Store + Product:
// same SKU at 5 (A) and 20 (B), plus rejection of cross-Store inventory
// for an owned product.
func TestScope_InventoryIsolation(t *testing.T) {
	f := openScopeFixture(t)
	rootA, tagA, rootB, tagB := f.scopeGraph(t, 200, "inv")
	ids := catalogIDs(t, 210, "prod-a", "prod-b")
	prodA, prodB := ids["prod-a"], ids["prod-b"]
	f.ingest(t, f.devA, f.credA, "e0000210-0000-4000-8000-000000000001",
		catalog.EventProductSnapshotV1, productPayload(prodA, "STOCK-X", "A", rootA, nil, []string{tagA}, 1))
	f.ingest(t, f.devB, f.credB, "e0000210-0000-4000-8000-000000000002",
		catalog.EventProductSnapshotV1, productPayload(prodB, "STOCK-X", "B", rootB, nil, []string{tagB}, 1))
	for _, event := range []string{
		"e0000210-0000-4000-8000-000000000001", "e0000210-0000-4000-8000-000000000002",
	} {
		requireOutcome(t, f.projectCatalog(t, event, catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")
	}
	f.ingest(t, f.devA, f.credA, "e0000210-0000-4000-8000-000000000003",
		catalog.EventInventoryProductSnapshotV1, inventoryPayload(prodA, 1, 5))
	f.ingest(t, f.devB, f.credB, "e0000210-0000-4000-8000-000000000004",
		catalog.EventInventoryProductSnapshotV1, inventoryPayload(prodB, 1, 20))
	requireOutcome(t, f.projectCatalog(t, "e0000210-0000-4000-8000-000000000003", catalog.EventInventoryProductSnapshotV1), catalog.OutcomeProcessed, "")
	requireOutcome(t, f.projectCatalog(t, "e0000210-0000-4000-8000-000000000004", catalog.EventInventoryProductSnapshotV1), catalog.OutcomeProcessed, "")
	var stockA, stockB int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT stock_quantity FROM catalog_product_inventory WHERE product_id=$1`, prodA).Scan(&stockA); err != nil || stockA != 5 {
		t.Fatalf("A stock 5: %d (%v)", stockA, err)
	}
	if err := f.pool.QueryRow(context.Background(),
		`SELECT stock_quantity FROM catalog_product_inventory WHERE product_id=$1`, prodB).Scan(&stockB); err != nil || stockB != 20 {
		t.Fatalf("B stock 20: %d (%v)", stockB, err)
	}
	if got := f.rowStore(t, "catalog_product_inventory", "product_id", prodA); got == nil || *got != scopeStoreA {
		t.Fatalf("inventory A scoped: %v", got)
	}
	if got := f.rowStore(t, "catalog_product_inventory", "product_id", prodB); got == nil || *got != scopeStoreB {
		t.Fatalf("inventory B scoped: %v", got)
	}
	// Cross-Store inventory for A's product: conflict, A untouched.
	f.ingest(t, f.devB, f.credB, "e0000210-0000-4000-8000-000000000005",
		catalog.EventInventoryProductSnapshotV1, inventoryPayload(prodA, 2, 99))
	res := f.projectCatalog(t, "e0000210-0000-4000-8000-000000000005", catalog.EventInventoryProductSnapshotV1)
	requireOutcome(t, res, catalog.OutcomeBlocked, ErrStoreScopeConflict)
	if err := f.pool.QueryRow(context.Background(),
		`SELECT stock_quantity FROM catalog_product_inventory WHERE product_id=$1`, prodA).Scan(&stockA); err != nil || stockA != 5 {
		t.Fatalf("A stock intact: %d (%v)", stockA, err)
	}
}

// TestScope_PolicyIsolation proves Store A disabling a product never
// alters Store B policy, with cross-Store policy rejected.
func TestScope_PolicyIsolation(t *testing.T) {
	f := openScopeFixture(t)
	rootA, tagA, rootB, tagB := f.scopeGraph(t, 220, "pol")
	ids := catalogIDs(t, 230, "prod-a", "prod-b")
	prodA, prodB := ids["prod-a"], ids["prod-b"]
	f.ingest(t, f.devA, f.credA, "e0000230-0000-4000-8000-000000000001",
		catalog.EventProductSnapshotV1, productPayload(prodA, "POL-X", "A", rootA, nil, []string{tagA}, 1))
	f.ingest(t, f.devB, f.credB, "e0000230-0000-4000-8000-000000000002",
		catalog.EventProductSnapshotV1, productPayload(prodB, "POL-X", "B", rootB, nil, []string{tagB}, 1))
	for _, event := range []string{
		"e0000230-0000-4000-8000-000000000001", "e0000230-0000-4000-8000-000000000002",
	} {
		requireOutcome(t, f.projectCatalog(t, event, catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")
	}
	f.ingest(t, f.devA, f.credA, "e0000230-0000-4000-8000-000000000003",
		catalog.EventProductSalesPolicySnapshotV1, policyPayload(prodA, 1, true, false, nil))
	f.ingest(t, f.devB, f.credB, "e0000230-0000-4000-8000-000000000004",
		catalog.EventProductSalesPolicySnapshotV1, policyPayload(prodB, 1, true, true, policyInt(10)))
	requireOutcome(t, f.projectCatalog(t, "e0000230-0000-4000-8000-000000000003", catalog.EventProductSalesPolicySnapshotV1), catalog.OutcomeProcessed, "")
	requireOutcome(t, f.projectCatalog(t, "e0000230-0000-4000-8000-000000000004", catalog.EventProductSalesPolicySnapshotV1), catalog.OutcomeProcessed, "")
	var onlineA, onlineB bool
	if err := f.pool.QueryRow(context.Background(),
		`SELECT sell_online FROM catalog_product_sales_policies WHERE product_id=$1`, prodA).Scan(&onlineA); err != nil || onlineA {
		t.Fatalf("A stays disabled: %v (%v)", onlineA, err)
	}
	if err := f.pool.QueryRow(context.Background(),
		`SELECT sell_online FROM catalog_product_sales_policies WHERE product_id=$1`, prodB).Scan(&onlineB); err != nil || !onlineB {
		t.Fatalf("B stays enabled: %v (%v)", onlineB, err)
	}
	f.ingest(t, f.devB, f.credB, "e0000230-0000-4000-8000-000000000005",
		catalog.EventProductSalesPolicySnapshotV1, policyPayload(prodA, 2, true, true, nil))
	res := f.projectCatalog(t, "e0000230-0000-4000-8000-000000000005", catalog.EventProductSalesPolicySnapshotV1)
	requireOutcome(t, res, catalog.OutcomeBlocked, ErrStoreScopeConflict)
}

// TestScope_SalesOwnership proves new scoped Sales project under their
// ingress Store, legacy Sales stay NULL (never adopted), and a second
// event for an owned sale_id blocks without overwrite.
func TestScope_SalesOwnership(t *testing.T) {
	f := openScopeFixture(t)
	saleA, saleB, saleLegacy := "aaaaaaaa-0000-4000-8000-0000000000a1", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb1", "cccccccc-cccc-4ccc-8ccc-ccccccccccc1"
	f.ingest(t, f.devA, f.credA, "e0000240-0000-4000-8000-000000000001", "sale.finalized.v1", scopedSalePayload(t, saleA))
	f.ingest(t, f.devB, f.credB, "e0000240-0000-4000-8000-000000000002", "sale.finalized.v1", scopedSalePayload(t, saleB))
	f.ingest(t, f.devC, f.credC, "e0000240-0000-4000-8000-000000000003", "sale.finalized.v1", scopedSalePayload(t, saleLegacy))
	res := f.projectSale(t, "e0000240-0000-4000-8000-000000000001")
	if res.Outcome != sale.OutcomeProcessed {
		t.Fatalf("sale A: %+v", res)
	}
	res = f.projectSale(t, "e0000240-0000-4000-8000-000000000002")
	if res.Outcome != sale.OutcomeProcessed {
		t.Fatalf("sale B: %+v", res)
	}
	res = f.projectSale(t, "e0000240-0000-4000-8000-000000000003")
	if res.Outcome != sale.OutcomeProcessed {
		t.Fatalf("legacy sale: %+v", res)
	}
	if got := saleStore(t, f, saleA); got == nil || *got != scopeStoreA {
		t.Fatalf("sale A scoped: %v", got)
	}
	if got := saleStore(t, f, saleB); got == nil || *got != scopeStoreB {
		t.Fatalf("sale B scoped: %v", got)
	}
	if got := saleStore(t, f, saleLegacy); got != nil {
		t.Fatalf("legacy sale stays NULL: %v", got)
	}
	var lines int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM sale_lines_projection WHERE sale_id=$1`, saleA).Scan(&lines); err != nil || lines == 0 {
		t.Fatalf("sale A children: %d (%v)", lines, err)
	}
	// A different event claiming A's sale_id: blocked, A intact.
	f.ingest(t, f.devB, f.credB, "e0000240-0000-4000-8000-000000000004", "sale.finalized.v1", scopedSalePayload(t, saleA))
	res = f.projectSale(t, "e0000240-0000-4000-8000-000000000004")
	if res.Outcome != sale.OutcomeBlocked {
		t.Fatalf("cross-store sale_id reuse must block: %+v", res)
	}
	if res.ErrorCode != ErrSaleIDConflict && res.ErrorCode != ErrStoreScopeConflict {
		t.Fatalf("bounded conflict code: %q", res.ErrorCode)
	}
	if got := saleStore(t, f, saleA); got == nil || *got != scopeStoreA {
		t.Fatalf("sale A intact: %v", got)
	}
}

// TestScope_SaleV2StorePreserved proves scoped v2 Sales keep Store
// context, historical categories/tags, and exact money.
func TestScope_SaleV2StorePreserved(t *testing.T) {
	f := openScopeFixture(t)
	raw := v2Fixture(t, nil)
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	saleID := "aaaaaaaa-0000-4000-8000-0000000000a2"
	m["sale_id"] = saleID
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	f.ingest(t, f.devA, f.credA, "e0000241-0000-4000-8000-000000000001", "sale.finalized.v2", string(out))
	res := f.projectSaleV2(t, "e0000241-0000-4000-8000-000000000001")
	if res.Outcome != sale.OutcomeProcessed {
		t.Fatalf("sale v2: %+v", res)
	}
	if got := saleStore(t, f, saleID); got == nil || *got != scopeStoreA {
		t.Fatalf("sale v2 scoped: %v", got)
	}
	var capture *bool
	if err := f.pool.QueryRow(context.Background(),
		`SELECT tag_capture FROM sales_projection WHERE sale_id=$1`, saleID).Scan(&capture); err != nil || capture == nil || !*capture {
		t.Fatalf("v2 tag capture: %v (%v)", capture, err)
	}
	var tags int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM sale_item_tag_snapshots WHERE sale_id=$1`, saleID).Scan(&tags); err != nil || tags == 0 {
		t.Fatalf("v2 tag snapshots: %d (%v)", tags, err)
	}
	var total int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT total_minor FROM sales_projection WHERE sale_id=$1`, saleID).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total <= 0 {
		t.Fatalf("v2 money preserved: %d", total)
	}
}

// TestScope_CrossStoreReturnRejected is the mandatory attack test: a
// Store B return referencing Store A's sale blocks with zero mutation.
func TestScope_CrossStoreReturnRejected(t *testing.T) {
	f := openScopeFixture(t)
	salePayload := scopedSalePayload(t, "aaaaaaaa-0000-4000-8000-0000000000a3")
	f.ingest(t, f.devA, f.credA, "e0000242-0000-4000-8000-000000000001", "sale.finalized.v1", salePayload)
	if res := f.projectSale(t, "e0000242-0000-4000-8000-000000000001"); res.Outcome != sale.OutcomeProcessed {
		t.Fatalf("sale A: %+v", res)
	}
	before := saleMoney(t, f, "aaaaaaaa-0000-4000-8000-0000000000a3")
	retPayload := alignedReturnPayload(t, salePayload, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb2", "RB-1", 1)
	f.ingest(t, f.devB, f.credB, "e0000242-0000-4000-8000-000000000002", "sale.return_refund.finalized.v1", retPayload)
	res := f.projectReturn(t, "e0000242-0000-4000-8000-000000000002")
	if res.Outcome != returnrefund.OutcomeBlocked || res.ErrorCode != ErrStoreScopeConflict {
		t.Fatalf("cross-store return must scope-block: %+v", res)
	}
	after := saleMoney(t, f, "aaaaaaaa-0000-4000-8000-0000000000a3")
	if before != after {
		t.Fatalf("sale A mutated: %v -> %v", before, after)
	}
	var returns int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM return_refund_projection WHERE return_refund_id='bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb2'`).Scan(&returns); err != nil || returns != 0 {
		t.Fatalf("no return row: %d (%v)", returns, err)
	}
	var status, code string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT status, COALESCE(last_error_code,'') FROM sync_event_processing WHERE event_id='e0000242-0000-4000-8000-000000000002' AND processor='return_refund_projection.v1'`).Scan(&status, &code); err != nil || status != "blocked" {
		t.Fatalf("terminal block without hot-loop: %q %q (%v)", status, code, err)
	}
}

type saleAmounts struct {
	total, subtotal, discount, tax int64
	lines                          int
}

func saleMoney(t *testing.T, f *scopeFixture, saleID string) saleAmounts {
	t.Helper()
	var out saleAmounts
	if err := f.pool.QueryRow(context.Background(),
		`SELECT total_minor, subtotal_minor, discount_minor, tax_minor FROM sales_projection WHERE sale_id=$1`,
		saleID).Scan(&out.total, &out.subtotal, &out.discount, &out.tax); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM sale_lines_projection WHERE sale_id=$1`, saleID).Scan(&out.lines); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestScope_ReturnSameStoreProjects proves the legitimate same-Store
// return path is unaffected: scoped sale + scoped return from one Store.
func TestScope_ReturnSameStoreProjects(t *testing.T) {
	f := openScopeFixture(t)
	salePayload := scopedSalePayload(t, "aaaaaaaa-0000-4000-8000-0000000000a4")
	f.ingest(t, f.devA, f.credA, "e0000243-0000-4000-8000-000000000001", "sale.finalized.v1", salePayload)
	if res := f.projectSale(t, "e0000243-0000-4000-8000-000000000001"); res.Outcome != sale.OutcomeProcessed {
		t.Fatalf("sale A: %+v", res)
	}
	retPayload := alignedReturnPayload(t, salePayload, "aaaaaaaa-0000-4000-8000-0000000000a5", "RA-1", 1)
	f.ingest(t, f.devA, f.credA, "e0000243-0000-4000-8000-000000000002", "sale.return_refund.finalized.v1", retPayload)
	res := f.projectReturn(t, "e0000243-0000-4000-8000-000000000002")
	if res.Outcome != returnrefund.OutcomeProcessed {
		t.Fatalf("return A: %+v", res)
	}
	var store *string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT store_id::text FROM return_refund_projection WHERE return_refund_id='aaaaaaaa-0000-4000-8000-0000000000a5'`).Scan(&store); err != nil || store == nil || *store != scopeStoreA {
		t.Fatalf("return scoped A: %v (%v)", store, err)
	}
	var refund int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT refund_total_minor FROM return_refund_projection WHERE return_refund_id='aaaaaaaa-0000-4000-8000-0000000000a5'`).Scan(&refund); err != nil || refund <= 0 {
		t.Fatalf("refund money: %d (%v)", refund, err)
	}
}

// TestScope_ReturnLegacyContinuity proves device-provenance continuity:
// the same device that created a legacy NULL sale may return it once
// bound, without rewriting the sale; a different device's scoped return
// stays ambiguous.
func TestScope_ReturnLegacyContinuity(t *testing.T) {
	f := openScopeFixture(t)
	salePayload := scopedSalePayload(t, "cccccccc-cccc-4ccc-8ccc-ccccccccccc2")
	f.ingest(t, f.devC, f.credC, "e0000244-0000-4000-8000-000000000001", "sale.finalized.v1", salePayload)
	if res := f.projectSale(t, "e0000244-0000-4000-8000-000000000001"); res.Outcome != sale.OutcomeProcessed {
		t.Fatalf("legacy sale: %+v", res)
	}
	devices := NewDevices(f.pool, 5*time.Second)
	if err := devices.EnrollStore(context.Background(), f.devC, scopeStoreA); err != nil {
		t.Fatalf("enroll C: %v", err)
	}
	samePayload := alignedReturnPayload(t, salePayload, "cccccccc-cccc-4ccc-8ccc-ccccccccccc3", "RC-1", 1)
	f.ingest(t, f.devC, f.credC, "e0000244-0000-4000-8000-000000000002", "sale.return_refund.finalized.v1", samePayload)
	res := f.projectReturn(t, "e0000244-0000-4000-8000-000000000002")
	if res.Outcome != returnrefund.OutcomeProcessed {
		t.Fatalf("same-device legacy return: %+v", res)
	}
	if got := saleStore(t, f, "cccccccc-cccc-4ccc-8ccc-ccccccccccc2"); got != nil {
		t.Fatalf("legacy sale untouched: %v", got)
	}
	var store *string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT store_id::text FROM return_refund_projection WHERE return_refund_id='cccccccc-cccc-4ccc-8ccc-ccccccccccc3'`).Scan(&store); err != nil || store == nil || *store != scopeStoreA {
		t.Fatalf("return scoped A: %v (%v)", store, err)
	}
	otherPayload := alignedReturnPayload(t, salePayload, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb3", "RB-2", 1)
	f.ingest(t, f.devB, f.credB, "e0000244-0000-4000-8000-000000000003", "sale.return_refund.finalized.v1", otherPayload)
	res = f.projectReturn(t, "e0000244-0000-4000-8000-000000000003")
	if res.Outcome != returnrefund.OutcomeBlocked || res.ErrorCode != ErrLegacyScopeAmbiguous {
		t.Fatalf("different-device legacy return must stay ambiguous: %+v", res)
	}
}

// TestScope_LegacySaleCollision proves a scoped event for a legacy NULL
// sale with a different event stays a scope conflict: the historical row
// is never adopted and never overwritten.
func TestScope_LegacySaleCollision(t *testing.T) {
	f := openScopeFixture(t)
	saleID := "cccccccc-cccc-4ccc-8ccc-ccccccccccd2"
	f.ingest(t, f.devC, f.credC, "e0000245-0000-4000-8000-000000000001", "sale.finalized.v1", scopedSalePayload(t, saleID))
	if res := f.projectSale(t, "e0000245-0000-4000-8000-000000000001"); res.Outcome != sale.OutcomeProcessed {
		t.Fatalf("legacy sale: %+v", res)
	}
	f.ingest(t, f.devA, f.credA, "e0000245-0000-4000-8000-000000000002", "sale.finalized.v1", scopedSalePayload(t, saleID))
	res := f.projectSale(t, "e0000245-0000-4000-8000-000000000002")
	if res.Outcome != sale.OutcomeBlocked {
		t.Fatalf("scoped collision with legacy sale: %+v", res)
	}
	if res.ErrorCode != ErrSaleIDConflict && res.ErrorCode != ErrStoreScopeConflict {
		t.Fatalf("bounded conflict code: %q", res.ErrorCode)
	}
	if got := saleStore(t, f, saleID); got != nil {
		t.Fatalf("legacy sale stays NULL: %v", got)
	}
	// Pre-backfill edge: the durable winner is gone (as for rows that
	// predate the ownership table), so arbitration lets the scoped event
	// through to the write boundary, where the scope backstop fires.
	if _, err := f.pool.Exec(context.Background(),
		`DELETE FROM sale_event_ownership WHERE sale_id=$1`, saleID); err != nil {
		t.Fatal(err)
	}
	f.ingest(t, f.devA, f.credA, "e0000245-0000-4000-8000-000000000003", "sale.finalized.v1", scopedSalePayload(t, saleID))
	res = f.projectSale(t, "e0000245-0000-4000-8000-000000000003")
	if res.Outcome != sale.OutcomeBlocked || res.ErrorCode != ErrStoreScopeConflict {
		t.Fatalf("write-boundary scope backstop: %+v", res)
	}
	if got := saleStore(t, f, saleID); got != nil {
		t.Fatalf("legacy sale stays NULL: %v", got)
	}
	var total int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT total_minor FROM sales_projection WHERE sale_id=$1`, saleID).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total <= 0 {
		t.Fatalf("legacy money intact: %d", total)
	}
}

// TestScope_MixedBacklog proves legacy NULL, Store A, and Store B events
// processed concurrently each retain their own ownership semantics.
func TestScope_MixedBacklog(t *testing.T) {
	f := openScopeFixture(t)
	ids := catalogIDs(t, 340, "root-m", "tag-m", "null-p", "a-p", "b-p")
	rootM, tagM := ids["root-m"], ids["tag-m"]
	names := map[string]string{"ar": "m"}
	for i, tc := range []struct{ event, typ, payload string }{
		{"e0000340-0000-4000-8000-000000000001", catalog.EventCategorySnapshotV1, categoryPayload(rootM, "active", names, nil, 1)},
		{"e0000340-0000-4000-8000-000000000002", catalog.EventTagSnapshotV1, tagPayload(tagM, "mixed-tag", true, names, 1)},
	} {
		f.ingest(t, f.devC, f.credC, tc.event, tc.typ, tc.payload)
		requireOutcome(t, f.projectCatalog(t, tc.event, tc.typ), catalog.OutcomeProcessed, "")
		_ = i
	}
	f.ingest(t, f.devC, f.credC, "e0000340-0000-4000-8000-000000000003",
		catalog.EventProductSnapshotV1, productPayload(ids["null-p"], "MIX-1", "Null", rootM, nil, []string{tagM}, 1))
	f.ingest(t, f.devA, f.credA, "e0000340-0000-4000-8000-000000000004",
		catalog.EventProductSnapshotV1, productPayload(ids["a-p"], "MIX-1", "A", rootM, nil, []string{tagM}, 1))
	f.ingest(t, f.devB, f.credB, "e0000340-0000-4000-8000-000000000005",
		catalog.EventProductSnapshotV1, productPayload(ids["b-p"], "MIX-1", "B", rootM, nil, []string{tagM}, 1))
	type rout struct {
		res catalog.ProjectResult
		err error
	}
	outs := make([]rout, 3)
	var wg sync.WaitGroup
	for i, event := range []string{
		"e0000340-0000-4000-8000-000000000003", "e0000340-0000-4000-8000-000000000004", "e0000340-0000-4000-8000-000000000005",
	} {
		wg.Add(1)
		go func(i int, event string) {
			defer wg.Done()
			for attempt := 0; attempt < 25; attempt++ {
				expireBackoff(t, f, event)
				store := NewDevices(f.pool, 5*time.Second)
				rec, _, err := store.LoadCatalogEvent(context.Background(), event)
				if err != nil {
					t.Errorf("load: %v", err)
					return
				}
				res, err := store.ProjectProduct(context.Background(), rec, time.Now())
				if err == nil && res.Outcome != catalog.OutcomeNotDue && res.Outcome != catalog.OutcomeRetryable {
					outs[i].res, outs[i].err = res, nil
					return
				}
				time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
			}
			t.Errorf("project %s never verdict", event)
		}(i, event)
	}
	wg.Wait()
	for i, o := range outs {
		if o.err != nil || (o.res.Outcome != catalog.OutcomeProcessed && o.res.Outcome != catalog.OutcomeAlready) {
			t.Fatalf("mixed backlog %d: %+v %v", i, o.res, o.err)
		}
	}
	if got := f.rowStore(t, "catalog_products", "product_id", ids["null-p"]); got != nil {
		t.Fatalf("legacy product stays NULL: %v", got)
	}
	if got := f.rowStore(t, "catalog_products", "product_id", ids["a-p"]); got == nil || *got != scopeStoreA {
		t.Fatalf("A product: %v", got)
	}
	if got := f.rowStore(t, "catalog_products", "product_id", ids["b-p"]); got == nil || *got != scopeStoreB {
		t.Fatalf("B product: %v", got)
	}
}
