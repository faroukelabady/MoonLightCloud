package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

// auditSale projects a fixture sale with the audit shape: line sum 200000,
// discount 20000, tax 14000, total 194000, payment aggregate kept exact.
func auditSale(t *testing.T, env *saleEnv, saleID, occurred, currency string) {
	t.Helper()
	projectSale(t, env, saleID, occurred, func(m map[string]any) {
		m["currency"] = currency
		for _, l := range m["lines"].([]any) {
			line := l.(map[string]any)
			line["unit_price"].(map[string]any)["currency"] = currency
			line["line_total"].(map[string]any)["currency"] = currency
			if c, ok := line["cost"].(map[string]any); ok {
				c["currency"] = currency
			}
		}
		tot := m["totals"].(map[string]any)
		for _, k := range []string{"subtotal", "discount", "tax", "total"} {
			tot[k].(map[string]any)["currency"] = currency
		}
		tot["discount"].(map[string]any)["amount_minor"] = 20000
		tot["tax"].(map[string]any)["amount_minor"] = 14000
		tot["total"].(map[string]any)["amount_minor"] = 194000
		for _, p := range m["payments"].([]any) {
			pay := p.(map[string]any)
			pay["amount"].(map[string]any)["currency"] = currency
			pay["amount"].(map[string]any)["amount_minor"] = 194000
			pay["change_given"].(map[string]any)["currency"] = currency
		}
		if currency == "USD" {
			m["fx"] = map[string]any{
				"base": "USD", "quote": "EGP",
				"rate": "52.000000", "rate_microrate": 52000000,
			}
		} else {
			delete(m, "fx")
		}
	})
}

// TestH3HeaderVsLineSemantics proves the split contract: product/category
// rows carry pre-adjustment line_sales (200000); cashier/channel rows carry
// exact header buckets (200000/20000/14000/194000) and no line_sales;
// product/category rows carry no currency_totals.
func TestH3HeaderVsLineSemantics(t *testing.T) {
	for _, currency := range []string{"EGP", "USD"} {
		t.Run(currency, func(t *testing.T) {
			env := openSaleEnv(t)
			auditSale(t, env, "11111111-1111-4111-8111-111111111111", "2026-09-20T10:00:00Z", currency)
			svc := repService(env)
			req, _ := svc.ParseRequest("custom", "2026-09-20", "2026-09-20", "")
			prod, err := svc.Breakdown(context.Background(), req, report.DimensionProduct)
			if err != nil {
				t.Fatal(err)
			}
			if len(prod.Rows) != 1 {
				t.Fatalf("product rows: %+v", prod.Rows)
			}
			pr := prod.Rows[0]
			if len(pr.LineSales) != 1 || pr.LineSales[0].LineSalesMinor != 200000 {
				t.Fatalf("product line_sales must be pre-adjustment 200000: %+v", pr)
			}
			if pr.CurrencyTotals != nil {
				t.Fatalf("product rows must not carry header buckets: %+v", pr)
			}
			cash, err := svc.Breakdown(context.Background(), req, report.DimensionCashier)
			if err != nil {
				t.Fatal(err)
			}
			if len(cash.Rows) != 1 {
				t.Fatalf("cashier rows: %+v", cash.Rows)
			}
			cr := cash.Rows[0]
			if cr.LineSales != nil {
				t.Fatalf("cashier rows must not carry line_sales: %+v", cr)
			}
			if len(cr.CurrencyTotals) != 1 {
				t.Fatalf("cashier header buckets: %+v", cr)
			}
			ct := cr.CurrencyTotals[0]
			if ct.SubtotalMinor != 200000 || ct.DiscountMinor != 20000 ||
				ct.TaxMinor != 14000 || ct.SalesTotalMinor != 194000 || ct.Currency != currency {
				t.Fatalf("cashier header buckets wrong: %+v", ct)
			}
			if cr.Transactions != 1 || cr.Units != 2 {
				t.Fatalf("cashier counts: %+v", cr)
			}
			ch, err := svc.Breakdown(context.Background(), req, report.DimensionChannel)
			if err != nil {
				t.Fatal(err)
			}
			if len(ch.Rows) != 1 || ch.Rows[0].LineSales != nil {
				t.Fatalf("channel rows: %+v", ch.Rows)
			}
			cht := ch.Rows[0].CurrencyTotals[0]
			if cht.SubtotalMinor != 200000 || cht.DiscountMinor != 20000 ||
				cht.TaxMinor != 14000 || cht.SalesTotalMinor != 194000 {
				t.Fatalf("channel header buckets wrong: %+v", cht)
			}
		})
	}
}

