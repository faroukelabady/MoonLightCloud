package orders

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// stubWebhookStore is an in-memory claim/finish ledger.
type stubWebhookStore struct {
	mu     sync.Mutex
	events map[string]*stubWebhookEvent
	order  []string
}

type stubWebhookEvent struct {
	provider, delivery, topic, orderID string
	status                             string
	attempts                           int
	next                               *time.Time
	code                               string
	processed                          bool
	owner                              string
	generation                         LeaseGeneration
}

func (s *stubWebhookStore) add(provider, delivery, topic, orderID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.events == nil {
		s.events = map[string]*stubWebhookEvent{}
	}
	s.events[provider+"/"+delivery] = &stubWebhookEvent{
		provider: provider, delivery: delivery, topic: topic, orderID: orderID, status: WebhookPending,
	}
	s.order = append(s.order, provider+"/"+delivery)
}

func (s *stubWebhookStore) ClaimOrderWebhookEvent(_ context.Context, owner string, _ time.Duration, now time.Time) (ClaimedWebhookEvent, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range s.order {
		event := s.events[key]
		if event.status != WebhookPending && event.status != WebhookRetry {
			continue
		}
		if event.next != nil && event.next.After(now) {
			continue
		}
		event.attempts++
		event.owner = owner
		event.generation++
		topic, err := ParseWebhookTopic(event.topic)
		if err != nil {
			return ClaimedWebhookEvent{}, false, err
		}
		return ClaimedWebhookEvent{
			ProviderKey: event.provider, DeliveryID: event.delivery,
			Topic: topic, ExternalOrderID: event.orderID,
			AttemptCount: int32(event.attempts),
			LeaseOwner:   owner, LeaseGeneration: event.generation,
		}, true, nil
	}
	return ClaimedWebhookEvent{}, false, nil
}

func (s *stubWebhookStore) FinishOrderWebhookEvent(_ context.Context, claim ClaimedWebhookEvent, status string, next *time.Time, processed bool, code string) (FinishResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	event := s.events[claim.ProviderKey+"/"+claim.DeliveryID]
	if event.owner != claim.LeaseOwner || event.generation != claim.LeaseGeneration {
		return FinishStale, nil
	}
	event.status = status
	event.next = next
	event.processed = processed
	event.code = code
	return FinishApplied, nil
}

func (s *stubWebhookStore) state(provider, delivery string) (string, int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	event := s.events[provider+"/"+delivery]
	return event.status, event.attempts, event.code
}

type stubClock struct{ now time.Time }

func (c *stubClock) Now() time.Time { return c.now }

func processorTestSetup(snapshot OrderSnapshot, err error) (*Processor, *stubWebhookStore, *stubOrderStore, *stubOrderProvider) {
	provider := &stubOrderProvider{key: "website", snapshot: snapshot, err: err}
	registry := commerce.NewRegistry()
	if rerr := registry.Register("website", provider); rerr != nil {
		panic(rerr)
	}
	webhooks := &stubWebhookStore{}
	store := &stubOrderStore{}
	service := NewOrderService(registry, store, nil)
	return NewProcessor(webhooks, service, &stubClock{now: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)}, "test-worker", nil), webhooks, store, provider
}

func TestProcessorCreatedEvent(t *testing.T) {
	ctx := context.Background()
	snapshot := baseSnapshot()
	snapshot.ProviderKey = "website"
	processor, webhooks, store, _ := processorTestSetup(snapshot, nil)
	webhooks.add("website", "delivery-1", "order.created", "100")
	processor.drain(ctx)
	status, attempts, code := webhooks.state("website", "delivery-1")
	if status != WebhookProcessed || attempts != 1 || code != "" {
		t.Fatalf("processed: %s %d %q", status, attempts, code)
	}
	stored, rev, found, err := store.LoadProjectedOrder(ctx, "website", "100")
	if err != nil || !found || rev != 1 || stored.TotalMinor != snapshot.TotalMinor {
		t.Fatalf("projected: %+v %d %v %v", stored, rev, found, err)
	}
}

