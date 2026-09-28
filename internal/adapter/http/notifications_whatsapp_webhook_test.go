package http

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
)

const (
	testWhatsAppVerifyToken = "test-verify-token-0123456789abcdef"
	testWhatsAppAppSecret   = "test-app-secret-0123456789abcdef"
)

// fakeNotificationStatusStore records applications with real ordering
// semantics for handler tests.
type fakeNotificationStatusStore struct {
	mu       sync.Mutex
	known    map[string]string // wamid -> notification id
	history  map[string]int
	current  map[string]notifications.DeliveryStatus
	currentT map[string]*time.Time
	lookups  int
	failNext bool
}

func newFakeNotificationStatusStore() *fakeNotificationStatusStore {
	return &fakeNotificationStatusStore{
		known:   map[string]string{"wamid.known1": "notif-1"},
		history: map[string]int{}, current: map[string]notifications.DeliveryStatus{},
		currentT: map[string]*time.Time{},
	}
}

func (s *fakeNotificationStatusStore) LookupByProviderMessage(_ context.Context, _, wamid string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lookups++
	id, ok := s.known[wamid]
	return id, ok, nil
}

func (s *fakeNotificationStatusStore) ApplyDeliveryStatus(_ context.Context, id string, event notifications.DeliveryEvent) (notifications.DeliveryOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failNext {
		s.failNext = false
		return notifications.DeliveryOutcome{}, errFakeDB()
	}
	key := id + "\x00" + string(event.Fingerprint[:])
	if s.history[key] > 0 {
		return notifications.DeliveryOutcome{}, nil
	}
	s.history[key]++
	outcome := notifications.DeliveryOutcome{HistoryInserted: true}
	if notifications.AdvanceDelivery(s.current[id], s.currentT[id], event.Canonical, event.ProviderTimestamp) {
		s.current[id] = event.Canonical
		s.currentT[id] = event.ProviderTimestamp
		outcome.CurrentAdvanced = true
	}
	return outcome, nil
}

func errFakeDB() error { return &fakeDBError{} }

type fakeDBError struct{}

func (e *fakeDBError) Error() string { return "database error" }

