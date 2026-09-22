package postgres

import (
	"context"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

// TestIntegratedAdversarialFixture builds one adversarial dataset and
// verifies every endpoint against exact expected values (§56-58):
//
//	Sale A: EGP, 2 lines (qty>1), discount+tax, split payments, old names.
//	Sale B: EGP, same root category ID, renamed snapshot.
//	Sale C: USD+FX, multiple subcategories, different cashier.
func TestIntegratedAdversarialFixture(t *testing.T) {
	env := openSaleEnv(t)
	svc := repService(env)
	ctx := context.Background()

	// ---- Sale A -------------------------------------------------------
	projectSale(t, env, "aaaaaaaa-1111-4111-8111-111111111111", "2026-09-20T10:00:00Z",
		func(m map[string]any) {
			m["actor"] = map[string]any{"cashier_id": "cashier-1", "cashier_name": "Amal"}
			l1 := m["lines"].([]any)[0].(map[string]any)
			l1["sale_item_id"] = "aaaaaaaa-2222-4222-8222-222222222222"
			l1["sku"] = "SKU-A1"
			l1["product_name"] = "Alpha"
			l1["quantity"] = 2
			l1["unit_price"].(map[string]any)["amount_minor"] = 50000
			l1["line_total"].(map[string]any)["amount_minor"] = 100000
			l1["cost"] = map[string]any{"amount_minor": 10000, "currency": "EGP"}
			l1["classifications"] = map[string]any{
				"roots": []any{map[string]any{
					"category_id": "00000000-0000-0000-0000-000000000101",
					"name_ar":     "قديم", "name_en": "Old Root"}},
				"subcategories": []any{
					map[string]any{"category_id": "00000000-0000-0000-0000-000000000301",
						"name_ar": "قطط", "name_en": "Cats"},
					map[string]any{"category_id": "00000000-0000-0000-0000-000000000302",
						"name_ar": "كلاب", "name_en": "Dogs"},
				},
			}
			l2 := map[string]any{
				"sale_item_id": "aaaaaaaa-3333-4333-8333-333333333333",
				"sku":          "SKU-A2", "product_name": "Beta",
				"quantity":   1,
				"unit_price": map[string]any{"amount_minor": 60000, "currency": "EGP"},
				"cost":       map[string]any{"amount_minor": 15000, "currency": "EGP"},
				"line_total": map[string]any{"amount_minor": 60000, "currency": "EGP"},
				"classifications": map[string]any{
					"roots": []any{map[string]any{
						"category_id": "00000000-0000-0000-0000-000000000101",
						"name_ar":     "قديم", "name_en": "Old Root"}},
					"subcategories": []any{},
				},
			}
			m["lines"] = []any{l1, l2}
			tot := m["totals"].(map[string]any)
			tot["subtotal"].(map[string]any)["amount_minor"] = 160000
			tot["discount"].(map[string]any)["amount_minor"] = 10000
			tot["tax"].(map[string]any)["amount_minor"] = 8000
			tot["total"].(map[string]any)["amount_minor"] = 158000
			m["payments"] = []any{
				map[string]any{"method": "cash",
					"amount":       map[string]any{"amount_minor": 100000, "currency": "EGP"},
					"change_given": map[string]any{"amount_minor": 0, "currency": "EGP"}},
				map[string]any{"method": "card",
					"amount":       map[string]any{"amount_minor": 58000, "currency": "EGP"},
					"change_given": map[string]any{"amount_minor": 0, "currency": "EGP"}},
			}
		})

	// ---- Sale B: same root category ID, renamed snapshot ----------------
	projectSale(t, env, "bbbbbbbb-1111-4111-8111-111111111111", "2026-09-20T11:00:00Z",
		func(m map[string]any) {
			m["actor"] = map[string]any{"cashier_id": "cashier-1", "cashier_name": "Amal"}
			line := m["lines"].([]any)[0].(map[string]any)
			line["sku"] = "SKU-B1"
			line["product_name"] = "Gamma"
			line["quantity"] = 1
			line["unit_price"].(map[string]any)["amount_minor"] = 70000
			line["line_total"].(map[string]any)["amount_minor"] = 70000
			line["cost"] = map[string]any{"amount_minor": 5000, "currency": "EGP"}
			line["classifications"] = map[string]any{
				"roots": []any{map[string]any{
					"category_id": "00000000-0000-0000-0000-000000000101",
					"name_ar":     "جديد", "name_en": "New Root"}},
				"subcategories": []any{},
			}
			tot := m["totals"].(map[string]any)
			tot["subtotal"].(map[string]any)["amount_minor"] = 70000
			tot["total"].(map[string]any)["amount_minor"] = 70000
			m["payments"].([]any)[0].(map[string]any)["amount"].(map[string]any)["amount_minor"] = 70000
		})

	// ---- Sale C: USD + FX, multi-subcategory, other cashier -------------
	projectSale(t, env, "cccccccc-1111-4111-8111-111111111111", "2026-09-20T12:00:00Z",
		func(m map[string]any) {
			m["currency"] = "USD"
			for _, l := range m["lines"].([]any) {
				line := l.(map[string]any)
				line["unit_price"].(map[string]any)["currency"] = "USD"
				line["line_total"].(map[string]any)["currency"] = "USD"
				if c, ok := line["cost"].(map[string]any); ok {
					c["currency"] = "USD"
				}
			}
			for _, k := range []string{"subtotal", "discount", "tax", "total"} {
				m["totals"].(map[string]any)[k].(map[string]any)["currency"] = "USD"
			}
			for _, p := range m["payments"].([]any) {
				pay := p.(map[string]any)
				pay["amount"].(map[string]any)["currency"] = "USD"
				pay["change_given"].(map[string]any)["currency"] = "USD"
			}
			m["actor"] = map[string]any{"cashier_id": "cashier-9", "cashier_name": "Zaynab"}
			m["fx"] = map[string]any{"base": "USD", "quote": "EGP",
				"rate": "52.000000", "rate_microrate": 52000000}
			line := m["lines"].([]any)[0].(map[string]any)
			line["sku"] = "SKU-C1"
			subs := line["classifications"].(map[string]any)["subcategories"].([]any)
			subs = append(subs, map[string]any{
				"category_id": "00000000-0000-0000-0000-000000000303",
				"name_ar":     "طيور", "name_en": "Birds"})
			line["classifications"].(map[string]any)["subcategories"] = subs
		})

	req, _ := svc.ParseRequest("custom", "2026-09-20", "2026-09-20", "")

	// ---- summary -------------------------------------------------------
	sum, err := svc.Summary(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if sum.TransactionCount != 3 || sum.UnitsSold != 6 {
		t.Fatalf("counts: txns=%d units=%d", sum.TransactionCount, sum.UnitsSold)
	}
	byCur := map[string]report.CurrencyTotal{}
	for _, b := range sum.CurrencyTotals {
		byCur[b.Currency] = b
	}
	egp := byCur["EGP"]
	if egp.SubtotalMinor != 230000 || egp.DiscountMinor != 10000 || egp.TaxMinor != 8000 ||
		egp.SalesTotalMinor != 228000 || egp.LineCostMinor != 40000 {
		t.Fatalf("EGP bucket: %+v", egp)
	}
	usd := byCur["USD"]
	if usd.SubtotalMinor != 200000 || usd.DiscountMinor != 0 || usd.TaxMinor != 0 ||
		usd.SalesTotalMinor != 200000 || usd.LineCostMinor != 0 {
		t.Fatalf("USD bucket: %+v", usd)
	}
	pays := map[string]map[string]int64{}
	for _, p := range sum.PaymentTotals {
		if pays[p.Method] == nil {
			pays[p.Method] = map[string]int64{}
		}
		pays[p.Method][p.Currency] = p.AmountMinor
	}
	if pays["cash"]["EGP"] != 170000 || pays["card"]["EGP"] != 58000 ||
		pays["cash"]["USD"] != 200000 {
		t.Fatalf("payments (no fanout multiplication): %+v", sum.PaymentTotals)
	}
	if _, hasCard := pays["card"]["USD"]; hasCard {
		t.Fatalf("no USD card payment exists: %+v", sum.PaymentTotals)
	}

	// ---- daily ---------------------------------------------------------
	daily, err := svc.Daily(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(daily.Days) != 1 || daily.Days[0].Transactions != 3 || daily.Days[0].Units != 6 {
		t.Fatalf("daily: %+v", daily.Days)
	}

	// ---- product (no header multiplication across lines/payments) ------
	prod, err := svc.Breakdown(ctx, req, report.DimensionProduct)
	if err != nil {
		t.Fatal(err)
	}
	lines := map[string][2]int64{} // sku -> [sales, cost]
	for _, r := range prod.Rows {
		lines[*r.SKU] = [2]int64{r.LineSales[0].LineSalesMinor, r.LineSales[0].LineCostMinor}
	}
	if lines["SKU-A1"] != [2]int64{100000, 20000} || lines["SKU-A2"] != [2]int64{60000, 15000} ||
		lines["SKU-B1"] != [2]int64{70000, 5000} || lines["SKU-C1"] != [2]int64{200000, 0} {
		t.Fatalf("product lines: %v", lines)
	}

	// ---- root categories: Old vs New snapshots separate, additive ------
	roots, err := svc.Breakdown(ctx, req, report.DimensionRootCategory)
	if err != nil {
		t.Fatal(err)
	}
	rv := map[string][3]int64{}
	for _, r := range roots.Rows {
		rv[*r.NameEN] = [3]int64{r.Units, r.LineSales[0].LineSalesMinor, r.LineSales[0].LineCostMinor}
	}
	if rv["Old Root"] != [3]int64{3, 160000, 35000} || rv["New Root"] != [3]int64{1, 70000, 5000} {
		t.Fatalf("roots: %v", rv)
	}

	// ---- subcategories: facet rows carry full lines --------------------
	subs, err := svc.Breakdown(ctx, req, report.DimensionSubcategory)
	if err != nil {
		t.Fatal(err)
	}
	sv := map[string][2]int64{}
	for _, r := range subs.Rows {
		sv[*r.NameEN] = [2]int64{r.Units, r.LineSales[0].LineSalesMinor}
	}
	if sv["Cats"] != [2]int64{2, 100000} || sv["Dogs"] != [2]int64{2, 100000} ||
		sv["Birds"] != [2]int64{2, 200000} {
		t.Fatalf("subcategories: %v", sv)
	}

	// ---- cashiers: exact header buckets --------------------------------
	cash, err := svc.Breakdown(ctx, req, report.DimensionCashier)
	if err != nil {
		t.Fatal(err)
	}
	cv := map[string]report.SaleCurrencyTotal{}
	var amalTxns int64
	for _, r := range cash.Rows {
		if r.CashierName != nil && *r.CashierName == "Amal" {
			amalTxns = r.Transactions
			for _, b := range r.CurrencyTotals {
				cv[b.Currency] = b
			}
		}
		if r.LineSales != nil {
			t.Fatalf("cashier rows must not carry line_sales: %+v", r)
		}
	}
	if amalTxns != 2 || cv["EGP"].SalesTotalMinor != 228000 || cv["EGP"].SubtotalMinor != 230000 {
		t.Fatalf("Amal buckets: txns=%d %+v", amalTxns, cv)
	}

	// ---- channel -------------------------------------------------------
	ch, err := svc.Breakdown(ctx, req, report.DimensionChannel)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Rows) != 1 || *ch.Rows[0].Channel != "STORE" || ch.Rows[0].Transactions != 3 {
		t.Fatalf("channel: %+v", ch.Rows)
	}
	if ch.Rows[0].CurrencyTotals[1].Currency != "USD" {
		t.Fatalf("channel buckets ordered: %+v", ch.Rows[0].CurrencyTotals)
	}
}
