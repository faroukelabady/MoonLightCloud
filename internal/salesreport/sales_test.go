package salesreport

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

func ptr(v int64) *int64 { return &v }

var cairo = func() *time.Location {
	loc, err := time.LoadLocation("Africa/Cairo")
	if err != nil {
		panic(err)
	}
	return loc
}()

// One EGP Sale (EGP 10.00, cost 5.20) and one USD Sale (USD 2.00 at 52,
// cost 1.00), both in Category "Papyrus"; the EGP line is half returned
// and restocked.
func fixture() Facts {
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	return Facts{
		Sales: []Sale{
			{ID: "usd", Number: "S-2", OccurredAt: at.Add(time.Hour), Currency: "USD", Subtotal: 200, Total: 200, FxMicrorate: 52_000_000, InPeriod: true},
			{ID: "egp", Number: "S-1", OccurredAt: at, Currency: "EGP", Subtotal: 1000, Total: 1000, InPeriod: true, USDRateMicrorate: 52_000_000},
		},
		Lines: []Line{
			{SaleID: "egp", ItemID: "l1", ProductID: "p1", SKU: "PAP-1", ProductName: "Papyrus", Quantity: 2, LineTotal: 1000, UnitCost: ptr(260)},
			{SaleID: "usd", ItemID: "l2", ProductID: "p2", SKU: "PAP-2", ProductName: "Scarab", Quantity: 1, LineTotal: 200, UnitCost: ptr(100)},
		},
		Classifications: []Classification{
			{SaleID: "egp", ItemID: "l1", Kind: "root", ID: "c1", NameAr: "بردي", NameEn: "Papyrus"},
			{SaleID: "usd", ItemID: "l2", Kind: "root", ID: "c1", NameAr: "بردي", NameEn: "Papyrus"},
		},
		Tags:     []Tag{{SaleID: "egp", ItemID: "l1", ID: "t1", NameAr: "ذهبي", NameEn: "Gold"}},
		Payments: []Payment{{SaleID: "egp", Method: "cash", Amount: 1000, Change: 100}, {SaleID: "usd", Method: "card", Amount: 200}},
		Returns:  []Return{{ID: "r1", SaleID: "egp", OccurredAt: at.Add(24 * time.Hour), Currency: "EGP", Total: 500}},
		ReturnLines: []ReturnLine{
			{ReturnID: "r1", SaleID: "egp", SaleLineID: "l1", Quantity: 1, Restocked: true, Refund: 500, Cost: ptr(260)},
		},
		ReturnPayments: []ReturnPayment{{ReturnID: "r1", Method: "cash", Amount: 500}},
	}
}

func TestBuildConvertsEverySaleIntoTheDisplayCurrency(t *testing.T) {
	cases := []struct {
		display                     string
		sales, cost, refund, profit int64
		categorySales               int64
	}{
		// EGP: 1000 + 200*52; cost 520 + 100*52; profit 11400-500 - (5720-260).
		{display: DisplayEGP, sales: 11_400, cost: 5_720, refund: 500, profit: 5_440, categorySales: 11_400},
		// USD: 1000/52 = 19.23 -> 19; cost 520/52 = 10; refund 500/52 = 9.6 -> 10; restocked 260/52 = 5.
		{display: DisplayUSD, sales: 219, cost: 110, refund: 10, profit: 104, categorySales: 219},
	}
	for _, tc := range cases {
		got, err := Build(fixture(), Options{Display: tc.display, Loc: cairo, Offset: 0, Limit: 50})
		if err != nil {
			t.Fatalf("%s: %v", tc.display, err)
		}
		if len(got.Summary) != 1 || got.Summary[0].Currency != tc.display {
			t.Fatalf("%s: summary %+v", tc.display, got.Summary)
		}
		s := got.Summary[0]
		if int64(s.Sales) != tc.sales || int64(s.Cost) != tc.cost || int64(s.Refund) != tc.refund || int64(s.Profit) != tc.profit {
			t.Fatalf("%s: summary %+v", tc.display, s)
		}
		if s.SalesCount != 2 || s.ItemsSold != 3 || s.ReturnedItems != 1 || s.ReturnCount != 1 {
			t.Fatalf("%s: counts %+v", tc.display, s)
		}
		if len(got.Categories) != 1 || int64(got.Categories[0].Sales) != tc.categorySales || got.Categories[0].ReturnedItems != 1 {
			t.Fatalf("%s: categories %+v", tc.display, got.Categories)
		}
		var paid int64
		for _, p := range got.Payments {
			if p.Currency != tc.display {
				t.Fatalf("%s: payment currency %+v", tc.display, p)
			}
			paid += int64(p.Amount)
		}
		if paid != tc.sales {
			t.Fatalf("%s: payments %d, want %d", tc.display, paid, tc.sales)
		}
		for _, row := range got.Sales {
			if row.Currency != tc.display {
				t.Fatalf("%s: sale row %+v", tc.display, row)
			}
		}
	}
}

