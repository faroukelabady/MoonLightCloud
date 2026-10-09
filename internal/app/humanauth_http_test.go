package app

// Phase 18 R1 human authentication & authorization proofs (ADR-0053) over
// the real HTTP stack and real PostgreSQL.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/faroukelabady/MoonLightCloud/internal/humanauth"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
)

type authEnv struct {
	t   *testing.T
	a   *App
	srv *httptest.Server
	clk *stepClock
	url string
}

func newAuthEnv(t *testing.T) *authEnv {
	t.Helper()
	url := testutil.Isolated(t)
	a, err := New(context.Background(), testConfig(t, url))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	clk := humanKit(t, a)
	srv := httptest.NewServer(a.Handler)
	t.Cleanup(srv.Close)
	return &authEnv{t: t, a: a, srv: srv, clk: clk, url: url}
}

func (e *authEnv) store(id string) {
	e.t.Helper()
	if _, err := e.a.Pool.Exec(context.Background(), `INSERT INTO stores (id, display_name, timezone) VALUES ($1, 'S', 'Africa/Cairo') ON CONFLICT DO NOTHING`, id); err != nil {
		e.t.Fatal(err)
	}
}

func (e *authEnv) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.a.Pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func sessionCookie(h *human) *http.Cookie {
	for _, c := range h.client.Jar.Cookies(mustURL(h.base)) {
		if strings.HasSuffix(c.Name, "mlc_session") {
			return c
		}
	}
	return nil
}

func withCookie(srv string, c *http.Cookie) *human {
	h := &human{base: srv}
	jarClient := &http.Client{Jar: newJar()}
	jarClient.Jar.SetCookies(mustURL(srv), []*http.Cookie{c})
	h.client = jarClient
	return h
}

func TestNoDefaultAccountExistsOnFreshCloud(t *testing.T) {
	e := newAuthEnv(t)
	if n := e.count(`SELECT count(*) FROM admin_users`); n != 0 {
		t.Fatalf("fresh Cloud has %d human accounts, want 0", n)
	}
	h := newHuman(t, e.srv, e.clk)
	for _, creds := range [][2]string{{"operator", "moonlight-dev-operator"}, {"admin", "admin"}, {"operator", "operator"}} {
		if code, _ := h.login(creds[0], creds[1]); code != 401 {
			t.Fatalf("default credential %v accepted: %d", creds, code)
		}
	}
	for _, path := range []string{"/api/v1/dashboard/overview", "/api/v1/dashboard/releases", "/api/v1/dashboard/users"} {
		if code, _ := h.raw("GET", path, nil, nil); code != 401 {
			t.Fatalf("%s unauthenticated: %d", path, code)
		}
	}
	// There is no public registration surface.
	for _, path := range []string{"/api/v1/dashboard/auth/register", "/api/v1/dashboard/auth/sign-up", "/api/v1/auth/register", "/register"} {
		if code, _ := h.raw("POST", path, map[string]string{"login": "x"}, map[string]string{"Origin": e.srv.URL}); code != 404 && code != 405 {
			t.Fatalf("%s must not exist: %d", path, code)
		}
	}
}

