package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Barriers observe the real workers at repository/provider boundaries. Timeout
// guards report deadlocks; they do not determine interleaving or lease expiry.
type r1Queue struct {
	Devices
	done chan struct{}
}

func (q *r1Queue) RetryProductReevaluation(ctx context.Context, c commerce.ProductReevaluation, next time.Time, code string) (bool, error) {
	ok, err := q.Devices.RetryProductReevaluation(ctx, c, next, code)
	if c.ProductID == prodP1 {
		q.done <- struct{}{}
	}
	return ok, err
}
func (q *r1Queue) CompleteProductReevaluation(ctx context.Context, c commerce.ProductReevaluation) (bool, error) {
	ok, err := q.Devices.CompleteProductReevaluation(ctx, c)
	if c.ProductID == prodP1 {
		q.done <- struct{}{}
	}
	return ok, err
}

type r1Provider struct {
	key       commerce.ProviderKey
	entered   chan commerce.ProductUpsertRequest
	release   chan struct{}
	published bool
	quantity  int64
}

func (p *r1Provider) Key() commerce.ProviderKey {
	if p.key != "" {
		return p.key
	}
	return "review"
}
func (p *r1Provider) UpsertProduct(ctx context.Context, r commerce.ProductUpsertRequest) (commerce.ProductUpsertResult, error) {
	p.entered <- r
	select {
	case <-p.release:
	case <-ctx.Done():
		return commerce.ProductUpsertResult{}, ctx.Err()
	}
	p.published = r.Published
	return commerce.ProductUpsertResult{ExternalProductID: "review-remote"}, nil
}
func (p *r1Provider) SetInventory(_ context.Context, r commerce.InventoryUpdateRequest) error {
	p.quantity = r.AvailableQuantity
	return nil
}
func r1Wait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("worker barrier timed out")
	}
}
func r1ReadyProduct(t *testing.T, pf *policyFixture) {
	t.Helper()
	for i, typ := range []string{catalog.EventProductSalesPolicySnapshotV1, catalog.EventInventoryProductSnapshotV1} {
		event := fmt.Sprintf("98989999-0000-4000-8000-%012d", i+1)
		body := policyPayload(prodP1, 1, true, true, nil)
		if i == 1 {
			body = inventoryPayload(prodP1, 1, 10)
		}
		pf.ingest(t, pf.devA, pf.credA, event, typ, body)
		projectCatalogTerminal(t, pf.scopeFixture, event, typ)
	}
}
func TestR1ToggleDuringProvider(t *testing.T) {
	for _, initial := range []bool{true, false} {
		for _, rapid := range []bool{false, true} {
			t.Run(fmt.Sprintf("initial-%t-rapid-%t", initial, rapid), func(t *testing.T) {
				pf := buildPolicyFixture(t)
				ctx := context.Background()
				d := NewDevices(pf.pool, 5*time.Second)
				r1ReadyProduct(t, pf)
				drainReevaluations(t, d)
				if !initial {
					pf.toggle(t, catIDA, false, 2)
					drainReevaluations(t, d)
				}
				if _, err := d.CreateProductMapping(ctx, "review", prodP1, "review-remote"); err != nil {
					t.Fatal(err)
				}
				if err := d.EnqueueProductReevaluation(ctx, prodP1, nil, "test"); err != nil {
					t.Fatal(err)
				}
				q := &r1Queue{Devices: d, done: make(chan struct{}, 8)}
				p := &r1Provider{entered: make(chan commerce.ProductUpsertRequest, 8), release: make(chan struct{})}
				reg := commerce.NewRegistry()
				if err := reg.Register(p.Key(), p); err != nil {
					t.Fatal(err)
				}
				logger := slog.New(slog.NewTextHandler(io.Discard, nil))
				svc := commerce.NewCommerceService(reg, d, commerce.NewCatalogCommerceSource(catalog.NewService(d)), logger)
				run := func() (context.CancelFunc, chan struct{}) {
					c, cancel := context.WithCancel(ctx)
					stopped := make(chan struct{})
					go func() { commerce.NewReevaluationWorker(q, svc, reg, time.Now, logger).Run(c); close(stopped) }()
					return cancel, stopped
				}
				cancel, stopped := run()
				select {
				case r := <-p.entered:
					if r.Published != initial {
						t.Fatal("wrong initial desired")
					}
				case <-time.After(10 * time.Second):
					cancel()
					t.Fatal("no provider request")
				}
				pf.toggle(t, catIDA, !initial, 3)
				want := !initial
				if rapid {
					pf.toggle(t, catIDA, initial, 4)
					want = initial
				}
				close(p.release)
				r1Wait(t, q.done)
				cancel()
				r1Wait(t, stopped)
				var generation int64
				if err := pf.pool.QueryRow(ctx, "SELECT requested_generation FROM commerce_product_reevaluations WHERE product_id=$1", prodP1).Scan(&generation); err != nil {
					t.Fatalf("newer intent lost: %v", err)
				}
				// A fresh worker (restart) must consume the surviving latest intent.
				cancel, stopped = run()
				r1Wait(t, q.done)
				cancel()
				r1Wait(t, stopped)
				wantQty := int64(0)
				if want {
					wantQty = 10
				}
				if p.published != want || p.quantity != wantQty {
					t.Fatalf("remote %t/%d wanted %t/%d", p.published, p.quantity, want, wantQty)
				}
				var pending int
				if err := pf.pool.QueryRow(ctx, "SELECT count(*) FROM commerce_product_reevaluations WHERE product_id=$1", prodP1).Scan(&pending); err != nil || pending != 0 {
					t.Fatalf("target queue not converged: %d %v", pending, err)
				}
				mapping, err := d.GetProductMapping(ctx, "review", prodP1)
				if err != nil || mapping.ExternalProductID != "review-remote" {
					t.Fatalf("mapping changed: %+v %v", mapping, err)
				}
				t.Logf("generation %d survives older send; restart converges publication=%t quantity=%d, identity retained", generation, want, wantQty)
			})
		}
	}
}
func TestR1ClaimFencing(t *testing.T) {
	pf := buildPolicyFixture(t)
	ctx := context.Background()
	d := NewDevices(pf.pool, 5*time.Second)
	drainReevaluations(t, d)
	enqueue := func() {
		t.Helper()
		if err := d.EnqueueProductReevaluation(ctx, prodP1, nil, "test"); err != nil {
			t.Fatal(err)
		}
	}
	claim := func() commerce.ProductReevaluation {
		t.Helper()
		rows, err := d.ClaimProductReevaluations(ctx, 1, time.Now().Add(time.Hour))
		if err != nil || len(rows) != 1 {
			t.Fatalf("claim %v %v", rows, err)
		}
		return rows[0]
	}
	expire := func() {
		t.Helper()
		tag, err := pf.pool.Exec(ctx, "UPDATE commerce_product_reevaluations SET lease_until=now()-interval '1 second' WHERE product_id=$1", prodP1)
		if err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("expire %v %v", tag, err)
		}
	}
	rejected := func(c commerce.ProductReevaluation) {
		t.Helper()
		if ok, err := d.CompleteProductReevaluation(ctx, c); err != nil || ok {
			t.Fatalf("stale complete %t %v", ok, err)
		}
		if ok, err := d.RetryProductReevaluation(ctx, c, time.Now().Add(time.Hour), "stale"); err != nil || ok {
			t.Fatalf("stale retry %t %v", ok, err)
		}
	}
	enqueue()
	first := claim()
	for i := 0; i < 3; i++ {
		enqueue()
	}
	if rows, err := d.ClaimProductReevaluations(ctx, 25, time.Now().Add(time.Hour)); err != nil || len(rows) != 0 {
		t.Fatalf("enqueue stole active lease %v %v", rows, err)
	}
	expire()
	rejected(first)
	second := claim()
	if second.LeaseGeneration != first.LeaseGeneration+1 || second.ClaimedGeneration != first.ClaimedGeneration+3 {
		t.Fatalf("lost generation %+v %+v", first, second)
	}
	rejected(first)
	enqueue()
	if ok, err := d.RetryProductReevaluation(ctx, second, time.Now().Add(time.Hour), "old-failure"); err != nil || !ok {
		t.Fatalf("retry %t %v", ok, err)
	}
	third := claim()
	if third.ClaimedGeneration != second.ClaimedGeneration+1 || third.Attempts != 0 {
		t.Fatalf("old retry overwrote newer work %+v", third)
	}
	if ok, err := d.CompleteProductReevaluation(ctx, third); err != nil || !ok {
		t.Fatalf("complete %t %v", ok, err)
	}
	// Delete/reinsert ABA: old generation=1 can never acknowledge new row gen=1.
	enqueue()
	fourth := claim()
	rejected(first)
	if ok, err := d.CompleteProductReevaluation(ctx, fourth); err != nil || !ok {
		t.Fatal(err)
	}
	enqueue()
	start := make(chan struct{})
	results := make(chan []commerce.ProductReevaluation, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			rows, err := d.ClaimProductReevaluations(ctx, 1, time.Now().Add(time.Hour))
			results <- rows
			errs <- err
		}()
	}
	close(start)
	a, b := <-results, <-results
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if len(a)+len(b) != 1 {
		t.Fatalf("two owners %v %v", a, b)
	}
	t.Log("live enqueue, coalescing, expiry, disappearance/reclaim, stale complete/retry, newer scheduling, ABA and two claimers fenced")
}

