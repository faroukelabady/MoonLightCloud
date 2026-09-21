package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/sale"
)

// TestConcurrentSameEventReplay closes the second-audit gap: the same event
// (ID, sale, payload) projected concurrently on two connections must yield
// one owner, one logical projection, no duplicate children, no false
// conflict.
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
	e1 := "41111111-1111-7111-8111-111111111111"
	saleID := "4aaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa"
	env.ingest(t, e1, fixture(t, "sale_egp.json"))
	_ = saleID

	storeA := NewDevices(poolA, 10*time.Second)
	storeB := NewDevices(poolB, 10*time.Second)
	recA, ok, err := storeA.LoadSaleEvent(context.Background(), e1)
	if err != nil || !ok {
		t.Fatal("load")
	}
	recB, ok, err := storeB.LoadSaleEvent(context.Background(), e1)
	if err != nil || !ok {
		t.Fatal("load")
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
		t.Fatal("deadlock in concurrent replay")
	}
	for i, o := range outs {
		if o.err != nil {
			t.Fatalf("replay %d: %v", i, o.err)
		}
		if o.res.Outcome != sale.OutcomeProcessed {
			t.Fatalf("replay %d: %+v, want processed (no false conflict)", i, o.res)
		}
	}
	// One logical projection: exactly one header/line/payment set.
	var sales, lines, pays int
	if err := poolA.QueryRow(context.Background(), `SELECT count(*) FROM sales_projection`).Scan(&sales); err != nil || sales != 1 {
		t.Fatalf("sales: %d (%v)", sales, err)
	}
	if err := poolA.QueryRow(context.Background(), `SELECT count(*) FROM sale_lines_projection`).Scan(&lines); err != nil || lines != 1 {
		t.Fatalf("lines: %d (%v)", lines, err)
	}
	if err := poolA.QueryRow(context.Background(), `SELECT count(*) FROM sale_payments_projection`).Scan(&pays); err != nil || pays != 1 {
		t.Fatalf("payments: %d (%v)", pays, err)
	}
	var owner string
	if err := poolA.QueryRow(context.Background(), `SELECT winning_event_id::text FROM sale_event_ownership`).Scan(&owner); err != nil || owner != e1 {
		t.Fatalf("owner: %q (%v)", owner, err)
	}
	var conflicts int
	if err := poolA.QueryRow(context.Background(),
		`SELECT count(*) FROM sync_event_processing WHERE last_error_code='SALE_ID_CONFLICT'`).Scan(&conflicts); err != nil || conflicts != 0 {
		t.Fatalf("false conflicts: %d (%v)", conflicts, err)
	}
}
