package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSaleEndToEndUSD(t *testing.T) {
	env := openSaleEnv(t)
	eventID := "22222222-2222-7222-8222-222222222222"
	res := env.ingest(t, eventID, fixture(t, "sale_usd.json"))
	if res.Events[0].Status != "accepted" {
		t.Fatalf("want accepted, got %+v", res)
	}
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")

	// Header exact.
	var (
		saleNumber, channel, currency, shopEN, cashier string
		subtotal, discount, tax, total, micro          int64
		fxBase, fxQuote, fxRate                        string
		srcEvent, srcDevice                            string
	)
	err := env.pool.QueryRow(context.Background(), `
		SELECT sale_number, channel, currency, shop_name_en, cashier_name,
			subtotal_minor, discount_minor, tax_minor, total_minor,
			fx_base, fx_quote, fx_rate, fx_rate_microrate,
			source_event_id::text, source_device_id::text
		FROM sales_projection WHERE sale_id = '11111111-1111-4111-8111-111111111111'`).Scan(
		&saleNumber, &channel, &currency, &shopEN, &cashier,
		&subtotal, &discount, &tax, &total, &fxBase, &fxQuote, &fxRate, &micro,
		&srcEvent, &srcDevice)
	if err != nil {
		t.Fatal(err)
	}
	if saleNumber != "MLR-20260920-11111111" || channel != "STORE" || currency != "USD" ||
		shopEN != "Papyrus Shop" || cashier != "Amal" ||
		subtotal != 1300 || discount != 100 || tax != 50 || total != 1250 ||
		fxBase != "USD" || fxQuote != "EGP" || fxRate != "52.000000" || micro != 52000000 ||
		srcEvent != eventID || srcDevice != env.devID {
		t.Fatal("sale header mismatch")
	}
	// Line exact (historical snapshot, single product_name).
	var sku, pname, lcurr string
	var qty, w, h int
	var unit, ltotal, cost int64
	err = env.pool.QueryRow(context.Background(), `
		SELECT sku, product_name, line_currency, quantity, width_cm, height_cm,
			unit_price_minor, line_total_minor, cost_minor
		FROM sale_lines_projection`).Scan(&sku, &pname, &lcurr, &qty, &w, &h, &unit, &ltotal, &cost)
	if err != nil {
		t.Fatal(err)
	}
	if sku != "PAP-001-70x100" || pname != "Tutankhamun" || lcurr != "USD" ||
		qty != 1 || w != 70 || h != 100 || unit != 1300 || ltotal != 1300 || cost != 400 {
		t.Fatal("sale line mismatch")
	}
	// Split payments preserved exactly, no aggregation.
	var methods []string
	rows, err := env.pool.Query(context.Background(),
		`SELECT method, amount_minor FROM sale_payments_projection ORDER BY position`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var amounts []int64
	for rows.Next() {
		var m string
		var a int64
		if err := rows.Scan(&m, &a); err != nil {
			t.Fatal(err)
		}
		methods, amounts = append(methods, m), append(amounts, a)
	}
	if len(methods) != 2 || methods[0] != "cash" || methods[1] != "card" ||
		amounts[0] != 1000 || amounts[1] != 250 {
		t.Fatalf("payments mismatch: %v %v", methods, amounts)
	}
	// Classifications: root + subcategory with bilingual names.
	var roots, subs int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FILTER (WHERE classification_kind='root'),
		        count(*) FILTER (WHERE classification_kind='subcategory')
		 FROM sale_line_classifications_projection`).Scan(&roots, &subs); err != nil || roots != 1 || subs != 1 {
		t.Fatalf("classifications mismatch: %d/%d (%v)", roots, subs, err)
	}
	var ar, en string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT name_ar, name_en FROM sale_line_classifications_projection
		 WHERE classification_kind='root'`).Scan(&ar, &en); err != nil || ar != "فرعوني" || en != "Pharaonic" {
		t.Fatalf("classification names mismatch: %q %q (%v)", ar, en, err)
	}
	// Processing marked processed.
	var status string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT status FROM sync_event_processing WHERE event_id=$1`, eventID).Scan(&status); err != nil || status != "processed" {
		t.Fatalf("processing status: %q (%v)", status, err)
	}
	// Operator stats must survive NULL error columns (regression).
	stats, err := NewDevices(env.pool, 5*time.Second).ProcessingStats(context.Background(), "sale_projection.v1")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Counts["processed"] != 1 || stats.PendingCount != 0 || stats.LastErrorCode != "" {
		t.Fatalf("bad stats: %+v", stats)
	}
}

func TestSaleEndToEndEGPNoFx(t *testing.T) {
	env := openSaleEnv(t)
	eventID := "33333333-3333-7333-8333-333333333333"
	env.ingest(t, eventID, fixture(t, "sale_egp.json"))
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "egp projection")
	var fxBase *string
	var micro *int64
	if err := env.pool.QueryRow(context.Background(),
		`SELECT fx_base, fx_rate_microrate FROM sales_projection`).Scan(&fxBase, &micro); err != nil {
		t.Fatal(err)
	}
	if fxBase != nil || micro != nil {
		t.Fatal("EGP sale must project no FX")
	}
}

func TestNoProductTableDependency(t *testing.T) {
	env := openSaleEnv(t)
	var exists bool
	for _, table := range []string{"products", "inventory", "returns", "commerce_orders"} {
		if err := env.pool.QueryRow(context.Background(),
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name=$1)`, table).Scan(&exists); err != nil || exists {
			t.Fatalf("table %s must not exist in Phase 2B", table)
		}
	}
}

