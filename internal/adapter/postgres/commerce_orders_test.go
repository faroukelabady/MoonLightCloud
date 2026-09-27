package postgres

import (
	"context"
	"crypto/sha256"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
)

// orderTestSnapshot builds a normalized snapshot for projection tests.
func orderTestSnapshot(provider, external string, status orders.CanonicalStatus, providerStatus string, total int64) orders.OrderSnapshot {
	created := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	modified := time.Date(2026, 9, 27, 11, 0, 0, 0, time.UTC)
	return orders.OrderSnapshot{
		ProviderKey: provider, ExternalOrderID: external, OrderNumber: external,
		ProviderStatus: providerStatus, Canonical: status,
		Currency: "EGP", TotalMinor: total,
		CreatedAt: created, ModifiedAt: modified,
		PaymentMethod: "cod", PaymentMethodTitle: "Cash",
		Customer: orders.Customer{FirstName: "A", Email: "a@example.com"},
		Billing:  orders.Address{Kind: "billing", City: "Cairo"},
		Shipping: orders.Address{Kind: "shipping", City: "Giza"},
		Lines: []orders.OrderLine{
			{ExternalLineID: 1, ExternalProductID: "500", SKU: "PAP-1", Name: "X", Quantity: 1, TotalMinor: total},
		},
	}
}

func reconcileSnapshot(t *testing.T, env *saleEnv, snapshot orders.OrderSnapshot) (int64, bool) {
	t.Helper()
	ctx := context.Background()
	store := catalogStore(env)
	generation, err := store.BeginOrderReconcile(ctx, snapshot.ProviderKey, snapshot.ExternalOrderID)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	outcome, err := store.ReconcileProjectedOrder(ctx, snapshot, orders.Fingerprint(snapshot), generation)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if outcome.Superseded {
		t.Fatal("fresh generation must not supersede")
	}
	return outcome.Revision, outcome.Changed
}

// TestCommerceOrderProjectionMatrix proves initial revision, semantic
// no-op, status/money/line/address/mapping changes, and unknown status.
func TestCommerceOrderProjectionMatrix(t *testing.T) {
	env := openSaleEnv(t)
	snapshot := orderTestSnapshot("website", "200", orders.StatusPending, "pending", 10000)

	revision, changed := reconcileSnapshot(t, env, snapshot)
	if revision != 1 || !changed {
		t.Fatalf("initial: %d %v", revision, changed)
	}
	// Same state: no-op, same revision, no duplicate history.
	revision, changed = reconcileSnapshot(t, env, snapshot)
	if revision != 1 || changed {
		t.Fatalf("no-op: %d %v", revision, changed)
	}
	if n := historyCount(t, env, "website", "200"); n != 1 {
		t.Fatalf("one history row, got %d", n)
	}

	// Status change: rev 2 + history.
	snapshot.ProviderStatus = "processing"
	snapshot.Canonical = orders.StatusProcessing
	revision, changed = reconcileSnapshot(t, env, snapshot)
	if revision != 2 || !changed {
		t.Fatalf("status change: %d %v", revision, changed)
	}
	if n := historyCount(t, env, "website", "200"); n != 2 {
		t.Fatalf("two history rows, got %d", n)
	}

	// Money-only change: rev 3, no new history.
	snapshot.TotalMinor = 12000
	revision, changed = reconcileSnapshot(t, env, snapshot)
	if revision != 3 || !changed {
		t.Fatalf("money change: %d %v", revision, changed)
	}
	if n := historyCount(t, env, "website", "200"); n != 2 {
		t.Fatalf("no history without status change, got %d", n)
	}

	// Mapping change: resolve the line, rev 4.
	productID := "11111111-1111-4111-8111-111111111111"
	if _, err := catalogStore(env).CreateProductMapping(context.Background(), "website", productID, "500"); err != nil {
		t.Fatal(err)
	}
	revision, changed = reconcileSnapshot(t, env, snapshot)
	if revision != 4 || !changed {
		t.Fatalf("mapping change: %d %v", revision, changed)
	}
	stored, _, found, err := catalogStore(env).LoadProjectedOrder(context.Background(), "website", "200")
	if err != nil || !found {
		t.Fatal("stored")
	}
	if !stored.MappingComplete || stored.UnmappedLines != 0 || len(stored.Lines) != 1 ||
		stored.Lines[0].MoonlightProduct == nil || *stored.Lines[0].MoonlightProduct != productID {
		t.Fatalf("resolved: %+v", stored)
	}
}

