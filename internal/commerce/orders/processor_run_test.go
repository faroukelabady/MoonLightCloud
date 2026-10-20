package orders

import (
	"context"
	"testing"
	"time"
)

// The real Run loop uses the existing no-I/O service/provider/store fakes.
// Each empty-claim barrier exposes a completed drain without polling or sleeps.
type runLoopWebhookStore struct {
	*stubWebhookStore
	empty chan chan struct{}
}

func (s *runLoopWebhookStore) ClaimOrderWebhookEvent(ctx context.Context, owner string, lease time.Duration, now time.Time) (ClaimedWebhookEvent, bool, error) {
	claim, ok, err := s.stubWebhookStore.ClaimOrderWebhookEvent(ctx, owner, lease, now)
	if err != nil || ok {
		return claim, ok, err
	}
	release := make(chan struct{})
	select {
	case s.empty <- release:
	case <-ctx.Done():
		return ClaimedWebhookEvent{}, false, ctx.Err()
	}
	select {
	case <-release:
		return claim, ok, nil
	case <-ctx.Done():
		return ClaimedWebhookEvent{}, false, ctx.Err()
	}
}

func awaitRunDrain(t *testing.T, ctx context.Context, empty <-chan chan struct{}) chan struct{} {
	t.Helper()
	select {
	case release := <-empty:
		return release
	case <-ctx.Done():
		t.Fatal("Run did not finish the expected drain before the deadline")
		return nil
	}
}

func TestProcessorRunStartupNotifyCoalescingAndCancellation(t *testing.T) {
	processor, webhooks, store, provider := processorTestSetup(baseSnapshot(), nil)
	gated := &runLoopWebhookStore{stubWebhookStore: webhooks, empty: make(chan chan struct{}, 1)}
	processor.webhooks = gated
	// Disable the periodic fallback so the second drain requires Notify.
	processor.interval = time.Hour
	webhooks.add("website", "startup", "order.created", "100")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		processor.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Run goroutine leaked after cancellation")
		}
	})

	firstRelease := awaitRunDrain(t, ctx, gated.empty)
	if status, attempts, code := webhooks.state("website", "startup"); status != WebhookProcessed || attempts != 1 || code != "" {
		t.Fatalf("startup drain must process without Notify: %s/%d/%s", status, attempts, code)
	}
	webhooks.add("website", "notified", "order.updated", "100")
	notified := make(chan struct{})
	go func() {
		defer close(notified)
		for i := 0; i < 10000; i++ {
			processor.Notify()
		}
	}()
	select {
	case <-notified:
	case <-ctx.Done():
		t.Fatal("Notify blocked while Run was inside a drain")
	}
	// Run cannot consume wakeups while held at the first empty-claim gate.
	if got := len(processor.wake); got != 1 {
		t.Fatalf("notification burst must coalesce to one queued wake, got %d", got)
	}
	close(firstRelease)
	secondRelease := awaitRunDrain(t, ctx, gated.empty)
	if status, attempts, code := webhooks.state("website", "notified"); status != WebhookProcessed || attempts != 1 || code != "" {
		t.Fatalf("Notify must drain newly queued work: %s/%d/%s", status, attempts, code)
	}
	if got := len(processor.wake); got != 0 {
		t.Fatalf("Run must consume the queued wake, got %d remaining", got)
	}
	close(secondRelease)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit on context cancellation")
	}
	// Run has stopped: reading the nonconcurrent service fakes is now safe.
	if provider.calls != 2 {
		t.Fatalf("want two provider reads, got %d", provider.calls)
	}
	snapshot, revision, found, err := store.LoadProjectedOrder(context.Background(), "website", "100")
	if err != nil || !found || revision != 1 || snapshot.TotalMinor != baseSnapshot().TotalMinor {
		t.Fatalf("both Run-triggered reconciliations must converge: %+v/%d/%v/%v", snapshot, revision, found, err)
	}
}

func TestProcessorRunAlreadyCancelledDoesNotClaim(t *testing.T) {
	processor, webhooks, _, provider := processorTestSetup(baseSnapshot(), nil)
	webhooks.add("website", "cancelled", "order.created", "100")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { defer close(done); processor.Run(ctx) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit for an already cancelled context")
	}
	if status, attempts, code := webhooks.state("website", "cancelled"); status != WebhookPending || attempts != 0 || code != "" {
		t.Fatalf("cancelled Run must leave queued work untouched: %s/%d/%s", status, attempts, code)
	}
	if provider.calls != 0 {
		t.Fatalf("cancelled Run reached provider fake %d times", provider.calls)
	}
}
