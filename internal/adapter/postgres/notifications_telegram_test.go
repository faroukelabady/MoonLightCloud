package postgres

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications/telegram"
)

// botStub is a deterministic TLS Bot API stand-in for postgres-level
// proofs. It is not Telegram.
type botStub struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests int
	bodies   []string
	chatID   int64
	message  int64
}

func newBotStub(t *testing.T) *botStub {
	t.Helper()
	stub := &botStub{chatID: 555666777, message: 31337}
	stub.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(raw)
		stub.mu.Lock()
		stub.requests++
		stub.bodies = append(stub.bodies, string(raw))
		stub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":31337,"chat":{"id":555666777,"type":"private"},"date":1790000000}}`))
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *botStub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func telegramTestProvider(t *testing.T, stub *botStub) *telegram.Provider {
	t.Helper()
	provider, err := telegram.NewProvider(config.TelegramNotificationConfig{
		Enabled: true, ProviderKey: "telegram-main",
		BotToken: "TESTTOKEN000:AAA-fake-test-only", BaseURL: stub.server.URL,
		HTTPTimeout: 10 * time.Second,
	}, stub.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func telegramTestRegistry(t *testing.T, provider *telegram.Provider) *notifications.Registry {
	t.Helper()
	registry := notifications.NewRegistry()
	if err := registry.Register(provider.Key(), provider); err != nil {
		t.Fatal(err)
	}
	return registry
}

func seedTelegramTemplateMapping(t *testing.T, env *saleEnv) {
	t.Helper()
	ctx := context.Background()
	store := catalogStore(env)
	for _, template := range []string{"daily_business_report_v1", "ten_day_business_report_v1",
		"operational_alert_open_v1", "operational_alert_resolved_v1", "operator_test_v1"} {
		for _, locale := range []string{"ar", "en"} {
			param := "report_body"
			if strings.HasPrefix(template, "operational_alert_") {
				param = "alert_body"
			}
			if template == "operator_test_v1" {
				param = "body"
			}
			if err := store.UpsertTemplateMapping(ctx, notifications.TemplateMapping{
				ProviderKey: "telegram-main", TemplateKey: template, Locale: locale,
				ExternalTemplateName: telegram.RendererBodyV1, ExternalLanguageCode: locale,
				ParameterNames: []string{param}, Enabled: true,
			}); err != nil {
				t.Fatalf("mapping: %v", err)
			}
		}
	}
}

// TestTelegramDurableDispatchProvesAcceptance runs the real durable
// chain on PostgreSQL: mapping, idempotent enqueue, lease-fenced
// dispatch, Telegram send, accepted completion with a chat-qualified
// identity. Skips without TEST_DATABASE_URL.
func TestTelegramDurableDispatchProvesAcceptance(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	seedTelegramTemplateMapping(t, env)
	service := notifications.NewService(store, store, slog.Default())

	result, err := service.EnqueueTemplate(ctx, notifications.EnqueueTemplateRequest{
		ProviderKey: "telegram-main", IdempotencyKey: "tg-proof-001",
		Recipient: "@operations", TemplateKey: "operator_test_v1", Locale: "ar",
		Parameters: map[string]string{"body": "manual proof"},
	})
	if err != nil || !result.Created {
		t.Fatalf("enqueue: %+v %v", result, err)
	}
	// Identical replay adopts; diverged payload conflicts.
	replay, err := service.EnqueueTemplate(ctx, notifications.EnqueueTemplateRequest{
		ProviderKey: "telegram-main", IdempotencyKey: "tg-proof-001",
		Recipient: "@operations", TemplateKey: "operator_test_v1", Locale: "ar",
		Parameters: map[string]string{"body": "manual proof"},
	})
	if err != nil || replay.Created || replay.ID != result.ID {
		t.Fatalf("replay must adopt: %+v %v", replay, err)
	}

	stub := newBotStub(t)
	dispatcher := notifications.NewDispatcher(store,
		telegramTestRegistry(t, telegramTestProvider(t, stub)),
		notifications.SystemClock{}, "worker-T", slog.Default())
	dispatcher.DrainForTest(ctx)

	status, err := store.GetNotificationStatus(ctx, result.ID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Dispatch != notifications.DispatchAccepted {
		t.Fatalf("dispatch: %q code %q", status.Dispatch, status.LastErrorCode)
	}
	if !strings.Contains(status.ProviderMessageID, ":") {
		t.Fatalf("chat-qualified identity required: %q", status.ProviderMessageID)
	}
	if stub.count() != 1 {
		t.Fatalf("one remote send, got %d", stub.count())
	}
	// Accepted rows never resend.
	dispatcher.DrainForTest(ctx)
	if stub.count() != 1 {
		t.Fatalf("accepted must never resend: %d", stub.count())
	}
}