func TestOwnerFirstUseLoginMFALogoutAndRestart(t *testing.T) {
	e := newAuthEnv(t)
	bootstrapOwner(t, e.a, "owner@shop.test")
	h := newHuman(t, e.srv, e.clk)
	code, stage := h.login("Owner@Shop.Test ", testPassword) // normalized
	if code != 200 || stage != "MFA_SETUP" {
		t.Fatalf("first login %d %s", code, stage)
	}
	// Password-only session cannot reach Admin APIs.
	if code, body := h.do("GET", "/api/v1/dashboard/overview", nil); code != 403 || errCode(body) != "MFA_SETUP_REQUIRED" {
		t.Fatalf("pre-MFA admin access %d %v", code, body)
	}
	codes := h.enroll()
	if len(codes) != humanauth.RecoveryCodeCount {
		t.Fatal("recovery codes not issued")
	}
	code, me := h.do("GET", "/api/v1/dashboard/auth/me", nil)
	if code != 200 || me["stage"] != "FULL" || !strings.Contains(me["_raw"].(string), "users.manage") {
		t.Fatalf("me %d %s", code, me["_raw"])
	}
	for _, secretish := range []string{"password", "secret", "hash", "totp"} {
		if strings.Contains(strings.ToLower(me["_raw"].(string)), secretish) {
			t.Fatalf("me leaks %q: %s", secretish, me["_raw"])
		}
	}
	// Fresh first use: no merchant catalog exists, and that is legitimate.
	if code, body := h.do("GET", "/api/v1/dashboard/catalog-health", nil); code != 200 {
		t.Fatalf("catalog health on empty shop %d %s", code, body["_raw"])
	}
	// Restart durability: a new process on the same DB honors the session.
	cookie := sessionCookie(h)
	cfg := testConfig(t, e.url)
	b, err := New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	b.HumanAuth.WithArgon2(cheapArgon).WithClock(e.clk)
	srv2 := httptest.NewServer(b.Handler)
	defer srv2.Close()
	h2 := withCookie(srv2.URL, cookie)
	h2.t, h2.csrf, h2.clk = t, h.csrf, e.clk
	if code, _ := h2.do("GET", "/api/v1/dashboard/auth/me", nil); code != 200 {
		t.Fatalf("session after restart %d", code)
	}
	// Logout revokes server-side; replaying the old cookie fails.
	if code, _ := h.do("POST", "/api/v1/dashboard/auth/logout", nil); code != 200 {
		t.Fatalf("logout %d", code)
	}
	replay := withCookie(e.srv.URL, cookie)
	replay.t = t
	if code, _ := replay.raw("GET", "/api/v1/dashboard/auth/me", nil, nil); code != 401 {
		t.Fatalf("logout replay %d", code)
	}
	// Second login: MFA challenge; wrong / expired codes fail; the pending
	// cookie is rotated away on success.
	h3 := newHuman(t, e.srv, e.clk)
	h3.secret = h.secret
	if code, stage := h3.login("owner@shop.test", testPassword); code != 200 || stage != "MFA_PENDING" {
		t.Fatalf("second login %d %s", code, stage)
	}
	pending := sessionCookie(h3)
	if code, body := h3.do("GET", "/api/v1/dashboard/overview", nil); code != 403 || errCode(body) != "MFA_REQUIRED" {
		t.Fatalf("MFA_PENDING admin access %d %v", code, body)
	}
	if code, _ := h3.do("POST", "/api/v1/dashboard/auth/mfa/verify", map[string]string{"code": "000000"}); code != 401 {
		t.Fatalf("wrong TOTP %d", code)
	}
	expired := humanauth.HOTP(h3.secret, humanauth.TOTPStep(e.clk.Now())-2)
	if code, _ := h3.do("POST", "/api/v1/dashboard/auth/mfa/verify", map[string]string{"code": expired}); code != 401 {
		t.Fatalf("expired TOTP %d", code)
	}
	h3.verify()
	old := withCookie(e.srv.URL, pending)
	old.t = t
	if code, _ := old.raw("GET", "/api/v1/dashboard/auth/me", nil, nil); code != 401 {
		t.Fatalf("pre-rotation cookie replay %d", code)
	}
	if code, _ := h3.do("GET", "/api/v1/dashboard/overview?period=today", nil); code != 200 {
		t.Fatalf("full session dashboard %d", code)
	}
	if n := e.count(`SELECT count(*) FROM auth_audit_events WHERE action IN ('auth.login','auth.mfa.enrolled','auth.logout','auth.mfa.verify')`); n < 5 {
		t.Fatalf("audit too sparse: %d", n)
	}
}

func TestBootstrapIsSingleUnderConcurrency(t *testing.T) {
	e := newAuthEnv(t)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := e.a.HumanAuth.BootstrapOwner(context.Background(), fmt.Sprintf("owner%d@shop.test", i), "Owner", testPassword, nil)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, humanauth.ErrAlreadyBootstrapped) {
			t.Fatalf("unexpected bootstrap error: %v", err)
		}
	}
	if wins != 1 || e.count(`SELECT count(*) FROM admin_users WHERE role = 'OWNER'`) != 1 {
		t.Fatalf("bootstrap winners=%d owners=%d", wins, e.count(`SELECT count(*) FROM admin_users WHERE role='OWNER'`))
	}
	if _, err := e.a.HumanAuth.BootstrapOwner(context.Background(), "late@shop.test", "Late", testPassword, nil); !errors.Is(err, humanauth.ErrAlreadyBootstrapped) {
		t.Fatalf("repeated bootstrap: %v", err)
	}
}

