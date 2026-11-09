package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestStoreDeviceManagementRequiresOTPAndKeepsCredentialsOneTime(t *testing.T) {
	e := newAuthEnv(t)
	owner := fullOwner(t, e.a, e.srv, e.clk, "device-owner@shop.test")
	storeID := uuid.NewString()
	storeBody := map[string]any{"store_id": storeID, "display_name": "New shop", "timezone": "Africa/Cairo"}
	expect := func(h *human, method, path string, body any, want int) map[string]any {
		t.Helper()
		status, result := h.do(method, path, body)
		if status != want {
			t.Fatalf("%s %s: got %d want %d: %v", method, path, status, want, result["error"])
		}
		return result
	}
	fresh := func(h *human) string { h.clk.Advance(30 * time.Second); return h.totp() }
	expect(owner, "POST", "/api/v1/dashboard/stores", storeBody, 403)
	storeBody["mfa_code"] = "000000"
	expect(owner, "POST", "/api/v1/dashboard/stores", storeBody, 403)
	storeBody["mfa_code"] = fresh(owner)
	expect(owner, "POST", "/api/v1/dashboard/stores", storeBody, 201)
	expect(owner, "POST", "/api/v1/dashboard/stores", storeBody, 403)
	if e.count(`SELECT count(*) FROM stores WHERE id=$1`, storeID) != 1 {
		t.Fatal("store not created exactly once")
	}
	limited := owner.createUser(e.srv, "device-admin@shop.test", "admin", false, storeID)
	otherStore := uuid.NewString()
	e.store(otherStore)
	expect(limited, "POST", "/api/v1/dashboard/stores", storeBody, 403)
	expect(limited, "POST", "/api/v1/dashboard/devices", map[string]any{"store_id": otherStore, "name": "Wrong shop", "mfa_code": fresh(limited)}, 403)
	request := map[string]any{"store_id": storeID, "name": "Shop PC", "mfa_code": fresh(limited)}
	if status, _ := limited.raw("POST", "/api/v1/dashboard/devices", request, map[string]string{"Origin": limited.base}); status != 403 {
		t.Fatal("provisioning without CSRF accepted")
	}
	created := expect(limited, "POST", "/api/v1/dashboard/devices", request, 201)
	deviceID, credential := created["device_id"].(string), created["credential"].(string)
	authenticate := func(value string) bool {
		parts := strings.Split(value, ".")
		if len(parts) != 3 {
			return false
		}
		_, _, err := e.a.Devices.Authenticate(context.Background(), parts[0], parts[1], parts[2])
		return err == nil
	}
	if !authenticate(credential) {
		t.Fatal("new credential cannot authenticate")
	}
	if e.count(`SELECT count(*) FROM device_store_bindings WHERE device_id=$1 AND store_id=$2`, deviceID, storeID) != 1 {
		t.Fatal("provisioning did not bind store")
	}
	// Retail registers its own Store ID before uploading. A dashboard-created
	// binding must accept that same identity and refuse any other Store.
	registration := map[string]any{"store_id": storeID, "display_name": "New shop", "timezone": "Africa/Cairo"}
	if status, _ := owner.raw("POST", "/api/v1/sync/store-registration", registration, map[string]string{"Authorization": "Bearer " + credential}); status != 200 {
		t.Fatalf("Retail registration with newly provisioned credential: %d", status)
	}
	registration["store_id"] = otherStore
	if status, _ := owner.raw("POST", "/api/v1/sync/store-registration", registration, map[string]string{"Authorization": "Bearer " + credential}); status != 409 {
		t.Fatalf("new device crossed its Store binding: %d", status)
	}
	list := expect(limited, "GET", "/api/v1/dashboard/devices", nil, 200)
	if strings.Contains(list["_raw"].(string), credential) || strings.Contains(list["_raw"].(string), strings.Split(credential, ".")[2]) {
		t.Fatal("secret leaked in metadata list")
	}
	expect(owner, "GET", "/api/v1/dashboard/devices/"+deviceID+"/credentials", nil, 404)
	outsider := owner.createUser(e.srv, "device-outsider@shop.test", "admin", false, otherStore)
	path := "/api/v1/dashboard/devices/" + deviceID
	expect(outsider, "POST", path+"/credentials/rotate", map[string]string{"mfa_code": fresh(outsider)}, 403)
	expect(outsider, "POST", path+"/revoke", map[string]string{"mfa_code": fresh(outsider)}, 403)
	expect(limited, "POST", path+"/credentials/rotate", map[string]string{}, 403)
	code := fresh(limited)
	rotated := expect(limited, "POST", path+"/credentials/rotate", map[string]string{"mfa_code": code}, 200)
	replacement := rotated["credential"].(string)
	if authenticate(credential) || !authenticate(replacement) {
		t.Fatal("rotation did not replace credential atomically")
	}
	expect(limited, "POST", path+"/credentials/rotate", map[string]string{"mfa_code": code}, 403)
	expect(limited, "POST", path+"/revoke", map[string]string{}, 403)
	expect(limited, "POST", path+"/revoke", map[string]string{"mfa_code": fresh(limited)}, 200)
	if authenticate(replacement) {
		t.Fatal("revoked device still authenticates")
	}
	expect(limited, "POST", path+"/credentials/rotate", map[string]string{"mfa_code": fresh(limited)}, 409)
	legacy, err := e.a.Devices.Create(context.Background(), "Legacy unbound device")
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := "/api/v1/dashboard/devices/" + legacy.Device.ID + "/revoke"
	expect(limited, "POST", legacyPath, map[string]string{"mfa_code": fresh(limited)}, 404)
	expect(owner, "POST", legacyPath, map[string]string{"mfa_code": fresh(owner)}, 200)

	var stored string
	if err := e.a.Pool.QueryRow(context.Background(), `SELECT details::text FROM auth_audit_events WHERE action='device.create' ORDER BY id DESC LIMIT 1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, strings.Split(credential, ".")[2]) {
		t.Fatal("audit contains raw secret")
	}
}
