package http

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/operations"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

type opsHTTPEnv struct {
	handlers *OperationsHandlers
	svc      *operations.Service
	store    postgres.Devices
	pool     *pgxpool.Pool
	auth     auth.Service
}

func openOpsHTTP(t *testing.T) *opsHTTPEnv {
	t.Helper()
	url := testutil.Isolated(t)
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := postgres.NewDevices(pool, 5*time.Second)
	pepper := make([]byte, 32)
	for i := range pepper {
		pepper[i] = 'o'
	}
	h, err := auth.NewHasher(pepper)
	if err != nil {
		t.Fatal(err)
	}
	authSvc := auth.NewService(store, h, "", 1, clock.System{}, ids.System{})
	svc := operations.NewService(store, ids.System{}.New, time.Now)
	reader := operations.NewOpsReader(store, svc, operations.NewMetrics())
	reader.SetManualGuard(5 * time.Minute)
	return &opsHTTPEnv{
		handlers: &OperationsHandlers{Svc: svc, Ops: reader},
		svc:      svc, store: store, pool: pool, auth: authSvc,
	}
}

func (e *opsHTTPEnv) device(t *testing.T, name string) string {
	t.Helper()
	prov, err := e.auth.Create(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return prov.Device.ID
}

func (e *opsHTTPEnv) incident(t *testing.T, rule, stype, sid, key string) operations.Incident {
	t.Helper()
	ctx := context.Background()
	if key != "" {
		in, _, err := e.svc.OpenEvent(ctx, rule, stype, sid, key)
		if err != nil {
			t.Fatal(err)
		}
		return in
	}
	in, _, err := e.svc.OpenStateful(ctx, rule, stype, sid, "")
	if err != nil {
		t.Fatal(err)
	}
	return in
}

// List is empty by default and filters/paginates deterministically.
func TestOpsHTTPList(t *testing.T) {
	env := openOpsHTTP(t)
	rec := httptest.NewRecorder()
	env.handlers.Incidents(rec, httptest.NewRequest("GET", "/api/v1/dashboard/operations/incidents", nil))
	if rec.Code != 200 {
		t.Fatalf("empty list: %d %s", rec.Code, rec.Body.String())
	}
	var empty struct {
		Incidents []any  `json:"incidents"`
		Next      string `json:"next_cursor"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &empty)
	if len(empty.Incidents) != 0 || empty.Next != "" {
		t.Fatalf("empty page: %s", rec.Body.String())
	}
	dev := env.device(t, "web-dev")
	env.incident(t, operations.RuleDeviceSyncFailed, operations.SubjectSyncCommand, "cmd-1", "sync-command:cmd-1")
	env.incident(t, operations.RuleNotificationAmb, operations.SubjectNotification, "n-1", "notification-ambiguous:n-1")
	_ = dev
	filtered := httptest.NewRecorder()
	env.handlers.Incidents(filtered, httptest.NewRequest("GET", "/api/v1/dashboard/operations/incidents?severity=urgent", nil))
	var urgent struct {
		Incidents []struct {
			Rule     string `json:"rule"`
			Severity string `json:"severity"`
		} `json:"incidents"`
	}
	_ = json.Unmarshal(filtered.Body.Bytes(), &urgent)
	if len(urgent.Incidents) != 2 {
		t.Fatalf("urgent filter: %s", filtered.Body.String())
	}
	bad := httptest.NewRecorder()
	env.handlers.Incidents(bad, httptest.NewRequest("GET", "/api/v1/dashboard/operations/incidents?state=bogus", nil))
	if bad.Code != 400 {
		t.Fatalf("bad state: %d", bad.Code)
	}
	limited := httptest.NewRecorder()
	env.handlers.Incidents(limited, httptest.NewRequest("GET", "/api/v1/dashboard/operations/incidents?limit=1", nil))
	var page struct {
		Incidents []struct {
			ID string `json:"id"`
		} `json:"incidents"`
		Next string `json:"next_cursor"`
	}
	_ = json.Unmarshal(limited.Body.Bytes(), &page)
	if len(page.Incidents) != 1 || page.Next == "" {
		t.Fatalf("page one: %s", limited.Body.String())
	}
	second := httptest.NewRecorder()
	env.handlers.Incidents(second, httptest.NewRequest("GET", "/api/v1/dashboard/operations/incidents?limit=1&cursor="+page.Next, nil))
	var page2 struct {
		Incidents []struct {
			ID string `json:"id"`
		} `json:"incidents"`
		Next string `json:"next_cursor"`
	}
	_ = json.Unmarshal(second.Body.Bytes(), &page2)
	if len(page2.Incidents) != 1 || page2.Incidents[0].ID == page.Incidents[0].ID || page2.Next != "" {
		t.Fatalf("page two: %s", second.Body.String())
	}
}

// Acknowledge is idempotent; resolved incidents read back unchanged.
func TestOpsHTTPAcknowledge(t *testing.T) {
	env := openOpsHTTP(t)
	in := env.incident(t, operations.RuleDeviceOffline, operations.SubjectDevice, "d-ack", "")
	rec := httptest.NewRecorder()
	env.handlers.Acknowledge(rec, httptest.NewRequest("POST", "/api/v1/dashboard/operations/incidents/"+in.ID+"/acknowledge", nil))
	if rec.Code != 200 {
		t.Fatalf("ack: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Incident struct {
			State string `json:"state"`
		} `json:"incident"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Incident.State != "acknowledged" {
		t.Fatalf("state: %s", rec.Body.String())
	}
	again := httptest.NewRecorder()
	env.handlers.Acknowledge(again, httptest.NewRequest("POST", "/api/v1/dashboard/operations/incidents/"+in.ID+"/acknowledge", nil))
	if again.Code != 200 {
		t.Fatalf("re-ack: %d", again.Code)
	}
	missing := httptest.NewRecorder()
	env.handlers.Acknowledge(missing, httptest.NewRequest("POST", "/api/v1/dashboard/operations/incidents/00000000-0000-4000-8000-000000000000/acknowledge", nil))
	if missing.Code != 404 {
		t.Fatalf("unknown: %d", missing.Code)
	}
}

// Resolve closes events; active stateful conditions conflict.
func TestOpsHTTPResolve(t *testing.T) {
	env := openOpsHTTP(t)
	ctx := context.Background()
	event := env.incident(t, operations.RuleDeviceSyncFailed, operations.SubjectSyncCommand, "cmd-r", "sync-command:cmd-r")
	rec := httptest.NewRecorder()
	env.handlers.Resolve(rec, httptest.NewRequest("POST", "/api/v1/dashboard/operations/incidents/"+event.ID+"/resolve", nil))
	if rec.Code != 200 {
		t.Fatalf("event resolve: %d %s", rec.Code, rec.Body.String())
	}
	dev := env.device(t, "still-off")
	if err := env.store.TouchSeen(ctx, dev, time.Now().UTC().Add(-time.Hour), true); err != nil {
		t.Fatal(err)
	}
	active := env.incident(t, operations.RuleDeviceOffline, operations.SubjectDevice, dev, "")
	conflict := httptest.NewRecorder()
	env.handlers.Resolve(conflict, httptest.NewRequest("POST", "/api/v1/dashboard/operations/incidents/"+active.ID+"/resolve", nil))
	if conflict.Code != 409 {
		t.Fatalf("active stateful must conflict: %d %s", conflict.Code, conflict.Body.String())
	}
	var env2 struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(conflict.Body.Bytes(), &env2)
	if env2.Error.Message != "CONDITION_STILL_ACTIVE" {
		t.Fatalf("code: %s", conflict.Body.String())
	}
}

// Detail exposes masked deliveries and recovery state, never recipients.
func TestOpsHTTPDetailPrivacy(t *testing.T) {
	env := openOpsHTTP(t)
	ctx := context.Background()
	rec, err := env.store.CreateOpsRecipient(ctx, ids.System{}.New(), "ops", "whatsapp-main", "201012345678", "ar", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	in := env.incident(t, operations.RuleNotificationAmb, operations.SubjectNotification, "n-priv", "notification-ambiguous:n-priv")
	if _, err := env.store.CreateDelivery(ctx, operations.Delivery{
		ID: ids.System{}.New(), IncidentID: in.ID, RecipientID: rec.ID, Event: operations.EventOpened,
		ProviderKey: "whatsapp-main", RecipientSnapshot: "201012345678", Locale: "ar",
		TemplateKey: operations.TemplateOpen, Body: "body", Fingerprint: []byte{1},
		NotificationKey: operations.AlertIdempotencyKey(in.ID, operations.EventOpened, ids.System{}.New()),
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	got := httptest.NewRecorder()
	env.handlers.IncidentDetail(got, httptest.NewRequest("GET", "/api/v1/dashboard/operations/incidents/"+in.ID, nil))
	if got.Code != 200 {
		t.Fatalf("detail: %d %s", got.Code, got.Body.String())
	}
	body := got.Body.String()
	if strings.Contains(body, "201012345678") {
		t.Fatal("full recipient must never appear")
	}
	var detail struct {
		Deliveries []struct {
			RecipientMasked string `json:"recipient_masked"`
			Recipient       string `json:"recipient"`
		} `json:"deliveries"`
	}
	_ = json.Unmarshal([]byte(body), &detail)
	if len(detail.Deliveries) != 1 || detail.Deliveries[0].RecipientMasked != "...5678" || detail.Deliveries[0].Recipient != "" {
		t.Fatalf("masked delivery: %s", body)
	}
}

// Summary carries counters and the batched device rollup.
func TestOpsHTTPSummary(t *testing.T) {
	env := openOpsHTTP(t)
	ctx := context.Background()
	dev := env.device(t, "sum-dev")
	if err := env.store.TouchSeen(ctx, dev, time.Now().UTC().Add(-time.Hour), true); err != nil {
		t.Fatal(err)
	}
	env.incident(t, operations.RuleDeviceOffline, operations.SubjectDevice, dev, "")
	rec := httptest.NewRecorder()
	env.handlers.Summary(rec, httptest.NewRequest("GET", "/api/v1/dashboard/operations/summary", nil))
	if rec.Code != 200 {
		t.Fatalf("summary: %d", rec.Code)
	}
	var summary struct {
		Devices []struct {
			DeviceID    string `json:"device_id"`
			OpenCount   int    `json:"open_count"`
			MaxSeverity string `json:"max_severity"`
		} `json:"devices"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &summary)
	if len(summary.Devices) != 1 || summary.Devices[0].OpenCount != 1 || summary.Devices[0].MaxSeverity != "warning" {
		t.Fatalf("rollup: %s", rec.Body.String())
	}
	drec := httptest.NewRecorder()
	env.handlers.DeviceSummary(drec, httptest.NewRequest("GET", "/api/v1/dashboard/operations/devices/summary", nil))
	if drec.Code != 200 {
		t.Fatalf("device summary: %d", drec.Code)
	}
}