func TestLoginEnumerationResistanceAndThrottling(t *testing.T) {
	e := newAuthEnv(t)
	owner := fullOwner(t, e.a, e.srv, e.clk, "owner@shop.test")
	_ = owner
	try := func(login, pw, ip string) (int, string) {
		h := newHuman(t, e.srv, e.clk)
		code, body := h.raw("POST", "/api/v1/dashboard/auth/login", map[string]string{"login": login, "password": pw},
			map[string]string{"Origin": e.srv.URL, "X-Forwarded-For": ip})
		return code, errCode(body)
	}
	// Unknown user, wrong password, over-long input: identical response.
	for _, tc := range [][2]string{{"nobody@shop.test", testPassword}, {"owner@shop.test", "wrong-password-123"},
		{"owner@shop.test", strings.Repeat("x", 2000)}, {"no", "x"}} {
		if code, ec := try(tc[0], tc[1], ""); code != 401 || ec != "INVALID_CREDENTIALS" {
			t.Fatalf("%v: %d %s", tc[0], code, ec)
		}
	}
	// An oversized request body is refused before any account lookup, with
	// the same response for known and unknown accounts.
	for _, login := range []string{"owner@shop.test", "nobody@shop.test"} {
		if code, ec := try(login, strings.Repeat("x", 100000), ""); code != 400 || ec != "INVALID_INPUT" {
			t.Fatalf("oversized body for %s: %d %s", login, code, ec)
		}
	}
	// A malformed stored hash fails closed (no panic, same response).
	if _, err := e.a.Pool.Exec(context.Background(), `UPDATE admin_users SET password_hash = '$argon2id$garbage' WHERE login = 'owner@shop.test'`); err != nil {
		t.Fatal(err)
	}
	if code, ec := try("owner@shop.test", testPassword, ""); code != 401 || ec != "INVALID_CREDENTIALS" {
		t.Fatalf("malformed hash %d %s", code, ec)
	}
	if _, err := e.a.HumanAuth.ResetPassword(context.Background(), "owner@shop.test", testPassword, false); err != nil {
		t.Fatal(err)
	}
	// Account throttle: after 5 failures even the right password is
	// temporarily refused (documented), then works after the lock.
	e.clk.Advance(time.Hour)
	for i := 0; i < 5; i++ {
		try("owner@shop.test", "wrong-password-123", "")
	}
	if code, ec := try("owner@shop.test", testPassword, ""); code != 429 || ec != "RATE_LIMITED" {
		t.Fatalf("throttled login %d %s", code, ec)
	}
	// Unknown accounts throttle identically (no enumeration via 429).
	for i := 0; i < 5; i++ {
		try("ghost@shop.test", "wrong-password-123", "")
	}
	if code, _ := try("ghost@shop.test", "wrong-password-123", ""); code != 429 {
		t.Fatalf("unknown account throttle %d", code)
	}
	e.clk.Advance(16 * time.Minute)
	if code, _ := try("owner@shop.test", testPassword, ""); code != 200 {
		t.Fatalf("login after lock expiry %d", code)
	}
	if n := e.count(`SELECT count(*) FROM admin_login_throttle WHERE key_kind = 'account' AND key = 'owner@shop.test'`); n != 0 {
		t.Fatal("successful login must clear the account throttle")
	}
}