func TestProcessorOutOfOrderWebhooks(t *testing.T) {
	ctx := context.Background()
	oldState := baseSnapshot()
	oldState.ProviderKey = "website"
	oldState.ProviderStatus = "pending"
	oldState.Canonical = StatusPending
	oldState.TotalMinor = 10000
	newState := baseSnapshot()
	newState.ProviderKey = "website"
	processor, webhooks, store, provider := processorTestSetup(oldState, nil)
	// B arrives first and projects; A arrives later but the provider now
	// reports B, so no regression is possible.
	webhooks.add("website", "delivery-B", "order.updated", "100")
	provider.snapshot = newState
	processor.drain(ctx)
	webhooks.add("website", "delivery-A", "order.updated", "100")
	processor.drain(ctx)
	stored, rev, _, err := store.LoadProjectedOrder(ctx, "website", "100")
	if err != nil {
		t.Fatal(err)
	}
	if stored.TotalMinor != 12000 || rev != 1 {
		t.Fatalf("newest wins without churn: %+v rev %d", stored, rev)
	}
	if status, _, _ := webhooks.state("website", "delivery-A"); status != WebhookProcessed {
		t.Fatalf("late event processed: %s", status)
	}
}

func TestProcessorTemporaryRetry(t *testing.T) {
	ctx := context.Background()
	processor, webhooks, _, _ := processorTestSetup(baseSnapshot(), commerce.TemporaryError("timeout"))
	webhooks.add("website", "delivery-1", "order.created", "100")
	processor.drain(ctx)
	status, attempts, code := webhooks.state("website", "delivery-1")
	if status != WebhookRetry || attempts != 1 || code != "ORDER_PROVIDER_RETRY" {
		t.Fatalf("retry: %s %d %q", status, attempts, code)
	}
}

func TestProcessorBlockedPermanent(t *testing.T) {
	ctx := context.Background()
	processor, webhooks, _, _ := processorTestSetup(baseSnapshot(), commerce.AuthenticationError("bad creds"))
	webhooks.add("website", "delivery-1", "order.created", "100")
	processor.drain(ctx)
	status, _, code := webhooks.state("website", "delivery-1")
	if status != WebhookBlocked {
		t.Fatalf("blocked: %s", status)
	}
	if code == "" {
		t.Fatal("machine-readable code persisted")
	}
}

func TestProcessorDeleteEvent(t *testing.T) {
	ctx := context.Background()
	snapshot := baseSnapshot()
	snapshot.ProviderKey = "website"
	processor, webhooks, store, provider := processorTestSetup(snapshot, nil)
	webhooks.add("website", "delivery-1", "order.created", "100")
	processor.drain(ctx)
	// Provider confirms absence for the delete attempt.
	provider.snapshot = OrderSnapshot{}
	provider.err = &BlockedError{Code: CodeOrderNotFound, Message: "gone"}
	webhooks.add("website", "delivery-2", "order.deleted", "100")
	processor.drain(ctx)
	stored, rev, _, err := store.LoadProjectedOrder(ctx, "website", "100")
	if err != nil {
		t.Fatal(err)
	}
	if !stored.ProviderDeleted || stored.Canonical != StatusDeleted || rev != 2 {
		t.Fatalf("deleted: %+v rev %d", stored, rev)
	}
	if status, _, _ := webhooks.state("website", "delivery-2"); status != WebhookProcessed {
		t.Fatalf("delete processed: %s", status)
	}
}

func TestProcessorCrashReplay(t *testing.T) {
	ctx := context.Background()
	snapshot := baseSnapshot()
	snapshot.ProviderKey = "website"
	processor, webhooks, store, _ := processorTestSetup(snapshot, nil)
	webhooks.add("website", "delivery-1", "order.created", "100")
	processor.drain(ctx)
	// Simulate crash-after-projection: reset the event to pending as if
	// the finish never committed, then replay.
	webhooks.mu.Lock()
	webhooks.events["website/delivery-1"].status = WebhookPending
	webhooks.mu.Unlock()
	processor.drain(ctx)
	stored, rev, _, err := store.LoadProjectedOrder(ctx, "website", "100")
	if err != nil {
		t.Fatal(err)
	}
	if rev != 1 || stored.TotalMinor != snapshot.TotalMinor {
		t.Fatalf("idempotent replay: %+v rev %d", stored, rev)
	}
	status, _, _ := webhooks.state("website", "delivery-1")
	if status != WebhookProcessed {
		t.Fatalf("eventually processed: %s", status)
	}
}
