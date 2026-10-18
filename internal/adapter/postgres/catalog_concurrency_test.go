package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

// driveCatalogToTerminal projects one event to a terminal outcome,
// defeating backoff horizons deterministically and tolerating transient
// serialization aborts with bounded retries. It returns the number of
// rounds used; runaway ping-pong fails the test instead of hanging it.
func driveCatalogToTerminal(t *testing.T, env *saleEnv, eventID string, maxRounds int) (catalog.ProjectResult, int) {
	t.Helper()
	result := runCatalogWorker(catalogStore(env), eventID, "", maxRounds)
	reportCatalogWorkerError(t, result.Err)
	return result.Result, result.Attempts
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
		ingestCatalog(t, env, event, catalog.EventProductSnapshotV2, "2026-09-20T12:00:00Z",
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
	if product.Revision != 6 || product.NameAR != "six" {
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
		ingestCatalog(t, env, event, catalog.EventProductSnapshotV2, "2026-09-20T12:00:00Z",
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
		ingestCatalog(t, env, event, catalog.EventCategorySnapshotV2, "2026-09-20T10:00:00Z",
			categoryPayload(ids[name], "active", map[string]string{"ar": name}, nil, 1))
		projectCatalogOnce(t, env, event)
	}
	mk := func(parents []string, revision int64, num string) string {
		event := fmt.Sprintf("d00042%s-0000-4000-8000-0000000000%s", num, num)
		ingestCatalog(t, env, event, catalog.EventCategorySnapshotV2, "2026-09-20T12:00:00Z",
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
	ingestCatalog(t, env, rev1, catalog.EventProductSnapshotV2, "2026-09-20T10:00:00Z",
		productPayload(productID, "PAP-RBK", "one", root, []string{sub}, []string{tag}, 1))
	projectCatalogOnce(t, env, rev1)

	rev2 := "d0005001-0000-4000-8000-000000000001"
	ingestCatalog(t, env, rev2, catalog.EventProductSnapshotV2, "2026-09-20T11:00:00Z",
		productPayload(productID, "PAP-RBK", "two", root, []string{sub}, []string{tag}, 2))
	if _, err := env.pool.Exec(context.Background(),
		`CREATE FUNCTION fail_catalog_tx() RETURNS trigger AS $$
		 BEGIN RAISE EXCEPTION 'injected failure'; RETURN NEW; END; $$ LANGUAGE plpgsql;`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(context.Background(),
		`CREATE TRIGGER trg_fail_catalog_tx BEFORE INSERT ON catalog_product_subcategories
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
		`SELECT source_revision, name_ar FROM catalog_products WHERE product_id=$1`, productID).Scan(&revision, &name); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || name != "one" {
		t.Fatalf("rev1 intact: %d %q", revision, name)
	}
	for table, want := range map[string]int{
		"catalog_product_prices":        2,
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
		`DROP TRIGGER trg_fail_catalog_tx ON catalog_product_subcategories; DROP FUNCTION fail_catalog_tx();`); err != nil {
		t.Fatal(err)
	}
	forceCatalogDue(t, env, catalog.ProcessorProductProjectionV1, rev2)
	if res := projectCatalogOnce(t, env, rev2); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("retry succeeds: %+v", res)
	}
	if err := env.pool.QueryRow(context.Background(),
		`SELECT source_revision, name_ar FROM catalog_products WHERE product_id=$1`, productID).Scan(&revision, &name); err != nil || revision != 2 || name != "two" {
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
	ingestCatalog(t, env, "d0005100-0000-4000-8000-000000000100", catalog.EventCategorySnapshotV2, "2026-09-20T10:00:00Z",
		categoryPayload(ids["a"], "active", map[string]string{"ar": "A"}, nil, 1))
	projectCatalogOnce(t, env, "d0005100-0000-4000-8000-000000000100")
	ingestCatalog(t, env, "d0005101-0000-4000-8000-000000000101", catalog.EventCategorySnapshotV2, "2026-09-20T10:00:00Z",
		categoryPayload(ids["sub"], "active", map[string]string{"ar": "S"}, []string{ids["a"]}, 1))
	projectCatalogOnce(t, env, "d0005101-0000-4000-8000-000000000101")

	rev2 := "d0005102-0000-4000-8000-000000000102"
	ingestCatalog(t, env, rev2, catalog.EventCategorySnapshotV2, "2026-09-20T11:00:00Z",
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