type r1CatalogScan struct {
	Devices
	empty chan struct{}
	once  sync.Once
}

func (s *r1CatalogScan) PendingCatalogEvents(ctx context.Context, processor, typ string, limit int) ([]string, error) {
	rows, err := s.Devices.PendingCatalogEvents(ctx, processor, typ, limit)
	if err == nil && len(rows) == 0 {
		s.once.Do(func() { close(s.empty) })
	}
	return rows, err
}
func r1RunCategory(t *testing.T, d Devices) {
	t.Helper()
	s := &r1CatalogScan{Devices: d, empty: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan struct{})
	go func() {
		catalog.NewCategoryProjector(s, clock.System{}, slog.New(slog.NewTextHandler(io.Discard, nil))).Run(ctx)
		close(stopped)
	}()
	r1Wait(t, s.empty)
	cancel()
	r1Wait(t, stopped)
}
func TestR1CategoryProductionDiscovery(t *testing.T) {
	for _, scenario := range []string{"v1-then-v2", "v2-then-stale-v1", "equal-contradiction", "duplicate-v2", "bootstrap"} {
		t.Run(scenario, func(t *testing.T) {
			f := openScopeFixture(t)
			ctx := context.Background()
			d := NewDevices(f.pool, 5*time.Second)
			old, current := "98987777-0000-4000-8000-000000000001", "98987777-0000-4000-8000-000000000002"
			send := func(id, typ string, rev int64, enabled bool) {
				t.Helper()
				body := categoryV2Payload(catIDA, "active", map[string]string{"en": "A", "ar": "أ"}, nil, rev, enabled)
				if typ == catalog.EventCategorySnapshotV1 {
					body = categoryPayload(catIDA, "active", map[string]string{"en": "A", "ar": "أ"}, nil, rev)
				}
				f.ingest(t, f.devA, f.credA, id, typ, body)
				r1RunCategory(t, d)
			}
			switch scenario {
			case "v1-then-v2":
				send(old, catalog.EventCategorySnapshotV1, 1, true)
				send(current, catalog.EventCategorySnapshotV2, 2, false)
			case "v2-then-stale-v1":
				send(current, catalog.EventCategorySnapshotV2, 2, false)
				send(old, catalog.EventCategorySnapshotV1, 1, true)
			case "equal-contradiction":
				send(old, catalog.EventCategorySnapshotV1, 2, true)
				send(current, catalog.EventCategorySnapshotV2, 2, false)
			case "duplicate-v2":
				send(old, catalog.EventCategorySnapshotV2, 2, false)
				send(current, catalog.EventCategorySnapshotV2, 2, false)
			case "bootstrap":
				f.ingest(t, f.devA, f.credA, old, catalog.EventCategorySnapshotV1, categoryPayload(catIDX, "active", map[string]string{"en": "X"}, nil, 1))
				f.ingest(t, f.devA, f.credA, current, catalog.EventCategorySnapshotV2, categoryV2Payload(catIDA, "active", map[string]string{"en": "A", "ar": "أ"}, nil, 2, false))
				r1RunCategory(t, d)
			}
			var rev int64
			var enabled bool
			if err := f.pool.QueryRow(ctx, "SELECT source_revision,online_enabled FROM catalog_categories WHERE category_id=$1", catIDA).Scan(&rev, &enabled); err != nil {
				t.Fatal(err)
			}
			if rev != 2 || enabled != (scenario == "equal-contradiction") {
				t.Fatalf("policy regressed %d %t", rev, enabled)
			}
			var state string
			if err := f.pool.QueryRow(ctx, "SELECT status FROM sync_event_processing WHERE event_id=$1 AND processor=$2", current, catalog.ProcessorCategoryProjectionV1).Scan(&state); err != nil {
				t.Fatal(err)
			}
			want := "processed"
			if scenario == "equal-contradiction" {
				want = "blocked"
			}
			if state != want {
				t.Fatalf("processing %s wanted %s", state, want)
			}
			t.Logf("startup production worker: revision=%d enabled=%t v2=%s", rev, enabled, state)
		})
	}
}
func TestR1HealthSummaryDetailParity(t *testing.T) {
	pf := buildPolicyFixture(t)
	ctx := context.Background()
	d := NewDevices(pf.pool, 5*time.Second)
	r1ReadyProduct(t, pf)
	// Missing inventory, Product-policy suppression and ready zero stock are
	// distinct durable states. A second Store uses the same SKU independently.
	for i, entry := range []struct{ dev, cred, typ, body string }{
		{pf.devA, pf.credA, catalog.EventProductSalesPolicySnapshotV1, policyPayload(prodP2, 1, true, true, nil)},
		{pf.devA, pf.credA, catalog.EventProductSalesPolicySnapshotV1, policyPayload(prodP3, 1, true, true, nil)},
		{pf.devA, pf.credA, catalog.EventInventoryProductSnapshotV1, inventoryPayload(prodP3, 1, 0)},
		{pf.devA, pf.credA, catalog.EventProductSalesPolicySnapshotV1, policyPayload(prodP5, 1, true, false, nil)},
		{pf.devB, pf.credB, catalog.EventCategorySnapshotV2, categoryV2Payload("bbbbbbbb-0000-4000-8000-000000000001", "active", map[string]string{"en": "B"}, nil, 1, true)},
		{pf.devB, pf.credB, catalog.EventProductSnapshotV1, productPayload("bbbbbbbb-0000-4000-8000-000000000002", "PAP-P1", "Foreign", "bbbbbbbb-0000-4000-8000-000000000001", nil, nil, 1)},
		{pf.devB, pf.credB, catalog.EventProductSalesPolicySnapshotV1, policyPayload("bbbbbbbb-0000-4000-8000-000000000002", 1, true, true, nil)},
		{pf.devB, pf.credB, catalog.EventInventoryProductSnapshotV1, inventoryPayload("bbbbbbbb-0000-4000-8000-000000000002", 1, 0)},
	} {
		id := fmt.Sprintf("98981111-0000-4000-8000-%012d", i+1)
		pf.ingest(t, entry.dev, entry.cred, id, entry.typ, entry.body)
		projectCatalogTerminal(t, pf.scopeFixture, id, entry.typ)
	}
	if _, err := d.CreateProductMapping(ctx, "review", prodP3, "review-other"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateProductMapping(ctx, "second", prodP4, "second-other"); err != nil {
		t.Fatal(err)
	}
	for _, suppressed := range []bool{false, true} {
		if suppressed {
			pf.toggle(t, catIDA, false, 2)
		}
		for _, store := range []string{"", scopeStoreA, scopeStoreB} {
			for _, provider := range []string{"", "review", "second", "unknown"} {
				summary, err := d.CatalogHealthSummaryRows(ctx, store, provider)
				if err != nil {
					t.Fatal(err)
				}
				details, err := d.CatalogHealthDetailRows(ctx, store, provider, "", 1000)
				if err != nil {
					t.Fatal(err)
				}
				actual := map[string]int64{}
				for _, row := range details {
					actual[row.ReasonCode]++
				}
				for _, row := range summary {
					if actual[row.ReasonCode] != row.Products {
						t.Fatalf("suppressed=%t store=%s provider=%s reason=%s summary=%d details=%d", suppressed, store, provider, row.ReasonCode, row.Products, actual[row.ReasonCode])
					}
					delete(actual, row.ReasonCode)
				}
				if len(actual) != 0 {
					t.Fatalf("detail reasons absent from summary %v", actual)
				}
			}
		}
	}
	summary, err := d.CatalogHealthSummaryRows(ctx, scopeStoreA, "review")
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	for _, row := range summary {
		counts[row.ReasonCode] = row.Products
	}
	if counts["CATEGORY_ONLINE_DISABLED"] != 2 || counts["COMMERCE_MAPPING_MISSING"] != 0 {
		t.Fatalf("suppression misclassified %v", counts)
	}
	t.Log("complete summary/detail parity for enabled/suppressed, all/A/B Stores, all/two/unknown providers")
}

func TestR1Migration28To29Preservation(t *testing.T) {
	pf := buildPolicyFixture(t)
	ctx := context.Background()
	d := NewDevices(pf.pool, 5*time.Second)
	r1ReadyProduct(t, pf)
	drainReevaluations(t, d)
	conn, err := sql.Open("pgx", pf.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := migrate.DownTo(ctx, conn, 28); err != nil {
		t.Fatal(err)
	}
	// Seed representative existing operational state in the exact old schema.
	seeds := []string{
		`INSERT INTO commerce_product_mappings(provider_key,product_id,external_product_id) VALUES ('review','11111111-0000-4000-8000-000000000001','preserved')`,
		`INSERT INTO commerce_product_mutation_barriers(operation_id,provider_key,product_id,request_fingerprint,state) VALUES ('11111111-0000-7000-8000-000000000009','review','11111111-0000-4000-8000-000000000001',repeat('a',64),'uncertain')`,
		`INSERT INTO notification_template_mappings(provider_key,template_key,locale,external_template_name,external_language_code) VALUES ('telegram-main','preserved','ar','plain','ar')`,
		`INSERT INTO notification_messages(id,provider_key,idempotency_key,semantic_fingerprint,recipient,template_key,locale,ext_template_name,ext_language_code,parameters,dispatch_status) VALUES ('11111111-0000-7000-8000-000000000001','telegram-main','preserve','\x0102','@preservedrecipient','preserved','ar','plain','ar','{"body":"تقرير محفوظ"}','accepted')`,
		`INSERT INTO business_report_recipients(id,label,provider_key,recipient,locale) VALUES ('11111111-0000-7000-8000-000000000002','preserved-report','telegram-main','@preservedreport','ar')`,
		`INSERT INTO operational_alert_recipients(id,label,provider_key,recipient,locale) VALUES ('11111111-0000-7000-8000-000000000003','preserved-operation','telegram-main','@preservedops','en')`,
		`INSERT INTO commerce_product_reevaluations(product_id,store_id,reason,attempts,next_attempt_at,last_error_code) VALUES ('11111111-0000-4000-8000-000000000001',NULL,'pending',0,now()-interval '1 minute',NULL),('11111111-0000-4000-8000-000000000002',NULL,'retry',3,now()+interval '1 hour','PRESERVED')`,
	}
	for _, q := range seeds {
		tag, err := pf.pool.Exec(ctx, q)
		want := int64(1)
		if strings.Contains(q, "'pending',0") {
			want = 2
		}
		if err != nil || tag.RowsAffected() != want {
			t.Fatalf("fixture %v %v", tag, err)
		}
	}
	tables, err := pf.pool.Query(ctx, `SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE' AND table_name<>'goose_db_version' ORDER BY table_name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := tables.Err(); err != nil {
		t.Fatal(err)
	}
	tables.Close()
	snapshot := func(name string, after bool) string {
		t.Helper()
		expr := "to_jsonb(t)"
		if after && name == "commerce_product_reevaluations" {
			expr += "-'requested_generation'-'claimed_generation'-'lease_generation'-'lease_token'-'lease_until'"
		}
		// Phase 15 00030 adds documented defaulted columns; business
		// values stay byte-identical across the upgrade cycle.
		if after && name == "catalog_products" {
			expr += "-'configuration_revision'"
			// Phase 17 00037 adds a nullable structural Type reference.
			// Compare all pre-existing values; assert the new column's
			// no-backfill semantics separately below.
			expr += "-'product_type_id'"
		}
		// Phase 17-R0 00035 RETIRES catalog_products.sku (Product has no
		// SKU authority anywhere, ADR-0049): the column is dropped in the
		// after schema, so both snapshots exclude the retired key. Every
		// remaining business value must stay byte-identical.
		if name == "catalog_products" {
			expr += "-'sku'"
		}
		if after && name == "commerce_online_order_lines" {
			expr += "-'configuration_id'-'frame_style_code'-'frame_style_name_ar'-'frame_style_name_en'-'frame_color_code'-'frame_color_name_ar'-'frame_color_name_en'-'configuration_price_delta_minor'-'provider_configuration_id'-'configuration_unresolved'"
		}
		// Append-only 00031 adds nullable receipt fields; compare every
		// pre-existing column exactly, as with the additions above.
		if after && name == "commerce_product_mutation_barriers" {
			expr += "-'async_role'-'async_intent'-'provider_operation_id'-'async_state'-'async_product_id'"
		}
		var data string
		if err := pf.pool.QueryRow(ctx, fmt.Sprintf(`SELECT COALESCE(jsonb_agg(%s ORDER BY (%s)::text),'[]'::jsonb)::text FROM %s t`, expr, expr, `"`+strings.ReplaceAll(name, `"`, `""`)+`"`)).Scan(&data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := map[string]string{}
	for _, name := range names {
		before[name] = snapshot(name, false)
	}
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	version, err := migrate.Current(ctx, conn)
	if err != nil || version != migrate.TargetVersion {
		t.Fatalf("schema %d %v", version, err)
	}
	var assignedTypes int64
	if err := pf.pool.QueryRow(ctx, `SELECT count(*) FROM catalog_products WHERE product_type_id IS NOT NULL`).Scan(&assignedTypes); err != nil || assignedTypes != 0 {
		t.Fatalf("migration fabricated ProductType references: %d %v", assignedTypes, err)
	}
	for _, name := range names {
		if snapshot(name, true) != before[name] {
			t.Fatalf("old table values altered: %s", name)
		}
	}
	claimed, err := d.ClaimProductReevaluations(ctx, 25, time.Now().Add(time.Hour))
	if err != nil || len(claimed) != 1 || claimed[0].ProductID != prodP1 || claimed[0].ClaimedGeneration != 1 {
		t.Fatalf("old pending work not claimable %+v %v", claimed, err)
	}
	if err := migrate.DownTo(ctx, conn, 28); err == nil {
		t.Fatal("rollback discarded fenced work")
	}
	version, err = migrate.Current(ctx, conn)
	if err != nil || version != 29 {
		t.Fatal("rollback partially applied", version, err)
	}
	t.Logf("exact 28→29 preserves values in %d old tables; old due work claimable, retry horizon/code retained; pending rollback refuses atomically", len(names))
}

func TestR1ProviderFailureIndependence(t *testing.T) {
	for _, failed := range []commerce.ProviderKey{"woo-local", "shopify-local"} {
		t.Run(string(failed), func(t *testing.T) {
			pf := buildPolicyFixture(t)
			ctx := context.Background()
			d := NewDevices(pf.pool, 5*time.Second)
			r1ReadyProduct(t, pf)
			drainReevaluations(t, d)
			providers := []*commerce.FakeProvider{commerce.NewFakeProvider("woo-local"), commerce.NewFakeProvider("shopify-local")}
			reg := commerce.NewRegistry()
			for _, p := range providers {
				if err := reg.Register(p.Key(), p); err != nil {
					t.Fatal(err)
				}
				if _, err := d.CreateProductMapping(ctx, p.Key(), prodP1, string(p.Key())+"-identity"); err != nil {
					t.Fatal(err)
				}
				p.OverrideExternal(prodP1, string(p.Key())+"-identity")
				if p.Key() == failed {
					p.FailUpsertOnce(prodP1, &commerce.ProviderError{Kind: commerce.ErrorTemporary, Message: "fixture"})
				}
			}
			q := &r1Queue{Devices: d, done: make(chan struct{}, 8)}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			svc := commerce.NewCommerceService(reg, d, commerce.NewCatalogCommerceSource(catalog.NewService(d)), logger)
			run := func() {
				c, cancel := context.WithCancel(ctx)
				stopped := make(chan struct{})
				go func() { commerce.NewReevaluationWorker(q, svc, reg, time.Now, logger).Run(c); close(stopped) }()
				r1Wait(t, q.done)
				cancel()
				r1Wait(t, stopped)
			}
			if err := d.EnqueueProductReevaluation(ctx, prodP1, nil, "test"); err != nil {
				t.Fatal(err)
			}
			run()
			for _, p := range providers {
				if p.Key() != failed {
					if len(p.Inventories()) != 1 || p.Inventories()[0].AvailableQuantity != 10 {
						t.Fatal("healthy provider held back")
					}
				}
			}
			tag, err := pf.pool.Exec(ctx, "UPDATE commerce_product_reevaluations SET next_attempt_at=now() WHERE product_id=$1", prodP1)
			if err != nil || tag.RowsAffected() != 1 {
				t.Fatal("retry intent lost", tag, err)
			}
			run()
			for _, p := range providers {
				v := p.Inventories()
				if len(v) == 0 || v[len(v)-1].AvailableQuantity != 10 {
					t.Fatalf("provider not recovered %s", p.Key())
				}
				mapping, err := d.GetProductMapping(ctx, p.Key(), prodP1)
				if err != nil || mapping.ExternalProductID != string(p.Key())+"-identity" {
					t.Fatal("mapping changed", err)
				}
			}
			t.Log("healthy provider progresses despite independent failure; failed provider recovers under durable retry without replacing either mapping")
		})
	}
}

func TestR1NewPolicyBetweenProviders(t *testing.T) {
	pf := buildPolicyFixture(t)
	ctx := context.Background()
	d := NewDevices(pf.pool, 5*time.Second)
	r1ReadyProduct(t, pf)
	drainReevaluations(t, d)
	first := &r1Provider{key: "a-woo", entered: make(chan commerce.ProductUpsertRequest, 8), release: make(chan struct{})}
	second := commerce.NewFakeProvider("z-shopify")
	second.OverrideExternal(prodP1, "shopify-identity")
	reg := commerce.NewRegistry()
	for _, p := range []commerce.CommerceProvider{first, second} {
		if err := reg.Register(p.Key(), p); err != nil {
			t.Fatal(err)
		}
		id := "review-remote"
		if p.Key() == second.Key() {
			id = "shopify-identity"
		}
		if _, err := d.CreateProductMapping(ctx, p.Key(), prodP1, id); err != nil {
			t.Fatal(err)
		}
	}
	q := &r1Queue{Devices: d, done: make(chan struct{}, 8)}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := commerce.NewCommerceService(reg, d, commerce.NewCatalogCommerceSource(catalog.NewService(d)), logger)
	run := func() (context.CancelFunc, chan struct{}) {
		c, cancel := context.WithCancel(ctx)
		stop := make(chan struct{})
		go func() { commerce.NewReevaluationWorker(q, svc, reg, time.Now, logger).Run(c); close(stop) }()
		return cancel, stop
	}
	if err := d.EnqueueProductReevaluation(ctx, prodP1, nil, "test"); err != nil {
		t.Fatal(err)
	}
	cancel, stop := run()
	select {
	case r := <-first.entered:
		if !r.Published {
			t.Fatal("first provider not original state")
		}
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("no first operation")
	}
	pf.toggle(t, catIDA, false, 2)
	close(first.release)
	r1Wait(t, q.done)
	cancel()
	r1Wait(t, stop)
	up := second.Upserts()
	if len(up) != 1 || up[0].Published {
		t.Fatal("second provider failed to reread current policy", up)
	}
	cancel, stop = run()
	r1Wait(t, q.done)
	cancel()
	r1Wait(t, stop)
	if first.published || first.quantity != 0 {
		t.Fatal("older provider not corrected")
	}
	inv := second.Inventories()
	if len(inv) == 0 || inv[len(inv)-1].AvailableQuantity != 0 {
		t.Fatal("second availability wrong")
	}
	t.Log("policy commits between providers; later provider reads latest policy, earlier provider's corrective intent survives and restart converges")
}
func TestR1HealthReadOnlySuppression(t *testing.T) {
	pf := buildPolicyFixture(t)
	ctx := context.Background()
	d := NewDevices(pf.pool, 5*time.Second)
	r1ReadyProduct(t, pf)
	pf.toggle(t, catIDA, false, 2)
	if _, err := d.CreateProductMapping(ctx, "review", prodP3, "mapped-other"); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var data string
		if err := pf.pool.QueryRow(ctx, `SELECT jsonb_build_object('queue',(SELECT coalesce(jsonb_agg(to_jsonb(q)),'[]') FROM commerce_product_reevaluations q),'barriers',(SELECT coalesce(jsonb_agg(to_jsonb(b)),'[]') FROM commerce_product_mutation_barriers b),'categories',(SELECT coalesce(jsonb_agg(to_jsonb(c)),'[]') FROM catalog_categories c),'mappings',(SELECT coalesce(jsonb_agg(to_jsonb(m)),'[]') FROM commerce_product_mappings m))::text`).Scan(&data); err != nil {
			t.Fatal(err)
		}
		return data
	}
	before := snapshot()
	cfg := pf.pool.Config().Copy()
	cfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	ro, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	readonly := NewDevices(ro, 5*time.Second)
	for i := 0; i < 3; i++ {
		summary, err := readonly.CatalogHealthSummaryRows(ctx, scopeStoreA, "review")
		if err != nil {
			t.Fatal(err)
		}
		details, err := readonly.CatalogHealthDetailRows(ctx, scopeStoreA, "review", "", 1000)
		if err != nil {
			t.Fatal(err)
		}
		actual := map[string]int64{}
		for _, r := range details {
			actual[r.ReasonCode]++
		}
		for _, r := range summary {
			if actual[r.ReasonCode] != r.Products {
				t.Fatal("readonly parity", r)
			}
		}
	}
	if before != snapshot() {
		t.Fatal("health altered durable queue/barrier/catalog/mapping state")
	}
	t.Log("read-only PostgreSQL sessions accept suppressed health queries; durable queue, barriers, catalog and mappings byte-equivalent")
}

func TestR1CategoryProductionFanout(t *testing.T) {
	pf := buildPolicyFixture(t)
	ctx := context.Background()
	d := NewDevices(pf.pool, 5*time.Second)
	drainReevaluations(t, d)
	id := "98983333-0000-4000-8000-000000000001"
	pf.ingest(t, pf.devA, pf.credA, id, catalog.EventCategorySnapshotV2, categoryV2Payload(catIDA, "active", map[string]string{"en": "A"}, nil, 2, false))
	r1RunCategory(t, d)
	rows := reevaluationRows(t, d)
	if len(rows) != 4 {
		t.Fatalf("production v2 fanout %v", rows)
	}
	if policyOrFail(t, d, prodP1).Allowed || !policyOrFail(t, d, prodP3).Allowed {
		t.Fatal("automatic policy/Store isolation wrong")
	}
	var gen int64
	if err := pf.pool.QueryRow(ctx, "SELECT requested_generation FROM commerce_product_reevaluations WHERE product_id=$1", prodP1).Scan(&gen); err != nil {
		t.Fatal(err)
	}
	dup := "98983333-0000-4000-8000-000000000002"
	pf.ingest(t, pf.devA, pf.credA, dup, catalog.EventCategorySnapshotV2, categoryV2Payload(catIDA, "active", map[string]string{"en": "A"}, nil, 2, false))
	r1RunCategory(t, d)
	var after int64
	if err := pf.pool.QueryRow(ctx, "SELECT requested_generation FROM commerce_product_reevaluations WHERE product_id=$1", prodP1).Scan(&after); err != nil || after != gen {
		t.Fatal("duplicate replay fanned out again", gen, after, err)
	}
	t.Log("production v2 projection queues exactly four affected Products; duplicate equal semantics do not advance request generation or fan out again")
}
