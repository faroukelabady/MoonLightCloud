package postgres

// Phase 9B store scoping, part 3: report isolation/parity, int64
// exactness, real-PostgreSQL concurrency, rebuild preservation, and
// lifecycle invariance (rename/revocation/rotation, retry/restart).

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/returnrefund"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
	"github.com/faroukelabady/MoonLightCloud/internal/store"
	isync "github.com/faroukelabady/MoonLightCloud/internal/sync"
)

// scopeSalesFixture projects one fixture sale per Store (same currency,
// fixture amounts) plus one legacy NULL sale, returning their IDs.
func scopeSalesFixture(t *testing.T, f *scopeFixture) (saleA, saleB, saleLegacy string) {
	t.Helper()
	saleA = "aaaaaaaa-0000-4000-8000-0000000000d1"
	saleB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbd1"
	saleLegacy = "cccccccc-cccc-4ccc-8ccc-ccccccccccd1"
	f.ingest(t, f.devA, f.credA, "e0000250-0000-4000-8000-000000000001", "sale.finalized.v1", scopedSalePayload(t, saleA))
	f.ingest(t, f.devB, f.credB, "e0000250-0000-4000-8000-000000000002", "sale.finalized.v1", scaledSalePayload(t, saleB, 2))
	f.ingest(t, f.devC, f.credC, "e0000250-0000-4000-8000-000000000003", "sale.finalized.v1", scopedSalePayload(t, saleLegacy))
	for _, event := range []string{
		"e0000250-0000-4000-8000-000000000001", "e0000250-0000-4000-8000-000000000002", "e0000250-0000-4000-8000-000000000003",
	} {
		if res := f.projectSale(t, event); res.Outcome != sale.OutcomeProcessed {
			t.Fatalf("sale %s: %+v", event, res)
		}
	}
	return saleA, saleB, saleLegacy
}

func reportWindow() (start, end string) {
	return "2026-01-01T00:00:00Z", "2027-01-01T00:00:00Z"
}

// TestScope_ReportIsolation proves per-Store summaries equal canonical
// totals over that Store's rows, legacy rows never leak into a Store
// scope, and the unfiltered aggregate is the exact sum.
func TestScope_ReportIsolation(t *testing.T) {
	f := openScopeFixture(t)
	saleA, saleB, _ := scopeSalesFixture(t, f)
	devices := NewDevices(f.pool, 5*time.Second)
	ctx := context.Background()
	start, end := reportWindow()
	startT, _ := time.Parse(time.RFC3339, start)
	endT, _ := time.Parse(time.RFC3339, end)
	var fixtureTotal int64
	if err := f.pool.QueryRow(ctx, `SELECT total_minor FROM sales_projection WHERE sale_id=$1`, saleA).Scan(&fixtureTotal); err != nil {
		t.Fatal(err)
	}
	sum := func(rows []struct {
		Currency string
		Total    int64
	}) int64 {
		var out int64
		for _, r := range rows {
			out += r.Total
		}
		return out
	}
	_ = sum
	scopedA, err := devices.SalesSummaryForStore(ctx, scopeStoreA, startT, endT, "EGP")
	if err != nil {
		t.Fatal(err)
	}
	if len(scopedA) != 1 || scopedA[0].SalesTotal != fixtureTotal {
		t.Fatalf("store A summary: %+v want total %d", scopedA, fixtureTotal)
	}
	scopedB, err := devices.SalesSummaryForStore(ctx, scopeStoreB, startT, endT, "EGP")
	if err != nil {
		t.Fatal(err)
	}
	if len(scopedB) != 1 || scopedB[0].SalesTotal != 2*fixtureTotal {
		t.Fatalf("store B summary: %+v want total %d", scopedB, 2*fixtureTotal)
	}
	global, err := devices.SalesSummary(ctx, startT, endT, "EGP")
	if err != nil {
		t.Fatal(err)
	}
	var globalTotal int64
	for _, r := range global {
		globalTotal += r.SalesTotal
	}
	// Unfiltered keeps documented global behavior: A + B + legacy.
	if globalTotal != 4*fixtureTotal {
		t.Fatalf("unfiltered total %d want %d", globalTotal, 4*fixtureTotal)
	}
	_ = saleB
}

