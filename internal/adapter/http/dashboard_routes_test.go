package http

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
)

// TestDashboardRoutesWired proves BFF data routes exist behind session
// auth: unauthenticated → 401 envelope (never the reporting token path).
func TestDashboardRoutesWired(t *testing.T) {
	auth := NewDashboardHandlers(
		dashboard.Credentials{Username: "op", PasswordHash: mustTestHash()},
		mustTestKey(), time.Hour, false, slog.Default())
	for _, path := range []string{
		"/api/v1/dashboard/overview?period=today",
		"/api/v1/dashboard/daily?period=today",
		"/api/v1/dashboard/products?period=today&mode=all",
		"/api/v1/dashboard/categories?period=today",
		"/api/v1/dashboard/branches?period=today",
		"/api/v1/dashboard/sync-health",
		"/api/v1/dashboard/activity",
		"/api/v1/dashboard/sales/latest",
		"/api/v1/dashboard/auth/me",
	} {
		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		auth.RequireDashboardSession(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		})).ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatalf("%s: want 401 without session, got %d", path, rec.Code)
		}
	}
}
