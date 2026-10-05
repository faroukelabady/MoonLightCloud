package postgres

import (
	"context"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"testing"
	"time"
)

func TestR3UnusedConfigurationIDTwoStoreRace(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	type candidate struct{ dev, cred, product, category, event string }
	candidates := []candidate{{f.devA, f.credA, "22222222-0000-4000-8000-0000000000a1", "aaaaaaaa-0000-4000-8000-0000000000a1", "e5c015a0-0000-4000-8000-000000000101"}, {f.devB, f.credB, "22222222-0000-4000-8000-0000000000b1", "aaaaaaaa-0000-4000-8000-0000000000b1", "e5c015a0-0000-4000-8000-000000000102"}}
	const config = "11111111-0000-4000-8000-0000000000ff"
	for i, c := range candidates {
		categoryEvent := "e5c015a0-0000-4000-8000-000000000201"
		productEvent := "e5c015a0-0000-4000-8000-000000000301"
		if i == 1 {
			categoryEvent = "e5c015a0-0000-4000-8000-000000000202"
			productEvent = "e5c015a0-0000-4000-8000-000000000302"
		}
		f.ingest(t, c.dev, c.cred, categoryEvent, catalog.EventCategorySnapshotV1, categoryPayload(c.category, "active", map[string]string{"en": "Root"}, nil, 1))
		if r := projectCatalogTerminal(t, f, categoryEvent, catalog.EventCategorySnapshotV1); r.Outcome != catalog.OutcomeProcessed {
			t.Fatal(r)
		}
		f.ingest(t, c.dev, c.cred, productEvent, catalog.EventProductSnapshotV1, productPayload(c.product, "R3-SCOPE", "Product", c.category, nil, nil, 1))
		if r := projectCatalogTerminal(t, f, productEvent, catalog.EventProductSnapshotV1); r.Outcome != catalog.OutcomeProcessed {
			t.Fatal(r)
		}
		f.ingest(t, c.dev, c.cred, c.event, catalog.EventProductConfigurationSnapshotV1, configPayload(c.product, 1, []map[string]any{configEntry(config, "classic", "black", 30000, 0)}))
	}
	d := NewDevices(f.pool, 5*time.Second)
	events := make([]catalog.EventRecord, 2)
	for i, c := range candidates {
		r, found, e := d.LoadCatalogEvent(ctx, c.event)
		if e != nil || !found {
			t.Fatal(e)
		}
		events[i] = r
	}
	type attempt struct {
		result catalog.ProjectResult
		err    error
	}
	start := make(chan struct{})
	out := make(chan attempt, 2)
	for _, event := range events {
		go func() { <-start; r, e := d.ProjectProductConfigurations(ctx, event, time.Now()); out <- attempt{r, e} }()
	}
	close(start)
	for i := 0; i < 2; i++ {
		r := <-out
		if r.err != nil && !catalogAttemptRetryable(r.result, r.err) {
			t.Fatal(r.err)
		}
	}
	// Resolve only documented transient outcomes, preserving real concurrent first attempts.
	for _, c := range candidates {
		_ = projectCatalogTerminal(t, f, c.event, catalog.EventProductConfigurationSnapshotV1)
	}
	var rows, owners int
	if e := f.pool.QueryRow(ctx, "SELECT count(*),count(DISTINCT product_id) FROM catalog_product_configurations WHERE configuration_id=$1", config).Scan(&rows, &owners); e != nil || rows != 1 || owners != 1 {
		t.Fatalf("single owner invariant rows=%d owners=%d %v", rows, owners, e)
	}
	var processed, blocked int
	if e := f.pool.QueryRow(ctx, "SELECT count(*) FILTER(WHERE status='processed'),count(*) FILTER(WHERE status='blocked') FROM sync_event_processing WHERE event_id=ANY($1::uuid[])", []string{candidates[0].event, candidates[1].event}).Scan(&processed, &blocked); e != nil || processed != 1 || blocked != 1 {
		t.Fatalf("outcomes %d/%d %v", processed, blocked, e)
	}
}

func TestR3ConfigurationUsesIngressStoreAfterRebind(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	category, product, event := "aaaaaaaa-0000-4000-8000-0000000000a1", "22222222-0000-4000-8000-0000000000a1", "e5c015a0-0000-4000-8000-000000000401"
	f.ingest(t, f.devA, f.credA, "e5c015a0-0000-4000-8000-000000000402", catalog.EventCategorySnapshotV1, categoryPayload(category, "active", map[string]string{"en": "Root"}, nil, 1))
	if r := projectCatalogTerminal(t, f, "e5c015a0-0000-4000-8000-000000000402", catalog.EventCategorySnapshotV1); r.Outcome != catalog.OutcomeProcessed {
		t.Fatal(r)
	}
	f.ingest(t, f.devA, f.credA, "e5c015a0-0000-4000-8000-000000000403", catalog.EventProductSnapshotV1, productPayload(product, "R3-REBIND", "Product", category, nil, nil, 1))
	if r := projectCatalogTerminal(t, f, "e5c015a0-0000-4000-8000-000000000403", catalog.EventProductSnapshotV1); r.Outcome != catalog.OutcomeProcessed {
		t.Fatal(r)
	}
	f.ingest(t, f.devA, f.credA, event, catalog.EventProductConfigurationSnapshotV1, configPayload(product, 1, []map[string]any{configEntry("11111111-0000-4000-8000-0000000000fe", "classic", "black", 30000, 0)}))
	tag, e := f.pool.Exec(ctx, "UPDATE device_store_bindings SET store_id=$1 WHERE device_id=$2", scopeStoreB, f.devA)
	if e != nil || tag.RowsAffected() != 1 {
		t.Fatal("fixture rebind failed")
	}
	r := projectCatalogTerminal(t, f, event, catalog.EventProductConfigurationSnapshotV1)
	if r.Outcome != catalog.OutcomeProcessed {
		t.Fatal(r)
	}
	var owner string
	if e := f.pool.QueryRow(ctx, "SELECT p.store_id::text FROM catalog_product_configurations c JOIN catalog_products p USING(product_id) JOIN sync_events e ON e.event_id=c.source_event_id WHERE c.product_id=$1 AND e.store_id=p.store_id", product).Scan(&owner); e != nil || owner != scopeStoreA {
		t.Fatalf("ingress owner changed %v", e)
	}
}
