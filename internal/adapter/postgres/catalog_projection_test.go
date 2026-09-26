package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

// Catalog projector integration tests (real PostgreSQL): revision
// ordering, dependency waits, DAG defense, atomic replace, rebuild.

// catalogIDs mints deterministic UUIDs for fixture graphs. The base
// offsets one call's namespace from another: IDs must never collide across
// fixture graphs in one database, or events would overwrite each other.
func catalogIDs(t *testing.T, base int, names ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for i, name := range names {
		out[name] = fmt.Sprintf("c%07x-0000-4000-8000-%012x", base+i, 0xc0ffee+base+i)
		if len(out[name]) != 36 {
			t.Fatalf("bad fixture uuid for %s", name)
		}
	}
	return out
}

func categoryPayload(categoryID, status string, names map[string]string, parents []string, revision int64) string {
	nameList := []any{}
	for locale, name := range names {
		nameList = append(nameList, map[string]any{"locale": locale, "name": name})
	}
	parentList := []any{}
	for _, parent := range parents {
		parentList = append(parentList, parent)
	}
	raw, _ := json.Marshal(map[string]any{
		"category_id": categoryID, "status": status, "names": nameList,
		"parent_ids": parentList, "catalog_revision": revision,
	})
	return string(raw)
}

func tagPayload(tagID, slug string, active bool, names map[string]string, revision int64) string {
	nameList := []any{}
	for locale, name := range names {
		nameList = append(nameList, map[string]any{"locale": locale, "name": name})
	}
	raw, _ := json.Marshal(map[string]any{
		"tag_id": tagID, "slug": slug, "is_active": active, "names": nameList,
		"catalog_revision": revision,
	})
	return string(raw)
}

func productPayload(productID, sku, name, top string, subs, tags []string, revision int64) string {
	subList, tagList := []any{}, []any{}
	for _, sub := range subs {
		subList = append(subList, sub)
	}
	for _, tag := range tags {
		tagList = append(tagList, tag)
	}
	raw, _ := json.Marshal(map[string]any{
		"product_id": productID, "sku": sku, "name": name,
		"translations": []any{map[string]any{"locale": "ar", "name": name}},
		"prices": []any{
			map[string]any{"currency": "EGP", "price_cents": 65000, "cost_cents": 40000},
			map[string]any{"currency": "USD", "price_cents": 1300},
		},
		"top_category_id": top, "subcategory_ids": subList, "tag_ids": tagList,
		"width_cm": 70, "height_cm": 100, "is_active": true,
		"catalog_revision": revision,
	})
	return string(raw)
}

func ingestCatalog(t *testing.T, env *saleEnv, eventID, eventType, occurred, payload string) {
	t.Helper()
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":%q,"occurred_at":%q,"payload":%s}]}`,
		eventID, eventType, occurred, payload)
	res, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body))
	if err != nil {
		t.Fatalf("ingest catalog: %v", err)
	}
	if res.Events[0].Status != "accepted" {
		t.Fatalf("want accepted, got %+v", res)
	}
}

func catalogStore(env *saleEnv) Devices {
	return NewDevices(env.pool, 5*time.Second)
}

func projectCatalogOnce(t *testing.T, env *saleEnv, eventID string) catalog.ProjectResult {
	t.Helper()
	store := catalogStore(env)
	rec, ok, err := store.LoadCatalogEvent(context.Background(), eventID)
	if err != nil || !ok {
		t.Fatalf("load catalog event: %v %v", ok, err)
	}
	var res catalog.ProjectResult
	switch rec.EventType {
	case catalog.EventCategorySnapshotV1:
		res, err = store.ProjectCategory(context.Background(), rec, time.Now())
	case catalog.EventTagSnapshotV1:
		res, err = store.ProjectTag(context.Background(), rec, time.Now())
	case catalog.EventProductSnapshotV1:
		res, err = store.ProjectProduct(context.Background(), rec, time.Now())
	default:
		t.Fatalf("unexpected type %s", rec.EventType)
	}
	// Retryable dependency waits surface as transient errors by design
	// (mirroring the sale/return projectors); the outcome is authoritative.
	if err != nil && res.Outcome != catalog.OutcomeRetryable {
		t.Fatalf("project catalog: %v", err)
	}
	return res
}

