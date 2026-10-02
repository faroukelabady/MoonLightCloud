package telegram

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications/whatsapp"
)

// graphStub is a minimal Meta Graph API stand-in: exact success shape
// only. It is not Meta.
type graphStub struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	requests int
}

func newGraphStub(t *testing.T) *graphStub {
	t.Helper()
	stub := &graphStub{t: t}
	stub.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(io.LimitReader(r.Body, 1<<20))
		stub.mu.Lock()
		stub.requests++
		stub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"messaging_product": "whatsapp",
			"messages":          []any{map[string]any{"id": "wamid.coexist0001"}},
		})
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *graphStub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// TestMultiProviderCoexistence pins the target architecture:
// WhatsApp and Telegram register side by side on the generic
// registry (count 2, no key collision), and one dispatcher delivers
// both rows independently through provider-neutral queue logic.
// A Telegram failure cannot corrupt WhatsApp delivery and vice versa.
func TestMultiProviderCoexistence(t *testing.T) {
	waStub := newGraphStub(t)
	tgHarness := newBotHarness(t)

	waProvider, err := whatsapp.NewProvider(config.WhatsAppNotificationConfig{
		Enabled: true, ProviderKey: "whatsapp-main", GraphVersion: "v25.0",
		BaseURL: waStub.server.URL, PhoneNumberID: "106540352242922",
		AccessToken: "EAATestAccessToken", AppSecret: "s", WebhookVerifyToken: "v",
		HTTPTimeout: 10 * time.Second,
	}, waStub.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	tgProvider, err := NewProvider(testBotConfig(tgHarness), tgHarness.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	registry := notifications.NewRegistry()
	if err := registry.Register(waProvider.Key(), waProvider); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(tgProvider.Key(), tgProvider); err != nil {
		t.Fatal(err)
	}
	if registry.Count() != 2 {
		t.Fatalf("registry count: %d", registry.Count())
	}
	keys := registry.List()
	if len(keys) != 2 || keys[0] == keys[1] {
		t.Fatalf("distinct keys: %v", keys)
	}

	store := newFakeTelegramStore()
	store.addRow("wa-1", false, "whatsapp-main", "201012345678",
		"daily_business_report_v1", map[string]string{"report_body": "wa body"},
		"moonlight_daily_ar", []string{"report_body"})
	store.addRow("tg-1", false, "telegram-main", "@operations",
		"daily_business_report_v1", map[string]string{"report_body": "tg body"},
		RendererBodyV1, []string{"report_body"})

	dispatcher := notifications.NewDispatcher(store, registry,
		notifications.SystemClock{}, "worker-A", slog.Default())
	dispatcher.DrainForTest(context.Background())

	if dispatch, _, wamid := store.state("wa-1"); dispatch != notifications.DispatchAccepted || wamid != "wamid.coexist0001" {
		t.Fatalf("whatsapp: %q %q", dispatch, wamid)
	}
	if dispatch, _, wamid := store.state("tg-1"); dispatch != notifications.DispatchAccepted || wamid == "" {
		t.Fatalf("telegram: %q %q", dispatch, wamid)
	}
	if waStub.count() != 1 || tgHarness.count() != 1 {
		t.Fatalf("one send per provider: whatsapp=%d telegram=%d", waStub.count(), tgHarness.count())
	}
	if got := tgHarness.recorded()[0].Body["text"]; got != "tg body" {
		t.Fatalf("telegram body: %v", got)
	}
}

// TestMultiProviderFailureIsolation pins independent dispatch
// outcomes: a Telegram terminal failure blocks only its own row;
// the WhatsApp row still accepts in the same drain.
func TestMultiProviderFailureIsolation(t *testing.T) {
	waStub := newGraphStub(t)
	tgHarness := newBotHarness(t)
	tgHarness.script = func(_ botRecordedRequest) (int, any, map[string]string) {
		return http.StatusForbidden, botFailure(403, "Forbidden: bot was blocked by the user", nil), nil
	}
	waProvider, err := whatsapp.NewProvider(config.WhatsAppNotificationConfig{
		Enabled: true, ProviderKey: "whatsapp-main", GraphVersion: "v25.0",
		BaseURL: waStub.server.URL, PhoneNumberID: "106540352242922",
		AccessToken: "EAATestAccessToken", AppSecret: "s", WebhookVerifyToken: "v",
		HTTPTimeout: 10 * time.Second,
	}, waStub.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	tgProvider, err := NewProvider(testBotConfig(tgHarness), tgHarness.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	registry := notifications.NewRegistry()
	if err := registry.Register(waProvider.Key(), waProvider); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(tgProvider.Key(), tgProvider); err != nil {
		t.Fatal(err)
	}
	store := newFakeTelegramStore()
	store.addRow("wa-1", false, "whatsapp-main", "201012345678",
		"daily_business_report_v1", map[string]string{"report_body": "wa body"},
		"moonlight_daily_ar", []string{"report_body"})
	store.addRow("tg-1", false, "telegram-main", "@operations",
		"daily_business_report_v1", map[string]string{"report_body": "tg body"},
		RendererBodyV1, []string{"report_body"})
	notifications.NewDispatcher(store, registry,
		notifications.SystemClock{}, "worker-A", slog.Default()).DrainForTest(context.Background())

	if dispatch, _, _ := store.state("wa-1"); dispatch != notifications.DispatchAccepted {
		t.Fatalf("whatsapp must accept independently: %q", dispatch)
	}
	if dispatch, code, _ := store.state("tg-1"); dispatch != notifications.DispatchBlocked || code != notifications.CodeProviderValidation {
		t.Fatalf("telegram must block independently: %q %q", dispatch, code)
	}
}
