package notifications

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// stubProvider is a scripted NotificationProvider for service tests.
type stubProvider struct {
	key  ProviderKey
	send func(ctx context.Context, req TemplateSendRequest) (SendResult, error)
}

func (s *stubProvider) Key() ProviderKey { return s.key }

func (s *stubProvider) SendTemplate(ctx context.Context, req TemplateSendRequest) (SendResult, error) {
	return s.send(ctx, req)
}

func TestRegistryBoundaries(t *testing.T) {
	registry := NewRegistry()
	if registry.Count() != 0 {
		t.Fatal("empty registry")
	}
	provider := &stubProvider{key: "whatsapp-main"}
	if err := registry.Register("whatsapp-main", provider); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := registry.Register("whatsapp-main", provider); err == nil {
		t.Fatal("duplicate key must fail")
	}
	var nilProvider *stubProvider
	if err := registry.Register("other", nilProvider); err == nil {
		t.Fatal("typed nil must fail")
	}
	if err := registry.Register("BAD KEY", provider); err == nil {
		t.Fatal("bad key must fail")
	}
	mismatched := &stubProvider{key: "different"}
	if err := registry.Register("whatsapp-main-2", mismatched); err == nil {
		t.Fatal("key mismatch must fail")
	}
	got, err := registry.Get("whatsapp-main")
	if err != nil || got.Key() != "whatsapp-main" {
		t.Fatalf("lookup: %v", got)
	}
	if _, err := registry.Get("missing"); err == nil {
		t.Fatal("unknown key must fail")
	}
	if keys := registry.List(); len(keys) != 1 || keys[0] != "whatsapp-main" {
		t.Fatalf("list: %v", keys)
	}
}

func TestFingerprintDeterministic(t *testing.T) {
	resolved := ResolvedTemplate{
		ExternalTemplateName: "moonlight_test_ar", ExternalLanguageCode: "ar",
		ParameterOrder: []string{"a", "b"},
	}
	params := map[string]string{"b": "2", "a": "1"}
	first := FingerprintSemantic("whatsapp-main", "201012345678", "operator_test_v1", "ar", params, resolved)
	// Differing Go map iteration order must not change the digest.
	for i := 0; i < 50; i++ {
		again := FingerprintSemantic("whatsapp-main", "201012345678", "operator_test_v1", "ar",
			map[string]string{"a": "1", "b": "2"}, resolved)
		if again != first {
			t.Fatal("nondeterministic fingerprint")
		}
	}
	changed := FingerprintSemantic("whatsapp-main", "201012345678", "operator_test_v1", "ar",
		map[string]string{"a": "1", "b": "3"}, resolved)
	if changed == first {
		t.Fatal("parameter change must change fingerprint")
	}
	remapped := resolved
	remapped.ExternalTemplateName = "other_name"
	if FingerprintSemantic("whatsapp-main", "201012345678", "operator_test_v1", "ar", params, remapped) == first {
		t.Fatal("mapping snapshot change must change fingerprint")
	}
}

func TestValidationBounds(t *testing.T) {
	// Shape validation cannot infer country code: a digits-only value
	// passes and is sent verbatim (Meta rejects the unknown). MoonLight
	// never rewrites national prefixes.
	if err := ValidateRecipient("01001234567"); err != nil {
		t.Fatalf("digits-only shape passes verbatim: %v", err)
	}
	for _, bad := range []string{"abc", "+", "20101234567890123", " 2010", ""} {
		if err := ValidateRecipient(bad); err == nil {
			t.Fatalf("recipient %q must fail", bad)
		}
	}
	// Phase 10 STOP expansion: the enqueue gate is the multi-provider
	// union, so short numerics are Telegram-shaped and now enqueue.
	// They can never be misdelivered: the WhatsApp adapter keeps
	// strict E.164 at send time (terminal block), and Telegram
	// rejects unknown chats the same way. 17-digit values stay
	// rejected at enqueue (beyond any Bot API chat ID).
	for _, telegramShaped := range []string{"12", "123456", "-1001", "@operations"} {
		if err := ValidateRecipient(telegramShaped); err != nil {
			t.Fatalf("telegram-shaped recipient %q: %v", telegramShaped, err)
		}
	}
	for _, recipient := range []string{"201012345678", "+201012345678", "16505551234"} {
		if err := ValidateRecipient(recipient); err != nil {
			t.Fatalf("recipient %q: %v", recipient, err)
		}
	}
	if err := ValidateIdempotencyKey("to=201012345678"); err != nil {
		t.Fatalf("idempotency key: %v", err)
	}
	if err := ValidateIdempotencyKey("201012345678"); err != nil {
		t.Fatalf("plain key valid shape but policy: %v", err)
	}
	big := map[string]string{}
	for i := 0; i < 33; i++ {
		big[string(rune('a'+i%26))+string(rune('0'+i%10))] = "x"
	}
	if err := ValidateParameters(big); err == nil {
		t.Fatal("33 parameters must fail")
	}
}