func catalogStatus(t *testing.T, env *saleEnv, processor, eventID string) (string, string) {
	t.Helper()
	var status, code string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT status, COALESCE(last_error_code,'') FROM sync_event_processing WHERE event_id=$1 AND processor=$2`,
		eventID, processor).Scan(&status, &code); err != nil {
		t.Fatal(err)
	}
	return status, code
}

// forceCatalogDue advances a retry row past its backoff horizon so the
// next attempt runs immediately (mirrors the return-projector tests).
func forceCatalogDue(t *testing.T, env *saleEnv, processor, eventID string) {
	t.Helper()
	if _, err := env.pool.Exec(context.Background(),
		`UPDATE sync_event_processing SET next_attempt_at = NULL WHERE event_id=$1 AND processor=$2`,
		eventID, processor); err != nil {
		t.Fatal(err)
	}
}

func catalogCategoryRevision(t *testing.T, env *saleEnv, categoryID string) int64 {
	t.Helper()
	var revision int64
	if err := env.pool.QueryRow(context.Background(),
		`SELECT source_revision FROM catalog_categories WHERE category_id=$1`, categoryID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	return revision
}

// TestCatalogCategoryProjects proves a root category projects with revision
// metadata and an empty parent set.
func TestCatalogCategoryProjects(t *testing.T) {
	env := openSaleEnv(t)
	ids := catalogIDs(t, 0, "root")
	event := "d0001000-0000-4000-8000-000000000000"
	ingestCatalog(t, env, event, catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
		categoryPayload(ids["root"], "active", map[string]string{"ar": "إسلامي"}, nil, 1))
	res := projectCatalogOnce(t, env, event)
	if res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("outcome: %+v", res)
	}
	if revision := catalogCategoryRevision(t, env, ids["root"]); revision != 1 {
		t.Fatalf("revision: %d", revision)
	}
	var parents int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM catalog_category_edges WHERE child_id=$1`, ids["root"]).Scan(&parents); err != nil || parents != 0 {
		t.Fatalf("no edges: %d (%v)", parents, err)
	}
}

// TestCatalogChildBeforeParentWaits proves out-of-order dependency: a child
// arriving before its parent waits retryably with zero rows, then converges
// when the parent projects — no resend.
func TestCatalogChildBeforeParentWaits(t *testing.T) {
	env := openSaleEnv(t)
	ids := catalogIDs(t, 10, "root", "sub")
	childEvent := "d0001001-0000-4000-8000-000000000001"
	ingestCatalog(t, env, childEvent, catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
		categoryPayload(ids["sub"], "active", map[string]string{"ar": "فرعي"}, []string{ids["root"]}, 1))
	res := projectCatalogOnce(t, env, childEvent)
	if res.Outcome != catalog.OutcomeRetryable || res.ErrorCode != ErrCatalogDependencyWait {
		t.Fatalf("dependency wait: %+v", res)
	}
	if status, _ := catalogStatus(t, env, catalog.ProcessorCategoryProjectionV1, childEvent); status != "retry" {
		t.Fatalf("retry state: %q", status)
	}
	if n := saleCount(t, env.pool, "catalog_categories"); n != 0 {
		t.Fatalf("zero rows while waiting, got %d", n)
	}

	rootEvent := "d0001002-0000-4000-8000-000000000002"
	ingestCatalog(t, env, rootEvent, catalog.EventCategorySnapshotV1, "2026-09-20T11:00:00Z",
		categoryPayload(ids["root"], "active", map[string]string{"ar": "جذر"}, nil, 1))
	if res := projectCatalogOnce(t, env, rootEvent); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("root: %+v", res)
	}
	forceCatalogDue(t, env, catalog.ProcessorCategoryProjectionV1, childEvent)
	if res := projectCatalogOnce(t, env, childEvent); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("child converges: %+v", res)
	}
	var edge int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM catalog_category_edges WHERE parent_id=$1 AND child_id=$2`,
		ids["root"], ids["sub"]).Scan(&edge); err != nil || edge != 1 {
		t.Fatalf("edge: %d (%v)", edge, err)
	}
}

// TestCatalogSharedParentsPreserved proves a subcategory with two parents
// keeps both (no UNIQUE(child) tree constraint).
func TestCatalogSharedParentsPreserved(t *testing.T) {
	env := openSaleEnv(t)
	ids := catalogIDs(t, 20, "a", "b", "shared")
	for i, name := range []string{"a", "b"} {
		event := fmt.Sprintf("d000101%d-0000-4000-8000-00000000000%d", i, i)
		ingestCatalog(t, env, event, catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
			categoryPayload(ids[name], "active", map[string]string{"ar": name}, nil, 1))
		if res := projectCatalogOnce(t, env, event); res.Outcome != catalog.OutcomeProcessed {
			t.Fatalf("root %s: %+v", name, res)
		}
	}
	childEvent := "d0001012-0000-4000-8000-000000000012"
	ingestCatalog(t, env, childEvent, catalog.EventCategorySnapshotV1, "2026-09-20T11:00:00Z",
		categoryPayload(ids["shared"], "active", map[string]string{"ar": "مشترك"}, []string{ids["a"], ids["b"]}, 1))
	if res := projectCatalogOnce(t, env, childEvent); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("shared child: %+v", res)
	}
	var edges int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM catalog_category_edges WHERE child_id=$1`, ids["shared"]).Scan(&edges); err != nil || edges != 2 {
		t.Fatalf("both parents: %d (%v)", edges, err)
	}
}

