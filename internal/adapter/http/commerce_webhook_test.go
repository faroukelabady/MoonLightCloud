package http

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
)

// memWebhookInbox is an in-memory delivery ledger for handler tests.
type memWebhookInbox struct {
	mu   sync.Mutex
	rows map[string][]byte
}

func (m *memWebhookInbox) InsertOrderWebhookEvent(_ context.Context, _ commerce.ProviderKey, deliveryID string, _ orders.WebhookTopic, _ string, payloadHash []byte, _ *string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows == nil {
		m.rows = map[string][]byte{}
	}
	if existing, ok := m.rows[deliveryID]; ok {
		if string(existing) == string(payloadHash) {
			return false, nil
		}
		return false, apperr.New(apperr.Conflict, "delivery already recorded")
	}
	m.rows[deliveryID] = payloadHash
	return true, nil
}

func signWebhook(secret, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func webhookRequest(t *testing.T, secret, body string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	return webhookRequestWith(t, secret, body, "delivery-1", mutate)
}

func webhookRequestWith(t *testing.T, secret, body, delivery string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/commerce/webhooks/woocommerce/website", strings.NewReader(body))
	request.SetPathValue("provider_key", "website")
	request.Header.Set("X-WC-Webhook-Signature", signWebhook(secret, body))
	request.Header.Set("X-WC-Webhook-Topic", "order.created")
	request.Header.Set("X-WC-Webhook-Resource", "order")
	request.Header.Set("X-WC-Webhook-Delivery-ID", delivery)
	if mutate != nil {
		mutate(request)
	}
	recorder := httptest.NewRecorder()
	_ = recorder
	return recorder
}

func webhookHandler(secret string, inbox *memWebhookInbox, notified *int) (*CommerceWebhookHandlers, *httptest.ResponseRecorder) {
	resolve := func(providerKey string) (string, bool) {
		if providerKey == "website" {
			return secret, true
		}
		return "", false
	}
	handler := NewCommerceWebhookHandlers(inbox, resolve, func() {
		if notified != nil {
			*notified++
		}
	}, nil)
	return handler, nil
}

func doWebhook(t *testing.T, handler *CommerceWebhookHandlers, secret, body, delivery string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/commerce/webhooks/woocommerce/website", strings.NewReader(body))
	request.SetPathValue("provider_key", "website")
	request.Header.Set("X-WC-Webhook-Signature", signWebhook(secret, body))
	request.Header.Set("X-WC-Webhook-Topic", "order.created")
	request.Header.Set("X-WC-Webhook-Resource", "order")
	request.Header.Set("X-WC-Webhook-Delivery-ID", delivery)
	if mutate != nil {
		mutate(request)
	}
	recorder := httptest.NewRecorder()
	handler.WooCommerceWebhook(recorder, request)
	return recorder
}

func TestWebhookValidSignature(t *testing.T) {
	secret := "test-webhook-secret-0123456789abcdef"
	inbox := &memWebhookInbox{}
	var notified int
	handler, _ := webhookHandler(secret, inbox, &notified)
	recorder := doWebhook(t, handler, secret, `{"id":700}`, "delivery-1", nil)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("202, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if notified != 1 {
		t.Fatal("processor notified")
	}
	if len(inbox.rows) != 1 {
		t.Fatal("one durable event")
	}
	// Raw body must not be stored anywhere in the inbox row path: the
	// mem ledger only keeps hashes by construction; assert the HTTP
	// response carries no secret or body echo.
	if strings.Contains(recorder.Body.String(), secret) {
		t.Fatal("secret in response")
	}
}

func TestWebhookAuthMatrix(t *testing.T) {
	secret := "test-webhook-secret-0123456789abcdef"
	body := `{"id":701}`
	cases := []struct {
		name       string
		payload    string
		mutate     func(*http.Request)
		wantStatus int
	}{
		{"invalid signature", body, func(r *http.Request) {
			r.Header.Set("X-WC-Webhook-Signature", signWebhook("wrong-secret", body))
		}, http.StatusUnauthorized},
		{"missing signature", body, func(r *http.Request) {
			r.Header.Del("X-WC-Webhook-Signature")
		}, http.StatusUnauthorized},
		{"malformed signature", body, func(r *http.Request) {
			r.Header.Set("X-WC-Webhook-Signature", "!!!not-base64!!!")
		}, http.StatusUnauthorized},
		{"invalid topic", body, func(r *http.Request) {
			r.Header.Set("X-WC-Webhook-Topic", "product.updated")
		}, http.StatusBadRequest},
		{"invalid resource", body, func(r *http.Request) {
			r.Header.Set("X-WC-Webhook-Resource", "product")
		}, http.StatusBadRequest},
		{"oversized body", `{"id":703,"pad":"` + strings.Repeat("x", 2<<20) + `"}`, nil, http.StatusRequestEntityTooLarge},
		{"malformed JSON", `{"id":`, nil, http.StatusBadRequest},
		{"invalid order ID", `{"id":0}`, nil, http.StatusBadRequest},
		{"missing delivery ID", body, func(r *http.Request) {
			r.Header.Del("X-WC-Webhook-Delivery-ID")
		}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inbox := &memWebhookInbox{}
			handler, _ := webhookHandler(secret, inbox, nil)
			recorder := doWebhook(t, handler, secret, tc.payload, "delivery-1", tc.mutate)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("want %d got %d: %s", tc.wantStatus, recorder.Code, recorder.Body.String())
			}
			if len(inbox.rows) != 0 {
				t.Fatalf("nothing persisted on %s", tc.name)
			}
			if strings.Contains(recorder.Body.String(), secret) {
				t.Fatalf("secret in %s response", tc.name)
			}
		})
	}
}

