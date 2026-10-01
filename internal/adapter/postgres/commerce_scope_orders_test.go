package postgres

// Phase 9C online-order Store ownership on real PostgreSQL. Order Store
// derives from unanimous resolved mapping ownership at reconciliation;
// mixed evidence blocks with zero writes; legacy NULL adopts only on
// deterministic proof. Children inherit through the order root.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
)

func scopeOrderFixture(t *testing.T) (*scopeFixture, string, string) {
	t.Helper()
	f := openScopeFixture(t)
	prodA, prodB := scopeProducts(t, f, 600, "ord", "ORD-001")
	createMapping(t, f, "website", prodA, "700")
	createMapping(t, f, "website", prodB, "701")
	return f, prodA, prodB
}

func orderLine(external string, lineID int64, total int64) orders.OrderLine {
	return orders.OrderLine{
		ExternalLineID: lineID, ExternalProductID: external,
		SKU: "ORD-001", Name: "Item", Quantity: 1,
		SubtotalMinor: total, TotalMinor: total,
	}
}

func scopeSnapshot(provider, external, number string, lines []orders.OrderLine) orders.OrderSnapshot {
	created := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	modified := time.Date(2026, 9, 27, 11, 0, 0, 0, time.UTC)
	return orders.OrderSnapshot{
		ProviderKey: provider, ExternalOrderID: external, OrderNumber: number,
		ProviderStatus: "processing", Canonical: orders.StatusProcessing,
		Currency: "EGP", TotalMinor: 10000,
		CreatedAt: created, ModifiedAt: modified,
		PaymentMethod: "cod", PaymentMethodTitle: "Cash",
		Customer: orders.Customer{FirstName: "A", LastName: "X", Email: "a@example.com", Phone: "201000000001"},
		Billing:  orders.Address{Kind: "billing", FirstName: "A", City: "Cairo", Country: "EG", Email: "a@example.com"},
		Shipping: orders.Address{Kind: "shipping", FirstName: "A", City: "Giza", Country: "EG"},
		Lines:    lines,
	}
}

func reconcileScoped(t *testing.T, f *scopeFixture, snapshot orders.OrderSnapshot) (orders.ReconcileOutcome, error) {
	t.Helper()
	ctx := context.Background()
	store := NewDevices(f.pool, 5*time.Second)
	generation, err := store.BeginOrderReconcile(ctx, snapshot.ProviderKey, snapshot.ExternalOrderID)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	return store.ReconcileProjectedOrder(ctx, snapshot, orders.Fingerprint(snapshot), generation)
}

func mustReconcile(t *testing.T, f *scopeFixture, snapshot orders.OrderSnapshot) orders.ReconcileOutcome {
	t.Helper()
	outcome, err := reconcileScoped(t, f, snapshot)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if outcome.Superseded {
		t.Fatal("fresh generation must not supersede")
	}
	return outcome
}

func orderStore(t *testing.T, f *scopeFixture, provider, external string) *string {
	t.Helper()
	var raw *string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT store_id::text FROM commerce_online_orders WHERE provider_key=$1 AND external_order_id=$2`,
		provider, external).Scan(&raw); err != nil {
		t.Fatalf("order store: %v", err)
	}
	return raw
}

// TestOrder_AllMappedScopes proves unanimous mapped lines attribute the
// order to their Store.
func TestOrder_AllMappedScopes(t *testing.T) {
	f, _, _ := scopeOrderFixture(t)
	outcome := mustReconcile(t, f, scopeSnapshot("website", "800", "W-800", []orders.OrderLine{orderLine("700", 1, 10000)}))
	if outcome.Revision != 1 || !outcome.Changed {
		t.Fatalf("initial: %+v", outcome)
	}
	if got := orderStore(t, f, "website", "800"); got == nil || *got != scopeStoreA {
		t.Fatalf("order A: %v", got)
	}
	outcome = mustReconcile(t, f, scopeSnapshot("website", "801", "W-801", []orders.OrderLine{orderLine("701", 1, 10000)}))
	if outcome.Revision != 1 || !outcome.Changed {
		t.Fatalf("initial B: %+v", outcome)
	}
	if got := orderStore(t, f, "website", "801"); got == nil || *got != scopeStoreB {
		t.Fatalf("order B: %v", got)
	}
}

// TestOrder_MixedBlocked proves A+B lines fail safely: terminal conflict,
// no row, no history, no partial state.
func TestOrder_MixedBlocked(t *testing.T) {
	f, _, _ := scopeOrderFixture(t)
	_, err := reconcileScoped(t, f, scopeSnapshot("website", "802", "W-802",
		[]orders.OrderLine{orderLine("700", 1, 5000), orderLine("701", 2, 5000)}))
	if err == nil {
		t.Fatal("mixed order must block")
	}
	if code, blocked := orders.IsBlocked(err); !blocked || code != orders.CodeCommerceStoreScopeConflict {
		t.Fatalf("stable conflict code: %v (%v)", code, err)
	}
	var count int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM commerce_online_orders WHERE provider_key='website' AND external_order_id='802'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("no order row: %d (%v)", count, err)
	}
	var history int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM commerce_online_order_status_history WHERE provider_key='website' AND external_order_id='802'`).Scan(&history); err != nil || history != 0 {
		t.Fatalf("no history: %d (%v)", history, err)
	}
}

