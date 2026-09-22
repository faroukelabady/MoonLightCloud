package postgres

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/sale"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

func testURL(t *testing.T) string {
	t.Helper()
	return testutil.Isolated(t)
}

func openPool(t *testing.T, url string) *pgxpool.Pool {
	t.Helper()
	p, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

// raceProject runs two events concurrently on separate connections, settles
// transients, and returns the winning event ID (exactly one processed).
func raceProject(t *testing.T, poolA, poolB *pgxpool.Pool, e1, e2 string) string {
	t.Helper()
	storeA := NewDevices(poolA, 10*time.Second)
	storeB := NewDevices(poolB, 10*time.Second)
	recA, ok, err := storeA.LoadSaleEvent(context.Background(), e1)
	if err != nil || !ok {
		t.Fatal("load e1")
	}
	recB, ok, err := storeB.LoadSaleEvent(context.Background(), e2)
	if err != nil || !ok {
		t.Fatal("load e2")
	}
	start := make(chan struct{})
	type outcome struct {
		res sale.ProjectResult
		err error
	}
	outs := make([]outcome, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		outs[0].res, outs[0].err = storeA.ProjectSale(context.Background(), recA, time.Now().UTC())
	}()
	go func() {
		defer wg.Done()
		<-start
		outs[1].res, outs[1].err = storeB.ProjectSale(context.Background(), recB, time.Now().UTC())
	}()
	close(start)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("deadlock in concurrent projection")
	}
	stores := []Devices{storeA, storeB}
	recs := []sale.EventRecord{recA, recB}
	for i, o := range outs {
		if o.err != nil || (o.res.Outcome != sale.OutcomeProcessed && o.res.Outcome != sale.OutcomeBlocked) {
			for k := 0; k < 10; k++ {
				res, err := stores[i].ProjectSale(context.Background(), recs[i], time.Now().UTC())
				outs[i] = outcome{res, err}
				if err == nil && (res.Outcome == sale.OutcomeProcessed || res.Outcome == sale.OutcomeBlocked) {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
	}
	winner := ""
	for i, o := range outs {
		if o.err != nil {
			t.Fatalf("event %d unsettled: %+v %v", i, o.res, o.err)
		}
		if o.res.Outcome == sale.OutcomeProcessed {
			if winner != "" {
				t.Fatal("two winners")
			}
			winner = []string{e1, e2}[i]
		} else if o.res.Outcome != sale.OutcomeBlocked || o.res.ErrorCode != ErrSaleIDConflict {
			t.Fatalf("event %d: %+v", i, o.res)
		}
	}
	if winner == "" {
		t.Fatal("no winner")
	}
	return winner
}

// TestFirstOwnerFailureRivalConflicts is the §16 sequence: E1 claims S,
// E1's projection fails after ownership is durable, E2 arrives and must
// conflict without children, then E1 retries to processed. Ownership never
// changes hands.
func TestFirstOwnerFailureRivalConflicts(t *testing.T) {
	url := testURL(t)
	pool := openPool(t, url)
	_, authSvc := openTestRepoOnURL(t, url)
	ctx := context.Background()
	p, err := authSvc.Create(ctx, "shop-firstowner")
	if err != nil {
		t.Fatal(err)
	}
	env := newSaleEnvOnPool(t, pool, p.Device.ID, p.Credential.ID)
	base := fixture(t, "sale_egp.json")
	saleID := "5aaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa"
	e1 := "51111111-1111-7111-8111-111111111111"
	e2 := "52222222-2222-7222-8222-222222222222"
	ingestRaw(t, env, e1, "2026-09-20T10:00:00Z",
		variantSale(t, base, saleID, "5aaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "PAP-A", 200000, 1))
	ingestRaw(t, env, e2, "2026-09-20T10:00:01Z",
		variantSale(t, base, saleID, "5bbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "PAP-B", 300000, 1))

	// Sabotage child inserts: E1's projection fails AFTER owning S.
	if _, err := pool.Exec(ctx, `
		CREATE OR REPLACE FUNCTION fail_firstowner_line() RETURNS trigger AS $$
		BEGIN
			RAISE EXCEPTION 'injected failure';
			RETURN NEW;
		END; $$ LANGUAGE plpgsql;
		CREATE TRIGGER trg_fail_firstowner BEFORE INSERT ON sale_lines_projection
		FOR EACH ROW EXECUTE FUNCTION fail_firstowner_line();`); err != nil {
		t.Fatal(err)
	}
	store := NewDevices(pool, 10*time.Second)
	rec1, ok, err := store.LoadSaleEvent(ctx, e1)
	if err != nil || !ok {
		t.Fatal("load e1")
	}
	if _, err := store.ProjectSale(ctx, rec1, time.Now().UTC()); err == nil {
		t.Fatal("E1 projection must fail under sabotage")
	}
	var owner string
	if err := pool.QueryRow(ctx,
		`SELECT winning_event_id::text FROM sale_event_ownership WHERE sale_id=$1`, saleID).Scan(&owner); err != nil || owner != e1 {
		t.Fatalf("ownership must be E1 despite projection failure: %q (%v)", owner, err)
	}
	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM sync_event_processing WHERE event_id=$1`, e1).Scan(&status); err != nil || status != "retry" {
		t.Fatalf("E1 must hold durable retry: %q (%v)", status, err)
	}

	// E2 arrives while E1 is unprojected: conflicts, zero children.
	rec2, ok, err := store.LoadSaleEvent(ctx, e2)
	if err != nil || !ok {
		t.Fatal("load e2")
	}
	res2, err := store.ProjectSale(ctx, rec2, time.Now().UTC())
	if err != nil {
		t.Fatalf("E2 must resolve deterministically: %v", err)
	}
	if res2.Outcome != sale.OutcomeBlocked || res2.ErrorCode != ErrSaleIDConflict {
		t.Fatalf("E2 must conflict: %+v", res2)
	}
	var lines int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sale_lines_projection`).Scan(&lines); err != nil || lines != 0 {
		t.Fatalf("E2 must create zero children: %d (%v)", lines, err)
	}

	// Remove sabotage, E1 retries to processed; ownership never changes.
	if _, err := pool.Exec(ctx,
		`DROP TRIGGER trg_fail_firstowner ON sale_lines_projection; DROP FUNCTION fail_firstowner_line();`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE sync_event_processing SET next_attempt_at = now() - interval '1 second' WHERE event_id=$1`, e1); err != nil {
		t.Fatal(err)
	}
	res1, err := store.ProjectSale(ctx, rec1, time.Now().UTC())
	if err != nil {
		t.Fatalf("E1 retry: %v", err)
	}
	if res1.Outcome != sale.OutcomeProcessed {
		t.Fatalf("E1 must process: %+v", res1)
	}
	if err := pool.QueryRow(ctx,
		`SELECT winning_event_id::text FROM sale_event_ownership WHERE sale_id=$1`, saleID).Scan(&owner); err != nil || owner != e1 {
		t.Fatalf("ownership must remain E1: %q (%v)", owner, err)
	}
	var sku string
	var amount int64
	if err := pool.QueryRow(ctx, `SELECT sku FROM sale_lines_projection WHERE sale_id=$1`, saleID).Scan(&sku); err != nil || sku != "PAP-A" {
		t.Fatalf("winner lines: %q (%v)", sku, err)
	}
	if err := pool.QueryRow(ctx, `SELECT amount_minor FROM sale_payments_projection WHERE sale_id=$1`, saleID).Scan(&amount); err != nil || amount != 200000 {
		t.Fatalf("winner payments: %d (%v)", amount, err)
	}
}

// dumpSale renders one sale's derived rows deterministically.
func dumpSale(t *testing.T, pool *pgxpool.Pool, saleID string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var b strings.Builder
	query := func(q string, args ...any) {
		rows, err := pool.Query(ctx, q, args...)
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
	query(`SELECT sale_id::text, source_event_id::text, source_device_id::text, sale_number, channel,
		occurred_at::text, paid_at::text, shop_name_ar, shop_name_en, currency,
		subtotal_minor, discount_minor, tax_minor, total_minor, fx_base, fx_quote, fx_rate, fx_rate_microrate
		FROM sales_projection WHERE sale_id=$1`, saleID)
	query(`SELECT sale_item_id::text, position, sku, product_name, quantity, unit_price_minor, line_total_minor
		FROM sale_lines_projection WHERE sale_id=$1 ORDER BY sale_item_id::text`, saleID)
	query(`SELECT position, method, amount_minor, change_minor FROM sale_payments_projection WHERE sale_id=$1 ORDER BY position`, saleID)
	query(`SELECT sale_item_id::text, classification_kind, classification_id::text, name_ar, name_en
		FROM sale_line_classifications_projection WHERE sale_id=$1 ORDER BY sale_item_id::text, classification_kind, classification_id::text`, saleID)
	return b.String()
}

// TestConflictingRebuildPreservesWinner is the P2D-HIGH-02 proof: after a
// live concurrent conflict decides winner W, clearing ONLY derived
// projections (retaining sync_events + sale_event_ownership) and rebuilding
// — with reversed processing order and two projector instances — must elect
// the same W with byte-exact rows. The loser deterministically returns to
// SALE_ID_CONFLICT and never temporarily wins.
func TestConflictingRebuildPreservesWinner(t *testing.T) {
	url := testURL(t)
	newPool := func(t *testing.T) *pgxpool.Pool { return openPool(t, url) }
	poolA := newPool(t)
	poolB := newPool(t)
	_, authSvc := openTestRepoOnURL(t, url)
	p, err := authSvc.Create(context.Background(), "shop-rebuild")
	if err != nil {
		t.Fatal(err)
	}
	env := newSaleEnvOnPool(t, poolA, p.Device.ID, p.Credential.ID)
	base := fixture(t, "sale_egp.json")

	const rounds = 5
	for r := 0; r < rounds; r++ {
		saleID := fmt.Sprintf("bbbbbbbb-bbbb-7bbb-8bbb-%012d", r)
		e1 := fmt.Sprintf("31111111-1111-7111-8111-%012d", r)
		e2 := fmt.Sprintf("32222222-2222-7222-8222-%012d", r)
		itemA := fmt.Sprintf("3aaaaaaa-aaaa-4aaa-8aaa-%012d", r)
		itemB := fmt.Sprintf("3bbbbbbb-bbbb-4bbb-8bbb-%012d", r)
		payA := variantSale(t, base, saleID, itemA, "PAP-A", 200000, 1)
		payB := variantSale(t, base, saleID, itemB, "PAP-B", 300000, 1)
		ingestRaw(t, env, e1, "2026-09-20T10:00:00Z", payA)
		ingestRaw(t, env, e2, "2026-09-20T10:00:01Z", payB)

		// Live concurrent projection determines W.
		winner := raceProject(t, poolA, poolB, e1, e2)
		var owner string
		if err := poolA.QueryRow(context.Background(),
			`SELECT winning_event_id::text FROM sale_event_ownership WHERE sale_id=$1`, saleID).Scan(&owner); err != nil {
			t.Fatalf("round %d: ownership: %v", r, err)
		}
		if owner != winner {
			t.Fatalf("round %d: ownership %s != winner %s", r, owner, winner)
		}
		before := dumpSale(t, poolA, saleID)

		// Delete ONLY derived state; retain inbox + ownership.
		if _, err := poolA.Exec(context.Background(), `DELETE FROM sales_projection`); err != nil {
			t.Fatal(err)
		}
		if _, err := poolA.Exec(context.Background(),
			`UPDATE sync_event_processing SET status='pending', next_attempt_at=NULL,
			 attempt_count=0, processed_at=NULL, last_error_code=NULL, last_error_message=NULL
			 WHERE processor='sale_projection.v1' AND event_id IN ($1,$2)`, e1, e2); err != nil {
			t.Fatal(err)
		}

		// Rebuild with REVERSED order and TWO projector instances.
		storeA := NewDevices(poolA, 10*time.Second)
		storeB := NewDevices(poolB, 10*time.Second)
		recB, ok, err := storeB.LoadSaleEvent(context.Background(), e2)
		if err != nil || !ok {
			t.Fatal("load e2")
		}
		recA, ok, err := storeA.LoadSaleEvent(context.Background(), e1)
		if err != nil || !ok {
			t.Fatal("load e1")
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i, job := range []struct {
			s Devices
			r sale.EventRecord
		}{{storeB, recB}, {storeA, recA}} {
			wg.Add(1)
			go func(s Devices, rec sale.EventRecord) {
				defer wg.Done()
				<-start
				// Settle transient retries sequentially inside worker.
				for k := 0; k < 10; k++ {
					res, err := s.ProjectSale(context.Background(), rec, time.Now().UTC())
					if err == nil && (res.Outcome == sale.OutcomeProcessed || res.Outcome == sale.OutcomeBlocked) {
						return
					}
					time.Sleep(50 * time.Millisecond)
				}
			}(job.s, job.r)
			_ = i
		}
		close(start)
		wg.Wait()

		// Same winner, exact rows, loser still conflict.
		if err := poolA.QueryRow(context.Background(),
			`SELECT winning_event_id::text FROM sale_event_ownership WHERE sale_id=$1`, saleID).Scan(&owner); err != nil {
			t.Fatalf("round %d: re-ownership: %v", r, err)
		}
		if owner != winner {
			t.Fatalf("round %d: rebuild changed winner %s -> %s", r, winner, owner)
		}
		if after := dumpSale(t, poolA, saleID); after != before {
			t.Fatalf("round %d: rebuild mismatch:\n%s\n%s", r, before, after)
		}
		var loserStatus, loserCode string
		loser := e1
		if winner == e1 {
			loser = e2
		}
		if err := poolA.QueryRow(context.Background(),
			`SELECT status, COALESCE(last_error_code,'') FROM sync_event_processing WHERE event_id=$1`, loser).Scan(&loserStatus, &loserCode); err != nil {
			t.Fatalf("round %d: loser state: %v", r, err)
		}
		if loserStatus != "blocked" || loserCode != "SALE_ID_CONFLICT" {
			t.Fatalf("round %d: loser = %s/%s, want blocked/SALE_ID_CONFLICT", r, loserStatus, loserCode)
		}
		// Integrity query must report zero mismatches.
		var mismatches int
		if err := poolA.QueryRow(context.Background(),
			`SELECT count(*) FROM sales_projection s JOIN sale_event_ownership o USING (sale_id) WHERE s.source_event_id <> o.winning_event_id`).Scan(&mismatches); err != nil || mismatches != 0 {
			t.Fatalf("round %d: integrity mismatches: %d (%v)", r, mismatches, err)
		}
	}
}
