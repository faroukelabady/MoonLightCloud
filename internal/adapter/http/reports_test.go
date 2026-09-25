package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

// stubReportRepo serves canned rows for handler contract tests.
type stubReportRepo struct {
	summary []report.SummaryRow
	pays    []report.PaymentRow
}

func (s stubReportRepo) SalesSummary(context.Context, time.Time, time.Time, string) ([]report.SummaryRow, error) {
	return s.summary, nil
}
func (s stubReportRepo) SalesPayments(context.Context, time.Time, time.Time, string) ([]report.PaymentRow, error) {
	return s.pays, nil
}
func (s stubReportRepo) SalesDaily(context.Context, time.Time, time.Time, string, string) ([]report.DailyRowRaw, error) {
	return nil, nil
}
func (s stubReportRepo) SalesByProduct(context.Context, time.Time, time.Time, string) ([]report.ProductRow, error) {
	return nil, nil
}
func (s stubReportRepo) SalesByRootCategory(context.Context, time.Time, time.Time, string) ([]report.CategoryRow, error) {
	return nil, nil
}
func (s stubReportRepo) SalesBySubcategory(context.Context, time.Time, time.Time, string) ([]report.CategoryRow, error) {
	return nil, nil
}
func (s stubReportRepo) SalesByCashier(context.Context, time.Time, time.Time, string) ([]report.CashierRow, error) {
	return nil, nil
}
func (s stubReportRepo) SalesByChannel(context.Context, time.Time, time.Time, string) ([]report.ChannelRow, error) {
	return nil, nil
}
func (s stubReportRepo) SalesProjectionFreshness(context.Context) (report.FreshnessRow, error) {
	return report.FreshnessRow{}, nil
}
func (s stubReportRepo) RefundsSummary(context.Context, time.Time, time.Time, string) ([]report.RefundSummaryRow, error) {
	return nil, nil
}
func (s stubReportRepo) RefundsDaily(context.Context, time.Time, time.Time, string, string) ([]report.RefundDailyRow, error) {
	return nil, nil
}
func (s stubReportRepo) RefundsByProduct(context.Context, time.Time, time.Time, string) ([]report.RefundProductRow, error) {
	return nil, nil
}
func (s stubReportRepo) RefundsByRootCategory(context.Context, time.Time, time.Time, string) ([]report.RefundCategoryRow, error) {
	return nil, nil
}
func (s stubReportRepo) RefundsBySubcategory(context.Context, time.Time, time.Time, string) ([]report.RefundCategoryRow, error) {
	return nil, nil
}
func (s stubReportRepo) RefundsByCashier(context.Context, time.Time, time.Time, string) ([]report.RefundCashierRow, error) {
	return nil, nil
}
func (s stubReportRepo) RefundsByChannel(context.Context, time.Time, time.Time, string) ([]report.RefundChannelRow, error) {
	return nil, nil
}
func (s stubReportRepo) ReturnProjectionFreshness(context.Context) (report.ReturnFreshnessRow, error) {
	return report.ReturnFreshnessRow{}, nil
}

func reportTestMux() http.Handler {
	loc, _ := time.LoadLocation("Africa/Cairo")
	svc := report.NewService(stubReportRepo{
		summary: []report.SummaryRow{{Currency: "EGP", Transactions: 2, Units: 3,
			Subtotal: 300000, Discount: 10000, Tax: 5000, SalesTotal: 295000, LineCost: 100000}},
	}, clock.Fixed{T: time.Date(2026, 9, 22, 7, 0, 0, 0, time.UTC)}, loc)
	h := NewReportHandlers(svc, slog.Default())
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/reports/sales/summary", ReportAuth("test-token-0123456789")(http.HandlerFunc(h.SalesSummary)))
	mux.Handle("GET /api/v1/reports/sales/daily", ReportAuth("test-token-0123456789")(http.HandlerFunc(h.SalesDaily)))
	mux.Handle("GET /api/v1/reports/sales/breakdown", ReportAuth("test-token-0123456789")(http.HandlerFunc(h.SalesBreakdown)))
	return mux
}

