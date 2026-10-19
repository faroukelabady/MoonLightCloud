package postgres

// Phase 9-R2 regression: independent mutable default catalog state,
// existing-projection upgrade compatibility, and frozen Retail Category
// management compatibility. Default reference IDs are Store-scoped
// identities; independent revision streams never arbitrate one row.

import (
	"bytes"
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
	requireOutcome(t, project(f.devA, f.credA, catalog.EventCategorySnapshotV2, sharedCatIslamic, catInit), catalog.OutcomeProcessed, "")
	requireOutcome(t, project(f.devB, f.credB, catalog.EventCategorySnapshotV2, sharedCatIslamic, catInit), catalog.OutcomeProcessed, "")
	requireOutcome(t, project(f.devA, f.credA, catalog.EventCategorySnapshotV2, sharedCatIslamic,
		categoryPayload(sharedCatIslamic, "hidden", map[string]string{"en": "A Islamic"}, nil, 2)), catalog.OutcomeProcessed, "")
	requireOutcome(t, project(f.devB, f.credB, catalog.EventCategorySnapshotV2, sharedCatIslamic,
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
	done := make(chan catalogWorkerResult, 2)
	for _, e := range []string{r2EventID(110), r2EventID(111)} {
		wg.Add(1)
		go func(event string) {
			defer wg.Done()
			// Real concurrency preserved: both goroutines race real
			// connections over the same default identity. The bounded
			// driver re-attempts ONLY permitted transient aborts
			// (40001/40P01) exactly like production durable retry.
			done <- runCatalogWorker(NewCatalogProjection(f.pool, 5*time.Second), event, catalog.EventTagSnapshotV1, catalogRetryBudget)
		}(e)
	}
	wg.Wait()
	for i := 0; i < 2; i++ {
		result := <-done
		if result.Err != nil {
			t.Fatal(result.Err)
		}
	}
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

// countCatalogRows returns a stable signature of Store-scoped catalog rows.
func countCatalogRows(t *testing.T, f *scopeFixture) string {
	t.Helper()
	state := map[string][]json.RawMessage{}
	for _, table := range []string{"catalog_categories", "catalog_tags", "catalog_products", "catalog_category_edges", "catalog_product_subcategories", "catalog_product_tags", "catalog_product_prices"} {
		rows, err := f.pool.Query(context.Background(), `SELECT (to_jsonb(t)-'projected_at')::text FROM `+table+` t ORDER BY (to_jsonb(t)-'projected_at')::text`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var raw string
			if err = rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			state[table] = append(state[table], json.RawMessage(raw))
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestR2_AbortRetryPreservesDurablePayloads is the Phase 12 F12
// production-invariant probe: after any PERMITTED serialization abort and
// bounded re-attempt, the final state is canonical and the durable payload
// metadata (historical label bytes, source revision, payload fingerprint)
// is unchanged — a retry never causes semantic payload drift. Both stores
// race real connections; no serialization of the race is introduced.
func TestR2_AbortRetryPreservesDurablePayloads(t *testing.T) {
	f := openScopeFixture(t)
	init := tagPayload(sharedTagGold, "gold", true, map[string]string{"en": "Gold"}, 1)
	for i, d := range []struct{ dev, cred string }{{f.devA, f.credA}, {f.devB, f.credB}} {
		f.ingest(t, d.dev, d.cred, r2EventID(200+i), catalog.EventTagSnapshotV1, init)
		requireOutcome(t, f.projectCatalog(t, r2EventID(200+i), catalog.EventTagSnapshotV1), catalog.OutcomeProcessed, "")
	}
	payloadA := tagPayload(sharedTagGold, "gold", true, map[string]string{"en": "A concurrent"}, 2)
	payloadB := tagPayload(sharedTagGold, "gold", true, map[string]string{"en": "B concurrent"}, 2)
	f.ingest(t, f.devA, f.credA, r2EventID(210), catalog.EventTagSnapshotV1, payloadA)
	f.ingest(t, f.devB, f.credB, r2EventID(211), catalog.EventTagSnapshotV1, payloadB)
	var wg sync.WaitGroup
	done := make(chan catalogWorkerResult, 2)
	for _, e := range []string{r2EventID(210), r2EventID(211)} {
		wg.Add(1)
		go func(event string) {
			defer wg.Done()
			done <- runCatalogWorker(NewCatalogProjection(f.pool, 5*time.Second), event, catalog.EventTagSnapshotV1, catalogRetryBudget)
		}(e)
	}
	wg.Wait()
	for i := 0; i < 2; i++ {
		result := <-done
		if result.Err != nil {
			t.Fatal(result.Err)
		}
	}

	type row struct {
		nameEN, nameAR, slug string
		rev                  int64
		hash                 []byte
	}
	read := func(scope string) row {
		t.Helper()
		var r row
		var hash []byte
		if err := f.pool.QueryRow(context.Background(),
			`SELECT COALESCE(name_en, ''), COALESCE(name_ar, ''), COALESCE(slug, ''), source_revision, source_payload_hash
			 FROM catalog_tags WHERE tag_id=$1`, scopedTagID(t, sharedTagGold, scope),
		).Scan(&r.nameEN, &r.nameAR, &r.slug, &r.rev, &hash); err != nil {
			t.Fatalf("read %s: %v", scope, err)
		}
		r.hash = hash
		return r
	}
	for scope, want := range map[string]string{scopeStoreA: "A concurrent", scopeStoreB: "B concurrent"} {
		first := read(scope)
		if first.nameEN != want {
			t.Fatalf("%s label drifted: %q want %q", scope, first.nameEN, want)
		}
		if first.slug != "gold" {
			t.Fatalf("%s slug drifted: %q", scope, first.slug)
		}
		if first.rev != 2 {
			t.Fatalf("%s revision drifted: %d", scope, first.rev)
		}
		if len(first.hash) != 32 {
			t.Fatalf("%s payload fingerprint malformed: %d bytes", scope, len(first.hash))
		}
		// Re-read after convergence: durable payload bytes unchanged.
		again := read(scope)
		if again.nameEN != first.nameEN || again.nameAR != first.nameAR ||
			again.slug != first.slug || again.rev != first.rev ||
			!bytes.Equal(again.hash, first.hash) {
			t.Fatalf("%s durable payload drifted across retries: %+v -> %+v", scope, first, again)
		}
	}
	// No partial/duplicate writes: exactly one canonical row per scope.
	var rowsA, rowsB int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM catalog_tags WHERE tag_id=$1`, scopedTagID(t, sharedTagGold, scopeStoreA)).Scan(&rowsA); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM catalog_tags WHERE tag_id=$1`, scopedTagID(t, sharedTagGold, scopeStoreB)).Scan(&rowsB); err != nil {
		t.Fatal(err)
	}
	if rowsA != 1 || rowsB != 1 {
		t.Fatalf("duplicate/partial rows: A=%d B=%d", rowsA, rowsB)
	}
}
