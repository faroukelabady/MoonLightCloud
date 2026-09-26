package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
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

// coordinatedFixture builds the §36 graph: roots A and B, sub S under A
// (all rev 1, projected), and product P (top A + S, rev 1, projected).
// Returns IDs plus the accepted (unprocessed) rev-2 category/product event
// IDs that move S under B and retarget P to top B.
func coordinatedFixture(t *testing.T, env *saleEnv) (a, b, s, product string, catRev2, prodRev2 string) {
	t.Helper()
	ids := catalogIDs(t, 200, "a", "b", "s")
	a, b, s = ids["a"], ids["b"], ids["s"]
	project := func(event, typ, occurred, payload string) {
		ingestCatalog(t, env, event, typ, occurred, payload)
		if res := projectCatalogOnce(t, env, event); res.Outcome != catalog.OutcomeProcessed {
			t.Fatalf("fixture %s: %+v", event, res)
		}
	}
	project("d0003000-0000-4000-8000-000000000000", catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
		categoryPayload(a, "active", map[string]string{"ar": "A"}, nil, 1))
	project("d0003001-0000-4000-8000-000000000001", catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
		categoryPayload(b, "active", map[string]string{"ar": "B"}, nil, 1))
	project("d0003002-0000-4000-8000-000000000002", catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
		categoryPayload(s, "active", map[string]string{"ar": "S"}, []string{a}, 1))
	product = "e0003000-0000-4000-8000-000000000000"
	project("d0003003-0000-4000-8000-000000000003", catalog.EventProductSnapshotV1, "2026-09-20T10:00:00Z",
		productPayload(product, "PAP-C", "x", a, []string{s}, nil, 1))
	catRev2 = "d0003004-0000-4000-8000-000000000004"
	ingestCatalog(t, env, catRev2, catalog.EventCategorySnapshotV1, "2026-09-20T11:00:00Z",
		categoryPayload(s, "active", map[string]string{"ar": "S"}, []string{b}, 2))
	prodRev2 = "d0003005-0000-4000-8000-000000000005"
	return a, b, s, product, catRev2, prodRev2
}

// ingestProdRev2 accepts the coordinated product repair event. Tests that
// need an accepted repair call this explicitly; the orphan test omits it
// to prove a repair-less graph change is rejected.
func ingestProdRev2(t *testing.T, env *saleEnv, product, b, s, prodRev2 string) {
	t.Helper()
	ingestCatalog(t, env, prodRev2, catalog.EventProductSnapshotV1, "2026-09-20T11:00:00Z",
		productPayload(product, "PAP-C", "x", b, []string{s}, nil, 2))
}

func catalogProductTop(t *testing.T, env *saleEnv, productID string) (string, int64) {
	t.Helper()
	var top string
	var revision int64
	if err := env.pool.QueryRow(context.Background(),
		`SELECT top_category_id::text, source_revision FROM catalog_products WHERE product_id=$1`,
		productID).Scan(&top, &revision); err != nil {
		t.Fatal(err)
	}
	return top, revision
}

func catalogChildParents(t *testing.T, env *saleEnv, childID string) []string {
	t.Helper()
	rows, err := env.pool.Query(context.Background(),
		`SELECT parent_id::text FROM catalog_category_edges WHERE child_id=$1 ORDER BY position, parent_id::text`, childID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var parents []string
	for rows.Next() {
		var parent string
		if err := rows.Scan(&parent); err != nil {
			t.Fatal(err)
		}
		parents = append(parents, parent)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return parents
}

// TestCatalogCoordinatedProductFirst proves §36 ordering: the product
// revision evaluated against the old graph waits (never terminally
// blocks), the category revision commits, and the product advances with
// no manual retry and no resend.
func TestCatalogCoordinatedProductFirst(t *testing.T) {
	env := openSaleEnv(t)
	a, b, s, product, catRev2, prodRev2 := coordinatedFixture(t, env)
	ingestProdRev2(t, env, product, b, s, prodRev2)

	res := projectCatalogOnce(t, env, prodRev2)
	if res.Outcome != catalog.OutcomeRetryable || res.ErrorCode != ErrCatalogDependencyWait {
		t.Fatalf("product waits on unsettled graph: %+v", res)
	}
	if top, revision := catalogProductTop(t, env, product); top != a || revision != 1 {
		t.Fatalf("product still rev1/top A: %s %d", top, revision)
	}

	if res := projectCatalogOnce(t, env, catRev2); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("category commits: %+v", res)
	}
	if parents := catalogChildParents(t, env, s); len(parents) != 1 || parents[0] != b {
		t.Fatalf("graph moved S→B: %v", parents)
	}
	// The category commit automatically re-armed the waiting product: no
	// operator reset, no forceDue call in this test.
	if res := projectCatalogOnce(t, env, prodRev2); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("product advances: %+v", res)
	}
	if top, revision := catalogProductTop(t, env, product); top != b || revision != 2 {
		t.Fatalf("final top B rev2: %s %d", top, revision)
	}
}

// TestCatalogCoordinatedCategoryFirst proves the reverse order converges
// to the same final state.
func TestCatalogCoordinatedCategoryFirst(t *testing.T) {
	env := openSaleEnv(t)
	_, b, s, product, catRev2, prodRev2 := coordinatedFixture(t, env)
	ingestProdRev2(t, env, product, b, s, prodRev2)

	if res := projectCatalogOnce(t, env, catRev2); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("category commits: %+v", res)
	}
	if res := projectCatalogOnce(t, env, prodRev2); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("product advances: %+v", res)
	}
	if top, revision := catalogProductTop(t, env, product); top != b || revision != 2 {
		t.Fatalf("final top B rev2: %s %d", top, revision)
	}
	if parents := catalogChildParents(t, env, s); len(parents) != 1 || parents[0] != b {
		t.Fatalf("graph S→B: %v", parents)
	}
}

