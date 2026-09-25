package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/returnrefund"

	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
)

// projectDashReturn ingests a return payload string and projects it
// synchronously via the return projector path.
func projectDashReturn(t *testing.T, env *dashEnv, eventID, occurred, payload string) {
	t.Helper()
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.return_refund.finalized.v1","occurred_at":%q,"payload":%s}]}`,
		eventID, occurred, payload)
	res, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body))
	if err != nil {
		t.Fatalf("ingest return: %v", err)
	}
	if res.Events[0].Status != "accepted" {
		t.Fatalf("want accepted, got %+v", res)
	}
	store := NewDevices(env.pool, 5*time.Second)
	rec, ok, err := store.LoadReturnEvent(context.Background(), eventID)
	if err != nil || !ok {
		t.Fatalf("load return: %v %v", ok, err)
	}
	pres, err := store.ProjectReturn(context.Background(), rec, time.Now())
	if err != nil {
		t.Fatalf("project return: %v", err)
	}
	if pres.Outcome != returnrefund.OutcomeProcessed && pres.Outcome != returnrefund.OutcomeAlready {
		t.Fatalf("return projection outcome: %+v", pres)
	}
}

// projectDashReturnOutcome is projectDashReturn without the outcome gate:
// callers asserting blocked/conflict paths use the returned outcome.
func projectDashReturnOutcome(t *testing.T, env *dashEnv, eventID, occurred, payload string) returnrefund.ProjectResult {
	t.Helper()
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.return_refund.finalized.v1","occurred_at":%q,"payload":%s}]}`,
		eventID, occurred, payload)
	res, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body))
	if err != nil {
		t.Fatalf("ingest return: %v", err)
	}
	if res.Events[0].Status != "accepted" {
		t.Fatalf("want accepted, got %+v", res)
	}
	store := NewDevices(env.pool, 5*time.Second)
	rec, ok, err := store.LoadReturnEvent(context.Background(), eventID)
	if err != nil || !ok {
		t.Fatalf("load return: %v %v", ok, err)
	}
	pres, err := store.ProjectReturn(context.Background(), rec, time.Now())
	if err != nil {
		t.Fatalf("project return: %v", err)
	}
	return pres
}