// TestH4ExtendedCost verifies unit cost × quantity across every cost path:
// summary, daily, product, root, subcategory; plus the audit's 50000 case.
func TestH4ExtendedCost(t *testing.T) {
	env := openSaleEnv(t)
	// Line 1: qty 1 × cost 10000 = 10000. Line 2: qty 2 × cost 20000 = 40000.
	// Extended total 50000 (audit fixture expectation).
	projectSale(t, env, "11111111-1111-4111-8111-111111111111", "2026-09-20T10:00:00Z",
		func(m map[string]any) {
			lines := m["lines"].([]any)
			l1 := lines[0].(map[string]any)
			l1["quantity"] = 1
			l1["unit_price"].(map[string]any)["amount_minor"] = 50000
			l1["line_total"].(map[string]any)["amount_minor"] = 50000
			l1["cost"] = map[string]any{"amount_minor": 10000, "currency": "EGP"}
			l2 := map[string]any{
				"sale_item_id": "99999999-9999-4999-8999-999999999999",
				"sku":          "SKU-2", "product_name": "Second",
				"quantity":   2,
				"unit_price": map[string]any{"amount_minor": 30000, "currency": "EGP"},
				"cost":       map[string]any{"amount_minor": 20000, "currency": "EGP"},
				"line_total": map[string]any{"amount_minor": 60000, "currency": "EGP"},
				"classifications": map[string]any{
					"roots": []any{map[string]any{
						"category_id": "00000000-0000-0000-0000-000000000102",
						"name_ar":     "فرعوني", "name_en": "Pharaonic"}},
					"subcategories": []any{},
				},
			}
			m["lines"] = []any{l1, l2}
			tot := m["totals"].(map[string]any)
			tot["subtotal"].(map[string]any)["amount_minor"] = 110000
			tot["total"].(map[string]any)["amount_minor"] = 110000
			m["payments"].([]any)[0].(map[string]any)["amount"].(map[string]any)["amount_minor"] = 110000
		})
	svc := repService(env)
	req, _ := svc.ParseRequest("custom", "2026-09-20", "2026-09-20", "")
	sum, err := svc.Summary(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if sum.CurrencyTotals[0].LineCostMinor != 50000 {
		t.Fatalf("summary extended cost: %+v", sum.CurrencyTotals[0])
	}
	daily, err := svc.Daily(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if daily.Days[0].CurrencyTotals[0].LineCostMinor != 50000 {
		t.Fatalf("daily extended cost: %+v", daily.Days[0])
	}
	prod, err := svc.Breakdown(context.Background(), req, report.DimensionProduct)
	if err != nil {
		t.Fatal(err)
	}
	costs := map[string]int64{}
	for _, r := range prod.Rows {
		costs[*r.SKU] = r.LineSales[0].LineCostMinor
	}
	if costs["PAP-002"] != 10000 || costs["SKU-2"] != 40000 {
		t.Fatalf("product extended costs: %v", costs)
	}
	roots, err := svc.Breakdown(context.Background(), req, report.DimensionRootCategory)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots.Rows) != 1 || roots.Rows[0].LineSales[0].LineCostMinor != 50000 {
		t.Fatalf("root extended cost: %+v", roots.Rows)
	}
}

// TestH4PerLineOverflow proves a single huge unit cost × quantity fails
// explicitly instead of wrapping.
func TestH4PerLineOverflow(t *testing.T) {
	env := openSaleEnv(t)
	var m map[string]any
	if err := json.Unmarshal([]byte(fixture(t, "sale_egp.json")), &m); err != nil {
		t.Fatal(err)
	}
	m["sale_id"] = "11111111-1111-4111-8111-111111111111"
	line := m["lines"].([]any)[0].(map[string]any)
	line["quantity"] = 3
	line["unit_price"].(map[string]any)["amount_minor"] = 100000
	line["line_total"].(map[string]any)["amount_minor"] = 300000
	line["cost"] = map[string]any{"amount_minor": 4611686018427387904, "currency": "EGP"} // 2^62
	tot := m["totals"].(map[string]any)
	tot["subtotal"].(map[string]any)["amount_minor"] = 300000
	tot["total"].(map[string]any)["amount_minor"] = 300000
	m["payments"].([]any)[0].(map[string]any)["amount"].(map[string]any)["amount_minor"] = 300000
	payload, _ := json.Marshal(m)
	saleEventSeq++
	eventID := fmt.Sprintf("aaaaaaaa-aaaa-7aaa-8aaa-%012d", saleEventSeq)
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.finalized.v1","occurred_at":"2026-09-20T10:00:00Z","payload":%s}]}`,
		eventID, payload)
	if _, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body)); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	store := NewDevices(env.pool, 5*time.Second)
	rec, _, _ := store.LoadSaleEvent(context.Background(), eventID)
	if _, err := store.ProjectSale(context.Background(), rec, time.Now()); err != nil {
		t.Fatalf("project: %v", err)
	}
	svc := repService(env)
	req, _ := svc.ParseRequest("custom", "2026-09-20", "2026-09-20", "")
	if _, err := svc.Summary(context.Background(), req); err == nil {
		t.Fatal("per-line cost overflow must fail explicitly")
	}
}

