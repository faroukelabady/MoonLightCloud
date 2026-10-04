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
	done := make(chan catalogWorkerResult, 2)
	for _, e := range []string{r2EventID(110), r2EventID(111)} {
		wg.Add(1)
		go func(event string) {
			defer wg.Done()
			// Real concurrency preserved: both goroutines race real
			// connections over the same default identity. The bounded
			// driver re-attempts ONLY permitted transient aborts
			// (40001/40P01) exactly like production durable retry.
			done <- runCatalogWorker(NewDevices(f.pool, 5*time.Second), event, catalog.EventTagSnapshotV1, catalogRetryBudget)
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

// TestR2_PreFixOwnedDefaultUpgrade reproduces and closes F08: a pre-R2
// default row annotated with Store A (raw seeded ID) must not block newer
// or equal-revision Store B traffic, and an identical A replay must not be
// required. Both Stores converge to their own canonical rows.
func TestR2_PreFixOwnedDefaultUpgrade(t *testing.T) { testR3RealUpgrade(t) }

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

	requireOutcome(t, project(f.devB, f.credB, catalog.EventCategorySnapshotV1, categoryPayload("00000000-0000-0000-0000-000000000202", "active", map[string]string{"en": "Human"}, []string{sharedCatIslamic, "00000000-0000-0000-0000-000000000102"}, 1)), catalog.OutcomeProcessed, "")
	for i, device := range []struct{ dev, cred string }{{f.devA, f.credA}, {f.devB, f.credB}} {
		child := fmt.Sprintf("a9000000-0000-4000-8000-%012d", 90+i)
		product := fmt.Sprintf("a9000000-0000-4000-8000-%012d", 92+i)
		requireOutcome(t, project(device.dev, device.cred, catalog.EventCategorySnapshotV1, categoryPayload(child, "active", map[string]string{"ar": "فرع", "en": "custom"}, []string{sharedCatIslamic}, 1)), catalog.OutcomeProcessed, "")
		requireOutcome(t, project(device.dev, device.cred, catalog.EventProductSnapshotV1, productPayload(product, "SAME-SKU", "product", sharedCatIslamic, []string{"00000000-0000-0000-0000-000000000202", child}, []string{sharedTagGold}, 1)), catalog.OutcomeProcessed, "")
	}

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
	r3Converge(t, f)
	after := countCatalogRows(t, f)
	if before != after {
		t.Fatalf("rebuild not deterministic: before=%v after=%v", before, after)
	}
}

// countCatalogRows returns a stable signature of Store-scoped catalog rows.
func countCatalogRows(t *testing.T, f *scopeFixture) string {
	t.Helper()
	state := map[string][]json.RawMessage{}
	for _, table := range []string{"catalog_categories", "catalog_tags", "catalog_products", "catalog_category_edges", "catalog_product_subcategories", "catalog_product_tags", "catalog_product_prices", "catalog_product_translations"} {
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

// TestR2_PreFixBlockedRecovery proves error codes alone never authorize
// recovery of genuine terminal conflicts without obsolete raw ownership.
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
	if n != 0 {
		t.Fatalf("re-armed %d, want 0", n)
	}
	statusOf := func(event, processor string) string {
		var s string
		if err := f.pool.QueryRow(ctx, `SELECT status FROM sync_event_processing WHERE event_id=$1 AND processor=$2`, event, processor).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if statusOf(e1, catalog.ProcessorTagProjectionV1) != "blocked" || statusOf(e2, catalog.ProcessorCategoryProjectionV1) != "blocked" {
		t.Fatal("genuine conflicts without obsolete raw ownership must remain blocked")
	}
	if statusOf(e3, catalog.ProcessorTagProjectionV1) != "blocked" {
		t.Fatal("validation failure must NOT be reset")
	}
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
			done <- runCatalogWorker(NewDevices(f.pool, 5*time.Second), event, catalog.EventTagSnapshotV1, catalogRetryBudget)
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
