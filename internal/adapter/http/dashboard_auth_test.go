package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
)

func testDashboardAuth() DashboardHandlers {
	return NewDashboardHandlers(
		dashboard.Credentials{Username: "operator", PasswordHash: mustTestHash()},
		mustTestKey(), time.Hour, false, slog.Default())
}

func mustTestHash() string {
	h, err := dashboard.HashPassword("correct-password")
	if err != nil {
		panic(err)
	}
	return h
}

func mustTestKey() []byte {
	k, err := dashboard.SessionKey([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		panic(err)
	}
	return k
}

func login(t *testing.T, h DashboardHandlers, user, pass, remote string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/dashboard/auth/login",
		strings.NewReader(`{"username":"`+user+`","password":"`+pass+`"}`))
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	h.Login(rec, req)
	return rec
}

func TestDashboardLoginValid(t *testing.T) {
	h := testDashboardAuth()
	rec := login(t, h, "operator", "correct-password", "10.0.0.1:1234")
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var found bool
	for _, c := range rec.Result().Cookies() {
		if c.Name != dashboard.SessionCookie {
			continue
		}
		found = true
		if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
			t.Fatalf("cookie flags: %+v", c)
		}
		if c.Value == "" {
			t.Fatal("empty session value")
		}
	}
	if !found {
		t.Fatal("session cookie not set")
	}
}

func TestDashboardLoginInvalid(t *testing.T) {
	h := testDashboardAuth()
	for name, cred := range map[string][2]string{
		"wrong password": {"operator", "nope"},
		"wrong user":     {"root", "correct-password"},
		"empty":          {"", ""},
	} {
		rec := login(t, h, cred[0], cred[1], "10.0.0.2:1234")
		if rec.Code != 401 {
			t.Fatalf("%s: want 401, got %d", name, rec.Code)
		}
		var env Envelope
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		if env.Error.Code != "UNAUTHORIZED" {
			t.Fatalf("%s: envelope: %+v", name, env)
		}
	}
}

func TestDashboardSessionGate(t *testing.T) {
	h := testDashboardAuth()
	next := h.RequireDashboardSession(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}))
	// No cookie → 401.
	req := httptest.NewRequest("GET", "/x", nil)
	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("no session: want 401, got %d", rec.Code)
	}
	// Tampered cookie → 401 (generic, same as missing).
	lrec := login(t, h, "operator", "correct-password", "10.0.0.3:1234")
	var session string
	for _, c := range lrec.Result().Cookies() {
		if c.Name == dashboard.SessionCookie {
			session = c.Value
		}
	}
	bad := httptest.NewRequest("GET", "/x", nil)
	bad.AddCookie(&http.Cookie{Name: dashboard.SessionCookie, Value: session + "tampered"})
	brec := httptest.NewRecorder()
	next.ServeHTTP(brec, bad)
	if brec.Code != 401 {
		t.Fatalf("tampered: want 401, got %d", brec.Code)
	}
	// Valid cookie → through.
	good := httptest.NewRequest("GET", "/x", nil)
	good.AddCookie(&http.Cookie{Name: dashboard.SessionCookie, Value: session})
	grec := httptest.NewRecorder()
	next.ServeHTTP(grec, good)
	if grec.Code != 200 {
		t.Fatalf("valid session: want 200, got %d", grec.Code)
	}
	// Expired sessions fail closed (unit-covered in dashboard package).
}

func TestDashboardLoginRateLimit(t *testing.T) {
	h := testDashboardAuth()
	ip := "10.9.9.9:1234"
	for i := 0; i < dashboard.LoginMaxFailures; i++ {
		rec := login(t, h, "operator", "wrong", ip)
		if rec.Code != 401 {
			t.Fatalf("attempt %d: want 401, got %d", i, rec.Code)
		}
	}
	rec := login(t, h, "operator", "wrong", ip)
	if rec.Code != 429 {
		t.Fatalf("over limit: want 429, got %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 must carry Retry-After")
	}
	// Other IPs unaffected.
	if rec := login(t, h, "operator", "wrong", "10.9.9.10:1234"); rec.Code != 401 {
		t.Fatalf("other IP: want 401, got %d", rec.Code)
	}
}

func TestDashboardLogout(t *testing.T) {
	h := testDashboardAuth()
	lrec := login(t, h, "operator", "correct-password", "10.0.0.4:1234")
	var session string
	for _, c := range lrec.Result().Cookies() {
		if c.Name == dashboard.SessionCookie {
			session = c.Value
		}
	}
	req := httptest.NewRequest("POST", "/api/v1/dashboard/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: dashboard.SessionCookie, Value: session})
	req.Host = "cloud.local"
	req.Header.Set("Origin", "http://cloud.local")
	rec := httptest.NewRecorder()
	RequireSameOrigin(h.RequireDashboardSession(http.HandlerFunc(h.Logout))).ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == dashboard.SessionCookie && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("logout must clear the cookie")
	}
	// Cross-origin POST refused even with a valid session.
	xreq := httptest.NewRequest("POST", "/api/v1/dashboard/auth/logout", nil)
	xreq.AddCookie(&http.Cookie{Name: dashboard.SessionCookie, Value: session})
	xreq.Host = "cloud.local"
	xreq.Header.Set("Origin", "https://evil.example")
	xrec := httptest.NewRecorder()
	RequireSameOrigin(h.RequireDashboardSession(http.HandlerFunc(h.Logout))).ServeHTTP(xrec, xreq)
	if xrec.Code != 403 {
		t.Fatalf("cross-origin: want 403, got %d", xrec.Code)
	}
}

func TestDashboardProductionSecureCookie(t *testing.T) {
	h := NewDashboardHandlers(
		dashboard.Credentials{Username: "operator", PasswordHash: mustTestHash()},
		mustTestKey(), time.Hour, true, slog.Default())
	rec := login(t, h, "operator", "correct-password", "10.0.0.5:1234")
	for _, c := range rec.Result().Cookies() {
		if c.Name == dashboard.SessionCookie && !c.Secure {
			t.Fatal("production cookies must be Secure")
		}
	}
}
