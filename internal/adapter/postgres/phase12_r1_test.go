package postgres

import (
	"context"
	"fmt"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
	"time"
)

func reviewReq(t *testing.T, d Devices, storeID string) report.Request {
	t.Helper()
	r, e := report.NewService(d, clock.System{}, time.UTC).ParseRequest("custom", "2026-09-20", "2026-09-30", "")
	if e != nil {
		t.Fatal(e)
	}
	r.Store, e = report.ParseStoreScope(storeID)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestPhase12R1OnlineCurrencyScopes(t *testing.T) {
	f, productA, _ := scopeOrderFixture(t)
	ctx := context.Background()
	d := NewDevices(f.pool, 5*time.Second)
	s := dashboard.NewService(report.NewService(d, clock.System{}, time.UTC), d, nil, clock.System{})
	for i, c := range []struct {
		external, currency, storeline string
		amount                        int64
		status                        orders.CanonicalStatus
	}{{"900", "EGP", "700", 9007199254740993, orders.StatusProcessing}, {"901", "USD", "700", 12345, orders.StatusCancelled}, {"902", "EGP", "701", 54321, orders.StatusProcessing}, {"903", "EGP", "missing", 222, orders.StatusUnknown}} {
		snap := scopeSnapshot("website", c.external, c.external, []orders.OrderLine{orderLine(c.storeline, int64(i+1), c.amount)})
		snap.Currency = c.currency
		snap.TotalMinor = c.amount
		snap.Canonical = c.status
		mustReconcile(t, f, snap)
	}
	createMapping(t, f, "other", productA, "703")
	other := scopeSnapshot("other", "904", "other", []orders.OrderLine{orderLine("703", 1, 99)})
	other.TotalMinor = 99
	mustReconcile(t, f, other)
	outside := scopeSnapshot("other", "905", "outside", []orders.OrderLine{orderLine("703", 1, 777)})
	outside.TotalMinor = 777
	outside.CreatedAt = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	outside.ModifiedAt = outside.CreatedAt
	mustReconcile(t, f, outside)
	req := reviewReq(t, d, scopeStoreA)
	out, e := s.OrderAnalytics(ctx, req, "website")
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("Store A %+v", out)
	if len(out.CurrencyTotals) != 2 || out.CurrencyTotals[0].ValueMinor != "9007199254740993" {
		t.Fatal("exact separation", out)
	}
	req.Currency = "USD"
	native, e := s.OrderAnalytics(ctx, req, "website")
	if e != nil {
		t.Fatal(e)
	}
	if len(native.CurrencyTotals) != 1 || native.CurrencyTotals[0].Currency != "USD" || native.CurrencyTotals[0].ValueMinor != "12345" || len(native.StatusCounts) != 1 || native.StatusCounts[0].CanonicalStatus != "CANCELLED" {
		t.Fatalf("USD scope not applied %+v", native)
	}
	unknown, e := s.OrderAnalytics(ctx, req, "unknown")
	if e != nil || len(unknown.CurrencyTotals) != 0 {
		t.Fatal("unknown provider widened", e)
	}
	global, e := s.OrderAnalytics(ctx, reviewReq(t, d, ""), "")
	if e != nil {
		t.Fatal(e)
	}
	if len(global.CurrencyTotals) != 2 || global.CurrencyTotals[0].Orders != 4 || global.CurrencyTotals[1].Orders != 1 {
		t.Fatalf("global/legacy/period counts %+v", global)
	}
	for _, tc := range []struct {
		store, provider, currency, value string
		orders                           int64
	}{
		{scopeStoreA, "other", "EGP", "99", 1}, {scopeStoreA, "other", "USD", "", 0},
		{scopeStoreB, "website", "EGP", "54321", 1}, {scopeStoreB, "other", "EGP", "", 0},
		{"99999999-9999-4999-8999-999999999999", "", "EGP", "", 0},
	} {
		r := reviewReq(t, d, tc.store)
		r.Currency = tc.currency
		a, err := s.OrderAnalytics(ctx, r, tc.provider)
		if err != nil {
			t.Fatal(err)
		}
		if tc.orders == 0 {
			if len(a.CurrencyTotals) != 0 || len(a.StatusCounts) != 0 || len(a.ProviderTotals) != 0 {
				t.Fatal("empty scope widened", tc, a)
			}
		} else if len(a.CurrencyTotals) != 1 || a.CurrencyTotals[0].Orders != tc.orders || a.CurrencyTotals[0].ValueMinor != tc.value {
			t.Fatal("combined scope", tc, a)
		}
	}
	t.Logf("All+legacy %+v; independent/combined Store/provider/currency/period filters PASS", global)
}
func TestPhase12R1OnlineOverflow(t *testing.T) {
	f, _, _ := scopeOrderFixture(t)
	ctx := context.Background()
	d := NewDevices(f.pool, 5*time.Second)
	for i, st := range []orders.CanonicalStatus{orders.StatusProcessing, orders.StatusCompleted} {
		snap := scopeSnapshot("website", fmt.Sprint(950+i), "overflow", []orders.OrderLine{orderLine("700", int64(i+1), 5000000000000000000)})
		snap.TotalMinor = 5000000000000000000
		snap.Canonical = st
		mustReconcile(t, f, snap)
	}
	s := dashboard.NewService(report.NewService(d, clock.System{}, time.UTC), d, nil, clock.System{})
	out, e := s.OrderAnalytics(ctx, reviewReq(t, d, scopeStoreA), "")
	t.Logf("two valid 5e18 buckets => %+v error=%v", out, e)
	if e == nil {
		t.Errorf("sum beyond int64 returned successful wrapped money: %+v", out.CurrencyTotals)
	}
}
func TestPhase12R1TagTiePlans(t *testing.T) {
	env := openSaleEnv(t)
	tagA := tagFixture("ffffffff-ffff-4fff-8fff-ffffffffffff", "tie-a", "متعادل", "Same")
	tagB := tagFixture("00000000-0000-4000-8000-000000000001", "tie-b", "متعادل", "Same")
	projectSaleV2(t, env, "11111111-1111-4111-8111-111111111111", "2026-09-21T10:00:00Z", []map[string]any{tagA, tagB})
	ctx := context.Background()
	cfg := env.pool.Config().Copy()
	cfg.MaxConns = 1
	p, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	d := NewDevices(p, 5*time.Second)
	s := report.NewService(d, clock.System{}, time.UTC)
	r := reviewReq(t, d, "")
	r.Currency = "EGP"
	r.Limit = 1
	first := ""
	for _, hash := range []string{"on", "off"} {
		if _, e = p.Exec(ctx, "SET enable_hashagg="+hash); e != nil {
			t.Fatal(e)
		}
		if hash == "on" {
			p.Exec(ctx, "SET enable_sort=off")
		} else {
			p.Exec(ctx, "SET enable_sort=on")
		}
		out, e := s.Breakdown(ctx, r, report.DimensionTag)
		if e != nil {
			t.Fatal(e)
		}
		id := *out.Rows[0].TagID
		t.Logf("hashagg %s => top Tag %s", hash, id)
		if first != "" && first != id {
			t.Errorf("same canonical facts choose different Top-1 under eligible plans: %s / %s", first, id)
		}
		first = id
	}
}

func TestPhase12R1HealthReadOnly(t *testing.T) {
	f, pA, pB := scopeOrderFixture(t)
	ctx := context.Background()
	d := NewDevices(f.pool, 5*time.Second)
	for i, p := range []string{pA, pB} {
		id := uuidEvent(t, 0xface, 10+i)
		dev, cred := f.devA, f.credA
		if i == 1 {
			dev, cred = f.devB, f.credB
		}
		f.ingest(t, dev, cred, id, catalog.EventProductSalesPolicySnapshotV1, policyPayload(p, 1, true, true, nil))
		projectCatalogTerminal(t, f, id, catalog.EventProductSalesPolicySnapshotV1)
	}
	inv := uuidEvent(t, 0xface, 20)
	f.ingest(t, f.devA, f.credA, inv, catalog.EventInventoryProductSnapshotV1, inventoryPayload(pA, 1, 0))
	projectCatalogTerminal(t, f, inv, catalog.EventInventoryProductSnapshotV1)
	createMapping(t, f, "other", pB, "702")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := f.pool.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(`UPDATE catalog_categories SET status='hidden' WHERE category_id=(SELECT top_category_id FROM catalog_products WHERE product_id=$1)`, pA)
	exec(`UPDATE commerce_product_mappings SET store_id=$2 WHERE provider_key='website' AND product_id=$1`, pA, scopeStoreB)
	exec(`INSERT INTO commerce_product_mutation_barriers(operation_id,provider_key,product_id,request_fingerprint,state) VALUES('11111111-0000-7000-8000-000000000001','website',$1,repeat('a',64),'uncertain')`, pA)
	cfg := f.pool.Config().Copy()
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	ro, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer ro.Close()
	readonly := NewDevices(ro, 5*time.Second)
	s := dashboard.NewService(report.NewService(readonly, clock.System{}, time.UTC), readonly, nil, clock.System{})
	req := reviewReq(t, d, scopeStoreA)
	before := r4Payloads(t, f)
	h, e := s.CatalogHealth(ctx, req, "", "", 1)
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("health %+v", h)
	counts := map[string]int64{}
	for _, c := range h.Counts {
		counts[c.ReasonCode] = c.Products
	}
	for code, w := range map[string]int64{"CATALOG_MISSING_SKU": 0, "CATALOG_MISSING_CATEGORY": 0, "AVAILABILITY_NOT_READY": 0, "COMMERCE_MAPPING_MISSING": 1, "COMMERCE_SYNC_AMBIGUOUS": 1, "COMMERCE_STORE_CONFLICT": 1} {
		if counts[code] != w {
			t.Errorf("%s=%d want=%d", code, counts[code], w)
		}
	}
	if !h.DetailTruncated || len(h.Detail) != 1 {
		t.Errorf("bounds %+v", h)
	}
	for _, provider := range []string{"website", "other", "unknown"} {
		x, e := s.CatalogHealth(ctx, req, provider, "", 100)
		if e != nil {
			t.Fatal(e)
		}
		for _, count := range x.Counts {
			// Provider narrowing applies to provider-scoped reasons
			// only (the codes computed against the durable provider
			// universe). Provider-independent lifecycle reasons (the
			// original CATALOG_MISSING_SKU/AVAILABILITY_NOT_READY family
			// and, since Phase 17, the product/variant variant-health
			// family) intentionally survive provider narrowing — they are
			// not provider concepts. The Phase 12 all-zero assertion held
			// only because this fixture produced zero for every
			// provider-independent code; it is now scoped to the codes
			// the narrowing contract actually covers.
			providerScoped := count.ReasonCode == "COMMERCE_MAPPING_MISSING" ||
				count.ReasonCode == "COMMERCE_SYNC_AMBIGUOUS" ||
				count.ReasonCode == "COMMERCE_STORE_CONFLICT" ||
				count.ReasonCode == "VARIANT_MAPPING_MISSING"
			if provider == "unknown" && providerScoped && count.Products != 0 {
				t.Fatalf("unknown provider widened: %+v", x)
			}
			if provider == "other" && (count.ReasonCode == "COMMERCE_SYNC_AMBIGUOUS" || count.ReasonCode == "COMMERCE_STORE_CONFLICT") && count.Products != 0 {
				t.Fatalf("independent provider reasons: %+v", x)
			}
		}
		t.Logf("provider %s %+v", provider, x)
	}
	unknown := reviewReq(t, d, "cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	x, e := s.CatalogHealth(ctx, unknown, "", "", 100)
	if e != nil || len(x.Detail) != 0 {
		t.Fatalf("unknown Store widened %+v %v", x, e)
	}
	if before != r4Payloads(t, f) {
		t.Fatal("payload state changed")
	}
	var conflictStore, barrier string
	if e = f.pool.QueryRow(ctx, `SELECT store_id::text FROM commerce_product_mappings WHERE provider_key='website' AND product_id=$1`, pA).Scan(&conflictStore); e != nil || conflictStore != scopeStoreB {
		t.Fatal("adoption occurred", e)
	}
	if e = f.pool.QueryRow(ctx, `SELECT state FROM commerce_product_mutation_barriers WHERE product_id=$1`, pA).Scan(&barrier); e != nil || barrier != "uncertain" {
		t.Fatal("barrier mutated", e)
	}
	exec(`UPDATE commerce_product_mutation_barriers SET state='resolved',resolved_at=now(),resolution='remote_not_applied' WHERE product_id=$1`, pA)
	exec(`UPDATE commerce_product_mappings SET store_id=NULL WHERE provider_key='website' AND product_id=$1`, pA)
	resolved, err := s.CatalogHealth(ctx, req, "", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range resolved.Counts {
		if (c.ReasonCode == "COMMERCE_SYNC_AMBIGUOUS" || c.ReasonCode == "COMMERCE_STORE_CONFLICT") && c.Products != 0 {
			t.Fatalf("resolved barrier or NULL owner false positive %+v", c)
		}
	}
	t.Log("resolved barrier excluded and NULL mapping owner not a Store conflict; read-only session PASS")
	exec(`ALTER TABLE catalog_products ALTER COLUMN top_category_id DROP NOT NULL`)
	exec(`UPDATE catalog_products SET sku=' ', top_category_id=NULL WHERE product_id=$1`, pA)
	exec(`DELETE FROM catalog_product_inventory WHERE product_id=$1`, pA)
	positive, e := s.CatalogHealth(ctx, req, "", "", 100)
	if e != nil {
		t.Fatal(e)
	}
	pc := map[string]int64{}
	for _, c := range positive.Counts {
		pc[c.ReasonCode] = c.Products
	}
	for _, code := range []string{"CATALOG_MISSING_SKU", "CATALOG_MISSING_CATEGORY", "AVAILABILITY_NOT_READY"} {
		if pc[code] != 1 {
			t.Errorf("positive %s=%d", code, pc[code])
		}
	}
	exec(`UPDATE catalog_products SET is_active=false WHERE product_id=$1`, pA)
	inactive, e := s.CatalogHealth(ctx, req, "", "", 100)
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range inactive.Counts {
		if (c.ReasonCode == "COMMERCE_MAPPING_MISSING" || c.ReasonCode == "AVAILABILITY_NOT_READY") && c.Products != 0 {
			t.Errorf("inactive false positive %+v", c)
		}
	}
	exec(`UPDATE catalog_products SET is_active=true WHERE product_id=$1`, pA)
	exec(`UPDATE catalog_product_sales_policies SET sell_online=false WHERE product_id=$1`, pA)
	offline, e := s.CatalogHealth(ctx, req, "", "", 100)
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range offline.Counts {
		if (c.ReasonCode == "COMMERCE_MAPPING_MISSING" || c.ReasonCode == "AVAILABILITY_NOT_READY") && c.Products != 0 {
			t.Errorf("offline false positive %+v", c)
		}
	}
	t.Log("positive missing SKU/category/inventory and inactive/offline exclusion controls PASS")
	t.Log("read-only PostgreSQL sessions accepted all health reads; hidden root and inventory=0 negative controls PASS; no adoption/barrier mutation")
}

func TestPhase12R1ProviderChoicesBeyondDetailPage(t *testing.T) {
	f, pA, pB := scopeOrderFixture(t)
	ctx := context.Background()
	d := NewDevices(f.pool, 5*time.Second)
	createMapping(t, f, "other", pB, "702")
	var root string
	if err := f.pool.QueryRow(ctx, "SELECT top_category_id::text FROM catalog_products WHERE product_id=$1", pA).Scan(&root); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 51; i++ {
		product := fmt.Sprintf("aa120000-0000-4000-8000-%012d", i)
		for j, entry := range []struct{ typ, payload string }{
			{catalog.EventProductSnapshotV1, productPayload(product, fmt.Sprintf("P12-%d", i), "ready", root, nil, nil, 1)},
			{catalog.EventProductSalesPolicySnapshotV1, policyPayload(product, 1, true, true, nil)},
			{catalog.EventInventoryProductSnapshotV1, inventoryPayload(product, 1, 0)},
		} {
			id := uuidEvent(t, 0xab12, 1000+i*3+j)
			f.ingest(t, f.devA, f.credA, id, entry.typ, entry.payload)
			requireOutcome(t, projectCatalogTerminal(t, f, id, entry.typ), catalog.OutcomeProcessed, "")
		}
	}
	cfg := f.pool.Config().Copy()
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	ro, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	readonly := NewDevices(ro, 5*time.Second)
	svc := dashboard.NewService(report.NewService(readonly, clock.System{}, time.UTC), readonly, nil, clock.System{})
	out, err := svc.CatalogHealth(ctx, reviewReq(t, d, scopeStoreA), "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Detail) != 50 || !out.DetailTruncated {
		t.Fatal("bounded detail", out)
	}
	seenWebsite := false
	for _, r := range out.Detail {
		seenWebsite = seenWebsite || r.ProviderKey == "website"
	}
	if seenWebsite {
		t.Fatal("fixture did not omit later provider")
	}
	if len(out.Providers) != 2 || out.Providers[0] != "other" || out.Providers[1] != "website" {
		t.Fatal("provider choices incomplete", out.Providers)
	}
	var count int64
	for _, c := range out.Counts {
		if c.ReasonCode == "COMMERCE_MAPPING_MISSING" {
			count = c.Products
		}
	}
	if count != 102 {
		t.Fatalf("complete counts=%d want=102", count)
	}
	narrowed, err := svc.CatalogHealth(ctx, reviewReq(t, d, scopeStoreA), "website", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range narrowed.Detail {
		if r.ProviderKey != "website" {
			t.Fatal("provider scope widened", r)
		}
	}
	unknown, err := svc.CatalogHealth(ctx, reviewReq(t, d, "cccccccc-cccc-4ccc-8ccc-cccccccccccc"), "", "", 50)
	if err != nil || len(unknown.Providers) != 0 || len(unknown.Detail) != 0 {
		t.Fatal("unknown Store widened", unknown, err)
	}
	t.Log("51 ready products, 102 complete mapping reasons, first 50 omit website, complete provider picker still offers website; read-only sessions PASS")
}

// The response cap bounds output, not canonical aggregation work.
func TestPhase12R1TopNRepresentativePerformance(t *testing.T) {
	env := openSaleEnv(t)
	tags := make([]map[string]any, 20)
	for i := range tags {
		tags[i] = tagFixture(fmt.Sprintf("10000000-0000-4000-8000-%012d", i+1), fmt.Sprintf("tag-%02d", i), "وسم", fmt.Sprintf("Tag %02d", i))
	}
	for i := 0; i < 300; i++ {
		projectSaleV2(t, env, fmt.Sprintf("20000000-0000-4000-8000-%012d", i+1), "2026-09-21T10:00:00Z", tags)
	}
	ctx := context.Background()
	d := NewDevices(env.pool, 5*time.Second)
	s := report.NewService(d, clock.System{}, time.UTC)
	r := reviewReq(t, d, "")
	r.Currency = "EGP"
	for _, limit := range []int{1, 10, 100} {
		r.Limit = limit
		start := time.Now()
		out, err := s.Breakdown(ctx, r, report.DimensionTag)
		if err != nil {
			t.Fatal(err)
		}
		want := limit
		if want > 20 {
			want = 20
		}
		if len(out.Rows) != want {
			t.Fatalf("limit %d rows %d", limit, len(out.Rows))
		}
		if out.Rows[0].Units != 600 {
			t.Fatal("aggregation incomplete", out.Rows[0])
		}
		t.Logf("6000 historical memberships, 300 Sales, limit=%d rows=%d duration=%s", limit, len(out.Rows), time.Since(start))
	}
	rows, err := env.pool.Query(ctx, `EXPLAIN (ANALYZE, BUFFERS) SELECT t.tag_id,t.slug,t.name_ar,t.name_en,l.line_currency,SUM(l.quantity),SUM(l.line_total_minor) FROM sale_item_tag_snapshots t JOIN sale_lines_projection l ON l.sale_id=t.sale_id AND l.sale_item_id=t.sale_item_id JOIN sales_projection s ON s.sale_id=t.sale_id WHERE s.occurred_at >= '2026-09-20' AND s.occurred_at < '2026-10-01' GROUP BY t.tag_id,t.slug,t.name_ar,t.name_en,l.line_currency`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var line string
		if err = rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		t.Log(line)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	indexes, err := env.pool.Query(ctx, `SELECT tablename,indexname,indexdef FROM pg_indexes WHERE schemaname=current_schema() AND tablename IN ('sales_projection','sale_lines_projection','sale_item_tag_snapshots') ORDER BY tablename,indexname`)
	if err != nil {
		t.Fatal(err)
	}
	defer indexes.Close()
	n := 0
	for indexes.Next() {
		var table, name, def string
		if err = indexes.Scan(&table, &name, &def); err != nil {
			t.Fatal(err)
		}
		t.Logf("index %s/%s: %s", table, name, def)
		n++
	}
	if n == 0 {
		t.Fatal("expected existing report indexes")
	}
}
