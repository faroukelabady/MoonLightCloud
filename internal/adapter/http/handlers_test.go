package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoonLightSoftware/MoonLightCloud/internal/apperr"
	"github.com/MoonLightSoftware/MoonLightCloud/internal/auth"
)

func TestLive(t *testing.T) {
	h := Health{LiveCheck: func() bool { return true }}
	req := httptest.NewRequest("GET", "/health/live", nil)
	rec := httptest.NewRecorder()
	h.ServeLive(rec, req)
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["status"] != "ok" {
		t.Fatalf("bad body: %s", rec.Body.String())
	}
}

func TestReadyHealthyAndSick(t *testing.T) {
	ok := Health{ReadyCheck: func(context.Context) error { return nil }}
	rec := httptest.NewRecorder()
	ok.ServeReady(rec, httptest.NewRequest("GET", "/health/ready", nil))
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	sick := Health{ReadyCheck: func(context.Context) error {
		return apperr.New(apperr.Unavailable, "postgres unreachable")
	}}
	rec = httptest.NewRecorder()
	sick.ServeReady(rec, httptest.NewRequest("GET", "/health/ready", nil))
	if rec.Code != 503 {
		t.Fatalf("want 503, got %d", rec.Code)
	}
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Error.Code != "UNAVAILABLE" {
		t.Fatalf("bad envelope: %s", rec.Body.String())
	}
}

func TestVersionNoSecrets(t *testing.T) {
	v := Version{App: "moonlight-cloud", Version: "1A", Commit: "abc", BuildTime: "now"}
	rec := httptest.NewRecorder()
	v.Handler(rec, httptest.NewRequest("GET", "/version", nil))
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	for _, k := range []string{"app", "version", "commit", "build_time"} {
		if body[k] == "" {
			t.Fatalf("missing %s in %v", k, body)
		}
	}
}

func TestErrorEnvelopeNeverLeaks(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, httptest.NewRequest("GET", "/", nil), errors.New("sql: SELECT * FROM devices password=hunter2 /etc/passwd"))
	var env Envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if rec.Code != 500 || env.Error.Code != "INTERNAL" || env.Error.Message != "internal error" {
		t.Fatalf("leak or bad mapping: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRequestIDPropagation(t *testing.T) {
	var seen string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = RequestID(r) })
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(RequestIDHeader, "corr-123")
	RequestIDMiddleware(next).ServeHTTP(rec, req)
	if seen != "corr-123" || rec.Header().Get(RequestIDHeader) != "corr-123" {
		t.Fatalf("request id not propagated: %q header %q", seen, rec.Header().Get(RequestIDHeader))
	}
	// Unbounded client values are replaced, never trusted.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set(RequestIDHeader, string(make([]byte, 200)))
	RequestIDMiddleware(next).ServeHTTP(rec2, req2)
	if seen == string(make([]byte, 200)) || seen == "" {
		t.Fatal("oversize client request id must be replaced")
	}
}

func TestDeviceAuthEndToEnd(t *testing.T) {
	repo := &memRepo{m: map[string]auth.Device{}}
	svc := deviceSvcForTest(repo)
	ctx := context.Background()
	p, err := svc.Create(ctx, "shop-dev")
	if err != nil {
		t.Fatal(err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dev, ok := DeviceOf(r)
		if !ok {
			t.Error("device missing in context")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"device_id": dev.ID})
	})
	h := DeviceAuth(svc)(next)

	good := httptest.NewRequest("GET", "/api/v1/device/ping", nil)
	good.Header.Set("Authorization", "Bearer "+p.Device.ID+"."+p.RawSecret)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, good)
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	for name, header := range map[string]string{
		"missing":       "",
		"bad scheme":    "Basic abc",
		"invalid":       "Bearer " + p.Device.ID + ".wrongsecret",
		"unknown":       "Bearer 22222222-2222-7222-8222-222222222222." + p.RawSecret,
		"query exfil ?": "Bearer " + p.Device.ID + ".",
	} {
		req := httptest.NewRequest("GET", "/api/v1/device/ping", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatalf("%s: want 401, got %d", name, rec.Code)
		}
		var env Envelope
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		if env.Error.Code != "UNAUTHORIZED" {
			t.Fatalf("%s: want UNAUTHORIZED envelope, got %s", name, rec.Body.String())
		}
	}

	if err := svc.Revoke(ctx, p.Device.ID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/device/ping", nil)
	req.Header.Set("Authorization", "Bearer "+p.Device.ID+"."+p.RawSecret)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("revoked: want 401, got %d", rec.Code)
	}
}