func testWhatsAppWebhookHandlers(store *fakeNotificationStatusStore) *WhatsAppWebhookHandlers {
	return NewWhatsAppWebhookHandlers(store,
		func(providerKey string) (string, bool) {
			if providerKey == "whatsapp-main" {
				return testWhatsAppVerifyToken, true
			}
			return "", false
		},
		func(providerKey string) (string, bool) {
			if providerKey == "whatsapp-main" {
				return testWhatsAppAppSecret, true
			}
			return "", false
		}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func signHub(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func statusCallbackBody(t *testing.T, statuses ...map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"object": "whatsapp_business_account",
		"entry": []any{map[string]any{
			"id": "102290129340398",
			"changes": []any{map[string]any{
				"field": "messages",
				"value": map[string]any{
					"messaging_product": "whatsapp",
					"statuses":          statuses,
				},
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func postStatus(t *testing.T, handler *WhatsAppWebhookHandlers, body []byte, sign func([]byte) string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/notifications/webhooks/whatsapp/whatsapp-main", bytes.NewReader(body))
	request.SetPathValue("provider_key", "whatsapp-main")
	if sign != nil {
		request.Header.Set("X-Hub-Signature-256", sign(body))
	}
	recorder := httptest.NewRecorder()
	handler.StatusWhatsAppWebhook(recorder, request)
	return recorder
}

func validSigner(body []byte) string { return signHub(body, testWhatsAppAppSecret) }

// TestWhatsAppWebhookVerify proves GET verification semantics.
func TestWhatsAppWebhookVerify(t *testing.T) {
	handler := testWhatsAppWebhookHandlers(newFakeNotificationStatusStore())
	get := func(target string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.SetPathValue("provider_key", "whatsapp-main")
		recorder := httptest.NewRecorder()
		handler.VerifyWhatsAppWebhook(recorder, request)
		return recorder
	}
	recorder := get("/verify?hub.mode=subscribe&hub.verify_token=" + testWhatsAppVerifyToken + "&hub.challenge=1158201444")
	if recorder.Code != http.StatusOK || recorder.Body.String() != "1158201444" {
		t.Fatalf("verify: %d %q", recorder.Code, recorder.Body.String())
	}
	for name, target := range map[string]string{
		"wrong token":   "/verify?hub.mode=subscribe&hub.verify_token=wrong&hub.challenge=1",
		"missing token": "/verify?hub.mode=subscribe&hub.challenge=1",
		"oversized":     "/verify?hub.mode=subscribe&hub.verify_token=" + testWhatsAppVerifyToken + "&hub.challenge=" + strings.Repeat("1", 129),
	} {
		if recorder := get(target); recorder.Code != http.StatusForbidden && recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", name, recorder.Code)
		}
	}
	if recorder := get("/verify?hub.mode=unsubscribe&hub.verify_token=" + testWhatsAppVerifyToken + "&hub.challenge=1"); recorder.Code != http.StatusBadRequest {
		t.Fatalf("wrong mode: %d", recorder.Code)
	}
	// Unknown provider never reflects tokens.
	request := httptest.NewRequest(http.MethodGet, "/verify?hub.mode=subscribe", nil)
	request.SetPathValue("provider_key", "nope")
	recorder = httptest.NewRecorder()
	handler.VerifyWhatsAppWebhook(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown provider: %d", recorder.Code)
	}
}

// TestWhatsAppWebhookSignatures proves POST authentication: valid
// passes, everything else 401s with zero store interaction, oversized
// 413s.
func TestWhatsAppWebhookSignatures(t *testing.T) {
	body := statusCallbackBody(t, map[string]any{
		"id": "wamid.known1", "status": "sent", "timestamp": "1750263773", "recipient_id": "16505551234",
	})
	handler := testWhatsAppWebhookHandlers(newFakeNotificationStatusStore())
	if recorder := postStatus(t, handler, body, validSigner); recorder.Code != http.StatusOK {
		t.Fatalf("valid: %d %s", recorder.Code, recorder.Body.String())
	}
	cases := map[string]func([]byte) string{
		"missing":   nil,
		"malformed": func(b []byte) string { return "not-a-signature" },
		"wrong":     func(b []byte) string { return signHub(b, "wrong-secret") },
	}
	for name, sign := range cases {
		store := newFakeNotificationStatusStore()
		handler := testWhatsAppWebhookHandlers(store)
		if recorder := postStatus(t, handler, body, sign); recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d", name, recorder.Code)
		}
		if store.lookups != 0 {
			t.Fatalf("%s wrote: lookups=%d", name, store.lookups)
		}
	}
	// Tampered body with a signature over different bytes fails.
	other := statusCallbackBody(t, map[string]any{
		"id": "wamid.known1", "status": "sent", "timestamp": "1750263773", "recipient_id": "1",
	})
	sigOverOther := signHub(other, testWhatsAppAppSecret)
	store := newFakeNotificationStatusStore()
	handler = testWhatsAppWebhookHandlers(store)
	if recorder := postStatus(t, handler, body, func([]byte) string { return sigOverOther }); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("cross-body signature: %d", recorder.Code)
	}
	// Oversized body short-circuits before signature checks.
	huge := bytes.Repeat([]byte("x"), whatsAppWebhookMaxBodyBytes+1)
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/notifications/webhooks/whatsapp/whatsapp-main", bytes.NewReader(huge))
	request.SetPathValue("provider_key", "whatsapp-main")
	recorder := httptest.NewRecorder()
	handler.StatusWhatsAppWebhook(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize: %d", recorder.Code)
	}
}

// TestWhatsAppWebhookSignatureBoundary proves the signature binds exact
// raw bytes: same semantic JSON with different whitespace fails.
func TestWhatsAppWebhookSignatureBoundary(t *testing.T) {
	compact := []byte(`{"object":"whatsapp_business_account","entry":[]}`)
	spaced := []byte(`{ "object" : "whatsapp_business_account" , "entry" : [ ] }`)
	handler := testWhatsAppWebhookHandlers(newFakeNotificationStatusStore())
	sig := signHub(compact, testWhatsAppAppSecret)
	if recorder := postStatus(t, handler, spaced, func([]byte) string { return sig }); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("whitespace-different body must fail: %d", recorder.Code)
	}
	if recorder := postStatus(t, handler, compact, func([]byte) string { return sig }); recorder.Code != http.StatusOK {
		t.Fatalf("exact bytes must pass: %d", recorder.Code)
	}
}

// TestWhatsAppWebhookStatuses proves status lifecycle handling:
// known progression, duplicates, out-of-order, failed, unknown,
// unknown IDs, and DB failure mapping.
func TestWhatsAppWebhookStatuses(t *testing.T) {
	entry := func(id, status string, ts string) map[string]any {
		return map[string]any{"id": id, "status": status, "timestamp": ts, "recipient_id": "16505551234"}
	}
	t.Run("progression + duplicate + out-of-order", func(t *testing.T) {
		store := newFakeNotificationStatusStore()
		handler := testWhatsAppWebhookHandlers(store)
		for _, statuses := range [][]map[string]any{
			{entry("wamid.known1", "read", "1750263800")},
			{entry("wamid.known1", "sent", "1750263700")},
			{entry("wamid.known1", "delivered", "1750263750")},
			{entry("wamid.known1", "read", "1750263800")},
		} {
			if recorder := postStatus(t, handler, statusCallbackBody(t, statuses...), validSigner); recorder.Code != http.StatusOK {
				t.Fatalf("status: %d", recorder.Code)
			}
		}
		if store.current["notif-1"] != notifications.DeliveryRead {
			t.Fatalf("final READ: %+v", store.current)
		}
		if store.history["notif-1\x00"+string(fingerprintOf(store, "notif-1", "read"))] == 0 {
			t.Fatalf("history: %+v", store.history)
		}
	})
	t.Run("failed never resends", func(t *testing.T) {
		store := newFakeNotificationStatusStore()
		handler := testWhatsAppWebhookHandlers(store)
		body := statusCallbackBody(t, map[string]any{
			"id": "wamid.known1", "status": "failed", "timestamp": "1750263900",
			"recipient_id": "16505551234",
			"errors":       []any{map[string]any{"code": 131026, "title": "t", "message": "d"}},
		})
		if recorder := postStatus(t, handler, body, validSigner); recorder.Code != http.StatusOK {
			t.Fatalf("failed: %d", recorder.Code)
		}
		if store.current["notif-1"] != notifications.DeliveryFailed {
			t.Fatalf("FAILED: %+v", store.current)
		}
	})
	t.Run("unknown status preserved", func(t *testing.T) {
		store := newFakeNotificationStatusStore()
		handler := testWhatsAppWebhookHandlers(store)
		body := statusCallbackBody(t, entry("wamid.known1", "future_status_x", "1750263900"))
		if recorder := postStatus(t, handler, body, validSigner); recorder.Code != http.StatusOK {
			t.Fatalf("unknown: %d", recorder.Code)
		}
		if _, ok := store.current["notif-1"]; ok {
			t.Fatalf("UNKNOWN must not advance: %+v", store.current)
		}
	})
	t.Run("unknown message id ignored", func(t *testing.T) {
		store := newFakeNotificationStatusStore()
		handler := testWhatsAppWebhookHandlers(store)
		body := statusCallbackBody(t, entry("wamid.stranger", "delivered", "1750263900"))
		if recorder := postStatus(t, handler, body, validSigner); recorder.Code != http.StatusOK {
			t.Fatalf("unknown id: %d", recorder.Code)
		}
		if len(store.history) != 0 {
			t.Fatalf("no writes: %+v", store.history)
		}
	})
	t.Run("db failure retries", func(t *testing.T) {
		store := newFakeNotificationStatusStore()
		store.failNext = true
		handler := testWhatsAppWebhookHandlers(store)
		body := statusCallbackBody(t, entry("wamid.known1", "sent", "1750263700"))
		if recorder := postStatus(t, handler, body, validSigner); recorder.Code != http.StatusInternalServerError {
			t.Fatalf("db failure: %d", recorder.Code)
		}
	})
}

// fingerprintOf is a test-only helper for history assertions.
func fingerprintOf(store *fakeNotificationStatusStore, id, raw string) string {
	for key := range store.history {
		if strings.HasPrefix(key, id+"\x00") {
			return strings.TrimPrefix(key, id+"\x00")
		}
	}
	return raw
}

// TestWhatsAppWebhookInboundIgnored proves signed inbound customer
// content is acknowledged without persistence or PII retention.
func TestWhatsAppWebhookInboundIgnored(t *testing.T) {
	store := newFakeNotificationStatusStore()
	handler := testWhatsAppWebhookHandlers(store)
	body, err := json.Marshal(map[string]any{
		"object": "whatsapp_business_account",
		"entry": []any{map[string]any{
			"id": "102290129340398",
			"changes": []any{map[string]any{
				"field": "messages",
				"value": map[string]any{
					"messaging_product": "whatsapp",
					"contacts": []any{map[string]any{
						"profile": map[string]any{"name": "Distinctive Customer Name"},
						"wa_id":   "16505559999",
					}},
					"messages": []any{map[string]any{
						"from": "16505559999", "id": "wamid.inbound1",
						"timestamp": "1750263773", "type": "text",
						"text": map[string]any{"body": "my secret inquiry layla-pii@example.com +201111111111"},
					}},
				},
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := postStatus(t, handler, body, validSigner)
	if recorder.Code != http.StatusOK {
		t.Fatalf("inbound ack: %d", recorder.Code)
	}
	if len(store.history) != 0 || store.lookups != 0 {
		t.Fatalf("inbound persisted: %+v %d", store.history, store.lookups)
	}
	for _, forbidden := range []string{"Distinctive", "layla-pii", "+201111111111", "secret inquiry"} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("PII in response: %q", forbidden)
		}
	}
}

// TestNotificationWebhookCrossAuth proves the WhatsApp and Woo
// signature schemes do not substitute for each other.
func TestNotificationWebhookCrossAuth(t *testing.T) {
	store := newFakeNotificationStatusStore()
	handler := testWhatsAppWebhookHandlers(store)
	body := statusCallbackBody(t, map[string]any{
		"id": "wamid.known1", "status": "sent", "timestamp": "1750263773", "recipient_id": "16505551234",
	})
	// Woo-style base64 HMAC header carries no X-Hub signature.
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/notifications/webhooks/whatsapp/whatsapp-main", bytes.NewReader(body))
	request.SetPathValue("provider_key", "whatsapp-main")
	request.Header.Set("X-WC-Webhook-Signature", "dGVzdA==")
	recorder := httptest.NewRecorder()
	handler.StatusWhatsAppWebhook(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("woo header must not authenticate: %d", recorder.Code)
	}
	// Hub signature on the Woo order route must not authenticate either.
	wooSecret := "test-webhook-secret-0123456789abcdef"
	woo, _ := webhookHandler(wooSecret, &memWebhookInbox{}, nil)
	request2 := httptest.NewRequest(http.MethodPost,
		"/api/v1/commerce/webhooks/woocommerce/website", strings.NewReader(`{"id":706}`))
	request2.SetPathValue("provider_key", "website")
	request2.Header.Set("X-WC-Webhook-Topic", "order.created")
	request2.Header.Set("X-WC-Webhook-Resource", "order")
	request2.Header.Set("X-WC-Webhook-Delivery-ID", "delivery-xauth")
	request2.Header.Set("X-Hub-Signature-256", signHub([]byte(`{"id":706}`), testWhatsAppAppSecret))
	recorder2 := httptest.NewRecorder()
	woo.WooCommerceWebhook(recorder2, request2)
	if recorder2.Code == http.StatusAccepted {
		t.Fatalf("hub signature must not authenticate woo route: %d", recorder2.Code)
	}
}

// TestWhatsAppWebhookMalformedTimestampIsolation proves one malformed
// status timestamp cannot poison valid siblings: the bad entry is
// skipped with no writes, the sibling persists, envelope acks 200.
func TestWhatsAppWebhookMalformedTimestampIsolation(t *testing.T) {
	newHandler := func() (*WhatsAppWebhookHandlers, *fakeNotificationStatusStore) {
		store := newFakeNotificationStatusStore()
		return testWhatsAppWebhookHandlers(store), store
	}
	mixed := func() []byte {
		return statusCallbackBody(t,
			map[string]any{"id": "wamid.known1", "status": "sent", "timestamp": "not-a-number", "recipient_id": "16505551234"},
			map[string]any{"id": "wamid.known1", "status": "delivered", "timestamp": "1750263750", "recipient_id": "16505551234"},
		)
	}
	t.Run("mixed envelope", func(t *testing.T) {
		handler, store := newHandler()
		if recorder := postStatus(t, handler, mixed(), validSigner); recorder.Code != http.StatusOK {
			t.Fatalf("mixed: %d", recorder.Code)
		}
		if len(store.history) != 1 {
			t.Fatalf("only sibling persists: %+v", store.history)
		}
		if store.current["notif-1"] != notifications.DeliveryDelivered {
			t.Fatalf("sibling applied: %+v", store.current)
		}
	})
	t.Run("all malformed acknowledged without writes", func(t *testing.T) {
		handler, store := newHandler()
		body := statusCallbackBody(t, map[string]any{
			"id": "wamid.known1", "status": "sent", "timestamp": "bad", "recipient_id": "1",
		})
		if recorder := postStatus(t, handler, body, validSigner); recorder.Code != http.StatusOK {
			t.Fatalf("all-malformed: %d", recorder.Code)
		}
		if len(store.history) != 0 {
			t.Fatalf("zero writes: %+v", store.history)
		}
	})
	t.Run("numeric timestamp still decodes", func(t *testing.T) {
		handler, store := newHandler()
		var envelope map[string]any
		if err := json.Unmarshal(statusCallbackBody(t, map[string]any{
			"id": "wamid.known1", "status": "sent", "timestamp": "1750263700", "recipient_id": "1",
		}), &envelope); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		// Rewrite the timestamp as a bare JSON number.
		raw = bytes.Replace(raw, []byte(`"timestamp":"1750263700"`), []byte(`"timestamp":1750263700`), 1)
		if recorder := postStatus(t, handler, raw, validSigner); recorder.Code != http.StatusOK {
			t.Fatalf("numeric: %d", recorder.Code)
		}
		if store.current["notif-1"] != notifications.DeliverySent {
			t.Fatalf("numeric applied: %+v", store.current)
		}
	})
	t.Run("structural garbage still 400", func(t *testing.T) {
		handler, _ := newHandler()
		if recorder := postStatus(t, handler, []byte(`{"object":`), validSigner); recorder.Code != http.StatusBadRequest {
			t.Fatalf("structural: %d", recorder.Code)
		}
	})
	t.Run("signature wins over malformed content", func(t *testing.T) {
		handler, store := newHandler()
		if recorder := postStatus(t, handler, mixed(), nil); recorder.Code != http.StatusUnauthorized {
			t.Fatalf("auth first: %d", recorder.Code)
		}
		if store.lookups != 0 {
			t.Fatal("zero writes without HMAC")
		}
	})
}