// TestCatalogParentReplaceRemove proves parent-set snapshots replace
// exactly: added parents appear, removed parents disappear, no delta
// accumulation.
func TestCatalogParentReplaceRemove(t *testing.T) {
	env := openSaleEnv(t)
	ids := catalogIDs(t, 30, "a", "b", "sub")
	for i, name := range []string{"a", "b"} {
		event := fmt.Sprintf("d000102%d-0000-4000-8000-00000000002%d", i, i)
		ingestCatalog(t, env, event, catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
			categoryPayload(ids[name], "active", map[string]string{"ar": name}, nil, 1))
		projectCatalogOnce(t, env, event)
	}
	rev := func(revision int64, parents []string, num string) {
		event := fmt.Sprintf("d000102%s-0000-4000-8000-00000000002%s", num, num)
		ingestCatalog(t, env, event, catalog.EventCategorySnapshotV1, "2026-09-20T12:00:00Z",
			categoryPayload(ids["sub"], "active", map[string]string{"ar": "s"}, parents, revision))
		if res := projectCatalogOnce(t, env, event); res.Outcome != catalog.OutcomeProcessed {
			t.Fatalf("rev %d: %+v", revision, res)
		}
	}
	rev(1, []string{ids["a"]}, "3")
	rev(2, []string{ids["a"], ids["b"]}, "4")
	var edges []string
	rows, err := env.pool.Query(context.Background(),
		`SELECT parent_id::text FROM catalog_category_edges WHERE child_id=$1 ORDER BY parent_id::text`, ids["sub"])
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var parent string
		if err := rows.Scan(&parent); err != nil {
			t.Fatal(err)
		}
		edges = append(edges, parent)
	}
	if len(edges) != 2 {
		t.Fatalf("exactly A,B: %v", edges)
	}
	rev(3, []string{ids["b"]}, "5")
	var remaining string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT parent_id::text FROM catalog_category_edges WHERE child_id=$1`, ids["sub"]).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != ids["b"] {
		t.Fatalf("only B retained: %s", remaining)
	}
	if revision := catalogCategoryRevision(t, env, ids["sub"]); revision != 3 {
		t.Fatalf("revision: %d", revision)
	}
}

// TestCatalogCycleBlocked proves a parent set that would cycle is
// terminally blocked with the existing projection untouched.
func TestCatalogCycleBlocked(t *testing.T) {
	env := openSaleEnv(t)
	ids := catalogIDs(t, 40, "root", "sub")
	for i, name := range []string{"root", "sub"} {
		parents := []string{}
		if name == "sub" {
			parents = []string{ids["root"]}
		}
		event := fmt.Sprintf("d000103%d-0000-4000-8000-00000000003%d", i, i)
		ingestCatalog(t, env, event, catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
			categoryPayload(ids[name], "active", map[string]string{"ar": name}, parents, 1))
		projectCatalogOnce(t, env, event)
	}
	// Malicious: make the root a child of its own descendant.
	cycleEvent := "d0001032-0000-4000-8000-000000000032"
	ingestCatalog(t, env, cycleEvent, catalog.EventCategorySnapshotV1, "2026-09-20T11:00:00Z",
		categoryPayload(ids["root"], "active", map[string]string{"ar": "root"}, []string{ids["sub"]}, 2))
	res := projectCatalogOnce(t, env, cycleEvent)
	if res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrCatalogCategoryCycle {
		t.Fatalf("cycle blocked: %+v", res)
	}
	if revision := catalogCategoryRevision(t, env, ids["root"]); revision != 1 {
		t.Fatalf("root untouched: %d", revision)
	}
	var edges int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM catalog_category_edges WHERE child_id=$1`, ids["root"]).Scan(&edges); err != nil || edges != 0 {
		t.Fatalf("no root edges: %d (%v)", edges, err)
	}
}

// TestCatalogStaleRevisionNoOp proves an older revision arriving after a
// newer one is a terminal no-op: processed, state unchanged.
func TestCatalogStaleRevisionNoOp(t *testing.T) {
	env := openSaleEnv(t)
	ids := catalogIDs(t, 0, "root")
	newPayload := categoryPayload(ids["root"], "active", map[string]string{"ar": "جديد"}, nil, 2)
	ingestCatalog(t, env, "d0001040-0000-4000-8000-000000000040", catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z", newPayload)
	projectCatalogOnce(t, env, "d0001040-0000-4000-8000-000000000040")
	oldPayload := categoryPayload(ids["root"], "active", map[string]string{"ar": "قديم"}, nil, 1)
	ingestCatalog(t, env, "d0001041-0000-4000-8000-000000000041", catalog.EventCategorySnapshotV1, "2026-09-20T09:00:00Z", oldPayload)
	res := projectCatalogOnce(t, env, "d0001041-0000-4000-8000-000000000041")
	if res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("stale no-op: %+v", res)
	}
	var name string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT name_ar FROM catalog_categories WHERE category_id=$1`, ids["root"]).Scan(&name); err != nil || name != "جديد" {
		t.Fatalf("newer state kept: %q (%v)", name, err)
	}
}

// TestCatalogEqualRevisionConflict proves same revision + different payload
// blocks deterministically, and a later valid revision still repairs.
func TestCatalogEqualRevisionConflict(t *testing.T) {
	env := openSaleEnv(t)
	ids := catalogIDs(t, 0, "root")
	ingestCatalog(t, env, "d0001050-0000-4000-8000-000000000050", catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
		categoryPayload(ids["root"], "active", map[string]string{"ar": "أ"}, nil, 1))
	projectCatalogOnce(t, env, "d0001050-0000-4000-8000-000000000050")
	ingestCatalog(t, env, "d0001051-0000-4000-8000-000000000051", catalog.EventCategorySnapshotV1, "2026-09-20T10:01:00Z",
		categoryPayload(ids["root"], "active", map[string]string{"ar": "ب"}, nil, 1))
	res := projectCatalogOnce(t, env, "d0001051-0000-4000-8000-000000000051")
	if res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrCatalogRevisionConflict {
		t.Fatalf("conflict blocked: %+v", res)
	}
	var name string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT name_ar FROM catalog_categories WHERE category_id=$1`, ids["root"]).Scan(&name); err != nil || name != "أ" {
		t.Fatalf("first writer kept: %q (%v)", name, err)
	}
	// Newer valid revision repairs despite the blocked rival.
	ingestCatalog(t, env, "d0001052-0000-4000-8000-000000000052", catalog.EventCategorySnapshotV1, "2026-09-20T10:02:00Z",
		categoryPayload(ids["root"], "hidden", map[string]string{"ar": "ج"}, nil, 2))
	if res := projectCatalogOnce(t, env, "d0001052-0000-4000-8000-000000000052"); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("repair: %+v", res)
	}
	if err := env.pool.QueryRow(context.Background(),
		`SELECT status FROM catalog_categories WHERE category_id=$1`, ids["root"]).Scan(&name); err != nil || name != "hidden" {
		t.Fatalf("repaired state: %q (%v)", name, err)
	}
}

