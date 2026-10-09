package app

import (
	"context"
	"encoding/base64"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/config"

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
	// Synthetic test-only MFA key (never a production value).
	t.Setenv("AUTH_MFA_ENCRYPTION_KEY", testMFAKey)
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

// testMFAKey is a synthetic, test-only AUTH_MFA_ENCRYPTION_KEY.
var testMFAKey = base64.StdEncoding.EncodeToString([]byte("test-only-mfa-key-not-production"))

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

// M03: with OPERATIONS_ENABLED=false no engine runs, yet manual
// resolution of an active stateful condition still returns 409 with the
// incident active; genuinely cleared conditions resolve.
func TestDisabledOperationsManualResolveGuard(t *testing.T) {
	url := testutil.Isolated(t)
	a, err := New(context.Background(), testConfig(t, url))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.OperationsEngine != nil {
		t.Fatal("engine must not start when disabled")
	}
	if a.OperationsService == nil || a.OperationsReader == nil {
		t.Fatal("operations reads stay available when disabled")
	}
	ctx := context.Background()
	prov, err := a.Devices.Create(ctx, "m03-dev")
	if err != nil {
		t.Fatal(err)
	}
	dev := prov.Device.ID
	if _, err := a.Pool.Exec(ctx, `INSERT INTO device_control_presence (device_id, last_seen_at, last_poll_at, created_at, updated_at)
		VALUES ($1, now() - interval '10 minutes', now() - interval '10 minutes', now(), now())`, dev); err != nil {
		t.Fatal(err)
	}
	incident, created, err := a.OperationsService.OpenStateful(ctx, "DEVICE_OFFLINE", "device", dev, "")
	if err != nil || !created {
		t.Fatalf("open: %v %v", incident, err)
	}
	if _, err := a.OperationsReader.ResolveOperator(ctx, incident.ID, ""); err == nil {
		t.Fatal("active condition must conflict even with engine disabled")
	} else if got := err.Error(); got == "" || !containsCode(got, "CONDITION_STILL_ACTIVE") {
		t.Fatalf("code: %v", err)
	}
	active, err := a.OperationsService.Get(ctx, incident.ID)
	if err != nil || active.State != "open" {
		t.Fatalf("incident still active: %+v %v", active, err)
	}
	// Genuinely cleared (fresh contact): resolution succeeds.
	if _, err := a.Pool.Exec(ctx, `UPDATE device_control_presence SET last_seen_at = now(), last_poll_at = now() WHERE device_id = $1`, dev); err != nil {
		t.Fatal(err)
	}
	resolved, err := a.OperationsReader.ResolveOperator(ctx, incident.ID, "")
	if err != nil || resolved.State != "resolved" {
		t.Fatalf("cleared resolves: %+v %v", resolved, err)
	}
}

func containsCode(s, code string) bool {
	for i := 0; i+len(code) <= len(s); i++ {
		if s[i:i+len(code)] == code {
			return true
		}
	}
	return false
}
