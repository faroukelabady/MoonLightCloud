package app

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
)

func testConfig(t *testing.T, url string) config.Config {
	t.Helper()
	loc, err := time.LoadLocation("Africa/Cairo")
	if err != nil {
		t.Fatal(err)
	}
	_ = loc
	t.Setenv("ENVIRONMENT", config.EnvDevelopment)
	t.Setenv("HTTP_ADDR", ":0")
	t.Setenv("DATABASE_URL", url)
	t.Setenv("LOG_LEVEL", "error")
	t.Setenv("DEVICE_SECRET_PEPPER", config.DevPepper)
	t.Setenv("STORE_TIMEZONE", "Africa/Cairo")
	// Explicit dev-open reporting (mirrors the dev compose default).
	t.Setenv("ALLOW_UNAUTHENTICATED_REPORTING", "true")
	t.Setenv("DASHBOARD_USERNAME", "op")
	t.Setenv("DASHBOARD_PASSWORD_HASH", testPasswordHash())
	c, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Fixed-point overrides the env cannot express.
	c.HTTPAddr = ":0"
	c.DBMaxConns = 4
	c.DBMinConns = 1
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	return c
}

func testPasswordHash() string {
	h, err := dashboard.HashPassword("op-test-password")
	if err != nil {
		panic(err)
	}
	return h
}

func TestNewHealthyAndReady(t *testing.T) {
	url := testutil.Isolated(t)
	a, err := New(context.Background(), testConfig(t, url))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	req := httptest.NewRequest("GET", "/health/ready", nil)
	rec := httptest.NewRecorder()
	a.Health.ServeReady(rec, req)
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestNewRejectsUnmigratedSchema(t *testing.T) {
	url := testutil.Raw(t)
	if _, err := New(context.Background(), testConfig(t, url)); err == nil {
		t.Fatal("startup must fail on unmigrated schema")
	}
}

func TestReadyFailsWithoutDB(t *testing.T) {
	url := testutil.Isolated(t)
	a, err := New(context.Background(), testConfig(t, url))
	if err != nil {
		t.Fatal(err)
	}
	a.Pool.Close() // simulate unavailable DB
	req := httptest.NewRequest("GET", "/health/ready", nil)
	rec := httptest.NewRecorder()
	a.Health.ServeReady(rec, req)
	if rec.Code != 503 {
		t.Fatalf("want 503, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestBlockedProjectionKeepsHealth proves a blocked sale projection is an
// operational condition, not process unavailability: readiness stays 200.
func TestBlockedProjectionKeepsHealth(t *testing.T) {
	url := testutil.Isolated(t)
	a, err := New(context.Background(), testConfig(t, url))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	p, err := a.Devices.Create(ctx, "shop-dev")
	if err != nil {
		t.Fatal(err)
	}
	// Bypass HTTP validation with a corrupt inbox row, then project it.
	eventID := "22222222-2222-7222-8222-222222222222"
	if _, err := a.Pool.Exec(ctx, `
		INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, received_at, payload, payload_hash)
		VALUES ($1, $2, 'sale.finalized.v1', now(), now(), '{}', '\x00')`,
		eventID, p.Device.ID); err != nil {
		t.Fatal(err)
	}
	rec, ok, err := a.SaleStore.LoadSaleEvent(ctx, eventID)
	if err != nil || !ok {
		t.Fatalf("event must load: %v", err)
	}
	_ = rec
	// Drive one projection through the app-wired projector store.
	if _, err := a.ProjectOne(ctx, eventID); err != nil {
		t.Fatalf("projection attempt must return an outcome, not an error: %v", err)
	}
	req := httptest.NewRequest("GET", "/health/ready", nil)
	ready := httptest.NewRecorder()
	a.Health.ServeReady(ready, req)
	if ready.Code != 200 {
		t.Fatalf("blocked projection must not break readiness: %d", ready.Code)
	}
	live := httptest.NewRecorder()
	a.Health.ServeLive(live, httptest.NewRequest("GET", "/health/live", nil))
	if live.Code != 200 {
		t.Fatalf("liveness must stay 200: %d", live.Code)
	}
}