func TestTransportDuplicateOneSale(t *testing.T) {
	env := openSaleEnv(t)
	eventID := "22222222-2222-7222-8222-222222222222"
	payload := fixture(t, "sale_egp.json")
	r1 := env.ingest(t, eventID, payload)
	r2 := env.ingest(t, eventID, payload)
	if r1.Events[0].Status != "accepted" || r2.Events[0].Status != "already_accepted" {
		t.Fatalf("bad statuses: %+v %+v", r1, r2)
	}
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")
	for _, table := range []string{"sale_lines_projection", "sale_payments_projection", "sale_line_classifications_projection"} {
		if n := saleCount(t, env.pool, table); n == 0 {
			t.Fatalf("%s must have rows", table)
		}
	}
	// Replay the projector explicitly: counts must not move.
	before := map[string]int{}
	for _, table := range []string{"sales_projection", "sale_lines_projection", "sale_payments_projection", "sale_line_classifications_projection"} {
		before[table] = saleCount(t, env.pool, table)
	}
	rec, ok, err := NewDevices(env.pool, 5*time.Second).LoadSaleEvent(context.Background(), eventID)
	if err != nil || !ok {
		t.Fatal("event must load")
	}
	if _, err := NewDevices(env.pool, 5*time.Second).ProjectSale(context.Background(), rec, time.Now()); err != nil {
		t.Fatal(err)
	}
	for table, n := range before {
		if saleCount(t, env.pool, table) != n {
			t.Fatalf("%s duplicated on replay", table)
		}
	}
}

func TestConcurrentDuplicateTransportOneSale(t *testing.T) {
	env := openSaleEnv(t)
	eventID := "22222222-2222-7222-8222-222222222222"
	payload := fixture(t, "sale_egp.json")
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.finalized.v1","occurred_at":"2026-09-20T10:00:00Z","payload":%s}]}`,
		eventID, payload)
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body))
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent identical ingest must succeed: %v", err)
		}
	}
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "sale projection")
	if n := saleCount(t, env.pool, "sale_lines_projection"); n != 1 {
		t.Fatalf("want 1 line, got %d", n)
	}
}

