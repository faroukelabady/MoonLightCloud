package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

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
	project("d0003000-0000-4000-8000-000000000000", catalog.EventCategorySnapshotV2, "2026-09-20T10:00:00Z",
		categoryPayload(a, "active", map[string]string{"ar": "A"}, nil, 1))
	project("d0003001-0000-4000-8000-000000000001", catalog.EventCategorySnapshotV2, "2026-09-20T10:00:00Z",
		categoryPayload(b, "active", map[string]string{"ar": "B"}, nil, 1))
	project("d0003002-0000-4000-8000-000000000002", catalog.EventCategorySnapshotV2, "2026-09-20T10:00:00Z",
		categoryPayload(s, "active", map[string]string{"ar": "S"}, []string{a}, 1))
	product = "e0003000-0000-4000-8000-000000000000"
	project("d0003003-0000-4000-8000-000000000003", catalog.EventProductSnapshotV2, "2026-09-20T10:00:00Z",
		productPayload(product, "PAP-C", "x", a, []string{s}, nil, 1))
	catRev2 = "d0003004-0000-4000-8000-000000000004"
	ingestCatalog(t, env, catRev2, catalog.EventCategorySnapshotV2, "2026-09-20T11:00:00Z",
		categoryPayload(s, "active", map[string]string{"ar": "S"}, []string{b}, 2))
	prodRev2 = "d0003005-0000-4000-8000-000000000005"
	return a, b, s, product, catRev2, prodRev2
}