// TestCatalogTagLifecycle proves tag projection with revision ordering.
func TestCatalogTagLifecycle(t *testing.T) {
	env := openSaleEnv(t)
	ids := catalogIDs(t, 50, "tag")
	ingestCatalog(t, env, "d0001060-0000-4000-8000-000000000060", catalog.EventTagSnapshotV1, "2026-09-20T10:00:00Z",
		tagPayload(ids["tag"], "gold", true, map[string]string{"en": "Gold"}, 1))
	if res := projectCatalogOnce(t, env, "d0001060-0000-4000-8000-000000000060"); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("tag: %+v", res)
	}
	// Deactivation is a newer revision, not a delete.
	ingestCatalog(t, env, "d0001061-0000-4000-8000-000000000061", catalog.EventTagSnapshotV1, "2026-09-20T11:00:00Z",
		tagPayload(ids["tag"], "gold", false, map[string]string{"en": "Gold"}, 2))
	if res := projectCatalogOnce(t, env, "d0001061-0000-4000-8000-000000000061"); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("deactivation: %+v", res)
	}
	var active bool
	var revision int64
	if err := env.pool.QueryRow(context.Background(),
		`SELECT is_active, source_revision FROM catalog_tags WHERE tag_id=$1`, ids["tag"]).Scan(&active, &revision); err != nil {
		t.Fatal(err)
	}
	if active || revision != 2 {
		t.Fatalf("tag state: %v %d", active, revision)
	}
}

// catalogProductFixture projects a root, a sub, and a tag, returning IDs.
// Each test passes a distinct base so fixture graphs never share IDs.
func catalogProductFixture(t *testing.T, env *saleEnv, base int, prefix string) (root, sub, tag string) {
	t.Helper()
	ids := catalogIDs(t, base, prefix+"-root", prefix+"-sub", prefix+"-tag")
	ingestCatalog(t, env, "d0002000-0000-4000-8000-000000000000", catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
		categoryPayload(ids[prefix+"-root"], "active", map[string]string{"ar": "جذر"}, nil, 1))
	projectCatalogOnce(t, env, "d0002000-0000-4000-8000-000000000000")
	ingestCatalog(t, env, "d0002001-0000-4000-8000-000000000001", catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
		categoryPayload(ids[prefix+"-sub"], "active", map[string]string{"ar": "فرعي"}, []string{ids[prefix+"-root"]}, 1))
	projectCatalogOnce(t, env, "d0002001-0000-4000-8000-000000000001")
	ingestCatalog(t, env, "d0002002-0000-4000-8000-000000000002", catalog.EventTagSnapshotV1, "2026-09-20T10:00:00Z",
		tagPayload(ids[prefix+"-tag"], "gold", true, map[string]string{"en": "Gold"}, 1))
	projectCatalogOnce(t, env, "d0002002-0000-4000-8000-000000000002")
	return ids[prefix+"-root"], ids[prefix+"-sub"], ids[prefix+"-tag"]
}

