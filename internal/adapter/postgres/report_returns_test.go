package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/report"
	"github.com/faroukelabady/MoonLightCloud/internal/returnrefund"
)

var returnEventSeq = 5000

// projectReturnSync ingests a return with exact caller-supplied economics
// and projects it synchronously. Callers own the arithmetic (fixtures use
// values derived from the frozen allocation); the projector cross-checks.
func projectReturnSync(t *testing.T, env *saleEnv, saleID, saleNumber, currency string, fx, shop map[string]any,
	lineID string, productID *string, qty int, gross, discount, tax, refund int64, cost *int64,
	occurred, kind, reason, rrSuffix string) string {
	t.Helper()
	returnEventSeq++
	eventID := fmt.Sprintf("7700%04d-7777-7777-8777-777777777777", returnEventSeq)
	rrID := fmt.Sprintf("7800%04d-8888-8888-8888-888888888888", returnEventSeq)
	money := func(a int64) map[string]any {
		return map[string]any{"amount_minor": a, "currency": currency}
	}
	var costJSON any
	if cost != nil {
		costJSON = money(*cost)
	}
	m := map[string]any{
		"return_refund_id": rrID,
		"return_number":    "RET-" + rrSuffix,
		"kind":             kind,
		"reason":           reason,
		"sale_id":          saleID,
		"sale_number":      saleNumber,
		"channel":          "STORE",
		"occurred_at":      occurred,
		"shop":             shop,
		"actor":            map[string]any{},
		"currency":         currency,
		"totals": map[string]any{
			"gross": money(gross), "discount": money(discount),
			"tax": money(tax), "refund_total": money(refund),
		},
		"lines": []any{map[string]any{
			"original_sale_line_id": lineID,
			"product_id":            productID,
			"quantity":              qty, "restocked": true,
			"gross": money(gross), "discount": money(discount),
			"tax": money(tax), "refund": money(refund),
			"cost": costJSON,
		}},
		"refunds": []any{map[string]any{"method": "cash", "amount": money(refund)}},
	}
	if fx != nil {
		m["fx"] = fx
	}
	payload, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
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
	return eventID
}

// returnScenario builds the §111 validation fixture: Sale A (EGP, partial
// returns A1+A2), Sale B (USD + historical FX return), Sale C (no return),
// Sale D (prior-day sale, current-day return).
func returnScenario(t *testing.T, env *saleEnv) {
	t.Helper()
	// Sale A: EGP fixture sale 4444... (line qty 2, unit 100000).
	projectSale(t, env, "44444444-4444-4444-8444-444444444444", "2026-09-20T10:00:00Z", nil)
	var saleA map[string]any
	if err := json.Unmarshal([]byte(fixture(t, "sale_egp.json")), &saleA); err != nil {
		t.Fatal(err)
	}
	lineA := saleA["lines"].([]any)[0].(map[string]any)
	unit := int(lineA["unit_price"].(map[string]any)["amount_minor"].(float64))
	for i, qty := range []int{1, 1} {
		projectReturnSync(t, env,
			"44444444-4444-4444-8444-444444444444", "MLR-20260920-44444444", "EGP", nil, saleA["shop"].(map[string]any),
			lineA["sale_item_id"].(string), strPtrOf(lineA["product_id"]), qty,
			int64(unit*qty), 0, 0, int64(unit*qty), int64Ptr(400*qty),
			"2026-09-20T12:00:00Z", "return", "customer_changed_mind", fmt.Sprintf("A%d", i+1))
	}
	// Sale B: USD fixture (own sale_id 1111...), full return at 52.0.
	saleEventSeq++
	usdEvent := fmt.Sprintf("aaaaaaaa-aaaa-7aaa-8aaa-%012d", saleEventSeq)
	usdBody := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.finalized.v1","occurred_at":"2026-09-20T10:00:00Z","payload":%s}]}`,
		usdEvent, fixture(t, "sale_usd.json"))
	if _, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(usdBody)); err != nil {
		t.Fatalf("ingest usd: %v", err)
	}
	store := NewDevices(env.pool, 5*time.Second)
	rec, _, _ := store.LoadSaleEvent(context.Background(), usdEvent)
	if _, err := store.ProjectSale(context.Background(), rec, time.Now()); err != nil {
		t.Fatalf("project usd: %v", err)
	}
	projectReturnSync(t, env,
		"11111111-1111-4111-8111-111111111111", "MLR-20260920-11111111", "USD",
		map[string]any{"base": "USD", "quote": "EGP", "rate": "52.000000", "rate_microrate": 52000000},
		map[string]any{"name_ar": "م", "name_en": "S", "address_ar": "A", "address_en": "A", "phone": "P", "receipt_footer_ar": "F", "receipt_footer_en": "F"},
		"33333333-3333-4333-8333-333333333333", strPtrOf("22222222-2222-4222-8222-222222222222"), 1,
		1300, 100, 50, 1250, int64Ptr(400),
		"2026-09-20T13:00:00Z", "return", "damaged_item", "B1")
	// Sale C: no return.
	projectSale(t, env, "cccccccc-cccc-4ccc-8ccc-cccccccccccc", "2026-09-20T11:00:00Z", nil)
	// Sale D: prior Cairo day, returned on the current day.
	projectSale(t, env, "dddddddd-dddd-4ddd-8ddd-dddddddddddd", "2026-09-19T10:00:00Z", nil)
	var saleD map[string]any
	if err := json.Unmarshal([]byte(fixture(t, "sale_egp.json")), &saleD); err != nil {
		t.Fatal(err)
	}
	lineD := saleD["lines"].([]any)[0].(map[string]any)
	projectReturnSync(t, env,
		"dddddddd-dddd-4ddd-8ddd-dddddddddddd", "MLR-20260919-dddddddd", "EGP", nil, saleD["shop"].(map[string]any),
		lineD["sale_item_id"].(string), strPtrOf(lineD["product_id"]), 1,
		100000, 0, 0, 100000, int64Ptr(400),
		"2026-09-20T14:00:00Z", "return", "wrong_item", "D1")
}

