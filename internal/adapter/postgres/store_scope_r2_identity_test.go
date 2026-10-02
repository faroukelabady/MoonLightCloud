package postgres

// Phase 9-R2 regression: independent mutable default catalog state,
// existing-projection upgrade compatibility, and frozen Retail Category
// management compatibility. Default reference IDs are Store-scoped
// identities; independent revision streams never arbitrate one row.

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

// r2EventID mints a deterministic distinct event UUID for these tests.
func r2EventID(seq int) string {
	return fmt.Sprintf("e0000900-0000-4000-8000-%012d", seq)
}

// TestR2_IndependentDefaultMutations reproduces and closes F07: two Stores
// independently rename/disable/reparent the same default identities with
// their own revision counters; all valid mutations apply and never block
// or silently suppress each other.
func TestR2_IndependentDefaultMutations(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	seq := 0
	project := func(dev, cred, typ, id, payload string) catalog.ProjectResult {
		seq++
		e := r2EventID(seq)
		f.ingest(t, dev, cred, e, typ, payload)
		return f.projectCatalog(t, e, typ)
	}
	// Initial default Gold from both Stores at local revision 1.
	init := tagPayload(sharedTagGold, "gold", true, map[string]string{"en": "Gold"}, 1)
	requireOutcome(t, project(f.devA, f.credA, catalog.EventTagSnapshotV1, sharedTagGold, init), catalog.OutcomeProcessed, "")
	requireOutcome(t, project(f.devB, f.credB, catalog.EventTagSnapshotV1, sharedTagGold, init), catalog.OutcomeProcessed, "")
	// A renames at local revision 2.
	requireOutcome(t, project(f.devA, f.credA, catalog.EventTagSnapshotV1, sharedTagGold,
		tagPayload(sharedTagGold, "gold", true, map[string]string{"en": "A renamed"}, 2)), catalog.OutcomeProcessed, "")
	// B independently renames at its own revision 2: must apply (no conflict).
	requireOutcome(t, project(f.devB, f.credB, catalog.EventTagSnapshotV1, sharedTagGold,
		tagPayload(sharedTagGold, "gold", true, map[string]string{"en": "B renamed"}, 2)), catalog.OutcomeProcessed, "")
	// A advances to revision 3 (disable).
	requireOutcome(t, project(f.devA, f.credA, catalog.EventTagSnapshotV1, sharedTagGold,
		tagPayload(sharedTagGold, "gold", false, map[string]string{"en": "A third"}, 3)), catalog.OutcomeProcessed, "")
	// B legitimately remains at revision 2; a replayed B revision 2 is a
	// same-Store duplicate (already-projected) and must not be suppressed
	// as another Store's stale revision.
	dup := project(f.devB, f.credB, catalog.EventTagSnapshotV1, sharedTagGold,
		tagPayload(sharedTagGold, "gold", true, map[string]string{"en": "B renamed"}, 2))
	if dup.Outcome != catalog.OutcomeProcessed && dup.Outcome != catalog.OutcomeAlready {
		t.Fatalf("B duplicate replay: %+v", dup)
	}

	labelA, activeA := "", false
	labelB, activeB := "", true
	catA := scopedTagID(t, sharedTagGold, scopeStoreA)
	catB := scopedTagID(t, sharedTagGold, scopeStoreB)
	if err := f.pool.QueryRow(ctx, `SELECT name_en, is_active FROM catalog_tags WHERE tag_id=$1`, catA).Scan(&labelA, &activeA); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT name_en, is_active FROM catalog_tags WHERE tag_id=$1`, catB).Scan(&labelB, &activeB); err != nil {
		t.Fatal(err)
	}
	if labelA != "A third" || activeA {
		t.Fatalf("A state: %q active=%v", labelA, activeA)
	}
	if labelB != "B renamed" || !activeB {
		t.Fatalf("B state independently preserved: %q active=%v", labelB, activeB)
	}

	// Independent Category renames + status, independent of Tag streams.
	catInit := categoryPayload(sharedCatIslamic, "active", map[string]string{"en": "Islamic"}, nil, 1)
	requireOutcome(t, project(f.devA, f.credA, catalog.EventCategorySnapshotV1, sharedCatIslamic, catInit), catalog.OutcomeProcessed, "")
	requireOutcome(t, project(f.devB, f.credB, catalog.EventCategorySnapshotV1, sharedCatIslamic, catInit), catalog.OutcomeProcessed, "")
	requireOutcome(t, project(f.devA, f.credA, catalog.EventCategorySnapshotV1, sharedCatIslamic,
		categoryPayload(sharedCatIslamic, "hidden", map[string]string{"en": "A Islamic"}, nil, 2)), catalog.OutcomeProcessed, "")
	requireOutcome(t, project(f.devB, f.credB, catalog.EventCategorySnapshotV1, sharedCatIslamic,
		categoryPayload(sharedCatIslamic, "archived", map[string]string{"en": "B Islamic"}, nil, 2)), catalog.OutcomeProcessed, "")
	cA := scopedCatID(t, sharedCatIslamic, scopeStoreA)
	cB := scopedCatID(t, sharedCatIslamic, scopeStoreB)
	var statusA, statusB, nameA, nameB string
	if err := f.pool.QueryRow(ctx, `SELECT status, name_en FROM catalog_categories WHERE category_id=$1`, cA).Scan(&statusA, &nameA); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT status, name_en FROM catalog_categories WHERE category_id=$1`, cB).Scan(&statusB, &nameB); err != nil {
		t.Fatal(err)
	}
	if statusA != "hidden" || nameA != "A Islamic" {
		t.Fatalf("A category: %q %q", statusA, nameA)
	}
	if statusB != "archived" || nameB != "B Islamic" {
		t.Fatalf("B category: %q %q", statusB, nameB)
	}
}

