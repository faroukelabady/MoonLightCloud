package http

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func assetDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>shell</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "real.js"), []byte("console.log(1)"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestDashboardAssets(t *testing.T) {
	h := DashboardAssets(assetDir(t), slog.Default())
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := get("/dashboard/login"); rec.Code != 200 {
		t.Fatalf("SPA deep link: want 200, got %d", rec.Code)
	}
	if rec := get("/dashboard/some-route"); rec.Code != 200 {
		t.Fatalf("SPA route: want 200, got %d", rec.Code)
	}
	rec := get("/dashboard/assets/real.js")
	if rec.Code != 200 || rec.Header().Get("Content-Type") == "text/html" {
		t.Fatalf("real asset: want 200 JS, got %d (%s)", rec.Code, rec.Header().Get("Content-Type"))
	}
	rec = get("/dashboard/assets/missing.js")
	if rec.Code != 404 {
		t.Fatalf("missing asset: want 404, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct == "text/html; charset=utf-8" {
		t.Fatalf("missing asset must not be text/html 200-shell, got %s", ct)
	}
	rec = get("/dashboard/assets/../index.html")
	if rec.Code != 404 {
		t.Fatalf("escape attempt: want 404, got %d", rec.Code)
	}
	// Security headers present on dashboard responses.
	rec = get("/dashboard/login")
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("CSP header required")
	}
	if rec.Header().Get("X-Content-Type-Options") == "" {
		t.Fatal("X-Content-Type-Options required")
	}
	// POST to assets is not served.
	req := httptest.NewRequest("POST", "/dashboard/assets/real.js", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == 200 {
		t.Fatal("POST to assets must not 200")
	}
	_ = http.StatusOK
}
