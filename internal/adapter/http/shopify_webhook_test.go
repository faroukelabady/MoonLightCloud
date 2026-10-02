package http

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
)

type inboxCall struct {
	ProviderKey   commerce.ProviderKey
	DeliveryID    string
	Topic         orders.WebhookTopic
	ExternalOrder string
	PayloadHash   []byte
	WebhookID     *string
}

type stubInbox struct {
	calls     []inboxCall
	conflict  bool
	identical bool
}

func (s *stubInbox) InsertOrderWebhookEvent(_ context.Context, providerKey commerce.ProviderKey, deliveryID string, topic orders.WebhookTopic, externalOrderID string, payloadHash []byte, webhookID *string) (orders.WebhookInsertOutcome, error) {
	s.calls = append(s.calls, inboxCall{
		ProviderKey: providerKey, DeliveryID: deliveryID, Topic: topic,
		ExternalOrder: externalOrderID, PayloadHash: payloadHash, WebhookID: webhookID,
	})
	if s.conflict {
		return 0, apperr.New(apperr.Conflict, "duplicate delivery identity")
	}
	if s.identical {
		return orders.WebhookDuplicateIdentical, nil
	}
	return orders.WebhookInserted, nil
}

const shopifyTestSecret = "test-client-secret-0123456789abcdef"

func signShopify(body []byte) string {
	mac := hmac.New(sha256.New, []byte(shopifyTestSecret))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func shopifyWebhookFixture(t *testing.T) (*ShopifyWebhookHandlers, *stubInbox, *int) {
	t.Helper()
	inbox := &stubInbox{}
	notifies := 0
	handlers := NewShopifyWebhookHandlers(inbox,
		func(providerKey string) (ShopifyWebhookConfig, bool) {
			if providerKey == "shopify-main" {
				return ShopifyWebhookConfig{
					ClientSecret: shopifyTestSecret, ShopDomain: "example.myshopify.com"}, true
			}
			return ShopifyWebhookConfig{}, false
		}, func() { notifies++ }, nil)
	return handlers, inbox, &notifies
}

func postShopifyWebhook(handler http.HandlerFunc, body []byte, mutate func(http.Header)) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/commerce/webhooks/shopify/shopify-main", bytes.NewReader(body))
	request.SetPathValue("provider_key", "shopify-main")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Shopify-Hmac-Sha256", signShopify(body))
	request.Header.Set("X-Shopify-Shop-Domain", "example.myshopify.com")
	request.Header.Set("X-Shopify-Topic", "orders/create")
	request.Header.Set("X-Shopify-Webhook-Id", "b54557e4-bdd9-4b37-8a5f-bf7d70bcd043")
	if mutate != nil {
		mutate(request.Header)
	}
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

var shopifyOrderBody = []byte(`{
  "id": 5231234567890,
  "admin_graphql_api_id": "gid://shopify/Order/5231234567890",
  "email": "buyer@example.com",
  "customer": {"first_name": "Amal", "phone": "+20100"},
  "billing_address": {"address1": "1 Nile St"},
  "line_items": [{"id": 1, "sku": "PAP-001", "quantity": 2}]
}`)

// Phase 11 §173/§111: valid HMAC over the exact raw bytes is accepted,
// dedupable, and wakes the processor.
func TestShopifyWebhookValidSignature(t *testing.T) {
	handlers, inbox, notifies := shopifyWebhookFixture(t)
	recorder := postShopifyWebhook(handlers.ShopifyWebhook, shopifyOrderBody, nil)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d body %s", recorder.Code, recorder.Body.String())
	}
	if len(inbox.calls) != 1 {
		t.Fatalf("inserts = %d", len(inbox.calls))
	}
	call := inbox.calls[0]
	if call.Topic != orders.TopicOrderCreated || call.ExternalOrder != "5231234567890" {
		t.Fatalf("call = %+v", call)
	}
	if call.ProviderKey != "shopify-main" {
		t.Fatalf("provider = %q", call.ProviderKey)
	}
	if *notifies != 1 {
		t.Fatal("processor not woken")
	}
}