// ingestProdRev2 accepts the coordinated product repair event. Tests that
// need an accepted repair call this explicitly; the orphan test omits it
// to prove a repair-less graph change is rejected.
func ingestProdRev2(t *testing.T, env *saleEnv, product, b, s, prodRev2 string) {
	t.Helper()
	ingestCatalog(t, env, prodRev2, catalog.EventProductSnapshotV2, "2026-09-20T11:00:00Z",
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
	ingestCatalog(t, env, orphan, catalog.EventCategorySnapshotV2, "2026-09-20T12:00:00Z",
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
		"catalog_product_tags", "catalog_product_subcategories",
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
			case catalog.EventCategorySnapshotV2:
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
		ingestCatalog(t, env, event, catalog.EventCategorySnapshotV2, "2026-09-20T10:00:00Z",
			categoryPayload(ids[link.name], "active", map[string]string{"ar": link.name}, parents, 1))
		if res := projectCatalogOnce(t, env, event); res.Outcome != catalog.OutcomeProcessed {
			t.Fatalf("chain %s: %+v", link.name, res)
		}
	}
	deep := "d0005210-0000-4000-8000-000000000010"
	ingestCatalog(t, env, deep, catalog.EventCategorySnapshotV2, "2026-09-20T11:00:00Z",
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
	project("d0006000-0000-4000-8000-000000000000", catalog.EventCategorySnapshotV2, "2026-09-20T10:00:00Z",
		categoryPayload(a, "active", map[string]string{"ar": "A"}, nil, 1))
	project("d0006001-0000-4000-8000-000000000001", catalog.EventCategorySnapshotV2, "2026-09-20T10:00:00Z",
		categoryPayload(b, "active", map[string]string{"ar": "B"}, nil, 1))
	project("d0006002-0000-4000-8000-000000000002", catalog.EventCategorySnapshotV2, "2026-09-20T10:00:00Z",
		categoryPayload(s, "active", map[string]string{"ar": "S"}, []string{a}, 1))
	productID := "e0006000-0000-4000-8000-000000000000"
	project("d0006003-0000-4000-8000-000000000003", catalog.EventProductSnapshotV2, "2026-09-20T10:00:00Z",
		productPayload(productID, "PAP-X", "x", b, nil, nil, 1))

	// Product rev2 wants top B + S while S is only under A: settled graph,
	// no repair accepted → terminal block.
	rev2 := "d0006004-0000-4000-8000-000000000004"
	ingestCatalog(t, env, rev2, catalog.EventProductSnapshotV2, "2026-09-20T11:00:00Z",
		productPayload(productID, "PAP-RF", "x", b, []string{s}, nil, 2))
	if res := projectCatalogOnce(t, env, rev2); res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrCatalogInvalidRelation {
		t.Fatalf("rev2 terminally blocked: %+v", res)
	}

	// Fault ONLY the post-commit reset, then repair the graph.
	installResetFault(t, env)
	rev3 := "d0006005-0000-4000-8000-000000000005"
	ingestCatalog(t, env, rev3, catalog.EventCategorySnapshotV2, "2026-09-20T12:00:00Z",
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
	project("d0006100-0000-4000-8000-000000000100", catalog.EventCategorySnapshotV2, "2026-09-20T10:00:00Z",
		categoryPayload(ids["a"], "active", map[string]string{"ar": "A"}, nil, 1))
	project("d0006101-0000-4000-8000-000000000001", catalog.EventCategorySnapshotV2, "2026-09-20T10:00:00Z",
		categoryPayload(ids["b"], "active", map[string]string{"ar": "B"}, nil, 1))
	project("d0006102-0000-4000-8000-000000000002", catalog.EventCategorySnapshotV2, "2026-09-20T10:00:00Z",
		categoryPayload(ids["s"], "active", map[string]string{"ar": "S"}, []string{ids["a"]}, 1))
	productID := "e0006100-0000-4000-8000-000000000000"
	bad := "d0006103-0000-4000-8000-000000000003"
	ingestCatalog(t, env, bad, catalog.EventProductSnapshotV2, "2026-09-20T11:00:00Z",
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
	project("d0006200-0000-4000-8000-000000000200", catalog.EventCategorySnapshotV2, "2026-09-20T10:00:00Z",
		categoryPayload(ids["a"], "active", map[string]string{"ar": "A"}, nil, 1))
	project("d0006201-0000-4000-8000-000000000201", catalog.EventCategorySnapshotV2, "2026-09-20T10:00:00Z",
		categoryPayload(ids["b"], "active", map[string]string{"ar": "B"}, nil, 1))
	project("d0006202-0000-4000-8000-000000000202", catalog.EventCategorySnapshotV2, "2026-09-20T10:00:00Z",
		categoryPayload(ids["s"], "active", map[string]string{"ar": "S"}, []string{ids["a"], ids["b"]}, 1))
	productID := "e0006200-0000-4000-8000-000000000000"
	// Rev1 uses the subcategory as top: structurally invalid, settled
	// (all categories current) → terminal block.
	rev1 := "d0006203-0000-4000-8000-000000000203"
	ingestCatalog(t, env, rev1, catalog.EventProductSnapshotV2, "2026-09-20T10:00:00Z",
		productPayload(productID, "PAP-SN", "x", ids["s"], nil, nil, 1))
	if res := projectCatalogOnce(t, env, rev1); res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrCatalogInvalidRelation {
		t.Fatalf("rev1 terminally blocked: %+v", res)
	}
	// Valid rev2 becomes authoritative.
	rev2 := "d0006204-0000-4000-8000-000000000204"
	ingestCatalog(t, env, rev2, catalog.EventProductSnapshotV2, "2026-09-20T11:00:00Z",
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
	ingestCatalog(t, env, first, catalog.EventCategorySnapshotV2, "2026-09-20T10:00:00Z", payload)
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
		`"name_ar":"جذر","name_en":null,"parent_ids":[],"online_enabled":true}`
	replay := "d0005301-0000-4000-8000-000000000001"
	ingestCatalog(t, env, replay, catalog.EventCategorySnapshotV2, "2026-09-20T10:01:00Z", reordered)
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
	ingestCatalog(t, env, rival, catalog.EventCategorySnapshotV2, "2026-09-20T10:02:00Z",
		categoryPayload(ids["root"], "active", map[string]string{"ar": "مختلف"}, nil, 1))
	if res := projectCatalogOnce(t, env, rival); res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrCatalogRevisionConflict {
		t.Fatalf("rival conflicts: %+v", res)
	}
}
