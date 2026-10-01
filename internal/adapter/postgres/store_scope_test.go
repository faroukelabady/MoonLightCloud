package postgres

// Phase 9B store-scoped projection isolation: every scoped projection
// family is exercised with two bound Stores (A/B) plus legacy NULL
// traffic on real PostgreSQL. Ownership always comes from the event's
// own ingress context (sync_events.store_id); projectors never consult
// current bindings, payloads, or lookups for scope.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/returnrefund"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
	"github.com/faroukelabady/MoonLightCloud/internal/store"
	isync "github.com/faroukelabady/MoonLightCloud/internal/sync"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	scopeStoreA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	scopeStoreB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

type scopeFixture struct {
	pool    *pgxpool.Pool
	syncSvc isync.Service
	devA    string
	credA   string
	devB    string
	credB   string
	devC    string
	credC   string
}

func openScopeFixture(t *testing.T) *scopeFixture {
	t.Helper()
	pool, authSvc := openTestRepo(t)
	ctx := context.Background()
	mkDevice := func(name string) (string, string) {
		t.Helper()
		p, err := authSvc.Create(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		return p.Device.ID, p.Credential.ID
	}
	devA, credA := mkDevice("scope-dev-a")
	devB, credB := mkDevice("scope-dev-b")
	devC, credC := mkDevice("scope-dev-legacy")
	devices := NewDevices(pool, 5*time.Second)
	if _, err := devices.RegisterStore(ctx, devA, store.RegistrationRequest{
		StoreID: scopeStoreA, DisplayName: "Store A", Timezone: "Africa/Cairo",
	}); err != nil {
		t.Fatalf("bind A: %v", err)
	}
	if _, err := devices.RegisterStore(ctx, devB, store.RegistrationRequest{
		StoreID: scopeStoreB, DisplayName: "Store B", Timezone: "Africa/Cairo",
	}); err != nil {
		t.Fatalf("bind B: %v", err)
	}
	return &scopeFixture{
		pool: pool, syncSvc: isync.NewService(devices, clock.System{}),
		devA: devA, credA: credA, devB: devB, credB: credB, devC: devC, credC: credC,
	}
}

func (f *scopeFixture) ingest(t *testing.T, devID, credID, eventID, eventType, payload string) {
	t.Helper()
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":%q,"occurred_at":"2026-09-20T10:00:00Z","payload":%s}]}`,
		eventID, eventType, payload)
	res, err := f.syncSvc.Ingest(context.Background(), devID, credID, []byte(body))
	if err != nil {
		t.Fatalf("ingest %s: %v", eventID, err)
	}
	if res.Events[0].Status != "accepted" {
		t.Fatalf("want accepted, got %+v", res)
	}
}

func (f *scopeFixture) projectCatalog(t *testing.T, eventID, eventType string) catalog.ProjectResult {
	t.Helper()
	store := NewDevices(f.pool, 5*time.Second)
	rec, ok, err := store.LoadCatalogEvent(context.Background(), eventID)
	if err != nil || !ok {
		t.Fatalf("load %s: %v %v", eventID, ok, err)
	}
	now := time.Now()
	switch eventType {
	case catalog.EventCategorySnapshotV1:
		res, err := store.ProjectCategory(context.Background(), rec, now)
		if err != nil {
			t.Fatalf("project category: %v", err)
		}
		return res
	case catalog.EventTagSnapshotV1:
		res, err := store.ProjectTag(context.Background(), rec, now)
		if err != nil {
			t.Fatalf("project tag: %v", err)
		}
		return res
	case catalog.EventProductSnapshotV1:
		res, err := store.ProjectProduct(context.Background(), rec, now)
		if err != nil {
			t.Fatalf("project product: %v", err)
		}
		return res
	case catalog.EventProductSalesPolicySnapshotV1:
		res, err := store.ProjectProductSalesPolicy(context.Background(), rec, now)
		if err != nil {
			t.Fatalf("project policy: %v", err)
		}
		return res
	case catalog.EventInventoryProductSnapshotV1:
		res, err := store.ProjectProductInventory(context.Background(), rec, now)
		if err != nil {
			t.Fatalf("project inventory: %v", err)
		}
		return res
	default:
		t.Fatalf("unknown catalog event %s", eventType)
		return catalog.ProjectResult{}
	}
}