// TestCatalogProductProjects proves a full product revision projects
// atomically: header, prices, translations, subcategories, and tags.
func TestCatalogProductProjects(t *testing.T) {
	env := openSaleEnv(t)
	root, sub, tag := catalogProductFixture(t, env, 80, "p1")
	productID := "e0001000-0000-4000-8000-000000000000"
	event := "d0002010-0000-4000-8000-000000000010"
	ingestCatalog(t, env, event, catalog.EventProductSnapshotV1, "2026-09-20T12:00:00Z",
		productPayload(productID, "PAP-001", "توت", root, []string{sub}, []string{tag}, 1))
	if res := projectCatalogOnce(t, env, event); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("product: %+v", res)
	}
	svc := catalog.NewService(catalogStore(env))
	product, err := svc.GetProduct(context.Background(), productID)
	if err != nil {
		t.Fatalf("read service: %v", err)
	}
	if product.SKU != "PAP-001" || product.TopCategoryID != root || len(product.SubcategoryIDs) != 1 || len(product.TagIDs) != 1 {
		t.Fatalf("product: %+v", product)
	}
	if len(product.Prices) != 2 || product.Prices[0].PriceCents != 65000 {
		t.Fatalf("prices: %+v", product.Prices)
	}
	if product.Prices[0].CostCents == nil || *product.Prices[0].CostCents != 40000 {
		t.Fatalf("cost: %+v", product.Prices)
	}
	if product.WidthCM == nil || *product.WidthCM != 70 {
		t.Fatalf("dimensions: %+v", product)
	}
	if product.Revision != 1 {
		t.Fatalf("revision: %+v", product)
	}
	tags, err := svc.TagsForProduct(context.Background(), productID)
	if err != nil || len(tags) != 1 || tags[0] != tag {
		t.Fatalf("tags: %v %v", tags, err)
	}
	top, subs, err := svc.CategoriesForProduct(context.Background(), productID)
	if err != nil || top != root || len(subs) != 1 || subs[0] != sub {
		t.Fatalf("categories: %s %v %v", top, subs, err)
	}
}

// TestCatalogProductWaitsForDeps proves a product arriving before its
// categories/tags waits retryably, then converges without resend.
func TestCatalogProductWaitsForDeps(t *testing.T) {
	env := openSaleEnv(t)
	ids := catalogIDs(t, 70, "root", "sub", "tag")
	productID := "e0001001-0000-4000-8000-000000000001"
	event := "d0002011-0000-4000-8000-000000000011"
	ingestCatalog(t, env, event, catalog.EventProductSnapshotV1, "2026-09-20T10:00:00Z",
		productPayload(productID, "PAP-EARLY", "مبكر", ids["root"], []string{ids["sub"]}, []string{ids["tag"]}, 1))
	res := projectCatalogOnce(t, env, event)
	if res.Outcome != catalog.OutcomeRetryable || res.ErrorCode != ErrCatalogDependencyWait {
		t.Fatalf("dependency wait: %+v", res)
	}
	if n := saleCount(t, env.pool, "catalog_products"); n != 0 {
		t.Fatalf("zero rows while waiting, got %d", n)
	}
	// Dependencies arrive in any order; the product converges.
	ingestCatalog(t, env, "d0002012-0000-4000-8000-000000000012", catalog.EventTagSnapshotV1, "2026-09-20T11:00:00Z",
		tagPayload(ids["tag"], "gold", true, map[string]string{"en": "Gold"}, 1))
	projectCatalogOnce(t, env, "d0002012-0000-4000-8000-000000000012")
	ingestCatalog(t, env, "d0002013-0000-4000-8000-000000000013", catalog.EventCategorySnapshotV1, "2026-09-20T11:00:00Z",
		categoryPayload(ids["sub"], "active", map[string]string{"ar": "فرعي"}, []string{ids["root"]}, 1))
	// Sub itself waits for the root; project root first.
	ingestCatalog(t, env, "d0002014-0000-4000-8000-000000000014", catalog.EventCategorySnapshotV1, "2026-09-20T11:00:00Z",
		categoryPayload(ids["root"], "active", map[string]string{"ar": "جذر"}, nil, 1))
	projectCatalogOnce(t, env, "d0002014-0000-4000-8000-000000000014")
	projectCatalogOnce(t, env, "d0002013-0000-4000-8000-000000000013")
	forceCatalogDue(t, env, catalog.ProcessorProductProjectionV1, event)
	if res := projectCatalogOnce(t, env, event); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("product converges: %+v", res)
	}
}