// TestOrder_UnresolvedStaysNull proves lines without mappings project
// truthfully unscoped (mapping_complete=false), never guessed.
func TestOrder_UnresolvedStaysNull(t *testing.T) {
	f, _, _ := scopeOrderFixture(t)
	outcome := mustReconcile(t, f, scopeSnapshot("website", "803", "W-803",
		[]orders.OrderLine{orderLine("no-such-external", 1, 10000)}))
	if outcome.Revision != 1 {
		t.Fatalf("initial: %+v", outcome)
	}
	if got := orderStore(t, f, "website", "803"); got != nil {
		t.Fatalf("unresolved stays NULL: %v", got)
	}
	var complete bool
	var unmapped int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT mapping_complete, unmapped_lines FROM commerce_online_orders WHERE provider_key='website' AND external_order_id='803'`).Scan(&complete, &unmapped); err != nil || complete || unmapped != 1 {
		t.Fatalf("incomplete markers: %v %d (%v)", complete, unmapped, err)
	}
}

// TestOrder_LegacyAdoption proves a NULL order adopts Store A once its
// lines deterministically resolve to A (same fingerprint path forces the
// write so adoption becomes durable).
func TestOrder_LegacyAdoption(t *testing.T) {
	f := openScopeFixture(t)
	outcome := mustReconcile(t, f, scopeSnapshot("website", "804", "W-804",
		[]orders.OrderLine{orderLine("future-700", 1, 10000)}))
	if outcome.Revision != 1 {
		t.Fatalf("initial NULL: %+v", outcome)
	}
	if got := orderStore(t, f, "website", "804"); got != nil {
		t.Fatalf("starts NULL: %v", got)
	}
	prodA, _ := scopeProducts(t, f, 610, "ado", "ORD-002")
	createMapping(t, f, "website", prodA, "future-700")
	// Same content, newly provable ownership: revision advances with Store.
	outcome = mustReconcile(t, f, scopeSnapshot("website", "804", "W-804",
		[]orders.OrderLine{orderLine("future-700", 1, 10000)}))
	if outcome.Revision != 2 || !outcome.Changed {
		t.Fatalf("adoption writes revision 2: %+v", outcome)
	}
	if got := orderStore(t, f, "website", "804"); got == nil || *got != scopeStoreA {
		t.Fatalf("adopted A: %v", got)
	}
}

// TestOrder_LegacyMixedStaysNull proves conflicting later evidence keeps
// a legacy order truthfully NULL instead of picking a side.
func TestOrder_LegacyMixedStaysNull(t *testing.T) {
	f := openScopeFixture(t)
	outcome := mustReconcile(t, f, scopeSnapshot("website", "805", "W-805",
		[]orders.OrderLine{orderLine("mix-700", 1, 5000), orderLine("mix-701", 2, 5000)}))
	if outcome.Revision != 1 {
		t.Fatalf("initial NULL: %+v", outcome)
	}
	prodA, prodB := scopeProducts(t, f, 620, "adx", "ORD-003")
	createMapping(t, f, "website", prodA, "mix-700")
	createMapping(t, f, "website", prodB, "mix-701")
	_, err := reconcileScoped(t, f, scopeSnapshot("website", "805", "W-805",
		[]orders.OrderLine{orderLine("mix-700", 1, 5000), orderLine("mix-701", 2, 5000)}))
	if err == nil {
		t.Fatal("mixed later evidence must block")
	}
	if code, blocked := orders.IsBlocked(err); !blocked || code != orders.CodeCommerceStoreScopeConflict {
		t.Fatalf("stable code: %v (%v)", code, err)
	}
	if got := orderStore(t, f, "website", "805"); got != nil {
		t.Fatalf("stays NULL: %v", got)
	}
}

// TestOrder_ExistingAAppearsBBlocked proves an established Store A order
// survives contradictory later evidence with no rewrite and no history.
func TestOrder_ExistingAAppearsBBlocked(t *testing.T) {
	f, _, _ := scopeOrderFixture(t)
	mustReconcile(t, f, scopeSnapshot("website", "806", "W-806", []orders.OrderLine{orderLine("700", 1, 10000)}))
	before := orderRow(t, f, "website", "806")
	_, err := reconcileScoped(t, f, scopeSnapshot("website", "806", "W-806", []orders.OrderLine{orderLine("701", 1, 10000)}))
	if err == nil {
		t.Fatal("contradictory ownership must block")
	}
	if code, blocked := orders.IsBlocked(err); !blocked || code != orders.CodeCommerceStoreScopeConflict {
		t.Fatalf("stable code: %v (%v)", code, err)
	}
	after := orderRow(t, f, "website", "806")
	if before != after {
		t.Fatalf("row intact:\n%s\n%s", before, after)
	}
	var history int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM commerce_online_order_status_history WHERE provider_key='website' AND external_order_id='806'`).Scan(&history); err != nil || history != 1 {
		t.Fatalf("no new history: %d (%v)", history, err)
	}
}

