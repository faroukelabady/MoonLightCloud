package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
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
func (p *r1Provider) UpsertProductVariants(_ context.Context, r commerce.ProductVariantsUpsertRequest) (commerce.ProductVariantsUpsertResult, error) {
	ids := make(map[string]string, len(r.Product.Variants))
	for _, variant := range r.Product.Variants {
		ids[variant.VariantID] = "review-" + variant.VariantID
	}
	return commerce.ProductVariantsUpsertResult{Variants: ids}, nil
}
func (p *r1Provider) SetVariantInventory(_ context.Context, r commerce.VariantInventoryUpdateRequest) error {
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
	seedScopeInventoryVariant(t, pf.scopeFixture, prodP1)
	for i, typ := range []string{catalog.EventProductSalesPolicySnapshotV1, catalog.EventInventoryProductVariantSnapshotV1} {
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

func (s *r1CatalogScan) PendingCatalogEvents(ctx context.Context, processor, typ string, limit int, asOf time.Time) ([]string, error) {
	rows, err := s.Devices.PendingCatalogEvents(ctx, processor, typ, limit, asOf)
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
	for _, scenario := range []string{"older-then-newer", "newer-then-stale", "equal-contradiction", "duplicate", "bootstrap"} {
		t.Run(scenario, func(t *testing.T) {
			f := openScopeFixture(t)
			ctx := context.Background()
			d := NewDevices(f.pool, 5*time.Second)
			old, current := "98987777-0000-4000-8000-000000000001", "98987777-0000-4000-8000-000000000002"
			send := func(id, typ string, rev int64, enabled bool) {
				t.Helper()
				body := categoryV2Payload(catIDA, "active", map[string]string{"en": "A", "ar": "أ"}, nil, rev, enabled)
				f.ingest(t, f.devA, f.credA, id, typ, body)
				r1RunCategory(t, d)
			}
			switch scenario {
			case "older-then-newer":
				send(old, catalog.EventCategorySnapshotV2, 1, true)
				send(current, catalog.EventCategorySnapshotV2, 2, false)
			case "newer-then-stale":
				send(current, catalog.EventCategorySnapshotV2, 2, false)
				send(old, catalog.EventCategorySnapshotV2, 1, true)
			case "equal-contradiction":
				send(old, catalog.EventCategorySnapshotV2, 2, true)
				send(current, catalog.EventCategorySnapshotV2, 2, false)
			case "duplicate":
				send(old, catalog.EventCategorySnapshotV2, 2, false)
				send(current, catalog.EventCategorySnapshotV2, 2, false)
			case "bootstrap":
				f.ingest(t, f.devA, f.credA, old, catalog.EventCategorySnapshotV2, categoryPayload(catIDX, "active", map[string]string{"en": "X"}, nil, 1))
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
		{pf.devA, pf.credA, catalog.EventInventoryProductVariantSnapshotV1, inventoryPayload(prodP3, 1, 0)},
		{pf.devA, pf.credA, catalog.EventProductSalesPolicySnapshotV1, policyPayload(prodP5, 1, true, false, nil)},
		{pf.devB, pf.credB, catalog.EventCategorySnapshotV2, categoryV2Payload("bbbbbbbb-0000-4000-8000-000000000001", "active", map[string]string{"en": "B"}, nil, 1, true)},
		{pf.devB, pf.credB, catalog.EventProductSnapshotV2, productPayload("bbbbbbbb-0000-4000-8000-000000000002", "PAP-P1", "Foreign", "bbbbbbbb-0000-4000-8000-000000000001", nil, nil, 1)},
		{pf.devB, pf.credB, catalog.EventProductSalesPolicySnapshotV1, policyPayload("bbbbbbbb-0000-4000-8000-000000000002", 1, true, true, nil)},
		{pf.devB, pf.credB, catalog.EventInventoryProductVariantSnapshotV1, inventoryPayload("bbbbbbbb-0000-4000-8000-000000000002", 1, 0)},
	} {
		id := fmt.Sprintf("98981111-0000-4000-8000-%012d", i+1)
		if entry.typ == catalog.EventInventoryProductVariantSnapshotV1 {
			var inventory catalog.ProductVariantInventorySnapshot
			if err := json.Unmarshal([]byte(entry.body), &inventory); err != nil {
				t.Fatal(err)
			}
			seedScopeInventoryVariant(t, pf.scopeFixture, inventory.ProductID)
		}
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
				summary, err := NewReporting(d.pool, d.timeout).CatalogHealthSummaryRows(ctx, store, provider)
				if err != nil {
					t.Fatal(err)
				}
				details, err := NewReporting(d.pool, d.timeout).CatalogHealthDetailRows(ctx, store, provider, "", 1000)
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
	summary, err := NewReporting(d.pool, d.timeout).CatalogHealthSummaryRows(ctx, scopeStoreA, "review")
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
					if len(p.VariantInventories()) != 1 || p.VariantInventories()[0].AvailableQuantity != 10 {
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
				v := p.VariantInventories()
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
	inv := second.VariantInventories()
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
		summary, err := NewReporting(readonly.pool, readonly.timeout).CatalogHealthSummaryRows(ctx, scopeStoreA, "review")
		if err != nil {
			t.Fatal(err)
		}
		details, err := NewReporting(readonly.pool, readonly.timeout).CatalogHealthDetailRows(ctx, scopeStoreA, "review", "", 1000)
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