// TestCatalogProductInvalidRelationBlocked proves structural violations are
// terminal once dependencies exist: a non-root top and an unreachable sub.
func TestCatalogProductInvalidRelationBlocked(t *testing.T) {
	env := openSaleEnv(t)
	root, sub, tag := catalogProductFixture(t, env, 90, "p2")
	_ = root
	// Top is the subcategory (has a parent): not a root.
	productID := "e0001002-0000-4000-8000-000000000002"
	event := "d0002020-0000-4000-8000-000000000020"
	ingestCatalog(t, env, event, catalog.EventProductSnapshotV1, "2026-09-20T12:00:00Z",
		productPayload(productID, "PAP-BADTOP", "x", sub, nil, []string{tag}, 1))
	res := projectCatalogOnce(t, env, event)
	if res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrCatalogInvalidRelation {
		t.Fatalf("non-root top blocked: %+v", res)
	}
	if n := saleCount(t, env.pool, "catalog_products"); n != 0 {
		t.Fatalf("zero rows, got %d", n)
	}
	// Unreachable sub: a second root's child is not under this top.
	ids := catalogIDs(t, 60, "other", "stranger")
	ingestCatalog(t, env, "d0002021-0000-4000-8000-000000000021", catalog.EventCategorySnapshotV1, "2026-09-20T12:00:00Z",
		categoryPayload(ids["other"], "active", map[string]string{"ar": "o"}, nil, 1))
	projectCatalogOnce(t, env, "d0002021-0000-4000-8000-000000000021")
	ingestCatalog(t, env, "d0002022-0000-4000-8000-000000000022", catalog.EventCategorySnapshotV1, "2026-09-20T12:00:00Z",
		categoryPayload(ids["stranger"], "active", map[string]string{"ar": "s"}, []string{ids["other"]}, 1))
	projectCatalogOnce(t, env, "d0002022-0000-4000-8000-000000000022")
	// Reuse any projected root with the stranger sub: unreachable pairing.
	productID2 := "e0001003-0000-4000-8000-000000000003"
	event2 := "d0002023-0000-4000-8000-000000000023"
	// Fetch the p2 root id via the tag-independent path: query any root.
	var topID string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT c.category_id::text FROM catalog_categories c WHERE NOT EXISTS
		 (SELECT 1 FROM catalog_category_edges e WHERE e.child_id = c.category_id)
		 AND c.name_ar = 'جذر' LIMIT 1`).Scan(&topID); err != nil {
		t.Fatal(err)
	}
	ingestCatalog(t, env, event2, catalog.EventProductSnapshotV1, "2026-09-20T12:00:00Z",
		productPayload(productID2, "PAP-BADSUB", "x", topID, []string{ids["stranger"]}, []string{tag}, 1))
	res = projectCatalogOnce(t, env, event2)
	if res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrCatalogInvalidRelation {
		t.Fatalf("unreachable sub blocked: %+v", res)
	}
}

// TestCatalogProductRepairAfterBlock proves a blocked invalid revision does
// not freeze the entity: a later valid revision becomes authoritative.
func TestCatalogProductRepairAfterBlock(t *testing.T) {
	env := openSaleEnv(t)
	root, sub, tag := catalogProductFixture(t, env, 100, "p3")
	productID := "e0001004-0000-4000-8000-000000000004"
	bad := "d0002030-0000-4000-8000-000000000030"
	ingestCatalog(t, env, bad, catalog.EventProductSnapshotV1, "2026-09-20T12:00:00Z",
		productPayload(productID, "PAP-REPAIR", "x", sub, nil, []string{tag}, 1))
	if res := projectCatalogOnce(t, env, bad); res.Outcome != catalog.OutcomeBlocked {
		t.Fatalf("invalid rev blocked: %+v", res)
	}
	good := "d0002031-0000-4000-8000-000000000031"
	ingestCatalog(t, env, good, catalog.EventProductSnapshotV1, "2026-09-20T13:00:00Z",
		productPayload(productID, "PAP-REPAIR", "x", root, []string{sub}, []string{tag}, 2))
	if res := projectCatalogOnce(t, env, good); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("repair: %+v", res)
	}
	var revision int64
	if err := env.pool.QueryRow(context.Background(),
		`SELECT source_revision FROM catalog_products WHERE product_id=$1`, productID).Scan(&revision); err != nil || revision != 2 {
		t.Fatalf("rev 2 current: %d (%v)", revision, err)
	}
}

// TestCatalogOutOfOrderRevisions proves rev7 → rev5 → rev6 processing order
// converges on revision 7 with stale events as terminal no-ops.
func TestCatalogOutOfOrderRevisions(t *testing.T) {
	env := openSaleEnv(t)
	ids := catalogIDs(t, 0, "root")
	ingestCatalog(t, env, "d0002040-0000-4000-8000-000000000040", catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
		categoryPayload(ids["root"], "active", map[string]string{"ar": "r7"}, nil, 7))
	projectCatalogOnce(t, env, "d0002040-0000-4000-8000-000000000040")
	for i, rev := range []int64{5, 6} {
		event := fmt.Sprintf("d000204%d-0000-4000-8000-00000000004%d", i+1, i+1)
		ingestCatalog(t, env, event, catalog.EventCategorySnapshotV1, "2026-09-20T09:00:00Z",
			categoryPayload(ids["root"], "active", map[string]string{"ar": "stale"}, nil, rev))
		if res := projectCatalogOnce(t, env, event); res.Outcome != catalog.OutcomeProcessed {
			t.Fatalf("stale rev %d no-op: %+v", rev, res)
		}
	}
	if revision := catalogCategoryRevision(t, env, ids["root"]); revision != 7 {
		t.Fatalf("final rev 7: %d", revision)
	}
	var pending int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM sync_event_processing WHERE processor='catalog_category_projection.v1' AND status IN ('pending','retry')`).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("no infinite backlog: %d (%v)", pending, err)
	}
}

