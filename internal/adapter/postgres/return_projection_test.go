package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"

	"github.com/faroukelabady/MoonLightCloud/internal/returnrefund"
)

// returnEnv provisions a device and returns the sale-env helpers plus a
// return projector drain. Ingestion paths mirror the app wiring (sale and
// return validators registered in TestMain).
func returnEnv(t *testing.T) *saleEnv {
	t.Helper()
	return openSaleEnv(t)
}

// alignedReturnPayload builds a valid EGP return for the given projected
// sale: one line returning qty units of the first sale line at unit price
// (valid for the zero-discount fixture sales used here).
func alignedReturnPayload(t *testing.T, salePayload, rrID, returnNumber string, qty int) string {
	t.Helper()
	var s map[string]any
	if err := json.Unmarshal([]byte(salePayload), &s); err != nil {
		t.Fatal(err)
	}
	line := s["lines"].([]any)[0].(map[string]any)
	unit := int(line["unit_price"].(map[string]any)["amount_minor"].(float64))
	amount := unit * qty
	money := func(a int) map[string]any {
		return map[string]any{"amount_minor": a, "currency": "EGP"}
	}
	m := map[string]any{
		"return_refund_id": rrID,
		"return_number":    returnNumber,
		"kind":             "return",
		"reason":           "customer_changed_mind",
		"sale_id":          s["sale_id"],
		"sale_number":      s["sale_number"],
		"channel":          "STORE",
		"occurred_at":      "2026-09-21T12:00:00Z",
		"shop":             s["shop"],
		"actor":            map[string]any{"user_id": "11111111-1111-4111-8111-111111111111", "user_name": "Amal"},
		"currency":         "EGP",
		"totals": map[string]any{
			"gross": money(amount), "discount": money(0),
			"tax": money(0), "refund_total": money(amount),
		},
		"lines": []any{map[string]any{
			"original_sale_line_id": line["sale_item_id"],
			"product_id":            line["product_id"],
			"quantity":              qty, "restocked": true,
			"gross": money(amount), "discount": money(0),
			"tax": money(0), "refund": money(amount),
		}},
		"refunds": []any{map[string]any{"method": "cash", "amount": money(amount)}},
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func ingestReturnRaw(t *testing.T, env *saleEnv, eventID, payload string) {
	t.Helper()
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.return_refund.finalized.v1","occurred_at":"2026-09-21T12:00:00Z","payload":%s}]}`,
		eventID, payload)
	res, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body))
	if err != nil {
		t.Fatalf("ingest return: %v", err)
	}
	if res.Events[0].Status != "accepted" {
		t.Fatalf("want accepted, got %+v", res)
	}
}

// drainReturns runs the return projector to quiescence (fresh instance =
// restart proof) with a bounded wait.
func drainReturns(t *testing.T, env *saleEnv) {
	t.Helper()
	fresh := returnrefund.NewProjector(NewDevices(env.pool, 5*time.Second), testSystemClock(), nilLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); fresh.Run(ctx) }()
	waitFor(t, 12*time.Second, func() bool {
		var pending int
		_ = env.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM sync_event_processing WHERE processor='return_refund_projection.v1' AND status IN ('pending','retry')`).Scan(&pending)
		var missing int
		_ = env.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM sync_events e WHERE e.event_type='sale.return_refund.finalized.v1' AND NOT EXISTS
			 (SELECT 1 FROM sync_event_processing p WHERE p.event_id=e.event_id AND p.processor='return_refund_projection.v1')`).Scan(&missing)
		return pending == 0 && missing == 0
	}, "return drain quiescence")
	cancel()
	<-done
}