// TestCatalogOrphaningBlockedWithoutRepair proves §38: a category revision
// that would orphan current products, with no accepted product repair,
// is terminally blocked and the previous graph stays authoritative.
func TestCatalogOrphaningBlockedWithoutRepair(t *testing.T) {
	env := openSaleEnv(t)
	a, b, s, product, _, _ := coordinatedFixture(t, env)
	_ = product
	// No product rev2 accepted: the reparent has no repair.
	orphan := "d0003006-0000-4000-8000-000000000006"
	ingestCatalog(t, env, orphan, catalog.EventCategorySnapshotV1, "2026-09-20T12:00:00Z",
		categoryPayload(s, "active", map[string]string{"ar": "S"}, []string{b}, 2))
	res := projectCatalogOnce(t, env, orphan)
	if res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrCatalogGraphConflict {
		t.Fatalf("orphaning change blocked: %+v", res)
	}
	if parents := catalogChildParents(t, env, s); len(parents) != 1 || parents[0] != a {
		t.Fatalf("previous graph preserved: %v", parents)
	}
	if top, revision := catalogProductTop(t, env, product); top != a || revision != 1 {
		t.Fatalf("product still valid rev1: %s %d", top, revision)
	}
	if status, _ := catalogStatus(t, env, catalog.ProcessorCategoryProjectionV1, orphan); status != "blocked" {
		t.Fatalf("terminal state: %q", status)
	}
}