func (f *scopeFixture) projectSale(t *testing.T, eventID string) sale.ProjectResult {
	t.Helper()
	store := NewDevices(f.pool, 5*time.Second)
	rec, ok, err := store.LoadSaleEvent(context.Background(), eventID)
	if err != nil || !ok {
		t.Fatalf("load sale %s: %v %v", eventID, ok, err)
	}
	res, err := store.ProjectSale(context.Background(), rec, time.Now())
	if err != nil {
		t.Fatalf("project sale: %v", err)
	}
	return res
}

func (f *scopeFixture) projectSaleV2(t *testing.T, eventID string) sale.ProjectResult {
	t.Helper()
	store := NewDevices(f.pool, 5*time.Second)
	rec, ok, err := store.LoadSaleEvent(context.Background(), eventID)
	if err != nil || !ok {
		t.Fatalf("load sale %s: %v %v", eventID, ok, err)
	}
	res, err := store.ProjectSaleV2(context.Background(), rec, time.Now())
	if err != nil {
		t.Fatalf("project sale v2: %v", err)
	}
	return res
}

func (f *scopeFixture) projectReturn(t *testing.T, eventID string) returnrefund.ProjectResult {
	t.Helper()
	store := NewDevices(f.pool, 5*time.Second)
	rec, ok, err := store.LoadReturnEvent(context.Background(), eventID)
	if err != nil || !ok {
		t.Fatalf("load return %s: %v %v", eventID, ok, err)
	}
	res, err := store.ProjectReturn(context.Background(), rec, time.Now())
	if err != nil {
		t.Fatalf("project return: %v", err)
	}
	return res
}

// rowStore reads a projection root's store_id (nil = unscoped legacy).
func (f *scopeFixture) rowStore(t *testing.T, table, idCol, id string) *string {
	t.Helper()
	var raw *string
	if err := f.pool.QueryRow(context.Background(),
		fmt.Sprintf(`SELECT store_id::text FROM %s WHERE %s = $1`, table, idCol), id).Scan(&raw); err != nil {
		t.Fatalf("row store %s/%s: %v", table, id, err)
	}
	return raw
}

func requireOutcome(t *testing.T, res catalog.ProjectResult, outcome catalog.Outcome, code string) {
	t.Helper()
	if res.Outcome != outcome || (code != "" && res.ErrorCode != code) {
		t.Fatalf("want outcome=%v code=%q, got %+v", outcome, code, res)
	}
}

// scopeGraph builds one root category + tag per side. Legacy (device C)
// rows stay NULL; scoped rows adopt their ingress Store.
func (f *scopeFixture) scopeGraph(t *testing.T, base int, prefix string) (rootA, tagA, rootB, tagB string) {
	t.Helper()
	ids := catalogIDs(t, base, prefix+"-root-a", prefix+"-tag-a", prefix+"-root-b", prefix+"-tag-b")
	rootA, tagA, rootB, tagB = ids[prefix+"-root-a"], ids[prefix+"-tag-a"], ids[prefix+"-root-b"], ids[prefix+"-tag-b"]
	names := func(n string) map[string]string { return map[string]string{"ar": n} }
	f.ingest(t, f.devA, f.credA, fmt.Sprintf("e%07x-0000-4000-8000-000000000001", base),
		catalog.EventCategorySnapshotV1, categoryPayload(rootA, "active", names("root A"), nil, 1))
	f.ingest(t, f.devA, f.credA, fmt.Sprintf("e%07x-0000-4000-8000-000000000002", base),
		catalog.EventTagSnapshotV1, tagPayload(tagA, prefix+"-shared", true, names("tag A"), 1))
	f.ingest(t, f.devB, f.credB, fmt.Sprintf("e%07x-0000-4000-8000-000000000003", base),
		catalog.EventCategorySnapshotV1, categoryPayload(rootB, "active", names("root B"), nil, 1))
	f.ingest(t, f.devB, f.credB, fmt.Sprintf("e%07x-0000-4000-8000-000000000004", base),
		catalog.EventTagSnapshotV1, tagPayload(tagB, prefix+"-shared", true, names("tag B"), 1))
	for i, tc := range []struct {
		event, typ string
	}{
		{fmt.Sprintf("e%07x-0000-4000-8000-000000000001", base), catalog.EventCategorySnapshotV1},
		{fmt.Sprintf("e%07x-0000-4000-8000-000000000002", base), catalog.EventTagSnapshotV1},
		{fmt.Sprintf("e%07x-0000-4000-8000-000000000003", base), catalog.EventCategorySnapshotV1},
		{fmt.Sprintf("e%07x-0000-4000-8000-000000000004", base), catalog.EventTagSnapshotV1},
	} {
		res := f.projectCatalog(t, tc.event, tc.typ)
		if res.Outcome != catalog.OutcomeProcessed && res.Outcome != catalog.OutcomeAlready {
			t.Fatalf("graph event %d: %+v", i, res)
		}
	}
	return rootA, tagA, rootB, tagB
}

