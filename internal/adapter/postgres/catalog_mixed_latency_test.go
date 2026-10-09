package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
)

// Exercise the complete scoped dependency graph with production workers and
// real time. Never clear next_attempt_at or force an event due. Homogeneous
// ProductType throughput does not prove this mixed bootstrap converges.
func TestCatalogMixedBootstrapConvergesWithoutDeadlineReset(t *testing.T) {
	f := openScopeFixture(t)
	d := NewDevices(f.pool, 5*time.Second)
	ids := catalogIDs(t, 65000, "root", "child", "leaf", "type", "type2", "tag1", "tag2", "tag3")
	ctx := context.Background()
	n := 0
	add := func(typ, raw string) {
		t.Helper()
		f.ingest(t, f.devA, f.credA, variantEventID(66000+n), typ, raw)
		n++
	}
	// Dependents precede their parents in ingress order, as in a reconnect.
	for i := 0; i < 4; i++ {
		entity := catalogIDs(t, 65100+i*2, "product", "variant")
		sku := fmt.Sprintf("ML-MIXED-%d", i)
		add(catalog.EventInventoryProductVariantSnapshotV1, variantInventoryPayload(entity["variant"], entity["product"], sku, 4+i, 8, true, nil))
		for rev := int64(1); rev <= 2; rev++ {
			add(catalog.EventProductVariantSnapshotV1, variantPayload(entity["variant"], entity["product"], sku, true, false, "color=blue", []any{variantAttr("color", "blue", 0)}, rev))
			var p map[string]any
			if err := json.Unmarshal([]byte(proofFixture(t, "../../catalog/testdata/wire_product_v2.json")), &p); err != nil {
				t.Fatal(err)
			}
			p["product_id"], p["product_type_id"] = entity["product"], ids["type"]
			p["top_category_id"], p["subcategory_ids"] = ids["root"], []string{ids["leaf"]}
			p["tag_ids"], p["catalog_revision"] = []string{ids["tag1"], ids["tag2"]}, rev
			p["name"] = fmt.Sprintf("Mixed Product %d revision %d", i, rev)
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			add(catalog.EventProductSnapshotV2, string(raw))
		}
		add(catalog.EventProductSalesPolicySnapshotV1, policyPayload(entity["product"], 1, true, true, nil))
	}
	add(catalog.EventCategorySnapshotV1, categoryPayload(ids["leaf"], "active", map[string]string{"ar": "فرع"}, []string{ids["child"]}, 1))
	add(catalog.EventCategorySnapshotV1, categoryPayload(ids["child"], "active", map[string]string{"ar": "فرع"}, []string{ids["root"]}, 1))
	add(catalog.EventCategorySnapshotV1, categoryPayload(ids["root"], "active", map[string]string{"ar": "قسم"}, nil, 1))
	for i := 1; i <= 3; i++ {
		add(catalog.EventTagSnapshotV1, tagPayload(ids[fmt.Sprintf("tag%d", i)], fmt.Sprintf("mixed-%d", i), true, map[string]string{"ar": "وسم"}, 1))
	}
	add(catalog.EventProductTypeSnapshotV1, typePayload(ids["type"], "mixed_book", 1))
	add(catalog.EventProductTypeSnapshotV1, typePayload(ids["type2"], "mixed_clothing", 1))
	if n != 32 {
		t.Fatalf("fixture events=%d, want32", n)
	}

	runCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	// Two instances of every involved stream retain database-based claiming;
	// WakeOnProgress uses the same non-blocking hints as the server wiring.
	var workers []*catalog.Projector
	for i := 0; i < 2; i++ {
		workers = append(workers,
			catalog.NewCategoryProjector(d, clock.System{}, nilLogger()),
			catalog.NewTagProjector(d, clock.System{}, nilLogger()),
			catalog.NewProductTypeProjector(d, clock.System{}, nilLogger()),
			catalog.NewProductProjector(d, clock.System{}, nilLogger()),
			catalog.NewProductVariantProjector(d, clock.System{}, nilLogger()),
			catalog.NewProductVariantInventoryProjector(d, clock.System{}, nilLogger()),
			catalog.NewProductSalesPolicyProjector(d, clock.System{}, nilLogger()))
	}
	for _, w := range workers {
		w.WakeOnProgress(func() {
			for _, dependent := range workers {
				dependent.Notify()
			}
		})
	}
	for _, w := range workers {
		wg.Add(1)
		go func() { defer wg.Done(); w.Run(runCtx) }()
	}
	var processed, blocked, retries int
	for {
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE p.status='processed'),
			count(*) FILTER(WHERE p.status='blocked'),COALESCE(sum(GREATEST(p.attempt_count-1,0)),0)
			FROM sync_events e LEFT JOIN sync_event_processing p ON p.event_id=e.event_id
			WHERE e.store_id=$1`, scopeStoreA).Scan(&processed, &blocked, &retries); err != nil {
			t.Fatal(err)
		}
		if blocked > 0 {
			t.Fatalf("valid bootstrap blocked: processed=%d blocked=%d retries=%d", processed, blocked, retries)
		}
		if processed == n {
			break
		}
		select {
		case <-runCtx.Done():
			t.Fatalf("mixed bootstrap exceeded20s: processed=%d/%d retries=%d", processed, n, retries)
		case <-time.After(50 * time.Millisecond):
		}
	}
	var seconds float64
	if err := f.pool.QueryRow(ctx, `SELECT max(extract(epoch FROM(p.processed_at-e.received_at)))::float8
		FROM sync_events e JOIN sync_event_processing p ON p.event_id=e.event_id WHERE e.store_id=$1`, scopeStoreA).Scan(&seconds); err != nil {
		t.Fatal(err)
	}
	if seconds > 20 {
		t.Fatalf("receipt-to-terminal max=%.3fs exceeds20s", seconds)
	}
	for i := 0; i < 4; i++ {
		entity := catalogIDs(t, 65100+i*2, "product", "variant")
		var stock, revision int64
		if err := f.pool.QueryRow(ctx, `SELECT stock_quantity,source_revision FROM catalog_product_variant_inventory
			WHERE variant_id=$1 AND store_id=$2`, entity["variant"], scopeStoreA).Scan(&stock, &revision); err != nil || stock != int64(4+i) || revision != 8 {
			t.Fatalf("variant%d stock=%d revision=%d err=%v", i, stock, revision, err)
		}
	}
	t.Logf("mixed events=%d instances=2 processed=%d blocked=%d retryAttempts=%d receipt-to-terminal max=%.3fs", n, processed, blocked, retries, seconds)
}

// An absent dependency must stay durable without a tight retry loop, even
// under notification floods. Short local waits are not in-process retries.
func TestCatalogMissingDependencyRemainsBoundedUnderWakeFlood(t *testing.T) {
	env := openSaleEnv(t)
	_, product := seedCatalogProduct(t, env, 67000, "ML-WAIT-P")
	missing := catalogIDs(t, 67010, "variant")["variant"]
	event := variantEventID(67020)
	ingestCatalog(t, env, event, catalog.EventInventoryProductVariantSnapshotV1, "2026-09-20T10:00:00Z",
		variantInventoryPayload(missing, product, "ML-WAIT-V", 4, 8, true, nil))
	store := &countingSkewStore{Devices: catalogStore(env)}
	p := catalog.NewProductVariantInventoryProjector(store, clock.System{}, nilLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); p.Run(ctx) }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for ctx.Err() == nil {
		select {
		case <-ticker.C:
			p.Notify()
		case <-ctx.Done():
		}
	}
	<-done
	var status, code string
	var attempts int
	if err := env.pool.QueryRow(context.Background(), `SELECT status,last_error_code,attempt_count
		FROM sync_event_processing WHERE event_id=$1 AND processor=$2`, event, catalog.ProcessorProductVariantInventoryProjectionV1).Scan(&status, &code, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "retry" || code != ErrCatalogDependencyWait || attempts < 2 || attempts > 4 || store.projects.Load() > 4 {
		t.Fatalf("unbounded/lost wait: status=%s code=%s attempts=%d projects=%d", status, code, attempts, store.projects.Load())
	}
	if n := saleCount(t, env.pool, "catalog_product_variant_inventory"); n != 0 {
		t.Fatalf("missing Variant projected %d inventory rows", n)
	}
	t.Logf("3s notification flood: attempts=%d projects=%d durable=%s", attempts, store.projects.Load(), status)
}