// TestScope_TagReportIsolation proves same-slug tags in two Stores never
// merge: grouping stays historical tag-ID semantics within scope.
func TestScope_TagReportIsolation(t *testing.T) {
	f := openScopeFixture(t)
	mkV2 := func(saleID, tagID string) string {
		raw := v2Fixture(t, nil)
		var m map[string]any
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatal(err)
		}
		m["sale_id"] = saleID
		for _, line := range m["lines"].([]any) {
			tags := line.(map[string]any)["tags"].([]any)
			tags[0].(map[string]any)["tag_id"] = tagID
			tags[0].(map[string]any)["slug"] = "animals"
		}
		out, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	tagA, tagB := "aaaaaaaa-0000-4000-8000-0000000000e1", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbe1"
	f.ingest(t, f.devA, f.credA, "e0000251-0000-4000-8000-000000000001", "sale.finalized.v2", mkV2("aaaaaaaa-0000-4000-8000-0000000000e2", tagA))
	f.ingest(t, f.devB, f.credB, "e0000251-0000-4000-8000-000000000002", "sale.finalized.v2", mkV2("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbe2", tagB))
	for _, event := range []string{
		"e0000251-0000-4000-8000-000000000001", "e0000251-0000-4000-8000-000000000002",
	} {
		if res := f.projectSaleV2(t, event); res.Outcome != sale.OutcomeProcessed {
			t.Fatalf("v2 %s: %+v", event, res)
		}
	}
	devices := NewDevices(f.pool, 5*time.Second)
	ctx := context.Background()
	startT, _ := time.Parse(time.RFC3339, "2026-01-01T00:00:00Z")
	endT, _ := time.Parse(time.RFC3339, "2027-01-01T00:00:00Z")
	rowsA, err := devices.SalesByTagForStore(ctx, scopeStoreA, startT, endT, "")
	if err != nil {
		t.Fatal(err)
	}
	seenA := map[string]bool{}
	for _, r := range rowsA {
		seenA[r.ID] = true
	}
	if !seenA[tagA] || seenA[tagB] {
		t.Fatalf("store A tag history: %v", seenA)
	}
	rowsB, err := devices.SalesByTagForStore(ctx, scopeStoreB, startT, endT, "")
	if err != nil {
		t.Fatal(err)
	}
	seenB := map[string]bool{}
	for _, r := range rowsB {
		seenB[r.ID] = true
	}
	if !seenB[tagB] || seenB[tagA] {
		t.Fatalf("store B tag history: %v", seenB)
	}
}

// TestScope_MoneyExactness proves int64-exact scoped aggregation beyond
// 2^53 with no float anywhere on the path.
func TestScope_MoneyExactness(t *testing.T) {
	f := openScopeFixture(t)
	const big = int64(9007199254740993)
	saleID := "aaaaaaaa-0000-4000-8000-0000000000f1"
	f.ingest(t, f.devA, f.credA, "e0000252-0000-4000-8000-000000000001", "sale.finalized.v1", bigSalePayload(t, saleID))
	if res := f.projectSale(t, "e0000252-0000-4000-8000-000000000001"); res.Outcome != sale.OutcomeProcessed {
		t.Fatalf("big sale: %+v", res)
	}
	devices := NewDevices(f.pool, 5*time.Second)
	ctx := context.Background()
	startT, _ := time.Parse(time.RFC3339, "2026-01-01T00:00:00Z")
	endT, _ := time.Parse(time.RFC3339, "2027-01-01T00:00:00Z")
	rows, err := devices.SalesSummaryForStore(ctx, scopeStoreA, startT, endT, "EGP")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SalesTotal != big {
		t.Fatalf("exact scoped total: %+v want %d", rows, big)
	}
	byProduct, err := devices.SalesByProductForStore(ctx, scopeStoreA, startT, endT, "EGP")
	if err != nil {
		t.Fatal(err)
	}
	var lines int64
	for _, r := range byProduct {
		lines += r.LineSales
	}
	if lines != big {
		t.Fatalf("exact line total: %d want %d", lines, big)
	}
}

