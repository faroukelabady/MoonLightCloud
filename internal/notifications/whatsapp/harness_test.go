package whatsapp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
)

// graphHarness is a deterministic TLS Graph stub: it records outbound
// requests and serves scripted responses. It is not Meta.
type graphHarness struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	requests []graphRecordedRequest
	// script, when set, overrides the default accepted response.
	script func(graphRecordedRequest) (status int, body any, headers map[string]string)
	// hangup, when set, reads the request then closes without responding
	// (after-write disconnect).
	hangup bool
}

type graphRecordedRequest struct {
	Method   string
	Path     string
	RawQuery string
	Auth     string
	Body     map[string]any
	Raw      []byte
}

func newGraphHarness(t *testing.T) *graphHarness {
	t.Helper()
	harness := &graphHarness{}
	harness.server = httptest.NewTLSServer(http.HandlerFunc(harness.serve))
	t.Cleanup(harness.server.Close)
	return harness
}

func (h *graphHarness) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var parsed map[string]any
	_ = json.Unmarshal(raw, &parsed)
	record := graphRecordedRequest{
		Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery,
		Auth: r.Header.Get("Authorization"), Body: parsed, Raw: raw,
	}
	h.mu.Lock()
	h.requests = append(h.requests, record)
	hangup := h.hangup
	script := h.script
	h.mu.Unlock()
	if hangup {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t := h.t
			t.Fatal("no hijack support")
			return
		}
		conn, _, _ := hijacker.Hijack()
		_ = conn.Close()
		return
	}
	var status int
	var body any
	var headers map[string]string
	status, body, headers = http.StatusOK, map[string]any{
		"messaging_product": "whatsapp",
		"messages":          []any{map[string]any{"id": "wamid.accepted0001"}},
	}, nil
	if script != nil {
		status, body, headers = script(record)
	}
	for key, value := range headers {
		w.Header().Set(key, value)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

func (h *graphHarness) recorded() []graphRecordedRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]graphRecordedRequest(nil), h.requests...)
}

func testGraphConfig(harness *graphHarness) config.WhatsAppNotificationConfig {
	return config.WhatsAppNotificationConfig{
		Enabled: true, ProviderKey: "whatsapp-main", GraphVersion: "v25.0",
		BaseURL: harness.server.URL, PhoneNumberID: "106540352242922",
		AccessToken: "EAATestAccessToken", AppSecret: "test-app-secret",
		WebhookVerifyToken: "test-verify-token", HTTPTimeout: 10 * time.Second,
	}
}

func testGraphProvider(t *testing.T, harness *graphHarness) *Provider {
	t.Helper()
	provider, err := NewProvider(testGraphConfig(harness), harness.server.Client())
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if provider.Key() != notifications.ProviderKey("whatsapp-main") {
		t.Fatalf("key: %q", provider.Key())
	}
	return provider
}

func testSendRequest() notifications.TemplateSendRequest {
	return notifications.TemplateSendRequest{
		ProviderKey: "whatsapp-main", Recipient: "201012345678",
		Resolved: notifications.ResolvedTemplate{
			TemplateKey: "operator_test_v1", Locale: "ar",
			ExternalTemplateName: "moonlight_operator_test_ar", ExternalLanguageCode: "ar",
			ParameterOrder: []string{"name", "message"},
			Parameters:     map[string]string{"name": "Moon Light", "message": "test body"},
		},
	}
}