func TestDeliveryPrecedence(t *testing.T) {
	if !(DeliveryPrecedence(DeliveryAccepted) < DeliveryPrecedence(DeliverySent) &&
		DeliveryPrecedence(DeliverySent) < DeliveryPrecedence(DeliveryDelivered) &&
		DeliveryPrecedence(DeliveryDelivered) < DeliveryPrecedence(DeliveryRead)) {
		t.Fatal("canonical progression must order")
	}
	now := time.Now().UTC()
	later := now.Add(time.Second)
	if !AdvanceDelivery(DeliverySent, &now, DeliveryDelivered, &later) {
		t.Fatal("newer timestamp must advance")
	}
	if AdvanceDelivery(DeliveryRead, &later, DeliverySent, &now) {
		t.Fatal("older timestamp must not regress")
	}
	if !AdvanceDelivery(DeliveryDelivered, &now, DeliveryFailed, &now) {
		t.Fatal("same-timestamp FAILED must win")
	}
	if AdvanceDelivery(DeliveryDelivered, &now, DeliverySent, &now) {
		t.Fatal("same-timestamp lesser state must not win")
	}
	if AdvanceDelivery(DeliveryAccepted, &now, DeliveryUnknown, &later) {
		t.Fatal("UNKNOWN must never advance")
	}
	if AdvanceDelivery(DeliveryAccepted, &now, DeliverySent, nil) {
		t.Fatal("missing timestamp must not advance")
	}
}

// memMappingStore is an in-memory MappingStore.
type memMappingStore struct {
	mu       sync.Mutex
	mappings map[string]TemplateMapping
}

func (s *memMappingStore) key(provider, template, locale string) string {
	return provider + "\x00" + template + "\x00" + locale
}

func (s *memMappingStore) UpsertTemplateMapping(_ context.Context, mapping TemplateMapping) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mappings == nil {
		s.mappings = map[string]TemplateMapping{}
	}
	s.mappings[s.key(mapping.ProviderKey, mapping.TemplateKey, mapping.Locale)] = mapping
	return nil
}

func (s *memMappingStore) GetTemplateMapping(_ context.Context, provider, template, locale string) (TemplateMapping, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mapping, ok := s.mappings[s.key(provider, template, locale)]
	return mapping, ok, nil
}

func (s *memMappingStore) ListTemplateMappings(_ context.Context, provider string) ([]TemplateMapping, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []TemplateMapping
	for _, mapping := range s.mappings {
		if provider == "" || mapping.ProviderKey == provider {
			out = append(out, mapping)
		}
	}
	return out, nil
}

// memOutbox is an in-memory EnqueueStore with real idempotency rules.
type memOutbox struct {
	mu   sync.Mutex
	rows map[string]memNotification
	seq  int
}

type memNotification struct {
	id          string
	fingerprint [32]byte
}

func (s *memOutbox) EnqueueNotification(_ context.Context, intent EnqueueIntent) (string, EnqueueOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = map[string]memNotification{}
	}
	key := intent.ProviderKey + "\x00" + intent.IdempotencyKey
	fingerprint := FingerprintSemantic(intent.ProviderKey, intent.Recipient, intent.TemplateKey,
		intent.Locale, intent.Parameters, ResolvedTemplate{
			ExternalTemplateName: intent.ExtTemplateName,
			ExternalLanguageCode: intent.ExtLanguageCode,
			ParameterOrder:       intent.ExtParameterOrder,
		})
	if existing, ok := s.rows[key]; ok {
		if existing.fingerprint != fingerprint {
			return "", EnqueueInserted, &conflictError{}
		}
		return existing.id, EnqueueDuplicateIdentical, nil
	}
	s.seq++
	id := fmt.Sprintf("test-notification-%d", s.seq)
	s.rows[key] = memNotification{id: id, fingerprint: fingerprint}
	return id, EnqueueInserted, nil
}

type conflictError struct{}

func (e *conflictError) Error() string { return "conflict" }

func testMapping() TemplateMapping {
	return TemplateMapping{
		ProviderKey: "whatsapp-main", TemplateKey: "operator_test_v1", Locale: "ar",
		ExternalTemplateName: "moonlight_operator_test_ar", ExternalLanguageCode: "ar",
		ParameterNames: []string{"name", "message"}, Enabled: true,
	}
}

func testEnqueueRequest() EnqueueTemplateRequest {
	return EnqueueTemplateRequest{
		ProviderKey: "whatsapp-main", IdempotencyKey: "manual-test-001",
		Recipient: "201012345678", TemplateKey: "operator_test_v1", Locale: "ar",
		Parameters: map[string]string{"name": "Moon Light", "message": "hello"},
	}
}