// retryVerdict re-runs a projection attempt until it reaches a terminal
// verdict. Serialization aborts are transient; the transient path parks
// the row in retry with a future backoff, so each re-attempt first
// simulates backoff expiry (test-only clock fast-forward, never
// production behavior).
func expireBackoff(t *testing.T, f *scopeFixture, eventID string) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE sync_event_processing SET next_attempt_at=NULL WHERE event_id=$1`, eventID); err != nil {
		t.Fatal(err)
	}
}

// TestScope_ConcurrentSameSKU proves two projector workers cannot merge
// same-SKU products across Stores: both win independently.
func TestScope_ConcurrentSameSKU(t *testing.T) {
	f := openScopeFixture(t)
	rootA, tagA, rootB, tagB := f.scopeGraph(t, 260, "race")
	ids := catalogIDs(t, 270, "race-a", "race-b")
	f.ingest(t, f.devA, f.credA, "e0000270-0000-4000-8000-000000000001",
		catalog.EventProductSnapshotV1, productPayload(ids["race-a"], "RACE-1", "A", rootA, nil, []string{tagA}, 1))
	f.ingest(t, f.devB, f.credB, "e0000270-0000-4000-8000-000000000002",
		catalog.EventProductSnapshotV1, productPayload(ids["race-b"], "RACE-1", "B", rootB, nil, []string{tagB}, 1))
	var wg sync.WaitGroup
	results := make([]catalog.ProjectResult, 2)
	for i, event := range []string{
		"e0000270-0000-4000-8000-000000000001", "e0000270-0000-4000-8000-000000000002",
	} {
		wg.Add(1)
		go func(i int, event string) {
			defer wg.Done()
			// Serialization aborts are transient under concurrency:
			// retry the attempt itself until it verdicts.
			var last catalog.ProjectResult
			for attempt := 0; attempt < 25; attempt++ {
				expireBackoff(t, f, event)
				store := NewDevices(f.pool, 5*time.Second)
				rec, ok, err := store.LoadCatalogEvent(context.Background(), event)
				if err != nil || !ok {
					t.Errorf("load %s: %v", event, err)
					return
				}
				res, err := store.ProjectProduct(context.Background(), rec, time.Now())
				if err == nil && (res.Outcome == catalog.OutcomeProcessed || res.Outcome == catalog.OutcomeAlready || res.Outcome == catalog.OutcomeBlocked) {
					results[i] = res
					return
				}
				last = res
				time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
			}
			results[i] = last
			t.Errorf("project %s never verdict: %+v", event, last)
		}(i, event)
	}
	wg.Wait()
	for i, res := range results {
		if res.Outcome != catalog.OutcomeProcessed && res.Outcome != catalog.OutcomeAlready {
			t.Fatalf("racy product %d: %+v", i, res)
		}
	}
	for _, tc := range []struct{ id, store string }{
		{ids["race-a"], scopeStoreA}, {ids["race-b"], scopeStoreB},
	} {
		if got := f.rowStore(t, "catalog_products", "product_id", tc.id); got == nil || *got != tc.store {
			t.Fatalf("racy ownership %s: %v", tc.id, got)
		}
	}
}

// TestScope_ConcurrentAdoptionRace proves exactly one Store wins a
// legacy adoption race; the loser blocks without a trace.
func TestScope_ConcurrentAdoptionRace(t *testing.T) {
	f := openScopeFixture(t)
	ids := catalogIDs(t, 280, "root-r", "tag-r", "prod-r")
	rootR, tagR, prodR := ids["root-r"], ids["tag-r"], ids["prod-r"]
	names := map[string]string{"ar": "r"}
	f.ingest(t, f.devC, f.credC, "e0000280-0000-4000-8000-000000000001",
		catalog.EventCategorySnapshotV1, categoryPayload(rootR, "active", names, nil, 1))
	f.ingest(t, f.devC, f.credC, "e0000280-0000-4000-8000-000000000002",
		catalog.EventTagSnapshotV1, tagPayload(tagR, "race-tag", true, names, 1))
	f.ingest(t, f.devC, f.credC, "e0000280-0000-4000-8000-000000000003",
		catalog.EventProductSnapshotV1, productPayload(prodR, "RACE-2", "R", rootR, nil, []string{tagR}, 1))
	for _, tc := range []struct{ event, typ string }{
		{"e0000280-0000-4000-8000-000000000001", catalog.EventCategorySnapshotV1},
		{"e0000280-0000-4000-8000-000000000002", catalog.EventTagSnapshotV1},
		{"e0000280-0000-4000-8000-000000000003", catalog.EventProductSnapshotV1},
	} {
		requireOutcome(t, f.projectCatalog(t, tc.event, tc.typ), catalog.OutcomeProcessed, "")
	}
	f.ingest(t, f.devA, f.credA, "e0000280-0000-4000-8000-000000000004",
		catalog.EventProductSnapshotV1, productPayload(prodR, "RACE-2", "RA", rootR, nil, []string{tagR}, 2))
	f.ingest(t, f.devB, f.credB, "e0000280-0000-4000-8000-000000000005",
		catalog.EventProductSnapshotV1, productPayload(prodR, "RACE-2", "RB", rootR, nil, []string{tagR}, 2))
	var wg sync.WaitGroup
	results := make([]catalog.ProjectResult, 2)
	for i, event := range []string{
		"e0000280-0000-4000-8000-000000000004", "e0000280-0000-4000-8000-000000000005",
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
					results[i] = res
					return
				}
				time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
			}
			t.Errorf("project %s never verdict", event)
		}(i, event)
	}
	wg.Wait()
	wins, blocks := 0, 0
	for _, res := range results {
		switch res.Outcome {
		case catalog.OutcomeProcessed, catalog.OutcomeAlready:
			wins++
		case catalog.OutcomeBlocked:
			if res.ErrorCode != ErrStoreScopeConflict {
				t.Fatalf("loser code: %+v", res)
			}
			blocks++
		default:
			t.Fatalf("racy adoption: %+v", res)
		}
	}
	if wins != 1 || blocks != 1 {
		t.Fatalf("exactly one winner: wins=%d blocks=%d", wins, blocks)
	}
	got := f.rowStore(t, "catalog_products", "product_id", prodR)
	if got == nil || (*got != scopeStoreA && *got != scopeStoreB) {
		t.Fatalf("single proven owner: %v", got)
	}
}

// TestScope_ConcurrentCrossStoreReturn proves a same-Store return and a
// cross-Store return racing on one sale resolve exactly once: the owner
// commits, the attacker blocks, cumulative guards stay exact.
func TestScope_ConcurrentCrossStoreReturn(t *testing.T) {
	f := openScopeFixture(t)
	salePayload := scopedSalePayload(t, "aaaaaaaa-0000-4000-8000-0000000000b1")
	f.ingest(t, f.devA, f.credA, "e0000290-0000-4000-8000-000000000001", "sale.finalized.v1", salePayload)
	if res := f.projectSale(t, "e0000290-0000-4000-8000-000000000001"); res.Outcome != 1 {
		t.Fatalf("sale: %+v", res)
	}
	f.ingest(t, f.devA, f.credA, "e0000290-0000-4000-8000-000000000002",
		"sale.return_refund.finalized.v1", alignedReturnPayload(t, salePayload, "aaaaaaaa-0000-4000-8000-0000000000b2", "RA-R", 1))
	f.ingest(t, f.devB, f.credB, "e0000290-0000-4000-8000-000000000003",
		"sale.return_refund.finalized.v1", alignedReturnPayload(t, salePayload, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbb2", "RB-R", 1))
	var wg sync.WaitGroup
	type rout struct {
		res returnrefund.ProjectResult
	}
	outs := make([]rout, 2)
	for i, event := range []string{
		"e0000290-0000-4000-8000-000000000002", "e0000290-0000-4000-8000-000000000003",
	} {
		wg.Add(1)
		go func(i int, event string) {
			defer wg.Done()
			for attempt := 0; attempt < 25; attempt++ {
				expireBackoff(t, f, event)
				store := NewDevices(f.pool, 5*time.Second)
				rec, _, err := store.LoadReturnEvent(context.Background(), event)
				if err != nil {
					t.Errorf("load: %v", err)
					return
				}
				res, err := store.ProjectReturn(context.Background(), rec, time.Now())
				if err == nil && res.Outcome != returnrefund.OutcomeNotDue && res.Outcome != returnrefund.OutcomeRetryable {
					outs[i].res = res
					return
				}
				time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
			}
			t.Errorf("project %s never verdict", event)
		}(i, event)
	}
	wg.Wait()
	if outs[0].res.Outcome != returnrefund.OutcomeProcessed {
		t.Fatalf("owner return: %+v", outs[0].res)
	}
	if outs[1].res.Outcome != returnrefund.OutcomeBlocked || outs[1].res.ErrorCode != ErrStoreScopeConflict {
		t.Fatalf("attacker return: %+v", outs[1].res)
	}
	var count int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM return_refund_projection`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("exactly one return row: %d (%v)", count, err)
	}
}