// TestCatalogTransientCategoryRecovery proves a transient category failure
// never strands the product: after recovery the category commits and the
// waiting product advances automatically.
func TestCatalogTransientCategoryRecovery(t *testing.T) {
	env := openSaleEnv(t)
	_, b, s, product, catRev2, prodRev2 := coordinatedFixture(t, env)
	ingestProdRev2(t, env, product, b, s, prodRev2)

	if _, err := env.pool.Exec(context.Background(),
		`CREATE FUNCTION fail_catalog_category() RETURNS trigger AS $$
		 BEGIN RAISE EXCEPTION 'injected failure'; RETURN NEW; END; $$ LANGUAGE plpgsql;`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(context.Background(),
		`CREATE TRIGGER trg_fail_catalog_category BEFORE INSERT ON catalog_category_edges
		 FOR EACH ROW EXECUTE FUNCTION fail_catalog_category();`); err != nil {
		t.Fatal(err)
	}
	// Product waits on the unsettled graph (not on the fault).
	if res := projectCatalogOnce(t, env, prodRev2); res.Outcome != catalog.OutcomeRetryable {
		t.Fatalf("product waits: %+v", res)
	}
	// Category commit hits the injected fault: transient, zero partial rows.
	store := catalogStore(env)
	rec, ok, err := store.LoadCatalogEvent(context.Background(), catRev2)
	if err != nil || !ok {
		t.Fatal("load")
	}
	if _, err := store.ProjectCategory(context.Background(), rec, time.Now()); err == nil {
		t.Fatal("injected failure must surface")
	}
	var edges int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM catalog_category_edges WHERE child_id=$1`, s).Scan(&edges); err != nil || edges != 1 {
		t.Fatalf("previous edges intact: %d (%v)", edges, err)
	}
	if status, _ := catalogStatus(t, env, catalog.ProcessorCategoryProjectionV1, catRev2); status != "retry" {
		t.Fatalf("category retryable: %q", status)
	}
	if _, err := env.pool.Exec(context.Background(),
		`DROP TRIGGER trg_fail_catalog_category ON catalog_category_edges; DROP FUNCTION fail_catalog_category();`); err != nil {
		t.Fatal(err)
	}
	forceCatalogDue(t, env, catalog.ProcessorCategoryProjectionV1, catRev2)
	if res := projectCatalogOnce(t, env, catRev2); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("category recovers: %+v", res)
	}
	if res := projectCatalogOnce(t, env, prodRev2); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("product advances: %+v", res)
	}
	if top, revision := catalogProductTop(t, env, product); top != b || revision != 2 {
		t.Fatalf("final top B rev2: %s %d", top, revision)
	}
}

// TestCatalogCoordinatedRebuild replays the §36 history through a full
// rebuild and expects the identical converged state.
func TestCatalogCoordinatedRebuild(t *testing.T) {
	env := openSaleEnv(t)
	_, b, s, product, catRev2, prodRev2 := coordinatedFixture(t, env)
	ingestProdRev2(t, env, product, b, s, prodRev2)
	projectCatalogOnce(t, env, prodRev2)
	forceCatalogDue(t, env, catalog.ProcessorProductProjectionV1, prodRev2)
	projectCatalogOnce(t, env, catRev2)
	projectCatalogOnce(t, env, prodRev2)
	before := dumpCatalog(t, env)

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
	// Deterministic multi-pass replay in inbox order: dependency waits
	// converge as their dependencies land, with retry horizons defeated
	// explicitly (no wall-clock dependence). Bounded: this DAG settles in
	// at most three passes.
	store := catalogStore(env)
	for pass := 0; pass < 5; pass++ {
		rows, err := env.pool.Query(ctx,
			`SELECT e.event_id::text, e.event_type FROM sync_events e
			 JOIN sync_event_processing p ON p.event_id = e.event_id
			 WHERE e.event_type LIKE 'catalog.%' AND p.status IN ('pending', 'retry')
			 ORDER BY e.received_at, e.event_id`)
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
		if len(order) == 0 {
			break
		}
		// Defeat backoff horizons deterministically.
		if _, err := env.pool.Exec(ctx,
			`UPDATE sync_event_processing SET next_attempt_at = NULL WHERE status = 'retry'`); err != nil {
			t.Fatal(err)
		}
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
				t.Fatalf("replay %s: %v", q.id, err)
			}
		}
		if pass == 4 {
			t.Fatal("rebuild did not converge in 5 passes")
		}
	}
	if after := dumpCatalog(t, env); after != before {
		t.Fatalf("rebuild mismatch:\nbefore: %s\nafter:  %s", before, after)
	}
	if top, revision := catalogProductTop(t, env, product); top != b || revision != 2 {
		t.Fatalf("rebuilt top B rev2: %s %d", top, revision)
	}
}

// driveCatalogToTerminal projects one event to a terminal outcome,
// defeating backoff horizons deterministically and tolerating transient
// serialization aborts with bounded retries. It returns the number of
// rounds used; runaway ping-pong fails the test instead of hanging it.
func driveCatalogToTerminal(t *testing.T, env *saleEnv, eventID string, maxRounds int) (catalog.ProjectResult, int) {
	t.Helper()
	store := catalogStore(env)
	ctx := context.Background()
	var last catalog.ProjectResult
	rounds := 0
	for rounds = 1; rounds <= maxRounds; rounds++ {
		if _, err := env.pool.Exec(ctx,
			`UPDATE sync_event_processing SET next_attempt_at = NULL WHERE event_id=$1 AND status='retry'`, eventID); err != nil {
			t.Fatal(err)
		}
		rec, ok, err := store.LoadCatalogEvent(ctx, eventID)
		if err != nil || !ok {
			t.Fatal("load")
		}
		var res catalog.ProjectResult
		var perr error
		switch rec.EventType {
		case catalog.EventCategorySnapshotV1:
			res, perr = store.ProjectCategory(ctx, rec, time.Now())
		case catalog.EventTagSnapshotV1:
			res, perr = store.ProjectTag(ctx, rec, time.Now())
		default:
			res, perr = store.ProjectProduct(ctx, rec, time.Now())
		}
		last = res
		if perr != nil && res.Outcome != catalog.OutcomeRetryable {
			t.Fatalf("event %s: %v", eventID, perr)
		}
		if res.Outcome == catalog.OutcomeProcessed || res.Outcome == catalog.OutcomeBlocked {
			return res, rounds
		}
	}
	t.Fatalf("event %s did not reach terminal state in %d rounds (last %+v)", eventID, maxRounds, last)
	return last, rounds
}

// TestCatalogConcurrentRevisionsDeterministic forces two product revisions
// through the competing revision path simultaneously (barrier start over
// pool connections). Whatever interleaving occurs, the final state must be
// revision 6 with revision 6 scalars and relations: the older transaction
// can never overwrite the newer revision or its relations.
func TestCatalogConcurrentRevisionsDeterministic(t *testing.T) {
	env := openSaleEnv(t)
	root, sub, tag := catalogProductFixture(t, env, 140, "cc")
	productID := "e0004000-0000-4000-8000-000000000000"
	rev := func(revision int64, name string, tags []string, num string) string {
		event := fmt.Sprintf("d00040%s-0000-4000-8000-0000000000%s", num, num)
		ingestCatalog(t, env, event, catalog.EventProductSnapshotV1, "2026-09-20T12:00:00Z",
			productPayload(productID, "PAP-CC", name, root, []string{sub}, tags, revision))
		return event
	}
	ev5 := rev(5, "five", []string{tag}, "50")
	ev6 := rev(6, "six", []string{}, "60")

	start := make(chan struct{})
	done := make(chan catalog.ProjectResult, 2)
	for _, event := range []string{ev5, ev6} {
		go func(event string) {
			store := catalogStore(env)
			ctx := context.Background()
			<-start
			rec, ok, err := store.LoadCatalogEvent(ctx, event)
			if err != nil || !ok {
				done <- catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: "LOAD"}
				return
			}
			res, err := store.ProjectProduct(ctx, rec, time.Now())
			if err != nil && res.Outcome != catalog.OutcomeRetryable {
				done <- catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: "ERR"}
				return
			}
			done <- res
		}(event)
	}
	close(start)
	results := []catalog.ProjectResult{<-done, <-done}
	// Drive any transient (serialization abort, backoff) to terminal.
	for _, event := range []string{ev5, ev6} {
		driveCatalogToTerminal(t, env, event, 10)
	}
	_ = results

	svc := catalog.NewService(catalogStore(env))
	product, err := svc.GetProduct(context.Background(), productID)
	if err != nil {
		t.Fatal(err)
	}
	if product.Revision != 6 || product.Name != "six" {
		t.Fatalf("newest revision wins: %+v", product)
	}
	if len(product.TagIDs) != 0 {
		t.Fatalf("rev6 relations only: %+v", product.TagIDs)
	}
	if len(product.SubcategoryIDs) != 1 || product.SubcategoryIDs[0] != sub {
		t.Fatalf("rev6 subs: %+v", product.SubcategoryIDs)
	}
	var headers, lines, prices int
	ctx := context.Background()
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM catalog_products WHERE product_id=$1`, productID).Scan(&headers); err != nil || headers != 1 {
		t.Fatalf("one header: %d (%v)", headers, err)
	}
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM catalog_product_tags WHERE product_id=$1`, productID).Scan(&lines); err != nil || lines != 0 {
		t.Fatalf("zero tags: %d (%v)", lines, err)
	}
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM catalog_product_prices WHERE product_id=$1`, productID).Scan(&prices); err != nil || prices != 2 {
		t.Fatalf("rev6 prices: %d (%v)", prices, err)
	}
}

