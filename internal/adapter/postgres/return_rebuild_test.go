package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

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
		fx_base, fx_quote, fx_rate_microrate, received_at::text
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
		rec, ok, err := NewReturnProjection(store.pool, store.timeout).LoadReturnEvent(context.Background(), id)
		if err != nil || !ok {
			t.Fatal("load")
		}
		if _, err := NewReturnProjection(store.pool, store.timeout).ProjectReturn(context.Background(), rec, time.Now()); err != nil {
			t.Fatalf("rebuild project %s: %v", id, err)
		}
	}
	// Settle any transient (none expected, bounded).
	drainReturns(t, env)
	if after := dumpReturns(t, env); after != before {
		t.Fatalf("rival rebuild mismatch:\nbefore: %s\nafter:  %s", before, after)
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
		 WHERE processor IN ('sale_projection.v3', 'return_refund_projection.v1')`); err != nil {
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
	rec, ok, err := NewReturnProjection(store.pool, store.timeout).LoadReturnEvent(context.Background(), backlog[0].event)
	if err != nil || !ok {
		t.Fatal("backlog event must load")
	}
	if _, err := NewReturnProjection(store.pool, store.timeout).ProjectReturn(context.Background(), rec, time.Now()); err != nil {
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
