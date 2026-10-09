package app

// Human-auth test kit (Phase 18 R1, ADR-0053): tests create EXPLICIT
// OWNER/ADMIN fixtures through the real service and drive the real HTTP
// login -> MFA flow. No default or implicit account exists.

import (
	"bytes"
	"context"
	"encoding/base32"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/humanauth"
)

// testPassword is a synthetic, test-only password.
const testPassword = "test-only-password-0001"

// cheapArgon keeps suites fast; production always uses CurrentArgon2.
var cheapArgon = humanauth.Argon2Params{Memory: 8 * 1024, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16}

// stepClock is a controllable application clock.
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *stepClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// humanKit prepares an App for human-auth tests: cheap hashing and a
// controllable clock starting at a TOTP step boundary.
func humanKit(t *testing.T, a *App) *stepClock {
	t.Helper()
	clk := &stepClock{t: time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)}
	a.HumanAuth.WithArgon2(cheapArgon).WithClock(clk)
	return clk
}

// bootstrapOwner creates the first OWNER (server-side path).
func bootstrapOwner(t *testing.T, a *App, login string, stores ...string) {
	t.Helper()
	if _, err := a.HumanAuth.BootstrapOwner(context.Background(), login, "Owner "+login, testPassword, stores); err != nil {
		t.Fatalf("bootstrap owner: %v", err)
	}
}

// human is a browser-like client holding one session.
type human struct {
	t      *testing.T
	base   string
	client *http.Client
	csrf   string
	secret []byte
	clk    *stepClock
}

func newHuman(t *testing.T, srv *httptest.Server, clk *stepClock) *human {
	jar, _ := cookiejar.New(nil)
	return &human{t: t, base: srv.URL, client: &http.Client{Jar: jar}, clk: clk}
}

// do sends a request with same-origin and CSRF evidence.
func (h *human) do(method, path string, body any) (int, map[string]any) {
	h.t.Helper()
	return h.raw(method, path, body, map[string]string{"Origin": h.base, "X-CSRF-Token": h.csrf})
}

func (h *human) raw(method, path string, body any, headers map[string]string) (int, map[string]any) {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, h.base+path, reader)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	out := map[string]any{}
	raw, _ := io.ReadAll(res.Body)
	_ = json.Unmarshal(raw, &out)
	if out == nil {
		out = map[string]any{}
	}
	out["_raw"] = string(raw)
	return res.StatusCode, out
}

func (h *human) totp() string {
	return humanauth.HOTP(h.secret, humanauth.TOTPStep(h.clk.Now()))
}

// login performs password login; returns the resulting stage.
func (h *human) login(login, password string) (int, string) {
	h.t.Helper()
	code, body := h.raw("POST", "/api/v1/dashboard/auth/login", map[string]string{"login": login, "password": password},
		map[string]string{"Origin": h.base})
	if code == 200 {
		h.csrf, _ = body["csrf_token"].(string)
	}
	stage, _ := body["stage"].(string)
	return code, stage
}

// enroll completes TOTP enrollment from an MFA_SETUP session.
func (h *human) enroll() []string {
	h.t.Helper()
	code, body := h.do("POST", "/api/v1/dashboard/auth/mfa/enroll/start", nil)
	if code != 200 {
		h.t.Fatalf("enroll start %d %s", code, body["_raw"])
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(body["secret"].(string))
	if err != nil {
		h.t.Fatal(err)
	}
	h.secret = secret
	code, body = h.do("POST", "/api/v1/dashboard/auth/mfa/enroll/confirm", map[string]string{"code": h.totp()})
	if code != 200 || body["stage"] != "FULL" {
		h.t.Fatalf("enroll confirm %d %s", code, body["_raw"])
	}
	h.csrf = body["csrf_token"].(string)
	var codes []string
	for _, c := range body["recovery_codes"].([]any) {
		codes = append(codes, c.(string))
	}
	return codes
}

// verify completes an MFA_PENDING session with the next TOTP step.
func (h *human) verify() {
	h.t.Helper()
	h.clk.Advance(30 * time.Second)
	code, body := h.do("POST", "/api/v1/dashboard/auth/mfa/verify", map[string]string{"code": h.totp()})
	if code != 200 || body["stage"] != "FULL" {
		h.t.Fatalf("mfa verify %d %s", code, body["_raw"])
	}
	h.csrf = body["csrf_token"].(string)
}

// fullOwner bootstraps an OWNER and returns a FULL session for it.
func fullOwner(t *testing.T, a *App, srv *httptest.Server, clk *stepClock, login string, stores ...string) *human {
	t.Helper()
	bootstrapOwner(t, a, login, stores...)
	h := newHuman(t, srv, clk)
	if code, stage := h.login(login, testPassword); code != 200 || stage != "MFA_SETUP" {
		t.Fatalf("owner login %d %s", code, stage)
	}
	h.enroll()
	return h
}

// createUser provisions a user as owner and activates it to FULL.
func (owner *human) createUser(srv *httptest.Server, login, role string, allStores bool, stores ...string) *human {
	owner.t.Helper()
	if stores == nil {
		stores = []string{}
	}
	code, body := owner.do("POST", "/api/v1/dashboard/users", map[string]any{"login": login, "display_name": "User " + login,
		"role": role, "all_stores": allStores, "store_ids": stores})
	if code != 201 {
		owner.t.Fatalf("create user %d %s", code, body["_raw"])
	}
	token := body["activation_token"].(string)
	u := newHuman(owner.t, srv, owner.clk)
	code, body = u.raw("POST", "/api/v1/dashboard/auth/activate", map[string]string{"token": token, "password": testPassword},
		map[string]string{"Origin": u.base})
	if code != 200 || body["stage"] != "MFA_SETUP" {
		owner.t.Fatalf("activate %d %s", code, body["_raw"])
	}
	u.csrf = body["csrf_token"].(string)
	u.enroll()
	return u
}

func errCode(body map[string]any) string {
	if e, ok := body["error"].(map[string]any); ok {
		if m, ok := e["message"].(string); ok && strings.ToUpper(m) == m {
			return m
		}
		c, _ := e["code"].(string)
		return c
	}
	return ""
}

func mustURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return u
}

func newJar() http.CookieJar {
	jar, _ := cookiejar.New(nil)
	return jar
}