// TestCatalogConcurrentFirstInsert proves rev1 vs rev2 with no existing row
// converge on rev2: row-level locking alone cannot serialize a missing row,
// so the entity advisory lock must.
func TestCatalogConcurrentFirstInsert(t *testing.T) {
	env := openSaleEnv(t)
	root, sub, tag := catalogProductFixture(t, env, 150, "fi")
	productID := "e0004001-0000-4000-8000-000000000001"
	mk := func(revision int64, num string) string {
		event := fmt.Sprintf("d00041%s-0000-4000-8000-0000000000%s", num, num)
		ingestCatalog(t, env, event, catalog.EventProductSnapshotV1, "2026-09-20T12:00:00Z",
			productPayload(productID, "PAP-FI", "rev", root, []string{sub}, []string{tag}, revision))
		return event
	}
	ev1 := mk(1, "10")
	ev2 := mk(2, "20")
	start := make(chan struct{})
	done := make(chan catalog.ProjectResult, 2)
	for _, event := range []string{ev1, ev2} {
		go func(event string) {
			store := catalogStore(env)
			ctx := context.Background()
			<-start
			rec, ok, err := store.LoadCatalogEvent(ctx, event)
			if err != nil || !ok {
				done <- catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: "LOAD"}
				return
			}
			res, err := store.ProjectProduct(ctx, rec, time.Now())
			if err != nil && res.Outcome != catalog.OutcomeRetryable {
				done <- catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: "ERR:" + err.Error()}
				return
			}
			done <- res
		}(event)
	}
	close(start)
	<-done
	<-done
	for _, event := range []string{ev1, ev2} {
		driveCatalogToTerminal(t, env, event, 10)
	}
	var revision int64
	if err := env.pool.QueryRow(context.Background(),
		`SELECT source_revision FROM catalog_products WHERE product_id=$1`, productID).Scan(&revision); err != nil || revision != 2 {
		t.Fatalf("final rev2: %d (%v)", revision, err)
	}
}

// TestCatalogConcurrentCategoryRevisions forces concurrent category
// revisions with different parent sets: newest revision wins with exactly
// its edges.
func TestCatalogConcurrentCategoryRevisions(t *testing.T) {
	env := openSaleEnv(t)
	ids := catalogIDs(t, 160, "a", "b", "sub")
	for i, name := range []string{"a", "b"} {
		event := fmt.Sprintf("d000420%d-0000-4000-8000-00000000002%d", i, i)
		ingestCatalog(t, env, event, catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
			categoryPayload(ids[name], "active", map[string]string{"ar": name}, nil, 1))
		projectCatalogOnce(t, env, event)
	}
	mk := func(parents []string, revision int64, num string) string {
		event := fmt.Sprintf("d00042%s-0000-4000-8000-0000000000%s", num, num)
		ingestCatalog(t, env, event, catalog.EventCategorySnapshotV1, "2026-09-20T12:00:00Z",
			categoryPayload(ids["sub"], "active", map[string]string{"ar": "s"}, parents, revision))
		return event
	}
	ev2 := mk([]string{ids["a"]}, 2, "30")
	ev3 := mk([]string{ids["b"]}, 3, "31")
	start := make(chan struct{})
	done := make(chan catalog.ProjectResult, 2)
	for _, event := range []string{ev2, ev3} {
		go func(event string) {
			store := catalogStore(env)
			ctx := context.Background()
			<-start
			rec, ok, err := store.LoadCatalogEvent(ctx, event)
			if err != nil || !ok {
				done <- catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: "LOAD"}
				return
			}
			res, err := store.ProjectCategory(ctx, rec, time.Now())
			if err != nil && res.Outcome != catalog.OutcomeRetryable {
				done <- catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: "ERR"}
				return
			}
			done <- res
		}(event)
	}
	close(start)
	<-done
	<-done
	for _, event := range []string{ev2, ev3} {
		driveCatalogToTerminal(t, env, event, 10)
	}
	if revision := catalogCategoryRevision(t, env, ids["sub"]); revision != 3 {
		t.Fatalf("final rev3: %d", revision)
	}
	if parents := catalogChildParents(t, env, ids["sub"]); len(parents) != 1 || parents[0] != ids["b"] {
		t.Fatalf("rev3 edges only: %v", parents)
	}
}

// TestCatalogConcurrentProductAndCategory proves a product projection
// racing a graph change converges without deadlock: barrier start, bounded
// drive, identical final state to serial execution.
func TestCatalogConcurrentProductAndCategory(t *testing.T) {
	env := openSaleEnv(t)
	a, b, s, product, catRev2, prodRev2 := coordinatedFixture(t, env)
	ingestProdRev2(t, env, product, b, s, prodRev2)
	_ = a
	start := make(chan struct{})
	done := make(chan catalog.ProjectResult, 2)
	go func() {
		store := catalogStore(env)
		ctx := context.Background()
		<-start
		rec, _, _ := store.LoadCatalogEvent(ctx, prodRev2)
		res, err := store.ProjectProduct(ctx, rec, time.Now())
		if err != nil && res.Outcome != catalog.OutcomeRetryable {
			done <- catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: "ERR"}
			return
		}
		done <- res
	}()
	go func() {
		store := catalogStore(env)
		ctx := context.Background()
		<-start
		rec, _, _ := store.LoadCatalogEvent(ctx, catRev2)
		res, err := store.ProjectCategory(ctx, rec, time.Now())
		if err != nil && res.Outcome != catalog.OutcomeRetryable {
			done <- catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: "ERR"}
			return
		}
		done <- res
	}()
	close(start)
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("deadlock: concurrent projections did not return")
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("deadlock: concurrent projections did not return")
	}
	driveCatalogToTerminal(t, env, prodRev2, 10)
	driveCatalogToTerminal(t, env, catRev2, 10)
	if top, revision := catalogProductTop(t, env, product); top != b || revision != 2 {
		t.Fatalf("final top B rev2: %s %d", top, revision)
	}
	if parents := catalogChildParents(t, env, s); len(parents) != 1 || parents[0] != b {
		t.Fatalf("graph S→B: %v", parents)
	}
}