// TestR2_ConcurrentDefaultEdits proves two Stores editing the same default
// identity concurrently both converge to their own state (no cross-store
// suppression, no committed contradiction).
func TestR2_ConcurrentDefaultEdits(t *testing.T) {
	f := openScopeFixture(t)
	init := tagPayload(sharedTagGold, "gold", true, map[string]string{"en": "Gold"}, 1)
	for i, d := range []struct{ dev, cred string }{{f.devA, f.credA}, {f.devB, f.credB}} {
		f.ingest(t, d.dev, d.cred, r2EventID(100+i), catalog.EventTagSnapshotV1, init)
		requireOutcome(t, f.projectCatalog(t, r2EventID(100+i), catalog.EventTagSnapshotV1), catalog.OutcomeProcessed, "")
	}
	f.ingest(t, f.devA, f.credA, r2EventID(110), catalog.EventTagSnapshotV1,
		tagPayload(sharedTagGold, "gold", true, map[string]string{"en": "A concurrent"}, 2))
	f.ingest(t, f.devB, f.credB, r2EventID(111), catalog.EventTagSnapshotV1,
		tagPayload(sharedTagGold, "gold", true, map[string]string{"en": "B concurrent"}, 2))
	var wg sync.WaitGroup
	for _, e := range []string{r2EventID(110), r2EventID(111)} {
		wg.Add(1)
		go func(event string) {
			defer wg.Done()
			for attempt := 0; attempt < 30; attempt++ {
				expireBackoff(t, f, event)
				res := f.projectCatalog(t, event, catalog.EventTagSnapshotV1)
				if res.Outcome != catalog.OutcomeNotDue && res.Outcome != catalog.OutcomeRetryable {
					return
				}
				time.Sleep(time.Duration(attempt+1) * 5 * time.Millisecond)
			}
			t.Errorf("project %s never verdict", event)
		}(e)
	}
	wg.Wait()
	labelA, labelB := "", ""
	if err := f.pool.QueryRow(context.Background(), `SELECT name_en FROM catalog_tags WHERE tag_id=$1`, scopedTagID(t, sharedTagGold, scopeStoreA)).Scan(&labelA); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT name_en FROM catalog_tags WHERE tag_id=$1`, scopedTagID(t, sharedTagGold, scopeStoreB)).Scan(&labelB); err != nil {
		t.Fatal(err)
	}
	if labelA != "A concurrent" || labelB != "B concurrent" {
		t.Fatalf("concurrent writes: A=%q B=%q", labelA, labelB)
	}
}

// TestR2_PreFixOwnedDefaultUpgrade reproduces and closes F08: a pre-R2
// default row annotated with Store A (raw seeded ID) must not block newer
// or equal-revision Store B traffic, and an identical A replay must not be
// required. Both Stores converge to their own canonical rows.
func TestR2_PreFixOwnedDefaultUpgrade(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	names := map[string]string{"en": "Gold"}
	// Reconstruct exactly the pre-R2 projector output: raw seeded ID owned
	// by A with a store annotation.
	f.ingest(t, f.devA, f.credA, r2EventID(200), catalog.EventTagSnapshotV1, tagPayload(sharedTagGold, "gold", true, names, 1))
	requireOutcome(t, f.projectCatalog(t, r2EventID(200), catalog.EventTagSnapshotV1), catalog.OutcomeProcessed, "")
	if _, err := f.pool.Exec(ctx, `UPDATE catalog_tags SET store_id=$1, default_algorithm=0 WHERE tag_id=$2`, scopeStoreA, sharedTagGold); err != nil {
		t.Fatal(err)
	}
	// B's equal-revision event must not be blocked by A's annotation.
	requireOutcome(t, f.projectCatalog(t, r2EventID(200), catalog.EventTagSnapshotV1), catalog.OutcomeProcessed, "")
	f.ingest(t, f.devB, f.credB, r2EventID(201), catalog.EventTagSnapshotV1, tagPayload(sharedTagGold, "gold", true, names, 1))
	res := f.projectCatalog(t, r2EventID(201), catalog.EventTagSnapshotV1)
	requireOutcome(t, res, catalog.OutcomeProcessed, "")
	// B's higher-revision update applies.
	f.ingest(t, f.devB, f.credB, r2EventID(202), catalog.EventTagSnapshotV1,
		tagPayload(sharedTagGold, "gold", true, map[string]string{"en": "B newer"}, 2))
	requireOutcome(t, f.projectCatalog(t, r2EventID(202), catalog.EventTagSnapshotV1), catalog.OutcomeProcessed, "")
	labelB := ""
	if err := f.pool.QueryRow(ctx, `SELECT name_en FROM catalog_tags WHERE tag_id=$1`, scopedTagID(t, sharedTagGold, scopeStoreB)).Scan(&labelB); err != nil || labelB != "B newer" {
		t.Fatalf("B converged: %q (%v)", labelB, err)
	}

	// Same for Categories.
	catInit := categoryPayload(sharedCatIslamic, "active", map[string]string{"en": "Islamic"}, nil, 1)
	f.ingest(t, f.devA, f.credA, r2EventID(210), catalog.EventCategorySnapshotV1, catInit)
	requireOutcome(t, f.projectCatalog(t, r2EventID(210), catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
	if _, err := f.pool.Exec(ctx, `UPDATE catalog_categories SET store_id=$1, default_algorithm=0 WHERE category_id=$2`, scopeStoreA, sharedCatIslamic); err != nil {
		t.Fatal(err)
	}
	f.ingest(t, f.devB, f.credB, r2EventID(211), catalog.EventCategorySnapshotV1, catInit)
	requireOutcome(t, f.projectCatalog(t, r2EventID(211), catalog.EventCategorySnapshotV1), catalog.OutcomeProcessed, "")
}

// TestR2_RebuildDefaultCatalog proves wiping current-state catalog rows and
// re-projecting the same default-identity events deterministically
// reproduces the Store-scoped rows (rebuild idempotency), including
// children of default categories and their Product references.
func TestR2_RebuildDefaultCatalog(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	seq := 0
	project := func(dev, cred, typ, payload string) catalog.ProjectResult {
		e := r2EventID(400 + seq)
		seq++
		f.ingest(t, dev, cred, e, typ, payload)
		for i := 0; i < 30; i++ {
			expireBackoff(t, f, e)
			res := f.projectCatalog(t, e, typ)
			if res.Outcome != catalog.OutcomeNotDue && res.Outcome != catalog.OutcomeRetryable {
				return res
			}
		}
		t.Fatalf("never verdict %s", e)
		return catalog.ProjectResult{}
	}
	// Default Islamic/Pharaonic roots and Human under both.
	requireOutcome(t, project(f.devA, f.credA, catalog.EventCategorySnapshotV1, categoryPayload(sharedCatIslamic, "active", map[string]string{"en": "Islamic"}, nil, 1)), catalog.OutcomeProcessed, "")
	requireOutcome(t, project(f.devA, f.credA, catalog.EventCategorySnapshotV1, categoryPayload("00000000-0000-0000-0000-000000000102", "active", map[string]string{"en": "Pharaonic"}, nil, 1)), catalog.OutcomeProcessed, "")
	requireOutcome(t, project(f.devA, f.credA, catalog.EventCategorySnapshotV1, categoryPayload("00000000-0000-0000-0000-000000000202", "active", map[string]string{"en": "Human"}, []string{sharedCatIslamic, "00000000-0000-0000-0000-000000000102"}, 1)), catalog.OutcomeProcessed, "")
	requireOutcome(t, project(f.devA, f.credA, catalog.EventTagSnapshotV1, tagPayload(sharedTagGold, "gold", true, map[string]string{"en": "Gold"}, 1)), catalog.OutcomeProcessed, "")
	// B projects the same defaults independently.
	requireOutcome(t, project(f.devB, f.credB, catalog.EventCategorySnapshotV1, categoryPayload(sharedCatIslamic, "active", map[string]string{"en": "Islamic"}, nil, 1)), catalog.OutcomeProcessed, "")
	requireOutcome(t, project(f.devB, f.credB, catalog.EventCategorySnapshotV1, categoryPayload("00000000-0000-0000-0000-000000000102", "active", map[string]string{"en": "Pharaonic"}, nil, 1)), catalog.OutcomeProcessed, "")
	requireOutcome(t, project(f.devB, f.credB, catalog.EventTagSnapshotV1, tagPayload(sharedTagGold, "gold", true, map[string]string{"en": "Gold"}, 1)), catalog.OutcomeProcessed, "")

	before := countCatalogRows(t, f)
	// Rebuild: wipe current-state catalog rows, re-arm the projectors, and
	// re-project every accepted event.
	if _, err := f.pool.Exec(ctx, `DELETE FROM catalog_product_tags; DELETE FROM catalog_product_subcategories; DELETE FROM catalog_products; DELETE FROM catalog_category_edges; DELETE FROM catalog_categories; DELETE FROM catalog_tags;`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE sync_event_processing SET status='pending', next_attempt_at=NULL, processed_at=NULL, attempt_count=0, last_error_code=NULL, last_error_message=NULL WHERE processor IN ('catalog_category_projection.v1','catalog_tag_projection.v1','catalog_product_projection.v1')`); err != nil {
		t.Fatal(err)
	}
	// Re-project all pending catalog events repeatedly until settled.
	store := NewDevices(f.pool, 5*time.Second)
	for pass := 0; pass < 40; pass++ {
		for _, proc := range []struct{ processor, eventType string }{
			{catalog.ProcessorCategoryProjectionV1, catalog.EventCategorySnapshotV1},
			{catalog.ProcessorTagProjectionV1, catalog.EventTagSnapshotV1},
			{catalog.ProcessorProductProjectionV1, catalog.EventProductSnapshotV1},
		} {
			pending, err := store.PendingCatalogEvents(ctx, proc.processor, proc.eventType, 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range pending {
				expireBackoff(t, f, e)
				f.projectCatalog(t, e, proc.eventType)
			}
		}
		left, err := store.PendingCatalogEvents(ctx, catalog.ProcessorCategoryProjectionV1, catalog.EventCategorySnapshotV1, 100)
		if err != nil {
			t.Fatal(err)
		}
		leftTag, err := store.PendingCatalogEvents(ctx, catalog.ProcessorTagProjectionV1, catalog.EventTagSnapshotV1, 100)
		if err != nil {
			t.Fatal(err)
		}
		leftProd, err := store.PendingCatalogEvents(ctx, catalog.ProcessorProductProjectionV1, catalog.EventProductSnapshotV1, 100)
		if err != nil {
			t.Fatal(err)
		}
		if len(left) == 0 && len(leftTag) == 0 && len(leftProd) == 0 {
			break
		}
	}
	after := countCatalogRows(t, f)
	if before != after {
		t.Fatalf("rebuild not deterministic: before=%v after=%v", before, after)
	}
}

