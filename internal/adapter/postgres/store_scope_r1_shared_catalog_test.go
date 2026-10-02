package postgres

// Phase 9-R1/R2 default reference-catalog identity regression: the default
// Retail catalog IDs are Store-scoped identities, so two fresh Stores
// submitting the same seeded IDs synchronize independently, each keeping
// its own mutable state. Store-created identities keep global aggregate
// identity and same-ID takeovers still conflict.

import (
	"context"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

// The exact default identities seeded by Retail migration 000001.
const (
	sharedCatIslamic = "00000000-0000-0000-0000-000000000101"
	sharedCatNature  = "00000000-0000-0000-0000-000000000201"
	sharedTagGold    = "10000000-0000-0000-0000-000000000002"
)

// scopedCatID / scopedTagID mirror the Cloud canonical mapping for a
// default reference ID under a Store, used to address DERIVED rows in
// assertions without relying on payload IDs.
func scopedCatID(t *testing.T, rawID, storeID string) string {
	t.Helper()
	s, err := parseUUID(storeID)
	if err != nil {
		t.Fatal(err)
	}
	return canonicalDefaultCategoryID(rawID, s)
}

func scopedTagID(t *testing.T, rawID, storeID string) string {
	t.Helper()
	s, err := parseUUID(storeID)
	if err != nil {
		t.Fatal(err)
	}
	return canonicalDefaultTagID(rawID, s)
}

func TestScope_SharedDefaultCatalogCoexists(t *testing.T) {
	f := openScopeFixture(t)
	names := map[string]string{"ar": "إسلامي", "en": "Islamic"}
	tagNames := map[string]string{"ar": "ذهب", "en": "Gold"}
	// Store A projects the seeded default category/tag.
	f.ingest(t, f.devA, f.credA, "e0000800-0000-4000-8000-000000000001",
		catalog.EventCategorySnapshotV1, categoryPayload(sharedCatIslamic, "active", names, nil, 1))
	f.ingest(t, f.devA, f.credA, "e0000800-0000-4000-8000-000000000002",
		catalog.EventTagSnapshotV1, tagPayload(sharedTagGold, "gold", true, tagNames, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000800-0000-4000-8000-000000000001", catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
	requireOutcome(t, f.projectCatalog(t, "e0000800-0000-4000-8000-000000000002", catalog.EventTagSnapshotV1), catalog.OutcomeProcessed, "")
	// The default ID is projected under A's own Store-scoped identity.
	catA := scopedCatID(t, sharedCatIslamic, scopeStoreA)
	tagA := scopedTagID(t, sharedTagGold, scopeStoreA)
	if got := f.rowStore(t, "catalog_categories", "category_id", catA); got == nil || *got != scopeStoreA {
		t.Fatalf("A default category must be Store-scoped: %v", got)
	}
	if got := f.rowStore(t, "catalog_tags", "tag_id", tagA); got == nil || *got != scopeStoreA {
		t.Fatalf("A default tag must be Store-scoped: %v", got)
	}

	// Store B projects the same seeded identities: no conflict and its own
	// independent rows.
	f.ingest(t, f.devB, f.credB, "e0000800-0000-4000-8000-000000000003",
		catalog.EventCategorySnapshotV1, categoryPayload(sharedCatIslamic, "active", names, nil, 1))
	f.ingest(t, f.devB, f.credB, "e0000800-0000-4000-8000-000000000004",
		catalog.EventTagSnapshotV1, tagPayload(sharedTagGold, "gold", true, tagNames, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000800-0000-4000-8000-000000000003", catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
	requireOutcome(t, f.projectCatalog(t, "e0000800-0000-4000-8000-000000000004", catalog.EventTagSnapshotV1), catalog.OutcomeProcessed, "")
	catB := scopedCatID(t, sharedCatIslamic, scopeStoreB)
	tagB := scopedTagID(t, sharedTagGold, scopeStoreB)
	if got := f.rowStore(t, "catalog_categories", "category_id", catB); got == nil || *got != scopeStoreB {
		t.Fatalf("B default category must be Store-scoped: %v", got)
	}
	if got := f.rowStore(t, "catalog_tags", "tag_id", tagB); got == nil || *got != scopeStoreB {
		t.Fatalf("B default tag must be Store-scoped: %v", got)
	}

	// Both Stores reference the default identities in their own products.
	ids := catalogIDs(t, 810, "prod-a", "prod-b")
	prodA, prodB := ids["prod-a"], ids["prod-b"]
	f.ingest(t, f.devA, f.credA, "e0000810-0000-4000-8000-000000000001",
		catalog.EventProductSnapshotV1, productPayload(prodA, "SHARED-1", "A", sharedCatIslamic, nil, []string{sharedTagGold}, 1))
	f.ingest(t, f.devB, f.credB, "e0000810-0000-4000-8000-000000000002",
		catalog.EventProductSnapshotV1, productPayload(prodB, "SHARED-1", "B", sharedCatIslamic, nil, []string{sharedTagGold}, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000810-0000-4000-8000-000000000001", catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")
	requireOutcome(t, f.projectCatalog(t, "e0000810-0000-4000-8000-000000000002", catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")
	if got := f.rowStore(t, "catalog_products", "product_id", prodA); got == nil || *got != scopeStoreA {
		t.Fatalf("A product ownership: %v", got)
	}
	if got := f.rowStore(t, "catalog_products", "product_id", prodB); got == nil || *got != scopeStoreB {
		t.Fatalf("B product ownership: %v", got)
	}
	// Each Store's product references its own canonical graph.
	var topA, topB string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT top_category_id::text FROM catalog_products WHERE product_id=$1`, prodA).Scan(&topA); err != nil || topA != catA {
		t.Fatalf("A product top: %q want %q (%v)", topA, catA, err)
	}
	if err := f.pool.QueryRow(context.Background(),
		`SELECT top_category_id::text FROM catalog_products WHERE product_id=$1`, prodB).Scan(&topB); err != nil || topB != catB {
		t.Fatalf("B product top: %q want %q (%v)", topB, catB, err)
	}

	// Independent label mutation: A changes its Gold, B keeps its own.
	f.ingest(t, f.devA, f.credA, "e0000810-0000-4000-8000-000000000004",
		catalog.EventTagSnapshotV1, tagPayload(sharedTagGold, "gold", true, map[string]string{"ar": "ذهب أ", "en": "Gold A"}, 2))
	requireOutcome(t, f.projectCatalog(t, "e0000810-0000-4000-8000-000000000004", catalog.EventTagSnapshotV1), catalog.OutcomeProcessed, "")
	var labelA, labelB string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT name_en FROM catalog_tags WHERE tag_id=$1`, tagA).Scan(&labelA); err != nil || labelA != "Gold A" {
		t.Fatalf("A label: %q (%v)", labelA, err)
	}
	if err := f.pool.QueryRow(context.Background(),
		`SELECT name_en FROM catalog_tags WHERE tag_id=$1`, tagB).Scan(&labelB); err != nil || labelB != "Gold" {
		t.Fatalf("B label unaffected: %q (%v)", labelB, err)
	}
}

// TestScope_DefaultCategoryChildUnderLocalRoot proves a Store may reparent
// a default reference category beneath its own Store-created category,
// mirroring the frozen Retail DAG contract (F09), while another Store's
// independent graph is unaffected.
func TestScope_DefaultCategoryChildUnderLocalRoot(t *testing.T) {
	f := openScopeFixture(t)
	ids := catalogIDs(t, 820, "local-root")
	localRoot := ids["local-root"]
	names := map[string]string{"en": "Nature"}
	// Store A creates a local root and moves default Nature beneath it.
	f.ingest(t, f.devA, f.credA, "e0000820-0000-4000-8000-000000000001",
		catalog.EventCategorySnapshotV1, categoryPayload(localRoot, "active", map[string]string{"en": "Local root"}, nil, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000820-0000-4000-8000-000000000001", catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
	f.ingest(t, f.devA, f.credA, "e0000820-0000-4000-8000-000000000002",
		catalog.EventCategorySnapshotV1, categoryPayload(sharedCatNature, "active", names, []string{localRoot}, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000820-0000-4000-8000-000000000002", catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")

	catNatureA := scopedCatID(t, sharedCatNature, scopeStoreA)
	var parent string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT parent_id::text FROM catalog_category_edges WHERE child_id=$1`, catNatureA).Scan(&parent); err != nil || parent != localRoot {
		t.Fatalf("A default Nature parent: %q want %q (%v)", parent, localRoot, err)
	}
	// Store B's independent default Nature stays a root.
	f.ingest(t, f.devB, f.credB, "e0000820-0000-4000-8000-000000000003",
		catalog.EventCategorySnapshotV1, categoryPayload(sharedCatNature, "active", names, nil, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000820-0000-4000-8000-000000000003", catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
	catNatureB := scopedCatID(t, sharedCatNature, scopeStoreB)
	var edges int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM catalog_category_edges WHERE child_id=$1`, catNatureB).Scan(&edges); err != nil || edges != 0 {
		t.Fatalf("B default Nature must stay root: %d (%v)", edges, err)
	}
}

// TestScope_DefaultChildAdditiveReparent proves an ADDITIVE parent change
// (a default child gains a local-root parent without dropping the existing
// parent) converges even while products reference it, mirroring the frozen
// Retail reparent that preserves product reachability (F09).
func TestScope_DefaultChildAdditiveReparent(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	ids := catalogIDs(t, 850, "local-root")
	localRoot := ids["local-root"]
	// A creates a local root; product references default Pharaonic (top)
	// and Nature (subcategory).
	f.ingest(t, f.devA, f.credA, "e0000850-0000-4000-8000-000000000001",
		catalog.EventCategorySnapshotV1, categoryPayload(localRoot, "active", map[string]string{"en": "Local root"}, nil, 1))
	f.ingest(t, f.devA, f.credA, "e0000850-0000-4000-8000-000000000002",
		catalog.EventCategorySnapshotV1, categoryPayload(sharedCatIslamic, "active", map[string]string{"en": "Islamic"}, nil, 1))
	f.ingest(t, f.devA, f.credA, "e0000850-0000-4000-8000-000000000003",
		catalog.EventCategorySnapshotV1, categoryPayload(sharedCatNature, "active", map[string]string{"en": "Nature"}, []string{sharedCatIslamic}, 1))
	f.ingest(t, f.devA, f.credA, "e0000850-0000-4000-8000-000000000004",
		catalog.EventTagSnapshotV1, tagPayload(sharedTagGold, "gold", true, map[string]string{"en": "Gold"}, 1))
	for _, e := range []string{
		"e0000850-0000-4000-8000-000000000001", "e0000850-0000-4000-8000-000000000002",
		"e0000850-0000-4000-8000-000000000003", "e0000850-0000-4000-8000-000000000004",
	} {
		typ := catalog.EventCategorySnapshotV1
		if e == "e0000850-0000-4000-8000-000000000004" {
			typ = catalog.EventTagSnapshotV1
		}
		requireOutcome(t, f.projectCatalog(t, e, typ), catalog.OutcomeProcessed, "")
	}
	prodA := "e0000850-1111-4111-8111-000000000001"
	f.ingest(t, f.devA, f.credA, "e0000850-0000-4000-8000-000000000005",
		catalog.EventProductSnapshotV1, productPayload(prodA, "F09-REPARENT", "P", sharedCatIslamic, []string{sharedCatNature}, []string{sharedTagGold}, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000850-0000-4000-8000-000000000005", catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")

	// Additive reparent: Nature gains localRoot while keeping Islamic.
	f.ingest(t, f.devA, f.credA, "e0000850-0000-4000-8000-000000000006",
		catalog.EventCategorySnapshotV1, categoryPayload(sharedCatNature, "active", map[string]string{"en": "Nature"}, []string{sharedCatIslamic, localRoot}, 2))
	requireOutcome(t, f.projectCatalog(t, "e0000850-0000-4000-8000-000000000006", catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")

	natA := scopedCatID(t, sharedCatNature, scopeStoreA)
	var parents string
	if err := f.pool.QueryRow(ctx, `SELECT string_agg(parent_id::text, ',' ORDER BY parent_id) FROM catalog_category_edges WHERE child_id=$1`, natA).Scan(&parents); err != nil {
		t.Fatal(err)
	}
	if parents != localRoot+","+scopedCatID(t, sharedCatIslamic, scopeStoreA) && parents != scopedCatID(t, sharedCatIslamic, scopeStoreA)+","+localRoot {
		t.Fatalf("Nature parents: %q (want Islamic + localRoot)", parents)
	}
	// Product remained valid and reachable.
	var top string
	if err := f.pool.QueryRow(ctx, `SELECT top_category_id::text FROM catalog_products WHERE product_id=$1`, prodA).Scan(&top); err != nil {
		t.Fatal(err)
	}
	if top != scopedCatID(t, sharedCatIslamic, scopeStoreA) {
		t.Fatalf("product top: %q", top)
	}
}

// TestScope_CategoryLabelRevisionWithProductsProjects proves an ordinary
// label/status revision of a category referenced by products is not
// mistaken for an orphaning graph change.
func TestScope_CategoryLabelRevisionWithProductsProjects(t *testing.T) {
	f := openScopeFixture(t)
	ids := catalogIDs(t, 840, "root-a", "child-a", "tag-a", "prod-a")
	rootA, childA, tagA, prodA := ids["root-a"], ids["child-a"], ids["tag-a"], ids["prod-a"]
	names := map[string]string{"ar": "قديم", "en": "Old"}
	f.ingest(t, f.devA, f.credA, "e0000840-0000-4000-8000-000000000001",
		catalog.EventCategorySnapshotV1, categoryPayload(rootA, "active", names, nil, 1))
	f.ingest(t, f.devA, f.credA, "e0000840-0000-4000-8000-000000000002",
		catalog.EventCategorySnapshotV1, categoryPayload(childA, "active", names, []string{rootA}, 1))
	f.ingest(t, f.devA, f.credA, "e0000840-0000-4000-8000-000000000003",
		catalog.EventTagSnapshotV1, tagPayload(tagA, "f04-label", true, names, 1))
	for _, tc := range []struct{ event, typ string }{
		{"e0000840-0000-4000-8000-000000000001", catalog.EventCategorySnapshotV1},
		{"e0000840-0000-4000-8000-000000000002", catalog.EventCategorySnapshotV1},
		{"e0000840-0000-4000-8000-000000000003", catalog.EventTagSnapshotV1},
	} {
		requireOutcome(t, f.projectCatalog(t, tc.event, tc.typ), catalog.OutcomeProcessed, "")
	}
	f.ingest(t, f.devA, f.credA, "e0000840-0000-4000-8000-000000000004",
		catalog.EventProductSnapshotV1, productPayload(prodA, "F04-LABEL-SKU", "P", rootA, []string{childA}, []string{tagA}, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000840-0000-4000-8000-000000000004", catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")

	// Label-only revision of the referenced root: must project.
	f.ingest(t, f.devA, f.credA, "e0000840-0000-4000-8000-000000000005",
		catalog.EventCategorySnapshotV1, categoryPayload(rootA, "active", map[string]string{"ar": "جديد", "en": "New"}, nil, 2))
	requireOutcome(t, f.projectCatalog(t, "e0000840-0000-4000-8000-000000000005", catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
	var label string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT name_en FROM catalog_categories WHERE category_id=$1`, rootA).Scan(&label); err != nil || label != "New" {
		t.Fatalf("label updated: %q (%v)", label, err)
	}
	var top string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT top_category_id::text FROM catalog_products WHERE product_id=$1`, prodA).Scan(&top); err != nil || top != rootA {
		t.Fatalf("product relation preserved: %q (%v)", top, err)
	}
}

// TestScope_SharedCatalogDoesNotWeakenOwnership proves Store-created
// identities keep global aggregate identity and same-ID takeover still
// conflicts; the default-identity scoping does not weaken Store isolation.
func TestScope_SharedCatalogDoesNotWeakenOwnership(t *testing.T) {
	f := openScopeFixture(t)
	ids := catalogIDs(t, 830, "owned-a", "owned-b")
	ownedA, ownedB := ids["owned-a"], ids["owned-b"]
	names := map[string]string{"en": "Owned"}

	// Store-created same ID across Stores still conflicts.
	f.ingest(t, f.devA, f.credA, "e0000830-0000-4000-8000-000000000001",
		catalog.EventCategorySnapshotV1, categoryPayload(ownedA, "active", names, nil, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000830-0000-4000-8000-000000000001", catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
	f.ingest(t, f.devB, f.credB, "e0000830-0000-4000-8000-000000000002",
		catalog.EventCategorySnapshotV1, categoryPayload(ownedA, "active", names, nil, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000830-0000-4000-8000-000000000002", catalog.EventCategorySnapshotV1), catalog.OutcomeBlocked, ErrStoreScopeConflict)

	// A Store-created child under a Store-created parent is still isolated:
	// B's own root does not admit A's node, and vice versa.
	f.ingest(t, f.devB, f.credB, "e0000830-0000-4000-8000-000000000003",
		catalog.EventCategorySnapshotV1, categoryPayload(ownedB, "active", names, []string{ownedA}, 1))
	res := f.projectCatalog(t, "e0000830-0000-4000-8000-000000000003", catalog.EventCategorySnapshotV1)
	if res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrStoreScopeConflict {
		t.Fatalf("cross-store owned edge must be rejected: %+v", res)
	}
}