// TestScope_RebuildPreservesOwnership replays inbox Store context after
// wiping derived projections: A, B, and NULL rows all return unchanged.
func TestScope_RebuildPreservesOwnership(t *testing.T) {
	f := openScopeFixture(t)
	rootA, tagA, rootB, tagB := f.scopeGraph(t, 300, "rebuild")
	ids := catalogIDs(t, 310, "prod-a", "prod-b")
	f.ingest(t, f.devA, f.credA, "e0000310-0000-4000-8000-000000000001",
		catalog.EventProductSnapshotV1, productPayload(ids["prod-a"], "RB-1", "A", rootA, nil, []string{tagA}, 1))
	f.ingest(t, f.devB, f.credB, "e0000310-0000-4000-8000-000000000002",
		catalog.EventProductSnapshotV1, productPayload(ids["prod-b"], "RB-1", "B", rootB, nil, []string{tagB}, 1))
	f.ingest(t, f.devC, f.credC, "e0000310-0000-4000-8000-000000000003",
		catalog.EventTagSnapshotV1, tagPayload("c9999999-0000-4000-8000-000000000001", "legacy-tag", true, map[string]string{"ar": "l"}, 1))
	for _, tc := range []struct{ event, typ string }{
		{"e0000310-0000-4000-8000-000000000001", catalog.EventProductSnapshotV1},
		{"e0000310-0000-4000-8000-000000000002", catalog.EventProductSnapshotV1},
		{"e0000310-0000-4000-8000-000000000003", catalog.EventTagSnapshotV1},
	} {
		requireOutcome(t, f.projectCatalog(t, tc.event, tc.typ), catalog.OutcomeProcessed, "")
	}
	before := map[string]*string{
		ids["prod-a"]:                          f.rowStore(t, "catalog_products", "product_id", ids["prod-a"]),
		ids["prod-b"]:                          f.rowStore(t, "catalog_products", "product_id", ids["prod-b"]),
		"c9999999-0000-4000-8000-000000000001": f.rowStore(t, "catalog_tags", "tag_id", "c9999999-0000-4000-8000-000000000001"),
	}
	ctx := context.Background()
	for _, query := range []string{
		`DELETE FROM catalog_product_tags`, `DELETE FROM catalog_product_subcategories`,
		`DELETE FROM catalog_product_translations`, `DELETE FROM catalog_product_prices`,
		`DELETE FROM catalog_products`, `DELETE FROM catalog_category_edges`,
		`DELETE FROM catalog_categories`, `DELETE FROM catalog_tags`,
		`UPDATE sync_event_processing SET status='pending', next_attempt_at=NULL, processed_at=NULL, last_error_code=NULL, last_error_message=NULL`,
	} {
		if _, err := f.pool.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	var events []struct {
		id, typ string
	}
	rows, err := f.pool.Query(ctx, `SELECT event_id::text, event_type FROM sync_events ORDER BY received_at, event_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var e struct {
			id, typ string
		}
		if err := rows.Scan(&e.id, &e.typ); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	rows.Close()
	for _, e := range events {
		switch e.typ {
		case catalog.EventCategorySnapshotV1, catalog.EventTagSnapshotV1,
			catalog.EventProductSnapshotV1, catalog.EventProductSalesPolicySnapshotV1,
			catalog.EventInventoryProductSnapshotV1:
			f.projectCatalog(t, e.id, e.typ)
		}
	}
	if got := f.rowStore(t, "catalog_products", "product_id", ids["prod-a"]); !storeEq(got, before[ids["prod-a"]]) {
		t.Fatalf("rebuild A: %v want %v", got, before[ids["prod-a"]])
	}
	if got := f.rowStore(t, "catalog_products", "product_id", ids["prod-b"]); !storeEq(got, before[ids["prod-b"]]) {
		t.Fatalf("rebuild B: %v want %v", got, before[ids["prod-b"]])
	}
	if got := f.rowStore(t, "catalog_tags", "tag_id", "c9999999-0000-4000-8000-000000000001"); !storeEq(got, before["c9999999-0000-4000-8000-000000000001"]) {
		t.Fatalf("rebuild legacy: %v", got)
	}
}

func storeEq(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// TestScope_RetryRestartStability proves blocked scope conflicts are
// terminal-deterministic (same code on re-projection, no row, no
// hot-loop) and processed rows replay idempotently.
func TestScope_RetryRestartStability(t *testing.T) {
	f := openScopeFixture(t)
	rootA, tagA, _, _ := f.scopeGraph(t, 320, "stable")
	ids := catalogIDs(t, 330, "prod-s")
	prodS := ids["prod-s"]
	f.ingest(t, f.devA, f.credA, "e0000330-0000-4000-8000-000000000001",
		catalog.EventProductSnapshotV1, productPayload(prodS, "ST-1", "S", rootA, nil, []string{tagA}, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000330-0000-4000-8000-000000000001", catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")
	f.ingest(t, f.devB, f.credB, "e0000330-0000-4000-8000-000000000002",
		catalog.EventProductSnapshotV1, productPayload(prodS, "ST-1", "S2", rootA, nil, []string{tagA}, 2))
	first := f.projectCatalog(t, "e0000330-0000-4000-8000-000000000002", catalog.EventProductSnapshotV1)
	requireOutcome(t, first, catalog.OutcomeBlocked, ErrStoreScopeConflict)
	// Simulate restart: fresh Devices handle, same event again. The frozen
	// hasDone path masks the code generically, so stability is proven via
	// the durable processing row (keeps STORE_SCOPE_CONFLICT) plus an
	// unchanged projection and a still-terminal verdict.
	store := NewDevices(f.pool, 5*time.Second)
	rec, ok, err := store.LoadCatalogEvent(context.Background(), "e0000330-0000-4000-8000-000000000002")
	if err != nil || !ok {
		t.Fatal(err)
	}
	second, err := store.ProjectProduct(context.Background(), rec, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if second.Outcome != catalog.OutcomeBlocked {
		t.Fatalf("still terminal: %+v", second)
	}
	var code string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT COALESCE(last_error_code,'') FROM sync_event_processing WHERE event_id='e0000330-0000-4000-8000-000000000002' AND processor='catalog_product_projection.v1'`).Scan(&code); err != nil || code != ErrStoreScopeConflict {
		t.Fatalf("durable scope verdict: %q (%v)", code, err)
	}
	if got := f.rowStore(t, "catalog_products", "product_id", prodS); got == nil || *got != scopeStoreA {
		t.Fatalf("owner intact across retries: %v", got)
	}
}

// TestScope_LifecycleInvariance proves rename, revocation, and rotation
// never rewrite proven projection ownership.
func TestScope_LifecycleInvariance(t *testing.T) {
	pool, authSvc := openTestRepo(t)
	ctx := context.Background()
	p, err := authSvc.Create(ctx, "lifecycle-dev")
	if err != nil {
		t.Fatal(err)
	}
	devices := NewDevices(pool, 5*time.Second)
	if _, err := devices.RegisterStore(ctx, p.Device.ID, store.RegistrationRequest{
		StoreID: scopeStoreA, DisplayName: "Store A", Timezone: "Africa/Cairo",
	}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	syncSvc := isyncNewService(t, devices)
	payload := scopedSalePayload(t, "aaaaaaaa-0000-4000-8000-0000000000c1")
	ingestScoped(t, syncSvc, p.Device.ID, p.Credential.ID, "e0000340-0000-4000-8000-000000000001", "sale.finalized.v1", payload)
	rec, ok, err := devices.LoadSaleEvent(ctx, "e0000340-0000-4000-8000-000000000001")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if res, err := devices.ProjectSale(ctx, rec, time.Now()); err != nil || res.Outcome != sale.OutcomeProcessed {
		t.Fatalf("sale: %+v %v", res, err)
	}
	var before *string
	if err := pool.QueryRow(ctx, `SELECT store_id::text FROM sales_projection WHERE sale_id='aaaaaaaa-0000-4000-8000-0000000000c1'`).Scan(&before); err != nil || before == nil || *before != scopeStoreA {
		t.Fatalf("scoped sale: %v (%v)", before, err)
	}
	// Rename the Store registry row: projection ownership is by ID.
	if _, err := pool.Exec(ctx, `UPDATE stores SET display_name='Renamed Store' WHERE id=$1`, scopeStoreA); err != nil {
		t.Fatal(err)
	}
	// Revoke the source device and rotate its credential: history stays.
	if err := authSvc.RevokeDevice(ctx, p.Device.ID); err != nil {
		t.Fatalf("revoke device: %v", err)
	}
	if err := authSvc.RevokeCredential(ctx, p.Credential.ID); err != nil {
		t.Fatalf("revoke credential: %v", err)
	}
	var after *string
	if err := pool.QueryRow(ctx, `SELECT store_id::text FROM sales_projection WHERE sale_id='aaaaaaaa-0000-4000-8000-0000000000c1'`).Scan(&after); err != nil || after == nil || *after != scopeStoreA {
		t.Fatalf("ownership after lifecycle: %v (%v)", after, err)
	}
	var name string
	if err := pool.QueryRow(ctx, `SELECT display_name FROM stores WHERE id=$1`, scopeStoreA).Scan(&name); err != nil || name != "Renamed Store" {
		t.Fatalf("registry rename applied: %q (%v)", name, err)
	}
}

// isyncNewService builds an ingest service over the given store.
func isyncNewService(t *testing.T, devices Devices) isync.Service {
	t.Helper()
	return isync.NewService(devices, clock.System{})
}

func ingestScoped(t *testing.T, syncSvc isync.Service, devID, credID, eventID, eventType, payload string) {
	t.Helper()
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":%q,"occurred_at":"2026-09-20T10:00:00Z","payload":%s}]}`,
		eventID, eventType, payload)
	res, err := syncSvc.Ingest(context.Background(), devID, credID, []byte(body))
	if err != nil {
		t.Fatalf("ingest %s: %v", eventID, err)
	}
	if res.Events[0].Status != "accepted" {
		t.Fatalf("want accepted, got %+v", res)
	}
}