// Phase 11 §173: missing/invalid/tampered signatures fail closed with
// zero persistence.
func TestShopifyWebhookSignatureFailures(t *testing.T) {
	cases := map[string]func(http.Header, *[]byte){
		"missing header":  func(h http.Header, _ *[]byte) { h.Del("X-Shopify-Hmac-Sha256") },
		"invalid base64":  func(h http.Header, _ *[]byte) { h.Set("X-Shopify-Hmac-Sha256", "!!!not-base64!!!") },
		"wrong signature": func(h http.Header, _ *[]byte) { h.Set("X-Shopify-Hmac-Sha256", signShopify([]byte("other"))) },
		"empty signature": func(h http.Header, _ *[]byte) { h.Set("X-Shopify-Hmac-Sha256", "") },
		"raw body tamper": func(_ http.Header, body *[]byte) { *body = append(*body, ' ') },
		"different ordering": func(_ http.Header, body *[]byte) {
			*body = []byte(`{"admin_graphql_api_id":"gid://shopify/Order/5231234567890","id":5231234567890}`)
		},
	}
	for name, mutate := range cases {
		handlers, inbox, _ := shopifyWebhookFixture(t)
		transmitted := append([]byte(nil), shopifyOrderBody...)
		request := httptest.NewRequest(http.MethodPost,
			"/api/v1/commerce/webhooks/shopify/shopify-main", nil)
		request.SetPathValue("provider_key", "shopify-main")
		request.Header.Set("Content-Type", "application/json")
		// Signature always covers the ORIGINAL raw bytes.
		request.Header.Set("X-Shopify-Hmac-Sha256", signShopify(shopifyOrderBody))
		request.Header.Set("X-Shopify-Shop-Domain", "example.myshopify.com")
		request.Header.Set("X-Shopify-Topic", "orders/create")
		request.Header.Set("X-Shopify-Webhook-Id", "b54557e4-bdd9-4b37-8a5f-bf7d70bcd043")
		mutate(request.Header, &transmitted)
		request.Body = io.NopCloser(bytes.NewReader(transmitted))
		recorder := httptest.NewRecorder()
		handlers.ShopifyWebhook(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d", name, recorder.Code)
		}
		if len(inbox.calls) != 0 {
			t.Fatalf("%s: %d rows persisted", name, len(inbox.calls))
		}
	}
}

// Phase 11 §123: HMAC is verified before dedupe — an invalid-signature
// duplicate is never accepted.
func TestShopifyWebhookHMACBeforeDedupe(t *testing.T) {
	handlers, inbox, _ := shopifyWebhookFixture(t)
	inbox.identical = true // simulate an already-recorded delivery
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/commerce/webhooks/shopify/shopify-main", bytes.NewReader(shopifyOrderBody))
	request.SetPathValue("provider_key", "shopify-main")
	request.Header.Set("X-Shopify-Hmac-Sha256", signShopify([]byte("forged")))
	request.Header.Set("X-Shopify-Shop-Domain", "example.myshopify.com")
	request.Header.Set("X-Shopify-Topic", "orders/create")
	request.Header.Set("X-Shopify-Webhook-Id", "b54557e4-bdd9-4b37-8a5f-bf7d70bcd043")
	recorder := httptest.NewRecorder()
	handlers.ShopifyWebhook(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", recorder.Code)
	}
	if len(inbox.calls) != 0 {
		t.Fatal("dedupe lookup/persistence ran before authentication")
	}
}

// Phase 11 §117/§174: a correctly signed webhook for another shop must
// not enter this provider instance.
func TestShopifyWebhookShopDomainValidation(t *testing.T) {
	cases := map[string]func(http.Header){
		"different myshopify domain": func(h http.Header) { h.Set("X-Shopify-Shop-Domain", "other.myshopify.com") },
		"missing domain":             func(h http.Header) { h.Del("X-Shopify-Shop-Domain") },
		"malformed domain":           func(h http.Header) { h.Set("X-Shopify-Shop-Domain", "https://example.myshopify.com") },
		"foreign host":               func(h http.Header) { h.Set("X-Shopify-Shop-Domain", "evil.example.com") },
	}
	for name, mutate := range cases {
		handlers, inbox, _ := shopifyWebhookFixture(t)
		recorder := postShopifyWebhook(handlers.ShopifyWebhook, shopifyOrderBody, mutate)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s: status = %d", name, recorder.Code)
		}
		if len(inbox.calls) != 0 {
			t.Fatalf("%s: persisted %d rows", name, len(inbox.calls))
		}
	}
	// The configured domain is accepted.
	handlers, inbox, _ := shopifyWebhookFixture(t)
	if recorder := postShopifyWebhook(handlers.ShopifyWebhook, shopifyOrderBody, nil); recorder.Code != http.StatusAccepted {
		t.Fatal("configured domain rejected")
	}
	if len(inbox.calls) != 1 {
		t.Fatal("no persistence for configured domain")
	}
}

// Phase 11 §118/§176: topics come from the header; cancellation maps to
// the frozen update path; unsupported topics never mutate orders.
func TestShopifyWebhookTopics(t *testing.T) {
	cases := map[string]struct {
		topic   string
		want    orders.WebhookTopic
		wantErr bool
	}{
		"orders/create":    {"orders/create", orders.TopicOrderCreated, false},
		"orders/updated":   {"orders/updated", orders.TopicOrderUpdated, false},
		"orders/cancelled": {"orders/cancelled", orders.TopicOrderUpdated, false},
		"orders/delete":    {"orders/delete", orders.TopicOrderDeleted, false},
		"unsupported":      {"products/update", "", true},
		"body topic only":  {"", "", true},
	}
	for name, tc := range cases {
		handlers, inbox, _ := shopifyWebhookFixture(t)
		recorder := postShopifyWebhook(handlers.ShopifyWebhook, shopifyOrderBody,
			func(h http.Header) {
				if tc.topic == "" {
					h.Del("X-Shopify-Topic")
				} else {
					h.Set("X-Shopify-Topic", tc.topic)
				}
			})
		if tc.wantErr {
			if recorder.Code != http.StatusBadRequest || len(inbox.calls) != 0 {
				t.Fatalf("%s: status = %d calls = %d", name, recorder.Code, len(inbox.calls))
			}
			continue
		}
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("%s: status = %d", name, recorder.Code)
		}
		if inbox.calls[0].Topic != tc.want {
			t.Fatalf("%s: topic = %s", name, inbox.calls[0].Topic)
		}
	}
}

