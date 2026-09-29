package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/devicecontrol"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

func apperrNewNotFound() error { return apperr.New(apperr.NotFound, "DEVICE_COMMAND_NOT_FOUND") }

type stubLister struct {
	devices []auth.Device
}

func (s stubLister) ListMetadata(context.Context) ([]auth.Device, error) {
	return s.devices, nil
}

func dashSetup(devices []auth.Device) *DashboardDeviceHandlers {
	store := newMemCtlStore()
	n := 0
	svc := devicecontrol.NewService(store, activeStub{map[string]bool{"dev-A": true, "dev-B": true}}, func() string {
		n++
		return "dash-cmd-" + string(rune('0'+n))
	}, time.Minute, time.Now)
	return &DashboardDeviceHandlers{Svc: svc, Auth: stubLister{devices}, OnlineWindow: time.Minute}
}

func TestDashboardDevicesConnectivity(t *testing.T) {
	h := dashSetup([]auth.Device{{ID: "dev-A", Name: "shop-pc", Status: "active"}, {ID: "dev-B", Name: "old", Status: "revoked"}})
	ctx := context.Background()
	if _, _, err := h.Svc.CreateSyncRequest(ctx, "dev-A", "k1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.Svc.Poll(ctx, "dev-A"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/dashboard/devices", nil)
	rec := httptest.NewRecorder()
	h.Devices(rec, req)
	if rec.Code != 200 {
		t.Fatalf("devices: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Devices []struct {
			DeviceID     string `json:"device_id"`
			Connectivity string `json:"connectivity"`
			Active       *struct {
				Status string `json:"status"`
			} `json:"active_command"`
		} `json:"devices"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Devices) != 2 {
		t.Fatalf("two devices: %s", rec.Body.String())
	}
	byID := map[string]string{}
	for _, d := range body.Devices {
		byID[d.DeviceID] = d.Connectivity
	}
	if byID["dev-A"] != "ONLINE" || byID["dev-B"] != "NEVER_SEEN" {
		t.Fatalf("connectivity: %v", byID)
	}
}

func TestDashboardSyncRequestIdempotent(t *testing.T) {
	h := dashSetup([]auth.Device{{ID: "dev-A", Status: "active"}})
	mk := func(key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/dashboard/devices/dev-A/sync-requests", nil)
		req.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		h.CreateSyncRequest(rec, req)
		return rec
	}
	first := mk("idem-1")
	if first.Code != 201 {
		t.Fatalf("create: %d %s", first.Code, first.Body.String())
	}
	var f struct {
		Command struct {
			ID string `json:"id"`
		} `json:"command"`
		Created bool `json:"created"`
	}
	_ = json.Unmarshal(first.Body.Bytes(), &f)
	if !f.Created {
		t.Fatal("first must be created")
	}
	second := mk("idem-1")
	var s struct {
		Command struct {
			ID string `json:"id"`
		} `json:"command"`
		Created bool `json:"created"`
	}
	_ = json.Unmarshal(second.Body.Bytes(), &s)
	if second.Code != 200 || s.Created || s.Command.ID != f.Command.ID {
		t.Fatalf("idempotent retry: %d %s", second.Code, second.Body.String())
	}
	third := mk("idem-2")
	if third.Code != 409 {
		t.Fatalf("second active must be 409: %d %s", third.Code, third.Body.String())
	}
	var env Envelope
	_ = json.Unmarshal(third.Body.Bytes(), &env)
	if env.Error.Message != "DEVICE_SYNC_ALREADY_ACTIVE" {
		t.Fatalf("machine code: %s", third.Body.String())
	}
}

func TestDashboardSyncRequestOfflineQueued(t *testing.T) {
	h := dashSetup([]auth.Device{{ID: "dev-A", Status: "active"}})
	req := httptest.NewRequest("POST", "/api/v1/dashboard/devices/dev-A/sync-requests", nil)
	req.Header.Set("Idempotency-Key", "offline-1")
	rec := httptest.NewRecorder()
	h.CreateSyncRequest(rec, req)
	if rec.Code != 201 {
		t.Fatalf("offline device still queues: %d", rec.Code)
	}
	// No poll happened: device stays NEVER_SEEN while command is pending.
	req2 := httptest.NewRequest("GET", "/api/v1/dashboard/devices", nil)
	rec2 := httptest.NewRecorder()
	h.Devices(rec2, req2)
	var body struct {
		Devices []struct {
			Connectivity string `json:"connectivity"`
			Active       *struct {
				Status string `json:"status"`
			} `json:"active_command"`
		} `json:"devices"`
	}
	_ = json.Unmarshal(rec2.Body.Bytes(), &body)
	if body.Devices[0].Connectivity != "NEVER_SEEN" || body.Devices[0].Active == nil {
		t.Fatalf("queued while never seen: %s", rec2.Body.String())
	}
}

// F07: dashboard list/create shapes match the documented contract.
func TestDashboardContractShapes(t *testing.T) {
	h := dashSetup([]auth.Device{{ID: "dev-A", Name: "shop", Status: "active"}})
	ctx := context.Background()
	if _, _, err := h.Svc.CreateSyncRequest(ctx, "dev-A", "shape-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.Svc.Poll(ctx, "dev-A"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/dashboard/devices", nil)
	rec := httptest.NewRecorder()
	h.Devices(rec, req)
	var list struct {
		Devices []struct {
			DeviceID     string  `json:"device_id"`
			Lifecycle    string  `json:"lifecycle"`
			Connectivity string  `json:"connectivity"`
			LastSeenAt   *string `json:"last_seen_at"`
			Active       *struct {
				ID     string `json:"id"`
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"active_command"`
			Recent []any `json:"recent_commands"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Devices) != 1 || list.Devices[0].Connectivity == "" || list.Devices[0].Active == nil {
		t.Fatalf("device list shape: %s", rec.Body.String())
	}
	// Missing Idempotency-Key is rejected, not defaulted.
	bad := httptest.NewRequest("POST", "/api/v1/dashboard/devices/dev-A/sync-requests", nil)
	brec := httptest.NewRecorder()
	h.CreateSyncRequest(brec, bad)
	if brec.Code != 400 {
		t.Fatalf("missing key must be 400: %d", brec.Code)
	}
}

type errDeviceStatus struct{ err error }

func (e errDeviceStatus) IsActive(context.Context, string) (bool, error) {
	return false, e.err
}

// F07 key matrix: missing/invalid/oversized → 400 with the specific
// bounded diagnostic (never the key); valid accepted.
func TestDashboardIdempotencyKeyMatrix(t *testing.T) {
	h := dashSetup([]auth.Device{{ID: "dev-A", Status: "active"}})
	cases := []struct {
		name   string
		key    string
		setKey bool
		want   int
	}{
		{"missing key", "", false, 400},
		{"empty key", "", true, 400},
		{"invalid characters", "key with spaces!", true, 400},
		{"oversized key", string(make([]byte, 0)) + string(bytes.Repeat([]byte("k"), 129)), true, 400},
		{"valid key", "r1-001_valid:key.~", true, 201},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/api/v1/dashboard/devices/dev-A/sync-requests", nil)
			if tc.setKey {
				req.Header.Set("Idempotency-Key", tc.key)
			}
			rec := httptest.NewRecorder()
			h.CreateSyncRequest(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("want %d, got %d (%s)", tc.want, rec.Code, rec.Body.String())
			}
			if tc.want == 400 {
				var env Envelope
				_ = json.Unmarshal(rec.Body.Bytes(), &env)
				if env.Error.Message != "DEVICE_COMMAND_INVALID_IDEMPOTENCY_KEY" {
					t.Fatalf("diagnostic: %s", rec.Body.String())
				}
				if bytes.Contains(rec.Body.Bytes(), []byte(tc.key)) && tc.key != "" {
					t.Fatal("key must never be echoed")
				}
			}
		})
	}
}

// F07: unknown device → 404; revoked device keeps existing refusal.
func TestDashboardUnknownAndRevokedDevice(t *testing.T) {
	store := newMemCtlStore()
	n := 0
	unknown := devicecontrol.NewService(store, errDeviceStatus{err: apperrNewNotFound()}, func() string {
		n++
		return "k-cmd"
	}, time.Minute, time.Now)
	h := &DashboardDeviceHandlers{Svc: unknown, Auth: stubLister{}, OnlineWindow: time.Minute}
	req := httptest.NewRequest("POST", "/api/v1/dashboard/devices/dev-ghost/sync-requests", nil)
	req.Header.Set("Idempotency-Key", "k1")
	rec := httptest.NewRecorder()
	h.CreateSyncRequest(rec, req)
	if rec.Code != 404 {
		t.Fatalf("unknown device must be 404: %d %s", rec.Code, rec.Body.String())
	}
	var env Envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error.Message != "DEVICE_COMMAND_NOT_FOUND" {
		t.Fatalf("diagnostic: %s", rec.Body.String())
	}

	revoked := dashSetup([]auth.Device{{ID: "dev-R", Status: "revoked"}})
	rreq := httptest.NewRequest("POST", "/api/v1/dashboard/devices/dev-R/sync-requests", nil)
	rreq.Header.Set("Idempotency-Key", "k1")
	rrec := httptest.NewRecorder()
	revoked.CreateSyncRequest(rrec, rreq)
	if rrec.Code != 400 {
		t.Fatalf("revoked keeps refusal: %d", rrec.Code)
	}
	var renv Envelope
	_ = json.Unmarshal(rrec.Body.Bytes(), &renv)
	if renv.Error.Message != "DEVICE_NOT_ACTIVE" {
		t.Fatalf("diagnostic: %s", rrec.Body.String())
	}
}

// F11: dashboard JSON exposes no credential material of any kind.
func TestDashboardNoCredentialExposure(t *testing.T) {
	h := dashSetup([]auth.Device{
		{ID: "dev-A", Name: "shop", Status: "active"},
		{ID: "dev-R", Name: "old", Status: "revoked"},
	})
	ctx := context.Background()
	if _, _, err := h.Svc.CreateSyncRequest(ctx, "dev-A", "exp-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.Svc.Poll(ctx, "dev-A"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/dashboard/devices", nil)
	rec := httptest.NewRecorder()
	h.Devices(rec, req)
	body := rec.Body.String()
	for _, leak := range []string{"credential", "verifier", "secret", "hash", "Bearer", "pepper", "salt"} {
		if bytes.Contains(bytes.ToLower([]byte(body)), []byte(leak)) {
			t.Fatalf("credential material %q in dashboard output", leak)
		}
	}
	var list struct {
		Devices []struct {
			DeviceID     string `json:"device_id"`
			Lifecycle    string `json:"lifecycle"`
			Connectivity string `json:"connectivity"`
		} `json:"devices"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Devices) != 2 {
		t.Fatalf("revoked device stays visible: %s", body)
	}
}

