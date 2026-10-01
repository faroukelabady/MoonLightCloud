package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/store"
)

// memRegistrar is an in-memory registration backend for handler tests,
// mirroring adapter conflict semantics.
type memRegistrar struct {
	stores   map[string]store.RegistrationResult
	bindings map[string]string
}

func (m *memRegistrar) RegisterStore(_ context.Context, deviceID string, request store.RegistrationRequest) (store.RegistrationResult, error) {
	if err := store.ValidateRegistration(request); err != nil {
		return store.RegistrationResult{}, err
	}
	if bound, ok := m.bindings[deviceID]; ok {
		if bound != request.StoreID {
			return store.RegistrationResult{}, apperr.New(apperr.Conflict, "STORE_BINDING_CONFLICT")
		}
		if existing, ok := m.stores[request.StoreID]; ok {
			existing.DisplayName = request.DisplayName
			existing.Timezone = request.Timezone
			m.stores[request.StoreID] = existing
			return existing, nil
		}
	}
	if _, ok := m.stores[request.StoreID]; !ok {
		m.stores[request.StoreID] = store.RegistrationResult{
			StoreID: request.StoreID, DisplayName: request.DisplayName, Timezone: request.Timezone, Bound: true,
		}
	} else {
		existing := m.stores[request.StoreID]
		existing.DisplayName = request.DisplayName
		existing.Timezone = request.Timezone
		m.stores[request.StoreID] = existing
	}
	m.bindings[deviceID] = request.StoreID
	return m.stores[request.StoreID], nil
}

func storeTestSetup(t *testing.T) (http.Handler, string, *memRegistrar) {
	t.Helper()
	authSvc := deviceSvcForTest(newMemRepo())
	p, err := authSvc.Create(context.Background(), "shop-dev")
	if err != nil {
		t.Fatal(err)
	}
	token := p.Device.ID + "." + p.Credential.ID + "." + p.RawSecret
	registrar := &memRegistrar{stores: map[string]store.RegistrationResult{}, bindings: map[string]string{}}
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/sync/store-registration", DeviceAuth(authSvc)(StoreRegistration(registrar)))
	return mux, token, registrar
}

func postRegistration(t *testing.T, h http.Handler, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/sync/store-registration", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestStoreRegistrationAccepted(t *testing.T) {
	h, token, _ := storeTestSetup(t)
	body := `{"store_id":"aaaaaaaa-0000-4000-8000-000000000001","display_name":"Cairo Gallery","timezone":"Africa/Cairo"}`
	rec := postRegistration(t, h, token, body)
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["store_id"] != "aaaaaaaa-0000-4000-8000-000000000001" {
		t.Fatalf("bound: %v", result)
	}
}

func TestStoreRegistrationValidation(t *testing.T) {
	h, token, _ := storeTestSetup(t)
	for name, body := range map[string]string{
		"bad uuid":     `{"store_id":"nope","display_name":"x","timezone":"Africa/Cairo"}`,
		"blank name":   `{"store_id":"aaaaaaaa-0000-4000-8000-000000000001","display_name":" ","timezone":"Africa/Cairo"}`,
		"bad timezone": `{"store_id":"aaaaaaaa-0000-4000-8000-000000000001","display_name":"x","timezone":"UTC+2"}`,
		"malformed":    `{"store_id":`,
	} {
		if rec := postRegistration(t, h, token, body); rec.Code != 400 {
			t.Fatalf("%s: want 400, got %d: %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestStoreRegistrationUnauthenticated(t *testing.T) {
	h, _, _ := storeTestSetup(t)
	body := `{"store_id":"aaaaaaaa-0000-4000-8000-000000000001","display_name":"x","timezone":"Africa/Cairo"}`
	if rec := postRegistration(t, h, "bad", body); rec.Code != 401 {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestStoreRegistrationRevokedDeviceRejected(t *testing.T) {
	authSvc := deviceSvcForTest(newMemRepo())
	p, err := authSvc.Create(context.Background(), "shop-dev")
	if err != nil {
		t.Fatal(err)
	}
	token := p.Device.ID + "." + p.Credential.ID + "." + p.RawSecret
	registrar := &memRegistrar{stores: map[string]store.RegistrationResult{}, bindings: map[string]string{}}
	mux := http.NewServeMux()
	mux.Handle("POST /api/v1/sync/store-registration", DeviceAuth(authSvc)(StoreRegistration(registrar)))
	body := `{"store_id":"aaaaaaaa-0000-4000-8000-000000000001","display_name":"x","timezone":"Africa/Cairo"}`
	if rec := postRegistration(t, mux, token, body); rec.Code != 200 {
		t.Fatalf("pre-revoke: want 200, got %d", rec.Code)
	}
	if err := authSvc.RevokeDevice(context.Background(), p.Device.ID); err != nil {
		t.Fatal(err)
	}
	if rec := postRegistration(t, mux, token, body); rec.Code != 401 {
		t.Fatalf("revoked device: want 401, got %d", rec.Code)
	}
}

func TestStoreRegistrationRawUTF8RejectedBeforeDecode(t *testing.T) {
	h, token, registrar := storeTestSetup(t)
	body := "{\"store_id\":\"aaaaaaaa-0000-4000-8000-000000000001\",\"display_name\":\"" + string([]byte{0xff}) + "\",\"timezone\":\"Africa/Cairo\"}"
	rec := postRegistration(t, h, token, body)
	if rec.Code != 400 || len(registrar.stores) != 0 || len(registrar.bindings) != 0 {
		t.Fatalf("invalid bytes mutated state: %d", rec.Code)
	}
}
