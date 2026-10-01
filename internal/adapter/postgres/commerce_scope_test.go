package postgres

// Phase 9C commerce mapping ownership on real PostgreSQL. Mapping Store
// always mirrors the authoritative catalog product (NULL for legacy
// products); cross-Store claims conflict instead of overwriting.
// Reuses the 9B scope fixture (two bound Stores + legacy device).

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// scopeProducts builds one same-SKU product per Store through the real
// catalog projector path and returns their IDs.
func scopeProducts(t *testing.T, f *scopeFixture, base int, prefix, sku string) (prodA, prodB string) {
	t.Helper()
	rootA, tagA, rootB, tagB := f.scopeGraph(t, base, prefix)
	ids := catalogIDs(t, base+10, prefix+"-a", prefix+"-b")
	prodA, prodB = ids[prefix+"-a"], ids[prefix+"-b"]
	f.ingest(t, f.devA, f.credA, uuidEvent(t, base, 11),
		catalog.EventProductSnapshotV1, productPayload(prodA, sku, "A", rootA, nil, []string{tagA}, 1))
	f.ingest(t, f.devB, f.credB, uuidEvent(t, base, 12),
		catalog.EventProductSnapshotV1, productPayload(prodB, sku, "B", rootB, nil, []string{tagB}, 1))
	requireOutcome(t, f.projectCatalog(t, uuidEvent(t, base, 11), catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")
	requireOutcome(t, f.projectCatalog(t, uuidEvent(t, base, 12), catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")
	return prodA, prodB
}

func uuidEvent(t *testing.T, base, seq int) string {
	t.Helper()
	return fmt.Sprintf("e%07x-0000-4000-8000-%012x", base, seq)
}

// mappingStore reads a mapping's store_id (nil = legacy).
func mappingStore(t *testing.T, f *scopeFixture, provider, product string) *string {
	t.Helper()
	var raw *string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT store_id::text FROM commerce_product_mappings WHERE provider_key=$1 AND product_id=$2`,
		provider, product).Scan(&raw); err != nil {
		t.Fatalf("mapping store: %v", err)
	}
	return raw
}

func createMapping(t *testing.T, f *scopeFixture, provider, product, external string) commerce.ProductMapping {
	t.Helper()
	mapping, err := NewDevices(f.pool, 5*time.Second).CreateProductMapping(
		context.Background(), commerce.ProviderKey(provider), product, external)
	if err != nil {
		t.Fatalf("create mapping: %v", err)
	}
	return mapping
}

// TestMapping_SameSKUCoexists proves mappings for same-SKU products in
// two Stores coexist with independent ownership and externals.
func TestMapping_SameSKUCoexists(t *testing.T) {
	f := openScopeFixture(t)
	prodA, prodB := scopeProducts(t, f, 400, "map", "MAP-001")
	mappingA := createMapping(t, f, "website", prodA, "900")
	mappingB := createMapping(t, f, "website", prodB, "901")
	if mappingA.StoreID == nil || *mappingA.StoreID != scopeStoreA {
		t.Fatalf("mapping A store: %v", mappingA.StoreID)
	}
	if mappingB.StoreID == nil || *mappingB.StoreID != scopeStoreB {
		t.Fatalf("mapping B store: %v", mappingB.StoreID)
	}
	if got := mappingStore(t, f, "website", prodA); got == nil || *got != scopeStoreA {
		t.Fatalf("durable mapping A: %v", got)
	}
}

// TestMapping_LegacyAdoption proves a NULL mapping adopts its product's
// proven Store through the idempotent same-pair path.
func TestMapping_LegacyAdoption(t *testing.T) {
	f := openScopeFixture(t)
	ids := catalogIDs(t, 420, "root-g", "tag-g", "prod-g")
	rootG, tagG, prodG := ids["root-g"], ids["tag-g"], ids["prod-g"]
	names := map[string]string{"ar": "g"}
	f.ingest(t, f.devC, f.credC, "e0000420-0000-4000-8000-000000000001",
		catalog.EventCategorySnapshotV1, categoryPayload(rootG, "active", names, nil, 1))
	f.ingest(t, f.devC, f.credC, "e0000420-0000-4000-8000-000000000002",
		catalog.EventTagSnapshotV1, tagPayload(tagG, "legacy-map-tag", true, names, 1))
	f.ingest(t, f.devC, f.credC, "e0000420-0000-4000-8000-000000000003",
		catalog.EventProductSnapshotV1, productPayload(prodG, "LEG-1", "G", rootG, nil, []string{tagG}, 1))
	for _, tc := range []struct{ event, typ string }{
		{"e0000420-0000-4000-8000-000000000001", catalog.EventCategorySnapshotV1},
		{"e0000420-0000-4000-8000-000000000002", catalog.EventTagSnapshotV1},
		{"e0000420-0000-4000-8000-000000000003", catalog.EventProductSnapshotV1},
	} {
		requireOutcome(t, f.projectCatalog(t, tc.event, tc.typ), catalog.OutcomeProcessed, "")
	}
	mapping := createMapping(t, f, "website", prodG, "902")
	if mapping.StoreID != nil {
		t.Fatalf("legacy mapping stays NULL: %v", mapping.StoreID)
	}
	// The product adopts Store A; the same-pair replay adopts the mapping.
	f.ingest(t, f.devA, f.credA, "e0000420-0000-4000-8000-000000000004",
		catalog.EventProductSnapshotV1, productPayload(prodG, "LEG-1", "G", rootG, nil, []string{tagG}, 2))
	requireOutcome(t, f.projectCatalog(t, "e0000420-0000-4000-8000-000000000004", catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")
	adopted := createMapping(t, f, "website", prodG, "902")
	if adopted.StoreID == nil || *adopted.StoreID != scopeStoreA {
		t.Fatalf("mapping adopted A: %v", adopted.StoreID)
	}
	if got := mappingStore(t, f, "website", prodG); got == nil || *got != scopeStoreA {
		t.Fatalf("durable mapping adopted: %v", got)
	}
}

// TestMapping_CrossStoreConflict proves identity collisions stay frozen
// conflicts: same external for another product, or a different external
// for a mapped product, never overwrite — across Stores alike.
func TestMapping_CrossStoreConflict(t *testing.T) {
	f := openScopeFixture(t)
	prodA, prodB := scopeProducts(t, f, 440, "mc", "MAP-002")
	createMapping(t, f, "website", prodA, "910")
	store := NewDevices(f.pool, 5*time.Second)
	ctx := context.Background()
	if _, err := store.CreateProductMapping(ctx, "website", prodA, "911"); err == nil {
		t.Fatal("remap same product must conflict")
	}
	if _, err := store.CreateProductMapping(ctx, "website", prodB, "910"); err == nil {
		t.Fatal("same external for another product must conflict")
	}
	mapping, err := store.GetProductMapping(ctx, "website", prodA)
	if err != nil || mapping.ExternalProductID != "910" {
		t.Fatalf("A mapping intact: %+v %v", mapping, err)
	}
	if mapping.StoreID == nil || *mapping.StoreID != scopeStoreA {
		t.Fatalf("A mapping store intact: %v", mapping.StoreID)
	}
	var count int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM commerce_product_mappings WHERE provider_key='website' AND external_product_id='910'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("one owner of external 910: %d (%v)", count, err)
	}
}

// TestMapping_ConcurrentAdoption proves racing adoptions converge on one
// proven owner with no rewrite.
func TestMapping_ConcurrentAdoption(t *testing.T) {
	f := openScopeFixture(t)
	prodA, _ := scopeProducts(t, f, 460, "mr", "MAP-003")
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := NewDevices(f.pool, 5*time.Second)
			_, errs[i] = store.CreateProductMapping(context.Background(), "website", prodA, "920")
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent same-pair: %v", err)
		}
	}
	if got := mappingStore(t, f, "website", prodA); got == nil || *got != scopeStoreA {
		t.Fatalf("single adopted owner: %v", got)
	}
	var count int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM commerce_product_mappings WHERE provider_key='website' AND product_id=$1`, prodA).Scan(&count); err != nil || count != 1 {
		t.Fatalf("one mapping row: %d (%v)", count, err)
	}
}

// TestMapping_RetryRestart proves same-pair replay is idempotent across
// handles (restart): same row, same Store, no duplicate.
func TestMapping_RetryRestart(t *testing.T) {
	f := openScopeFixture(t)
	prodA, _ := scopeProducts(t, f, 480, "mrr", "MAP-004")
	first := createMapping(t, f, "website", prodA, "930")
	second := createMapping(t, f, "website", prodA, "930")
	if first.ExternalProductID != second.ExternalProductID {
		t.Fatalf("replay identical: %+v vs %+v", first, second)
	}
	if second.StoreID == nil || *second.StoreID != scopeStoreA {
		t.Fatalf("replay keeps Store: %v", second.StoreID)
	}
}

// TestMapping_PublicationEnumeration proves the Store-aware enumeration
// returns exactly the Store's online products (legacy excluded).
func TestMapping_PublicationEnumeration(t *testing.T) {
	f := openScopeFixture(t)
	prodA, prodB := scopeProducts(t, f, 500, "men", "MAP-005")
	f.ingest(t, f.devA, f.credA, uuidEvent(t, 510, 1),
		catalog.EventProductSalesPolicySnapshotV1, policyPayload(prodA, 1, true, true, policyInt(10)))
	f.ingest(t, f.devB, f.credB, uuidEvent(t, 510, 2),
		catalog.EventProductSalesPolicySnapshotV1, policyPayload(prodB, 1, true, true, policyInt(10)))
	requireOutcome(t, f.projectCatalog(t, uuidEvent(t, 510, 1), catalog.EventProductSalesPolicySnapshotV1), catalog.OutcomeProcessed, "")
	requireOutcome(t, f.projectCatalog(t, uuidEvent(t, 510, 2), catalog.EventProductSalesPolicySnapshotV1), catalog.OutcomeProcessed, "")
	store := NewDevices(f.pool, 5*time.Second)
	ctx := context.Background()
	onlyA, err := store.CatalogOnlineConfiguredProductsForStore(ctx, scopeStoreA, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyA) != 1 || onlyA[0] != prodA {
		t.Fatalf("store A enumeration: %v", onlyA)
	}
	onlyB, err := store.CatalogOnlineConfiguredProductsForStore(ctx, scopeStoreB, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyB) != 1 || onlyB[0] != prodB {
		t.Fatalf("store B enumeration: %v", onlyB)
	}
}

// TestMapping_ExternalClaimRace proves concurrent cross-Store claims of
// one provider external ID resolve to at most one owner with a
// deterministic conflict for the loser and no mapping rewrite.
func TestMapping_ExternalClaimRace(t *testing.T) {
	f := openScopeFixture(t)
	prodA, prodB := scopeProducts(t, f, 540, "mcr", "MAP-006")
	var wg sync.WaitGroup
	type rout struct {
		mapping commerce.ProductMapping
		err     error
	}
	outs := make([]rout, 2)
	for i, prod := range []string{prodA, prodB} {
		wg.Add(1)
		go func(i int, prod string) {
			defer wg.Done()
			store := NewDevices(f.pool, 5*time.Second)
			mapping, err := store.CreateProductMapping(context.Background(), "website", prod, "940")
			outs[i].mapping, outs[i].err = mapping, err
		}(i, prod)
	}
	wg.Wait()
	wins := 0
	for _, o := range outs {
		if o.err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("exactly one external owner: %+v", outs)
	}
	var owner, ownerStore string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT product_id::text, COALESCE(store_id::text,'NULL') FROM commerce_product_mappings WHERE provider_key='website' AND external_product_id='940'`).Scan(&owner, &ownerStore); err != nil {
		t.Fatal(err)
	}
	if ownerStore != scopeStoreA && ownerStore != scopeStoreB {
		t.Fatalf("proven owner store: %s", ownerStore)
	}
	if (owner == prodA && ownerStore != scopeStoreA) || (owner == prodB && ownerStore != scopeStoreB) {
		t.Fatalf("owner consistency: %s %s", owner, ownerStore)
	}
}
