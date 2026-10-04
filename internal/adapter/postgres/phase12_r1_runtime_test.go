package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPhase12R1Runtime(t *testing.T) {
	image := os.Getenv("MOONLIGHT_PHASE12_R1_RUNTIME_IMAGE")
	if image == "" {
		t.Skip("exact working-image runtime opt-in unset")
	}
	f, _, _ := scopeOrderFixture(t)
	ctx := context.Background()
	d := NewDevices(f.pool, 5*time.Second)
	for i, c := range []string{"EGP", "USD"} {
		snap := scopeSnapshot("website", fmt.Sprint(980+i), "runtime", []orders.OrderLine{orderLine("700", int64(i+1), 10000)})
		snap.Currency = c
		mustReconcile(t, f, snap)
	}
	env := &saleEnv{pool: f.pool, syncSvc: f.syncSvc, devID: f.devA, credID: f.credA}
	tag := tagFixture("aaaaaaaa-0000-4000-8000-000000000001", "gold", "ذهب", "Gold")
	projectSaleV2(t, env, "11111111-0000-4000-8000-000000000001", "2026-09-21T10:00:00Z", []map[string]any{tag})
	tag["name_en"] = "Golden"
	tag["name_ar"] = "ذهبي"
	projectSaleV2(t, env, "11111111-0000-4000-8000-000000000002", "2026-09-22T10:00:00Z", []map[string]any{tag})
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	addr := ln.Addr().String()
	ln.Close()
	name := fmt.Sprintf("p12-r1-runtime-%d", time.Now().UnixNano())
	var contacts atomic.Int64
	fake := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { contacts.Add(1); w.WriteHeader(503) }))
	fake.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			contacts.Add(1)
		}
	}
	fake.StartTLS()
	defer fake.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { contacts.Add(1); w.WriteHeader(502) }))
	defer proxy.Close()
	args := []string{"run", "--detach", "--name", name, "--network=host", "--env", "DATABASE_URL=" + f.pool.Config().ConnString(), "--env", "ENVIRONMENT=development", "--env", "ALLOW_UNAUTHENTICATED_REPORTING=true", "--env", "HTTP_ADDR=" + addr, "--env", "COMMERCE_WOO_ENABLED=true", "--env", "COMMERCE_WOO_PROVIDER_KEY=woo-local", "--env", "COMMERCE_WOO_BASE_URL=" + fake.URL, "--env", "COMMERCE_WOO_CONSUMER_KEY=ck_local_fixture", "--env", "COMMERCE_WOO_CONSUMER_SECRET=sk_local_fixture", "--env", "COMMERCE_WOO_CURRENCY=EGP", "--env", "COMMERCE_WOO_DIMENSION_UNIT=cm", "--env", "COMMERCE_SHOPIFY_ENABLED=true", "--env", "COMMERCE_SHOPIFY_PROVIDER_KEY=shopify-local", "--env", "COMMERCE_SHOPIFY_SHOP_DOMAIN=test.myshopify.com", "--env", "COMMERCE_SHOPIFY_API_VERSION=2026-10", "--env", "COMMERCE_SHOPIFY_ACCESS_TOKEN=local_runtime_token", "--env", "COMMERCE_SHOPIFY_CURRENCY=EGP", "--env", "COMMERCE_SHOPIFY_LOCATION_ID=gid://shopify/Location/10", "--env", "COMMERCE_SHOPIFY_PUBLICATION_ID=gid://shopify/Publication/11", "--env", "HTTPS_PROXY=" + proxy.URL, "--env", "NO_PROXY=127.0.0.1,localhost", image, "serve"}
	if b, e := exec.Command("podman", args...).CombinedOutput(); e != nil {
		t.Fatalf("start %v %s", e, b)
	}
	defer exec.Command("podman", "rm", "-f", name).Run()
	base := "http://" + addr
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar, Timeout: 3 * time.Second}
	get := func(path string) (int, []byte) {
		t.Helper()
		res, e := cl.Get(base + path)
		if e != nil {
			return 0, nil
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b
	}
	waitFor(t, 10*time.Second, func() bool { st, _ := get("/health/ready"); return st == 200 }, "actual image readiness")
	params := "?period=custom&from_date=2026-09-20&to_date=2026-09-30&store_id=" + scopeStoreA
	for _, path := range []string{"/api/v1/dashboard/tags", "/api/v1/dashboard/orders/summary", "/api/v1/dashboard/catalog-health"} {
		if st, _ := get(path + params); st != 401 {
			t.Fatalf("auth %s=%d", path, st)
		}
	}
	res, e := cl.Post(base+"/api/v1/dashboard/auth/login", "application/json", strings.NewReader(`{"username":"operator","password":"moonlight-dev-operator"}`))
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("login %d", res.StatusCode)
	}
	for _, path := range []string{"/dashboard/", "/api/v1/dashboard/tags", "/api/v1/dashboard/orders/summary", "/api/v1/dashboard/catalog-health"} {
		query := params
		if path == "/dashboard/" {
			query = ""
		}
		st, b := get(path + query)
		if st != 200 {
			t.Fatalf("%s %d %s", path, st, b)
		}
		t.Logf("actual OCI %s 200 bytes=%d", path, len(b))
	}
	st, b := get("/api/v1/dashboard/orders/summary" + params + "&currency=USD")
	var out dashboard.OrderAnalytics
	if e = json.Unmarshal(b, &out); e != nil || st != 200 {
		t.Fatal(e, st)
	}
	t.Logf("actual currency=USD => %+v", out.CurrencyTotals)
	if len(out.CurrencyTotals) != 1 || out.CurrencyTotals[0].Currency != "USD" {
		t.Fatalf("currency narrowing failed %+v", out)
	}
	for _, path := range []string{"/api/v1/dashboard/tags" + params + "&limit=0", "/api/v1/dashboard/orders/summary" + params + "&currency=JPY", "/api/v1/dashboard/catalog-health?provider_key=bad!"} {
		if status, body := get(path); status != 400 {
			t.Fatalf("malformed validation %s=%d %s", path, status, body)
		}
	}
	before := r4Payloads(t, f)
	for i := 0; i < 3; i++ {
		get("/api/v1/dashboard/catalog-health" + params)
	}
	if before != r4Payloads(t, f) {
		t.Fatal("runtime health mutated state")
	}
	play := exec.Command("npx", "playwright", "test", "e2e/store-scope.spec.ts", "e2e/phase12-r1.spec.ts", "--reporter=line", "--output=/tmp/moonlight-phase12-r1-evidence/playwright-artifacts")
	play.Dir = "../../../dashboard"
	play.Env = append(os.Environ(), "E2E_BASE_URL="+base+"/dashboard/")
	pb, pe := play.CombinedOutput()
	os.WriteFile("/tmp/moonlight-phase12-r1-evidence/playwright.log", pb, 0600)
	t.Logf("existing Playwright suite exit=%v", pe)
	if pe != nil {
		t.Errorf("Playwright: %s", pb)
	}
	for i, status := range []orders.CanonicalStatus{orders.StatusProcessing, orders.StatusCompleted} {
		snap := scopeSnapshot("website", fmt.Sprint(990+i), "overflow", []orders.OrderLine{orderLine("700", int64(i+1), 5000000000000000000)})
		snap.TotalMinor = 5000000000000000000
		snap.Canonical = status
		mustReconcile(t, f, snap)
	}
	if status, body := get("/api/v1/dashboard/orders/summary" + params + "&currency=EGP"); status != 500 || strings.Contains(string(body), "5000000000000000000") || strings.Contains(string(body), "SQL") || strings.Contains(string(body), "sk_local_fixture") {
		t.Fatalf("overflow response unsafe status=%d body=%s", status, body)
	}
	if contacts.Load() != 0 {
		t.Fatalf("analytics/health contacted fake providers %d times", contacts.Load())
	}
	t.Log("authenticated overflow-safe HTTP response, currency narrowing, malformed validation and both enabled fake providers: zero network contacts PASS")
	if b, e := exec.Command("podman", "inspect", "--format", "{{.Config.User}}", name).Output(); e != nil || strings.TrimSpace(string(b)) != "65532:65532" {
		t.Fatal("nonroot", e, string(b))
	}
	if b, e := exec.Command("podman", "restart", name).CombinedOutput(); e != nil {
		t.Fatal(e, string(b))
	}
	waitFor(t, 10*time.Second, func() bool { st, _ := get("/health/ready"); return st == 200 }, "actual restart readiness")
	st, _ = get("/api/v1/dashboard/tags" + params)
	if st != 200 {
		t.Fatalf("session/restart %d", st)
	}
	if b, e := exec.Command("podman", "stop", "--time", "10", name).CombinedOutput(); e != nil {
		t.Fatal(e, string(b))
	}
	t.Log("exact image nonroot, schema 27, authenticated analytics, dashboard, restart and bounded graceful shutdown PASS")
	_ = ctx
	_ = d
}