// TestCatalogProductProjectionRollback injects a fault in the middle of
// product relation writes: the previous revision must remain fully intact
// (zero partial rows), processing stays retryable, and retry after fault
// removal succeeds exactly once.
func TestCatalogProductProjectionRollback(t *testing.T) {
	env := openSaleEnv(t)
	root, sub, tag := catalogProductFixture(t, env, 170, "rbk")
	productID := "e0005000-0000-4000-8000-000000000000"
	rev1 := "d0005000-0000-4000-8000-000000000000"
	ingestCatalog(t, env, rev1, catalog.EventProductSnapshotV1, "2026-09-20T10:00:00Z",
		productPayload(productID, "PAP-RBK", "one", root, []string{sub}, []string{tag}, 1))
	projectCatalogOnce(t, env, rev1)

	rev2 := "d0005001-0000-4000-8000-000000000001"
	ingestCatalog(t, env, rev2, catalog.EventProductSnapshotV1, "2026-09-20T11:00:00Z",
		productPayload(productID, "PAP-RBK", "two", root, []string{sub}, []string{tag}, 2))
	if _, err := env.pool.Exec(context.Background(),
		`CREATE FUNCTION fail_catalog_tx() RETURNS trigger AS $$
		 BEGIN RAISE EXCEPTION 'injected failure'; RETURN NEW; END; $$ LANGUAGE plpgsql;`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(context.Background(),
		`CREATE TRIGGER trg_fail_catalog_tx BEFORE INSERT ON catalog_product_translations
		 FOR EACH ROW EXECUTE FUNCTION fail_catalog_tx();`); err != nil {
		t.Fatal(err)
	}
	store := catalogStore(env)
	rec, ok, err := store.LoadCatalogEvent(context.Background(), rev2)
	if err != nil || !ok {
		t.Fatal("load")
	}
	if _, err := store.ProjectProduct(context.Background(), rec, time.Now()); err == nil {
		t.Fatal("injected failure must surface")
	}
	// Previous revision fully intact: header, prices, translations, subs, tags.
	var revision int64
	var name string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT source_revision, name FROM catalog_products WHERE product_id=$1`, productID).Scan(&revision, &name); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || name != "one" {
		t.Fatalf("rev1 intact: %d %q", revision, name)
	}
	for table, want := range map[string]int{
		"catalog_product_prices": 2, "catalog_product_translations": 1,
		"catalog_product_subcategories": 1, "catalog_product_tags": 1,
	} {
		var n int
		if err := env.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM `+table+` WHERE product_id=$1`, productID).Scan(&n); err != nil || n != want {
			t.Fatalf("%s: %d (%v), want %d", table, n, err, want)
		}
	}
	if status, _ := catalogStatus(t, env, catalog.ProcessorProductProjectionV1, rev2); status != "retry" {
		t.Fatalf("retryable: %q", status)
	}
	if _, err := env.pool.Exec(context.Background(),
		`DROP TRIGGER trg_fail_catalog_tx ON catalog_product_translations; DROP FUNCTION fail_catalog_tx();`); err != nil {
		t.Fatal(err)
	}
	forceCatalogDue(t, env, catalog.ProcessorProductProjectionV1, rev2)
	if res := projectCatalogOnce(t, env, rev2); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("retry succeeds: %+v", res)
	}
	if err := env.pool.QueryRow(context.Background(),
		`SELECT source_revision, name FROM catalog_products WHERE product_id=$1`, productID).Scan(&revision, &name); err != nil || revision != 2 || name != "two" {
		t.Fatalf("rev2 exactly once: %d %q (%v)", revision, name, err)
	}
	var headers int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM catalog_products WHERE product_id=$1`, productID).Scan(&headers); err != nil || headers != 1 {
		t.Fatalf("one header: %d (%v)", headers, err)
	}
}

// TestCatalogCategoryProjectionRollback proves a mid-transaction category
// fault leaves previous edges intact with retryable processing.
func TestCatalogCategoryProjectionRollback(t *testing.T) {
	env := openSaleEnv(t)
	ids := catalogIDs(t, 180, "a", "sub")
	ingestCatalog(t, env, "d0005100-0000-4000-8000-000000000100", catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
		categoryPayload(ids["a"], "active", map[string]string{"ar": "A"}, nil, 1))
	projectCatalogOnce(t, env, "d0005100-0000-4000-8000-000000000100")
	ingestCatalog(t, env, "d0005101-0000-4000-8000-000000000101", catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
		categoryPayload(ids["sub"], "active", map[string]string{"ar": "S"}, []string{ids["a"]}, 1))
	projectCatalogOnce(t, env, "d0005101-0000-4000-8000-000000000101")

	rev2 := "d0005102-0000-4000-8000-000000000102"
	ingestCatalog(t, env, rev2, catalog.EventCategorySnapshotV1, "2026-09-20T11:00:00Z",
		categoryPayload(ids["sub"], "hidden", map[string]string{"ar": "S"}, []string{ids["a"]}, 2))
	if _, err := env.pool.Exec(context.Background(),
		`CREATE FUNCTION fail_catalog_edge() RETURNS trigger AS $$
		 BEGIN RAISE EXCEPTION 'injected failure'; RETURN NEW; END; $$ LANGUAGE plpgsql;`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(context.Background(),
		`CREATE TRIGGER trg_fail_catalog_edge BEFORE INSERT ON catalog_category_edges
		 FOR EACH ROW EXECUTE FUNCTION fail_catalog_edge();`); err != nil {
		t.Fatal(err)
	}
	store := catalogStore(env)
	rec, ok, err := store.LoadCatalogEvent(context.Background(), rev2)
	if err != nil || !ok {
		t.Fatal("load")
	}
	if _, err := store.ProjectCategory(context.Background(), rec, time.Now()); err == nil {
		t.Fatal("injected failure must surface")
	}
	if revision := catalogCategoryRevision(t, env, ids["sub"]); revision != 1 {
		t.Fatalf("rev1 intact: %d", revision)
	}
	if parents := catalogChildParents(t, env, ids["sub"]); len(parents) != 1 || parents[0] != ids["a"] {
		t.Fatalf("edges intact: %v", parents)
	}
	if status, _ := catalogStatus(t, env, catalog.ProcessorCategoryProjectionV1, rev2); status != "retry" {
		t.Fatalf("retryable: %q", status)
	}
	if _, err := env.pool.Exec(context.Background(),
		`DROP TRIGGER trg_fail_catalog_edge ON catalog_category_edges; DROP FUNCTION fail_catalog_edge();`); err != nil {
		t.Fatal(err)
	}
	forceCatalogDue(t, env, catalog.ProcessorCategoryProjectionV1, rev2)
	if res := projectCatalogOnce(t, env, rev2); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("retry succeeds: %+v", res)
	}
}

// TestCatalogDepthDiagnostic proves depth violations report
// CATALOG_CATEGORY_DEPTH while real cycles report CATALOG_CATEGORY_CYCLE.
func TestCatalogDepthDiagnostic(t *testing.T) {
	env := openSaleEnv(t)
	ids := catalogIDs(t, 190, "root", "a", "b", "c")
	chain := []struct{ name, parent string }{
		{"root", ""}, {"a", "root"}, {"b", "a"},
	}
	for i, link := range chain {
		parents := []string{}
		if link.parent != "" {
			parents = []string{ids[link.parent]}
		}
		event := fmt.Sprintf("d00052%02d-0000-4000-8000-0000000000%02d", i, i)
		ingestCatalog(t, env, event, catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
			categoryPayload(ids[link.name], "active", map[string]string{"ar": link.name}, parents, 1))
		if res := projectCatalogOnce(t, env, event); res.Outcome != catalog.OutcomeProcessed {
			t.Fatalf("chain %s: %+v", link.name, res)
		}
	}
	deep := "d0005210-0000-4000-8000-000000000010"
	ingestCatalog(t, env, deep, catalog.EventCategorySnapshotV1, "2026-09-20T11:00:00Z",
		categoryPayload(ids["c"], "active", map[string]string{"ar": "c"}, []string{ids["b"]}, 1))
	res := projectCatalogOnce(t, env, deep)
	if res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrCatalogCategoryDepth {
		t.Fatalf("depth diagnostic: %+v", res)
	}
	if status, code := catalogStatus(t, env, catalog.ProcessorCategoryProjectionV1, deep); status != "blocked" || code != ErrCatalogCategoryDepth {
		t.Fatalf("persisted diagnostic: %q/%q", status, code)
	}
}

// installResetFault blocks exactly the post-commit product reset path:
// blocked→pending transitions on processing rows. Category and
// product projection transactions are unaffected, so this reproduces the
// reviewer M01 (reset fails, category still commits) and nothing else.
func installResetFault(t *testing.T, env *saleEnv) {
	t.Helper()
	if _, err := env.pool.Exec(context.Background(),
		`CREATE FUNCTION fail_catalog_reset() RETURNS trigger AS $$
		 BEGIN
		   IF OLD.status = 'blocked' AND NEW.status = 'pending' THEN
		     RAISE EXCEPTION 'injected reset failure';
		   END IF;
		   RETURN NEW;
		 END; $$ LANGUAGE plpgsql;`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(context.Background(),
		`CREATE TRIGGER trg_fail_catalog_reset BEFORE UPDATE ON sync_event_processing
		 FOR EACH ROW EXECUTE FUNCTION fail_catalog_reset();`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = env.pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS trg_fail_catalog_reset ON sync_event_processing;`)
		_, _ = env.pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS fail_catalog_reset();`)
	})
}

// TestCatalogResetFailureEventuallyRearmsProduct reproduces M01 exactly:
// a product blocked INVALID_RELATION stays blocked when the post-commit
// reset fails, then converges automatically through the durable fallback
// with no manual retry, no resend, no newer revision, and no second
// category commit.
func TestCatalogResetFailureEventuallyRearmsProduct(t *testing.T) {
	env := openSaleEnv(t)
	ids := catalogIDs(t, 220, "a", "b", "s")
	a, b, s := ids["a"], ids["b"], ids["s"]
	project := func(event, typ, occurred, payload string) {
		ingestCatalog(t, env, event, typ, occurred, payload)
		if res := projectCatalogOnce(t, env, event); res.Outcome != catalog.OutcomeProcessed {
			t.Fatalf("fixture %s: %+v", event, res)
		}
	}
	project("d0006000-0000-4000-8000-000000000000", catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
		categoryPayload(a, "active", map[string]string{"ar": "A"}, nil, 1))
	project("d0006001-0000-4000-8000-000000000001", catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
		categoryPayload(b, "active", map[string]string{"ar": "B"}, nil, 1))
	project("d0006002-0000-4000-8000-000000000002", catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
		categoryPayload(s, "active", map[string]string{"ar": "S"}, []string{a}, 1))
	productID := "e0006000-0000-4000-8000-000000000000"
	project("d0006003-0000-4000-8000-000000000003", catalog.EventProductSnapshotV1, "2026-09-20T10:00:00Z",
		productPayload(productID, "PAP-X", "x", b, nil, nil, 1))

	// Product rev2 wants top B + S while S is only under A: settled graph,
	// no repair accepted → terminal block.
	rev2 := "d0006004-0000-4000-8000-000000000004"
	ingestCatalog(t, env, rev2, catalog.EventProductSnapshotV1, "2026-09-20T11:00:00Z",
		productPayload(productID, "PAP-RF", "x", b, []string{s}, nil, 2))
	if res := projectCatalogOnce(t, env, rev2); res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrCatalogInvalidRelation {
		t.Fatalf("rev2 terminally blocked: %+v", res)
	}

	// Fault ONLY the post-commit reset, then repair the graph.
	installResetFault(t, env)
	rev3 := "d0006005-0000-4000-8000-000000000005"
	ingestCatalog(t, env, rev3, catalog.EventCategorySnapshotV1, "2026-09-20T12:00:00Z",
		categoryPayload(s, "active", map[string]string{"ar": "S"}, []string{a, b}, 2))
	if res := projectCatalogOnce(t, env, rev3); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("category commits despite reset failure: %+v", res)
	}
	// Graph is repaired (S under A+B) but the product is still blocked:
	// the reset was absorbed by design.
	if status, code := catalogStatus(t, env, catalog.ProcessorProductProjectionV1, rev2); status != "blocked" || code != ErrCatalogInvalidRelation {
		t.Fatalf("still blocked after failed reset: %q/%q", status, code)
	}
	if parents := catalogChildParents(t, env, s); len(parents) != 2 {
		t.Fatalf("graph repaired: %v", parents)
	}

	// Fresh store + projector instances (process-restart simulation):
	// the durable fallback re-arms the product with no manual retry,
	// no resend, no newer revision, no second category commit. The fault
	// was transient (a temporary DB failure); drop it first, exactly as
	// production recovers, then prove convergence needs nothing else.
	if _, err := env.pool.Exec(context.Background(),
		`DROP TRIGGER IF EXISTS trg_fail_catalog_reset ON sync_event_processing;
		 DROP FUNCTION IF EXISTS fail_catalog_reset();`); err != nil {
		t.Fatal(err)
	}
	fresh := NewDevices(env.pool, 5*time.Second)
	if err := fresh.RearmBlockedProducts(context.Background()); err != nil {
		t.Fatalf("fallback re-arm: %v", err)
	}
	if status, _ := catalogStatus(t, env, catalog.ProcessorProductProjectionV1, rev2); status != "pending" {
		t.Fatalf("re-armed to pending: %q", status)
	}
	drainCatalog(t, env)
	if status, code := catalogStatus(t, env, catalog.ProcessorProductProjectionV1, rev2); status != "processed" || code != "" {
		t.Fatalf("converged: %q/%q", status, code)
	}
	var top string
	var revision int64
	if err := env.pool.QueryRow(context.Background(),
		`SELECT top_category_id::text, source_revision FROM catalog_products WHERE product_id=$1`,
		productID).Scan(&top, &revision); err != nil {
		t.Fatal(err)
	}
	if top != b || revision != 2 {
		t.Fatalf("final top B rev2: %s %d", top, revision)
	}
	var subs int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM catalog_product_subcategories WHERE product_id=$1`, productID).Scan(&subs); err != nil || subs != 1 {
		t.Fatalf("final subs: %d (%v)", subs, err)
	}
}

