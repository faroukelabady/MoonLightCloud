package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/sync"
)

// memSyncRepo is an in-memory ingestion repo for handler tests.
type memSyncRepo struct {
	committed map[string]bool
}

func (m *memSyncRepo) IngestBatch(_ context.Context, _, _ string, events []sync.Event, _ time.Time) ([]sync.EventResult, error) {
	out := make([]sync.EventResult, 0, len(events))
	for _, e := range events {
		if m.committed[e.EventID] {
			out = append(out, sync.EventResult{EventID: e.EventID, Status: sync.StatusAlreadyAccepted})
			continue
		}
		m.committed[e.EventID] = true
		out = append(out, sync.EventResult{EventID: e.EventID, Status: sync.StatusAccepted})
	}
	return out, nil
}

func syncTestSetup(t *testing.T) (http.Handler, string) {
	t.Helper()
	authSvc := deviceSvcForTest(newMemRepo())
	p, err := authSvc.Create(context.Background(), "shop-dev")
	if err != nil {
		t.Fatal(err)
	}
	token := p.Device.ID + "." + p.Credential.ID + "." + p.RawSecret
	syncSvc := sync.NewService(&memSyncRepo{committed: map[string]bool{}},
		clock.Fixed{T: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)})
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/sync/batches", DeviceAuth(authSvc)(SyncBatch(syncSvc, nil)))
	mux.Handle("GET /api/v1/sync/capabilities", DeviceAuth(authSvc)(SyncCapabilities(syncSvc)))
	return mux, token
}

func postBatch(t *testing.T, h http.Handler, token, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/sync/batches", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSyncBatchAccepted(t *testing.T) {
	h, token := syncTestSetup(t)
	body := `{"events":[{"event_id":"22222222-2222-7222-8222-222222222222","event_type":"system.test.v1","occurred_at":"2026-09-19T10:20:30Z","payload":{"ping":1}}]}`
	rec := postBatch(t, h, token, body, "application/json")
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var res sync.BatchResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Events) != 1 || res.Events[0].Status != sync.StatusAccepted {
		t.Fatalf("bad result: %+v", res)
	}
	// Retry → already_accepted.
	rec = postBatch(t, h, token, body, "application/json; charset=utf-8")
	var res2 sync.BatchResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res2)
	if res2.Events[0].Status != sync.StatusAlreadyAccepted {
		t.Fatalf("want already_accepted, got %+v", res2)
	}
}

func TestSyncBatchErrors(t *testing.T) {
	h, token := syncTestSetup(t)
	good := `{"event_id":"22222222-2222-7222-8222-222222222222","event_type":"system.test.v1","occurred_at":"2026-09-19T10:20:30Z","payload":{}}`
	cases := []struct {
		name        string
		token       string
		body        string
		contentType string
		want        int
	}{
		{"unauthenticated", "bad", `{"events":[}`, "application/json", 401},
		{"wrong media", token, `{"events":[]}`, "text/plain", 415},
		{"missing media", token, `{"events":[]}`, "", 415},
		{"malformed", token, `{"events":[]}`, "application/json", 400},
		{"unsupported type", token, `{"events":[{"event_id":"22222222-2222-7222-8222-222222222222","event_type":"sale.finalized.v1","occurred_at":"2026-09-19T10:20:30Z","payload":{}}]}`, "application/json", 422},
		{"mismatch device", token, `{"events":[` + strings.Replace(good, `"payload"`, `"device_id":"99999999-9999-7999-8999-999999999999","payload"`, 1) + `]}`, "application/json", 403},
	}
	_ = good
	for _, tc := range cases {
		rec := postBatch(t, h, tc.token, tc.body, tc.contentType)
		if rec.Code != tc.want {
			t.Fatalf("%s: want %d, got %d: %s", tc.name, tc.want, rec.Code, rec.Body.String())
		}
		var env Envelope
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		if env.Error.Code == "" {
			t.Fatalf("%s: want error envelope", tc.name)
		}
	}
}

func TestSyncBodyTooLarge(t *testing.T) {
	h, token := syncTestSetup(t)
	// Body over 8 MiB must be rejected even with valid framing.
	big := `{"events":[{"event_id":"22222222-2222-7222-8222-222222222222","event_type":"system.test.v1","occurred_at":"2026-09-19T10:20:30Z","payload":{"pad":"`
	big += strings.Repeat("x", 8*1024*1024) + `"}}]}`
	rec := postBatch(t, h, token, big, "application/json")
	if rec.Code != 413 {
		t.Fatalf("want 413, got %d", rec.Code)
	}
}

func TestSyncCapabilitiesAdvertisesSale(t *testing.T) {
	// Mirror app wiring: registration advertises the type + limits.
	sync.RegisterEventType("sale.finalized.v1", func(json.RawMessage) error { return nil })
	h, token := syncTestSetup(t)
	req := httptest.NewRequest("GET", "/api/v1/sync/capabilities", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var caps sync.Capabilities
	if err := json.Unmarshal(rec.Body.Bytes(), &caps); err != nil {
		t.Fatal(err)
	}
	if caps.APIVersion != "v1" || len(caps.SupportedEvents) == 0 || caps.ServerTime == "" {
		t.Fatalf("bad capabilities: %+v", caps)
	}
	found := false
	for _, e := range caps.SupportedEvents {
		if e == "sale.finalized.v1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("capabilities must advertise sale.finalized.v1: %+v", caps)
	}
	if caps.MaxPayloadBytes != 262144 || caps.MaxBatchEvents != 100 || caps.MaxBodyBytes != 8*1024*1024 {
		t.Fatalf("capabilities must report finalized limits: %+v", caps)
	}
}
