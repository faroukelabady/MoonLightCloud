package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/sale"
)

func replayEventID(r int) string { return fmt.Sprintf("41ffffff-1111-7111-8111-%012x", r) }

// TestConcurrentSameEventReplay is the P2E-MED-01 high-count proof: the same
// event (ID, sale, payload) projected concurrently on two connections must
// converge idempotently every time — one owner, one logical projection, no
// transient arbitration error, no retry pollution, no false conflict, no
// OutcomeNotDue from peer retry mutation.
func TestConcurrentSameEventReplay(t *testing.T) {
	url := testURL(t)
	poolA := openPool(t, url)
	poolB := openPool(t, url)
	_, authSvc := openTestRepoOnURL(t, url)
	p, err := authSvc.Create(context.Background(), "shop-replay")
	if err != nil {
		t.Fatal(err)
	}
	env := newSaleEnvOnPool(t, poolA, p.Device.ID, p.Credential.ID)
	base := fixture(t, "sale_egp.json")

	// Canonical CI count: barrier synchronization makes each round a real
	// race; targeted stress uses -count / higher N via TEST_REPLAY_ROUNDS.
	const rounds = 100
	for r := 0; r < rounds; r++ {
		e1 := replayEventID(r)
		saleID := fmt.Sprintf("42ffffff-aaaa-7aaa-8aaa-%012x", r)
		itemID := fmt.Sprintf("42ffffff-bbbb-4bbb-8bbb-%012x", r)
		payload := variantSale(t, base, saleID, itemID, "PAP-R", 200000, 1)
		ingestRaw(t, env, e1, "2026-09-20T10:00:00Z", payload)
		storeA := NewDevices(poolA, 10*time.Second)
		storeB := NewDevices(poolB, 10*time.Second)
		recA, ok, err := storeA.LoadSaleEvent(context.Background(), e1)
		if err != nil || !ok {
			t.Fatalf("round %d: load", r)
		}
		recB, ok, err := storeB.LoadSaleEvent(context.Background(), e1)
		if err != nil || !ok {
			t.Fatalf("round %d: load", r)
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
			t.Fatalf("round %d: deadlock in concurrent replay", r)
		}
		for i, o := range outs {
			if o.err != nil {
				t.Fatalf("round %d replay %d: error %v (no transient allowed)", r, i, o.err)
			}
			if o.res.Outcome != sale.OutcomeProcessed {
				t.Fatalf("round %d replay %d: %+v, want processed", r, i, o.res)
			}
		}
		ctx := context.Background()
		var owner string
		var ownedSale string
		if err := poolA.QueryRow(ctx,
			`SELECT sale_id::text, winning_event_id::text FROM sale_event_ownership WHERE winning_event_id=$1`, e1).Scan(&ownedSale, &owner); err != nil {
			t.Fatalf("round %d: ownership: %v", r, err)
		}
		if owner != e1 {
			t.Fatalf("round %d: owner %q", r, owner)
		}
		checks := []struct {
			name  string
			query string
			args  []any
			want  int
		}{
			{"ownership rows", `SELECT count(*) FROM sale_event_ownership WHERE sale_id=$1`, []any{ownedSale}, 1},
			{"projection rows", `SELECT count(*) FROM sales_projection WHERE sale_id=$1`, []any{ownedSale}, 1},
			{"line rows", `SELECT count(*) FROM sale_lines_projection WHERE sale_id=$1`, []any{ownedSale}, 1},
			{"payment rows", `SELECT count(*) FROM sale_payments_projection WHERE sale_id=$1`, []any{ownedSale}, 1},
			{"classification rows", `SELECT count(*) FROM sale_line_classifications_projection WHERE sale_id=$1`, []any{ownedSale}, 1},
		}
		for _, c := range checks {
			var n int
			if err := poolA.QueryRow(ctx, c.query, c.args...).Scan(&n); err != nil || n != c.want {
				t.Fatalf("round %d %s: got %d want %d (%v)", r, c.name, n, c.want, err)
			}
		}
		var status string
		var next *time.Time
		var code string
		if err := poolA.QueryRow(ctx,
			`SELECT status, next_attempt_at, COALESCE(last_error_code,'') FROM sync_event_processing WHERE event_id=$1`, e1).Scan(&status, &next, &code); err != nil {
			t.Fatalf("round %d: processing: %v", r, err)
		}
		if status != "processed" || next != nil || code == "SALE_ID_CONFLICT" {
			t.Fatalf("round %d: processing=%s next=%v code=%q", r, status, next, code)
		}
	}
}

// TestImpossibleWinningEventReuse is the defensive invariant test (§13):
// winning_event_id E already owns S1; a claim of E for S2 must fail with a
// deterministic integrity error — never idempotent success, never
// SALE_ID_CONFLICT against S2, never transient retry, never silent ignore.
func TestImpossibleWinningEventReuse(t *testing.T) {
	url := testURL(t)
	pool := openPool(t, url)
	_, authSvc := openTestRepoOnURL(t, url)
	ctx := context.Background()
	p, err := authSvc.Create(ctx, "shop-impossible")
	if err != nil {
		t.Fatal(err)
	}
	dev, _ := parseUUID(p.Device.ID)
	e, _ := parseUUID("41111111-1111-7111-8111-111111111111")
	s1, _ := parseUUID("4aaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa")
	s2, _ := parseUUID("4bbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb")
	if _, err := pool.Exec(ctx, `INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, received_at, payload, payload_hash)
		VALUES ($1,$2,'sale.finalized.v1',now(),now(),'{}','\x00')`, "41111111-1111-7111-8111-111111111111", p.Device.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO sale_event_ownership (sale_id, winning_event_id, winning_device_id)
		VALUES ($1,$2,$3)`, "4aaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa", "41111111-1111-7111-8111-111111111111", p.Device.ID); err != nil {
		t.Fatal(err)
	}
	store := NewDevices(pool, 10*time.Second)
	_ = dev
	_ = s1
	winner, err := store.arbitrate(ctx, s2, e, dev, time.Now().UTC())
	if err == nil {
		t.Fatalf("impossible reuse must fail, got winner %s", winner)
	}
	var ierr *integrityError
	if !errors.As(err, &ierr) {
		t.Fatalf("want deterministic integrity error, got %v", err)
	}
	// No ownership row for S2, no projection rows anywhere.
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sale_event_ownership WHERE sale_id=$1`, "4bbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb").Scan(&n); err != nil || n != 0 {
		t.Fatalf("no S2 ownership: %d (%v)", n, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sales_projection`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("no projections: %d (%v)", n, err)
	}
}