func getReport(t *testing.T, mux http.Handler, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestReportAuthBoundary(t *testing.T) {
	mux := reportTestMux()
	const good = "test-token-0123456789"
	if rec := getReport(t, mux, "/api/v1/reports/sales/summary?period=today", ""); rec.Code != 401 {
		t.Fatalf("missing token: want 401, got %d", rec.Code)
	}
	if rec := getReport(t, mux, "/api/v1/reports/sales/summary?period=today", "wrong"); rec.Code != 401 {
		t.Fatalf("wrong token: want 401, got %d", rec.Code)
	}
	// Device-style credential must not authenticate reporting.
	if rec := getReport(t, mux, "/api/v1/reports/sales/summary?period=today",
		"11111111-1111-4111-8111-111111111111.22222222-2222-4222-8222-222222222222.secret"); rec.Code != 401 {
		t.Fatalf("device credential on reports: want 401, got %d", rec.Code)
	}
	rec := getReport(t, mux, "/api/v1/reports/sales/summary?period=today", good)
	if rec.Code != 200 {
		t.Fatalf("valid token: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var sum report.Summary
	if err := json.Unmarshal(rec.Body.Bytes(), &sum); err != nil {
		t.Fatal(err)
	}
	if sum.TransactionCount != 2 || len(sum.CurrencyTotals) != 1 {
		t.Fatalf("summary: %+v", sum)
	}
	if sum.Period.Kind != "today" || sum.Timezone != "Africa/Cairo" {
		t.Fatalf("metadata: %+v", sum.Period)
	}
}

func TestReportOpenModeRequiresExplicitOptIn(t *testing.T) {
	// Empty configured token = explicit dev-open wiring only. The
	// fail-closed decision lives in config; the middleware honors an
	// explicitly empty token and warns once.
	loc, _ := time.LoadLocation("Africa/Cairo")
	svc := report.NewService(stubReportRepo{}, clock.Fixed{T: time.Date(2026, 9, 22, 7, 0, 0, 0, time.UTC)}, loc)
	h := NewReportHandlers(svc, slog.Default())
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/reports/sales/summary", ReportAuth("")(http.HandlerFunc(h.SalesSummary)))
	req := httptest.NewRequest("GET", "/api/v1/reports/sales/summary?period=today", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("explicit dev-open wiring allows: want 200, got %d", rec.Code)
	}
}

func TestReportParamErrors(t *testing.T) {
	mux := reportTestMux()
	const good = "test-token-0123456789"
	cases := []struct {
		name string
		path string
		want int
	}{
		{"missing period", "/api/v1/reports/sales/summary", 400},
		{"bad period", "/api/v1/reports/sales/summary?period=weekly", 400},
		{"custom missing to", "/api/v1/reports/sales/summary?period=custom&from_date=2026-01-01", 400},
		{"from after to", "/api/v1/reports/sales/summary?period=custom&from_date=2026-02-02&to_date=2026-02-01", 400},
		{"bad currency", "/api/v1/reports/sales/summary?period=today&currency=EUR", 400},
		{"bad dimension", "/api/v1/reports/sales/breakdown?period=today&dimension=profit", 400},
		{"missing dimension", "/api/v1/reports/sales/breakdown?period=today", 400},
	}
	for _, tc := range cases {
		rec := getReport(t, mux, tc.path, good)
		if rec.Code != tc.want {
			t.Fatalf("%s: want %d, got %d: %s", tc.name, tc.want, rec.Code, rec.Body.String())
		}
		var env Envelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Error.Code == "" {
			t.Fatalf("%s: want error envelope", tc.name)
		}
	}
}

func TestReportNoDataContract(t *testing.T) {
	mux := reportTestMux()
	rec := getReport(t, mux, "/api/v1/reports/sales/daily?period=custom&from_date=2020-01-01&to_date=2020-01-31",
		"test-token-0123456789")
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var daily report.Daily
	if err := json.Unmarshal(rec.Body.Bytes(), &daily); err != nil {
		t.Fatal(err)
	}
	if daily.Days == nil {
		t.Fatal("days must be [], not null")
	}
	// Token never appears in logs or bodies (spot check body).
	if strings.Contains(rec.Body.String(), "test-token-0123456789") {
		t.Fatal("token leaked into response body")
	}
}

// shapeRepo returns one cashier row and one product row for DTO shape tests.
type shapeRepo struct{ stubReportRepo }

func (s shapeRepo) SalesByCashier(context.Context, time.Time, time.Time, string) ([]report.CashierRow, error) {
	id, name := "cashier-1", "Amal"
	return []report.CashierRow{{CashierID: &id, CashierName: &name, Transactions: 1,
		Units: 2, Currency: "EGP", Subtotal: 200000, Discount: 20000, Tax: 14000, SalesTotal: 194000}}, nil
}

func (s shapeRepo) SalesByProduct(context.Context, time.Time, time.Time, string) ([]report.ProductRow, error) {
	pid := "66666666-6666-6666-8666-666666666666"
	return []report.ProductRow{{ProductID: &pid, SKU: "SKU-1", ProductName: "Alpha",
		Units: 2, Currency: "EGP", LineSales: 200000, LineCost: 20000}}, nil
}

// TestBreakdownDTOShapesMatchOpenAPI proves runtime JSON matches the
// documented split contract: header rows carry currency_totals (never
// line_sales); line rows carry line_sales (never currency_totals).
func TestBreakdownDTOShapesMatchOpenAPI(t *testing.T) {
	loc, _ := time.LoadLocation("Africa/Cairo")
	svc := report.NewService(shapeRepo{}, clock.Fixed{T: time.Date(2026, 9, 22, 7, 0, 0, 0, time.UTC)}, loc)
	h := NewReportHandlers(svc, slog.Default())
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/reports/sales/breakdown", ReportAuth("test-token-0123456789")(http.HandlerFunc(h.SalesBreakdown)))

	cashier := getReport(t, mux, "/api/v1/reports/sales/breakdown?period=today&dimension=cashier", "test-token-0123456789")
	if cashier.Code != 200 {
		t.Fatalf("cashier: %d", cashier.Code)
	}
	var cashBody map[string]any
	if err := json.Unmarshal(cashier.Body.Bytes(), &cashBody); err != nil {
		t.Fatal(err)
	}
	cashRow := cashBody["rows"].([]any)[0].(map[string]any)
	if _, ok := cashRow["currency_totals"]; !ok {
		t.Fatal("cashier row must carry currency_totals")
	}
	if _, ok := cashRow["line_sales"]; ok {
		t.Fatal("cashier row must not carry line_sales")
	}
	ct := cashRow["currency_totals"].([]any)[0].(map[string]any)
	for _, k := range []string{"subtotal_minor", "discount_minor", "tax_minor", "sales_total_minor"} {
		if _, ok := ct[k]; !ok {
			t.Fatalf("cashier bucket missing %s", k)
		}
	}

	product := getReport(t, mux, "/api/v1/reports/sales/breakdown?period=today&dimension=product", "test-token-0123456789")
	var prodBody map[string]any
	if err := json.Unmarshal(product.Body.Bytes(), &prodBody); err != nil {
		t.Fatal(err)
	}
	prodRow := prodBody["rows"].([]any)[0].(map[string]any)
	if _, ok := prodRow["line_sales"]; !ok {
		t.Fatal("product row must carry line_sales")
	}
	if _, ok := prodRow["currency_totals"]; ok {
		t.Fatal("product row must not carry currency_totals")
	}
}

// TestBreakdownNegativeRowContract enforces the F1 split at runtime using
// exact key sets: valid rows carry only their family's keys, and any
// cross-shape mixture is rejected. Structural companion to the OpenAPI
// additionalProperties/oneOf assertions.
func TestBreakdownNegativeRowContract(t *testing.T) {
	loc, _ := time.LoadLocation("Africa/Cairo")
	svc := report.NewService(shapeRepo{}, clock.Fixed{T: time.Date(2026, 9, 22, 7, 0, 0, 0, time.UTC)}, loc)
	h := NewReportHandlers(svc, slog.Default())
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/reports/sales/breakdown", ReportAuth("test-token-0123456789")(http.HandlerFunc(h.SalesBreakdown)))

	lineKeys := map[string]bool{"dimension": true, "units": true, "units_returned": true, "line_sales": true,
		"product_id": true, "sku": true, "product_name": true,
		"classification_kind": true, "classification_id": true, "name_ar": true, "name_en": true}
	headerKeys := map[string]bool{"dimension": true, "units": true, "units_returned": true, "transactions": true,
		"return_transactions": true,
		"currency_totals":     true, "cashier_id": true, "cashier_name": true, "channel": true}
	lineBucketKeys := map[string]bool{"currency": true, "line_sales_minor": true, "line_cost_minor": true,
		"line_refund_minor": true, "line_returned_cost_minor": true}
	headerBucketKeys := map[string]bool{"currency": true, "subtotal_minor": true, "discount_minor": true,
		"tax_minor": true, "sales_total_minor": true, "refund_total_minor": true, "net_sales_minor": true}

	check := func(dim string, isLine bool) {
		rec := getReport(t, mux,
			"/api/v1/reports/sales/breakdown?period=today&dimension="+dim, "test-token-0123456789")
		if rec.Code != 200 {
			t.Fatalf("%s: %d", dim, rec.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		rows := body["rows"].([]any)
		if len(rows) == 0 {
			t.Fatalf("%s: no rows", dim)
		}
		for _, r := range rows {
			row := r.(map[string]any)
			allowed := lineKeys
			forbidden := "currency_totals"
			if !isLine {
				allowed = headerKeys
				forbidden = "line_sales"
			}
			for k := range row {
				if !allowed[k] {
					t.Fatalf("%s row carries forbidden key %q: %v", dim, k, row)
				}
			}
			if _, ok := row[forbidden]; ok {
				t.Fatalf("%s row carries %s: %v", dim, forbidden, row)
			}
			bucketKey := "line_sales"
			if !isLine {
				bucketKey = "currency_totals"
			}
			buckets, ok := row[bucketKey].([]any)
			if !ok || len(buckets) == 0 {
				t.Fatalf("%s row missing buckets: %v", dim, row)
			}
			wantKeys := lineBucketKeys
			if !isLine {
				wantKeys = headerBucketKeys
			}
			for _, b := range buckets {
				bm := b.(map[string]any)
				if len(bm) != len(wantKeys) {
					t.Fatalf("%s bucket key count: %v", dim, bm)
				}
				for k := range bm {
					if !wantKeys[k] {
						t.Fatalf("%s bucket carries forbidden key %q: %v", dim, k, bm)
					}
				}
			}
		}
	}
	check("product", true)
	check("cashier", false)
}

// errRepo injects storage failures for error-contract tests.
type errRepo struct {
	stubReportRepo
	err error
}

func (e errRepo) SalesSummary(context.Context, time.Time, time.Time, string) ([]report.SummaryRow, error) {
	return nil, e.err
}

// TestReportServerErrorEnvelope proves 500/503 map to the shared envelope
// schema with stable codes and no internals.
func TestReportServerErrorEnvelope(t *testing.T) {
	loc, _ := time.LoadLocation("Africa/Cairo")
	mk := func(err error) http.Handler {
		svc := report.NewService(errRepo{err: err},
			clock.Fixed{T: time.Date(2026, 9, 22, 7, 0, 0, 0, time.UTC)}, loc)
		h := NewReportHandlers(svc, slog.Default())
		mux := http.NewServeMux()
		mux.Handle("GET /api/v1/reports/sales/summary",
			ReportAuth("test-token-0123456789")(http.HandlerFunc(h.SalesSummary)))
		return mux
	}
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"unavailable", apperr.New(apperr.Unavailable, "sales summary temporarily unavailable"), 503, "UNAVAILABLE"},
		{"internal", apperr.New(apperr.Internal, "sales summary"), 500, "INTERNAL"},
	}
	for _, tc := range cases {
		rec := getReport(t, mk(tc.err),
			"/api/v1/reports/sales/summary?period=today", "test-token-0123456789")
		if rec.Code != tc.status {
			t.Fatalf("%s: want %d, got %d", tc.name, tc.status, rec.Code)
		}
		var env Envelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s: body must be the shared envelope: %v", tc.name, err)
		}
		if env.Error.Code != tc.code || env.Error.Message == "" {
			t.Fatalf("%s: envelope fields: %+v", tc.name, env)
		}
	}
}