func historyCount(t *testing.T, env *saleEnv, provider, external string) int {
	t.Helper()
	var n int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM commerce_online_order_status_history WHERE provider_key=$1 AND external_order_id=$2`,
		provider, external).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestCommerceOrderAtomicity injects a failure after the header write by
// violating a line constraint: the previous full revision must stay
// visible with no partial children.
func TestCommerceOrderAtomicity(t *testing.T) {
	env := openSaleEnv(t)
	snapshot := orderTestSnapshot("website", "201", orders.StatusPending, "pending", 10000)
	if revision, changed := reconcileSnapshot(t, env, snapshot); revision != 1 || !changed {
		t.Fatalf("initial: %d %v", revision, changed)
	}
	// Break the second write with an invalid line (quantity 0 is
	// rejected by the CHECK constraint inside the same transaction).
	snapshot.TotalMinor = 12000
	snapshot.ModifiedAt = snapshot.ModifiedAt.Add(time.Hour)
	snapshot.Lines[0].Quantity = 0
	store := catalogStore(env)
	generation, err := store.BeginOrderReconcile(context.Background(), snapshot.ProviderKey, snapshot.ExternalOrderID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ReconcileProjectedOrder(context.Background(), snapshot, orders.Fingerprint(snapshot), generation)
	if err == nil {
		t.Fatal("constraint failure must surface")
	}
	stored, revision, found, rerr := catalogStore(env).LoadProjectedOrder(context.Background(), "website", "201")
	if rerr != nil || !found {
		t.Fatal("previous revision intact")
	}
	if revision != 1 || stored.TotalMinor != 10000 || len(stored.Lines) != 1 || stored.Lines[0].Quantity != 1 {
		t.Fatalf("no mixed version: %+v rev %d", stored, revision)
	}
}

// TestCommerceOrderUnmappedLine proves foreign lines persist safely with
// completeness exposed and no product fabrication.
func TestCommerceOrderUnmappedLine(t *testing.T) {
	env := openSaleEnv(t)
	snapshot := orderTestSnapshot("website", "202", orders.StatusPending, "pending", 10000)
	snapshot.Lines = append(snapshot.Lines, orders.OrderLine{
		ExternalLineID: 2, ExternalProductID: "999", SKU: "FOREIGN", Name: "Foreign", Quantity: 1, TotalMinor: 500,
	})
	revision, changed := reconcileSnapshot(t, env, snapshot)
	if revision != 1 || !changed {
		t.Fatalf("initial: %d %v", revision, changed)
	}
	stored, _, _, err := catalogStore(env).LoadProjectedOrder(context.Background(), "website", "202")
	if err != nil {
		t.Fatal(err)
	}
	if stored.MappingComplete || stored.UnmappedLines != 2 {
		t.Fatalf("completeness: %+v", stored)
	}
	var mappings int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM commerce_product_mappings WHERE provider_key='website'`).Scan(&mappings); err != nil || mappings != 0 {
		t.Fatalf("no fabricated mappings: %d (%v)", mappings, err)
	}
}

// TestCommerceOrderRenamePreservesHistory proves historical line
// snapshots survive a current-product rename: the Woo name/SKU stay,
// while the resolved product identity is retained.
func TestCommerceOrderRenamePreservesHistory(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	root, sub, tag := catalogProductFixture(t, env, 300, "ordren")
	productID := "e0003000-0000-4000-8000-000000000000"
	ingestCatalog(t, env, "d0007000-0000-4000-8000-000000000000", catalog.EventProductSnapshotV1, "2026-09-20T12:00:00Z",
		productPayload(productID, "PAP-REN", "Old Name", root, []string{sub}, []string{tag}, 1))
	projectCatalogOnce(t, env, "d0007000-0000-4000-8000-000000000000")
	if _, err := store.CreateProductMapping(ctx, "website", productID, "500"); err != nil {
		t.Fatal(err)
	}
	snapshot := orderTestSnapshot("website", "203", orders.StatusPending, "pending", 10000)
	if revision, changed := reconcileSnapshot(t, env, snapshot); revision != 1 || !changed {
		t.Fatalf("initial: %d %v", revision, changed)
	}
	// Rename the current product at catalog rev 2.
	ingestCatalog(t, env, "d0007001-0000-4000-8000-000000000001", catalog.EventProductSnapshotV1, "2026-09-20T13:00:00Z",
		productPayload(productID, "PAP-REN", "New Name", root, []string{sub}, []string{tag}, 2))
	projectCatalogOnce(t, env, "d0007001-0000-4000-8000-000000000001")

	stored, _, _, err := store.LoadProjectedOrder(ctx, "website", "203")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Lines) != 1 || stored.Lines[0].Name != "X" || stored.Lines[0].SKU != "PAP-1" {
		t.Fatalf("historical snapshot: %+v", stored.Lines)
	}
	if stored.Lines[0].MoonlightProduct == nil || *stored.Lines[0].MoonlightProduct != productID {
		t.Fatalf("identity retained: %+v", stored.Lines[0])
	}
}