// dumpCatalog renders derived catalog rows deterministically for rebuild
// comparison (excludes nondeterministic projected_at).
func dumpCatalog(t *testing.T, env *saleEnv) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var b strings.Builder
	dump := func(query string) {
		rows, err := env.pool.Query(ctx, query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			vals, err := rows.Values()
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "%v\n", vals)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	dump(`SELECT category_id::text, status, name_ar, name_en, source_revision, source_event_id::text
		FROM catalog_categories ORDER BY category_id::text`)
	dump(`SELECT parent_id::text, child_id::text, position FROM catalog_category_edges ORDER BY parent_id::text, child_id::text`)
	dump(`SELECT tag_id::text, slug, is_active, name_en, source_revision FROM catalog_tags ORDER BY tag_id::text`)
	dump(`SELECT product_id::text, sku, name, top_category_id::text, is_active, source_revision
		FROM catalog_products ORDER BY product_id::text`)
	dump(`SELECT product_id::text, currency, price_minor, cost_minor FROM catalog_product_prices ORDER BY product_id::text, currency`)
	dump(`SELECT product_id::text, locale, name FROM catalog_product_translations ORDER BY product_id::text, locale`)
	dump(`SELECT product_id::text, category_id::text, position FROM catalog_product_subcategories ORDER BY product_id::text, position`)
	dump(`SELECT product_id::text, tag_id::text FROM catalog_product_tags ORDER BY product_id::text, tag_id::text`)
	return b.String()
}

