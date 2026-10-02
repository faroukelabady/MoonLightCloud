package telegram

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
)

// stubMappingStore is an in-memory MappingStore keyed by the full
// (provider, template, locale) identity: providers and locales never
// share rows.
type stubMappingStore struct {
	mu   sync.Mutex
	rows map[string]notifications.TemplateMapping
}

func mappingKey(provider, template, locale string) string {
	return provider + "\x00" + template + "\x00" + locale
}

func (s *stubMappingStore) UpsertTemplateMapping(_ context.Context, mapping notifications.TemplateMapping) error {
	if s.rows == nil {
		s.rows = map[string]notifications.TemplateMapping{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[mappingKey(mapping.ProviderKey, mapping.TemplateKey, mapping.Locale)] = mapping
	return nil
}

func (s *stubMappingStore) GetTemplateMapping(_ context.Context, providerKey, templateKey, locale string) (notifications.TemplateMapping, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mapping, ok := s.rows[mappingKey(providerKey, templateKey, locale)]
	return mapping, ok, nil
}

func (s *stubMappingStore) ListTemplateMappings(_ context.Context, providerKey string) ([]notifications.TemplateMapping, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []notifications.TemplateMapping
	for _, mapping := range s.rows {
		if mapping.ProviderKey == providerKey {
			out = append(out, mapping)
		}
	}
	return out, nil
}

// recordingOutbox is an in-memory EnqueueStore with the frozen
// idempotency rule: same identity plus same semantic fingerprint
// adopts the existing row; same identity plus different semantics
// conflicts without mutating the original.
type recordingOutbox struct {
	mu      sync.Mutex
	intents []notifications.EnqueueIntent
	byKey   map[string]int
}

func (s *recordingOutbox) EnqueueNotification(_ context.Context, intent notifications.EnqueueIntent) (string, notifications.EnqueueOutcome, error) {
	key := intent.ProviderKey + "\x00" + intent.IdempotencyKey
	fingerprint := notifications.FingerprintSemantic(intent.ProviderKey, intent.Recipient,
		intent.TemplateKey, intent.Locale, intent.Parameters,
		notifications.ResolvedTemplate{
			TemplateKey: intent.TemplateKey, Locale: intent.Locale,
			ExternalTemplateName: intent.ExtTemplateName, ExternalLanguageCode: intent.ExtLanguageCode,
			ParameterOrder: intent.ExtParameterOrder,
		})
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byKey == nil {
		s.byKey = map[string]int{}
	}
	if idx, ok := s.byKey[key]; ok {
		existing := s.intents[idx]
		existingFingerprint := notifications.FingerprintSemantic(existing.ProviderKey, existing.Recipient,
			existing.TemplateKey, existing.Locale, existing.Parameters,
			notifications.ResolvedTemplate{
				TemplateKey: existing.TemplateKey, Locale: existing.Locale,
				ExternalTemplateName: existing.ExtTemplateName, ExternalLanguageCode: existing.ExtLanguageCode,
				ParameterOrder: existing.ExtParameterOrder,
			})
		if existingFingerprint == fingerprint {
			return fmt.Sprintf("notif-%d", idx), notifications.EnqueueDuplicateIdentical, nil
		}
		return "", 0, apperr.New(apperr.Conflict, "conflicting notification payload")
	}
	s.intents = append(s.intents, intent)
	id := fmt.Sprintf("notif-%d", len(s.intents)-1)
	s.byKey[key] = len(s.intents) - 1
	return id, notifications.EnqueueInserted, nil
}

func (s *recordingOutbox) last() notifications.EnqueueIntent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.intents[len(s.intents)-1]
}

func telegramTestMapping() notifications.TemplateMapping {
	return notifications.TemplateMapping{
		ProviderKey: "telegram-main", TemplateKey: "daily_business_report_v1", Locale: "ar",
		ExternalTemplateName: RendererBodyV1, ExternalLanguageCode: "ar",
		ParameterNames: []string{"report_body"}, Enabled: true,
	}
}

func telegramTestEnqueue(key string) notifications.EnqueueTemplateRequest {
	return notifications.EnqueueTemplateRequest{
		ProviderKey: "telegram-main", IdempotencyKey: key,
		Recipient: "@operations", TemplateKey: "daily_business_report_v1", Locale: "ar",
		Parameters: map[string]string{"report_body": "totals 68000"},
	}
}

// TestTelegramMappingSnapshotIsolation pins the immutable-queue rule
// for Telegram: editing a mapping after enqueue cannot alter the
// already-queued intent; only new enqueues see the new mapping.
func TestTelegramMappingSnapshotIsolation(t *testing.T) {
	ctx := context.Background()
	mappings := &stubMappingStore{}
	outbox := &recordingOutbox{}
	service := notifications.NewService(mappings, outbox, nil)
	if err := mappings.UpsertTemplateMapping(ctx, telegramTestMapping()); err != nil {
		t.Fatal(err)
	}
	first, err := service.EnqueueTemplate(ctx, telegramTestEnqueue("report-001"))
	if err != nil || !first.Created {
		t.Fatalf("enqueue: %+v %v", first, err)
	}
	frozen := outbox.last()

	changed := telegramTestMapping()
	changed.ExternalTemplateName = "telegram_text_v1"
	changed.ParameterNames = []string{"report_body", "extra"}
	if err := mappings.UpsertTemplateMapping(ctx, changed); err != nil {
		t.Fatal(err)
	}
	second, err := service.EnqueueTemplate(ctx, telegramTestEnqueue("report-002"))
	if err == nil {
		t.Fatalf("new param set must reject old payload: %+v", second)
	}
	still := outbox.intents[0]
	if still.ExtTemplateName != frozen.ExtTemplateName || len(still.ExtParameterOrder) != 1 {
		t.Fatalf("queued intent mutated: %+v", still)
	}
}

// TestTelegramCrossProviderMappingIndependence pins provider
// isolation: same template_key and locale coexist per provider, and
// editing one provider's mapping leaves the other's intent byte
// identical.
func TestTelegramCrossProviderMappingIndependence(t *testing.T) {
	ctx := context.Background()
	mappings := &stubMappingStore{}
	outbox := &recordingOutbox{}
	service := notifications.NewService(mappings, outbox, nil)
	waMapping := telegramTestMapping()
	waMapping.ProviderKey = "whatsapp-main"
	waMapping.ExternalTemplateName = "moonlight_daily_ar"
	if err := mappings.UpsertTemplateMapping(ctx, waMapping); err != nil {
		t.Fatal(err)
	}
	if err := mappings.UpsertTemplateMapping(ctx, telegramTestMapping()); err != nil {
		t.Fatal(err)
	}
	waReq := telegramTestEnqueue("wa-001")
	waReq.ProviderKey = "whatsapp-main"
	waReq.Recipient = "201012345678"
	if _, err := service.EnqueueTemplate(ctx, waReq); err != nil {
		t.Fatalf("whatsapp enqueue: %v", err)
	}
	if _, err := service.EnqueueTemplate(ctx, telegramTestEnqueue("tg-001")); err != nil {
		t.Fatalf("telegram enqueue: %v", err)
	}
	waBefore := outbox.intents[0]

	waMapping.ExternalTemplateName = "moonlight_daily_ar_v2"
	if err := mappings.UpsertTemplateMapping(ctx, waMapping); err != nil {
		t.Fatal(err)
	}
	if outbox.intents[0].ExtTemplateName != waBefore.ExtTemplateName {
		t.Fatal("whatsapp mapping edit must not touch queued rows")
	}
	if outbox.intents[1].ExtTemplateName != RendererBodyV1 {
		t.Fatalf("telegram intent must keep its renderer: %+v", outbox.intents[1])
	}
}

// TestTelegramLocaleIsolation pins locale independence: ar and en
// mappings coexist, and a missing locale blocks without silent
// fallback to another locale.
func TestTelegramLocaleIsolation(t *testing.T) {
	ctx := context.Background()
	mappings := &stubMappingStore{}
	outbox := &recordingOutbox{}
	service := notifications.NewService(mappings, outbox, nil)
	if err := mappings.UpsertTemplateMapping(ctx, telegramTestMapping()); err != nil {
		t.Fatal(err)
	}
	en := telegramTestMapping()
	en.Locale = "en"
	en.ExternalLanguageCode = "en"
	if err := mappings.UpsertTemplateMapping(ctx, en); err != nil {
		t.Fatal(err)
	}
	if _, err := service.EnqueueTemplate(ctx, telegramTestEnqueue("tg-ar-001")); err != nil {
		t.Fatalf("ar enqueue: %v", err)
	}
	enReq := telegramTestEnqueue("tg-en-001")
	enReq.Locale = "en"
	enReq.Parameters = map[string]string{"report_body": "totals 68000"}
	if _, err := service.EnqueueTemplate(ctx, enReq); err != nil {
		t.Fatalf("en enqueue: %v", err)
	}
	frReq := telegramTestEnqueue("tg-fr-001")
	frReq.Locale = "fr"
	if _, err := service.EnqueueTemplate(ctx, frReq); err == nil {
		t.Fatal("missing locale must block without fallback")
	} else if !strings.Contains(err.Error(), notifications.CodeTemplateMappingMissing) {
		t.Fatalf("missing locale must report mapping-missing: %v", err)
	}
}

// TestTelegramEnqueueIdempotency pins the frozen identity rule for
// Telegram rows: same key plus same payload adopts; same key plus
// different payload conflicts.
func TestTelegramEnqueueIdempotency(t *testing.T) {
	ctx := context.Background()
	mappings := &stubMappingStore{}
	outbox := &recordingOutbox{}
	service := notifications.NewService(mappings, outbox, nil)
	if err := mappings.UpsertTemplateMapping(ctx, telegramTestMapping()); err != nil {
		t.Fatal(err)
	}
	first, err := service.EnqueueTemplate(ctx, telegramTestEnqueue("tg-idem-001"))
	if err != nil || !first.Created {
		t.Fatalf("first: %+v %v", first, err)
	}
	replay, err := service.EnqueueTemplate(ctx, telegramTestEnqueue("tg-idem-001"))
	if err != nil || replay.Created || replay.ID != first.ID {
		t.Fatalf("identical replay must adopt: %+v %v", replay, err)
	}
	diverged := telegramTestEnqueue("tg-idem-001")
	diverged.Parameters = map[string]string{"report_body": "other totals"}
	if _, err := service.EnqueueTemplate(ctx, diverged); err == nil {
		t.Fatal("diverged payload must conflict")
	}
}