// TestCommerceOrderSurvivesCatalogRebuild proves order state and
// mappings survive the documented catalog rebuild: clear derived
// catalog tables, replay inbox, and the order still references the
// same MoonLight product with identical history.
func TestCommerceOrderSurvivesCatalogRebuild(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	root, sub, tag := catalogProductFixture(t, env, 310, "orbr")
	productID := "e0003100-0000-4000-8000-000000000000"
	ingestCatalog(t, env, "d0007100-0000-4000-8000-000000000000", catalog.EventProductSnapshotV1, "2026-09-20T12:00:00Z",
		productPayload(productID, "PAP-RB", "x", root, []string{sub}, []string{tag}, 1))
	projectCatalogOnce(t, env, "d0007100-0000-4000-8000-000000000000")
	if _, err := store.CreateProductMapping(ctx, "website", productID, "500"); err != nil {
		t.Fatal(err)
	}
	snapshot := orderTestSnapshot("website", "210", orders.StatusProcessing, "processing", 15000)
	if revision, changed := reconcileSnapshot(t, env, snapshot); revision != 1 || !changed {
		t.Fatalf("initial: %d %v", revision, changed)
	}

	// Documented catalog rebuild: derived tables only, inbox preserved.
	for _, table := range []string{
		"catalog_product_tags", "catalog_product_subcategories", "catalog_product_translations",
		"catalog_product_prices", "catalog_products", "catalog_category_edges",
		"catalog_tags", "catalog_categories",
	} {
		if _, err := env.pool.Exec(ctx, `DELETE FROM `+table); err != nil {
			t.Fatal(err)
		}
	}
	// Order + mapping state untouched by the rebuild.
	stored, rev, _, err := store.LoadProjectedOrder(ctx, "website", "210")
	if err != nil {
		t.Fatal(err)
	}
	if rev != 1 || len(stored.Lines) != 1 || stored.Lines[0].MoonlightProduct == nil ||
		*stored.Lines[0].MoonlightProduct != productID {
		t.Fatalf("order intact: %+v rev %d", stored, rev)
	}
	var mappings int
	if err := env.pool.QueryRow(ctx,
		`SELECT count(*) FROM commerce_product_mappings WHERE provider_key='website'`).Scan(&mappings); err != nil || mappings != 1 {
		t.Fatalf("mapping intact: %d (%v)", mappings, err)
	}
	var webhooks int
	if err := env.pool.QueryRow(ctx,
		`SELECT count(*) FROM commerce_online_order_webhook_events`).Scan(&webhooks); err != nil {
		t.Fatal(err)
	}

	// Catalog replays; order line still references the same product.
	drainCatalog(t, env)
	restored, _, _, err := store.LoadProjectedOrder(ctx, "website", "210")
	if err != nil {
		t.Fatal(err)
	}
	if restored.Lines[0].MoonlightProduct == nil || *restored.Lines[0].MoonlightProduct != productID {
		t.Fatalf("identity stable: %+v", restored.Lines[0])
	}
	var history int
	if err := env.pool.QueryRow(ctx,
		`SELECT count(*) FROM commerce_online_order_status_history WHERE provider_key='website' AND external_order_id='210'`).Scan(&history); err != nil || history != 1 {
		t.Fatalf("history intact: %d (%v)", history, err)
	}
}