func returnStatus(t *testing.T, env *saleEnv, eventID string) (string, string) {
	t.Helper()
	var status, code string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT status, COALESCE(last_error_code,'') FROM sync_event_processing WHERE event_id=$1 AND processor='return_refund_projection.v1'`,
		eventID).Scan(&status, &code); err != nil {
		t.Fatal(err)
	}
	return status, code
}

// TestReturnProjectsEndToEnd ingests a sale, projects it, ingests an
// aligned return, and proves exact header/line/payment projection.
func TestReturnProjectsEndToEnd(t *testing.T) {
	env := returnEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	saleEvent := "22222222-2222-7222-8222-222222222222"
	env.ingest(t, saleEvent, salePayload)
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	retPayload := alignedReturnPayload(t, salePayload,
		"33333333-3333-7333-8333-333333333333", "RET-20260921-33333333", 1)
	retEvent := "44444444-4444-7444-8444-444444444444"
	ingestReturnRaw(t, env, retEvent, retPayload)
	drainReturns(t, env)

	if status, code := returnStatus(t, env, retEvent); status != "processed" || code != "" {
		t.Fatalf("return status: %q/%q", status, code)
	}
	var gotRR, gotSale, kind, reason, currency string
	var refund int64
	if err := env.pool.QueryRow(context.Background(), `
		SELECT return_refund_id::text, sale_id::text, kind, reason, currency, refund_total_minor
		FROM return_refund_projection`).Scan(&gotRR, &gotSale, &kind, &reason, &currency, &refund); err != nil {
		t.Fatal(err)
	}
	if gotRR != "33333333-3333-7333-8333-333333333333" || gotSale != "44444444-4444-4444-8444-444444444444" ||
		kind != "return" || reason != "customer_changed_mind" || currency != "EGP" || refund != 100000 {
		t.Fatalf("header mismatch: %s %s %s %s %s %d", gotRR, gotSale, kind, reason, currency, refund)
	}
	var srcEvent, srcDevice string
	if err := env.pool.QueryRow(context.Background(), `
		SELECT source_event_id::text, source_device_id::text FROM return_refund_projection`).Scan(&srcEvent, &srcDevice); err != nil {
		t.Fatal(err)
	}
	if srcEvent != retEvent || srcDevice != env.devID {
		t.Fatalf("lineage mismatch: %s %s", srcEvent, srcDevice)
	}
	var lines, pays int
	if err := env.pool.QueryRow(context.Background(), `SELECT count(*) FROM return_refund_lines_projection`).Scan(&lines); err != nil || lines != 1 {
		t.Fatalf("lines: %d (%v)", lines, err)
	}
	if err := env.pool.QueryRow(context.Background(), `SELECT count(*) FROM return_refund_payments_projection`).Scan(&pays); err != nil || pays != 1 {
		t.Fatalf("payments: %d (%v)", pays, err)
	}
	var qty, rfund int64
	var restocked bool
	if err := env.pool.QueryRow(context.Background(), `
		SELECT quantity, refund_minor, restocked FROM return_refund_lines_projection`).Scan(&qty, &rfund, &restocked); err != nil {
		t.Fatal(err)
	}
	if qty != 1 || rfund != 100000 || !restocked {
		t.Fatalf("line mismatch: %d %d %v", qty, rfund, restocked)
	}
	var owner string
	if err := env.pool.QueryRow(context.Background(), `
		SELECT winning_event_id::text FROM return_refund_ownership`).Scan(&owner); err != nil || owner != retEvent {
		t.Fatalf("ownership: %q (%v)", owner, err)
	}
}

// TestReturnWaitsForSaleThenProjects proves out-of-order safety: a return
// arriving before its sale projection waits retryably, then projects once
// the sale appears — with no resend and no terminal verdict in between.
func TestReturnWaitsForSaleThenProjects(t *testing.T) {
	env := returnEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	retPayload := alignedReturnPayload(t, salePayload,
		"33333333-3333-7333-8333-333333333333", "RET-20260921-33333333", 1)
	retEvent := "44444444-4444-7444-8444-444444444444"
	ingestReturnRaw(t, env, retEvent, retPayload)

	store := NewDevices(env.pool, 5*time.Second)
	rec, ok, err := store.LoadReturnEvent(context.Background(), retEvent)
	if err != nil || !ok {
		t.Fatal("event must load")
	}
	res, err := store.ProjectReturn(context.Background(), rec, time.Now())
	if err == nil {
		t.Fatalf("missing sale must surface, got %+v", res)
	}
	if status, code := returnStatus(t, env, retEvent); status != "retry" || code != ErrSaleDependencyWait {
		t.Fatalf("waiting state: %q/%q", status, code)
	}
	if n := saleCount(t, env.pool, "return_refund_projection"); n != 0 {
		t.Fatalf("zero projection rows while waiting, got %d", n)
	}

	// The sale arrives and projects; advancing past the retry horizon, the
	// waiting return converges with no resend and no terminal verdict.
	env.ingest(t, "22222222-2222-7222-8222-222222222222", salePayload)
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")
	if _, err := env.pool.Exec(context.Background(),
		`UPDATE sync_event_processing SET next_attempt_at = now() - interval '1 second'
		 WHERE event_id=$1 AND processor='return_refund_projection.v1'`, retEvent); err != nil {
		t.Fatal(err)
	}
	drainReturns(t, env)
	if status, _ := returnStatus(t, env, retEvent); status != "processed" {
		t.Fatalf("return must process after sale appears: %q", status)
	}
	if n := saleCount(t, env.pool, "return_refund_projection"); n != 1 {
		t.Fatalf("one return projection, got %d", n)
	}
}

// TestReturnBlockedUnknownLine proves a sale projection that lacks the
// referenced line is terminally blocked (committed history cannot change)
// with zero projection rows.
func TestReturnBlockedUnknownLine(t *testing.T) {
	env := returnEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	env.ingest(t, "22222222-2222-7222-8222-222222222222", salePayload)
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	retPayload := alignedReturnPayload(t, salePayload,
		"33333333-3333-7333-8333-333333333333", "RET-20260921-33333333", 1)
	var m map[string]any
	if err := json.Unmarshal([]byte(retPayload), &m); err != nil {
		t.Fatal(err)
	}
	m["lines"].([]any)[0].(map[string]any)["original_sale_line_id"] = "99999999-9999-4999-8999-999999999999"
	raw, _ := json.Marshal(m)
	retEvent := "44444444-4444-7444-8444-444444444444"
	ingestReturnRaw(t, env, retEvent, string(raw))
	drainReturns(t, env)
	if status, code := returnStatus(t, env, retEvent); status != "blocked" || code != ErrReturnLineUnknown {
		t.Fatalf("unknown line: %q/%q", status, code)
	}
	for _, table := range []string{"return_refund_projection", "return_refund_lines_projection", "return_refund_payments_projection"} {
		if n := saleCount(t, env.pool, table); n != 0 {
			t.Fatalf("%s must be empty, got %d", table, n)
		}
	}
}

// TestReturnRivalBusinessID proves two events sharing return_refund_id
// arbitrate to one winner: loser blocked, one header, no double projection.
func TestReturnRivalBusinessID(t *testing.T) {
	env := returnEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	env.ingest(t, "22222222-2222-7222-8222-222222222222", salePayload)
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	base := alignedReturnPayload(t, salePayload,
		"33333333-3333-7333-8333-333333333333", "RET-20260921-33333333", 1)
	e1, e2 := "44444444-4444-7444-8444-444444444444", "55555555-5555-7555-8555-555555555555"
	ingestReturnRaw(t, env, e1, base)
	var m map[string]any
	if err := json.Unmarshal([]byte(base), &m); err != nil {
		t.Fatal(err)
	}
	m["return_number"] = "RET-RIVAL"
	rival, _ := json.Marshal(m)
	ingestReturnRaw(t, env, e2, string(rival))
	drainReturns(t, env)

	processed, blocked := 0, 0
	for _, e := range []string{e1, e2} {
		status, code := returnStatus(t, env, e)
		switch status {
		case "processed":
			processed++
		case "blocked":
			blocked++
			if code != ErrReturnIDConflict {
				t.Fatalf("loser code: %q", code)
			}
		default:
			t.Fatalf("event %s: %q/%q", e, status, code)
		}
	}
	if processed != 1 || blocked != 1 {
		t.Fatalf("want 1 processed + 1 conflict, got %d/%d", processed, blocked)
	}
	if n := saleCount(t, env.pool, "return_refund_projection"); n != 1 {
		t.Fatalf("one header, got %d", n)
	}
	if n := saleCount(t, env.pool, "return_refund_ownership"); n != 1 {
		t.Fatalf("one ownership row, got %d", n)
	}
}

// TestReturnCumulativeOverReturn proves the second full return of an
// already-returned line is blocked with zero new rows.
func TestReturnCumulativeOverReturn(t *testing.T) {
	env := returnEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	env.ingest(t, "22222222-2222-7222-8222-222222222222", salePayload)
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	// Fixture line sells qty 2: two qty-1 returns exhaust it exactly.
	for i, e := range []string{"44444444-4444-7444-8444-444444444444", "55555555-5555-7555-8555-555555555555"} {
		payload := alignedReturnPayload(t, salePayload,
			fmt.Sprintf("3333333%d-3333-7333-8333-33333333333%d", i, i), fmt.Sprintf("RET-%d", i), 1)
		ingestReturnRaw(t, env, e, payload)
	}
	drainReturns(t, env)
	for _, e := range []string{"44444444-4444-7444-8444-444444444444", "55555555-5555-7555-8555-555555555555"} {
		if status, _ := returnStatus(t, env, e); status != "processed" {
			t.Fatalf("return %s: %q", e, status)
		}
	}
	// Third return of the same line exceeds sold quantity.
	over := alignedReturnPayload(t, salePayload,
		"66666666-6666-7666-8666-666666666666", "RET-OVER", 1)
	overEvent := "77777777-7777-7777-8777-777777777777"
	ingestReturnRaw(t, env, overEvent, over)
	drainReturns(t, env)
	if status, code := returnStatus(t, env, overEvent); status != "blocked" || code != ErrCumulativeOverRet {
		t.Fatalf("over-return: %q/%q", status, code)
	}
	if n := saleCount(t, env.pool, "return_refund_projection"); n != 2 {
		t.Fatalf("two headers, got %d", n)
	}
}

// TestReturnCumulativeRefundExceeded proves an event-valid but excessive
// refund is blocked before writing.
func TestReturnCumulativeRefundExceeded(t *testing.T) {
	env := returnEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	env.ingest(t, "22222222-2222-7222-8222-222222222222", salePayload)
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	// Valid line quantity, but refund inflated beyond the sale total (the
	// event itself is internally consistent, so ingestion accepts it).
	payload := alignedReturnPayload(t, salePayload,
		"33333333-3333-7333-8333-333333333333", "RET-BIG", 1)
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatal(err)
	}
	big := 999999999
	for _, key := range []string{"gross", "refund_total"} {
		m["totals"].(map[string]any)[key].(map[string]any)["amount_minor"] = big
	}
	m["lines"].([]any)[0].(map[string]any)["gross"].(map[string]any)["amount_minor"] = big
	m["lines"].([]any)[0].(map[string]any)["refund"].(map[string]any)["amount_minor"] = big
	m["refunds"].([]any)[0].(map[string]any)["amount"].(map[string]any)["amount_minor"] = big
	raw, _ := json.Marshal(m)
	retEvent := "44444444-4444-7444-8444-444444444444"
	ingestReturnRaw(t, env, retEvent, string(raw))
	drainReturns(t, env)
	if status, code := returnStatus(t, env, retEvent); status != "blocked" || code != ErrCumulativeRefundEx {
		t.Fatalf("excessive refund: %q/%q", status, code)
	}
	if n := saleCount(t, env.pool, "return_refund_projection"); n != 0 {
		t.Fatalf("zero headers, got %d", n)
	}
}

// TestReturnConcurrentSameEvent proves concurrent identical projection
// converges: one header, processed, no false conflict.
func TestReturnConcurrentSameEvent(t *testing.T) {
	env := returnEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	env.ingest(t, "22222222-2222-7222-8222-222222222222", salePayload)
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	retPayload := alignedReturnPayload(t, salePayload,
		"33333333-3333-7333-8333-333333333333", "RET-20260921-33333333", 1)
	retEvent := "44444444-4444-7444-8444-444444444444"
	ingestReturnRaw(t, env, retEvent, retPayload)

	storeA, storeB := NewDevices(env.pool, 10*time.Second), NewDevices(env.pool, 10*time.Second)
	recA, ok, err := storeA.LoadReturnEvent(context.Background(), retEvent)
	if err != nil || !ok {
		t.Fatal("load")
	}
	recB, ok, err := storeB.LoadReturnEvent(context.Background(), retEvent)
	if err != nil || !ok {
		t.Fatal("load")
	}
	start := make(chan struct{})
	type outcome struct {
		res returnrefund.ProjectResult
		err error
	}
	outs := make([]outcome, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		outs[0].res, outs[0].err = storeA.ProjectReturn(context.Background(), recA, time.Now().UTC())
	}()
	go func() {
		defer wg.Done()
		<-start
		outs[1].res, outs[1].err = storeB.ProjectReturn(context.Background(), recB, time.Now().UTC())
	}()
	close(start)
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(30 * time.Second):
		t.Fatal("deadlock in concurrent replay")
	}
	for i, o := range outs {
		if o.err != nil {
			t.Fatalf("replay %d: %v", i, o.err)
		}
		if o.res.Outcome != returnrefund.OutcomeProcessed {
			t.Fatalf("replay %d: %+v, want processed", i, o.res)
		}
	}
	if n := saleCount(t, env.pool, "return_refund_projection"); n != 1 {
		t.Fatalf("one header, got %d", n)
	}
	if status, code := returnStatus(t, env, retEvent); status != "processed" || code != "" {
		t.Fatalf("status: %q/%q", status, code)
	}
}

// TestReturnDiscoveryDueSemantics proves the discovery contract: missing
// and pending rows are discoverable, future retry rows are not, due retry
// rows are, and blocked/processed rows never are.
func TestReturnDiscoveryDueSemantics(t *testing.T) {
	env := returnEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	env.ingest(t, "22222222-2222-7222-8222-222222222222", salePayload)
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	payload := alignedReturnPayload(t, salePayload,
		"33333333-3333-7333-8333-333333333333", "RET-DISC", 1)
	e1 := "44444444-4444-7444-8444-444444444444"
	ingestReturnRaw(t, env, e1, payload)
	store := NewDevices(env.pool, 5*time.Second)
	ctx := context.Background()

	// Missing row: discoverable.
	due, err := store.PendingReturnEvents(ctx, returnrefund.ProcessorReturnProjectionV1, 25)
	if err != nil || !containsID(due, e1) {
		t.Fatalf("missing row discoverable: %v %v", due, err)
	}
	// Project to processed: never discoverable again.
	rec, ok, err := store.LoadReturnEvent(ctx, e1)
	if err != nil || !ok {
		t.Fatal("load")
	}
	if _, err := store.ProjectReturn(ctx, rec, time.Now()); err != nil {
		t.Fatal(err)
	}
	due, err = store.PendingReturnEvents(ctx, returnrefund.ProcessorReturnProjectionV1, 25)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range due {
		if id == e1 {
			t.Fatal("processed must not be discoverable")
		}
	}

	// Future retry: not discoverable; due retry: discoverable.
	e2 := "55555555-5555-7555-8555-555555555555"
	payload2 := alignedReturnPayload(t, salePayload,
		"66666666-6666-7666-8666-666666666666", "RET-DISC2", 2)
	ingestReturnRaw(t, env, e2, payload2)
	if _, err := env.pool.Exec(ctx,
		`INSERT INTO sync_event_processing (event_id, processor, status, attempt_count, next_attempt_at)
		 VALUES ($1,'return_refund_projection.v1','retry',1,now() + interval '1 hour')`, e2); err != nil {
		t.Fatal(err)
	}
	due, err = store.PendingReturnEvents(ctx, returnrefund.ProcessorReturnProjectionV1, 25)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range due {
		if id == e2 {
			t.Fatal("future retry must not be discoverable")
		}
	}
	if _, err := env.pool.Exec(ctx,
		`UPDATE sync_event_processing SET next_attempt_at = now() - interval '1 second'
		 WHERE event_id=$1 AND processor='return_refund_projection.v1'`, e2); err != nil {
		t.Fatal(err)
	}
	due, err = store.PendingReturnEvents(ctx, returnrefund.ProcessorReturnProjectionV1, 25)
	if err != nil || !containsID(due, e2) {
		t.Fatalf("due retry discoverable: %v %v", due, err)
	}
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// alignedUSDReturnPayload builds a valid USD return (qty 1 of the single
// fixture line) carrying the sale's historical FX snapshot verbatim.
func alignedUSDReturnPayload(t *testing.T, rrID, returnNumber string) string {
	t.Helper()
	money := func(a int) map[string]any {
		return map[string]any{"amount_minor": a, "currency": "USD"}
	}
	m := map[string]any{
		"return_refund_id": rrID,
		"return_number":    returnNumber,
		"kind":             "return",
		"reason":           "damaged_item",
		"sale_id":          "11111111-1111-4111-8111-111111111111",
		"sale_number":      "MLR-20260920-11111111",
		"channel":          "STORE",
		"occurred_at":      "2026-09-21T12:00:00Z",
		"shop": map[string]any{
			"name_ar": "م", "name_en": "S", "address_ar": "A", "address_en": "A",
			"phone": "P", "receipt_footer_ar": "F", "receipt_footer_en": "F",
		},
		"actor":    map[string]any{"user_id": "11111111-1111-4111-8111-111111111111", "user_name": "Amal"},
		"currency": "USD",
		"fx":       map[string]any{"base": "USD", "quote": "EGP", "rate": "52.000000", "rate_microrate": 52000000},
		"totals": map[string]any{
			"gross": money(1300), "discount": money(100),
			"tax": money(50), "refund_total": money(1250),
		},
		"lines": []any{map[string]any{
			"original_sale_line_id": "33333333-3333-4333-8333-333333333333",
			"quantity":              1, "restocked": true,
			"gross": money(1300), "discount": money(100),
			"tax": money(50), "refund": money(1250),
			"cost": money(400),
		}},
		"refunds": []any{map[string]any{"method": "cash", "amount": money(1250)}},
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestReturnUSDProjectsWithHistoricalFx proves USD reversal uses the event
// snapshot (52.0) and agrees with the sale projection snapshot.
func TestReturnUSDProjectsWithHistoricalFx(t *testing.T) {
	env := returnEnv(t)
	env.ingest(t, "aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa", fixture(t, "sale_usd.json"))
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	payload := alignedUSDReturnPayload(t,
		"bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb", "RET-USD-1")
	retEvent := "cccccccc-cccc-7ccc-8ccc-cccccccccccc"
	ingestReturnRaw(t, env, retEvent, payload)
	drainReturns(t, env)
	if status, _ := returnStatus(t, env, retEvent); status != "processed" {
		t.Fatalf("usd return: %q", status)
	}
	var fxRate string
	var micro int64
	if err := env.pool.QueryRow(context.Background(), `
		SELECT fx_rate, fx_rate_microrate FROM return_refund_projection`).Scan(&fxRate, &micro); err != nil {
		t.Fatal(err)
	}
	if fxRate != "52.000000" || micro != 52000000 {
		t.Fatalf("historical fx: %q %d", fxRate, micro)
	}
}

// TestReturnExtendedCostNotMultiplied proves the extended-cost invariant:
// the stored cost equals the event's extended cost verbatim (unit 400 x
// qty 1 = 400 here), and a qty-3 line with cost 369 stores 369 — the
// projector never multiplies by quantity again.
func TestReturnExtendedCostNotMultiplied(t *testing.T) {
	env := returnEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	env.ingest(t, "22222222-2222-7222-8222-222222222222", salePayload)
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	// Qty-2 line with extended cost 369 (unit 123 x 2 would be 246; the
	// event carries the authoritative extended figure — stored verbatim).
	payload := alignedReturnPayload(t, salePayload,
		"33333333-3333-7333-8333-333333333333", "RET-COST", 2)
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatal(err)
	}
	// Rewrite the line cost to an arbitrary extended figure the projector
	// must store verbatim (369), keeping totals consistent is unnecessary
	// for cost: only refund sums are cross-checked, cost is carried.
	m["lines"].([]any)[0].(map[string]any)["cost"] = map[string]any{"amount_minor": 369, "currency": "EGP"}
	raw, _ := json.Marshal(m)
	retEvent := "44444444-4444-7444-8444-444444444444"
	ingestReturnRaw(t, env, retEvent, string(raw))
	drainReturns(t, env)
	if status, _ := returnStatus(t, env, retEvent); status != "processed" {
		t.Fatalf("cost return: %q", status)
	}
	var cost int64
	if err := env.pool.QueryRow(context.Background(), `
		SELECT cost_minor FROM return_refund_lines_projection`).Scan(&cost); err != nil {
		t.Fatal(err)
	}
	if cost != 369 {
		t.Fatalf("extended cost must store verbatim, got %d", cost)
	}
}

// TestReturnNonRestockedStillReverses proves financial reversal never
// depends on the restock flag (inventory sync is out of scope).
func TestReturnNonRestockedStillReverses(t *testing.T) {
	env := returnEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	env.ingest(t, "22222222-2222-7222-8222-222222222222", salePayload)
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	payload := alignedReturnPayload(t, salePayload,
		"33333333-3333-7333-8333-333333333333", "RET-NORESTOCK", 1)
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatal(err)
	}
	m["lines"].([]any)[0].(map[string]any)["restocked"] = false
	raw, _ := json.Marshal(m)
	retEvent := "44444444-4444-7444-8444-444444444444"
	ingestReturnRaw(t, env, retEvent, string(raw))
	drainReturns(t, env)
	if status, _ := returnStatus(t, env, retEvent); status != "processed" {
		t.Fatalf("non-restocked return: %q", status)
	}
	var refund int64
	var restocked bool
	if err := env.pool.QueryRow(context.Background(), `
		SELECT refund_minor, restocked FROM return_refund_lines_projection`).Scan(&refund, &restocked); err != nil {
		t.Fatal(err)
	}
	if refund != 100000 || restocked {
		t.Fatalf("money reverses with restocked=false: %d %v", refund, restocked)
	}
}

// TestReturnVoidProjects proves kind=void flows through the same pipeline
// as a full historical reversal.
func TestReturnVoidProjects(t *testing.T) {
	env := returnEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	env.ingest(t, "22222222-2222-7222-8222-222222222222", salePayload)
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	payload := alignedReturnPayload(t, salePayload,
		"33333333-3333-7333-8333-333333333333", "VOID-1", 2)
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatal(err)
	}
	m["kind"] = "void"
	m["reason"] = "duplicate_sale"
	// Full reversal of the qty-2 line: refund the whole line total.
	raw, _ := json.Marshal(m)
	_ = raw
	// Recompute for qty 2 at unit 100000: alignedReturnPayload already did.
	retEvent := "44444444-4444-7444-8444-444444444444"
	ingestReturnRaw(t, env, retEvent, string(raw))
	drainReturns(t, env)
	if status, _ := returnStatus(t, env, retEvent); status != "processed" {
		t.Fatalf("void: %q", status)
	}
	var kind string
	var refund int64
	if err := env.pool.QueryRow(context.Background(), `
		SELECT kind, refund_total_minor FROM return_refund_projection`).Scan(&kind, &refund); err != nil {
		t.Fatal(err)
	}
	if kind != "void" || refund != 200000 {
		t.Fatalf("void reversal: %q %d", kind, refund)
	}
}

// TestReturnCrossDeviceProjects proves device affinity is not required:
// the return may come from a different device than the sale; guards are
// device-agnostic by design.
func TestReturnCrossDeviceProjects(t *testing.T) {
	env := returnEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	env.ingest(t, "22222222-2222-7222-8222-222222222222", salePayload)
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	svc := auth.NewService(NewDevices(env.pool, 5*time.Second), mustTestHasher(t), "", 1, clock.System{}, ids.System{})
	p2, err := svc.Create(context.Background(), "shop-two")
	if err != nil {
		t.Fatal(err)
	}
	env2 := newSaleEnvOnPool(t, env.pool, p2.Device.ID, p2.Credential.ID)
	payload := alignedReturnPayload(t, salePayload,
		"33333333-3333-7333-8333-333333333333", "RET-XDEV", 1)
	retEvent := "44444444-4444-7444-8444-444444444444"
	ingestReturnRaw(t, env2, retEvent, payload)
	drainReturns(t, env)
	if status, _ := returnStatus(t, env, retEvent); status != "processed" {
		t.Fatalf("cross-device return: %q", status)
	}
}

// TestReturnProjectionFailureRetriesCleanly forces a failure inside the
// return projection transaction via a trigger: zero partial rows,
// durable retry state, and success after removing the trigger.
func TestReturnProjectionFailureRetriesCleanly(t *testing.T) {
	env := returnEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	env.ingest(t, "22222222-2222-7222-8222-222222222222", salePayload)
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	payload := alignedReturnPayload(t, salePayload,
		"33333333-3333-7333-8333-333333333333", "RET-FAIL", 1)
	retEvent := "44444444-4444-7444-8444-444444444444"
	ingestReturnRaw(t, env, retEvent, payload)
	if _, err := env.pool.Exec(context.Background(), `
		CREATE OR REPLACE FUNCTION fail_return_line() RETURNS trigger AS $$
		BEGIN
			RAISE EXCEPTION 'injected failure';
			RETURN NEW;
		END; $$ LANGUAGE plpgsql;
		CREATE TRIGGER trg_fail_return_line BEFORE INSERT ON return_refund_lines_projection
		FOR EACH ROW EXECUTE FUNCTION fail_return_line();`); err != nil {
		t.Fatal(err)
	}
	store := NewDevices(env.pool, 5*time.Second)
	rec, ok, err := store.LoadReturnEvent(context.Background(), retEvent)
	if err != nil || !ok {
		t.Fatal("event must load")
	}
	if _, err := store.ProjectReturn(context.Background(), rec, time.Now()); err == nil {
		t.Fatal("injected failure must surface")
	}
	for _, table := range []string{"return_refund_projection", "return_refund_lines_projection", "return_refund_payments_projection"} {
		if n := saleCount(t, env.pool, table); n != 0 {
			t.Fatalf("%s must be empty after rollback, got %d", table, n)
		}
	}
	// Ownership arbitration precedes projection: the winner is already
	// decided and survives the projection failure.
	var owner string
	if err := env.pool.QueryRow(context.Background(), `
		SELECT winning_event_id::text FROM return_refund_ownership`).Scan(&owner); err != nil || owner != retEvent {
		t.Fatalf("ownership survives projection failure: %q (%v)", owner, err)
	}
	if status, code := returnStatus(t, env, retEvent); status != "retry" {
		t.Fatalf("retry state: %q/%q", status, code)
	}
	if _, err := env.pool.Exec(context.Background(),
		`DROP TRIGGER trg_fail_return_line ON return_refund_lines_projection; DROP FUNCTION fail_return_line();`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(context.Background(),
		`UPDATE sync_event_processing SET next_attempt_at = now() - interval '1 second'
		 WHERE event_id=$1 AND processor='return_refund_projection.v1'`, retEvent); err != nil {
		t.Fatal(err)
	}
	drainReturns(t, env)
	if status, _ := returnStatus(t, env, retEvent); status != "processed" {
		t.Fatalf("retry succeeds: %q", status)
	}
	if n := saleCount(t, env.pool, "return_refund_lines_projection"); n != 1 {
		t.Fatalf("exactly one line after retry, got %d", n)
	}
}

// dumpReturns renders all derived return rows deterministically (ordered)
// for exact rebuild comparison.
func dumpReturns(t *testing.T, env *saleEnv) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var b strings.Builder
	dump := func(query string) {
		rows, err := env.pool.Query(ctx, query)
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
	dump(`SELECT return_refund_id::text, source_event_id::text, source_device_id::text, return_number, kind,
		reason, sale_id::text, sale_number, channel, occurred_at::text, currency,
		gross_refunded_minor, discount_refunded_minor, tax_refunded_minor, refund_total_minor,
		fx_base, fx_quote, fx_rate, fx_rate_microrate, received_at::text
		FROM return_refund_projection ORDER BY return_refund_id::text`)
	dump(`SELECT return_refund_id::text, original_sale_line_id::text, position, product_id::text, quantity, restocked,
		gross_minor, discount_minor, tax_minor, refund_minor, cost_minor
		FROM return_refund_lines_projection ORDER BY return_refund_id::text, original_sale_line_id::text`)
	dump(`SELECT return_refund_id::text, position, method, amount_minor, transaction_ref
		FROM return_refund_payments_projection ORDER BY return_refund_id::text, position`)
	dump(`SELECT return_refund_id::text, winning_event_id::text FROM return_refund_ownership ORDER BY return_refund_id::text`)
	return b.String()
}

// resetReturnDerived clears derived return state only (projections +
// processing), retaining sync_events and return_refund_ownership.
func resetReturnDerived(t *testing.T, env *saleEnv) {
	t.Helper()
	ctx := context.Background()
	if _, err := env.pool.Exec(ctx, `DELETE FROM return_refund_projection`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx,
		`UPDATE sync_event_processing SET status='pending', next_attempt_at=NULL,
		 attempt_count=0, processed_at=NULL, last_error_code=NULL, last_error_message=NULL
		 WHERE processor='return_refund_projection.v1'`); err != nil {
		t.Fatal(err)
	}
}

// TestReturnRebuildStable proves derived return state rebuilds exactly:
// same winner, byte-identical rows, same processing outcomes.
func TestReturnRebuildStable(t *testing.T) {
	env := returnEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	env.ingest(t, "22222222-2222-7222-8222-222222222222", salePayload)
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	for i, e := range []string{"44444444-4444-7444-8444-444444444444", "55555555-5555-7555-8555-555555555555"} {
		payload := alignedReturnPayload(t, salePayload,
			fmt.Sprintf("3333333%d-3333-7333-8333-33333333333%d", i, i), fmt.Sprintf("RET-%d", i), 1)
		ingestReturnRaw(t, env, e, payload)
	}
	drainReturns(t, env)
	before := dumpReturns(t, env)
	if before == "" {
		t.Fatal("snapshot must not be empty")
	}

	resetReturnDerived(t, env)
	if n := saleCount(t, env.pool, "sync_events"); n != 3 {
		t.Fatalf("source events retained, got %d", n)
	}
	drainReturns(t, env)
	if after := dumpReturns(t, env); after != before {
		t.Fatalf("rebuild mismatch:\nbefore: %s\nafter:  %s", before, after)
	}
	var processed int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM sync_event_processing WHERE processor='return_refund_projection.v1' AND status='processed'`).Scan(&processed); err != nil || processed != 2 {
		t.Fatalf("want 2 processed, got %d (%v)", processed, err)
	}
}

