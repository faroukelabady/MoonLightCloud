package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
)

// memNotificationStore is an in-memory template/status store for CLI
// tests with real mapping/enqueue semantics behind the runners.
type memNotificationStore struct {
	mappings  map[string]notifications.TemplateMapping
	status    postgres.NotificationStatus
	statusErr error
}

func (s *memNotificationStore) UpsertTemplateMapping(_ context.Context, mapping notifications.TemplateMapping) error {
	if s.mappings == nil {
		s.mappings = map[string]notifications.TemplateMapping{}
	}
	s.mappings[mapping.ProviderKey+"\x00"+mapping.TemplateKey+"\x00"+mapping.Locale] = mapping
	return nil
}

func (s *memNotificationStore) ListTemplateMappings(_ context.Context, provider string) ([]notifications.TemplateMapping, error) {
	var out []notifications.TemplateMapping
	for _, mapping := range s.mappings {
		if provider == "" || mapping.ProviderKey == provider {
			out = append(out, mapping)
		}
	}
	return out, nil
}

func (s *memNotificationStore) GetNotificationStatus(_ context.Context, _ string) (postgres.NotificationStatus, error) {
	if s.statusErr != nil {
		return postgres.NotificationStatus{}, s.statusErr
	}
	return s.status, nil
}

func TestNotificationTemplateMapCLI(t *testing.T) {
	ctx := context.Background()
	store := &memNotificationStore{}
	var out bytes.Buffer
	if err := runTemplateMapSet(ctx, store, &out, "whatsapp-main", "operator_test_v1", "ar",
		"moonlight_operator_test_ar", "ar", "name,message", true); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "template=operator_test_v1") {
		t.Fatalf("set output: %q", got)
	}
	out.Reset()
	if err := runTemplateMapList(ctx, store, &out, "whatsapp-main"); err != nil {
		t.Fatalf("list: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "count=1") || !strings.Contains(got, "params=name,message") {
		t.Fatalf("list output: %q", got)
	}
}

func TestNotificationEnqueueCLI(t *testing.T) {
	ctx := context.Background()
	mappings := &memMappingStoreAdapter{m: map[string]notifications.TemplateMapping{
		"whatsapp-main\x00operator_test_v1\x00ar": {
			ProviderKey: "whatsapp-main", TemplateKey: "operator_test_v1", Locale: "ar",
			ExternalTemplateName: "moonlight_operator_test_ar", ExternalLanguageCode: "ar",
			ParameterNames: []string{"name", "message"}, Enabled: true,
		},
	}}
	outbox := &memOutboxAdapter{rows: map[string]memOutboxRow{}}
	service := notifications.NewService(mappings, outbox, nil)
	var out bytes.Buffer
	params := map[string]string{"name": "Moon Light", "message": "hello"}
	if err := runNotificationEnqueue(ctx, service, &out, "whatsapp-main", "201012345678",
		"operator_test_v1", "ar", "manual-test-001", params); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	first := out.String()
	if !strings.Contains(first, "created=true") || !strings.Contains(first, "id=") {
		t.Fatalf("enqueue output: %q", first)
	}
	for _, forbidden := range []string{"201012345678", "hello", "Moon Light"} {
		if strings.Contains(first, forbidden) {
			t.Fatalf("PII in CLI output: %q", forbidden)
		}
	}
	// Duplicate enqueue returns the same ID without a second row.
	out.Reset()
	if err := runNotificationEnqueue(ctx, service, &out, "whatsapp-main", "201012345678",
		"operator_test_v1", "ar", "manual-test-001", params); err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	if !strings.Contains(out.String(), "created=false") {
		t.Fatalf("duplicate output: %q", out.String())
	}
	if len(outbox.rows) != 1 {
		t.Fatalf("one row: %d", len(outbox.rows))
	}
	// Conflicting payload errors without mutating.
	out.Reset()
	changed := map[string]string{"name": "Moon Light", "message": "other"}
	if err := runNotificationEnqueue(ctx, service, &out, "whatsapp-main", "201012345678",
		"operator_test_v1", "ar", "manual-test-001", changed); err == nil {
		t.Fatal("conflict must fail")
	}
}

func TestNotificationStatusCLI(t *testing.T) {
	ctx := context.Background()
	store := &memNotificationStore{
		status: postgres.NotificationStatus{
			ID: "notif-1", ProviderKey: "whatsapp-main", TemplateKey: "operator_test_v1",
			Locale: "ar", Dispatch: notifications.DispatchAccepted, Delivery: notifications.DeliveryRead,
			AttemptCount: 1, ProviderMessageID: "wamid.1",
		},
	}
	var out bytes.Buffer
	if err := runNotificationStatus(ctx, store, &out, "notif-1"); err != nil {
		t.Fatalf("status: %v", err)
	}
	got := out.String()
	for _, want := range []string{"dispatch=accepted", "delivery=READ", "message_id=wamid.1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status output missing %q: %q", want, got)
		}
	}
}

// memMappingStoreAdapter adapts the in-memory mapping table to the
// service boundary.
type memMappingStoreAdapter struct {
	m map[string]notifications.TemplateMapping
}

func (s *memMappingStoreAdapter) UpsertTemplateMapping(_ context.Context, mapping notifications.TemplateMapping) error {
	s.m[mapping.ProviderKey+"\x00"+mapping.TemplateKey+"\x00"+mapping.Locale] = mapping
	return nil
}

func (s *memMappingStoreAdapter) GetTemplateMapping(_ context.Context, provider, template, locale string) (notifications.TemplateMapping, bool, error) {
	mapping, ok := s.m[provider+"\x00"+template+"\x00"+locale]
	return mapping, ok, nil
}

func (s *memMappingStoreAdapter) ListTemplateMappings(_ context.Context, provider string) ([]notifications.TemplateMapping, error) {
	var out []notifications.TemplateMapping
	for _, mapping := range s.m {
		if provider == "" || mapping.ProviderKey == provider {
			out = append(out, mapping)
		}
	}
	return out, nil
}

// memOutboxAdapter adapts an in-memory row map to the enqueue boundary
// with real idempotency semantics.
type memOutboxAdapter struct {
	rows map[string]memOutboxRow
	seq  int
}

type memOutboxRow struct {
	id          string
	fingerprint [32]byte
}

func (s *memOutboxAdapter) EnqueueNotification(_ context.Context, intent notifications.EnqueueIntent) (string, notifications.EnqueueOutcome, error) {
	key := intent.ProviderKey + "\x00" + intent.IdempotencyKey
	fingerprint := notifications.FingerprintSemantic(intent.ProviderKey, intent.Recipient,
		intent.TemplateKey, intent.Locale, intent.Parameters, notifications.ResolvedTemplate{
			ExternalTemplateName: intent.ExtTemplateName,
			ExternalLanguageCode: intent.ExtLanguageCode,
			ParameterOrder:       intent.ExtParameterOrder,
		})
	if existing, ok := s.rows[key]; ok {
		if existing.fingerprint != fingerprint {
			return "", notifications.EnqueueInserted, apperr.New(apperr.Conflict, "idempotency key already used with different payload")
		}
		return existing.id, notifications.EnqueueDuplicateIdentical, nil
	}
	s.seq++
	id := fmt.Sprintf("cli-test-%d", s.seq)
	s.rows[key] = memOutboxRow{id: id, fingerprint: fingerprint}
	return id, notifications.EnqueueInserted, nil
}
