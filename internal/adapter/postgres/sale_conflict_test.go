package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
	isync "github.com/faroukelabady/MoonLightCloud/internal/sync"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

func openTestRepoOnURL(t *testing.T, url string) (*pgxpool.Pool, auth.Service) {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	h, err := auth.NewHasher(testPepperBytes)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewDevices(pool, 5*time.Second)
	svc := auth.NewService(repo, h, "", 1, clock.System{}, ids.System{})
	return pool, svc
}

func newSaleEnvOnPool(t *testing.T, pool *pgxpool.Pool, devID, credID string) *saleEnv {
	t.Helper()
	store := NewDevices(pool, 5*time.Second)
	return &saleEnv{
		pool:    pool,
		syncSvc: isync.NewService(store, clock.System{}),
		proj: sale.NewProjector(store, clock.System{},
			slog.New(slog.NewTextHandler(io.Discard, nil))),
		devID: devID, credID: credID,
	}
}

// variantSale returns a valid EGP payload with the given identities and
// unit economics (discount/tax zero, single cash payment exact).
func variantSale(t *testing.T, base, saleID, itemID, sku string, unit int64, qty int) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(base), &m); err != nil {
		t.Fatal(err)
	}
	lineTotal := unit * int64(qty)
	m["sale_id"] = saleID
	line := m["lines"].([]any)[0].(map[string]any)
	line["sale_item_id"] = itemID
	line["sku"] = sku
	line["quantity"] = float64(qty)
	line["unit_price"].(map[string]any)["amount_minor"] = float64(unit)
	line["line_total"].(map[string]any)["amount_minor"] = float64(lineTotal)
	m["totals"].(map[string]any)["subtotal"].(map[string]any)["amount_minor"] = float64(lineTotal)
	m["totals"].(map[string]any)["total"].(map[string]any)["amount_minor"] = float64(lineTotal)
	m["payments"].([]any)[0].(map[string]any)["amount"].(map[string]any)["amount_minor"] = float64(lineTotal)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func ingestRaw(t *testing.T, env *saleEnv, eventID, occurred string, payload string) {
	t.Helper()
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.finalized.v1","occurred_at":%q,"payload":%s}]}`,
		eventID, occurred, payload)
	if _, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body)); err != nil {
		t.Fatalf("ingest %s: %v", eventID, err)
	}
}

