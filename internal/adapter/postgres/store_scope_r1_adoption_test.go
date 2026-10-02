package postgres

// Phase 9-R1 F02 regression: a Store adoption must validate the complete
// affected ownership relationship (incoming dependencies, child edges,
// referencing products, attached tags) before committing, and availability
// / publication must never consume a foreign Store's stock or policy even
// if a contradictory legacy state already exists.

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

// scopeLegacyProduct creates a legacy NULL category/tag/product triple via
// the unbound device and projects it, returning the IDs.
func scopeLegacyProduct(t *testing.T, f *scopeFixture, base int, prefix string) (root, tag, product string) {
	t.Helper()
	ids := catalogIDs(t, base, prefix+"-root", prefix+"-tag", prefix+"-prod")
	root, tag, product = ids[prefix+"-root"], ids[prefix+"-tag"], ids[prefix+"-prod"]
	names := map[string]string{"ar": prefix}
	for i, tc := range []struct {
		event, typ, payload string
	}{
		{fmt.Sprintf("e%07x-0000-4000-8000-000000000001", base), catalog.EventCategorySnapshotV1, categoryPayload(root, "active", names, nil, 1)},
		{fmt.Sprintf("e%07x-0000-4000-8000-000000000002", base), catalog.EventTagSnapshotV1, tagPayload(tag, prefix+"-tag", true, names, 1)},
		{fmt.Sprintf("e%07x-0000-4000-8000-000000000003", base), catalog.EventProductSnapshotV1, productPayload(product, prefix+"-SKU", prefix, root, nil, []string{tag}, 1)},
	} {
		f.ingest(t, f.devC, f.credC, tc.event, tc.typ, tc.payload)
		requireOutcome(t, f.projectCatalog(t, tc.event, tc.typ), catalog.OutcomeProcessed, "")
		_ = i
	}
	return root, tag, product
}