// TestCommerceOrderReads proves list/detail/counts/stats over projected
// orders with deterministic pagination and exact string money.
func TestCommerceOrderReads(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	first := orderTestSnapshot("website", "220", orders.StatusPending, "pending", 9007199254740993)
	second := orderTestSnapshot("website", "221", orders.StatusCompleted, "completed", 1300)
	second.CreatedAt = second.CreatedAt.Add(-time.Hour)
	for _, snapshot := range []orders.OrderSnapshot{first, second} {
		generation, err := store.BeginOrderReconcile(ctx, snapshot.ProviderKey, snapshot.ExternalOrderID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReconcileProjectedOrder(ctx, snapshot, orders.Fingerprint(snapshot), generation); err != nil {
			t.Fatal(err)
		}
	}

	summaries, err := store.ListOrderSummaries(ctx, "", "", 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 2 {
		t.Fatalf("two orders: %d", len(summaries))
	}
	// created_at DESC: 220 first.
	if summaries[0].ExternalOrderID != "220" || summaries[1].ExternalOrderID != "221" {
		t.Fatalf("ordering: %+v", summaries)
	}
	if summaries[0].TotalMinor != "9007199254740993" {
		t.Fatalf("exact money string: %q", summaries[0].TotalMinor)
	}
	// Cursor pagination: second page returns the remainder exactly once.
	page, err := store.ListOrderSummaries(ctx, "", "", 10, &orders.OrderCursor{
		CreatedAt:   mustParseTime(t, summaries[0].CreatedAt),
		ProviderKey: summaries[0].ProviderKey, ExternalOrderID: summaries[0].ExternalOrderID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].ExternalOrderID != "221" {
		t.Fatalf("cursor page: %+v", page)
	}
	// Status filter.
	pending, err := store.ListOrderSummaries(ctx, "", "PENDING", 10, nil)
	if err != nil || len(pending) != 1 || pending[0].ExternalOrderID != "220" {
		t.Fatalf("status filter: %+v %v", pending, err)
	}
	// Detail with lines, addresses, history.
	detail, err := store.GetOrderDetail(ctx, "website", "220")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Lines) != 1 || len(detail.Addresses) != 2 || len(detail.StatusHistory) != 1 {
		t.Fatalf("detail: %+v", detail)
	}
	if detail.Summary.TotalMinor != "9007199254740993" {
		t.Fatalf("detail money: %q", detail.Summary.TotalMinor)
	}
	// Counts by status.
	counts, err := store.CountOrdersByStatus(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	byStatus := map[string]int64{}
	for _, count := range counts {
		byStatus[count.CanonicalStatus] = count.Total
	}
	if byStatus["PENDING"] != 1 || byStatus["COMPLETED"] != 1 {
		t.Fatalf("counts: %+v", byStatus)
	}
	// Webhook inbox stats shape.
	stats, err := store.OrderInboxStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 0 || stats.Retry != 0 || stats.Blocked != 0 {
		t.Fatalf("empty inbox stats: %+v", stats)
	}
}

func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// TestCommerceWebhookInboxMinimization proves the durable inbox row
// carries delivery metadata plus hash only: no email, phone, name,
// address, or raw JSON, even for a PII-laden delivery.
func TestCommerceWebhookInboxMinimization(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	body := []byte(`{"id":400,"billing":{"first_name":"Layla","email":"layla-pii@example.com","phone":"+201111111111","address_1":"5 PII Street"},"customer_note":"PII note here"}`)
	sum := sha256.Sum256(body)
	outcome, err := catalogStore(env).InsertOrderWebhookEvent(ctx, "website", "delivery-pii", orders.TopicOrderCreated, "400", sum[:], nil)
	if err != nil || outcome != orders.WebhookInserted {
		t.Fatalf("insert: %v %v", outcome, err)
	}
	var dump string
	if err := env.pool.QueryRow(ctx,
		`SELECT row_to_json(t)::text FROM commerce_online_order_webhook_events t WHERE provider_key='website' AND delivery_id='delivery-pii'`).Scan(&dump); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"layla-pii", "+201111111111", "Layla", "PII Street", "PII note", `"id":400`} {
		if strings.Contains(dump, forbidden) {
			t.Fatalf("PII in inbox row %q: %s", forbidden, dump)
		}
	}
}

// TestCommerceOrderDeleteThenRevive proves a tombstoned order revives
// when the provider serves it again: deleted clears, revision advances,
// no fabricated carryover.
func TestCommerceOrderDeleteThenRevive(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	snapshot := orderTestSnapshot("website", "230", orders.StatusProcessing, "processing", 15000)
	if revision, changed := reconcileSnapshot(t, env, snapshot); revision != 1 || !changed {
		t.Fatalf("initial: %d %v", revision, changed)
	}
	registry := commerce.NewRegistry()
	if err := registry.Register("website", &stubCommerceOrderProvider{key: "website"}); err != nil {
		t.Fatal(err)
	}
	service := orders.NewOrderService(registry, store, nilLogger())
	deleted, err := service.ReconcileDeletion(ctx, "website", "230")
	if err != nil || deleted.Revision != 2 || !deleted.ProviderDeleted {
		t.Fatalf("tombstone: %+v %v", deleted, err)
	}
	// Provider serves the live order again: revive clears deletion and
	// advances the revision.
	live := orderTestSnapshot("website", "230", orders.StatusProcessing, "processing", 15000)
	reviving := &stubCommerceOrderProvider{key: "website", snapshots: map[string]orders.OrderSnapshot{"230": live}}
	revRegistry := commerce.NewRegistry()
	if err := revRegistry.Register("website", reviving); err != nil {
		t.Fatal(err)
	}
	reviveService := orders.NewOrderService(revRegistry, store, nilLogger())
	revived, err := reviveService.ReconcileOrder(ctx, "website", "230")
	if err != nil {
		t.Fatalf("revive: %v", err)
	}
	if revived.Revision != 3 || revived.ProviderDeleted || !revived.Changed {
		t.Fatalf("revived: %+v", revived)
	}
	stored, _, _, err := store.LoadProjectedOrder(ctx, "website", "230")
	if err != nil || stored.ProviderDeleted || stored.TotalMinor != 15000 {
		t.Fatalf("live state restored: %+v %v", stored, err)
	}
}

// TestCommerceOrderLeavesSalesMetricsUnchanged is the F-05 gate: a Woo
// order ingested through pending → completed → refunded must not move
// frozen Sale/Return reporting. Uses the real reporting query path on
// real PostgreSQL; no production Sale/Return code is touched.
func TestCommerceOrderLeavesSalesMetricsUnchanged(t *testing.T) {
	env := openDashEnv(t)
	ctx := context.Background()
	projectDashSale(t, env, "77777777-7777-4777-8777-777777777777", "2026-09-20T10:00:00Z", fixture(t, "sale_egp.json"))
	req := dashReq(t, env, "custom", "2026-09-20", "2026-09-20")
	before, err := env.rep.Summary(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if before.TransactionCount == 0 {
		t.Fatal("seeded sale must register")
	}
	snapshot := func() (int64, int64, int64) {
		t.Helper()
		sum, err := env.rep.Summary(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		var revenue, refunds int64
		for _, bucket := range sum.CurrencyTotals {
			revenue += bucket.SalesTotalMinor
			refunds += bucket.RefundTotalMinor
		}
		return sum.TransactionCount, revenue, refunds
	}
	baseCount, baseRevenue, baseRefunds := snapshot()

	store := catalogStore(env.saleEnv)
	project := func(status orders.CanonicalStatus, providerStatus string, total int64) {
		t.Helper()
		order := orderTestSnapshot("website", "970", status, providerStatus, total)
		generation, err := store.BeginOrderReconcile(ctx, "website", "970")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.ReconcileProjectedOrder(ctx, order, orders.Fingerprint(order), generation); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		status         orders.CanonicalStatus
		providerStatus string
	}{
		{orders.StatusPending, "pending"},
		{orders.StatusCompleted, "completed"},
		{orders.StatusRefunded, "refunded"},
	} {
		project(tc.status, tc.providerStatus, 68000)
		if count, revenue, refunds := snapshot(); count != baseCount || revenue != baseRevenue || refunds != baseRefunds {
			t.Fatalf("%s moved sales metrics: (%d,%d,%d) vs baseline (%d,%d,%d)",
				tc.status, count, revenue, refunds, baseCount, baseRevenue, baseRefunds)
		}
	}
}

// TestCommerceInboxOldestPending proves the operational oldest-pending
// metric reflects the earliest eligible received timestamp, not the
// newest: pending 10:00 + pending 11:00 + retry 09:00 → 09:00, while
// processed/blocked rows never count. Empty inbox keeps null/zero.
func TestCommerceInboxOldestPending(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	seed := func(delivery, topic, order, status, received string) {
		t.Helper()
		if _, err := env.pool.Exec(ctx,
			`INSERT INTO commerce_online_order_webhook_events (provider_key, delivery_id, topic, external_order_id, payload_hash, status, received_at)
			 VALUES ('website', $1, $2, $3, '\x02', $4, $5::timestamptz)`,
			delivery, topic, order, status, received); err != nil {
			t.Fatalf("seed %s: %v", delivery, err)
		}
	}
	seed("old-p", "order.created", "700", "pending", "2026-09-27T10:00:00Z")
	seed("old-q", "order.updated", "701", "pending", "2026-09-27T11:00:00Z")
	seed("old-r", "order.updated", "702", "retry", "2026-09-27T09:00:00Z")
	seed("old-ok", "order.updated", "703", "processed", "2026-09-27T08:00:00Z")
	seed("old-block", "order.created", "704", "blocked", "2026-09-27T07:00:00Z")
	stats, err := catalogStore(env).OrderInboxStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 2 || stats.Retry != 1 || stats.Blocked != 1 {
		t.Fatalf("counts: %+v", stats)
	}
	if stats.OldestPending == nil || *stats.OldestPending != "2026-09-27T09:00:00Z" {
		t.Fatalf("oldest pending: %+v", stats.OldestPending)
	}
	// Empty inbox: zero counts and null oldest.
	fresh := openSaleEnv(t)
	empty, err := catalogStore(fresh).OrderInboxStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Pending != 0 || empty.Retry != 0 || empty.Blocked != 0 || empty.OldestPending != nil {
		t.Fatalf("empty inbox: %+v", empty)
	}
}

// seedPagedOrders projects count orders with distinct creation seconds
// under provider; every third order is DELETED-tombstoned, every fifth
// unmapped, so traversal tests prove deleted/unmapped rows stay
// pageable. IDs are zero-padded for deterministic tie-breaks.
func seedPagedOrders(t *testing.T, env *saleEnv, provider string, base time.Time, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		external := "04" + strings.Repeat("0", 2) + strconv.Itoa(100+i)
		snapshot := orderTestSnapshot(provider, external, orders.StatusProcessing, "processing", int64(1000+i))
		snapshot.CreatedAt = base.Add(time.Duration(i) * time.Second)
		snapshot.ModifiedAt = snapshot.CreatedAt.Add(time.Hour)
		if i%5 == 4 {
			snapshot.Lines[0].ExternalProductID = "foreign"
		}
		if _, changed := reconcileSnapshot(t, env, snapshot); !changed {
			t.Fatalf("seed %s changed", external)
		}
		if i%3 == 2 {
			store := catalogStore(env)
			generation, err := store.BeginOrderReconcile(context.Background(), provider, external)
			if err != nil {
				t.Fatal(err)
			}
			live, _, _, err := store.LoadProjectedOrder(context.Background(), provider, external)
			if err != nil {
				t.Fatal(err)
			}
			live.ProviderDeleted = true
			live.Canonical = orders.StatusDeleted
			if outcome, err := store.ReconcileProjectedOrder(context.Background(), live, orders.Fingerprint(live), generation); err != nil || !outcome.Changed {
				t.Fatalf("tombstone %s: %+v %v", external, outcome, err)
			}
		}
	}
}

// collectPageIDs walks ListOrderPage to exhaustion, asserting stable
// keyset traversal: every row exactly once, in deterministic order.
func collectPageIDs(t *testing.T, env *saleEnv, provider, status string, limit int) []string {
	t.Helper()
	ctx := context.Background()
	store := catalogStore(env)
	var ids []string
	seen := map[string]bool{}
	var cursor *orders.OrderCursor
	for {
		page, err := store.ListOrderPage(ctx, provider, status, limit, cursor)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) > limit {
			t.Fatalf("page exceeds limit: %d", len(page.Items))
		}
		for _, item := range page.Items {
			id := item.ProviderKey + "/" + item.ExternalOrderID
			if seen[id] {
				t.Fatalf("duplicate row %s", id)
			}
			seen[id] = true
			ids = append(ids, id)
		}
		if page.Next == nil {
			break
		}
		cursor = page.Next
	}
	return ids
}

// TestCommerceOrderPageTraversal proves full keyset traversal: 25
// orders at limit 10 visit every durable row (live, deleted, unmapped)
// exactly once across pages.
func TestCommerceOrderPageTraversal(t *testing.T) {
	env := openSaleEnv(t)
	base := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	seedPagedOrders(t, env, "website", base, 25)
	ids := collectPageIDs(t, env, "website", "", 10)
	if len(ids) != 25 {
		t.Fatalf("traversal: %d rows", len(ids))
	}
}

// TestCommerceOrderPageEqualTimestamps proves identical created_at
// values paginate without duplicates or gaps via (provider, order)
// tie-breaks.
func TestCommerceOrderPageEqualTimestamps(t *testing.T) {
	env := openSaleEnv(t)
	base := time.Date(2026, 9, 27, 13, 0, 0, 0, time.UTC)
	for i := 0; i < 7; i++ {
		external := "05" + strings.Repeat("0", 2) + strconv.Itoa(100+i)
		snapshot := orderTestSnapshot("website", external, orders.StatusPending, "pending", 1000)
		snapshot.CreatedAt = base
		snapshot.ModifiedAt = base.Add(time.Hour)
		if _, changed := reconcileSnapshot(t, env, snapshot); !changed {
			t.Fatalf("seed %s changed", external)
		}
	}
	ids := collectPageIDs(t, env, "website", "", 3)
	if len(ids) != 7 {
		t.Fatalf("equal-timestamp traversal: %d rows", len(ids))
	}
	for i := 1; i < len(ids); i++ {
		if ids[i-1] >= ids[i] {
			t.Fatalf("unstable order: %v", ids)
		}
	}
}

// TestCommerceOrderPageFilters proves provider and status filters
// traverse exactly their matching rows.
func TestCommerceOrderPageFilters(t *testing.T) {
	env := openSaleEnv(t)
	base := time.Date(2026, 9, 27, 14, 0, 0, 0, time.UTC)
	seedPagedOrders(t, env, "website", base, 12)
	seedPagedOrders(t, env, "shop2", base, 6)
	website := collectPageIDs(t, env, "website", "", 5)
	if len(website) != 12 {
		t.Fatalf("provider filter: %d rows", len(website))
	}
	for _, id := range website {
		if !strings.HasPrefix(id, "website/") {
			t.Fatalf("cross-provider leak: %s", id)
		}
	}
	// Status filter: website seeds alternate live PROCESSING and
	// DELETED tombstones (every third). PROCESSING count = 8 of 12.
	processing := collectPageIDs(t, env, "website", "PROCESSING", 5)
	if len(processing) != 8 {
		t.Fatalf("status filter: %d rows", len(processing))
	}
	store := catalogStore(env)
	page, err := store.ListOrderPage(context.Background(), "website", "PROCESSING", 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range page.Items {
		if item.CanonicalStatus != "PROCESSING" {
			t.Fatalf("status leak: %+v", item)
		}
	}
}

// TestCommerceOrderPageConcurrentInsert proves keyset current-state
// semantics: rows inserted after page 1 never duplicate in later pages.
func TestCommerceOrderPageConcurrentInsert(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	base := time.Date(2026, 9, 27, 15, 0, 0, 0, time.UTC)
	seedPagedOrders(t, env, "website", base, 8)
	first, err := store.ListOrderPage(ctx, "website", "", 5, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 5 || first.Next == nil {
		t.Fatalf("page1: %+v", first)
	}
	newer := orderTestSnapshot("website", "04999", orders.StatusPending, "pending", 1000)
	newer.CreatedAt = base.Add(24 * time.Hour)
	newer.ModifiedAt = newer.CreatedAt.Add(time.Hour)
	if _, changed := reconcileSnapshot(t, env, newer); !changed {
		t.Fatal("newer seed changed")
	}
	older := orderTestSnapshot("website", "04000", orders.StatusPending, "pending", 1000)
	older.CreatedAt = base.Add(-24 * time.Hour)
	older.ModifiedAt = older.CreatedAt.Add(-23 * time.Hour)
	if _, changed := reconcileSnapshot(t, env, older); !changed {
		t.Fatal("older seed changed")
	}
	second, err := store.ListOrderPage(ctx, "website", "", 5, first.Next)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, item := range first.Items {
		seen[item.ProviderKey+"/"+item.ExternalOrderID] = true
	}
	for _, item := range second.Items {
		id := item.ProviderKey + "/" + item.ExternalOrderID
		if seen[id] {
			t.Fatalf("duplicate after insert: %s", id)
		}
	}
}