func strPtrOf(v any) *string {
	if v == nil {
		return nil
	}
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}

func int64Ptr(v int) *int64 {
	out := int64(v)
	return &out
}

func reportDaily(t *testing.T, env *saleEnv, kind, from, to, currency string) report.Daily {
	t.Helper()
	svc := repService(env)
	req, err := svc.ParseRequest(kind, from, to, currency)
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	out, err := svc.Daily(context.Background(), req)
	if err != nil {
		t.Fatalf("daily: %v", err)
	}
	return out
}

func reportBreakdown(t *testing.T, env *saleEnv, kind, from, to, currency, dim string) report.Breakdown {
	t.Helper()
	svc := repService(env)
	req, err := svc.ParseRequest(kind, from, to, currency)
	if err != nil {
		t.Fatalf("parse request: %v", err)
	}
	out, err := svc.Breakdown(context.Background(), req, dim)
	if err != nil {
		t.Fatalf("breakdown: %v", err)
	}
	return out
}

// TestReportSummaryNet verifies summary gross/refund/net, counts, costs,
// and freshness with return projection state.
func TestReportSummaryNet(t *testing.T) {
	env := openSaleEnv(t)
	returnScenario(t, env)

	sum := reportSummary(t, env, "custom", "2026-09-20", "2026-09-20", "")
	// Sales: A(200000) + B(1250 USD) + C(200000). Returns: A1+A2(200000) + B1(1250).
	// Sales A, B, C; returns A1, A2, B1, D1 (D1 falls on 09-20 too).
	if sum.TransactionCount != 3 || sum.ReturnTransactionCount != 4 {
		t.Fatalf("counts: %+v", sum)
	}
	if sum.UnitsSold != 5 || sum.UnitsReturned != 4 {
		t.Fatalf("units: %+v", sum)
	}
	if len(sum.CurrencyTotals) != 2 {
		t.Fatalf("buckets: %+v", sum.CurrencyTotals)
	}
	egp, usd := sum.CurrencyTotals[0], sum.CurrencyTotals[1]
	if egp.Currency != "EGP" || egp.SalesTotalMinor != 400000 || egp.RefundTotalMinor != 300000 || egp.NetSalesMinor != 100000 {
		t.Fatalf("egp bucket: %+v", egp)
	}
	// Fixture EGP sale lines carry no cost snapshot; return costs (3x400)
	// still reverse exactly, driving net cost negative here.
	if egp.ReturnedUnits != 3 || egp.ReturnedCostMinor != 1200 || egp.LineCostMinor != 0 || egp.NetCostMinor != -1200 {
		t.Fatalf("egp cost/units: %+v", egp)
	}
	if usd.Currency != "USD" || usd.SalesTotalMinor != 1250 || usd.RefundTotalMinor != 1250 || usd.NetSalesMinor != 0 {
		t.Fatalf("usd bucket: %+v", usd)
	}
	if usd.ReturnedUnits != 1 || usd.ReturnedCostMinor != 400 || usd.NetCostMinor != 0 {
		t.Fatalf("usd cost/units: %+v", usd)
	}
	if !sum.Freshness.ReturnProjectionComplete || sum.Freshness.ReturnBacklogCount != 0 {
		t.Fatalf("return freshness: %+v", sum.Freshness)
	}
	if sum.Freshness.LatestReturnEventReceivedAt == nil || sum.Freshness.LatestProjectedReturnOccurredAt == nil {
		t.Fatalf("return timestamps: %+v", sum.Freshness)
	}
}