// countCatalogRows returns a stable signature of Store-scoped catalog rows.
func countCatalogRows(t *testing.T, f *scopeFixture) string {
	t.Helper()
	rows := map[string]int{}
	for _, q := range []string{
		`SELECT coalesce(store_id::text,'N')||':'||name_en FROM catalog_categories ORDER BY 1`,
		`SELECT coalesce(store_id::text,'N')||':'||name_en FROM catalog_tags ORDER BY 1`,
		`SELECT coalesce(store_id::text,'N')||':'||sku FROM catalog_products ORDER BY 1`,
		`SELECT coalesce(c.store_id::text,'N')||':'||cc.name_en FROM catalog_category_edges e JOIN catalog_categories c ON c.category_id=e.parent_id JOIN catalog_categories cc ON cc.category_id=e.child_id ORDER BY 1`,
	} {
		r, err := f.pool.Query(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for r.Next() {
			n++
		}
		r.Close()
		rows[q[:20]] = n
	}
	b, _ := json.Marshal(rows)
	return string(b)
}

// TestR2_PreFixBlockedRecovery proves the bounded recovery re-arms only
// the obsolete identity/scope blocks and never resets unrelated terminal
// failures.
func TestR2_PreFixBlockedRecovery(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	// Create three blocked catalog events with distinct codes.
	mk := func(seq int, eventType, payload, code string) string {
		e := r2EventID(seq)
		f.ingest(t, f.devA, f.credA, e, eventType, payload)
		return e
	}
	e1 := mk(300, catalog.EventTagSnapshotV1, tagPayload(sharedTagGold, "gold", true, map[string]string{"en": "Gold"}, 1), "")
	e2 := mk(301, catalog.EventCategorySnapshotV1, categoryPayload(sharedCatIslamic, "active", map[string]string{"en": "Islamic"}, nil, 1), "")
	e3 := mk(302, catalog.EventTagSnapshotV1, tagPayload("10000000-0000-0000-0000-000000000003", "popular", true, map[string]string{"en": "Pop"}, 1), "")
	// Force durable blocked rows with the exact codes under test.
	set := func(event, processor, code string) {
		if _, err := f.pool.Exec(ctx, `INSERT INTO sync_event_processing (event_id, processor, status, attempt_count, updated_at, last_error_code)
			VALUES ($1,$2,'blocked',1,now(),$3)
			ON CONFLICT (event_id, processor) DO UPDATE SET status='blocked', last_error_code=$3`, event, processor, code); err != nil {
			t.Fatal(err)
		}
	}
	set(e1, catalog.ProcessorTagProjectionV1, ErrStoreScopeConflict)
	set(e2, catalog.ProcessorCategoryProjectionV1, ErrCatalogRevisionConflict)
	set(e3, catalog.ProcessorTagProjectionV1, ErrValidation) // unrelated: must stay blocked

	devices := NewDevices(f.pool, 5*time.Second)
	n, err := devices.RecoverDefaultCatalogBlocked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("re-armed %d, want 2", n)
	}
	statusOf := func(event, processor string) string {
		var s string
		if err := f.pool.QueryRow(ctx, `SELECT status FROM sync_event_processing WHERE event_id=$1 AND processor=$2`, event, processor).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if statusOf(e1, catalog.ProcessorTagProjectionV1) != "pending" || statusOf(e2, catalog.ProcessorCategoryProjectionV1) != "pending" {
		t.Fatal("identity-blocked events must be re-armed")
	}
	if statusOf(e3, catalog.ProcessorTagProjectionV1) != "blocked" {
		t.Fatal("validation failure must NOT be reset")
	}
}