// Phase 11 §115/§175: delivery identity dedupe semantics — identical
// redelivery is idempotent, hash conflicts are 409, distinct deliveries
// of the same payload both record.
func TestShopifyWebhookDeliveryDedupe(t *testing.T) {
	handlers, inbox, _ := shopifyWebhookFixture(t)
	inbox.identical = true
	if recorder := postShopifyWebhook(handlers.ShopifyWebhook, shopifyOrderBody, nil); recorder.Code != http.StatusAccepted {
		t.Fatalf("identical redelivery status = %d", recorder.Code)
	}
	inbox2 := &stubInbox{conflict: true}
	notifies := 0
	handlers2 := NewShopifyWebhookHandlers(inbox2,
		func(providerKey string) (ShopifyWebhookConfig, bool) {
			return ShopifyWebhookConfig{ClientSecret: shopifyTestSecret, ShopDomain: "example.myshopify.com"}, true
		}, func() { notifies++ }, nil)
	if recorder := postShopifyWebhook(handlers2.ShopifyWebhook, shopifyOrderBody, nil); recorder.Code != http.StatusConflict {
		t.Fatalf("same id different hash status = %d", recorder.Code)
	}
	// Different delivery ids, same payload: both record.
	handlers3, inbox3, _ := shopifyWebhookFixture(t)
	postShopifyWebhook(handlers3.ShopifyWebhook, shopifyOrderBody, nil)
	postShopifyWebhook(handlers3.ShopifyWebhook, shopifyOrderBody,
		func(h http.Header) { h.Set("X-Shopify-Webhook-Id", "b54557e4-bdd9-4b37-8a5f-bf7d70bcd044") })
	if len(inbox3.calls) != 2 {
		t.Fatalf("distinct deliveries = %d", len(inbox3.calls))
	}
}

// Phase 11 §113/§173: oversized deliveries are refused before signature
// processing with zero DB writes.
func TestShopifyWebhookBodyLimit(t *testing.T) {
	handlers, inbox, _ := shopifyWebhookFixture(t)
	body := append([]byte(`{"pad":"`), bytes.Repeat([]byte("x"), shopifyWebhookMaxBodyBytes)...)
	body = append(body, []byte(`"}`)...)
	recorder := postShopifyWebhook(handlers.ShopifyWebhook, body, nil)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", recorder.Code)
	}
	if len(inbox.calls) != 0 {
		t.Fatal("oversized delivery persisted")
	}
}

// Phase 11 §116/§177: the inbox receives only bounded durable metadata —
// no raw body, no customer PII, no subscription metadata.
func TestShopifyWebhookInboxCarriesNoPII(t *testing.T) {
	handlers, inbox, _ := shopifyWebhookFixture(t)
	recorder := postShopifyWebhook(handlers.ShopifyWebhook, shopifyOrderBody, nil)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d", recorder.Code)
	}
	call := inbox.calls[0]
	if call.WebhookID != nil {
		t.Fatal("subscription metadata persisted")
	}
	if len(call.PayloadHash) != sha256.Size {
		t.Fatalf("payload hash = %d bytes", len(call.PayloadHash))
	}
	// Every persisted field is checked: nothing customer-derived.
	for _, value := range []string{call.DeliveryID, call.ExternalOrder, string(call.Topic)} {
		for _, pii := range []string{"Amal", "buyer@example.com", "+20100", "Nile", "PAP-001"} {
			if strings.Contains(value, pii) {
				t.Fatalf("PII %q leaked into inbox field %q", pii, value)
			}
		}
	}
	if strings.Contains(string(call.PayloadHash), "Amal") {
		t.Fatal("raw payload persisted")
	}
}

// Phase 11 §108/§123: unknown provider keys are absent (404), and
// invalid order identities never persist.
func TestShopifyWebhookProviderAndIdentity(t *testing.T) {
	handlers, inbox, _ := shopifyWebhookFixture(t)
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/commerce/webhooks/shopify/unknown", bytes.NewReader(shopifyOrderBody))
	request.SetPathValue("provider_key", "unknown")
	recorder := httptest.NewRecorder()
	handlers.ShopifyWebhook(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown provider status = %d", recorder.Code)
	}

	badBody := []byte(`{"id":"drop table"}`)
	if recorder := postShopifyWebhook(handlers.ShopifyWebhook, badBody, nil); recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid identity status = %d", recorder.Code)
	}
	if len(inbox.calls) != 0 {
		t.Fatal("invalid identity persisted")
	}
}