// TestReportNegativeNet proves a refund-only period reports signed net
// without clamping, overflow, or schema failure.
func TestReportNegativeNet(t *testing.T) {
	env := openSaleEnv(t)
	returnScenario(t, env)

	// Only Sale D's return falls on 09-21... no: returns are on 09-20.
	// Use a dedicated pair: sale on 09-18, return on 09-20, query 09-20
	// restricted so only the return lands: craft via custom dates on a
	// fresh env instead.
	env2 := openSaleEnv(t)
	projectSale(t, env2, "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", "2026-09-18T10:00:00Z", nil)
	var saleE map[string]any
	if err := json.Unmarshal([]byte(fixture(t, "sale_egp.json")), &saleE); err != nil {
		t.Fatal(err)
	}
	lineE := saleE["lines"].([]any)[0].(map[string]any)
	projectReturnSync(t, env2,
		"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", "MLR-20260918-eeeeeeee", "EGP", nil, saleE["shop"].(map[string]any),
		lineE["sale_item_id"].(string), strPtrOf(lineE["product_id"]), 2,
		200000, 0, 0, 200000, int64Ptr(800),
		"2026-09-20T10:00:00Z", "return", "other", "NEG")
	sum := reportSummary(t, env2, "custom", "2026-09-20", "2026-09-20", "")
	if len(sum.CurrencyTotals) != 1 {
		t.Fatalf("one bucket: %+v", sum.CurrencyTotals)
	}
	egp := sum.CurrencyTotals[0]
	if egp.SalesTotalMinor != 0 || egp.RefundTotalMinor != 200000 || egp.NetSalesMinor != -200000 {
		t.Fatalf("negative net: %+v", egp)
	}
	if egp.NetCostMinor != -800 {
		t.Fatalf("negative net cost: %+v", egp)
	}
	if sum.TransactionCount != 0 || sum.ReturnTransactionCount != 1 {
		t.Fatalf("counts: %+v", sum)
	}
	// JSON round trip keeps the sign (schema-signed proof).
	raw, err := json.Marshal(sum)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	bucket := decoded["currency_totals"].([]any)[0].(map[string]any)
	if bucket["net_sales_minor"].(float64) != -200000 {
		t.Fatalf("net sign lost in JSON: %v", bucket["net_sales_minor"])
	}
}

// TestReportDailyNet verifies per-day gross/refund/net with sale and
// return dates kept distinct (Sale D on 09-19, its return on 09-20).
func TestReportDailyNet(t *testing.T) {
	env := openSaleEnv(t)
	returnScenario(t, env)

	daily := reportDaily(t, env, "custom", "2026-09-19", "2026-09-20", "")
	if len(daily.Days) != 2 {
		t.Fatalf("two days: %+v", daily.Days)
	}
	day19, day20 := daily.Days[0], daily.Days[1]
	if day19.Date != "2026-09-19" || day20.Date != "2026-09-20" {
		t.Fatalf("ascending dates: %+v", daily.Days)
	}
	// 09-19: only Sale D gross, no refunds.
	if day19.Transactions != 1 || day19.ReturnTransactions != 0 {
		t.Fatalf("day19 counts: %+v", day19)
	}
	// 09-20: 3 sales + 4 returns (A1, A2, B1, D1).
	if day20.Transactions != 3 || day20.ReturnTransactions != 4 {
		t.Fatalf("day20 counts: %+v", day20)
	}
	for _, day := range daily.Days {
		for _, b := range day.CurrencyTotals {
			if b.NetSalesMinor != b.SalesTotalMinor-b.RefundTotalMinor {
				t.Fatalf("day %s bucket %s net: %+v", day.Date, b.Currency, b)
			}
		}
	}
	egp20 := day20.CurrencyTotals[0]
	if egp20.Currency != "EGP" || egp20.SalesTotalMinor != 400000 || egp20.RefundTotalMinor != 300000 || egp20.NetSalesMinor != 100000 {
		t.Fatalf("day20 egp: %+v", egp20)
	}
}