func orderRow(t *testing.T, f *scopeFixture, provider, external string) string {
	t.Helper()
	var out string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT provider_key||'|'||external_order_id||'|'||revision::text||'|'||COALESCE(store_id::text,'NULL')||'|'||COALESCE(fingerprint,'') FROM commerce_online_orders WHERE provider_key=$1 AND external_order_id=$2`,
		provider, external).Scan(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestOrder_ReadIsolation proves Store-scoped lists, details, and counts
// isolate graphs and PII; cross-Store detail reads 404; global reads see
// everything.
func TestOrder_ReadIsolation(t *testing.T) {
	f, _, _ := scopeOrderFixture(t)
	mkOrder := func(external, first, email string, lines []orders.OrderLine) {
		snapshot := scopeSnapshot("website", external, "W-"+external, lines)
		snapshot.Customer.FirstName = first
		snapshot.Customer.Email = email
		snapshot.Billing.Email = email
		mustReconcile(t, f, snapshot)
	}
	mkOrder("810", "Alice", "alice@example.com", []orders.OrderLine{orderLine("700", 1, 10000)})
	mkOrder("811", "Bob", "bob@example.com", []orders.OrderLine{orderLine("701", 1, 10000)})
	mkOrder("812", "Legacy", "legacy@example.com", []orders.OrderLine{orderLine("unmapped-x", 1, 10000)})
	devices := NewDevices(f.pool, 5*time.Second)
	ctx := context.Background()
	summaries, err := devices.ListOrderSummariesForStore(ctx, scopeStoreA, "", "", 100, nil)
	if err != nil || len(summaries) != 1 || summaries[0].ExternalOrderID != "810" {
		t.Fatalf("A list: %+v (%v)", summaries, err)
	}
	summaries, err = devices.ListOrderSummariesForStore(ctx, scopeStoreB, "", "", 100, nil)
	if err != nil || len(summaries) != 1 || summaries[0].ExternalOrderID != "811" {
		t.Fatalf("B list: %+v (%v)", summaries, err)
	}
	detail, err := devices.GetOrderDetailForStore(ctx, scopeStoreA, "website", "810")
	if err != nil {
		t.Fatal(err)
	}
	if detail.CustomerEmail != "alice@example.com" || len(detail.Lines) != 1 || len(detail.Addresses) != 2 || len(detail.StatusHistory) != 1 {
		t.Fatalf("A detail graph: %+v", detail)
	}
	if _, err := devices.GetOrderDetailForStore(ctx, scopeStoreA, "website", "811"); err == nil {
		t.Fatal("A must not read B order")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("repository-standard not-found: %v", err)
	}
	full, err := devices.GetOrderDetail(ctx, "website", "811")
	if err != nil || full.CustomerEmail != "bob@example.com" {
		t.Fatalf("global admin read intact: %+v (%v)", full, err)
	}
	counts, err := devices.CountOrdersByStatusForStore(ctx, scopeStoreA, "")
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, c := range counts {
		total += c.Total
	}
	if total != 1 {
		t.Fatalf("A counts: %+v", counts)
	}
	page, err := devices.ListOrderPageForStore(ctx, scopeStoreB, "", "", 10, nil)
	if err != nil || len(page.Items) != 1 || page.Next != nil {
		t.Fatalf("B page: %+v (%v)", page, err)
	}
}

// TestOrder_DeletionPreserves proves tombstones keep established Store
// ownership with frozen deletion semantics.
func TestOrder_DeletionPreserves(t *testing.T) {
	f, _, _ := scopeOrderFixture(t)
	mustReconcile(t, f, scopeSnapshot("website", "820", "W-820", []orders.OrderLine{orderLine("700", 1, 10000)}))
	devices := NewDevices(f.pool, 5*time.Second)
	ctx := context.Background()
	loaded, _, found, err := devices.LoadProjectedOrder(ctx, "website", "820")
	if err != nil || !found {
		t.Fatalf("load: %v %v", found, err)
	}
	loaded.ProviderDeleted = true
	loaded.Canonical = orders.StatusDeleted
	generation, err := devices.BeginOrderReconcile(ctx, "website", "820")
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := devices.ReconcileProjectedOrder(ctx, loaded, orders.Fingerprint(loaded), generation)
	if err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	if !outcome.Changed {
		t.Fatalf("deletion lands: %+v", outcome)
	}
	if got := orderStore(t, f, "website", "820"); got == nil || *got != scopeStoreA {
		t.Fatalf("tombstone keeps A: %v", got)
	}
	var status string
	if err := f.pool.QueryRow(ctx,
		`SELECT canonical_status FROM commerce_online_orders WHERE provider_key='website' AND external_order_id='820'`).Scan(&status); err != nil || status != "DELETED" {
		t.Fatalf("deleted status: %q (%v)", status, err)
	}
}

// TestOrder_ReplayIdempotent proves same-snapshot replay is a silent
// no-op with no duplicate history.
func TestOrder_ReplayIdempotent(t *testing.T) {
	f, _, _ := scopeOrderFixture(t)
	snapshot := scopeSnapshot("website", "821", "W-821", []orders.OrderLine{orderLine("700", 1, 10000)})
	first := mustReconcile(t, f, snapshot)
	second := mustReconcile(t, f, snapshot)
	if first.Revision != 1 || second.Revision != 1 || second.Changed {
		t.Fatalf("replay no-op: %+v %+v", first, second)
	}
	var history int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM commerce_online_order_status_history WHERE provider_key='website' AND external_order_id='821'`).Scan(&history); err != nil || history != 1 {
		t.Fatalf("one history row: %d (%v)", history, err)
	}
}