func TestInvalidSaleRejectsWholeBatch(t *testing.T) {
	env := openSaleEnv(t)
	// Structurally valid envelope, invalid sale semantics (line total
	// mismatch): entire batch rejected before ACK, zero rows committed.
	bad := mutatePayload(t, fixture(t, "sale_egp.json"), "44444444-4444-4444-8444-444444444444", "2026-09-20T11:00:00Z")
	var m map[string]any
	if err := json.Unmarshal([]byte(bad), &m); err != nil {
		t.Fatal(err)
	}
	m["totals"].(map[string]any)["total"].(map[string]any)["amount_minor"] = 1
	raw, _ := json.Marshal(m)
	body := fmt.Sprintf(`{"events":[{"event_id":"22222222-2222-7222-8222-222222222222","event_type":"sale.finalized.v1","occurred_at":"2026-09-20T11:00:00Z","payload":%s}]}`, raw)
	_, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body))
	if err == nil {
		t.Fatal("invalid sale must reject the batch")
	}
	if n := saleCount(t, env.pool, "sync_events"); n != 0 {
		t.Fatalf("zero rows on invalid batch, got %d", n)
	}
}

func TestLogicalSaleConflict(t *testing.T) {
	env := openSaleEnv(t)
	payload := fixture(t, "sale_egp.json")
	e1 := "22222222-2222-7222-8222-222222222222"
	e2 := "44444444-4444-7444-8444-444444444444"
	// Same sale_id, different event_ids: both durably accepted (transport ACK).
	r1 := env.ingest(t, e1, payload)
	r2 := env.ingest(t, e2, payload)
	if r1.Events[0].Status != "accepted" || r2.Events[0].Status != "accepted" {
		t.Fatalf("both events must ACK: %+v %+v", r1, r2)
	}
	if n := saleCount(t, env.pool, "sync_events"); n != 2 {
		t.Fatalf("both source events preserved, got %d", n)
	}
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "one sale")
	// Exactly one blocked SALE_ID_CONFLICT, first sale unchanged.
	var code string
	var blocked int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM sync_event_processing WHERE status='blocked' AND last_error_code='SALE_ID_CONFLICT'`).Scan(&blocked); err != nil || blocked != 1 {
		t.Fatalf("want 1 SALE_ID_CONFLICT, got %d (%v)", blocked, err)
	}
	if err := env.pool.QueryRow(context.Background(),
		`SELECT last_error_code FROM sync_event_processing WHERE event_id=$1`, e2).Scan(&code); err != nil {
		// Either event may win the race; the loser must carry the conflict.
		if err := env.pool.QueryRow(context.Background(),
			`SELECT last_error_code FROM sync_event_processing WHERE event_id=$1`, e1).Scan(&code); err != nil {
			t.Fatal(err)
		}
	}
	if code != "SALE_ID_CONFLICT" {
		t.Fatalf("loser must be SALE_ID_CONFLICT, got %q", code)
	}
	_ = code
}

func TestOutOfOrderSales(t *testing.T) {
	env := openSaleEnv(t)
	// B occurred later but arrives first; A occurred earlier, arrives second.
	payloadB := mutatePayload(t, fixture(t, "sale_egp.json"), "44444444-4444-4444-8444-444444444444", "2026-09-20T11:00:00Z")
	payloadA := mutatePayload(t, fixture(t, "sale_egp.json"), "77777777-7777-7777-8777-777777777777", "2026-09-20T10:00:00Z")
	env.ingest(t, "aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa", payloadB)
	env.ingest(t, "bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb", payloadA)
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 2 }, "both sales")
	var a, b string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT occurred_at::text FROM sales_projection WHERE sale_id='77777777-7777-7777-8777-777777777777'`).Scan(&a); err != nil {
		t.Fatal(err)
	}
	if err := env.pool.QueryRow(context.Background(),
		`SELECT occurred_at::text FROM sales_projection WHERE sale_id='44444444-4444-4444-8444-444444444444'`).Scan(&b); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a, "2026-09-20 10:00") || !strings.HasPrefix(b, "2026-09-20 11:00") {
		t.Fatalf("occurrence timestamps must be preserved: %q %q", a, b)
	}
}

func mutatePayload(t *testing.T, payload, saleID, occurred string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatal(err)
	}
	m["sale_id"] = saleID
	m["occurred_at"] = occurred
	m["paid_at"] = occurred
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