// TestReportBreakdownNet verifies product/root/subcategory/cashier/channel
// breakdowns merge refunds into the same attribution the sales used, with
// net derivable and counts un-conflated.
func TestReportBreakdownNet(t *testing.T) {
	env := openSaleEnv(t)
	returnScenario(t, env)

	// Product: fixture product reverses 2 units / 200000 (A1+A2), plus the
	// D1 return of the same product snapshot.
	prod := reportBreakdown(t, env, "custom", "2026-09-20", "2026-09-20", "", "product")
	if len(prod.Rows) == 0 {
		t.Fatal("product rows must exist")
	}
	// PAP-002 rides sales A and C in-window (Sale D is on 09-19);
	// returns A1, A2, D1 all fall on 09-20.
	found := false
	for _, row := range prod.Rows {
		if row.SKU == nil || *row.SKU != "PAP-002" {
			continue
		}
		found = true
		if row.Units != 4 || row.UnitsReturned != 3 {
			t.Fatalf("product units: %+v", row)
		}
		var gross, refund int64
		for _, b := range row.LineSales {
			if b.Currency != "EGP" {
				continue
			}
			gross = b.LineSalesMinor
			refund = b.LineRefundMinor
			if b.LineReturnedCostMinor != 1200 {
				t.Fatalf("product returned cost: %+v", b)
			}
		}
		if gross != 400000 || refund != 300000 || gross-refund != 100000 {
			t.Fatalf("product gross/refund: %+v", row.LineSales)
		}
	}
	if !found {
		t.Fatal("fixture product row missing")
	}

	// Root categories mirror the same fanout the sales used.
	roots := reportBreakdown(t, env, "custom", "2026-09-20", "2026-09-20", "", "root_category")
	if len(roots.Rows) == 0 {
		t.Fatal("root rows must exist")
	}
	for _, row := range roots.Rows {
		if row.ClassificationKind == nil || *row.ClassificationKind != "root" {
			t.Fatalf("root kind: %+v", row)
		}
		for _, b := range row.LineSales {
			if b.LineRefundMinor < 0 || b.LineReturnedCostMinor < 0 {
				t.Fatalf("negative reversal: %+v", b)
			}
		}
	}
	subs := reportBreakdown(t, env, "custom", "2026-09-20", "2026-09-20", "", "subcategory")
	if len(subs.Rows) == 0 {
		t.Fatal("subcategory rows must exist (facet semantics preserved)")
	}

	// Cashier: refunds attribute to the original sale cashier.
	cashiers := reportBreakdown(t, env, "custom", "2026-09-20", "2026-09-20", "", "cashier")
	if len(cashiers.Rows) == 0 {
		t.Fatal("cashier rows must exist")
	}
	for _, row := range cashiers.Rows {
		if row.Transactions == 0 && row.ReturnTransactions == 0 {
			t.Fatalf("empty cashier row: %+v", row)
		}
		for _, b := range row.CurrencyTotals {
			if b.NetSalesMinor != b.SalesTotalMinor-b.RefundTotalMinor {
				t.Fatalf("cashier net: %+v", b)
			}
		}
	}

	// Channel: single STORE channel carries both sides.
	channels := reportBreakdown(t, env, "custom", "2026-09-20", "2026-09-20", "", "channel")
	if len(channels.Rows) != 1 || channels.Rows[0].Channel == nil || *channels.Rows[0].Channel != "STORE" {
		t.Fatalf("channel rows: %+v", channels.Rows)
	}
	if channels.Rows[0].ReturnTransactions != 4 {
		t.Fatalf("channel return transactions: %+v", channels.Rows[0])
	}
}