// TestConcurrentDistinctEventsSameSaleID is the CRIT-01 race proof: two
// different events carrying the same sale_id project concurrently over
// separate database connections. Exactly one owns the sale; the loser
// inserts zero children and becomes SALE_ID_CONFLICT; both inbox rows
// survive; no deadlock; no partial projection.
func TestConcurrentDistinctEventsSameSaleID(t *testing.T) {
	url := testutil.Isolated(t)
	newPool := func(t *testing.T) *pgxpool.Pool {
		p, err := pgxpool.New(context.Background(), url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	poolA := newPool(t)
	poolB := newPool(t)
	// Provision one device through poolA (shared database).
	env := &saleEnv{}
	{
		pool, authSvc := openTestRepoOnURL(t, url)
		_ = pool
		p, err := authSvc.Create(context.Background(), "shop-dev")
		if err != nil {
			t.Fatal(err)
		}
		env = newSaleEnvOnPool(t, poolA, p.Device.ID, p.Credential.ID)
	}
	_ = poolB
	base := fixture(t, "sale_egp.json")
	const rounds = 15
	for r := 0; r < rounds; r++ {
		saleID := fmt.Sprintf("aaaaaaaa-aaaa-7aaa-8aaa-%012d", r)
		e1 := fmt.Sprintf("11111111-1111-7111-8111-%012d", r)
		e2 := fmt.Sprintf("22222222-2222-7222-8222-%012d", r)
		itemA := fmt.Sprintf("aaaaaaaa-aaaa-4aaa-8aaa-%012d", r)
		itemB := fmt.Sprintf("bbbbbbbb-bbbb-4bbb-8bbb-%012d", r)
		payA := variantSale(t, base, saleID, itemA, "PAP-A", 200000, 1)
		payB := variantSale(t, base, saleID, itemB, "PAP-B", 300000, 1)
		ingestRaw(t, env, e1, "2026-09-20T10:00:00Z", payA)
		ingestRaw(t, env, e2, "2026-09-20T10:00:01Z", payB)

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
		// Barrier forces meaningful overlap of the two projections.
		start := make(chan struct{})
		var wg sync.WaitGroup
		type outcome struct {
			res sale.ProjectResult
			err error
		}
		outs := make([]outcome, 2)
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
			t.Fatalf("round %d: deadlock or hang in concurrent projection", r)
		}

		// Settle any transient serialization loser sequentially (bounded).
		stores := []interface {
			ProjectSale(context.Context, sale.EventRecord, time.Time) (sale.ProjectResult, error)
		}{storeA, storeB}
		recs := []sale.EventRecord{recA, recB}
		for i, o := range outs {
			if o.err != nil || o.res.Outcome == sale.OutcomeRetryable || o.res.Outcome == sale.OutcomeNotDue {
				for k := 0; k < 5; k++ {
					res, err := stores[i].ProjectSale(context.Background(), recs[i], time.Now().UTC())
					outs[i] = outcome{res, err}
					if err == nil && (res.Outcome == sale.OutcomeProcessed || res.Outcome == sale.OutcomeBlocked) {
						break
					}
					time.Sleep(100 * time.Millisecond)
				}
			}
		}

		// Exactly one winner (processed), one SALE_ID_CONFLICT loser.
		processed, blocked := 0, 0
		winner := ""
		for i, o := range outs {
			if o.err != nil {
				t.Fatalf("round %d: event %d unsettled: %+v err=%v", r, i, o.res, o.err)
			}
			switch o.res.Outcome {
			case sale.OutcomeProcessed:
				processed++
				winner = []string{e1, e2}[i]
			case sale.OutcomeBlocked:
				if o.res.ErrorCode != ErrSaleIDConflict {
					t.Fatalf("round %d: loser code = %q, want SALE_ID_CONFLICT", r, o.res.ErrorCode)
				}
				blocked++
			default:
				t.Fatalf("round %d: event %d outcome %v not terminal", r, i, o.res.Outcome)
			}
		}
		if processed != 1 || blocked != 1 {
			t.Fatalf("round %d: want 1 processed + 1 blocked, got %d/%d (%+v)", r, processed, blocked, outs)
		}

		// One complete coherent projection owned by the winner.
		var gotSale, gotSrc string
		if err := poolA.QueryRow(context.Background(),
			`SELECT sale_id::text, source_event_id::text FROM sales_projection WHERE sale_id=$1`, saleID).Scan(&gotSale, &gotSrc); err != nil {
			t.Fatalf("round %d: header: %v", r, err)
		}
		if gotSrc != winner {
			t.Fatalf("round %d: header owned by %s, winner %s", r, gotSrc, winner)
		}
		var lines int
		if err := poolA.QueryRow(context.Background(),
			`SELECT count(*) FROM sale_lines_projection WHERE sale_id=$1`, saleID).Scan(&lines); err != nil || lines != 1 {
			t.Fatalf("round %d: want exactly 1 line, got %d (%v)", r, lines, err)
		}
		var itemID, sku string
		var amount int64
		if err := poolA.QueryRow(context.Background(),
			`SELECT sale_item_id::text, sku FROM sale_lines_projection WHERE sale_id=$1`, saleID).Scan(&itemID, &sku); err != nil {
			t.Fatalf("round %d: line: %v", r, err)
		}
		if err := poolA.QueryRow(context.Background(),
			`SELECT amount_minor FROM sale_payments_projection WHERE sale_id=$1`, saleID).Scan(&amount); err != nil {
			t.Fatalf("round %d: payment: %v", r, err)
		}
		if winner == e1 {
			if itemID != itemA || sku != "PAP-A" || amount != 200000 {
				t.Fatalf("round %d: children not all winner A: %s %s %d", r, itemID, sku, amount)
			}
		} else {
			if itemID != itemB || sku != "PAP-B" || amount != 300000 {
				t.Fatalf("round %d: children not all winner B: %s %s %d", r, itemID, sku, amount)
			}
		}
		// Zero children from the loser: no foreign SKUs under this sale.
		var foreign int
		if err := poolA.QueryRow(context.Background(),
			`SELECT count(*) FROM sale_lines_projection WHERE sale_id=$1 AND sku NOT IN ('PAP-A','PAP-B')`, saleID).Scan(&foreign); err != nil || foreign != 0 {
			t.Fatalf("round %d: foreign children: %d (%v)", r, foreign, err)
		}
		// Both immutable inbox rows remain.
		var inbox int
		if err := poolA.QueryRow(context.Background(),
			`SELECT count(*) FROM sync_events WHERE event_id IN ($1,$2)`, e1, e2).Scan(&inbox); err != nil || inbox != 2 {
			t.Fatalf("round %d: inbox rows: %d (%v)", r, inbox, err)
		}
	}
}