// dashReturnPayload builds a valid EGP return for the given sale payload
// (full-line return at unit economics, zero discount allocation).
func dashReturnPayload(t *testing.T, salePayload, rrID, number, occurred string, qty int) string {
	t.Helper()
	var s map[string]any
	if err := json.Unmarshal([]byte(salePayload), &s); err != nil {
		t.Fatal(err)
	}
	line := s["lines"].([]any)[0].(map[string]any)
	unit := int(line["unit_price"].(map[string]any)["amount_minor"].(float64))
	cur := s["currency"].(string)
	amount := unit * qty
	money := func(a int) map[string]any {
		return map[string]any{"amount_minor": a, "currency": cur}
	}
	m := map[string]any{
		"return_refund_id": rrID,
		"return_number":    number,
		"kind":             "return",
		"reason":           "other",
		"sale_id":          s["sale_id"],
		"sale_number":      s["sale_number"],
		"channel":          "STORE",
		"occurred_at":      occurred,
		"shop":             s["shop"],
		"actor":            map[string]any{},
		"currency":         cur,
		"totals": map[string]any{
			"gross": money(amount), "discount": money(0),
			"tax": money(0), "refund_total": money(amount),
		},
		"lines": []any{map[string]any{
			"original_sale_line_id": line["sale_item_id"],
			"quantity":              qty, "restocked": true,
			"gross": money(amount), "discount": money(0),
			"tax": money(0), "refund": money(amount),
		}},
		"refunds": []any{map[string]any{"method": "cash", "amount": money(amount)}},
	}
	if fx, ok := s["fx"]; ok {
		m["fx"] = fx
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestDashboardNetOverview proves net KPI math: normalized gross minus
// normalized refunds with per-event historical FX, counts split.
func TestDashboardNetOverview(t *testing.T) {
	env := openDashEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	projectDashSale(t, env, "aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa", "2026-09-20T10:00:00Z", salePayload)
	projectDashReturn(t, env, "bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb", "2026-09-20T12:00:00Z",
		dashReturnPayload(t, salePayload, "cccccccc-cccc-7ccc-8ccc-cccccccccccc", "RET-1", "2026-09-20T12:00:00Z", 1))

	req := dashReq(t, env, "custom", "2026-09-20", "2026-09-20")
	over, err := env.dash.Overview(context.Background(), req)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	// Fixture sale: total 200000; return: 100000 (unit of qty-2 line).
	if over.Normalized.NormalizedTotalMinor != "200000" {
		t.Fatalf("gross normalized: %+v", over.Normalized)
	}
	if over.Normalized.NormalizedRefundMinor != "100000" {
		t.Fatalf("refund normalized: %+v", over.Normalized)
	}
	if over.Normalized.NormalizedNetMinor != "100000" {
		t.Fatalf("net normalized: %+v", over.Normalized)
	}
	if over.Normalized.Transactions != 1 || over.Normalized.ReturnTransactions != 1 {
		t.Fatalf("transaction split: %+v", over.Normalized)
	}
	if over.Normalized.Units != 2 || over.Normalized.UnitsReturned != 1 {
		t.Fatalf("unit split: %+v", over.Normalized)
	}
	// Summary buckets carry the same net natively.
	if len(over.Summary.CurrencyTotals) != 1 {
		t.Fatalf("buckets: %+v", over.Summary.CurrencyTotals)
	}
	bucket := over.Summary.CurrencyTotals[0]
	if bucket.SalesTotalMinor != "200000" || bucket.RefundTotalMinor != "100000" || bucket.NetSalesMinor != "100000" {
		t.Fatalf("summary bucket: %+v", bucket)
	}
	if over.Summary.TransactionCount != 1 || over.Summary.ReturnTransactionCount != 1 {
		t.Fatalf("summary counts: %+v", over.Summary)
	}
}

// TestDashboardUSDRefundNormalization proves per-event historical FX for
// refunds: two USD returns at different rates normalize independently.
func TestDashboardUSDRefundNormalization(t *testing.T) {
	env := openDashEnv(t)
	usd := fixture(t, "sale_usd.json")
	projectDashSale(t, env, "aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa", "2026-09-20T10:00:00Z", usd)
	// Second USD sale at a different rate (99.0), same line shape.
	var m map[string]any
	if err := json.Unmarshal([]byte(usd), &m); err != nil {
		t.Fatal(err)
	}
	m["sale_id"] = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	m["fx"] = map[string]any{"base": "USD", "quote": "EGP", "rate": "99.000000", "rate_microrate": 99000000}
	raw99, _ := json.Marshal(m)
	projectDashSale(t, env, "dddddddd-dddd-7ddd-8ddd-dddddddddddd", "2026-09-20T11:00:00Z", string(raw99))

	projectDashReturn(t, env, "bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb", "2026-09-20T12:00:00Z",
		usdReturnAt(t, "52.000000", 52000000, "eeeeeeee-eeee-7eee-8eee-eeeeeeeeeeee", "RET-52",
			"11111111-1111-4111-8111-111111111111", "MLR-20260920-11111111"))
	projectDashReturn(t, env, "ffffffff-ffff-7fff-8fff-ffffffffffff", "2026-09-20T13:00:00Z",
		usdReturnAt(t, "99.000000", 99000000, "11111111-1111-7111-8111-111111111111", "RET-99",
			"dddddddd-dddd-4ddd-8ddd-dddddddddddd", "MLR-20260920-dddddddd"))

	req := dashReq(t, env, "custom", "2026-09-20", "2026-09-20")
	over, err := env.dash.Overview(context.Background(), req)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	// round(1250*52) + round(1250*99) = 65000 + 123750 = 188750.
	if over.Normalized.NormalizedRefundMinor != "188750" {
		t.Fatalf("per-event FX refund: %+v", over.Normalized)
	}
	// Gross: round(1250*52) + round(1250*99) = 188750; net zero.
	if over.Normalized.NormalizedTotalMinor != "188750" || over.Normalized.NormalizedNetMinor != "0" {
		t.Fatalf("gross/net: %+v", over.Normalized)
	}
}

// usdReturnAt builds a full USD return for the fixture USD sale with an
// explicit historical rate (mirrors the sale only when rate matches).
func usdReturnAt(t *testing.T, rate string, micro int64, rrID, number, saleID, saleNumber string) string {
	t.Helper()
	money := func(a int) map[string]any {
		return map[string]any{"amount_minor": a, "currency": "USD"}
	}
	m := map[string]any{
		"return_refund_id": rrID,
		"return_number":    number,
		"kind":             "return",
		"reason":           "damaged_item",
		"sale_id":          saleID,
		"sale_number":      saleNumber,
		"channel":          "STORE",
		"occurred_at":      "2026-09-20T12:00:00Z",
		"shop": map[string]any{
			"name_ar": "م", "name_en": "S", "address_ar": "A", "address_en": "A",
			"phone": "P", "receipt_footer_ar": "F", "receipt_footer_en": "F",
		},
		"actor":    map[string]any{},
		"currency": "USD",
		"fx":       map[string]any{"base": "USD", "quote": "EGP", "rate": rate, "rate_microrate": micro},
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

// TestDashboardSyncHealthReturnScope proves sale and return completeness
// stay separate metrics with the 4A note gone from the data model.
func TestDashboardSyncHealthReturnScope(t *testing.T) {
	env := openDashEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	projectDashSale(t, env, "aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa", "2026-09-20T10:00:00Z", salePayload)
	projectDashReturn(t, env, "bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb", "2026-09-20T12:00:00Z",
		dashReturnPayload(t, salePayload, "cccccccc-cccc-7ccc-8ccc-cccccccccccc", "RET-1", "2026-09-20T12:00:00Z", 1))

	health, err := env.dash.SyncHealth(context.Background())
	if err != nil {
		t.Fatalf("sync health: %v", err)
	}
	if health.ProcessedCount != 1 || health.ReturnProcessedCount != 1 {
		t.Fatalf("processed split: %+v", health)
	}
	if !health.Freshness.CloudProjectionComplete || !health.Freshness.ReturnProjectionComplete {
		t.Fatalf("completeness split: %+v", health.Freshness)
	}
	if health.Freshness.LatestReturnEventReceivedAt == nil {
		t.Fatal("return freshness timestamps must exist")
	}
	var _ = dashboard.SyncHealthItem{}
}

// TestDashboardActivityReturnKinds proves return events surface with
// truthful kinds from durable data (no invented activity).
func TestDashboardActivityReturnKinds(t *testing.T) {
	env := openDashEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	projectDashSale(t, env, "aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa", "2026-09-20T10:00:00Z", salePayload)
	projectDashReturn(t, env, "bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb", "2026-09-20T12:00:00Z",
		dashReturnPayload(t, salePayload, "cccccccc-cccc-7ccc-8ccc-cccccccccccc", "RET-1", "2026-09-20T12:00:00Z", 1))

	items, err := env.dash.RecentActivity(context.Background(), 20)
	if err != nil {
		t.Fatalf("activity: %v", err)
	}
	kinds := map[string]bool{}
	for _, it := range items {
		kinds[it.Kind] = true
	}
	for _, want := range []string{"accepted", "projected", "return_accepted", "return_projected"} {
		if !kinds[want] {
			t.Fatalf("activity kinds %v missing %q", kinds, want)
		}
	}
}

// TestDashboardAllNetRanking proves All-mode products/categories rank by
// normalized net: a fully-returned high seller sorts below an untouched
// seller even when it sold more units.
func TestDashboardAllNetRanking(t *testing.T) {
	env := openDashEnv(t)
	withProduct := func(payload, saleID, pid, sku, name string) string {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			t.Fatal(err)
		}
		m["sale_id"] = saleID
		line := m["lines"].([]any)[0].(map[string]any)
		line["product_id"], line["sku"], line["product_name"] = pid, sku, name
		raw, _ := json.Marshal(m)
		return string(raw)
	}
	base := fixture(t, "sale_egp.json")
	saleA := withProduct(base, "aaaaaaaa-1111-4111-8111-111111111111", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "NRANK-A", "NRank A")
	saleB := withProduct(base, "bbbbbbbb-2222-4222-8222-222222222222", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "NRANK-B", "NRank B")
	projectDashSale(t, env, "aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa", "2026-09-20T10:00:00Z", saleA)
	projectDashSale(t, env, "bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb", "2026-09-20T11:00:00Z", saleB)
	// Fully return A (qty 2 of fixture line).
	projectDashReturn(t, env, "cccccccc-cccc-7ccc-8ccc-cccccccccccc", "2026-09-20T12:00:00Z",
		dashReturnPayload(t, saleA, "dddddddd-dddd-7ddd-8ddd-dddddddddddd", "RET-NR", "2026-09-20T12:00:00Z", 2))

	req := dashReq(t, env, "custom", "2026-09-20", "2026-09-20")
	products, err := env.dash.ProductsNormalized(context.Background(), req)
	if err != nil {
		t.Fatalf("products: %v", err)
	}
	if len(products) != 2 {
		t.Fatalf("two products: %+v", products)
	}
	if products[0].SKU != "NRANK-B" || products[1].SKU != "NRANK-A" {
		t.Fatalf("All net order NRANK-B then NRANK-A: %+v", products)
	}
	if products[1].NetNormalizedMinor != "0" {
		t.Fatalf("fully returned net is zero: %+v", products[1])
	}
	cats, err := env.dash.CategoriesNormalized(context.Background(), req, "root_category")
	if err != nil {
		t.Fatalf("categories: %v", err)
	}
	if len(cats) < 1 {
		t.Fatalf("category rows: %+v", cats)
	}
	// Both sales share the fixture root; single row nets 200000.
	if cats[0].NetNormalizedMinor != "200000" {
		t.Fatalf("root net = 400000 gross − 200000 refund: %+v", cats[0])
	}
}

// TestDashboardDualErrorChannels proves a blocked Sale error and a blocked
// Return error coexist on independent diagnostic channels: neither
// overwrites the other, and both carry allowlisted codes + labels.
func TestDashboardDualErrorChannels(t *testing.T) {
	env := openDashEnv(t)
	salePayload := fixture(t, "sale_egp.json")
	// Same sale_id twice: second sale blocks with SALE_ID_CONFLICT.
	projectDashSale(t, env, "aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa", "2026-09-20T10:00:00Z", salePayload)
	projectDashSale(t, env, "dddddddd-dddd-7ddd-8ddd-dddddddddddd", "2026-09-20T11:00:00Z", salePayload)
	// Rival returns: loser blocks with RETURN_REFUND_ID_CONFLICT.
	base := dashReturnPayload(t, salePayload, "cccccccc-cccc-7ccc-8ccc-cccccccccccc", "RET-LIVE", "2026-09-20T12:00:00Z", 1)
	projectDashReturn(t, env, "bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb", "2026-09-20T12:00:00Z", base)
	var m map[string]any
	if err := json.Unmarshal([]byte(base), &m); err != nil {
		t.Fatal(err)
	}
	m["return_number"] = "RET-RIVAL"
	rival, _ := json.Marshal(m)
	rout := projectDashReturnOutcome(t, env, "eeeeeeee-eeee-7eee-8eee-eeeeeeeeeeee", "2026-09-20T13:00:00Z", string(rival))
	if rout.Outcome != returnrefund.OutcomeBlocked || rout.ErrorCode != "RETURN_REFUND_ID_CONFLICT" {
		t.Fatalf("rival outcome: %+v", rout)
	}

	health, err := env.dash.SyncHealth(context.Background())
	if err != nil {
		t.Fatalf("sync health: %v", err)
	}
	if health.LastErrorCode != "SALE_ID_CONFLICT" {
		t.Fatalf("sale channel preserved: %+v", health)
	}
	if health.ReturnLastErrorCode != "RETURN_REFUND_ID_CONFLICT" {
		t.Fatalf("return channel populated: %+v", health)
	}
	if health.LastErrorLabelAR == "" || health.ReturnLastErrorLabelAR == "" {
		t.Fatalf("bilingual labels required: %+v", health)
	}
	if health.BlockedCount != 1 || health.ReturnBlockedCount != 1 {
		t.Fatalf("blocked split: %+v", health)
	}
}