// TestOrder_ExactMoney proves >2^53 minor-unit totals reconcile exactly.
func TestOrder_ExactMoney(t *testing.T) {
	f, _, _ := scopeOrderFixture(t)
	const big = int64(9007199254740993)
	lines := []orders.OrderLine{orderLine("700", 1, big)}
	snapshot := scopeSnapshot("website", "822", "W-822", lines)
	snapshot.TotalMinor = big
	mustReconcile(t, f, snapshot)
	var total int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT total_minor FROM commerce_online_orders WHERE provider_key='website' AND external_order_id='822'`).Scan(&total); err != nil || total != big {
		t.Fatalf("exact total: %d (%v)", total, err)
	}
	if got := orderStore(t, f, "website", "822"); got == nil || *got != scopeStoreA {
		t.Fatalf("big order scoped: %v", got)
	}
}

// TestOrder_ConcurrentReconcile proves fence serialization: parallel
// reconciles converge without mixed state or duplicate history.
func TestOrder_ConcurrentReconcile(t *testing.T) {
	f, _, _ := scopeOrderFixture(t)
	var wg sync.WaitGroup
	type rout struct {
		outcome orders.ReconcileOutcome
		err     error
	}
	outs := make([]rout, 4)
	for i := range outs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := NewDevices(f.pool, 5*time.Second)
			ctx := context.Background()
			snapshot := scopeSnapshot("website", "823", "W-823", []orders.OrderLine{orderLine("700", 1, 10000)})
			generation, err := store.BeginOrderReconcile(ctx, snapshot.ProviderKey, snapshot.ExternalOrderID)
			if err != nil {
				outs[i].err = err
				return
			}
			outs[i].outcome, outs[i].err = store.ReconcileProjectedOrder(ctx, snapshot, orders.Fingerprint(snapshot), generation)
		}(i)
	}
	wg.Wait()
	revisions := map[int64]int{}
	for _, o := range outs {
		if o.err != nil {
			if _, blocked := orders.IsBlocked(o.err); blocked {
				t.Fatalf("no scope block expected: %v", o.err)
			}
			t.Fatalf("reconcile: %v", o.err)
		}
		if !o.outcome.Superseded {
			revisions[o.outcome.Revision]++
		}
	}
	if got := orderStore(t, f, "website", "823"); got == nil || *got != scopeStoreA {
		t.Fatalf("converged store: %v", got)
	}
	var history int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM commerce_online_order_status_history WHERE provider_key='website' AND external_order_id='823'`).Scan(&history); err != nil || history != 1 {
		t.Fatalf("one history row: %d (%v)", history, err)
	}
}