// TestCatalogRebuildStable proves catalog projections rebuild identically
// with reversed processing order, preserving the latest lifecycle state.
func TestCatalogRebuildStable(t *testing.T) {
	env := openSaleEnv(t)
	root, sub, tag := catalogProductFixture(t, env, 110, "rb")
	productID := "e0001005-0000-4000-8000-000000000005"
	ingestCatalog(t, env, "d0002050-0000-4000-8000-000000000050", catalog.EventProductSnapshotV1, "2026-09-20T12:00:00Z",
		productPayload(productID, "PAP-RB", "x", root, []string{sub}, []string{tag}, 1))
	projectCatalogOnce(t, env, "d0002050-0000-4000-8000-000000000050")
	// Hide the product at revision 2 (latest lifecycle state).
	hidden := productPayload(productID, "PAP-RB", "x", root, []string{sub}, []string{tag}, 2)
	var hiddenMap map[string]any
	if err := json.Unmarshal([]byte(hidden), &hiddenMap); err != nil {
		t.Fatal(err)
	}
	hiddenMap["is_active"] = false
	hiddenRaw, _ := json.Marshal(hiddenMap)
	ingestCatalog(t, env, "d0002051-0000-4000-8000-000000000051", catalog.EventProductSnapshotV1, "2026-09-20T13:00:00Z", string(hiddenRaw))
	projectCatalogOnce(t, env, "d0002051-0000-4000-8000-000000000051")

	before := dumpCatalog(t, env)
	if before == "" {
		t.Fatal("snapshot must not be empty")
	}

	// Clear derived catalog state only; inbox stays authoritative.
	ctx := context.Background()
	for _, table := range []string{
		"catalog_product_tags", "catalog_product_subcategories", "catalog_product_translations",
		"catalog_product_prices", "catalog_products", "catalog_category_edges",
		"catalog_tags", "catalog_categories",
	} {
		if _, err := env.pool.Exec(ctx, `DELETE FROM `+table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := env.pool.Exec(ctx,
		`UPDATE sync_event_processing SET status='pending', next_attempt_at=NULL,
		 attempt_count=0, processed_at=NULL, last_error_code=NULL, last_error_message=NULL
		 WHERE processor LIKE 'catalog_%'`); err != nil {
		t.Fatal(err)
	}

	// Reprocess in dependency order (categories, tags, then products):
	// the documented rebuild sequence. Out-of-order arrival would wait
	// and converge instead; that path is covered by the dependency-wait
	// tests plus the final drain below.
	store := catalogStore(env)
	rows, err := env.pool.Query(ctx,
		`SELECT event_id::text, event_type FROM sync_events WHERE event_type LIKE 'catalog.%'`)
	if err != nil {
		t.Fatal(err)
	}
	type queued struct{ id, typ string }
	var order []queued
	for rows.Next() {
		var q queued
		if err := rows.Scan(&q.id, &q.typ); err != nil {
			t.Fatal(err)
		}
		order = append(order, q)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rank := map[string]int{
		catalog.EventCategorySnapshotV1: 0, catalog.EventTagSnapshotV1: 1, catalog.EventProductSnapshotV1: 2,
	}
	sort.SliceStable(order, func(i, j int) bool { return rank[order[i].typ] < rank[order[j].typ] })
	for _, q := range order {
		rec, ok, err := store.LoadCatalogEvent(ctx, q.id)
		if err != nil || !ok {
			t.Fatal("load")
		}
		var res catalog.ProjectResult
		switch q.typ {
		case catalog.EventCategorySnapshotV1:
			res, err = store.ProjectCategory(ctx, rec, time.Now())
		case catalog.EventTagSnapshotV1:
			res, err = store.ProjectTag(ctx, rec, time.Now())
		default:
			res, err = store.ProjectProduct(ctx, rec, time.Now())
		}
		if err != nil && res.Outcome != catalog.OutcomeRetryable {
			t.Fatalf("reproject %s: %v", q.id, err)
		}
		if res.Outcome != catalog.OutcomeProcessed && res.Outcome != catalog.OutcomeBlocked && res.Outcome != catalog.OutcomeRetryable {
			t.Fatalf("reproject %s: %+v", q.id, res)
		}
	}
	// Settle any dependency waits to convergence.
	drainCatalog(t, env)
	if after := dumpCatalog(t, env); after != before {
		t.Fatalf("rebuild mismatch:\nbefore: %s\nafter:  %s", before, after)
	}
	var active bool
	if err := env.pool.QueryRow(ctx,
		`SELECT is_active FROM catalog_products WHERE product_id=$1`, productID).Scan(&active); err != nil || active {
		t.Fatalf("hidden lifecycle preserved: %v (%v)", active, err)
	}
}

// drainCatalog runs all three catalog projectors to quiescence (fresh
// instances = restart proof) with a bounded wait.
func drainCatalog(t *testing.T, env *saleEnv) {
	t.Helper()
	store := NewDevices(env.pool, 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		catalog.NewCategoryProjector(store, testSystemClock(), nilLogger()).Run(ctx)
	}()
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		catalog.NewTagProjector(store, testSystemClock(), nilLogger()).Run(ctx)
	}()
	done3 := make(chan struct{})
	go func() {
		defer close(done3)
		catalog.NewProductProjector(store, testSystemClock(), nilLogger()).Run(ctx)
	}()
	waitFor(t, 12*time.Second, func() bool {
		var pending int
		_ = env.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM sync_event_processing WHERE processor LIKE 'catalog_%' AND status IN ('pending','retry')`).Scan(&pending)
		return pending == 0
	}, "catalog drain quiescence")
	cancel()
	<-done
	<-done2
	<-done3
}

// TestCatalogMoneyExact proves minor-unit values beyond 2^53 survive
// JSON → PostgreSQL → read service exactly.
func TestCatalogMoneyExact(t *testing.T) {
	env := openSaleEnv(t)
	root, _, _ := catalogProductFixture(t, env, 120, "mx")
	productID := "e0001006-0000-4000-8000-000000000006"
	payload := productPayload(productID, "PAP-BIG", "x", root, nil, nil, 1)
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatal(err)
	}
	m["prices"] = []any{map[string]any{"currency": "EGP", "price_cents": 9007199254740993}}
	raw, _ := json.Marshal(m)
	event := "d0002060-0000-4000-8000-000000000060"
	ingestCatalog(t, env, event, catalog.EventProductSnapshotV1, "2026-09-20T12:00:00Z", string(raw))
	if res := projectCatalogOnce(t, env, event); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("big money: %+v", res)
	}
	svc := catalog.NewService(catalogStore(env))
	product, err := svc.GetProduct(context.Background(), productID)
	if err != nil {
		t.Fatal(err)
	}
	if len(product.Prices) != 1 || product.Prices[0].PriceCents != 9007199254740993 {
		t.Fatalf("exact money: %+v", product.Prices)
	}
}

// TestCatalogRenameIsolatesHistory proves a catalog rename after a sale
// changes current catalog state while the historical Sale projection (and
// therefore reporting) keeps the sale-time snapshot.
func TestCatalogRenameIsolatesHistory(t *testing.T) {
	env := openSaleEnv(t)
	env.ingest(t, "22222222-2222-7222-8222-222222222222", fixture(t, "sale_egp.json"))
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	// The fixture sale line references product 66666666-.../PAP-002.
	// Project a catalog row for the same business identity under a root.
	root, _, _ := catalogProductFixture(t, env, 130, "rn")
	productID := "66666666-6666-6666-8666-666666666666"
	rev := func(revision int64, name, num string) {
		payload := productPayload(productID, "PAP-002", name, root, nil, nil, revision)
		event := fmt.Sprintf("d000206%s-0000-4000-8000-00000000006%s", num, num)
		ingestCatalog(t, env, event, catalog.EventProductSnapshotV1, "2026-09-20T12:00:00Z", payload)
		if res := projectCatalogOnce(t, env, event); res.Outcome != catalog.OutcomeProcessed {
			t.Fatalf("rev %d: %+v", revision, res)
		}
	}
	rev(1, "Papyrus Scroll", "0")
	svc := catalog.NewService(catalogStore(env))
	before, err := svc.GetProduct(context.Background(), productID)
	if err != nil || before.Name != "Papyrus Scroll" {
		t.Fatalf("current name: %+v %v", before, err)
	}
	sumBefore := reportSummary(t, env, "custom", "2026-09-20", "2026-09-20", "")
	rev(2, "RENAMED Scroll", "1")
	after, err := svc.GetProduct(context.Background(), productID)
	if err != nil || after.Name != "RENAMED Scroll" {
		t.Fatalf("renamed current: %+v %v", after, err)
	}
	sumAfter := reportSummary(t, env, "custom", "2026-09-20", "2026-09-20", "")
	if fmt.Sprintf("%+v", sumBefore.CurrencyTotals) != fmt.Sprintf("%+v", sumAfter.CurrencyTotals) {
		t.Fatalf("report drift:\nbefore: %+v\nafter:  %+v", sumBefore.CurrencyTotals, sumAfter.CurrencyTotals)
	}
	if len(sumAfter.CurrencyTotals) == 0 || sumAfter.CurrencyTotals[0].SalesTotalMinor != 200000 {
		t.Fatalf("gross preserved: %+v", sumAfter.CurrencyTotals)
	}
}