// L01: Sync Now error contract. Malformed and unknown device IDs return
// 404; missing/invalid/oversized keys return 400 with the specific
// diagnostic; revoked devices keep 400 DEVICE_NOT_ACTIVE.
func TestDashboardSyncRequestErrorContract(t *testing.T) {
	url := testutil.Isolated(t)
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := postgres.NewDevices(pool, 5*time.Second)
	svc := devicecontrol.NewService(store, lifecycleStub{active: map[string]bool{"dev-A": true}}, ids.System{}.New, time.Minute, time.Now)
	h := &DashboardDeviceHandlers{Svc: svc, Auth: stubLister{}, OnlineWindow: time.Minute}
	post := func(path, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, nil)
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		h.CreateSyncRequest(rec, req)
		return rec
	}
	cases := []struct {
		name string
		path string
		key  string
		want int
		msg  string
	}{
		{"missing key", "/api/v1/dashboard/devices/dev-A/sync-requests", "", 400, "DEVICE_COMMAND_INVALID_IDEMPOTENCY_KEY"},
		{"invalid chars", "/api/v1/dashboard/devices/dev-A/sync-requests", "bad key!", 400, "DEVICE_COMMAND_INVALID_IDEMPOTENCY_KEY"},
		{"oversized key", "/api/v1/dashboard/devices/dev-A/sync-requests", strings.Repeat("k", 129), 400, "DEVICE_COMMAND_INVALID_IDEMPOTENCY_KEY"},
		{"malformed device id", "/api/v1/dashboard/devices/!!!/sync-requests", "k1", 404, "DEVICE_COMMAND_NOT_FOUND"},
		{"unknown device", "/api/v1/dashboard/devices/11111111-1111-4111-8111-111111111111/sync-requests", "k1", 404, "DEVICE_COMMAND_NOT_FOUND"},
		{"revoked device", "/api/v1/dashboard/devices/dev-B/sync-requests", "k1", 400, "DEVICE_NOT_ACTIVE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := post(tc.path, tc.key)
			if rec.Code != tc.want {
				t.Fatalf("want %d, got %d (%s)", tc.want, rec.Code, rec.Body.String())
			}
			var env struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &env)
			if env.Error.Message != tc.msg {
				t.Fatalf("diagnostic: %s", rec.Body.String())
			}
		})
	}
}

type lifecycleStub struct {
	active map[string]bool
}

func (s lifecycleStub) IsActive(_ context.Context, id string) (bool, error) {
	if s.active[id] {
		return true, nil
	}
	if id == "dev-B" {
		// Revoked device: known but inactive.
		return false, nil
	}
	// Unknown or malformed IDs behave like the auth lookup: not found.
	return false, apperr.New(apperr.NotFound, "DEVICE_COMMAND_NOT_FOUND")
}