// TestCatalogTrueInvalidNeverRearms proves the fallback does not churn
// terminally invalid products: settled-invalid with no graph advancement
// stays blocked across re-arm scans, with untouched diagnostics.
func TestCatalogTrueInvalidNeverRearms(t *testing.T) {
	env := openSaleEnv(t)
	ids := catalogIDs(t, 230, "a", "b", "s")
	project := func(event, typ, occurred, payload string) {
		ingestCatalog(t, env, event, typ, occurred, payload)
		if res := projectCatalogOnce(t, env, event); res.Outcome != catalog.OutcomeProcessed {
			t.Fatalf("fixture %s: %+v", event, res)
		}
	}
	project("d0006100-0000-4000-8000-000000000100", catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
		categoryPayload(ids["a"], "active", map[string]string{"ar": "A"}, nil, 1))
	project("d0006101-0000-4000-8000-000000000001", catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
		categoryPayload(ids["b"], "active", map[string]string{"ar": "B"}, nil, 1))
	project("d0006102-0000-4000-8000-000000000002", catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
		categoryPayload(ids["s"], "active", map[string]string{"ar": "S"}, []string{ids["a"]}, 1))
	productID := "e0006100-0000-4000-8000-000000000000"
	bad := "d0006103-0000-4000-8000-000000000003"
	ingestCatalog(t, env, bad, catalog.EventProductSnapshotV1, "2026-09-20T11:00:00Z",
		productPayload(productID, "PAP-TI", "x", ids["b"], []string{ids["s"]}, nil, 1))
	if res := projectCatalogOnce(t, env, bad); res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrCatalogInvalidRelation {
		t.Fatalf("terminally blocked: %+v", res)
	}
	var before string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT updated_at::text FROM sync_event_processing WHERE event_id=$1 AND processor='catalog_product_projection.v1'`,
		bad).Scan(&before); err != nil {
		t.Fatal(err)
	}
	// Re-arm scans ( plural: no hot loop, no state change without graph
	// advancement).
	fresh := NewDevices(env.pool, 5*time.Second)
	for i := 0; i < 3; i++ {
		if err := fresh.RearmBlockedProducts(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if status, code := catalogStatus(t, env, catalog.ProcessorProductProjectionV1, bad); status != "blocked" || code != ErrCatalogInvalidRelation {
		t.Fatalf("remains terminal: %q/%q", status, code)
	}
	var after string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT updated_at::text FROM sync_event_processing WHERE event_id=$1 AND processor='catalog_product_projection.v1'`,
		bad).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("re-arm scans must not touch settled terminal rows")
	}
	if n := saleCount(t, env.pool, "catalog_products"); n != 0 {
		t.Fatalf("zero product rows, got %d", n)
	}
}