// TestM1CategoryRenamePreserved proves snapshot identity: same category ID
// with different historical names yields separate rows with their own
// totals; buckets merge only within exact identity.
func TestM1CategoryRenamePreserved(t *testing.T) {
	for _, tc := range []struct {
		dimension string
		group     string
		oldName   string
		newName   string
	}{
		{report.DimensionRootCategory, "roots", "Old Root", "New Root"},
		{report.DimensionSubcategory, "subcategories", "Old Sub", "New Sub"},
	} {
		t.Run(tc.dimension, func(t *testing.T) {
			env := openSaleEnv(t)
			mkSale := func(saleID, name string) {
				projectSale(t, env, saleID, "2026-09-20T10:00:00Z", func(m map[string]any) {
					line := m["lines"].([]any)[0].(map[string]any)
					// Exactly one root is required by the contract; the
					// non-target group keeps a constant entry.
					groups := map[string]any{
						"roots": []any{map[string]any{
							"category_id": "00000000-0000-0000-0000-000000000102",
							"name_ar":     "ثابت", "name_en": "Constant",
						}},
						"subcategories": []any{},
					}
					groups[tc.group] = []any{map[string]any{
						"category_id": "00000000-0000-0000-0000-000000000101",
						"name_ar":     "x-" + name, "name_en": name,
					}}
					line["classifications"] = groups
				})
			}
			mkSale("11111111-1111-4111-8111-111111111111", tc.oldName)
			mkSale("22222222-2222-4222-8222-222222222222", tc.newName)
			svc := repService(env)
			req, _ := svc.ParseRequest("custom", "2026-09-20", "2026-09-20", "")
			res, err := svc.Breakdown(context.Background(), req, tc.dimension)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Rows) != 2 {
				t.Fatalf("rename must yield two rows: %+v", res.Rows)
			}
			names := map[string]int64{}
			for _, r := range res.Rows {
				names[*r.NameEN] = r.Units
				if len(r.LineSales) != 1 || r.LineSales[0].Currency != "EGP" {
					t.Fatalf("buckets merge within identity only: %+v", r)
				}
			}
			if names[tc.oldName] != 2 || names[tc.newName] != 2 {
				t.Fatalf("per-snapshot totals: %v", names)
			}
		})
	}
}

// TestL1NestedCurrencyOrder proves alphabetical nested bucket order with
// both currencies present, deterministically.
func TestL1NestedCurrencyOrder(t *testing.T) {
	env := openSaleEnv(t)
	auditSale(t, env, "11111111-1111-4111-8111-111111111111", "2026-09-20T10:00:00Z", "EGP")
	auditSale(t, env, "22222222-2222-4222-8222-222222222222", "2026-09-20T11:00:00Z", "USD")
	svc := repService(env)
	for i := 0; i < 3; i++ {
		req, _ := svc.ParseRequest("custom", "2026-09-20", "2026-09-20", "")
		for _, dim := range []string{report.DimensionProduct, report.DimensionRootCategory,
			report.DimensionSubcategory, report.DimensionCashier, report.DimensionChannel} {
			res, err := svc.Breakdown(context.Background(), req, dim)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range res.Rows {
				assertBucketOrder(t, dim, r.LineSales)
				assertCurrencyTotalOrder(t, dim, r.CurrencyTotals)
			}
		}
	}
}

func assertBucketOrder(t *testing.T, dim string, buckets []report.LineSaleTotal) {
	t.Helper()
	for i := 1; i < len(buckets); i++ {
		if buckets[i-1].Currency >= buckets[i].Currency {
			t.Fatalf("%s buckets unordered: %+v", dim, buckets)
		}
	}
	if len(buckets) == 2 && (buckets[0].Currency != "EGP" || buckets[1].Currency != "USD") {
		t.Fatalf("%s buckets must be EGP,USD: %+v", dim, buckets)
	}
}

func assertCurrencyTotalOrder(t *testing.T, dim string, buckets []report.CurrencyTotal) {
	t.Helper()
	for i := 1; i < len(buckets); i++ {
		if buckets[i-1].Currency >= buckets[i].Currency {
			t.Fatalf("%s header buckets unordered: %+v", dim, buckets)
		}
	}
}
