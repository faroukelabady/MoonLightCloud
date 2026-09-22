package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

func TestReportDSTDayGrouping(t *testing.T) {
	env := openSaleEnv(t)
	loc := cairoLoc(t)
	// Discover the real spring transition, then place sales on the days
	// around it: one per local date. Grouping must follow local dates
	// despite the 23h UTC day.
	spring := findSpringTransition(t, loc)
	dates := []string{
		spring.AddDate(0, 0, -1).Format("2006-01-02"),
		spring.Format("2006-01-02"),
		spring.AddDate(0, 0, 1).Format("2006-01-02"),
	}
	for i, d := range dates {
		// Noon local always exists and is unambiguous.
		noon, _ := time.ParseInLocation("2006-01-02 15:04", d+" 12:00", loc)
		projectSale(t, env,
			fmt.Sprintf("1111111%d-1111-4111-8111-11111111111%d", i, i),
			noon.UTC().Format(time.RFC3339), nil)
	}
	svc := repService(env)
	req, err := svc.ParseRequest("custom", dates[0], dates[2], "")
	if err != nil {
		t.Fatal(err)
	}
	daily, err := svc.Daily(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(daily.Days) != 3 {
		t.Fatalf("three local dates, got %+v", daily.Days)
	}
	for i, d := range dates {
		if daily.Days[i].Date != d || daily.Days[i].Transactions != 1 {
			t.Fatalf("day %d: %+v", i, daily.Days[i])
		}
	}
}

func findSpringTransition(t *testing.T, loc *time.Location) time.Time {
	t.Helper()
	prev := time.Date(2026, 1, 1, 12, 0, 0, 0, loc)
	_, prevOff := prev.Zone()
	for m := time.January; m <= time.December; m++ {
		for d := 1; d <= 31; d++ {
			cur := time.Date(2026, m, d, 12, 0, 0, 0, loc)
			if cur.Month() != m {
				break
			}
			_, off := cur.Zone()
			if off > prevOff {
				return time.Date(2026, m, d, 0, 0, 0, 0, loc)
			}
			prevOff = off
		}
	}
	t.Fatal("no spring transition in 2026")
	return time.Time{}
}

func TestReportMultiSubcategoryFacet(t *testing.T) {
	env := openSaleEnv(t)
	// EGP fixture line has no subcategories; attach two.
	projectSale(t, env, "11111111-1111-4111-8111-111111111111", "2026-09-20T10:00:00Z",
		func(m map[string]any) {
			line := m["lines"].([]any)[0].(map[string]any)
			line["classifications"].(map[string]any)["subcategories"] = []any{
				map[string]any{
					"category_id": "00000000-0000-0000-0000-000000000301",
					"name_ar":     "قطط", "name_en": "Cats",
				},
				map[string]any{
					"category_id": "00000000-0000-0000-0000-000000000302",
					"name_ar":     "كلاب", "name_en": "Dogs",
				},
			}
		})
	svc := repService(env)
	req, _ := svc.ParseRequest("custom", "2026-09-20", "2026-09-20", "")
	roots, err := svc.Breakdown(context.Background(), req, report.DimensionRootCategory)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots.Rows) != 1 || roots.Rows[0].Units != 2 {
		t.Fatalf("root counted once: %+v", roots.Rows)
	}
	subs, err := svc.Breakdown(context.Background(), req, report.DimensionSubcategory)
	if err != nil {
		t.Fatal(err)
	}
	if len(subs.Rows) != 2 {
		t.Fatalf("two subcategory rows: %+v", subs.Rows)
	}
	var facetUnits int64
	for _, r := range subs.Rows {
		if r.Units != 2 {
			t.Fatalf("each subcategory carries the full line: %+v", r)
		}
		facetUnits += r.Units
	}
	if facetUnits != 4 {
		t.Fatalf("facet sum exceeds line total by design: %d", facetUnits)
	}
}

