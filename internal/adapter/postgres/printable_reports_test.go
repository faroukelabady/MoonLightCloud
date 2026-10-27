package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
	"github.com/faroukelabady/MoonLightCloud/internal/salesreport"
	"github.com/faroukelabady/MoonLightCloud/internal/settings"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPrintableSalesReportConvertsEverySale seeds the EGP fixture (EGP
// 2,000.00, two units, no cost), the USD fixture (USD 12.50 at 52, tax
// 0.50, cost 4.00) and a one-unit EGP return of EGP 1,000.00, then reads the report
// in every display currency and Store scope.
func TestPrintableSalesReportConvertsEverySale(t *testing.T) {
	env := openDashEnv(t)
	egp := fixture(t, "sale_egp.json")
	projectDashSale(t, env, "aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa", "2026-09-20T11:00:00Z", egp)
	projectDashSale(t, env, "bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb", "2026-09-20T10:00:00Z", fixture(t, "sale_usd.json"))
	projectDashReturn(t, env, "cccccccc-cccc-7ccc-8ccc-cccccccccccc", "2026-09-20T12:00:00Z",
		dashReturnPayload(t, egp, "dddddddd-dddd-7ddd-8ddd-dddddddddddd", "RET-1", "2026-09-20T12:00:00Z", 1))

	svc := printableService(t, env.pool, true)
	req := dashReq(t, env, "custom", "2026-09-20", "2026-09-20")
	read := func(t *testing.T, req report.Request, display string) salesreport.SalesReport {
		t.Helper()
		got, err := svc.Sales(context.Background(), req, display, salesreport.Filter{}, 0, 50)
		if err != nil {
			t.Fatalf("%s: %v", display, err)
		}
		return got
	}

	for _, tc := range []struct {
		display                     string
		sales, refund, cost, profit int64
	}{
		// 200000 + 1250*52; tax 50*52; cost 400*52 (EGP lines have none).
		// Profit: 265000 - 100000 - 2600 - 20800.
		{display: salesreport.DisplayEGP, sales: 265_000, refund: 100_000, cost: 20_800, profit: 141_600},
		// The EGP Sale uses the Store's USD rate 52: 200000/52 = 3846.15 -> 3846;
		// refund 100000/52 = 1923.08 -> 1923. Profit: 5096 - 1923 - 50 - 400.
		{display: salesreport.DisplayUSD, sales: 5_096, refund: 1_923, cost: 400, profit: 2_723},
	} {
		got := read(t, req, tc.display)
		if len(got.Summary) != 1 {
			t.Fatalf("%s: summary %+v", tc.display, got.Summary)
		}
		s := got.Summary[0]
		if s.Currency != tc.display || s.SalesCount != 2 || s.ItemsSold != 3 || s.ReturnedItems != 1 || s.MissingCostItems != 2 {
			t.Fatalf("%s: counts %+v", tc.display, s)
		}
		if int64(s.Sales) != tc.sales || int64(s.Refund) != tc.refund || int64(s.Cost) != tc.cost || int64(s.Profit) != tc.profit {
			t.Fatalf("%s: money %+v", tc.display, s)
		}
		if got.Base.Sales != 265_000 {
			t.Fatalf("%s: the EGP base never depends on the display currency: %+v", tc.display, got.Base)
		}
		if got.SalesTotalCount != 2 || len(got.Products) != 2 || len(got.Categories) != 1 || len(got.Daily) != 1 {
			t.Fatalf("%s: sections %+v", tc.display, got.Sections)
		}
		if got.Shop == nil || got.Shop.NameEn == "" || got.Period.StartDate != "2026-09-20" || got.Period.EndDate != "2026-09-20" {
			t.Fatalf("%s: header %+v %+v", tc.display, got.Shop, got.Period)
		}
	}

	all := read(t, req, salesreport.DisplayAll)
	if len(all.Summary) != 2 || all.Summary[0].Sales != 200_000 || all.Summary[1].Sales != 1_250 {
		t.Fatalf("all keeps currencies apart: %+v", all.Summary)
	}

	scoped := req
	scoped.Store = &report.StoreScope{StoreID: env.storeID}
	if got := read(t, scoped, salesreport.DisplayEGP); got.Summary[0].Sales != 265_000 {
		t.Fatalf("own Store: %+v", got.Summary)
	}
	other := req
	other.Store = &report.StoreScope{StoreID: uuid.NewString()}
	if got := read(t, other, salesreport.DisplayEGP); len(got.Summary) != 0 || got.SalesTotalCount != 0 || got.Shop != nil {
		t.Fatalf("another Store sees nothing: %+v", got)
	}
}

func TestPrintableSalesReportNeedsAUSDRateForUSD(t *testing.T) {
	env := openDashEnv(t)
	projectDashSale(t, env, "aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa", "2026-09-20T11:00:00Z", fixture(t, "sale_egp.json"))
	svc := printableService(t, env.pool, true)
	req := dashReq(t, env, "custom", "2026-09-20", "2026-09-20")
	_, err := svc.Sales(context.Background(), req, salesreport.DisplayUSD, salesreport.Filter{}, 0, 50)
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Message != salesreport.ErrRateRequired {
		t.Fatalf("err = %v", err)
	}
	if _, err := svc.Sales(context.Background(), req, salesreport.DisplayEGP, salesreport.Filter{}, 0, 50); err != nil {
		t.Fatalf("EGP: %v", err)
	}
}

func TestPrintableStockValuesVariantsAtEGPCostAndPrice(t *testing.T) {
	env := openSaleEnv(t)
	f := seedProductStockFixture(t, env)
	stocked := f.variant(t, 1, true, false)
	f.inventory(t, stocked, 3, 1, true)
	f.variant(t, 2, true, false) // sellable, never counted: a zero row
	f.inventory(t, f.variant(t, 3, false, false), 0, 1, true)

	svc := printableService(t, env.pool, true)
	got, err := svc.Stock(context.Background(), env.storeID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Product cost EGP 400.00; the Variant overrides its price to 1,234.56.
	want := salesreport.StockTotals{ProductsInStock: 1, VariantsInStock: 1, Units: 3, Cost: 120_000, Retail: 370_368}
	if got.Totals != want {
		t.Fatalf("totals %+v, want %+v", got.Totals, want)
	}
	if len(got.Items) != 2 || len(got.Categories) != 1 || got.Categories[0].Units != 3 {
		t.Fatalf("inactive empty variants drop out: items %+v categories %+v", got.Items, got.Categories)
	}
	if other, err := svc.Stock(context.Background(), uuid.NewString(), time.Now()); err != nil || len(other.Items) != 0 {
		t.Fatalf("another Store: %+v %v", other, err)
	}
}

// printableService builds the report service over the test database with
// the business tax toggle set as given.
func printableService(t *testing.T, pool *pgxpool.Pool, tax bool) salesreport.Service {
	t.Helper()
	business := settings.NewService(NewBusinessSettings(pool, 5*time.Second), uuid.NewString, time.Now)
	current, err := business.Business(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if current.TaxEnabled != tax {
		if _, err := business.UpdateBusiness(context.Background(), settings.Business{TaxEnabled: tax}, current.Revision, ""); err != nil {
			t.Fatal(err)
		}
	}
	return salesreport.NewService(NewReporting(pool, 5*time.Second), business)
}

// With tax off the report drops the USD Sale's tax (0.50 = EGP 26.00);
// a Cloud rate of 50 then wins over the USD Sale's 52 for EGP Sales.
func TestPrintableSalesReportUsesBusinessSettingsAndCloudRates(t *testing.T) {
	env := openDashEnv(t)
	projectDashSale(t, env, "aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa", "2026-09-20T11:00:00Z", fixture(t, "sale_egp.json"))
	projectDashSale(t, env, "bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb", "2026-09-20T10:00:00Z", fixture(t, "sale_usd.json"))
	svc := printableService(t, env.pool, false)
	req := dashReq(t, env, "custom", "2026-09-20", "2026-09-20")
	ctx := context.Background()

	got, err := svc.Sales(ctx, req, salesreport.DisplayEGP, salesreport.Filter{}, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary[0].Sales != 262_400 || got.Summary[0].Tax != 0 || got.Settings.TaxEnabled {
		t.Fatalf("tax off: %+v %+v", got.Summary[0], got.Settings)
	}

	rates := settings.NewService(NewBusinessSettings(env.pool, 5*time.Second), uuid.NewString, time.Now)
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if _, err := rates.AddRate(ctx, "50", at, "bank", ""); err != nil {
		t.Fatal(err)
	}
	var ae *apperr.Error
	if _, err := rates.AddRate(ctx, "51", at, "", ""); !errors.As(err, &ae) || ae.Kind != apperr.Conflict {
		t.Fatalf("same instant twice: %v", err)
	}
	// A later rate does not apply to the September Sale.
	if _, err := rates.AddRate(ctx, "60", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), "", ""); err != nil {
		t.Fatal(err)
	}
	usd, err := svc.Sales(ctx, req, salesreport.DisplayUSD, salesreport.Filter{}, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	// 200000/50 = 4000, plus the USD Sale's 1250 - 50 tax.
	if usd.Summary[0].Sales != 5_200 {
		t.Fatalf("cloud rate: %+v", usd.Summary[0])
	}
	list, err := rates.Rates(ctx)
	if err != nil || len(list) != 2 || list[0].Rate != "60" || list[1].Note != "bank" {
		t.Fatalf("rates %+v %v", list, err)
	}
	if err := rates.DeleteRate(ctx, list[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := rates.DeleteRate(ctx, list[0].ID); !errors.As(err, &ae) || ae.Kind != apperr.NotFound {
		t.Fatalf("second delete: %v", err)
	}
}
