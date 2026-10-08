package app

// Phase 18 HTTP authorization boundary for the release registry, fleet
// control and device update channel (prompt §64-§65, §73).

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/release"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	"github.com/google/uuid"
)

type updateHTTP struct {
	t      *testing.T
	a      *App
	srv    *httptest.Server
	client *http.Client
	priv   ed25519.PrivateKey
}

func newUpdateHTTP(t *testing.T) *updateHTTP {
	t.Helper()
	url := testutil.Isolated(t)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, url)
	cfg.ReleaseTrustedPublicKeys = base64.StdEncoding.EncodeToString(pub)
	a, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	srv := httptest.NewServer(a.Handler)
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return &updateHTTP{t: t, a: a, srv: srv, client: &http.Client{Jar: jar}, priv: priv}
}

func (h *updateHTTP) do(method, path string, body any, header map[string]string, client *http.Client) (int, string) {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, reader)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(out)
}

func (h *updateHTTP) login() {
	h.t.Helper()
	if code, body := h.do("POST", "/api/v1/dashboard/auth/login", map[string]string{"username": "op", "password": "op-test-password"}, nil, h.client); code != 200 {
		h.t.Fatalf("login %d %s", code, body)
	}
}

func (h *updateHTTP) bound(name, storeID string) auth.Provisioned {
	h.t.Helper()
	ctx := context.Background()
	p, err := h.a.Devices.Create(ctx, name)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.a.Pool.Exec(ctx, `INSERT INTO stores (id, display_name, timezone) VALUES ($1, 'S', 'Africa/Cairo') ON CONFLICT DO NOTHING`, storeID); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.a.Pool.Exec(ctx, `INSERT INTO device_store_bindings (device_id, store_id) VALUES ($1, $2)`, p.Device.ID, storeID); err != nil {
		h.t.Fatal(err)
	}
	return p
}

func bearer(p auth.Provisioned) map[string]string {
	return map[string]string{"Authorization": "Bearer " + p.Device.ID + "." + p.Credential.ID + "." + p.RawSecret}
}

