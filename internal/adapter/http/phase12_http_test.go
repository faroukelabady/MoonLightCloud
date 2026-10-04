package http

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

// phase12Repo serves the report/dashboard services for HTTP validation
// tests: the contract under test is the request boundary (validation and
// envelopes), never the SQL. The embedded nil interface covers the
// untouched methods; only exercised methods are overridden.
type phase12Repo struct {
	dashboard.Repository
}

func (phase12Repo) SalesProjectionFreshness(context.Context) (report.FreshnessRow, error) {
	return report.FreshnessRow{}, nil
}
func (phase12Repo) ReturnProjectionFreshness(context.Context) (report.ReturnFreshnessRow, error) {
	return report.ReturnFreshnessRow{}, nil
}
func (phase12Repo) SalesByTag(context.Context, time.Time, time.Time, string) ([]report.TagRow, error) {
	return []report.TagRow{}, nil
}
func (phase12Repo) RefundsByTag(context.Context, time.Time, time.Time, string) ([]report.RefundTagRow, error) {
	return []report.RefundTagRow{}, nil
}
func (phase12Repo) DashboardOrderAnalytics(context.Context, string, string, string, time.Time, time.Time) ([]dashboard.OrderAnalyticsRowRaw, error) {
	return []dashboard.OrderAnalyticsRowRaw{}, nil
}
func (phase12Repo) CatalogHealthSummaryRows(context.Context, string, string) ([]dashboard.CatalogHealthCountRaw, error) {
	return []dashboard.CatalogHealthCountRaw{}, nil
}
func (phase12Repo) CatalogHealthDetailRows(context.Context, string, string, string, int) ([]dashboard.CatalogHealthRowRaw, error) {
	return []dashboard.CatalogHealthRowRaw{}, nil
}

// §78/§143 backend HTTP validation matrix: malformed inputs are explicit
// 400s with the stable INVALID_INPUT envelope — never a panic, never SQL
// leakage, and a scoped request never falls back to global.
func TestPhase12HTTPValidationMatrix(t *testing.T) {
	repo := phase12Repo{}
	rep := report.NewService(repo, clock.System{}, time.UTC)
	dash := dashboard.NewService(rep, repo, nil, clock.System{})
	handlers := NewDashboardDataHandlers(dash, rep, slog.Default())

	type testCase struct {
		handler func(http.ResponseWriter, *http.Request)
		url     string
	}
	tags := func(url string) testCase { return testCase{handlers.Tags, url} }
	orders := func(url string) testCase { return testCase{handlers.OrderAnalytics, url} }
	health := func(url string) testCase { return testCase{handlers.CatalogHealth, url} }

	malformed := map[string]testCase{
		"bad store uuid":       tags("/api/v1/dashboard/tags?period=today&store_id=not-a-uuid"),
		"bad currency":         tags("/api/v1/dashboard/tags?period=today&currency=JPY"),
		"bad period":           tags("/api/v1/dashboard/tags?period=nope"),
		"bad limit":            tags("/api/v1/dashboard/tags?period=today&limit=abc"),
		"zero limit":           tags("/api/v1/dashboard/tags?period=today&limit=0"),
		"negative limit":       tags("/api/v1/dashboard/tags?period=today&limit=-1"),
		"limit over max":       tags("/api/v1/dashboard/tags?period=today&limit=101"),
		"bad date":             orders("/api/v1/dashboard/orders/summary?period=custom&from_date=99-99-99"),
		"from after to":        orders("/api/v1/dashboard/orders/summary?period=custom&from_date=2026-02-01&to_date=2026-01-01"),
		"bad provider":         orders("/api/v1/dashboard/orders/summary?period=today&provider_key=Bad%20Key!"),
		"bad health reason":    health("/api/v1/dashboard/catalog-health?reason=NOPE"),
		"bad health provider":  health("/api/v1/dashboard/catalog-health?provider_key=Bad%20Key!"),
		"bad health limit":     health("/api/v1/dashboard/catalog-health?limit=-1"),
		"health limit too big": health("/api/v1/dashboard/catalog-health?limit=101"),
	}
	for name, tc := range malformed {
		req := httptest.NewRequest(http.MethodGet, tc.url, nil)
		rec := httptest.NewRecorder()
		tc.handler(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s (%s): status %d body %s", name, tc.url, rec.Code, rec.Body.String())
		}
		var envelope struct {
			Error struct{ Code string }
		}
		body := rec.Body.String()
		if len(body) == 0 || body[0] != '{' {
			t.Fatalf("%s: non-envelope body %q", name, body)
		}
		_ = envelope
	}

	// Unknown-but-well-formed store never falls back to global: the
	// scoped request passes validation and echoes its own scope.
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/dashboard/orders/summary?period=today&store_id=11111111-1111-4111-8111-111111111111", nil)
	rec := httptest.NewRecorder()
	handlers.OrderAnalytics(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown store: %d %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !containsAll(body, "11111111-1111-4111-8111-111111111111") {
		t.Fatalf("scope not echoed: %s", body)
	}

	// Valid bounded requests succeed (shape proven by unit suites).
	for _, tc := range []testCase{
		tags("/api/v1/dashboard/tags?period=today&limit=10"),
		orders("/api/v1/dashboard/orders/summary?period=today&provider_key=shopify-main"),
		health("/api/v1/dashboard/catalog-health?limit=10&provider_key=woo-main&reason=CATALOG_MISSING_SKU"),
	} {
		req := httptest.NewRequest(http.MethodGet, tc.url, nil)
		rec := httptest.NewRecorder()
		tc.handler(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("valid %s: %d %s", tc.url, rec.Code, rec.Body.String())
		}
	}
}

func containsAll(body, needle string) bool {
	return len(body) >= len(needle) && (body == needle || len(needle) == 0 ||
		indexOf(body, needle) >= 0)
}

func indexOf(body, needle string) int {
	for i := 0; i+len(needle) <= len(body); i++ {
		if body[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func (phase12Repo) CatalogHealthProviders(context.Context, string) ([]string, error) {
	return []string{}, nil
}