func TestBuildAllKeepsCurrenciesSeparate(t *testing.T) {
	got, err := Build(fixture(), Options{Display: DisplayAll, Loc: cairo, Offset: 0, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Summary) != 2 || got.Summary[0].Currency != "EGP" || got.Summary[1].Currency != "USD" {
		t.Fatalf("summary %+v", got.Summary)
	}
	if got.Summary[0].Sales != 1000 || got.Summary[1].Sales != 200 {
		t.Fatalf("native totals %+v", got.Summary)
	}
	if len(got.Categories) != 2 {
		t.Fatalf("one category row per currency, got %+v", got.Categories)
	}
	// The return lands on its own day, after the sales day.
	if len(got.Daily) != 3 || got.Daily[2].Date != "2026-10-21" || got.Daily[2].Refund != 500 || got.Daily[2].ReturnedItems != 1 {
		t.Fatalf("daily %+v", got.Daily)
	}
	if got.Tags[0].NetSales != 500 || got.Products[0].NetItems != 1 && got.Products[1].NetItems != 1 {
		t.Fatalf("tags %+v products %+v", got.Tags, got.Products)
	}
}

func TestBuildDetailsPageAndListNames(t *testing.T) {
	got, err := Build(fixture(), Options{Display: DisplayEGP, Loc: cairo, Offset: 1, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got.SalesTotalCount != 2 || len(got.Sales) != 1 || got.Sales[0].SaleNumber != "S-1" {
		t.Fatalf("page %+v", got.Sales)
	}
	row := got.Sales[0]
	if row.Products != "Papyrus" || row.RootNamesEn != "Papyrus" || row.ItemsSold != 2 || row.Cost != 520 || len(row.PaymentMethods) != 1 {
		t.Fatalf("row %+v", row)
	}
}

func TestBuildNeedsARateToShowEGPInUSD(t *testing.T) {
	f := fixture()
	f.Sales[1].USDRateMicrorate = 0
	_, err := Build(f, Options{Display: DisplayUSD, Loc: cairo, Offset: 0, Limit: 50})
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Message != ErrRateRequired {
		t.Fatalf("err = %v", err)
	}
	if _, err := Build(f, Options{Display: DisplayEGP, Loc: cairo, Offset: 0, Limit: 50}); err != nil {
		t.Fatalf("EGP keeps working from USD snapshots: %v", err)
	}
}

func TestBuildFlagsLinesWithoutCost(t *testing.T) {
	f := fixture()
	f.Lines[0].UnitCost = nil
	got, err := Build(f, Options{Display: DisplayAll, Loc: cairo, Offset: 0, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary[0].MissingCostItems != 2 || got.Summary[0].Cost != 0 {
		t.Fatalf("summary %+v", got.Summary[0])
	}
}

func TestConvertRoundsHalfAwayFromZero(t *testing.T) {
	b := &builder{}
	for _, tc := range []struct{ amount, num, den, want int64 }{
		{1250, 52_000_000, 1_000_000, 65_000}, {1, 1, 3, 0}, {2, 1, 3, 1}, {3, 1, 2, 2}, {-3, 1, 2, -2},
	} {
		if got := b.convert(conversion{num: tc.num, den: tc.den}, tc.amount); got != tc.want {
			t.Fatalf("%d*%d/%d = %d, want %d", tc.amount, tc.num, tc.den, got, tc.want)
		}
	}
}

func TestMoneyIsAJSONString(t *testing.T) {
	out, err := json.Marshal(struct {
		M Money `json:"m"`
	}{M: 9_007_199_254_740_993})
	if err != nil || string(out) != `{"m":"9007199254740993"}` {
		t.Fatalf("%s %v", out, err)
	}
}

func TestBuildStockValuesEveryVariant(t *testing.T) {
	totals, categories, rows, err := BuildStock([]StockItem{
		{VariantID: "v1", ProductID: "p1", CategoryID: "c1", CategoryNameAr: "بردي", Active: true, Quantity: 3, UnitCost: ptr(500), UnitPrice: ptr(1000)},
		{VariantID: "v2", ProductID: "p1", CategoryID: "c1", Active: true, Quantity: 2, UnitPrice: ptr(1000)},
		{VariantID: "v3", ProductID: "p2", CategoryID: "c2", Active: false, Quantity: 1, UnitCost: ptr(300)},
		{VariantID: "v4", ProductID: "p3", CategoryID: "c2", Active: true, Quantity: 0, UnitCost: ptr(300), UnitPrice: ptr(400)},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := StockTotals{ProductsInStock: 2, VariantsInStock: 3, Units: 6, Cost: 1_800, Retail: 5_000,
		UnitsWithoutCost: 2, UnitsWithoutPrice: 1, InactiveUnits: 1}
	if totals != want {
		t.Fatalf("totals %+v, want %+v", totals, want)
	}
	if len(categories) != 2 || categories[0].ProductsInStock != 1 || categories[0].Units != 5 || categories[0].Retail != 5_000 {
		t.Fatalf("categories %+v", categories)
	}
	if len(rows) != 4 || rows[1].Cost != nil || *rows[0].Retail != 3_000 {
		t.Fatalf("rows %+v", rows)
	}
}

func TestBuildFiltersWholeSalesAndTheirReturns(t *testing.T) {
	all := Options{Display: DisplayAll, Loc: cairo, Limit: 50}

	byTag := all
	byTag.Filter = Filter{TagIDs: []string{"t1"}}
	got, err := Build(fixture(), byTag)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Summary) != 1 || got.Summary[0].Currency != "EGP" || got.Summary[0].ReturnCount != 1 {
		t.Fatalf("tag keeps the EGP sale and its return: %+v", got.Summary)
	}

	// The category matches both lines, the card payment only the USD sale.
	byCard := all
	byCard.Filter = Filter{RootIDs: []string{"c1"}, Payment: "card"}
	got, err = Build(fixture(), byCard)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Summary) != 1 || got.Summary[0].Currency != "USD" || got.Summary[0].ReturnCount != 0 {
		t.Fatalf("card keeps the USD sale only: %+v", got.Summary)
	}
	if len(got.Options.Categories) != 1 || len(got.Options.Tags) != 1 {
		t.Fatalf("options list the unfiltered period: %+v", got.Options)
	}

	// Category and tag must match on the same line.
	none := all
	none.Filter = Filter{RootIDs: []string{"c1"}, TagIDs: []string{"missing"}}
	if got, _ = Build(fixture(), none); len(got.Summary) != 0 || got.SalesTotalCount != 0 {
		t.Fatalf("no line matches: %+v", got.Summary)
	}
}

func TestBuildExcludesTaxWhenTheBusinessDoesNotUseIt(t *testing.T) {
	f := fixture()
	f.Sales[1].Tax, f.Sales[1].Total = 140, 1140
	f.Returns[0].Tax, f.Returns[0].Total = 70, 570
	opts := Options{Display: DisplayAll, Loc: cairo, Limit: 50, ExcludeTax: true}
	got, err := Build(f, opts)
	if err != nil {
		t.Fatal(err)
	}
	egp := got.Summary[0]
	if egp.Sales != 1000 || egp.Tax != 0 || egp.Refund != 500 || egp.RefundedTax != 0 || got.Sales[1].Total != 1000 {
		t.Fatalf("tax excluded: %+v", egp)
	}
	opts.ExcludeTax = false
	if got, _ = Build(f, opts); got.Summary[0].Sales != 1140 || got.Summary[0].Tax != 140 {
		t.Fatalf("tax kept: %+v", got.Summary[0])
	}
}

func splitTaxPaymentFacts() Facts {
	at := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	return Facts{
		Sales: []Sale{{ID: "sale", Number: "S-1", Currency: "EGP", Subtotal: 90, Tax: 11, Total: 101, USDRateMicrorate: 2_000_000, InPeriod: true, OccurredAt: at}},
		Lines: []Line{{SaleID: "sale", ItemID: "line", SKU: "SKU", Quantity: 1, LineTotal: 90, UnitCost: ptr(10)}},
		Payments: []Payment{
			{SaleID: "sale", Method: "cash", Amount: 51, Change: 10},
			{SaleID: "sale", Method: "card", Amount: 30},
			{SaleID: "sale", Method: "card", Amount: 20},
		},
		Returns:        []Return{{ID: "return", SaleID: "sale", Currency: "EGP", Total: 51, Tax: 6, OccurredAt: at.Add(time.Hour)}},
		ReturnPayments: []ReturnPayment{{ReturnID: "return", Method: "cash", Amount: 26}, {ReturnID: "return", Method: "card", Amount: 25}},
	}
}

func TestBuildPaymentTaxAndFXAllocationReconcilesEachDocument(t *testing.T) {
	for _, tc := range []struct {
		name, display                              string
		taxOff                                     bool
		cardPaid, cashPaid, cardRefund, cashRefund int64
	}{
		{"native tax on", DisplayAll, false, 50, 51, 25, 26},
		{"native tax off", DisplayAll, true, 44, 46, 22, 23},
		{"converted tax on", DisplayUSD, false, 25, 26, 12, 14},
		{"converted tax off", DisplayUSD, true, 22, 23, 11, 12},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := splitTaxPaymentFacts()
			opts := Options{Display: tc.display, ExcludeTax: tc.taxOff, Loc: time.UTC, Limit: 50}
			got, err := Build(f, opts)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]PaymentRow{
				"card": {Method: "card", Currency: got.Summary[0].Currency, PaymentCount: 2, Amount: Money(tc.cardPaid), Refund: Money(tc.cardRefund), Net: Money(tc.cardPaid - tc.cardRefund)},
				"cash": {Method: "cash", Currency: got.Summary[0].Currency, PaymentCount: 1, Amount: Money(tc.cashPaid), Refund: Money(tc.cashRefund), Net: Money(tc.cashPaid - tc.cashRefund)},
			}
			var paid, refunded, net Money
			for _, row := range got.Payments {
				if row != want[row.Method] {
					t.Fatalf("%s: %+v want %+v", row.Method, row, want[row.Method])
				}
				paid += row.Amount
				refunded += row.Refund
				net += row.Net
			}
			if len(got.Payments) != 2 || paid != got.Summary[0].Sales || refunded != got.Summary[0].Refund || net != got.Summary[0].NetSales {
				t.Fatalf("payments do not reconcile: %+v summary=%+v", got.Payments, got.Summary)
			}
			// Method allocation cannot depend on private payment IDs, wire order,
			// or whether one method is split into several original tender rows.
			f.Payments[0], f.Payments[2] = f.Payments[2], f.Payments[0]
			f.ReturnPayments[0], f.ReturnPayments[1] = f.ReturnPayments[1], f.ReturnPayments[0]
			reordered, err := Build(f, opts)
			if err != nil || !reflect.DeepEqual(got.Payments, reordered.Payments) || !reflect.DeepEqual(got.Sales[0].PaymentMethods, reordered.Sales[0].PaymentMethods) {
				t.Fatalf("payment order changed result: %+v %v", reordered.Payments, err)
			}
			f.Payments = []Payment{{SaleID: "sale", Method: "card", Amount: 50}, {SaleID: "sale", Method: "cash", Amount: 51}}
			coalesced, err := Build(f, opts)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range coalesced.Payments {
				w := want[row.Method]
				w.PaymentCount = 1
				if row != w {
					t.Fatalf("same-method splits changed allocation: %+v want %+v", row, w)
				}
			}
		})
	}
}

func TestBuildRefundOnlyPaymentAllocationUsesOriginalSaleRate(t *testing.T) {
	f := splitTaxPaymentFacts()
	f.Sales[0].InPeriod = false
	got, err := Build(f, Options{Display: DisplayUSD, ExcludeTax: true, Loc: time.UTC, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sales) != 0 || got.Summary[0].SalesCount != 0 || got.Summary[0].Refund != 23 {
		t.Fatalf("refund-only summary: %+v", got.Summary)
	}
	var refund, net Money
	for _, row := range got.Payments {
		if row.PaymentCount != 0 || row.Amount != 0 {
			t.Fatalf("out-of-period tender counted as sale: %+v", row)
		}
		refund += row.Refund
		net += row.Net
	}
	if refund != 23 || net != -23 {
		t.Fatalf("refund-only allocation %+v", got.Payments)
	}
}

func TestBuildChangeMetadataDoesNotReduceAppliedPayments(t *testing.T) {
	for _, display := range []string{DisplayAll, DisplayEGP, DisplayUSD} {
		for _, taxOff := range []bool{false, true} {
			opts := Options{Display: display, ExcludeTax: taxOff, Loc: time.UTC, Limit: 50}
			base := splitTaxPaymentFacts()
			for n := range base.Payments {
				base.Payments[n].Change = 0
			}
			want, err := Build(base, opts)
			if err != nil {
				t.Fatal(err)
			}
			for _, change := range []int64{10, 5000, math.MaxInt64} {
				f := splitTaxPaymentFacts()
				for n := range f.Payments {
					f.Payments[n].Change = change
				}
				got, err := Build(f, opts)
				if err != nil || !reflect.DeepEqual(got.Payments, want.Payments) || !reflect.DeepEqual(got.Summary, want.Summary) {
					t.Fatalf("%s taxOff=%t change=%d altered applied contributions: got=%+v want=%+v err=%v", display, taxOff, change, got.Payments, want.Payments, err)
				}
			}
		}
	}
}

func TestBuildPaymentAllocationZeroTargetsAndAmounts(t *testing.T) {
	for _, zeroWeights := range []bool{false, true} {
		f := splitTaxPaymentFacts()
		f.Sales[0].Subtotal = 0
		f.Sales[0].Tax = f.Sales[0].Total
		f.Returns[0].Tax = f.Returns[0].Total
		if zeroWeights {
			f.Sales[0].Total = 0
			f.Sales[0].Tax = 0
			f.Returns[0].Total = 0
			f.Returns[0].Tax = 0
			for n := range f.Payments {
				f.Payments[n].Amount = 0
				f.Payments[n].Change = 0
			}
			for n := range f.ReturnPayments {
				f.ReturnPayments[n].Amount = 0
			}
		}
		got, err := Build(f, Options{Display: DisplayUSD, ExcludeTax: true, Loc: time.UTC, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Payments) != 2 {
			t.Fatalf("zero allocations lost tender methods: %+v", got.Payments)
		}
		for _, row := range got.Payments {
			if row.Amount != 0 || row.Refund != 0 || row.Net != 0 || row.PaymentCount < 1 {
				t.Fatalf("zero result: %+v", row)
			}
		}
	}
}

func TestDocumentPaymentAllocationUsesCheckedWideArithmetic(t *testing.T) {
	doc := &documentPayments{}
	if err := doc.add("cash", math.MaxInt64-1); err != nil {
		t.Fatal(err)
	}
	if err := doc.add("card", 1); err != nil {
		t.Fatal(err)
	}
	got, methods, err := doc.allocate(math.MaxInt64)
	if err != nil || !reflect.DeepEqual(methods, []string{"card", "cash"}) || got["card"] != 1 || got["cash"] != math.MaxInt64-1 {
		t.Fatalf("wide allocation: %+v %v %v", got, methods, err)
	}
	if err := doc.add("other", 1); err == nil {
		t.Fatal("document amount overflow accepted")
	}
	if _, _, err := doc.allocate(-1); err == nil {
		t.Fatal("negative target accepted")
	}
	zero := &documentPayments{}
	if err := zero.add("cash", 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := zero.allocate(1); err == nil {
		t.Fatal("positive target without weights accepted")
	}
	if err := zero.add("cash", -1); err == nil {
		t.Fatal("negative payment accepted")
	}
}

func TestBuildRejectsPaymentAndConversionOverflow(t *testing.T) {
	for _, mode := range []string{"sum", "convert", "negative change"} {
		f := splitTaxPaymentFacts()
		switch mode {
		case "sum":
			f.Payments = []Payment{{SaleID: "sale", Method: "cash", Amount: math.MaxInt64}, {SaleID: "sale", Method: "card", Amount: 1}}
		case "convert":
			f.Sales[0].Currency = "USD"
			f.Sales[0].FxMicrorate = 2_000_000
			f.Payments = []Payment{{SaleID: "sale", Method: "cash", Amount: math.MaxInt64}}
		case "negative change":
			f.Payments = []Payment{{SaleID: "sale", Method: "cash", Amount: 1, Change: -1}}
		}
		if _, err := Build(f, Options{Display: DisplayEGP, Loc: time.UTC, Limit: 50}); err == nil {
			t.Fatalf("%s invalid payment/overflow accepted", mode)
		}
	}
}

// Each Sale keeps how it was recorded and the rate that converted it; the
// currency mix adds up to the summary; the rate range covers every rate
// the period's Sales used (a USD Sale's own snapshot always counts).
func TestBuildExplainsCurrenciesAndRates(t *testing.T) {
	facts := fixture()
	facts.Sales[1].USDRateMicrorate = 50_000_000
	type mixWant struct{ count, total, report int64 }
	cases := []struct {
		display      string
		egp, usd     mixWant
		egpRate      int64 // 0 when the EGP Sale keeps its own currency
		usdRate      int64
		minRate, max int64
	}{
		{display: DisplayEGP, egp: mixWant{1, 1000, 1000}, usd: mixWant{1, 200, 10_400}, usdRate: 52_000_000, minRate: 52_000_000, max: 52_000_000},
		// 1000/50 = 20.
		{display: DisplayUSD, egp: mixWant{1, 1000, 20}, usd: mixWant{1, 200, 200}, egpRate: 50_000_000, minRate: 50_000_000, max: 52_000_000},
		{display: DisplayAll, egp: mixWant{1, 1000, 1000}, usd: mixWant{1, 200, 200}, minRate: 52_000_000, max: 52_000_000},
	}
	for _, tc := range cases {
		got, err := Build(facts, Options{Display: tc.display, Loc: cairo, Offset: 0, Limit: 50})
		if err != nil {
			t.Fatalf("%s: %v", tc.display, err)
		}
		if len(got.CurrencyMix) != 2 || got.CurrencyMix[0].Currency != "EGP" || got.CurrencyMix[1].Currency != "USD" {
			t.Fatalf("%s: mix %+v", tc.display, got.CurrencyMix)
		}
		var mixed int64
		for i, want := range []mixWant{tc.egp, tc.usd} {
			row := got.CurrencyMix[i]
			if row.SalesCount != want.count || int64(row.Total) != want.total || int64(row.ReportTotal) != want.report {
				t.Fatalf("%s: mix row %+v, want %+v", tc.display, row, want)
			}
			if tc.display != DisplayAll && row.ReportCurrency != tc.display || tc.display == DisplayAll && row.ReportCurrency != row.Currency {
				t.Fatalf("%s: report currency %+v", tc.display, row)
			}
			mixed += int64(row.ReportTotal)
		}
		if tc.display != DisplayAll && mixed != int64(got.Summary[0].Sales) {
			t.Fatalf("%s: mix %d does not add up to the summary %d", tc.display, mixed, got.Summary[0].Sales)
		}
		for _, row := range got.Sales {
			want, original := tc.egpRate, int64(1000)
			if row.SaleID == "usd" {
				want, original = tc.usdRate, 200
			}
			if int64(row.OriginalTotal) != original || row.OriginalCurrency != map[string]string{"usd": "USD", "egp": "EGP"}[row.SaleID] {
				t.Fatalf("%s: recorded as %+v", tc.display, row)
			}
			if (want == 0) != (row.RateMicrorate == nil) || want != 0 && int64(*row.RateMicrorate) != want {
				t.Fatalf("%s: sale %s rate %v, want %d", tc.display, row.SaleID, row.RateMicrorate, want)
			}
		}
		r := got.ExchangeRate
		if r.PeriodMin == nil || r.PeriodMax == nil || int64(*r.PeriodMin) != tc.minRate || int64(*r.PeriodMax) != tc.max {
			t.Fatalf("%s: rate range %v..%v", tc.display, r.PeriodMin, r.PeriodMax)
		}
	}

	// Without a USD Sale and without conversion no rate was used.
	egpOnly := fixture()
	egpOnly.Sales = egpOnly.Sales[1:]
	got, err := Build(egpOnly, Options{Display: DisplayEGP, Loc: cairo, Offset: 0, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if got.ExchangeRate.PeriodMin != nil || got.ExchangeRate.PeriodMax != nil || len(got.CurrencyMix) != 1 {
		t.Fatalf("egp only: %+v %+v", got.ExchangeRate, got.CurrencyMix)
	}
	body, err := json.Marshal(got.Sales[0])
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil || wire["rate_microrate"] != nil || wire["original_total_minor"] != "1000" {
		t.Fatalf("wire %s", body)
	}
}