// TestCatalogSupersededNeverRearmed proves an old blocked revision is not
// re-armed over a newer current revision: rev1 is terminally invalid (top
// points at a subcategory), then valid rev2 becomes authoritative. The
// re-arm scan must leave rev1 blocked and rev2 processed.
func TestCatalogSupersededNeverRearmed(t *testing.T) {
	env := openSaleEnv(t)
	ids := catalogIDs(t, 240, "a", "b", "s")
	project := func(event, typ, occurred, payload string) {
		ingestCatalog(t, env, event, typ, occurred, payload)
		projectCatalogOnce(t, env, event)
	}
	project("d0006200-0000-4000-8000-000000000200", catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
		categoryPayload(ids["a"], "active", map[string]string{"ar": "A"}, nil, 1))
	project("d0006201-0000-4000-8000-000000000201", catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
		categoryPayload(ids["b"], "active", map[string]string{"ar": "B"}, nil, 1))
	project("d0006202-0000-4000-8000-000000000202", catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z",
		categoryPayload(ids["s"], "active", map[string]string{"ar": "S"}, []string{ids["a"], ids["b"]}, 1))
	productID := "e0006200-0000-4000-8000-000000000000"
	// Rev1 uses the subcategory as top: structurally invalid, settled
	// (all categories current) → terminal block.
	rev1 := "d0006203-0000-4000-8000-000000000203"
	ingestCatalog(t, env, rev1, catalog.EventProductSnapshotV1, "2026-09-20T10:00:00Z",
		productPayload(productID, "PAP-SN", "x", ids["s"], nil, nil, 1))
	if res := projectCatalogOnce(t, env, rev1); res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrCatalogInvalidRelation {
		t.Fatalf("rev1 terminally blocked: %+v", res)
	}
	// Valid rev2 becomes authoritative.
	rev2 := "d0006204-0000-4000-8000-000000000204"
	ingestCatalog(t, env, rev2, catalog.EventProductSnapshotV1, "2026-09-20T11:00:00Z",
		productPayload(productID, "PAP-SN", "x", ids["b"], []string{ids["s"]}, nil, 2))
	if res := projectCatalogOnce(t, env, rev2); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("rev2 authoritative: %+v", res)
	}
	// Re-arm must not touch the superseded blocked rev1.
	fresh := NewDevices(env.pool, 5*time.Second)
	if err := fresh.RearmBlockedProducts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status, code := catalogStatus(t, env, catalog.ProcessorProductProjectionV1, rev1); status != "blocked" || code != ErrCatalogInvalidRelation {
		t.Fatalf("rev1 stays blocked: %q/%q", status, code)
	}
	if status, _ := catalogStatus(t, env, catalog.ProcessorProductProjectionV1, rev2); status != "processed" {
		t.Fatalf("rev2 stays processed: %q", status)
	}
}