// TestReportRenameStaysSeparate proves returns aggregate under the
// historical snapshot identity: a renamed product snapshot forms its own
// row and the return follows the sale-time snapshot, never the current one.
func TestReportRenameStaysSeparate(t *testing.T) {
	env := openSaleEnv(t)
	projectSale(t, env, "aaaaaaaa-1111-4111-8111-111111111111", "2026-09-20T10:00:00Z", func(m map[string]any) {
		m["lines"].([]any)[0].(map[string]any)["product_name"] = "Tutankhamun OLD"
	})
	projectSale(t, env, "bbbbbbbb-2222-4222-8222-222222222222", "2026-09-20T11:00:00Z", func(m map[string]any) {
		m["lines"].([]any)[0].(map[string]any)["product_name"] = "Tutankhamun NEW"
	})
	prod := reportBreakdown(t, env, "custom", "2026-09-20", "2026-09-20", "", "product")
	if len(prod.Rows) != 2 {
		t.Fatalf("renamed snapshots stay separate rows: %+v", prod.Rows)
	}
}

// TestReportNativeNetRanking proves native-scope product/category rows rank
// by net (gross − refund), not units: a fully-returned high seller sorts
// below an untouched seller, and negative nets sort naturally unclamped.
func TestReportNativeNetRanking(t *testing.T) {
	env := openSaleEnv(t)
	var base map[string]any
	if err := json.Unmarshal([]byte(fixture(t, "sale_egp.json")), &base); err != nil {
		t.Fatal(err)
	}
	shop := base["shop"].(map[string]any)
	lineID := base["lines"].([]any)[0].(map[string]any)["sale_item_id"].(string)

	withProduct := func(pid, sku, name, rootID, rootEN string) func(map[string]any) {
		return func(m map[string]any) {
			line := m["lines"].([]any)[0].(map[string]any)
			line["product_id"], line["sku"], line["product_name"] = pid, sku, name
			line["classifications"] = map[string]any{"roots": []any{map[string]any{
				"category_id": rootID, "name_ar": "ف", "name_en": rootEN}}, "subcategories": []any{}}
		}
	}
	// Sale A: fully returned (net 0). Sale B: untouched (net 200000).
	// IDs chosen so frozen units-tie order would rank A first.
	projectSale(t, env, "aaaaaaaa-1111-4111-8111-111111111111", "2026-09-20T10:00:00Z",
		withProduct("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "RANK-A", "Rank A", "00000000-0000-0000-0000-0000000000a1", "Root A"))
	projectSale(t, env, "bbbbbbbb-2222-4222-8222-222222222222", "2026-09-20T11:00:00Z",
		withProduct("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "RANK-B", "Rank B", "00000000-0000-0000-0000-0000000000b2", "Root B"))
	projectReturnSync(t, env,
		"aaaaaaaa-1111-4111-8111-111111111111", "MLR-A", "EGP", nil, shop,
		lineID, strPtrOf("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"), 2,
		200000, 0, 0, 200000, int64Ptr(800),
		"2026-09-20T12:00:00Z", "return", "other", "RANKA")

	prod := reportBreakdown(t, env, "custom", "2026-09-20", "2026-09-20", "EGP", "product")
	if len(prod.Rows) != 2 {
		t.Fatalf("two product rows: %+v", prod.Rows)
	}
	if *prod.Rows[0].SKU != "RANK-B" || *prod.Rows[1].SKU != "RANK-A" {
		t.Fatalf("net order RANK-B then RANK-A: %+v", prod.Rows)
	}
	roots := reportBreakdown(t, env, "custom", "2026-09-20", "2026-09-20", "EGP", "root_category")
	if len(roots.Rows) != 2 {
		t.Fatalf("two root rows: %+v", roots.Rows)
	}
	if *roots.Rows[0].NameEN != "Root B" || *roots.Rows[1].NameEN != "Root A" {
		t.Fatalf("net order Root B then Root A: %+v", roots.Rows)
	}

	// Negative net window: only A's return lands on 09-21... returns are on
	// 09-20, so use a fresh env: sale 09-18, return 09-20, query 09-20.
	env2 := openSaleEnv(t)
	projectSale(t, env2, "cccccccc-cccc-4ccc-8ccc-cccccccccccc", "2026-09-18T10:00:00Z",
		withProduct("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "RANK-A", "Rank A", "00000000-0000-0000-0000-0000000000a1", "Root A"))
	projectSale(t, env2, "dddddddd-dddd-4ddd-8ddd-dddddddddddd", "2026-09-20T10:00:00Z",
		withProduct("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "RANK-B", "Rank B", "00000000-0000-0000-0000-0000000000b2", "Root B"))
	projectReturnSync(t, env2,
		"cccccccc-cccc-4ccc-8ccc-cccccccccccc", "MLR-A", "EGP", nil, shop,
		lineID, strPtrOf("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"), 2,
		200000, 0, 0, 200000, int64Ptr(800),
		"2026-09-20T12:00:00Z", "return", "other", "RANKANEG")
	neg := reportBreakdown(t, env2, "custom", "2026-09-20", "2026-09-20", "EGP", "product")
	if len(neg.Rows) != 2 {
		t.Fatalf("two product rows: %+v", neg.Rows)
	}
	if *neg.Rows[0].SKU != "RANK-B" || *neg.Rows[1].SKU != "RANK-A" {
		t.Fatalf("positive net before negative net: %+v", neg.Rows)
	}
	for _, b := range neg.Rows[1].LineSales {
		if b.Currency == "EGP" && (b.LineSalesMinor-b.LineRefundMinor) != -200000 {
			t.Fatalf("negative net unclamped: %+v", b)
		}
	}
}

// TestReportUSDNetRanking proves the USD native scope ranks by net through
// the same currency-parameterized path (per-event historical FX preserved).
func TestReportUSDNetRanking(t *testing.T) {
	env := openSaleEnv(t)
	ingestUSD := func(saleID, pid, sku, name string) {
		t.Helper()
		var m map[string]any
		if err := json.Unmarshal([]byte(fixture(t, "sale_usd.json")), &m); err != nil {
			t.Fatal(err)
		}
		m["sale_id"] = saleID
		line := m["lines"].([]any)[0].(map[string]any)
		line["product_id"], line["sku"], line["product_name"] = pid, sku, name
		payload, _ := json.Marshal(m)
		saleEventSeq++
		eventID := fmt.Sprintf("aaaaaaaa-aaaa-7aaa-8aaa-%012d", saleEventSeq)
		body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.finalized.v1","occurred_at":"2026-09-20T10:00:00Z","payload":%s}]}`,
			eventID, payload)
		if _, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body)); err != nil {
			t.Fatalf("ingest usd: %v", err)
		}
		store := NewDevices(env.pool, 5*time.Second)
		rec, ok, err := store.LoadSaleEvent(context.Background(), eventID)
		if err != nil || !ok {
			t.Fatalf("load usd: %v %v", ok, err)
		}
		if _, err := store.ProjectSale(context.Background(), rec, time.Now()); err != nil {
			t.Fatalf("project usd: %v", err)
		}
	}
	fx := map[string]any{"base": "USD", "quote": "EGP", "rate": "52.000000", "rate_microrate": 52000000}
	shopUSD := map[string]any{"name_ar": "م", "name_en": "S", "address_ar": "A", "address_en": "A", "phone": "P", "receipt_footer_ar": "F", "receipt_footer_en": "F"}
	ingestUSD("aaaaaaaa-1111-4111-8111-111111111111", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "URANK-A", "URank A")
	ingestUSD("bbbbbbbb-2222-4222-8222-222222222222", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "URANK-B", "URank B")
	// Fixture USD line: id 33333333-..., refundable 1250. Fully return A.
	projectReturnSync(t, env,
		"aaaaaaaa-1111-4111-8111-111111111111", "MLR-UA", "USD", fx, shopUSD,
		"33333333-3333-4333-8333-333333333333", strPtrOf("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"), 1,
		1300, 100, 50, 1250, int64Ptr(400),
		"2026-09-20T12:00:00Z", "return", "other", "URANKA")

	prod := reportBreakdown(t, env, "custom", "2026-09-20", "2026-09-20", "USD", "product")
	if len(prod.Rows) != 2 {
		t.Fatalf("two product rows: %+v", prod.Rows)
	}
	if *prod.Rows[0].SKU != "URANK-B" || *prod.Rows[1].SKU != "URANK-A" {
		t.Fatalf("USD net order URANK-B then URANK-A: %+v", prod.Rows)
	}
}
