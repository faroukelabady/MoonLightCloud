package http

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

// TestStoreScopeMatrix pins the 9D scope model: empty selects global
// (nil, backward compatible), valid UUIDs scope, malformed values are
// 400 InvalidInput (never silent global fallback, never 500).
func TestStoreScopeMatrix(t *testing.T) {
	valid := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	for name, tc := range map[string]struct {
		input      string
		wantScoped bool
		wantID     string
	}{
		"empty selects global":      {input: "", wantScoped: false},
		"whitespace selects global": {input: "   ", wantScoped: false},
		"valid UUID scopes":         {input: valid, wantScoped: true, wantID: valid},
		"padded UUID trims":         {input: "  " + valid + "  ", wantScoped: true, wantID: valid},
	} {
		scope, err := report.ParseStoreScope(tc.input)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		req := report.Request{Store: scope}
		if req.Scoped() != tc.wantScoped {
			t.Fatalf("%s: scoped=%v want %v", name, req.Scoped(), tc.wantScoped)
		}
		if tc.wantScoped && req.ScopeStoreID() != tc.wantID {
			t.Fatalf("%s: id=%q want %q", name, req.ScopeStoreID(), tc.wantID)
		}
		if !tc.wantScoped && req.ScopeStoreID() != "" {
			t.Fatalf("%s: global ScopeStoreID must be empty", name)
		}
		if !tc.wantScoped && req.StoreIDOrNil() != nil {
			t.Fatalf("%s: global StoreIDOrNil must be nil (JSON null)", name)
		}
	}
	for _, bad := range []string{"not-a-uuid", "123", "store-1", valid + "-extra", "null"} {
		if _, err := report.ParseStoreScope(bad); err == nil {
			t.Fatalf("%q must fail", bad)
		}
	}
}

// TestDashboardStoreScopeParamRejected proves malformed store_id is a 400
// on every scoped surface (dashboard overview, report summary, orders
// list), never a silent global read. Malformed scopes fail in request
// parsing before any service is touched, so zero-value services suffice.
func TestDashboardStoreScopeParamRejected(t *testing.T) {
	loc, err := time.LoadLocation("Africa/Cairo")
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rep := report.NewService(stubReportRepo{}, clock.System{}, loc)
	// Zero-value dashboard service: parse fails before it is touched.
	dashHandlers := NewDashboardDataHandlers(dashboard.Service{}, rep, log)
	repHandlers := NewReportHandlers(rep, log)
	orderHandlers := NewDashboardOrderHandlers(&stubOrderReader{}, log)

	for name, req := range map[string]*http.Request{
		"dashboard overview": httptest.NewRequest("GET", "/api/v1/dashboard/overview?period=today&store_id=nope", nil),
		"report summary":     httptest.NewRequest("GET", "/api/v1/reports/sales/summary?period=today&store_id=nope", nil),
		"orders list":        httptest.NewRequest("GET", "/api/v1/dashboard/orders?store_id=nope", nil),
	} {
		rec := httptest.NewRecorder()
		switch name {
		case "dashboard overview":
			dashHandlers.Overview(rec, req)
		case "report summary":
			repHandlers.SalesSummary(rec, req)
		case "orders list":
			orderHandlers.Orders(rec, req)
		}
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: want 400, got %d", name, rec.Code)
		}
	}
}
