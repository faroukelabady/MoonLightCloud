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