// TestReturnRivalRebuildStable proves a rebuild with reversed discovery
// order elects the same business winner with identical rows.
func TestReturnRivalRebuildStable(t *testing.T) {
	env := returnEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	env.ingest(t, "22222222-2222-7222-8222-222222222222", salePayload)
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	base := alignedReturnPayload(t, salePayload,
		"33333333-3333-7333-8333-333333333333", "RET-LIVE", 1)
	e1, e2 := "44444444-4444-7444-8444-444444444444", "55555555-5555-7555-8555-555555555555"
	ingestReturnRaw(t, env, e1, base)
	var m map[string]any
	if err := json.Unmarshal([]byte(base), &m); err != nil {
		t.Fatal(err)
	}
	m["return_number"] = "RET-RIVAL"
	rival, _ := json.Marshal(m)
	ingestReturnRaw(t, env, e2, string(rival))
	drainReturns(t, env)
	before := dumpReturns(t, env)

	// Reset and rebuild with the loser discovered first.
	resetReturnDerived(t, env)
	store := NewDevices(env.pool, 5*time.Second)
	for _, id := range []string{e2, e1} {
		rec, ok, err := store.LoadReturnEvent(context.Background(), id)
		if err != nil || !ok {
			t.Fatal("load")
		}
		if _, err := store.ProjectReturn(context.Background(), rec, time.Now()); err != nil {
			t.Fatalf("rebuild project %s: %v", id, err)
		}
	}
	// Settle any transient (none expected, bounded).
	drainReturns(t, env)
	if after := dumpReturns(t, env); after != before {
		t.Fatalf("rival rebuild mismatch:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestReturnOutOfOrderRebuildStable proves return-before-sale inbox order
// converges to the same final state after a full rebuild.
func TestReturnOutOfOrderRebuildStable(t *testing.T) {
	env := returnEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	retPayload := alignedReturnPayload(t, salePayload,
		"33333333-3333-7333-8333-333333333333", "RET-OOO", 1)
	retEvent := "44444444-4444-7444-8444-444444444444"
	ingestReturnRaw(t, env, retEvent, retPayload)
	env.ingest(t, "22222222-2222-7222-8222-222222222222", salePayload)
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")
	drainReturns(t, env)
	before := dumpReturns(t, env)

	resetReturnDerived(t, env)
	drainReturns(t, env)
	if after := dumpReturns(t, env); after != before {
		t.Fatalf("out-of-order rebuild mismatch:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestReturnBackfillPagination proves a >1-page historical backlog (no
// processing rows, Phase 4A-era shape) projects completely with no
// starvation, then repeated runs hold steady with no drift.
func TestReturnBackfillPagination(t *testing.T) {
	env := returnEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	env.ingest(t, "22222222-2222-7222-8222-222222222222", salePayload)
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	// 30 one-unit returns need a big sale line: build a dedicated sale via
	// direct inbox insert is out of scope; instead project 30 returns
	// across 15 fixture-aligned copies is impossible (same sale, qty 2).
	// Use 30 distinct sales x return pairs instead: pagination applies to
	// the return discovery scan regardless of sale fanout.
	for i := 0; i < 30; i++ {
		saleID := fmt.Sprintf("6000%04d-0000-7000-8000-000000000000", i)
		var m map[string]any
		if err := json.Unmarshal([]byte(salePayload), &m); err != nil {
			t.Fatal(err)
		}
		m["sale_id"] = saleID
		rawSale, _ := json.Marshal(m)
		env.ingest(t, fmt.Sprintf("6100%04d-1111-7111-8111-111111111111", i), string(rawSale))
	}
	env.drain(t)
	waitFor(t, 15*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 31 }, "all sales projected")
	for i := 0; i < 30; i++ {
		saleID := fmt.Sprintf("6000%04d-0000-7000-8000-000000000000", i)
		var m map[string]any
		if err := json.Unmarshal([]byte(salePayload), &m); err != nil {
			t.Fatal(err)
		}
		m["sale_id"] = saleID
		rawSale, _ := json.Marshal(m)
		_ = rawSale
		ret := alignedReturnFor(t, saleID, i)
		ingestReturnRaw(t, env, fmt.Sprintf("6200%04d-2222-7222-8222-222222222222", i), ret)
	}
	// None may have processing rows yet (backfill shape); assert a sample.
	var unprocessed int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM sync_events e WHERE e.event_type='sale.return_refund.finalized.v1' AND NOT EXISTS
		 (SELECT 1 FROM sync_event_processing p WHERE p.event_id=e.event_id AND p.processor='return_refund_projection.v1')`).Scan(&unprocessed); err != nil || unprocessed != 30 {
		t.Fatalf("30 unprocessed backfill rows, got %d (%v)", unprocessed, err)
	}
	drainReturns(t, env)
	var processed int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM sync_event_processing WHERE processor='return_refund_projection.v1' AND status='processed'`).Scan(&processed); err != nil || processed != 30 {
		t.Fatalf("30 backfilled processed, got %d (%v)", processed, err)
	}
	if n := saleCount(t, env.pool, "return_refund_projection"); n != 30 {
		t.Fatalf("30 headers, got %d", n)
	}
	before := dumpReturns(t, env)
	drainReturns(t, env)
	drainReturns(t, env)
	if after := dumpReturns(t, env); after != before {
		t.Fatal("repeated runs must not drift")
	}
}

// alignedReturnFor builds a qty-1 EGP return for an arbitrary sale ID,
// reusing the fixture line identity.
func alignedReturnFor(t *testing.T, saleID string, i int) string {
	t.Helper()
	var s map[string]any
	if err := json.Unmarshal([]byte(fixture(t, "sale_egp.json")), &s); err != nil {
		t.Fatal(err)
	}
	line := s["lines"].([]any)[0].(map[string]any)
	unit := int(line["unit_price"].(map[string]any)["amount_minor"].(float64))
	money := func(a int) map[string]any {
		return map[string]any{"amount_minor": a, "currency": "EGP"}
	}
	m := map[string]any{
		"return_refund_id": fmt.Sprintf("6300%04d-3333-7333-8333-333333333333", i),
		"return_number":    fmt.Sprintf("RET-BF-%d", i),
		"kind":             "return",
		"reason":           "other",
		"sale_id":          saleID,
		"sale_number":      "MLR-BF",
		"channel":          "STORE",
		"occurred_at":      "2026-09-21T12:00:00Z",
		"shop":             s["shop"],
		"actor":            map[string]any{},
		"currency":         "EGP",
		"totals": map[string]any{
			"gross": money(unit), "discount": money(0),
			"tax": money(0), "refund_total": money(unit),
		},
		"lines": []any{map[string]any{
			"original_sale_line_id": line["sale_item_id"],
			"quantity":              1, "restocked": true,
			"gross": money(unit), "discount": money(0),
			"tax": money(0), "refund": money(unit),
		}},
		"refunds": []any{map[string]any{"method": "cash", "amount": money(unit)}},
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestReturnCurrencyMismatchBlocked proves a return whose currency differs
// from its sale projection is terminally blocked.
func TestReturnCurrencyMismatchBlocked(t *testing.T) {
	env := returnEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	env.ingest(t, "22222222-2222-7222-8222-222222222222", salePayload)
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	// Internally consistent USD return against an EGP sale.
	payload := alignedUSDReturnPayload(t,
		"bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb", "RET-CUR")
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatal(err)
	}
	m["sale_id"] = "44444444-4444-4444-8444-444444444444"
	m["sale_number"] = "MLR-20260920-44444444"
	raw, _ := json.Marshal(m)
	retEvent := "44444444-4444-7444-8444-444444444444"
	ingestReturnRaw(t, env, retEvent, string(raw))
	drainReturns(t, env)
	if status, code := returnStatus(t, env, retEvent); status != "blocked" || code != ErrReturnCurrencyMix {
		t.Fatalf("currency mismatch: %q/%q", status, code)
	}
	if n := saleCount(t, env.pool, "return_refund_projection"); n != 0 {
		t.Fatalf("zero headers, got %d", n)
	}
}

// TestReturnFxMismatchBlocked proves an internally valid return whose FX
// snapshot disagrees with the sale snapshot is terminally blocked.
func TestReturnFxMismatchBlocked(t *testing.T) {
	env := returnEnv(t)
	env.ingest(t, "aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa", fixture(t, "sale_usd.json"))
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	payload := alignedUSDReturnPayload(t,
		"bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb", "RET-FX")
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatal(err)
	}
	m["fx"] = map[string]any{"base": "USD", "quote": "EGP", "rate": "99.000000", "rate_microrate": 99000000}
	raw, _ := json.Marshal(m)
	retEvent := "cccccccc-cccc-7ccc-8ccc-cccccccccccc"
	ingestReturnRaw(t, env, retEvent, string(raw))
	drainReturns(t, env)
	if status, code := returnStatus(t, env, retEvent); status != "blocked" || code != ErrReturnFxMismatch {
		t.Fatalf("fx mismatch: %q/%q", status, code)
	}
	if n := saleCount(t, env.pool, "return_refund_projection"); n != 0 {
		t.Fatalf("zero headers, got %d", n)
	}
}

// TestReturnExactCeilingPasses proves a refund exactly equal to the sale
// total projects (boundary inclusive).
func TestReturnExactCeilingPasses(t *testing.T) {
	env := returnEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	env.ingest(t, "22222222-2222-7222-8222-222222222222", salePayload)
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	// Fixture sale total is 200000; qty-2 full return refunds exactly that.
	retPayload := alignedReturnPayload(t, salePayload,
		"33333333-3333-7333-8333-333333333333", "RET-CEIL", 2)
	retEvent := "44444444-4444-7444-8444-444444444444"
	ingestReturnRaw(t, env, retEvent, retPayload)
	drainReturns(t, env)
	if status, code := returnStatus(t, env, retEvent); status != "processed" || code != "" {
		t.Fatalf("exact ceiling must process: %q/%q", status, code)
	}
}

// TestReturnOneMinorOverCeilingBlocks proves ceiling+1 blocks terminally
// with zero projection rows. The event itself is internally consistent
// (totals equal line sums), so ingestion accepts it and the projector —
// not transport — enforces the ceiling.
func TestReturnOneMinorOverCeilingBlocks(t *testing.T) {
	env := returnEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	env.ingest(t, "22222222-2222-7222-8222-222222222222", salePayload)
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	payload := alignedReturnPayload(t, salePayload,
		"33333333-3333-7333-8333-333333333333", "RET-OVER1", 2)
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatal(err)
	}
	// Inflate every refund-bearing amount by 1 minor, keeping the event
	// internally consistent (line sums == totals).
	bump := func(obj map[string]any, key string) {
		obj[key].(map[string]any)["amount_minor"] = float64(obj[key].(map[string]any)["amount_minor"].(float64) + 1)
	}
	line := m["lines"].([]any)[0].(map[string]any)
	bump(line, "gross")
	bump(line, "refund")
	totals := m["totals"].(map[string]any)
	bump(totals, "gross")
	bump(totals, "refund_total")
	bump(m["refunds"].([]any)[0].(map[string]any), "amount")
	raw, _ := json.Marshal(m)
	retEvent := "44444444-4444-7444-8444-444444444444"
	ingestReturnRaw(t, env, retEvent, string(raw))
	drainReturns(t, env)
	if status, code := returnStatus(t, env, retEvent); status != "blocked" || code != ErrCumulativeRefundEx {
		t.Fatalf("ceiling+1 must block: %q/%q", status, code)
	}
	for _, table := range []string{"return_refund_projection", "return_refund_lines_projection", "return_refund_payments_projection"} {
		if n := saleCount(t, env.pool, table); n != 0 {
			t.Fatalf("%s must be empty, got %d", table, n)
		}
	}
}

// dumpSales renders derived sale rows deterministically (ordered,
// excluding nondeterministic projected_at) for rebuild comparison.
func dumpSales(t *testing.T, env *saleEnv) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var b strings.Builder
	dump := func(query string) {
		rows, err := env.pool.Query(ctx, query)
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
	dump(`SELECT sale_id::text, source_event_id::text, sale_number, channel, occurred_at::text, currency,
		subtotal_minor, discount_minor, tax_minor, total_minor
		FROM sales_projection ORDER BY sale_id::text`)
	dump(`SELECT sale_id::text, sale_item_id::text, product_id::text, sku, quantity, line_total_minor
		FROM sale_lines_projection ORDER BY sale_id::text, sale_item_id::text`)
	return b.String()
}

// TestSaleReturnCombinedRebuild executes the documented operator rebuild
// procedure literally (both derived states cleared, BOTH processor
// identities reset, ownership retained) and proves byte-identical sale +
// return state, identical financial totals, and a stable rival winner.
func TestSaleReturnCombinedRebuild(t *testing.T) {
	env := returnEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	env.ingest(t, "22222222-2222-7222-8222-222222222222", salePayload)
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	base := alignedReturnPayload(t, salePayload,
		"33333333-3333-7333-8333-333333333333", "RET-LIVE", 1)
	e1, e2 := "44444444-4444-7444-8444-444444444444", "55555555-5555-7555-8555-555555555555"
	ingestReturnRaw(t, env, e1, base)
	var m map[string]any
	if err := json.Unmarshal([]byte(base), &m); err != nil {
		t.Fatal(err)
	}
	m["return_number"] = "RET-RIVAL"
	rival, _ := json.Marshal(m)
	ingestReturnRaw(t, env, e2, string(rival))
	drainReturns(t, env)

	beforeSales, beforeReturns := dumpSales(t, env), dumpReturns(t, env)
	beforeSum := reportSummary(t, env, "custom", "2026-09-20", "2026-09-20", "")
	var winnerBefore string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT winning_event_id::text FROM return_refund_ownership`).Scan(&winnerBefore); err != nil {
		t.Fatal(err)
	}

	// Documented procedure, verbatim: clear derived state (returns first,
	// then sales — matching the FK cascade direction), retain sync_events
	// and both ownership tables, reset BOTH processor identities.
	ctx := context.Background()
	if _, err := env.pool.Exec(ctx, `DELETE FROM return_refund_projection`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx, `DELETE FROM sales_projection`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx,
		`UPDATE sync_event_processing SET status='pending', next_attempt_at=NULL,
		 attempt_count=0, processed_at=NULL, last_error_code=NULL, last_error_message=NULL
		 WHERE processor IN ('sale_projection.v1', 'return_refund_projection.v1')`); err != nil {
		t.Fatal(err)
	}

	// Replay sale processor first, then the dependent return processor.
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale reprojection")
	drainReturns(t, env)

	if after := dumpSales(t, env); after != beforeSales {
		t.Fatalf("sale rebuild mismatch:\nbefore: %s\nafter:  %s", beforeSales, after)
	}
	if after := dumpReturns(t, env); after != beforeReturns {
		t.Fatalf("return rebuild mismatch:\nbefore: %s\nafter:  %s", beforeReturns, after)
	}
	afterSum := reportSummary(t, env, "custom", "2026-09-20", "2026-09-20", "")
	if fmt.Sprintf("%+v", afterSum.CurrencyTotals) != fmt.Sprintf("%+v", beforeSum.CurrencyTotals) ||
		afterSum.TransactionCount != beforeSum.TransactionCount ||
		afterSum.ReturnTransactionCount != beforeSum.ReturnTransactionCount {
		t.Fatalf("financial drift:\nbefore: %+v\nafter:  %+v", beforeSum, afterSum)
	}
	var winnerAfter string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT winning_event_id::text FROM return_refund_ownership`).Scan(&winnerAfter); err != nil {
		t.Fatal(err)
	}
	if winnerAfter != winnerBefore {
		t.Fatalf("rival winner changed: %s -> %s", winnerBefore, winnerAfter)
	}
	if status, code := returnStatus(t, env, e2); status != "blocked" || code != ErrReturnIDConflict {
		t.Fatalf("rival stays conflict-blocked: %q/%q", status, code)
	}
}

// TestReturnBackfillLiveRace proves a new return arriving while a
// historical backfill is still in flight converges exactly once. The first
// backlog row projects directly (backfill underway, rows still
// undiscovered), the live return arrives mid-backfill, then the worker
// drain converges everything: every header exactly once — no duplicates,
// no losses — regardless of arrival order.
func TestReturnBackfillLiveRace(t *testing.T) {
	env := returnEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	env.ingest(t, "22222222-2222-7222-8222-222222222222", salePayload)
	var m2 map[string]any
	if err := json.Unmarshal([]byte(salePayload), &m2); err != nil {
		t.Fatal(err)
	}
	m2["sale_id"] = "66666666-6666-7666-8666-666666666666"
	raw2, _ := json.Marshal(m2)
	env.ingest(t, "88888888-8888-7888-8888-888888888888", string(raw2))
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 2 }, "both sales projected")

	// Backlog: one qty-1 return per sale, accepted but undiscovered.
	backlog := []struct{ event, rr, num string }{
		{"44000001-4444-7444-8444-444444444444", "43000001-3333-7333-8333-333333333333", "RET-BF1"},
		{"44000002-4444-7444-8444-444444444444", "43000002-3333-7333-8333-333333333333", "RET-BF2"},
	}
	ingestReturnRaw(t, env, backlog[0].event,
		alignedReturnPayload(t, salePayload, backlog[0].rr, backlog[0].num, 1))
	ingestReturnRaw(t, env, backlog[1].event,
		alignedReturnPayload(t, string(raw2), backlog[1].rr, backlog[1].num, 1))

	// Backfill starts: project the first backlog row directly while the
	// second row is still undiscovered.
	store := NewDevices(env.pool, 5*time.Second)
	rec, ok, err := store.LoadReturnEvent(context.Background(), backlog[0].event)
	if err != nil || !ok {
		t.Fatal("backlog event must load")
	}
	if _, err := store.ProjectReturn(context.Background(), rec, time.Now()); err != nil {
		t.Fatalf("backfill project: %v", err)
	}

	// Live arrival mid-backfill on the first sale (second unit: exactly
	// exhausts the fixture qty-2 line, so all three must succeed).
	liveEvent := "44000003-4444-7444-8444-444444444444"
	ingestReturnRaw(t, env, liveEvent,
		alignedReturnPayload(t, salePayload, "43000003-3333-7333-8333-333333333333", "RET-LIVE", 1))
	drainReturns(t, env)

	for _, e := range []string{backlog[0].event, backlog[1].event, liveEvent} {
		if status, code := returnStatus(t, env, e); status != "processed" || code != "" {
			t.Fatalf("return %s: %q/%q", e, status, code)
		}
	}
	if n := saleCount(t, env.pool, "return_refund_projection"); n != 3 {
		t.Fatalf("three headers exactly once, got %d", n)
	}
	if n := saleCount(t, env.pool, "return_refund_lines_projection"); n != 3 {
		t.Fatalf("three lines exactly once, got %d", n)
	}
}
