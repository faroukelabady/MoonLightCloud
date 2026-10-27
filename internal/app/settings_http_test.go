package app

import (
	"strings"
	"testing"

	"github.com/google/uuid"
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
