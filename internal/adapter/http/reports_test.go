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