// TestScope_ProductAdoptionBlockedByForeignDependency reproduces the F02
// repro: a legacy Product backed by Store A inventory and policy may not be
// adopted by Store B; availability never exposes A's stock for it. A
// matching Store A adoption still succeeds.
func TestScope_ProductAdoptionBlockedByForeignDependency(t *testing.T) {
	f := openScopeFixture(t)
	rootC, tagC, prodP := scopeLegacyProduct(t, f, 400, "f02prod")
	names := map[string]string{"ar": "f02"}

	f.ingest(t, f.devA, f.credA, "e0000400-0000-4000-8000-000000000004",
		catalog.EventInventoryProductSnapshotV1, inventoryPayload(prodP, 1, 20))
	f.ingest(t, f.devA, f.credA, "e0000400-0000-4000-8000-000000000005",
		catalog.EventProductSalesPolicySnapshotV1, policyPayload(prodP, 1, true, true, nil))
	requireOutcome(t, f.projectCatalog(t, "e0000400-0000-4000-8000-000000000004", catalog.EventInventoryProductSnapshotV1), catalog.OutcomeProcessed, "")
	requireOutcome(t, f.projectCatalog(t, "e0000400-0000-4000-8000-000000000005", catalog.EventProductSalesPolicySnapshotV1), catalog.OutcomeProcessed, "")

	// Store B adopts with a higher revision: must fail, state untouched.
	f.ingest(t, f.devB, f.credB, "e0000400-0000-4000-8000-000000000006",
		catalog.EventProductSnapshotV1, productPayload(prodP, "f02prod-SKU", "F02 B", rootC, nil, []string{tagC}, 5))
	res := f.projectCatalog(t, "e0000400-0000-4000-8000-000000000006", catalog.EventProductSnapshotV1)
	requireOutcome(t, res, catalog.OutcomeBlocked, ErrStoreScopeConflict)

	if got := f.rowStore(t, "catalog_products", "product_id", prodP); got != nil {
		t.Fatalf("product must stay legacy NULL, got %v", got)
	}
	if got := f.rowStore(t, "catalog_product_inventory", "product_id", prodP); got == nil || *got != scopeStoreA {
		t.Fatalf("inventory stays A: %v", got)
	}
	if got := f.rowStore(t, "catalog_product_sales_policies", "product_id", prodP); got == nil || *got != scopeStoreA {
		t.Fatalf("policy stays A: %v", got)
	}

	// Defensive boundary: availability must not expose A's proven stock for
	// a product not owned by A.
	devices := NewDevices(f.pool, 5*time.Second)
	av, err := devices.CatalogProductAvailability(context.Background(), prodP)
	if err != nil {
		t.Fatal(err)
	}
	if av.OnlineAvailable != 0 || av.Ready {
		t.Fatalf("legacy product must not consume foreign stock: %+v", av)
	}
	// Publication enumeration for A must not list a product whose owning
	// product row is not A.
	listed, err := devices.CatalogOnlineConfiguredProductsForStore(context.Background(), scopeStoreA, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range listed {
		if id == prodP {
			t.Fatalf("contradictory product published for A: %v", listed)
		}
	}

	// Matching adoption positive control: Store A may continue the aggregate.
	f.ingest(t, f.devA, f.credA, "e0000400-0000-4000-8000-000000000007",
		catalog.EventProductSnapshotV1, productPayload(prodP, "f02prod-SKU", "F02 A", rootC, nil, []string{tagC}, 6))
	requireOutcome(t, f.projectCatalog(t, "e0000400-0000-4000-8000-000000000007", catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")
	if got := f.rowStore(t, "catalog_products", "product_id", prodP); got == nil || *got != scopeStoreA {
		t.Fatalf("matching adoption succeeds: %v", got)
	}
	av, err = devices.CatalogProductAvailability(context.Background(), prodP)
	if err != nil {
		t.Fatal(err)
	}
	if av.OnlineAvailable != 20 || !av.Ready {
		t.Fatalf("matching Store availability: %+v", av)
	}
	_ = names
}

// TestScope_ProductAdoptionBlockedByForeignMapping proves the incoming
// provider-mapping dependency also gates a Product Store adoption.
func TestScope_ProductAdoptionBlockedByForeignMapping(t *testing.T) {
	f := openScopeFixture(t)
	rootC, tagC, prodP := scopeLegacyProduct(t, f, 410, "f02map")
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO commerce_product_mappings (provider_key, product_id, external_product_id, store_id)
		VALUES ('website', $1, 'ext-f02-map', $2)`, prodP, scopeStoreA); err != nil {
		t.Fatal(err)
	}
	f.ingest(t, f.devB, f.credB, "e0000410-0000-4000-8000-000000000004",
		catalog.EventProductSnapshotV1, productPayload(prodP, "f02map-SKU", "F02 Map B", rootC, nil, []string{tagC}, 3))
	requireOutcome(t, f.projectCatalog(t, "e0000410-0000-4000-8000-000000000004", catalog.EventProductSnapshotV1), catalog.OutcomeBlocked, ErrStoreScopeConflict)
	if got := f.rowStore(t, "catalog_products", "product_id", prodP); got != nil {
		t.Fatalf("product must stay legacy NULL: %v", got)
	}
	var mappingStore *string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT store_id::text FROM commerce_product_mappings WHERE product_id=$1`, prodP).Scan(&mappingStore); err != nil {
		t.Fatal(err)
	}
	if mappingStore == nil || *mappingStore != scopeStoreA {
		t.Fatalf("mapping stays A: %v", mappingStore)
	}
}

// TestScope_CategoryAdoptionBlockedByChildEdge reproduces "legacy parent
// with A child": the legacy parent may not adopt B while an A child edge
// survives.
func TestScope_CategoryAdoptionBlockedByChildEdge(t *testing.T) {
	f := openScopeFixture(t)
	ids := catalogIDs(t, 420, "parent-c", "child-a")
	parentC, childA := ids["parent-c"], ids["child-a"]
	names := map[string]string{"ar": "f02cat"}

	f.ingest(t, f.devC, f.credC, "e0000420-0000-4000-8000-000000000001",
		catalog.EventCategorySnapshotV1, categoryPayload(parentC, "active", names, nil, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000420-0000-4000-8000-000000000001", catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")

	// A child under the legacy NULL parent is allowed (NULL is a wildcard).
	f.ingest(t, f.devA, f.credA, "e0000420-0000-4000-8000-000000000002",
		catalog.EventCategorySnapshotV1, categoryPayload(childA, "active", names, []string{parentC}, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000420-0000-4000-8000-000000000002", catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
	if got := f.rowStore(t, "catalog_categories", "category_id", childA); got == nil || *got != scopeStoreA {
		t.Fatalf("child owned by A: %v", got)
	}

	// B adopts the parent: blocked by the A child edge.
	f.ingest(t, f.devB, f.credB, "e0000420-0000-4000-8000-000000000003",
		catalog.EventCategorySnapshotV1, categoryPayload(parentC, "active", names, nil, 2))
	requireOutcome(t, f.projectCatalog(t, "e0000420-0000-4000-8000-000000000003", catalog.EventCategorySnapshotV1), catalog.OutcomeBlocked, ErrStoreScopeConflict)
	if got := f.rowStore(t, "catalog_categories", "category_id", parentC); got != nil {
		t.Fatalf("parent stays legacy NULL: %v", got)
	}
	var edges int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM catalog_category_edges WHERE parent_id=$1 AND child_id=$2`, parentC, childA).Scan(&edges); err != nil || edges != 1 {
		t.Fatalf("edge unchanged: %d (%v)", edges, err)
	}
}

// TestScope_CategoryAdoptionBlockedByReferencingProduct reproduces "legacy
// Category assigned to A Product": the category may not adopt B while an A
// product references it.
func TestScope_CategoryAdoptionBlockedByReferencingProduct(t *testing.T) {
	f := openScopeFixture(t)
	ids := catalogIDs(t, 440, "cat-c", "tag-c", "prod-a")
	catC, tagC, prodA := ids["cat-c"], ids["tag-c"], ids["prod-a"]
	names := map[string]string{"ar": "f02catp"}
	f.ingest(t, f.devC, f.credC, "e0000440-0000-4000-8000-000000000001",
		catalog.EventCategorySnapshotV1, categoryPayload(catC, "active", names, nil, 1))
	f.ingest(t, f.devC, f.credC, "e0000440-0000-4000-8000-000000000002",
		catalog.EventTagSnapshotV1, tagPayload(tagC, "f02catp-tag", true, names, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000440-0000-4000-8000-000000000001", catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
	requireOutcome(t, f.projectCatalog(t, "e0000440-0000-4000-8000-000000000002", catalog.EventTagSnapshotV1), catalog.OutcomeProcessed, "")

	// A product referencing the legacy category projects under A.
	f.ingest(t, f.devA, f.credA, "e0000440-0000-4000-8000-000000000003",
		catalog.EventProductSnapshotV1, productPayload(prodA, "f02catp-SKU", "P", catC, nil, []string{tagC}, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000440-0000-4000-8000-000000000003", catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")
	if got := f.rowStore(t, "catalog_products", "product_id", prodA); got == nil || *got != scopeStoreA {
		t.Fatalf("product owned by A: %v", got)
	}

	f.ingest(t, f.devB, f.credB, "e0000440-0000-4000-8000-000000000004",
		catalog.EventCategorySnapshotV1, categoryPayload(catC, "active", names, nil, 2))
	requireOutcome(t, f.projectCatalog(t, "e0000440-0000-4000-8000-000000000004", catalog.EventCategorySnapshotV1), catalog.OutcomeBlocked, ErrStoreScopeConflict)
	if got := f.rowStore(t, "catalog_categories", "category_id", catC); got != nil {
		t.Fatalf("category stays legacy NULL: %v", got)
	}
}

// TestScope_TagAdoptionBlockedByAttachedProduct reproduces "legacy Tag
// attached to a B Product": the tag may not adopt A while the B product
// remains attached.
func TestScope_TagAdoptionBlockedByAttachedProduct(t *testing.T) {
	f := openScopeFixture(t)
	ids := catalogIDs(t, 460, "root-b", "tag-c", "prod-b")
	rootB, tagC, prodB := ids["root-b"], ids["tag-c"], ids["prod-b"]
	names := map[string]string{"ar": "f02tag"}
	f.ingest(t, f.devB, f.credB, "e0000460-0000-4000-8000-000000000001",
		catalog.EventCategorySnapshotV1, categoryPayload(rootB, "active", names, nil, 1))
	f.ingest(t, f.devC, f.credC, "e0000460-0000-4000-8000-000000000002",
		catalog.EventTagSnapshotV1, tagPayload(tagC, "f02tag", true, names, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000460-0000-4000-8000-000000000001", catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
	requireOutcome(t, f.projectCatalog(t, "e0000460-0000-4000-8000-000000000002", catalog.EventTagSnapshotV1), catalog.OutcomeProcessed, "")

	f.ingest(t, f.devB, f.credB, "e0000460-0000-4000-8000-000000000003",
		catalog.EventProductSnapshotV1, productPayload(prodB, "f02tag-SKU", "P", rootB, nil, []string{tagC}, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000460-0000-4000-8000-000000000003", catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")
	if got := f.rowStore(t, "catalog_products", "product_id", prodB); got == nil || *got != scopeStoreB {
		t.Fatalf("product owned by B: %v", got)
	}

	f.ingest(t, f.devA, f.credA, "e0000460-0000-4000-8000-000000000004",
		catalog.EventTagSnapshotV1, tagPayload(tagC, "f02tag", true, names, 2))
	requireOutcome(t, f.projectCatalog(t, "e0000460-0000-4000-8000-000000000004", catalog.EventTagSnapshotV1), catalog.OutcomeBlocked, ErrStoreScopeConflict)
	if got := f.rowStore(t, "catalog_tags", "tag_id", tagC); got != nil {
		t.Fatalf("tag stays legacy NULL: %v", got)
	}
	var links int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM catalog_product_tags WHERE product_id=$1 AND tag_id=$2`, prodB, tagC).Scan(&links); err != nil || links != 1 {
		t.Fatalf("product/tag edge unchanged: %d (%v)", links, err)
	}
}

// TestScope_AdoptionRacingDependencyCreation proves product adoption and a
// concurrent foreign inventory write cannot both commit: no durable state
// may end with a Store B product backed by Store A inventory.
func TestScope_AdoptionRacingDependencyCreation(t *testing.T) {
	f := openScopeFixture(t)
	rootC, tagC, prodP := scopeLegacyProduct(t, f, 480, "f02race")

	// Store B product adoption and Store A inventory creation, concurrently.
	f.ingest(t, f.devB, f.credB, "e0000480-0000-4000-8000-000000000004",
		catalog.EventProductSnapshotV1, productPayload(prodP, "f02race-SKU", "B", rootC, nil, []string{tagC}, 2))
	f.ingest(t, f.devA, f.credA, "e0000480-0000-4000-8000-000000000005",
		catalog.EventInventoryProductSnapshotV1, inventoryPayload(prodP, 1, 20))

	var wg sync.WaitGroup
	for _, tc := range []struct {
		event string
		typ   string
	}{
		{"e0000480-0000-4000-8000-000000000004", catalog.EventProductSnapshotV1},
		{"e0000480-0000-4000-8000-000000000005", catalog.EventInventoryProductSnapshotV1},
	} {
		wg.Add(1)
		go func(event, typ string) {
			defer wg.Done()
			for attempt := 0; attempt < 30; attempt++ {
				expireBackoff(t, f, event)
				store := NewDevices(f.pool, 5*time.Second)
				rec, ok, err := store.LoadCatalogEvent(context.Background(), event)
				if err != nil || !ok {
					t.Errorf("load %s: %v %v", event, ok, err)
					return
				}
				var res catalog.ProjectResult
				switch typ {
				case catalog.EventProductSnapshotV1:
					res, err = store.ProjectProduct(context.Background(), rec, time.Now())
				case catalog.EventInventoryProductSnapshotV1:
					res, err = store.ProjectProductInventory(context.Background(), rec, time.Now())
				}
				if err == nil && res.Outcome != catalog.OutcomeNotDue && res.Outcome != catalog.OutcomeRetryable {
					return
				}
				time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
			}
			t.Errorf("project %s never verdict", event)
		}(tc.event, tc.typ)
	}
	wg.Wait()

	product := f.rowStore(t, "catalog_products", "product_id", prodP)
	inventory := f.rowStore(t, "catalog_product_inventory", "product_id", prodP)
	if product != nil && *product == scopeStoreB && inventory != nil && *inventory == scopeStoreA {
		t.Fatalf("contradiction committed: product=B inventory=A")
	}
	// Any committed pair must agree (or the inventory is absent).
	if product != nil && inventory != nil && *product != *inventory {
		t.Fatalf("contradictory ownership: product=%v inventory=%v", *product, *inventory)
	}
}

// TestScope_ExistingContradictionAvailability proves a pre-existing
// contradictory durable state (product B with A inventory/policy, created
// outside the projector) cannot expose foreign stock or policy through the
// read boundary.
func TestScope_ExistingContradictionAvailability(t *testing.T) {
	f := openScopeFixture(t)
	_, _, prodP := scopeLegacyProduct(t, f, 500, "f02legacy")
	ctx := context.Background()
	// Legacy product backed by Store A inventory/policy, then force the
	// ownership annotation to B (simulating a pre-fix contradictory state).
	f.ingest(t, f.devA, f.credA, "e0000500-0000-4000-8000-000000000004",
		catalog.EventInventoryProductSnapshotV1, inventoryPayload(prodP, 1, 20))
	f.ingest(t, f.devA, f.credA, "e0000500-0000-4000-8000-000000000005",
		catalog.EventProductSalesPolicySnapshotV1, policyPayload(prodP, 1, true, true, nil))
	requireOutcome(t, f.projectCatalog(t, "e0000500-0000-4000-8000-000000000004", catalog.EventInventoryProductSnapshotV1), catalog.OutcomeProcessed, "")
	requireOutcome(t, f.projectCatalog(t, "e0000500-0000-4000-8000-000000000005", catalog.EventProductSalesPolicySnapshotV1), catalog.OutcomeProcessed, "")
	if _, err := f.pool.Exec(ctx,
		`UPDATE catalog_products SET store_id=$1 WHERE product_id=$2`, scopeStoreB, prodP); err != nil {
		t.Fatal(err)
	}

	devices := NewDevices(f.pool, 5*time.Second)
	av, err := devices.CatalogProductAvailability(ctx, prodP)
	if err != nil {
		t.Fatal(err)
	}
	if av.OnlineAvailable != 0 || av.Ready {
		t.Fatalf("contradictory state must not expose foreign stock: %+v", av)
	}
	listed, err := devices.CatalogOnlineConfiguredProductsForStore(ctx, scopeStoreA, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range listed {
		if id == prodP {
			t.Fatalf("contradictory policy published for A: %v", listed)
		}
	}
}