func TestUpdateRoutesAuthorizationBoundary(t *testing.T) {
	h := newUpdateHTTP(t)
	storeA, storeB := uuid.NewString(), uuid.NewString()
	devA := h.bound("upd-a", storeA)
	devB := h.bound("upd-b", storeB)

	// Unauthenticated operator routes → 401.
	for _, p := range []string{"/api/v1/dashboard/releases", "/api/v1/dashboard/fleet", "/api/v1/dashboard/rollouts", "/api/v1/dashboard/update-audit"} {
		if code, _ := h.do("GET", p, nil, nil, nil); code != 401 {
			t.Fatalf("%s unauthenticated: %d", p, code)
		}
	}
	// Device credential on operator routes → rejected.
	if code, _ := h.do("GET", "/api/v1/dashboard/fleet", nil, bearer(devA), nil); code != 401 {
		t.Fatalf("device credential accepted on operator route: %d", code)
	}
	// No device credential on device routes → 401; operator session too.
	if code, _ := h.do("GET", "/api/v1/device-control/update/command", nil, nil, nil); code != 401 {
		t.Fatalf("unauthenticated device poll: %d", code)
	}
	h.login()
	if code, _ := h.do("GET", "/api/v1/device-control/update/command", nil, nil, h.client); code != 401 {
		t.Fatalf("operator session accepted on device route: %d", code)
	}

	// Cross-origin operator mutation refused even with a session.
	m := release.Manifest{ReleaseSequence: 11, Version: "1.1.0", BuildCommit: strings.Repeat("b", 40),
		Artifacts: []release.Artifact{{OS: "linux", Arch: "amd64", Package: "tar.gz", FileName: "r.tar.gz", Size: 10, SHA256: strings.Repeat("c", 64)}}}
	env, _, err := release.Sign(m, h.priv)
	if err != nil {
		t.Fatal(err)
	}
	importBody := map[string]any{"envelope": json.RawMessage(env), "artifact_urls": map[string]string{"r.tar.gz": "https://artifacts.example.test/r.tar.gz"}}
	if code, _ := h.do("POST", "/api/v1/dashboard/releases", importBody, map[string]string{"Origin": "https://evil.example"}, h.client); code != 403 {
		t.Fatalf("cross-origin import: %d", code)
	}
	// Manually invented identity fields are not part of the API.
	if code, _ := h.do("POST", "/api/v1/dashboard/releases", map[string]any{"version": "9.9", "sha256": strings.Repeat("0", 64)}, map[string]string{"Origin": h.srv.URL}, h.client); code != 400 {
		t.Fatalf("invented release accepted: %d", code)
	}
	code, body := h.do("POST", "/api/v1/dashboard/releases", importBody, map[string]string{"Origin": h.srv.URL}, h.client)
	if code != 201 {
		t.Fatalf("import %d %s", code, body)
	}
	var rel struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &rel)
	code, body = h.do("POST", "/api/v1/dashboard/rollouts", map[string]any{"release_id": rel.ID, "scope": "STORE", "store_id": storeA,
		"mode": "MANDATORY", "percentage": 100}, map[string]string{"Origin": h.srv.URL}, h.client)
	if code != 201 {
		t.Fatalf("rollout %d %s", code, body)
	}
	var ro struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &ro)
	if code, body := h.do("POST", "/api/v1/dashboard/rollouts/"+ro.ID+"/start", nil, map[string]string{"Origin": h.srv.URL}, h.client); code != 200 {
		t.Fatalf("start %d %s", code, body)
	}
	status := map[string]any{"version": "1.0.0", "build_commit": strings.Repeat("a", 40), "release_sequence": 10, "os": "linux", "arch": "amd64",
		"updater_protocol": 1, "updater_capable": true, "update_state": "IDLE"}
	for _, d := range []auth.Provisioned{devA, devB} {
		if code, body := h.do("POST", "/api/v1/device-control/update/status", status, bearer(d), nil); code != 200 {
			t.Fatalf("status %d %s", code, body)
		}
	}
	// Device identity comes from the credential, never the payload.
	spoof := map[string]any{}
	for k, v := range status {
		spoof[k] = v
	}
	spoof["device_id"] = devB.Device.ID
	if code, _ := h.do("POST", "/api/v1/device-control/update/status", spoof, bearer(devA), nil); code != 400 {
		t.Fatalf("payload device identity accepted: %d", code)
	}
	_, body = h.do("GET", "/api/v1/device-control/update/command", nil, bearer(devB), nil)
	if !strings.Contains(body, `"command":null`) {
		t.Fatalf("Store B device received Store A command: %s", body)
	}
	_, body = h.do("GET", "/api/v1/device-control/update/command", nil, bearer(devA), nil)
	var poll struct {
		Command struct {
			TargetID string `json:"target_id"`
			Envelope string `json:"envelope"`
		} `json:"command"`
	}
	if err := json.Unmarshal([]byte(body), &poll); err != nil || poll.Command.TargetID == "" {
		t.Fatalf("poll %s", body)
	}
	if !bytes.Equal([]byte(poll.Command.Envelope), env) {
		t.Fatal("Cloud must relay the exact signed envelope bytes")
	}
	report := map[string]any{"state": "DOWNLOADING", "retryable": false, "release_sequence": 11}
	if code, _ := h.do("POST", "/api/v1/device-control/update/targets/"+poll.Command.TargetID+"/report", report, bearer(devB), nil); code != 404 {
		t.Fatalf("foreign device result accepted: %d", code)
	}
	if code, body := h.do("POST", "/api/v1/device-control/update/targets/"+poll.Command.TargetID+"/report", report, bearer(devA), nil); code != 200 {
		t.Fatalf("own report %d %s", code, body)
	}
	// Revoked device: every device route rejected.
	if err := h.a.Devices.RevokeDevice(context.Background(), devA.Device.ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := h.do("GET", "/api/v1/device-control/update/command", nil, bearer(devA), nil); code != 401 {
		t.Fatalf("revoked device poll: %d", code)
	}
	if code, _ := h.do("POST", "/api/v1/device-control/update/targets/"+poll.Command.TargetID+"/report", report, bearer(devA), nil); code != 401 {
		t.Fatalf("revoked device report: %d", code)
	}
}