// TestOrder_RestartStability proves a fresh handle re-reconciling the
// same snapshot converges identically (no duplicate rows/history).
func TestOrder_RestartStability(t *testing.T) {
	f, _, _ := scopeOrderFixture(t)
	snapshot := scopeSnapshot("website", "824", "W-824", []orders.OrderLine{orderLine("701", 1, 10000)})
	first := mustReconcile(t, f, snapshot)
	second := mustReconcile(t, f, snapshot)
	if first.Revision != second.Revision || second.Changed {
		t.Fatalf("restart-stable: %+v %+v", first, second)
	}
	if got := orderStore(t, f, "website", "824"); got == nil || *got != scopeStoreB {
		t.Fatalf("store stable: %v", got)
	}
}

// TestOrder_ExistingPreservedOnUnmapped proves an established Store
// survives a later unresolvable reconciliation: no null-out, content
// revision advances normally.
func TestOrder_ExistingPreservedOnUnmapped(t *testing.T) {
	f, _, _ := scopeOrderFixture(t)
	mustReconcile(t, f, scopeSnapshot("website", "830", "W-830", []orders.OrderLine{orderLine("700", 1, 10000)}))
	next := scopeSnapshot("website", "830", "W-830", []orders.OrderLine{orderLine("unknown-ext", 1, 10000)})
	next.ProviderStatus = "processing"
	next.ModifiedAt = next.ModifiedAt.Add(time.Hour)
	outcome := mustReconcile(t, f, next)
	if outcome.Revision != 2 || !outcome.Changed {
		t.Fatalf("content revision advances: %+v", outcome)
	}
	if got := orderStore(t, f, "website", "830"); got == nil || *got != scopeStoreA {
		t.Fatalf("established Store preserved: %v", got)
	}
}

// TestOrder_GhostTombstoneNull proves deletion of an unseen order writes
// a truthfully unscoped tombstone (no invented ownership).
func TestOrder_GhostTombstoneNull(t *testing.T) {
	f := openScopeFixture(t)
	devices := NewDevices(f.pool, 5*time.Second)
	ctx := context.Background()
	loaded := scopeSnapshot("website", "831", "W-831", []orders.OrderLine{orderLine("ghost-ext", 1, 100)})
	loaded.ProviderDeleted = true
	loaded.Canonical = orders.StatusDeleted
	generation, err := devices.BeginOrderReconcile(ctx, "website", "831")
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := devices.ReconcileProjectedOrder(ctx, loaded, orders.Fingerprint(loaded), generation)
	if err != nil {
		t.Fatalf("ghost tombstone: %v", err)
	}
	if outcome.Revision != 1 || !outcome.Changed {
		t.Fatalf("tombstone lands: %+v", outcome)
	}
	if got := orderStore(t, f, "website", "831"); got != nil {
		t.Fatalf("ghost tombstone unscoped: %v", got)
	}
	var status string
	if err := f.pool.QueryRow(ctx,
		`SELECT canonical_status FROM commerce_online_orders WHERE provider_key='website' AND external_order_id='831'`).Scan(&status); err != nil || status != "DELETED" {
		t.Fatalf("deleted status: %q (%v)", status, err)
	}
}