func TestReportHeaderVsLineAdjustments(t *testing.T) {
	env := openSaleEnv(t)
	// Sale-level discount + tax: header totals exact, lines untouched.
	projectSale(t, env, "11111111-1111-4111-8111-111111111111", "2026-09-20T10:00:00Z",
		func(m map[string]any) {
			tot := m["totals"].(map[string]any)
			tot["discount"].(map[string]any)["amount_minor"] = 20000
			tot["tax"].(map[string]any)["amount_minor"] = 14000
			tot["total"].(map[string]any)["amount_minor"] = 194000 // 200000+14000-20000
			// Keep the payment aggregate invariant (sum == total ±1).
			m["payments"].([]any)[0].(map[string]any)["amount"].(map[string]any)["amount_minor"] = 194000
		})
	sum := reportSummary(t, env, "custom", "2026-09-20", "2026-09-20", "")
	egp := sum.CurrencyTotals[0]
	if egp.SubtotalMinor != 200000 || egp.DiscountMinor != 20000 ||
		egp.TaxMinor != 14000 || egp.SalesTotalMinor != 194000 {
		t.Fatalf("header totals: %+v", egp)
	}
	svc := repService(env)
	req, _ := svc.ParseRequest("custom", "2026-09-20", "2026-09-20", "")
	prod, err := svc.Breakdown(context.Background(), req, report.DimensionProduct)
	if err != nil {
		t.Fatal(err)
	}
	if len(prod.Rows) != 1 || prod.Rows[0].LineSales[0].LineSalesMinor != 200000 {
		t.Fatalf("line metrics must not allocate header discount/tax: %+v", prod.Rows)
	}
}

func TestReportChannelCashier(t *testing.T) {
	env := openSaleEnv(t)
	projectSale(t, env, "11111111-1111-4111-8111-111111111111", "2026-09-20T10:00:00Z", nil)
	// Anonymous sale: null actor snapshot.
	projectSale(t, env, "22222222-2222-4222-8222-222222222222", "2026-09-20T11:00:00Z",
		func(m map[string]any) {
			m["actor"] = map[string]any{}
		})
	svc := repService(env)
	req, _ := svc.ParseRequest("custom", "2026-09-20", "2026-09-20", "")
	ch, err := svc.Breakdown(context.Background(), req, report.DimensionChannel)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Rows) != 1 || *ch.Rows[0].Channel != "STORE" || ch.Rows[0].Transactions != 2 {
		t.Fatalf("channel: %+v", ch.Rows)
	}
	cash, err := svc.Breakdown(context.Background(), req, report.DimensionCashier)
	if err != nil {
		t.Fatal(err)
	}
	if len(cash.Rows) != 2 {
		t.Fatalf("attributed + null bucket: %+v", cash.Rows)
	}
	var nulls, named int
	for _, r := range cash.Rows {
		if r.CashierID == nil {
			nulls++
			if r.CashierName != nil {
				t.Fatalf("null bucket must stay null: %+v", r)
			}
		} else {
			named++
		}
	}
	if nulls != 1 || named != 1 {
		t.Fatalf("buckets: %+v", cash.Rows)
	}
	// Ordering: units desc; tie (both 2 units? no: 2 vs 2? fixture qty 2 each) → id tiebreak.
	_ = named
}

func TestReportHistoricalSnapshots(t *testing.T) {
	env := openSaleEnv(t)
	projectSale(t, env, "11111111-1111-4111-8111-111111111111", "2026-09-20T10:00:00Z",
		func(m map[string]any) {
			line := m["lines"].([]any)[0].(map[string]any)
			line["product_name"] = "Old Historical Name"
			line["sku"] = "OLD-SKU-1"
		})
	svc := repService(env)
	req, _ := svc.ParseRequest("custom", "2026-09-20", "2026-09-20", "")
	prod, err := svc.Breakdown(context.Background(), req, report.DimensionProduct)
	if err != nil {
		t.Fatal(err)
	}
	if *prod.Rows[0].ProductName != "Old Historical Name" || *prod.Rows[0].SKU != "OLD-SKU-1" {
		t.Fatalf("snapshots must come from the event: %+v", prod.Rows[0])
	}
	// Product rename semantics: same product_id, different snapshot tuple =
	// separate historical rows (no cosmetic merging).
	projectSale(t, env, "22222222-2222-4222-8222-222222222222", "2026-09-20T11:00:00Z",
		func(m map[string]any) {
			line := m["lines"].([]any)[0].(map[string]any)
			line["product_name"] = "New Historical Name"
		})
	prod, err = svc.Breakdown(context.Background(), req, report.DimensionProduct)
	if err != nil {
		t.Fatal(err)
	}
	if len(prod.Rows) != 2 {
		t.Fatalf("renamed snapshots stay separate rows: %+v", prod.Rows)
	}
	// Null product_id (desktop-optional) groups under an explicit null id.
	projectSale(t, env, "33333333-3333-4333-8333-333333333333", "2026-09-20T12:00:00Z",
		func(m map[string]any) {
			line := m["lines"].([]any)[0].(map[string]any)
			delete(line, "product_id")
		})
	prod, err = svc.Breakdown(context.Background(), req, report.DimensionProduct)
	if err != nil {
		t.Fatal(err)
	}
	var nulls int
	for _, r := range prod.Rows {
		if r.ProductID == nil {
			nulls++
		}
	}
	if nulls != 1 {
		t.Fatalf("null product_id must form its own row: %+v", prod.Rows)
	}
}