func TestMFAAdversarialAndRecoveryCodes(t *testing.T) {
	e := newAuthEnv(t)
	owner := fullOwner(t, e.a, e.srv, e.clk, "owner@shop.test")
	codes := []string{}
	code, body := owner.do("POST", "/api/v1/dashboard/auth/mfa/recovery-codes", nil)
	if code != 200 {
		t.Fatalf("regenerate %d", code)
	}
	for _, c := range body["recovery_codes"].([]any) {
		codes = append(codes, c.(string))
	}
	// Ten parallel MFA_PENDING sessions race the same recovery code: one wins.
	e.clk.Advance(time.Minute)
	var sessions []*human
	for i := 0; i < 10; i++ {
		h := newHuman(t, e.srv, e.clk)
		if code, stage := h.login("owner@shop.test", testPassword); code != 200 || stage != "MFA_PENDING" {
			t.Fatalf("login %d %s", code, stage)
		}
		sessions = append(sessions, h)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for _, h := range sessions {
		wg.Add(1)
		go func(h *human) {
			defer wg.Done()
			code, _ := h.do("POST", "/api/v1/dashboard/auth/mfa/verify", map[string]string{"recovery_code": strings.ToLower(codes[0])})
			if code == 200 {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(h)
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("recovery code used %d times, want exactly 1", wins)
	}
	// Reused code fails afterwards; regenerated set invalidates old codes.
	h := newHuman(t, e.srv, e.clk)
	h.login("owner@shop.test", testPassword)
	if code, _ := h.do("POST", "/api/v1/dashboard/auth/mfa/verify", map[string]string{"recovery_code": codes[0]}); code != 401 {
		t.Fatalf("reused recovery code %d", code)
	}
	if n := e.count(`SELECT count(*) FROM auth_audit_events WHERE action = 'auth.mfa.recovery_used' AND outcome = 'success'`); n != 1 {
		t.Fatalf("recovery use audit %d", n)
	}
	// Losing recovery attempts are MFA failures that may have locked the
	// account (the winner's success clears it); wait out any bounded lock.
	e.clk.Advance(16 * time.Minute)
	// Repeated wrong TOTP codes throttle the account deterministically.
	brute := newHuman(t, e.srv, e.clk)
	brute.login("owner@shop.test", testPassword)
	for i := 0; i < 5; i++ {
		if code, _ := brute.do("POST", "/api/v1/dashboard/auth/mfa/verify", map[string]string{"code": "000000"}); code != 401 {
			t.Fatalf("wrong TOTP %d -> %d", i, code)
		}
	}
	if code, body := brute.do("POST", "/api/v1/dashboard/auth/mfa/verify", map[string]string{"code": "000000"}); code != 429 || errCode(body) != "RATE_LIMITED" {
		t.Fatalf("MFA brute force not throttled: %d %v", code, body)
	}
	e.clk.Advance(16 * time.Minute)
	// TOTP replay: the step used by one session cannot complete another.
	e.clk.Advance(time.Minute)
	a1, a2 := newHuman(t, e.srv, e.clk), newHuman(t, e.srv, e.clk)
	a1.secret, a2.secret = owner.secret, owner.secret
	a1.login("owner@shop.test", testPassword)
	a2.login("owner@shop.test", testPassword)
	step := a1.totp()
	if code, _ := a1.do("POST", "/api/v1/dashboard/auth/mfa/verify", map[string]string{"code": step}); code != 200 {
		t.Fatalf("first TOTP %d", code)
	}
	if code, _ := a2.do("POST", "/api/v1/dashboard/auth/mfa/verify", map[string]string{"code": step}); code != 401 {
		t.Fatalf("replayed TOTP accepted: %d", code)
	}
	// Future-window code (next step) is accepted once.
	future := humanauth.HOTP(owner.secret, humanauth.TOTPStep(e.clk.Now())+1)
	if code, _ := a2.do("POST", "/api/v1/dashboard/auth/mfa/verify", map[string]string{"code": future}); code != 200 {
		t.Fatalf("future-window TOTP %d", code)
	}
	// Tampered encrypted secret fails safely.
	if _, err := e.a.Pool.Exec(context.Background(), `UPDATE admin_mfa_credentials SET secret_ciphertext = set_byte(secret_ciphertext, 20, get_byte(secret_ciphertext, 20) # 1)`); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(time.Minute)
	t3 := newHuman(t, e.srv, e.clk)
	t3.secret = owner.secret
	t3.login("owner@shop.test", testPassword)
	if code, _ := t3.do("POST", "/api/v1/dashboard/auth/mfa/verify", map[string]string{"code": t3.totp()}); code != 401 {
		t.Fatalf("tampered secret accepted: %d", code)
	}
	// Secrets are never stored in usable form.
	var cipher []byte
	if err := e.a.Pool.QueryRow(context.Background(), `SELECT secret_ciphertext FROM admin_mfa_credentials`).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cipher), string(owner.secret)) {
		t.Fatal("TOTP secret stored in plaintext")
	}
	if n := e.count(`SELECT count(*) FROM auth_audit_events WHERE details::text ILIKE $1 OR details::text ILIKE $2`, "%"+testPassword+"%", "%"+codes[1]+"%"); n != 0 {
		t.Fatal("audit contains a secret")
	}
}

func TestSessionRevocationSemantics(t *testing.T) {
	e := newAuthEnv(t)
	storeA := uuid.NewString()
	e.store(storeA)
	owner := fullOwner(t, e.a, e.srv, e.clk, "owner@shop.test")
	admin := owner.createUser(e.srv, "admin@shop.test", "ADMIN", false, storeA)
	// Random and tampered cookies.
	for _, v := range []string{"random", strings.Repeat("A", 43), sessionCookie(admin).Value + "x"} {
		h := withCookie(e.srv.URL, &http.Cookie{Name: sessionCookie(admin).Name, Value: v, Path: "/"})
		h.t = t
		if code, _ := h.raw("GET", "/api/v1/dashboard/auth/me", nil, nil); code != 401 {
			t.Fatalf("forged cookie %q accepted: %d", v, code)
		}
	}
	// Membership removal takes effect on the next request.
	if code, _ := admin.do("GET", "/api/v1/dashboard/overview?period=today&store_id="+storeA, nil); code != 200 {
		t.Fatalf("member store %d", code)
	}
	adminID := e.adminID("admin@shop.test")
	storeB := uuid.NewString()
	e.store(storeB)
	if code, body := owner.do("POST", "/api/v1/dashboard/users/"+adminID+"/memberships", map[string]any{"all_stores": false, "store_ids": []string{storeB}}); code != 200 {
		t.Fatalf("memberships %d %s", code, body["_raw"])
	}
	if code, _ := admin.do("GET", "/api/v1/dashboard/overview?period=today&store_id="+storeA, nil); code != 403 {
		t.Fatalf("removed membership still honored: %d", code)
	}
	// Disabling the ADMIN kills its live session immediately.
	if code, _ := owner.do("POST", "/api/v1/dashboard/users/"+adminID+"/status", map[string]string{"status": "DISABLED"}); code != 200 {
		t.Fatalf("disable %d", code)
	}
	if code, _ := admin.do("GET", "/api/v1/dashboard/auth/me", nil); code != 401 {
		t.Fatalf("disabled user session %d", code)
	}
	if code, _ := admin.login("admin@shop.test", testPassword); code != 401 {
		t.Fatalf("disabled user login %d", code)
	}
	// Password change revokes other sessions and rotates the current one.
	other := newHuman(t, e.srv, e.clk)
	other.secret = owner.secret
	other.login("owner@shop.test", testPassword)
	other.verify()
	before := sessionCookie(owner)
	if code, body := owner.do("POST", "/api/v1/dashboard/auth/password", map[string]string{"current_password": testPassword, "new_password": "a-new-test-only-password"}); code != 200 {
		t.Fatalf("password change %d %s", code, body["_raw"])
	} else {
		owner.csrf = body["csrf_token"].(string)
	}
	if code, _ := other.do("GET", "/api/v1/dashboard/auth/me", nil); code != 401 {
		t.Fatalf("other session survived password change: %d", code)
	}
	stale := withCookie(e.srv.URL, before)
	stale.t = t
	if code, _ := stale.raw("GET", "/api/v1/dashboard/auth/me", nil, nil); code != 401 {
		t.Fatalf("pre-rotation cookie survived: %d", code)
	}
	if code, _ := owner.do("GET", "/api/v1/dashboard/auth/me", nil); code != 200 {
		t.Fatalf("rotated session %d", code)
	}
	// Idle timeout (30m) and absolute timeout (12h) on the server clock.
	e.clk.Advance(31 * time.Minute)
	if code, _ := owner.do("GET", "/api/v1/dashboard/auth/me", nil); code != 401 {
		t.Fatalf("idle-expired session %d", code)
	}
	fresh := newHuman(t, e.srv, e.clk)
	fresh.secret = owner.secret
	fresh.login("owner@shop.test", "a-new-test-only-password")
	fresh.verify()
	for i := 0; i < 25; i++ { // keep active past the absolute bound
		e.clk.Advance(29 * time.Minute)
		code, _ := fresh.do("GET", "/api/v1/dashboard/auth/me", nil)
		if i < 23 && code != 200 {
			t.Fatalf("active session expired early at step %d: %d", i, code)
		}
		if i == 24 && code != 401 {
			t.Fatalf("absolute expiry not enforced: %d", code)
		}
	}
}

func (e *authEnv) adminID(login string) string {
	e.t.Helper()
	var id string
	if err := e.a.Pool.QueryRow(context.Background(), `SELECT id::text FROM admin_users WHERE login = $1`, login).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func TestCSRFProtectionOnMutations(t *testing.T) {
	e := newAuthEnv(t)
	owner := fullOwner(t, e.a, e.srv, e.clk, "owner@shop.test")
	body := map[string]any{"login": "admin@shop.test", "display_name": "Admin", "role": "ADMIN", "all_stores": true}
	for name, headers := range map[string]map[string]string{
		"no CSRF token":   {"Origin": e.srv.URL},
		"wrong token":     {"Origin": e.srv.URL, "X-CSRF-Token": "wrong"},
		"foreign origin":  {"Origin": "https://evil.example", "X-CSRF-Token": owner.csrf},
		"no origin":       {"X-CSRF-Token": owner.csrf},
		"foreign referer": {"Referer": "https://evil.example/page", "X-CSRF-Token": owner.csrf},
	} {
		if code, b := owner.raw("POST", "/api/v1/dashboard/users", body, headers); code != 403 || errCode(b) != "CSRF_REJECTED" {
			t.Fatalf("%s: %d %v", name, code, b)
		}
	}
	if e.count(`SELECT count(*) FROM admin_users`) != 1 {
		t.Fatal("a rejected mutation wrote")
	}
	if code, _ := owner.do("POST", "/api/v1/dashboard/users", body); code != 201 {
		t.Fatalf("valid same-origin mutation %d", code)
	}
	// Cross-origin login is refused too (no session-fixation via login CSRF).
	h := newHuman(t, e.srv, e.clk)
	if code, _ := h.raw("POST", "/api/v1/dashboard/auth/login", map[string]string{"login": "owner@shop.test", "password": testPassword},
		map[string]string{"Origin": "https://evil.example"}); code != 403 {
		t.Fatalf("cross-origin login %d", code)
	}
}

// TestStoreAuthorizationAcrossHumans is §78: OwnerA/AdminA -> Store A,
// OwnerB -> Store B; the frontend Store selector is never authorization.
func TestStoreAuthorizationAcrossHumans(t *testing.T) {
	e := newAuthEnv(t)
	storeA, storeB := uuid.NewString(), uuid.NewString()
	e.store(storeA)
	e.store(storeB)
	root := fullOwner(t, e.a, e.srv, e.clk, "root@shop.test")
	ownerA := root.createUser(e.srv, "owner-a@shop.test", "OWNER", false, storeA)
	adminA := root.createUser(e.srv, "admin-a@shop.test", "ADMIN", false, storeA)
	ownerB := root.createUser(e.srv, "owner-b@shop.test", "OWNER", false, storeB)
	devA, devB := e.boundDevice("dev-a", storeA), e.boundDevice("dev-b", storeB)

	expect := func(h *human, method, path string, body any, want int) {
		t.Helper()
		code, b := h.do(method, path, body)
		if code != want {
			t.Fatalf("%s %s: %d want %d (%s)", method, path, code, want, b["_raw"])
		}
	}
	// OwnerA cannot read Store B catalog; can read Store A.
	expect(ownerA, "GET", "/api/v1/dashboard/catalog-admin/products?store_id="+storeB, nil, 403)
	expect(ownerA, "GET", "/api/v1/dashboard/catalog-admin/products?store_id="+storeA, nil, 200)
	expect(ownerA, "GET", "/api/v1/dashboard/catalog-admin/categories?store_id="+storeB, nil, 403)
	expect(ownerA, "POST", "/api/v1/dashboard/catalog-admin/commands", map[string]any{"store_id": storeB, "type": "catalog.category.rename.v1",
		"entity_id": uuid.NewString(), "expected_revision": 1, "payload": map[string]any{"name_ar": "x"}}, 403)
	// Aggregate (all-Store) views need an all-Stores human.
	expect(ownerA, "GET", "/api/v1/dashboard/overview?period=today", nil, 403)
	expect(root, "GET", "/api/v1/dashboard/overview?period=today", nil, 200)
	// AdminA cannot target Store B devices.
	expect(adminA, "POST", "/api/v1/dashboard/devices/"+devB+"/sync-requests", nil, 403)
	code, _ := adminA.raw("POST", "/api/v1/dashboard/devices/"+devA+"/sync-requests", nil,
		map[string]string{"Origin": e.srv.URL, "X-CSRF-Token": adminA.csrf, "Idempotency-Key": "k-1"})
	if code == 403 {
		t.Fatal("AdminA denied its own Store device")
	}
	// Device lists and Store lists are filtered server-side.
	_, devices := adminA.do("GET", "/api/v1/dashboard/devices", nil)
	if strings.Contains(devices["_raw"].(string), devB) || !strings.Contains(devices["_raw"].(string), devA) {
		t.Fatalf("device list not Store-filtered: %s", devices["_raw"])
	}
	_, stores := ownerA.do("GET", "/api/v1/dashboard/stores", nil)
	if strings.Contains(stores["_raw"].(string), storeB) {
		t.Fatalf("store list leaks Store B: %s", stores["_raw"])
	}
	// OwnerA cannot create Store B or ALL rollouts, nor import releases.
	expect(ownerA, "POST", "/api/v1/dashboard/rollouts", map[string]any{"release_id": uuid.NewString(), "scope": "STORE",
		"store_id": storeB, "mode": "OPTIONAL", "percentage": 10}, 403)
	expect(ownerA, "POST", "/api/v1/dashboard/rollouts", map[string]any{"release_id": uuid.NewString(), "scope": "ALL",
		"mode": "MANDATORY", "percentage": 100}, 403)
	expect(ownerA, "POST", "/api/v1/dashboard/releases", map[string]any{"envelope": "{}", "artifact_urls": map[string]string{}}, 403)
	expect(ownerA, "GET", "/api/v1/dashboard/releases", nil, 200) // global read with releases.read
	// OwnerB cannot access Store A admin data or operations.
	expect(ownerB, "GET", "/api/v1/dashboard/catalog-admin/tags?store_id="+storeA, nil, 403)
	expect(ownerB, "GET", "/api/v1/dashboard/fleet?store_id="+storeA, nil, 403)
	expect(ownerB, "GET", "/api/v1/dashboard/operations/incidents", nil, 403)
	expect(ownerB, "GET", "/api/v1/dashboard/orders?store_id="+storeA, nil, 403)
	// Restricted OWNERs cannot reach beyond their Stores in user admin.
	expect(ownerA, "POST", "/api/v1/dashboard/users", map[string]any{"login": "x@shop.test", "display_name": "X", "role": "ADMIN", "all_stores": true}, 403)
	expect(ownerA, "POST", "/api/v1/dashboard/users", map[string]any{"login": "y@shop.test", "display_name": "Y", "role": "ADMIN", "store_ids": []string{storeB}}, 403)
	expect(ownerA, "POST", "/api/v1/dashboard/users/"+e.adminID("owner-b@shop.test")+"/status", map[string]string{"status": "DISABLED"}, 403)
	// ADMIN cannot administer accounts at all.
	expect(adminA, "GET", "/api/v1/dashboard/users", nil, 403)
	expect(adminA, "POST", "/api/v1/dashboard/users", map[string]any{"login": "z@shop.test", "display_name": "Z", "role": "OWNER", "store_ids": []string{storeA}}, 403)
	expect(adminA, "POST", "/api/v1/dashboard/users/"+e.adminID("root@shop.test")+"/role", map[string]string{"role": "ADMIN"}, 403)
	expect(adminA, "POST", "/api/v1/dashboard/users/"+e.adminID("admin-a@shop.test")+"/role", map[string]string{"role": "OWNER"}, 403)
	if n := e.count(`SELECT count(*) FROM auth_audit_events WHERE outcome = 'denied'`); n == 0 {
		t.Fatal("sensitive denials must be audited")
	}
}

func (e *authEnv) boundDevice(name, storeID string) string {
	e.t.Helper()
	p, err := e.a.Devices.Create(context.Background(), name)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.a.Pool.Exec(context.Background(), `INSERT INTO device_store_bindings (device_id, store_id) VALUES ($1, $2)`, p.Device.ID, storeID); err != nil {
		e.t.Fatal(err)
	}
	return p.Device.ID
}

func TestFinalOwnerProtectionAndConcurrency(t *testing.T) {
	e := newAuthEnv(t)
	owner := fullOwner(t, e.a, e.srv, e.clk, "owner@shop.test")
	ownerID := e.adminID("owner@shop.test")
	for _, req := range []struct{ path, field, value string }{
		{"/status", "status", "DISABLED"}, {"/role", "role", "ADMIN"},
	} {
		if code, b := owner.do("POST", "/api/v1/dashboard/users/"+ownerID+req.path, map[string]string{req.field: req.value}); code != 409 || errCode(b) != "LAST_OWNER" {
			t.Fatalf("final OWNER %s: %d %v", req.path, code, b)
		}
	}
	// Activation tokens are single-use under concurrency.
	code, body := owner.do("POST", "/api/v1/dashboard/users", map[string]any{"login": "race@shop.test", "display_name": "Race", "role": "ADMIN", "all_stores": true})
	if code != 201 {
		t.Fatalf("create %d", code)
	}
	token := body["activation_token"].(string)
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h := newHuman(t, e.srv, e.clk)
			c, _ := h.raw("POST", "/api/v1/dashboard/auth/activate", map[string]string{"token": token, "password": testPassword},
				map[string]string{"Origin": e.srv.URL})
			if c == 200 {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("activation token used %d times", wins)
	}
	// Expired activation tokens fail.
	_, body = owner.do("POST", "/api/v1/dashboard/users", map[string]any{"login": "late@shop.test", "display_name": "Late", "role": "ADMIN", "all_stores": true})
	e.clk.Advance(25 * time.Hour)
	h := newHuman(t, e.srv, e.clk)
	if c, _ := h.raw("POST", "/api/v1/dashboard/auth/activate", map[string]string{"token": body["activation_token"].(string), "password": testPassword},
		map[string]string{"Origin": e.srv.URL}); c != 400 {
		t.Fatalf("expired activation %d", c)
	}
}

// TestTwoOwnersRemovingEachOtherConcurrently (§76): with exactly two
// active OWNERs racing to disable (or demote) each other, the transactional
// guard lets at most one win and an active OWNER always remains.
func TestTwoOwnersRemovingEachOtherConcurrently(t *testing.T) {
	for round := 0; round < 4; round++ {
		t.Run(fmt.Sprintf("round-%d", round), func(t *testing.T) {
			e := newAuthEnv(t)
			first := fullOwner(t, e.a, e.srv, e.clk, "first@shop.test")
			second := first.createUser(e.srv, "second@shop.test", "OWNER", true)
			ids := []string{e.adminID("first@shop.test"), e.adminID("second@shop.test")}
			path, field, value := "/status", "status", "DISABLED"
			if round%2 == 1 {
				path, field, value = "/role", "role", "ADMIN"
			}
			var wg sync.WaitGroup
			codes := make([]int, 2)
			for i, actor := range []*human{first, second} {
				wg.Add(1)
				go func(i int, actor *human) {
					defer wg.Done()
					codes[i], _ = actor.do("POST", "/api/v1/dashboard/users/"+ids[1-i]+path, map[string]string{field: value})
				}(i, actor)
			}
			wg.Wait()
			successes := 0
			for _, c := range codes {
				if c == 200 {
					successes++
				}
			}
			if n := e.count(`SELECT count(*) FROM admin_users WHERE role = 'OWNER' AND status = 'ACTIVE'`); n != 1 || successes > 1 {
				t.Fatalf("active owners=%d successes=%d codes=%v", n, successes, codes)
			}
		})
	}
}

// TestEveryDashboardRouteRequiresHumanAuth mechanically covers EVERY
// /api/v1/dashboard route registered by the router source: anonymous,
// device-credential and password-only (pre-MFA) callers are all refused.
func TestEveryDashboardRouteRequiresHumanAuth(t *testing.T) {
	e := newAuthEnv(t)
	var src strings.Builder
	for _, f := range []string{"../adapter/http/server.go", "../adapter/http/humanauth.go"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src.Write(raw)
	}
	re := regexp.MustCompile(`"(GET|POST) (/api/v1/dashboard/[^"]+)"`)
	matches := re.FindAllStringSubmatch(src.String(), -1)
	if len(matches) < 50 {
		t.Fatalf("route extraction found only %d routes", len(matches))
	}
	public := map[string]bool{"/api/v1/dashboard/auth/login": true, "/api/v1/dashboard/auth/activate": true}
	param := regexp.MustCompile(`\{[^}]+\}`)
	devCred, err := e.a.Devices.Create(context.Background(), "cred-probe")
	if err != nil {
		t.Fatal(err)
	}
	bearer := "Bearer " + devCred.Device.ID + "." + devCred.Credential.ID + "." + devCred.RawSecret
	bootstrapOwner(t, e.a, "owner@shop.test")
	pending := newHuman(t, e.srv, e.clk)
	pending.login("owner@shop.test", testPassword) // MFA_SETUP only
	for _, m := range matches {
		method, path := m[1], param.ReplaceAllString(m[2], uuid.NewString())
		if public[m[2]] {
			continue
		}
		anon := newHuman(t, e.srv, e.clk)
		if code, _ := anon.raw(method, path, map[string]any{}, map[string]string{"Origin": e.srv.URL}); code != 401 {
			t.Fatalf("anonymous %s %s -> %d", method, m[2], code)
		}
		if code, _ := anon.raw(method, path, map[string]any{}, map[string]string{"Origin": e.srv.URL, "Authorization": bearer}); code != 401 {
			t.Fatalf("device credential %s %s -> %d", method, m[2], code)
		}
		if strings.HasPrefix(m[2], "/api/v1/dashboard/auth/") {
			continue // MFA endpoints are reachable by design pre-MFA
		}
		if code, _ := pending.do(method, path, map[string]any{}); code != 403 {
			t.Fatalf("password-only session %s %s -> %d", method, m[2], code)
		}
	}
}

func TestSecurityHeadersOnShellAndAPI(t *testing.T) {
	e := newAuthEnv(t)
	res, err := http.Get(e.srv.URL + "/api/v1/dashboard/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("API headers: %v", res.Header)
	}
}
