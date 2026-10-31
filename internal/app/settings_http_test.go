package app

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/exchangerate"
	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/settings"
)

// Business settings are readable by every report reader; only all-Stores
// humans with settings.manage change them or see report delivery.
func TestBusinessSettingsHTTP(t *testing.T) {
	e := newAuthEnv(t)
	storeA := uuid.NewString()
	e.store(storeA)
	root := fullOwner(t, e.a, e.srv, e.clk, "root@shop.test")
	ownerA := root.createUser(e.srv, "owner-a@shop.test", "owner", false, storeA)

	expect := func(h *human, method, path string, body any, want int) map[string]any {
		t.Helper()
		code, b := h.do(method, path, body)
		if code != want {
			t.Fatalf("%s %s: %d want %d (%s)", method, path, code, want, b["_raw"])
		}
		return b
	}

	got := expect(ownerA, "GET", "/api/v1/dashboard/settings", nil, 200)
	business := got["business"].(map[string]any)
	if business["returns_enabled"] != false || business["revision"].(float64) != 1 {
		t.Fatalf("defaults: %v", business)
	}
	toggles := map[string]any{"tax_enabled": true, "discount_enabled": false, "returns_enabled": true, "revision": 1}
	expect(ownerA, "PUT", "/api/v1/dashboard/settings/business", toggles, 403)
	saved := expect(root, "PUT", "/api/v1/dashboard/settings/business", toggles, 200)
	if saved["revision"].(float64) != 2 || saved["returns_enabled"] != true {
		t.Fatalf("saved: %v", saved)
	}
	// A second editor holding revision 1 must reload instead of overwriting.
	if b := expect(root, "PUT", "/api/v1/dashboard/settings/business", toggles, 409); !strings.Contains(b["_raw"].(string), "SETTINGS_CHANGED") {
		t.Fatalf("conflict body: %s", b["_raw"])
	}

	expect(ownerA, "POST", "/api/v1/dashboard/settings/exchange-rates", map[string]any{"rate": "50.5"}, 403)
	expect(root, "POST", "/api/v1/dashboard/settings/exchange-rates", map[string]any{"rate": "abc"}, 400)
	rate := expect(root, "POST", "/api/v1/dashboard/settings/exchange-rates",
		map[string]any{"rate": "50.5", "effective_at": "2026-10-01T00:00:00Z", "note": "bank"}, 201)
	if rate["rate"] != "50.5" || rate["rate_microrate"].(float64) != 50_500_000 {
		t.Fatalf("rate: %v", rate)
	}
	listed := expect(ownerA, "GET", "/api/v1/dashboard/settings", nil, 200)
	if rates := listed["exchange_rates"].([]any); len(rates) != 1 {
		t.Fatalf("rates: %v", rates)
	}
	expect(root, "DELETE", "/api/v1/dashboard/settings/exchange-rates/"+rate["id"].(string), nil, 200)

	// Report delivery covers every Store: restricted owners cannot see it.
	expect(ownerA, "GET", "/api/v1/dashboard/settings/messaging", nil, 403)
	messaging := expect(root, "GET", "/api/v1/dashboard/settings/messaging", nil, 200)
	if providers := messaging["providers"].([]any); len(providers) != 0 {
		t.Fatalf("no provider is configured in tests: %v", providers)
	}
	expect(root, "POST", "/api/v1/dashboard/settings/messaging/recipients",
		map[string]any{"label": "Owner", "provider_key": "whatsapp-main", "recipient": "201012345678", "locale": "ar"}, 400)
}

// Cloud fetches the USD->EGP rate from the configured provider (ADR-0058):
// only settings.manage may ask for it, a repeat stores nothing, and a
// provider fault keeps the stored rate.
func TestExchangeRateFetchHTTP(t *testing.T) {
	answer := `{"result":"success","base_code":"USD","rates":{"EGP":48.7123456}}`
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(provider.Close)
	t.Setenv("EXCHANGE_RATE_URL", provider.URL+"/v6/latest/USD")
	e := newAuthEnv(t)
	storeA := uuid.NewString()
	e.store(storeA)
	root := fullOwner(t, e.a, e.srv, e.clk, "root@shop.test")
	ownerA := root.createUser(e.srv, "owner-a@shop.test", "owner", false, storeA)

	code, got := ownerA.do("GET", "/api/v1/dashboard/settings", nil)
	fetch, _ := got["rate_fetch"].(map[string]any)
	if code != 200 || fetch["enabled"] != true || fetch["source"] != "127.0.0.1" || fetch["interval_hours"].(float64) != 12 {
		t.Fatalf("rate_fetch: %d %v", code, got["rate_fetch"])
	}
	if code, _ := ownerA.do("POST", "/api/v1/dashboard/settings/exchange-rates/fetch", nil); code != 403 {
		t.Fatalf("restricted owner fetched: %d", code)
	}
	code, res := root.do("POST", "/api/v1/dashboard/settings/exchange-rates/fetch", nil)
	if code != 200 || res["rate"] != "48.712346" || res["changed"] != true {
		t.Fatalf("fetch: %d %v", code, res)
	}
	// Within a minute a second click reuses the answer.
	if code, res := root.do("POST", "/api/v1/dashboard/settings/exchange-rates/fetch", nil); code != 200 || res["changed"] != false {
		t.Fatalf("repeat: %d %v", code, res)
	}
	_, listed := root.do("GET", "/api/v1/dashboard/settings", nil)
	rates := listed["exchange_rates"].([]any)
	if len(rates) != 1 || rates[0].(map[string]any)["source"] != "127.0.0.1" {
		t.Fatalf("stored: %v", rates)
	}

	// A provider fault after the gap keeps the stored rate.
	answer = `{"result":"error"}`
	fault := settings.NewFetcher(settings.NewService(postgres.NewBusinessSettings(e.a.Pool, 5*time.Second), uuid.NewString, time.Now),
		mustProvider(t, provider.URL), slog.Default())
	if _, err := fault.Refresh(context.Background()); err == nil {
		t.Fatal("provider fault accepted")
	}
	if n := e.count(`SELECT count(*) FROM exchange_rates`); n != 1 {
		t.Fatalf("rows after fault: %d", n)
	}
}

// With EXCHANGE_RATE_URL empty the page hides fetching and the route says so.
func TestExchangeRateFetchOffHTTP(t *testing.T) {
	e := newAuthEnv(t)
	root := fullOwner(t, e.a, e.srv, e.clk, "root@shop.test")
	_, got := root.do("GET", "/api/v1/dashboard/settings", nil)
	if fetch := got["rate_fetch"].(map[string]any); fetch["enabled"] != false || fetch["source"] != "" {
		t.Fatalf("off: %v", fetch)
	}
	code, res := root.do("POST", "/api/v1/dashboard/settings/exchange-rates/fetch", nil)
	if code != 409 || !strings.Contains(res["_raw"].(string), "RATE_FETCH_DISABLED") {
		t.Fatalf("off fetch: %d %v", code, res)
	}
}

func mustProvider(t *testing.T, url string) *exchangerate.Provider {
	t.Helper()
	p, err := exchangerate.New(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
