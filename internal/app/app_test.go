package app

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/config"

	istore "github.com/faroukelabady/MoonLightCloud/internal/store"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	"github.com/google/uuid"
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
	// No outside rate provider unless a test points at its own (ADR-0058).
	if _, set := os.LookupEnv("EXCHANGE_RATE_URL"); !set {
		t.Setenv("EXCHANGE_RATE_URL", "")
	}
	// No messaging providers or report runner, even when .env.local (which
	// check.sh loads) turns them on for local use.
	t.Setenv("NOTIFICATIONS_TELEGRAM_ENABLED", "false")
	t.Setenv("NOTIFICATIONS_WHATSAPP_ENABLED", "false")
	t.Setenv("BUSINESS_REPORTS_ENABLED", "false")
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

// The pre-production pair has one producer version per stream. Retired
// versions are rejected before durable acceptance, not silently normalized.
func TestRetiredCatalogVersionsRejected(t *testing.T) {
	a, err := New(context.Background(), testConfig(t, testutil.Isolated(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	device, err := a.Devices.Create(ctx, "catalog-version-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, retired := range []string{"catalog.category.snapshot.v1", "catalog.product.snapshot.v1", "inventory.product.snapshot.v1"} {
		for _, advertised := range a.Sync.Capabilities().SupportedEvents {
			if advertised == retired {
				t.Fatalf("retired event advertised: %s", retired)
			}
		}
		body := fmt.Sprintf(`{"events":[{"event_id":"11111111-1111-4111-8111-111111111111","event_type":%q,"occurred_at":"2026-10-01T12:00:00Z","payload":{}}]}`, retired)
		if _, err := a.Sync.Ingest(ctx, device.Device.ID, device.Credential.ID, []byte(body)); err == nil {
			t.Fatalf("retired event accepted: %s", retired)
		}
	}
	var count int
	if err := a.Pool.QueryRow(ctx, `SELECT count(*) FROM sync_events`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejection persisted events: %d (%v)", count, err)
	}
}

func TestConfiguredDatabaseTimeoutAppliesToApplicationRepositories(t *testing.T) {
	cfg := testConfig(t, testutil.Isolated(t))
	cfg.DBQueryTimeout = 300 * time.Millisecond
	a, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	device, err := a.Devices.Create(ctx, "timeout-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Devices.Get(ctx, device.Device.ID); err != nil {
		t.Fatalf("positive control: %v", err)
	}
	// A disposable view delays the real repository query without modifying
	// production SQL. The configured deadline must cancel it; the old
	// hard-coded five-second default would allow the query to succeed.
	_, err = a.Pool.Exec(ctx, `ALTER TABLE devices RENAME TO timeout_devices;
		CREATE VIEW devices AS SELECT d.* FROM timeout_devices d
		CROSS JOIN (SELECT pg_sleep(2)) delay`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Devices.Get(ctx, device.Device.ID); err == nil {
		t.Fatal("configured repository timeout was ignored")
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
	bindAppTestStore(t, a, p.Device.ID)
	// Bypass HTTP validation with a corrupt inbox row, then project it.
	eventID := "22222222-2222-7222-8222-222222222222"
	if _, err := a.Pool.Exec(ctx, `
		INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, received_at, payload, payload_hash, store_id)
		VALUES ($1, $2, 'sale.finalized.v3', now(), now(), '{}', '\x00', (SELECT store_id FROM device_store_bindings WHERE device_id = $2))`,
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
	if _, err := a.Pool.Exec(ctx, `WITH seen AS (UPDATE devices SET last_seen_at = now() - interval '10 minutes' WHERE id = $1)
		INSERT INTO device_control_presence (device_id, last_poll_at, created_at, updated_at)
		VALUES ($1, now() - interval '10 minutes', now(), now())`, dev); err != nil {
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
	if _, err := a.Pool.Exec(ctx, `WITH seen AS (UPDATE devices SET last_seen_at = now() WHERE id = $1) UPDATE device_control_presence SET last_poll_at = now() WHERE device_id = $1`, dev); err != nil {
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

// bindAppTestStore enrolls a device into a fresh Store; every stored event
// carries its device's Store (ADR-0055).
func bindAppTestStore(t *testing.T, a *App, deviceID string) {
	t.Helper()
	devices := postgres.NewStoreRegistry(a.Pool, 5*time.Second)
	if _, err := devices.RegisterStore(context.Background(), deviceID, istore.RegistrationRequest{
		StoreID: uuid.NewString(), DisplayName: "Test Store", Timezone: "Africa/Cairo",
	}); err != nil {
		t.Fatalf("bind test store: %v", err)
	}
}