// TestScope_SameSKUCoexists proves the Product matrix core: identical SKU
// in two Stores projects twice, with independent ownership.
func TestScope_SameSKUCoexists(t *testing.T) {
	f := openScopeFixture(t)
	rootA, tagA, rootB, tagB := f.scopeGraph(t, 100, "sku")
	ids := catalogIDs(t, 110, "prod-a", "prod-b")
	prodA, prodB := ids["prod-a"], ids["prod-b"]
	f.ingest(t, f.devA, f.credA, "e0000110-0000-4000-8000-000000000001",
		catalog.EventProductSnapshotV1, productPayload(prodA, "ML-001", "Horse A", rootA, nil, []string{tagA}, 1))
	f.ingest(t, f.devB, f.credB, "e0000110-0000-4000-8000-000000000002",
		catalog.EventProductSnapshotV1, productPayload(prodB, "ML-001", "Horse B", rootB, nil, []string{tagB}, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000110-0000-4000-8000-000000000001", catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")
	requireOutcome(t, f.projectCatalog(t, "e0000110-0000-4000-8000-000000000002", catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")
	if got := f.rowStore(t, "catalog_products", "product_id", prodA); got == nil || *got != scopeStoreA {
		t.Fatalf("prodA store: %v", got)
	}
	if got := f.rowStore(t, "catalog_products", "product_id", prodB); got == nil || *got != scopeStoreB {
		t.Fatalf("prodB store: %v", got)
	}
}

// TestScope_SameStoreSKUConflict keeps same-Store SKU uniqueness: a
// second product reusing Store A's SKU blocks without touching the first.
func TestScope_SameStoreSKUConflict(t *testing.T) {
	f := openScopeFixture(t)
	rootA, tagA, _, _ := f.scopeGraph(t, 120, "dup")
	ids := catalogIDs(t, 130, "first", "second")
	f.ingest(t, f.devA, f.credA, "e0000130-0000-4000-8000-000000000001",
		catalog.EventProductSnapshotV1, productPayload(ids["first"], "DUP-1", "First", rootA, nil, []string{tagA}, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000130-0000-4000-8000-000000000001", catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")
	f.ingest(t, f.devA, f.credA, "e0000130-0000-4000-8000-000000000002",
		catalog.EventProductSnapshotV1, productPayload(ids["second"], "DUP-1", "Second", rootA, nil, []string{tagA}, 1))
	res := f.projectCatalog(t, "e0000130-0000-4000-8000-000000000002", catalog.EventProductSnapshotV1)
	if res.Outcome != catalog.OutcomeBlocked {
		t.Fatalf("same-store SKU reuse must block: %+v", res)
	}
	if got := f.rowStore(t, "catalog_products", "product_id", ids["first"]); got == nil || *got != scopeStoreA {
		t.Fatalf("first product intact: %v", got)
	}
	var count int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM catalog_products WHERE sku='DUP-1'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("exactly one DUP-1 row: %d (%v)", count, err)
	}
}

// TestScope_ProductAdoptionAndConflict proves the legacy current-state
// adoption rule: a NULL row adopts Store A by aggregate-ID + revision
// continuity, then a Store B event for the same aggregate conflicts
// instead of overwriting.
func TestScope_ProductAdoptionAndConflict(t *testing.T) {
	f := openScopeFixture(t)
	ids := catalogIDs(t, 140, "root-c", "tag-c", "prod-c")
	rootC, tagC, prodC := ids["root-c"], ids["tag-c"], ids["prod-c"]
	names := map[string]string{"ar": "c"}
	// Legacy NULL baseline via the unbound device.
	f.ingest(t, f.devC, f.credC, "e0000140-0000-4000-8000-000000000001",
		catalog.EventCategorySnapshotV1, categoryPayload(rootC, "active", names, nil, 1))
	f.ingest(t, f.devC, f.credC, "e0000140-0000-4000-8000-000000000002",
		catalog.EventTagSnapshotV1, tagPayload(tagC, "adopt-tag", true, names, 1))
	f.ingest(t, f.devC, f.credC, "e0000140-0000-4000-8000-000000000003",
		catalog.EventProductSnapshotV1, productPayload(prodC, "ADOPT-1", "Adopt", rootC, nil, []string{tagC}, 1))
	for _, tc := range []struct{ event, typ string }{
		{"e0000140-0000-4000-8000-000000000001", catalog.EventCategorySnapshotV1},
		{"e0000140-0000-4000-8000-000000000002", catalog.EventTagSnapshotV1},
		{"e0000140-0000-4000-8000-000000000003", catalog.EventProductSnapshotV1},
	} {
		requireOutcome(t, f.projectCatalog(t, tc.event, tc.typ), catalog.OutcomeProcessed, "")
	}
	if got := f.rowStore(t, "catalog_products", "product_id", prodC); got != nil {
		t.Fatalf("legacy row stays NULL: %v", got)
	}
	// Store A continues the aggregate: safe adoption.
	f.ingest(t, f.devA, f.credA, "e0000140-0000-4000-8000-000000000004",
		catalog.EventProductSnapshotV1, productPayload(prodC, "ADOPT-1", "Adopt A", rootC, nil, []string{tagC}, 2))
	requireOutcome(t, f.projectCatalog(t, "e0000140-0000-4000-8000-000000000004", catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")
	if got := f.rowStore(t, "catalog_products", "product_id", prodC); got == nil || *got != scopeStoreA {
		t.Fatalf("adopted to A: %v", got)
	}
	// Store B attempts the same aggregate: permanent conflict, A intact.
	f.ingest(t, f.devB, f.credB, "e0000140-0000-4000-8000-000000000005",
		catalog.EventProductSnapshotV1, productPayload(prodC, "ADOPT-1", "Adopt B", rootC, nil, []string{tagC}, 3))
	res := f.projectCatalog(t, "e0000140-0000-4000-8000-000000000005", catalog.EventProductSnapshotV1)
	requireOutcome(t, res, catalog.OutcomeBlocked, ErrStoreScopeConflict)
	if got := f.rowStore(t, "catalog_products", "product_id", prodC); got == nil || *got != scopeStoreA {
		t.Fatalf("A ownership intact: %v", got)
	}
	var name string
	var rev int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT name, source_revision FROM catalog_products WHERE product_id=$1`, prodC).Scan(&name, &rev); err != nil {
		t.Fatal(err)
	}
	if name != "Adopt A" || rev != 2 {
		t.Fatalf("B write left no trace: %q rev %d", name, rev)
	}
}

// TestScope_CategoryDAGIsolation proves same-ID categories conflict
// across Stores, same names coexist under different IDs, and cross-Store
// edges are impossible.
func TestScope_CategoryDAGIsolation(t *testing.T) {
	f := openScopeFixture(t)
	ids := catalogIDs(t, 150, "shared", "a-only", "b-only", "b-child")
	shared, aOnly, bOnly, bChild := ids["shared"], ids["a-only"], ids["b-only"], ids["b-child"]
	names := func(n string) map[string]string { return map[string]string{"ar": n} }
	f.ingest(t, f.devA, f.credA, "e0000150-0000-4000-8000-000000000001",
		catalog.EventCategorySnapshotV1, categoryPayload(shared, "active", names("Shared"), nil, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000150-0000-4000-8000-000000000001", catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
	// Same category ID from Store B: conflict, A graph untouched.
	f.ingest(t, f.devB, f.credB, "e0000150-0000-4000-8000-000000000002",
		catalog.EventCategorySnapshotV1, categoryPayload(shared, "active", names("Shared B"), nil, 1))
	res := f.projectCatalog(t, "e0000150-0000-4000-8000-000000000002", catalog.EventCategorySnapshotV1)
	requireOutcome(t, res, catalog.OutcomeBlocked, ErrStoreScopeConflict)
	if got := f.rowStore(t, "catalog_categories", "category_id", shared); got == nil || *got != scopeStoreA {
		t.Fatalf("shared node stays A: %v", got)
	}
	// Independent same-name categories coexist.
	f.ingest(t, f.devA, f.credA, "e0000150-0000-4000-8000-000000000003",
		catalog.EventCategorySnapshotV1, categoryPayload(aOnly, "active", names("Cats"), []string{shared}, 1))
	f.ingest(t, f.devB, f.credB, "e0000150-0000-4000-8000-000000000004",
		catalog.EventCategorySnapshotV1, categoryPayload(bOnly, "active", names("Cats"), nil, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000150-0000-4000-8000-000000000003", catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
	requireOutcome(t, f.projectCatalog(t, "e0000150-0000-4000-8000-000000000004", catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
	// Cross-Store edge (B child under A parent) is impossible.
	f.ingest(t, f.devB, f.credB, "e0000150-0000-4000-8000-000000000005",
		catalog.EventCategorySnapshotV1, categoryPayload(bChild, "active", names("B child"), []string{shared}, 1))
	res = f.projectCatalog(t, "e0000150-0000-4000-8000-000000000005", catalog.EventCategorySnapshotV1)
	requireOutcome(t, res, catalog.OutcomeBlocked, ErrStoreScopeConflict)
	var edges int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM catalog_category_edges WHERE child_id=$1`, bChild).Scan(&edges); err != nil || edges != 0 {
		t.Fatalf("no cross-store edge: %d (%v)", edges, err)
	}
}

// TestScope_ProductTagIsolation proves a product cannot attach another
// Store's tag.
func TestScope_ProductTagIsolation(t *testing.T) {
	f := openScopeFixture(t)
	rootA, tagA, _, tagB := f.scopeGraph(t, 160, "pt")
	ids := catalogIDs(t, 170, "prod-x")
	f.ingest(t, f.devA, f.credA, "e0000170-0000-4000-8000-000000000001",
		catalog.EventProductSnapshotV1, productPayload(ids["prod-x"], "PT-1", "X", rootA, nil, []string{tagA, tagB}, 1))
	res := f.projectCatalog(t, "e0000170-0000-4000-8000-000000000001", catalog.EventProductSnapshotV1)
	requireOutcome(t, res, catalog.OutcomeBlocked, ErrStoreScopeConflict)
	var count int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM catalog_products WHERE product_id=$1`, ids["prod-x"]).Scan(&count); err != nil || count != 0 {
		t.Fatalf("no partial product row: %d (%v)", count, err)
	}
}

// TestScope_LegacyEventKeepsAdoptedStore proves COALESCE preservation: a
// legacy event with a higher revision updates business data on an
// adopted row without wiping its Store.
func TestScope_LegacyEventKeepsAdoptedStore(t *testing.T) {
	f := openScopeFixture(t)
	ids := catalogIDs(t, 180, "root-l", "tag-l", "prod-l")
	rootL, tagL, prodL := ids["root-l"], ids["tag-l"], ids["prod-l"]
	names := map[string]string{"ar": "l"}
	for i, tc := range []struct{ event, typ, payload string }{
		{"e0000180-0000-4000-8000-000000000001", catalog.EventCategorySnapshotV1, categoryPayload(rootL, "active", names, nil, 1)},
		{"e0000180-0000-4000-8000-000000000002", catalog.EventTagSnapshotV1, tagPayload(tagL, "keep-tag", true, names, 1)},
		{"e0000180-0000-4000-8000-000000000003", catalog.EventProductSnapshotV1, productPayload(prodL, "KEEP-1", "Keep", rootL, nil, []string{tagL}, 1)},
	} {
		f.ingest(t, f.devC, f.credC, tc.event, tc.typ, tc.payload)
		requireOutcome(t, f.projectCatalog(t, tc.event, tc.typ), catalog.OutcomeProcessed, "")
		_ = i
	}
	f.ingest(t, f.devA, f.credA, "e0000180-0000-4000-8000-000000000004",
		catalog.EventProductSnapshotV1, productPayload(prodL, "KEEP-1", "Keep", rootL, nil, []string{tagL}, 2))
	requireOutcome(t, f.projectCatalog(t, "e0000180-0000-4000-8000-000000000004", catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")
	// Legacy higher revision: business data advances, Store stays A.
	f.ingest(t, f.devC, f.credC, "e0000180-0000-4000-8000-000000000005",
		catalog.EventProductSnapshotV1, productPayload(prodL, "KEEP-1", "Keep legacy", rootL, nil, []string{tagL}, 3))
	requireOutcome(t, f.projectCatalog(t, "e0000180-0000-4000-8000-000000000005", catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")
	if got := f.rowStore(t, "catalog_products", "product_id", prodL); got == nil || *got != scopeStoreA {
		t.Fatalf("adopted store preserved: %v", got)
	}
	var name string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT name FROM catalog_products WHERE product_id=$1`, prodL).Scan(&name); err != nil || name != "Keep legacy" {
		t.Fatalf("arbitration still advances data: %q (%v)", name, err)
	}
}

// TestScope_RevisionRulesPreserved proves Store context is an additional
// invariant, never a replacement for revision ordering.
func TestScope_RevisionRulesPreserved(t *testing.T) {
	f := openScopeFixture(t)
	rootA, tagA, _, _ := f.scopeGraph(t, 190, "rev")
	ids := catalogIDs(t, 195, "prod-r")
	prodR := ids["prod-r"]
	f.ingest(t, f.devA, f.credA, "e0000195-0000-4000-8000-000000000001",
		catalog.EventProductSnapshotV1, productPayload(prodR, "REV-1", "R2", rootA, nil, []string{tagA}, 2))
	requireOutcome(t, f.projectCatalog(t, "e0000195-0000-4000-8000-000000000001", catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")
	// Same Store stale revision: frozen stale no-op.
	f.ingest(t, f.devA, f.credA, "e0000195-0000-4000-8000-000000000002",
		catalog.EventProductSnapshotV1, productPayload(prodR, "REV-1", "R1", rootA, nil, []string{tagA}, 1))
	res := f.projectCatalog(t, "e0000195-0000-4000-8000-000000000002", catalog.EventProductSnapshotV1)
	if res.Outcome != catalog.OutcomeProcessed && res.Outcome != catalog.OutcomeAlready {
		t.Fatalf("stale no-op: %+v", res)
	}
	// Same Store equal revision, divergent state: frozen conflict verdict.
	f.ingest(t, f.devA, f.credA, "e0000195-0000-4000-8000-000000000003",
		catalog.EventProductSnapshotV1, productPayload(prodR, "REV-1", "R2-divergent", rootA, nil, []string{tagA}, 2))
	res = f.projectCatalog(t, "e0000195-0000-4000-8000-000000000003", catalog.EventProductSnapshotV1)
	requireOutcome(t, res, catalog.OutcomeBlocked, ErrCatalogRevisionConflict)
	var name string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT name FROM catalog_products WHERE product_id=$1`, prodR).Scan(&name); err != nil || name != "R2" {
		t.Fatalf("stored revision intact: %q (%v)", name, err)
	}
}
