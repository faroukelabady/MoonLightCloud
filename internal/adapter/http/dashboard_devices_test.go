package http

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/devicecontrol"
)

type stubLister struct {
	devices []auth.Device
}

func (s stubLister) List(context.Context) ([]auth.Device, map[string][]auth.Credential, error) {
	return s.devices, nil, nil
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