func TestReportRebuildInvariance(t *testing.T) {
	env := openSaleEnv(t)
	projectSale(t, env, "11111111-1111-4111-8111-111111111111", "2026-09-20T10:00:00Z", nil)
	before := reportSummary(t, env, "custom", "2026-09-20", "2026-09-20", "")
	beforeJSON, _ := json.Marshal(struct {
		Txns  int64
		Units int64
		Bucks []report.CurrencyTotal
		Pays  []report.PaymentTotal
	}{before.TransactionCount, before.UnitsSold, before.CurrencyTotals, before.PaymentTotals})

	// Delete derived projections (retain events + ownership), reset
	// processing, re-project through the real projector path.
	ctx := context.Background()
	if _, err := env.pool.Exec(ctx, `DELETE FROM sales_projection`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx,
		`UPDATE sync_event_processing SET status='pending', next_attempt_at=now()
		  WHERE processor='sale_projection.v1'`); err != nil {
		t.Fatal(err)
	}
	store := NewDevices(env.pool, 5*time.Second)
	ids, err := store.PendingSaleEvents(ctx, "sale_projection.v1", 25)
	if err != nil || len(ids) != 1 {
		t.Fatalf("rediscovery: %v %v", ids, err)
	}
	rec, _, _ := store.LoadSaleEvent(ctx, ids[0])
	if _, err := store.ProjectSale(ctx, rec, time.Now()); err != nil {
		t.Fatalf("re-project: %v", err)
	}
	after := reportSummary(t, env, "custom", "2026-09-20", "2026-09-20", "")
	afterJSON, _ := json.Marshal(struct {
		Txns  int64
		Units int64
		Bucks []report.CurrencyTotal
		Pays  []report.PaymentTotal
	}{after.TransactionCount, after.UnitsSold, after.CurrencyTotals, after.PaymentTotals})
	if string(beforeJSON) != string(afterJSON) {
		t.Fatalf("rebuild changed report:\n%s\n%s", beforeJSON, afterJSON)
	}
}

func TestReportAggregateOverflow(t *testing.T) {
	env := openSaleEnv(t)
	// Project two real sales, then push their totals near int64 max:
	// SUM overflows → the cast fails explicitly, never wraps.
	for i, sid := range []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
	} {
		_ = i
		projectSale(t, env, sid, "2026-09-20T10:00:00Z", nil)
	}
	const huge = int64(9223372036854775000)
	if _, err := env.pool.Exec(context.Background(),
		`UPDATE sales_projection SET subtotal_minor=$1, total_minor=$1`, huge); err != nil {
		t.Fatalf("seed huge totals: %v", err)
	}
	svc := repService(env)
	req, err := svc.ParseRequest("custom", "2026-09-01", "2026-09-30", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Summary(context.Background(), req); err == nil {
		t.Fatal("overflowing aggregates must fail explicitly, never wrap")
	} else {
		// Financial overflow is an internal processing failure (500),
		// never a dependency outage (503) and never success-with-zero.
		var ae *apperr.Error
		if !errors.As(err, &ae) || ae.Kind != apperr.Internal {
			t.Fatalf("overflow must classify INTERNAL, got: %v", err)
		}
	}
}
