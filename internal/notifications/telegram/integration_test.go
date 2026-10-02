package telegram

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
	"github.com/faroukelabady/MoonLightCloud/internal/operations"
)

// reportBodyAR is a representative DAILY body: Arabic locale, exact
// minor-unit money strings (never floats), including a >2^53 total.
// The Telegram adapter performs no financial formatting: it delivers
// this byte-identically.
const reportBodyAR = "تقرير الأعمال اليومي 2026-09-28\n" +
	"مبيعات الجنيه: 68000 (680.00 ج.م)\n" +
	"مبيعات الدولار: 9007199254740993 (‏$90,071,992,547,409.93)\n" +
	"المرتجعات: 1500\n" +
	"الصافي: 9007199254739493"

// reportBodyEN is the English canonical twin.
const reportBodyEN = "Daily business report 2026-09-28\n" +
	"EGP sales: 68000\n" +
	"USD sales: 9007199254740993\n" +
	"Refunds: 1500\n" +
	"Net: 9007199254739493"

// seedTelegramReportMappings installs telegram-main mappings for the
// frozen Phase 7B logical templates in both locales.
func seedTelegramReportMappings(t *testing.T, mappings *stubMappingStore, ctx context.Context) {
	t.Helper()
	for _, template := range []string{"daily_business_report_v1", "ten_day_business_report_v1"} {
		for _, locale := range []string{"ar", "en"} {
			if err := mappings.UpsertTemplateMapping(ctx, notifications.TemplateMapping{
				ProviderKey: "telegram-main", TemplateKey: template, Locale: locale,
				ExternalTemplateName: RendererBodyV1, ExternalLanguageCode: locale,
				ParameterNames: []string{"report_body"}, Enabled: true,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// dispatchIntent delivers one recorded enqueue intent through a real
// dispatcher and provider, returning the final state and identity.
func dispatchIntent(t *testing.T, harness *botHarness, intent notifications.EnqueueIntent) (notifications.DispatchStatus, string) {
	t.Helper()
	store := newFakeTelegramStore()
	store.addRow("int-1", false, intent.ProviderKey, intent.Recipient,
		intent.TemplateKey, intent.Parameters, intent.ExtTemplateName, intent.ExtParameterOrder)
	telegramDispatcher(store, harness, "worker-A").DrainForTest(context.Background())
	dispatch, _, wamid := store.state("int-1")
	return dispatch, wamid
}

// TestTelegramDailyReportProof runs the Phase 7B-shaped delivery for
// DAILY/ar through real enqueue, snapshot, dispatch, and provider:
// one row, one send, byte-identical body, canonical money untouched.
func TestTelegramDailyReportProof(t *testing.T) {
	ctx := context.Background()
	mappings := &stubMappingStore{}
	outbox := &recordingOutbox{}
	service := notifications.NewService(mappings, outbox, nil)
	seedTelegramReportMappings(t, mappings, ctx)

	result, err := service.EnqueueTemplate(ctx, notifications.EnqueueTemplateRequest{
		ProviderKey: "telegram-main", IdempotencyKey: "business-report:run-1:del-1",
		Recipient: "@operations", TemplateKey: "daily_business_report_v1", Locale: "ar",
		Parameters: map[string]string{"report_body": reportBodyAR},
	})
	if err != nil || !result.Created {
		t.Fatalf("enqueue: %+v %v", result, err)
	}
	if len(outbox.intents) != 1 {
		t.Fatalf("one notification row, got %d", len(outbox.intents))
	}

	harness := newBotHarness(t)
	dispatch, wamid := dispatchIntent(t, harness, outbox.intents[0])
	if dispatch != notifications.DispatchAccepted || wamid == "" {
		t.Fatalf("dispatch: %q %q", dispatch, wamid)
	}
	if harness.count() != 1 {
		t.Fatalf("one remote send, got %d", harness.count())
	}
	if text := harness.recorded()[0].Body["text"]; text != reportBodyAR {
		t.Fatalf("body must be byte-identical")
	}
	for _, money := range []string{"9007199254740993", "9007199254739493", "68000"} {
		if !strings.Contains(string(harness.recorded()[0].Raw), money) {
			t.Fatalf("exact money %q missing from wire body", money)
		}
	}
}

// TestTelegramReportLocalesAndKinds repeats the proof for TEN_DAY and
// English: same computation shape, independent rows, locale-exact
// bodies through the same generic interfaces.
func TestTelegramReportLocalesAndKinds(t *testing.T) {
	ctx := context.Background()
	mappings := &stubMappingStore{}
	outbox := &recordingOutbox{}
	service := notifications.NewService(mappings, outbox, nil)
	seedTelegramReportMappings(t, mappings, ctx)

	cases := []struct {
		template string
		locale   string
		body     string
	}{
		{"ten_day_business_report_v1", "ar", reportBodyAR},
		{"daily_business_report_v1", "en", reportBodyEN},
		{"ten_day_business_report_v1", "en", reportBodyEN},
	}
	for i, tc := range cases {
		result, err := service.EnqueueTemplate(ctx, notifications.EnqueueTemplateRequest{
			ProviderKey: "telegram-main", IdempotencyKey: "business-report:run-9:del-" + string(rune('a'+i)),
			Recipient: "987654321", TemplateKey: tc.template, Locale: tc.locale,
			Parameters: map[string]string{"report_body": tc.body},
		})
		if err != nil || !result.Created {
			t.Fatalf("%s/%s: %+v %v", tc.template, tc.locale, result, err)
		}
		harness := newBotHarness(t)
		dispatch, _ := dispatchIntent(t, harness, outbox.intents[len(outbox.intents)-1])
		if dispatch != notifications.DispatchAccepted {
			t.Fatalf("%s/%s dispatch: %q", tc.template, tc.locale, dispatch)
		}
		if text := harness.recorded()[0].Body["text"]; text != tc.body {
			t.Fatalf("%s/%s body mutated", tc.template, tc.locale)
		}
	}
}

// TestTelegramMultiProviderReportSlot pins same-slot independence:
// one WhatsApp and one Telegram recipient share the canonical report
// computation (identical bodies) yet yield two independent
// notification rows with provider-specific mappings.
func TestTelegramMultiProviderReportSlot(t *testing.T) {
	ctx := context.Background()
	mappings := &stubMappingStore{}
	outbox := &recordingOutbox{}
	service := notifications.NewService(mappings, outbox, nil)
	seedTelegramReportMappings(t, mappings, ctx)
	if err := mappings.UpsertTemplateMapping(ctx, notifications.TemplateMapping{
		ProviderKey: "whatsapp-main", TemplateKey: "daily_business_report_v1", Locale: "ar",
		ExternalTemplateName: "moonlight_daily_ar", ExternalLanguageCode: "ar",
		ParameterNames: []string{"report_body"}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	waReq := notifications.EnqueueTemplateRequest{
		ProviderKey: "whatsapp-main", IdempotencyKey: "business-report:run-2:wa",
		Recipient: "201012345678", TemplateKey: "daily_business_report_v1", Locale: "ar",
		Parameters: map[string]string{"report_body": reportBodyAR},
	}
	tgReq := notifications.EnqueueTemplateRequest{
		ProviderKey: "telegram-main", IdempotencyKey: "business-report:run-2:tg",
		Recipient: "@operations", TemplateKey: "daily_business_report_v1", Locale: "ar",
		Parameters: map[string]string{"report_body": reportBodyAR},
	}
	if _, err := service.EnqueueTemplate(ctx, waReq); err != nil {
		t.Fatalf("whatsapp slot: %v", err)
	}
	if _, err := service.EnqueueTemplate(ctx, tgReq); err != nil {
		t.Fatalf("telegram slot: %v", err)
	}
	if len(outbox.intents) != 2 {
		t.Fatalf("two independent rows, got %d", len(outbox.intents))
	}
	if outbox.intents[0].Parameters["report_body"] != outbox.intents[1].Parameters["report_body"] {
		t.Fatal("same slot must share the canonical computation")
	}
	if outbox.intents[0].ExtTemplateName == outbox.intents[1].ExtTemplateName {
		t.Fatal("provider mappings must stay distinct")
	}
	harness := newBotHarness(t)
	dispatch, _ := dispatchIntent(t, harness, outbox.intents[1])
	if dispatch != notifications.DispatchAccepted || harness.count() != 1 {
		t.Fatalf("telegram slot dispatch: %q (%d sends)", dispatch, harness.count())
	}
}

// stubOpsStore feeds the real 7D alert processor one Telegram
// recipient. The operations package itself never imports Telegram:
// this stub lives in the Telegram test package, not in operations.
type stubOpsStore struct {
	operations.Store
	mu         sync.Mutex
	recipient  operations.Recipient
	claimed    []operations.Delivery
	sent       map[string]string
	blocked    map[string]string
	claimIndex int
}

func (s *stubOpsStore) ListEnabledOpsRecipients(_ context.Context) ([]operations.Recipient, error) {
	return []operations.Recipient{s.recipient}, nil
}

func (s *stubOpsStore) ClaimDelivery(_ context.Context) (operations.Delivery, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimIndex >= len(s.claimed) {
		return operations.Delivery{}, false, nil
	}
	delivery := s.claimed[s.claimIndex]
	s.claimIndex++
	return delivery, true, nil
}

func (s *stubOpsStore) FinishOpsDeliverySent(_ context.Context, id, notificationID string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sent == nil {
		s.sent = map[string]string{}
	}
	s.sent[id] = notificationID
	return nil
}

func (s *stubOpsStore) FinishOpsDeliveryBlocked(_ context.Context, id, code string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blocked == nil {
		s.blocked = map[string]string{}
	}
	s.blocked[id] = code
	return nil
}

// TestTelegramOperationalAlertProof runs the real Phase 7D compose
// path (BuildDeliveries with a Telegram recipient) through real
// enqueue, dispatch, and provider: open alert, one send, frozen
// alert wording untouched by Telegram, recursion-safe key shape.
func TestTelegramOperationalAlertProof(t *testing.T) {
	ctx := context.Background()
	mappings := &stubMappingStore{}
	outbox := &recordingOutbox{}
	notify := notifications.NewService(mappings, outbox, nil)
	for _, template := range []string{operations.TemplateOpen, operations.TemplateResolved} {
		if err := mappings.UpsertTemplateMapping(ctx, notifications.TemplateMapping{
			ProviderKey: "telegram-main", TemplateKey: template, Locale: "ar",
			ExternalTemplateName: RendererBodyV1, ExternalLanguageCode: "ar",
			ParameterNames: []string{operations.ParamAlertBody}, Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	opsStore := &stubOpsStore{
		recipient: operations.Recipient{
			ID: "rec-tg-1", Label: "ops-tg", ProviderKey: "telegram-main",
			Recipient: "@operations", Locale: "ar", Enabled: true,
		},
	}
	opsService := operations.NewService(opsStore, func() string { return "id-1" }, time.Now)
	processor := operations.NewAlertProcessor(opsStore, opsService, notify,
		func() string { return "del-1" }, time.Now, operations.NewMetrics())

	incident := operations.Incident{
		ID: "inc-1", Rule: "DEVICE_OFFLINE", SubjectType: "device",
		SubjectID: "dev-1", Severity: "high", OpenedAt: time.Now().UTC(),
	}
	deliveries, err := processor.BuildDeliveries(ctx, incident, operations.EventOpened)
	if err != nil || len(deliveries) != 1 {
		t.Fatalf("compose: %v %+v", deliveries, err)
	}
	delivery := deliveries[0]
	if delivery.TemplateKey != operations.TemplateOpen || delivery.ProviderKey != "telegram-main" {
		t.Fatalf("delivery: %+v", delivery)
	}
	if !operations.OpsOwnedKey(delivery.NotificationKey, delivery.TemplateKey) {
		t.Fatalf("7D deliveries must stay recursion-excluded: %q", delivery.NotificationKey)
	}
	// Telegram composes nothing: the frozen alert body rides through.
	if !strings.Contains(delivery.Body, "dev-1") {
		t.Fatalf("alert body must carry the frozen incident wording: %q", delivery.Body)
	}

	opsStore.claimed = []operations.Delivery{delivery}
	worked, err := processor.ProcessOne(ctx)
	if err != nil || !worked {
		t.Fatalf("process: %v %v", worked, err)
	}
	if len(outbox.intents) != 1 {
		t.Fatalf("one notification intent, got %d", len(outbox.intents))
	}
	intent := outbox.intents[0]
	if intent.Parameters[operations.ParamAlertBody] != delivery.Body {
		t.Fatal("alert body must reach the queue byte-identical")
	}

	harness := newBotHarness(t)
	dispatch, wamid := dispatchIntent(t, harness, intent)
	if dispatch != notifications.DispatchAccepted || wamid == "" {
		t.Fatalf("dispatch: %q %q", dispatch, wamid)
	}
	if text := harness.recorded()[0].Body["text"]; text != delivery.Body {
		t.Fatalf("telegram must not recompose incident wording")
	}
	if opsStore.sent[delivery.ID] == "" {
		t.Fatal("delivery must link the durable notification")
	}
}
