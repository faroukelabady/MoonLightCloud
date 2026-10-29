package app

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// Product, price and catalog changes need a fresh authenticator code on
// top of the FULL session: missing or wrong codes are refused (403, the
// session stays valid), a code approves at most one change, and online
// selling needs none. With no shop device allowing the change, an approved
// request still stops at CLOUD_PERMISSION_DISABLED, so nothing is queued.
func TestCatalogChangesNeedAFreshAuthenticatorCode(t *testing.T) {
	e := newAuthEnv(t)
	owner := fullOwner(t, e.a, e.srv, e.clk, "owner@shop.test")
	storeID, productID := uuid.NewString(), uuid.NewString()
	details := func(code string) map[string]any {
		return map[string]any{"store_id": storeID, "type": "catalog.product.details.update.v1", "entity_id": productID,
			"expected_revision": 1, "payload": map[string]any{"product_id": productID}, "mfa_code": code}
	}
	if code, body := owner.do("POST", "/api/v1/dashboard/catalog-admin/commands", details("")); code != 403 || errCode(body) != "MFA_REQUIRED" {
		t.Fatalf("no code: %d %s", code, body["_raw"])
	}
	if code, body := owner.do("POST", "/api/v1/dashboard/catalog-admin/commands", details("000000")); code != 403 || errCode(body) != "MFA_INVALID" {
		t.Fatalf("wrong code: %d %s", code, body["_raw"])
	}
	// The session survived both refusals.
	if code, _ := owner.do("GET", "/api/v1/dashboard/auth/me", nil); code != 200 {
		t.Fatalf("session lost after a refused code: %d", code)
	}
	owner.clk.Advance(30 * time.Second)
	fresh := owner.totp()
	if code, body := owner.do("POST", "/api/v1/dashboard/catalog-admin/commands", details(fresh)); code != 409 || errCode(body) != "CLOUD_PERMISSION_DISABLED" {
		t.Fatalf("approved change without an allowing device: %d %s", code, body["_raw"])
	}
	if code, body := owner.do("POST", "/api/v1/dashboard/catalog-admin/commands", details(fresh)); code != 403 || errCode(body) != "MFA_INVALID" {
		t.Fatalf("replayed code: %d %s", code, body["_raw"])
	}
	online := map[string]any{"store_id": storeID, "type": "catalog.product.online-policy.update.v1", "entity_id": productID,
		"expected_revision": 1, "payload": map[string]any{"product_id": productID, "sell_online": false}}
	if code, body := owner.do("POST", "/api/v1/dashboard/catalog-admin/commands", online); code != 409 || errCode(body) != "CLOUD_PERMISSION_DISABLED" {
		t.Fatalf("online selling skips MFA and stops at permissions: %d %s", code, body["_raw"])
	}
	if code, body := owner.do("GET", "/api/v1/dashboard/catalog-admin/permissions?store_id="+storeID, nil); code != 200 || body["devices"] != float64(0) || body["products"] != false {
		t.Fatalf("permissions: %d %s", code, body["_raw"])
	}
}