func TestServiceEnqueue(t *testing.T) {
	ctx := context.Background()
	mappings := &memMappingStore{}
	outbox := &memOutbox{}
	service := NewService(mappings, outbox, nil)
	if _, err := service.EnqueueTemplate(ctx, testEnqueueRequest()); err == nil {
		t.Fatal("missing mapping must fail")
	}
	if err := mappings.UpsertTemplateMapping(ctx, testMapping()); err != nil {
		t.Fatal(err)
	}
	disabled := testMapping()
	disabled.Locale = "en"
	disabled.Enabled = false
	if err := mappings.UpsertTemplateMapping(ctx, disabled); err != nil {
		t.Fatal(err)
	}
	first, err := service.EnqueueTemplate(ctx, testEnqueueRequest())
	if err != nil || !first.Created {
		t.Fatalf("first enqueue: %+v %v", first, err)
	}
	second, err := service.EnqueueTemplate(ctx, testEnqueueRequest())
	if err != nil || second.Created || second.ID != first.ID {
		t.Fatalf("duplicate must return same ID: %+v %v", second, err)
	}
	changed := testEnqueueRequest()
	changed.Parameters = map[string]string{"name": "Moon Light", "message": "other"}
	if _, err := service.EnqueueTemplate(ctx, changed); err == nil {
		t.Fatal("conflicting payload must fail")
	}
	extra := testEnqueueRequest()
	extra.IdempotencyKey = "manual-test-002"
	extra.Parameters = map[string]string{"name": "x", "message": "y", "extra": "z"}
	if _, err := service.EnqueueTemplate(ctx, extra); err == nil {
		t.Fatal("unknown extra parameter must fail")
	}
	missing := testEnqueueRequest()
	missing.IdempotencyKey = "manual-test-003"
	missing.Parameters = map[string]string{"name": "x"}
	if _, err := service.EnqueueTemplate(ctx, missing); err == nil {
		t.Fatal("missing parameter must fail")
	}
	short := testEnqueueRequest()
	short.IdempotencyKey = "manual-test-004"
	short.Recipient = " 12"
	if _, err := service.EnqueueTemplate(ctx, short); err == nil {
		t.Fatal("whitespace-edged recipient must fail")
	}
	// Phase 10 STOP expansion: short numerics are Telegram-shaped and
	// enqueue under the union; WhatsApp send-time strictness (proven in
	// the WhatsApp adapter suite) terminally blocks them at dispatch.
	telegramMapping := testMapping()
	telegramMapping.ProviderKey = "telegram-main"
	telegramMapping.ExternalTemplateName = "telegram_text_v1"
	telegramMapping.ParameterNames = []string{"body"}
	if err := mappings.UpsertTemplateMapping(ctx, telegramMapping); err != nil {
		t.Fatal(err)
	}
	telegramShaped := testEnqueueRequest()
	telegramShaped.IdempotencyKey = "manual-test-005"
	telegramShaped.Recipient = "12"
	telegramShaped.ProviderKey = "telegram-main"
	telegramShaped.Parameters = map[string]string{"body": "hello"}
	if _, err := service.EnqueueTemplate(ctx, telegramShaped); err != nil {
		t.Fatalf("telegram-shaped recipient must enqueue: %v", err)
	}
}

// TestAdvanceDeliveryAcceptedBaseline proves local ACCEPTED yields to
// the first known provider callback regardless of clock skew, then
// anchors normal provider ordering afterwards.
func TestAdvanceDeliveryAcceptedBaseline(t *testing.T) {
	local := time.Date(2026, 9, 28, 12, 0, 41, 50130000, time.UTC)
	// Same-second provider callback advances despite trailing local microseconds.
	second := time.Date(2026, 9, 28, 12, 0, 40, 0, time.UTC)
	if !AdvanceDelivery(DeliveryAccepted, &local, DeliveryDelivered, &second) {
		t.Fatal("same-second DELIVERED must advance past local ACCEPTED")
	}
	// Provider callback predating local persistence advances.
	earlier := time.Date(2026, 9, 28, 12, 0, 40, 0, time.UTC)
	if !AdvanceDelivery(DeliveryAccepted, &local, DeliverySent, &earlier) {
		t.Fatal("earlier SENT must establish provider state")
	}
	// UNKNOWN against ACCEPTED still records history only.
	if AdvanceDelivery(DeliveryAccepted, &local, DeliveryUnknown, &second) {
		t.Fatal("UNKNOWN must not advance ACCEPTED")
	}
	// FAILED as first provider callback establishes FAILED.
	if !AdvanceDelivery(DeliveryAccepted, &local, DeliveryFailed, &earlier) {
		t.Fatal("first FAILED must advance")
	}
	// After the baseline yields, normal provider ordering resumes:
	// older events stay history-only, precedence still deterministic.
	providerBase := second
	if !AdvanceDelivery(DeliverySent, &providerBase, DeliveryDelivered, &second) {
		t.Fatal("ordering must resume after baseline")
	}
	older := time.Date(2026, 9, 28, 12, 0, 39, 0, time.UTC)
	if AdvanceDelivery(DeliveryDelivered, &second, DeliverySent, &older) {
		t.Fatal("older event must stay history-only")
	}
}
