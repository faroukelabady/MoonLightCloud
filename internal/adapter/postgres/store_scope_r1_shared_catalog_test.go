package postgres

// Phase 9-R1 F04 regression: the default Retail reference catalog is a
// shared, Store-less namespace, so two fresh Stores submitting the same
// seeded Category/Tag identities synchronize independently. Store-created
// identities keep Store ownership and same-ID takeovers still conflict.

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
	if got := f.rowStore(t, "catalog_categories", "category_id", sharedCatIslamic); got != nil {
		t.Fatalf("shared category must be Store-less, got %v", got)
	}
	if got := f.rowStore(t, "catalog_tags", "tag_id", sharedTagGold); got != nil {
		t.Fatalf("shared tag must be Store-less, got %v", got)
	}

	// Store B projects the same identities: no STORE_SCOPE_CONFLICT.
	f.ingest(t, f.devB, f.credB, "e0000800-0000-4000-8000-000000000003",
		catalog.EventCategorySnapshotV1, categoryPayload(sharedCatIslamic, "active", names, nil, 1))
	f.ingest(t, f.devB, f.credB, "e0000800-0000-4000-8000-000000000004",
		catalog.EventTagSnapshotV1, tagPayload(sharedTagGold, "gold", true, tagNames, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000800-0000-4000-8000-000000000003", catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
	requireOutcome(t, f.projectCatalog(t, "e0000800-0000-4000-8000-000000000004", catalog.EventTagSnapshotV1), catalog.OutcomeProcessed, "")

	// Both Stores reference the shared identities in their own products.
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

	// A shared-reference child under a shared parent projects from both.
	f.ingest(t, f.devB, f.credB, "e0000810-0000-4000-8000-000000000003",
		catalog.EventCategorySnapshotV1, categoryPayload(sharedCatNature, "active", map[string]string{"en": "Nature"}, []string{sharedCatIslamic}, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000810-0000-4000-8000-000000000003", catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
	var edges int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM catalog_category_edges WHERE parent_id=$1 AND child_id=$2`, sharedCatIslamic, sharedCatNature).Scan(&edges); err != nil || edges != 1 {
		t.Fatalf("shared edge: %d (%v)", edges, err)
	}

	// Label mutation from a Store updates the shared reference row. (A tag
	// is used here because a category label change on a referenced category
	// is independently gated by the frozen cross-aggregate orphan guard.)
	f.ingest(t, f.devA, f.credA, "e0000810-0000-4000-8000-000000000004",
		catalog.EventTagSnapshotV1, tagPayload(sharedTagGold, "gold", true, map[string]string{"ar": "ذهب", "en": "Gold 2"}, 2))
	requireOutcome(t, f.projectCatalog(t, "e0000810-0000-4000-8000-000000000004", catalog.EventTagSnapshotV1), catalog.OutcomeProcessed, "")
	var label string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT name_en FROM catalog_tags WHERE tag_id=$1`, sharedTagGold).Scan(&label); err != nil || label != "Gold 2" {
		t.Fatalf("shared label updated: %q (%v)", label, err)
	}
}

// TestScope_CategoryLabelRevisionWithProductsProjects proves an ordinary
// label/status revision of a category referenced by products is not
// mistaken for an orphaning graph change: it projects, products stay valid,
// and the label mutation required by the F04 flow commits.
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

// TestScope_SharedCatalogDoesNotWeakenOwnership proves the shared namespace
// is narrow: Store-created identities still conflict on same-ID takeover,
// and a shared category may not be re-parented under a Store-owned node.
func TestScope_SharedCatalogDoesNotWeakenOwnership(t *testing.T) {
	f := openScopeFixture(t)
	ids := catalogIDs(t, 830, "owned-a", "owned-b", "shared-child")
	ownedA, ownedB, sharedChild := ids["owned-a"], ids["owned-b"], ids["shared-child"]
	names := map[string]string{"en": "Owned"}

	// Store-created same ID across Stores still conflicts.
	f.ingest(t, f.devA, f.credA, "e0000830-0000-4000-8000-000000000001",
		catalog.EventCategorySnapshotV1, categoryPayload(ownedA, "active", names, nil, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000830-0000-4000-8000-000000000001", catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
	f.ingest(t, f.devB, f.credB, "e0000830-0000-4000-8000-000000000002",
		catalog.EventCategorySnapshotV1, categoryPayload(ownedA, "active", names, nil, 1))
	requireOutcome(t, f.projectCatalog(t, "e0000830-0000-4000-8000-000000000002", catalog.EventCategorySnapshotV1), catalog.OutcomeBlocked, ErrStoreScopeConflict)

	// A shared default identity may not be re-parented under an
	// A-owned category (hijacking the global reference hierarchy).
	f.ingest(t, f.devA, f.credA, "e0000830-0000-4000-8000-000000000003",
		catalog.EventCategorySnapshotV1, categoryPayload(sharedCatNature, "active", names, []string{ownedB}, 1))
	// ownedB is not projected yet (projected via B would be ownership B, but
	// keep it unprojected so the shared-parent rule is what rejects it).
	res := f.projectCatalog(t, "e0000830-0000-4000-8000-000000000003", catalog.EventCategorySnapshotV1)
	if res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrStoreScopeConflict {
		t.Fatalf("shared category under owned parent must be rejected: %+v", res)
	}
	_ = ownedA
	_ = sharedChild
}