// TestCatalogOldHashCompatibility proves upgrade safety (§72) with NEW
// event IDs, so the processing layer cannot short-circuit on an
// already-processed event: a projection row stored by the Phase 5A
// candidate (raw-payload hash) does not falsely conflict when R1 replays a
// semantically identical revision, because identity comes from
// reconstructed state, never the stored hash.
func TestCatalogOldHashCompatibility(t *testing.T) {
	env := openSaleEnv(t)
	ids := catalogIDs(t, 200, "root")
	first := "d0005300-0000-4000-8000-000000000000"
	payload := categoryPayload(ids["root"], "active", map[string]string{"ar": "جذر"}, nil, 1)
	ingestCatalog(t, env, first, catalog.EventCategorySnapshotV1, "2026-09-20T10:00:00Z", payload)
	if res := projectCatalogOnce(t, env, first); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("project: %+v", res)
	}
	// Simulate candidate storage: overwrite the semantic fingerprint with
	// the raw-payload hash the old code persisted (computed client-side;
	// no pgcrypto dependency).
	rawHash := sha256.Sum256([]byte(payload))
	if _, err := env.pool.Exec(context.Background(),
		`UPDATE catalog_categories SET source_payload_hash = $1 WHERE category_id = $2`,
		rawHash[:], ids["root"]); err != nil {
		t.Fatal(err)
	}
	// NEW event, same entity/revision/semantics (reordered keys): must
	// reach the equal-revision comparison and stay idempotent.
	reordered := `{"status":"active","catalog_revision":1,"category_id":"` + ids["root"] + `",` +
		`"names":[{"locale":"ar","name":"جذر"}],"parent_ids":[]}`
	replay := "d0005301-0000-4000-8000-000000000001"
	ingestCatalog(t, env, replay, catalog.EventCategorySnapshotV1, "2026-09-20T10:01:00Z", reordered)
	if res := projectCatalogOnce(t, env, replay); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("old-hash replay idempotent: %+v", res)
	}
	if status, code := catalogStatus(t, env, catalog.ProcessorCategoryProjectionV1, replay); status != "processed" || code != "" {
		t.Fatalf("clean terminal state: %q/%q", status, code)
	}
	// The stored hash is still the old raw hash: we never trusted it.
	var stored []byte
	if err := env.pool.QueryRow(context.Background(),
		`SELECT source_payload_hash FROM catalog_categories WHERE category_id=$1`, ids["root"]).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, rawHash[:]) {
		t.Fatal("stored hash must remain the untouched candidate hash")
	}
	// A genuinely different payload at the same revision still conflicts.
	rival := "d0005302-0000-4000-8000-000000000002"
	ingestCatalog(t, env, rival, catalog.EventCategorySnapshotV1, "2026-09-20T10:02:00Z",
		categoryPayload(ids["root"], "active", map[string]string{"ar": "مختلف"}, nil, 1))
	if res := projectCatalogOnce(t, env, rival); res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrCatalogRevisionConflict {
		t.Fatalf("rival conflicts: %+v", res)
	}
}