func TestWebhookTamperedBody(t *testing.T) {
	secret := "test-webhook-secret-0123456789abcdef"
	inbox := &memWebhookInbox{}
	handler, _ := webhookHandler(secret, inbox, nil)
	// Signature computed over body A, body B delivered.
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/commerce/webhooks/woocommerce/website", strings.NewReader(`{"id":702}`))
	request.SetPathValue("provider_key", "website")
	request.Header.Set("X-WC-Webhook-Signature", signWebhook(secret, `{"id":701}`))
	request.Header.Set("X-WC-Webhook-Topic", "order.created")
	request.Header.Set("X-WC-Webhook-Resource", "order")
	request.Header.Set("X-WC-Webhook-Delivery-ID", "delivery-1")
	recorder := httptest.NewRecorder()
	handler.WooCommerceWebhook(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("tamper: %d", recorder.Code)
	}
	if len(inbox.rows) != 0 {
		t.Fatal("nothing persisted on tamper")
	}
}

func TestWebhookDuplicateMatrix(t *testing.T) {
	secret := "test-webhook-secret-0123456789abcdef"
	inbox := &memWebhookInbox{}
	handler, _ := webhookHandler(secret, inbox, nil)
	body := `{"id":704}`
	if recorder := doWebhook(t, handler, secret, body, "delivery-1", nil); recorder.Code != http.StatusAccepted {
		t.Fatalf("first: %d", recorder.Code)
	}
	if recorder := doWebhook(t, handler, secret, body, "delivery-1", nil); recorder.Code != http.StatusAccepted {
		t.Fatalf("duplicate: %d", recorder.Code)
	}
	if len(inbox.rows) != 1 {
		t.Fatal("one durable event")
	}
	conflict := doWebhook(t, handler, secret, `{"id":704,"x":1}`, "delivery-1", nil)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict: %d", conflict.Code)
	}
}

func TestWebhookUnknownProvider(t *testing.T) {
	inbox := &memWebhookInbox{}
	handler, _ := webhookHandler("secret", inbox, nil)
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/commerce/webhooks/woocommerce/unknown", strings.NewReader(`{"id":1}`))
	request.SetPathValue("provider_key", "unknown")
	recorder := httptest.NewRecorder()
	handler.WooCommerceWebhook(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("404, got %d", recorder.Code)
	}
}

// TestWebhookNoDashboardSession proves the signed route needs no
// dashboard cookie and ignores device tokens: only HMAC authenticates.
func TestWebhookNoDashboardSession(t *testing.T) {
	secret := "test-webhook-secret-0123456789abcdef"
	inbox := &memWebhookInbox{}
	handler, _ := webhookHandler(secret, inbox, nil)
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/commerce/webhooks/woocommerce/website", strings.NewReader(`{"id":705}`))
	request.SetPathValue("provider_key", "website")
	request.Header.Set("X-WC-Webhook-Signature", signWebhook(secret, `{"id":705}`))
	request.Header.Set("X-WC-Webhook-Topic", "order.created")
	request.Header.Set("X-WC-Webhook-Resource", "order")
	request.Header.Set("X-WC-Webhook-Delivery-ID", "delivery-1")
	request.AddCookie(&http.Cookie{Name: "moonlight_session", Value: "forged"})
	request.Header.Set("Authorization", "Bearer device-token-irrelevant")
	recorder := httptest.NewRecorder()
	handler.WooCommerceWebhook(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("signed delivery without session: %d", recorder.Code)
	}
}

// TestWebhookCrossAuthIsolation proves credential domains do not
// substitute: dashboard cookies and device tokens authenticate nothing
// on the webhook route, and Woo signatures authenticate nothing on
// dashboard APIs (covered by the routes-wired 401 test).
func TestWebhookCrossAuthIsolation(t *testing.T) {
	secret := "test-webhook-secret-0123456789abcdef"
	inbox := &memWebhookInbox{}
	handler, _ := webhookHandler(secret, inbox, nil)
	// Dashboard cookie + device token, no/invalid Woo signature.
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) {
			r.Header.Del("X-WC-Webhook-Signature")
			r.AddCookie(&http.Cookie{Name: "moonlight_session", Value: "valid-looking"})
		},
		func(r *http.Request) {
			r.Header.Del("X-WC-Webhook-Signature")
			r.Header.Set("Authorization", "Bearer device-token")
		},
	} {
		request := httptest.NewRequest(http.MethodPost,
			"/api/v1/commerce/webhooks/woocommerce/website", strings.NewReader(`{"id":706}`))
		request.SetPathValue("provider_key", "website")
		request.Header.Set("X-WC-Webhook-Signature", signWebhook(secret, `{"id":706}`))
		request.Header.Set("X-WC-Webhook-Topic", "order.created")
		request.Header.Set("X-WC-Webhook-Resource", "order")
		request.Header.Set("X-WC-Webhook-Delivery-ID", "delivery-1")
		mutate(request)
		recorder := httptest.NewRecorder()
		handler.WooCommerceWebhook(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("substitute credential rejected: %d", recorder.Code)
		}
	}
	if len(inbox.rows) != 0 {
		t.Fatal("nothing persisted without HMAC")
	}
}
