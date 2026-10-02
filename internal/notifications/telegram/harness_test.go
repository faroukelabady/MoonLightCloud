package telegram

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
)

// testBotToken is an unmistakably fake token. Real tokens never appear
// in fixtures, assertions, or failure output.
const testBotToken = "TESTTOKEN000:AAA-fake-test-only"

// botHarness is a deterministic TLS Bot API stub: it records outbound
// requests and serves scripted responses. It is not Telegram. The
// recorded path necessarily contains the fake token (the official API
// carries it in the path); helpers redact it before any failure
// output so emitted diagnostics stay secret-free.
type botHarness struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	requests []botRecordedRequest
	// script, when set, overrides the default accepted response.
	script func(botRecordedRequest) (status int, body any, headers map[string]string)
	// hangup, when set, reads the request then closes without
	// responding (after-write disconnect).
	hangup bool
	// sleep, when set, delays the response (timeout tests).
	sleep time.Duration
}

type botRecordedRequest struct {
	Method   string
	Path     string
	RawQuery string
	Auth     string
	Body     map[string]any
	Raw      []byte
}

func newBotHarness(t *testing.T) *botHarness {
	t.Helper()
	harness := &botHarness{}
	harness.server = httptest.NewTLSServer(http.HandlerFunc(harness.serve))
	t.Cleanup(harness.server.Close)
	return harness
}

func (h *botHarness) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var parsed map[string]any
	_ = json.Unmarshal(raw, &parsed)
	record := botRecordedRequest{
		Method: r.Method, Path: r.URL.EscapedPath(), RawQuery: r.URL.RawQuery,
		Auth: r.Header.Get("Authorization"), Body: parsed, Raw: raw,
	}
	h.mu.Lock()
	h.requests = append(h.requests, record)
	hangup := h.hangup
	script := h.script
	sleep := h.sleep
	h.mu.Unlock()
	if hangup {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			h.t.Error("no hijack support")
			return
		}
		conn, _, _ := hijacker.Hijack()
		_ = conn.Close()
		return
	}
	if sleep > 0 {
		time.Sleep(sleep)
	}
	var status int
	var body any
	var headers map[string]string
	status, body, headers = http.StatusOK, map[string]any{
		"ok": true,
		"result": map[string]any{
			"message_id": 42,
			"chat":       map[string]any{"id": 987654321, "type": "private"},
			"date":       1790000000,
			"text":       "hello",
		},
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

func (h *botHarness) recorded() []botRecordedRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]botRecordedRequest(nil), h.requests...)
}

func (h *botHarness) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.requests)
}

// redactedPath replaces the fake token segment before any failure
// output: secret-bearing URLs never appear in diagnostics even when
// an assertion prints the request line.
func (h *botHarness) redactedPath(path string) string {
	return strings.ReplaceAll(path, testBotToken, "<redacted-token>")
}

func testBotConfig(harness *botHarness) config.TelegramNotificationConfig {
	return config.TelegramNotificationConfig{
		Enabled: true, ProviderKey: "telegram-main",
		BotToken: testBotToken, BaseURL: harness.server.URL,
		HTTPTimeout: 10 * time.Second,
	}
}

func testBotProvider(t *testing.T, harness *botHarness) *Provider {
	t.Helper()
	provider, err := NewProvider(testBotConfig(harness), harness.server.Client())
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	if provider.Key() != notifications.ProviderKey("telegram-main") {
		t.Fatalf("key: %q", provider.Key())
	}
	return provider
}

func testBotRequest(recipient string) notifications.TemplateSendRequest {
	return notifications.TemplateSendRequest{
		ProviderKey: "telegram-main", Recipient: recipient,
		Resolved: notifications.ResolvedTemplate{
			TemplateKey: "operator_test_v1", Locale: "ar",
			ExternalTemplateName: RendererBodyV1, ExternalLanguageCode: "ar",
			ParameterOrder: []string{"body"},
			Parameters:     map[string]string{"body": "test body"},
		},
	}
}

// botSuccess builds a valid sendMessage success envelope.
func botSuccess(chatID int64, messageID int64) map[string]any {
	return map[string]any{
		"ok": true,
		"result": map[string]any{
			"message_id": messageID,
			"chat":       map[string]any{"id": chatID, "type": "private"},
			"date":       1790000000,
		},
	}
}

// botFailure builds a Bot API error envelope.
func botFailure(code int, description string, params map[string]any) map[string]any {
	env := map[string]any{"ok": false, "error_code": code, "description": description}
	if params != nil {
		env["parameters"] = params
	}
	return env
}

// asNotificationError unwraps the taxonomy for assertions.
func asNotificationError(t *testing.T, err error) *notifications.NotificationError {
	t.Helper()
	var target *notifications.NotificationError
	if !notifications.AsNotificationError(err, &target) {
		t.Fatalf("want *NotificationError, got %T (%v)", err, err)
	}
	return target
}

// getEnvOptIn reads opt-in-only live-test variables.
func getEnvOptIn(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}
