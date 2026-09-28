package notifications

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeDispatchStore is an in-memory DispatchStore with real fencing
// rules: finishes require owner+generation on non-terminal rows.
type fakeDispatchStore struct {
	mu   sync.Mutex
	rows map[string]*fakeRow
}

type fakeRow struct {
	claimed       ClaimedNotification
	dispatch      DispatchStatus
	next          *time.Time
	code          string
	sendStarted   bool
	wamid         string
	finishCalls   int
	staleFinishes int
}

func newFakeDispatchStore() *fakeDispatchStore {
	return &fakeDispatchStore{rows: map[string]*fakeRow{}}
}

func (s *fakeDispatchStore) add(id string, sendStarted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[id] = &fakeRow{
		claimed: ClaimedNotification{
			ID: id, ProviderKey: "whatsapp-main", Recipient: "201012345678",
			TemplateKey: "operator_test_v1", Locale: "ar",
			Parameters:      map[string]string{"name": "n", "message": "m"},
			ExtTemplateName: "ext", ExtLanguageCode: "ar",
			ExtParameterOrder: []string{"name", "message"},
			Dispatch:          DispatchPending,
		},
		dispatch:    DispatchPending,
		sendStarted: sendStarted,
	}
}

func (s *fakeDispatchStore) ClaimNotification(_ context.Context, owner string, _ time.Duration, _ time.Time) (ClaimedNotification, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.rows {
		if row.dispatch != DispatchPending && row.dispatch != DispatchRetry {
			continue
		}
		if row.claimed.LeaseOwner != "" {
			continue
		}
		row.claimed.LeaseOwner = owner
		row.claimed.LeaseGeneration++
		row.claimed.AttemptCount++
		row.claimed.SendStarted = row.sendStarted
		return row.claimed, true, nil
	}
	return ClaimedNotification{}, false, nil
}

func (s *fakeDispatchStore) expire(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[id].claimed.LeaseOwner = ""
}

func (s *fakeDispatchStore) MarkNotificationSendStarted(_ context.Context, id, owner string, generation int64) (FinishResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.rows[id]
	if row.claimed.LeaseOwner != owner || row.claimed.LeaseGeneration != generation {
		return FinishStale, nil
	}
	row.sendStarted = true
	return FinishApplied, nil
}

func (s *fakeDispatchStore) finish(id, owner string, generation int64, dispatch DispatchStatus, code, wamid string, clearMarker bool) (FinishResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.rows[id]
	if row.claimed.LeaseOwner != owner || row.claimed.LeaseGeneration != generation {
		row.staleFinishes++
		return FinishStale, nil
	}
	if row.dispatch != DispatchPending && row.dispatch != DispatchRetry {
		return FinishStale, nil
	}
	row.dispatch = dispatch
	row.code = code
	if wamid != "" {
		row.wamid = wamid
	}
	if clearMarker {
		row.sendStarted = false
	}
	row.claimed.LeaseOwner = ""
	row.finishCalls++
	return FinishApplied, nil
}

func (s *fakeDispatchStore) FinishNotificationAccepted(_ context.Context, id, owner string, generation int64, wamid string) (FinishResult, error) {
	return s.finish(id, owner, generation, DispatchAccepted, "", wamid, false)
}

func (s *fakeDispatchStore) FinishNotificationRetry(_ context.Context, id, owner string, generation int64, next time.Time, code string) (FinishResult, error) {
	result, err := s.finish(id, owner, generation, DispatchRetry, code, "", true)
	if err == nil && result == FinishApplied {
		s.mu.Lock()
		nextCopy := next
		s.rows[id].next = &nextCopy
		s.mu.Unlock()
	}
	return result, err
}

func (s *fakeDispatchStore) FinishNotificationBlocked(ctx context.Context, id, owner string, generation int64, code string) (FinishResult, error) {
	_ = ctx
	return s.finish(id, owner, generation, DispatchBlocked, code, "", false)
}

func (s *fakeDispatchStore) FinishNotificationAmbiguous(_ context.Context, id, owner string, generation int64, code string) (FinishResult, error) {
	return s.finish(id, owner, generation, DispatchAmbiguous, code, "", false)
}

func dispatchTestSetup(sends *int, sendErr error) (*Dispatcher, *fakeDispatchStore, *Registry) {
	store := newFakeDispatchStore()
	registry := NewRegistry()
	provider := &stubProvider{key: "whatsapp-main", send: func(context.Context, TemplateSendRequest) (SendResult, error) {
		*sends++
		if sendErr != nil {
			return SendResult{}, sendErr
		}
		return SendResult{ProviderMessageID: "wamid.test1"}, nil
	}}
	if err := registry.Register("whatsapp-main", provider); err != nil {
		panic(err)
	}
	return NewDispatcher(store, registry, SystemClock{}, "worker-A", nil), store, registry
}

// TestDispatcherAccepted proves one claim → one send → accepted with
// the provider message ID.
func TestDispatcherAccepted(t *testing.T) {
	var sends int
	dispatcher, store, _ := dispatchTestSetup(&sends, nil)
	store.add("n1", false)
	dispatcher.drain(context.Background())
	if sends != 1 {
		t.Fatalf("sends: %d", sends)
	}
	row := store.rows["n1"]
	if row.dispatch != DispatchAccepted || row.wamid != "wamid.test1" {
		t.Fatalf("accepted: %+v", row)
	}
}

// TestDispatcherRetry429 proves explicit 429 → retry with honored
// backoff and cleared marker (next attempt authorized).
func TestDispatcherRetry429(t *testing.T) {
	var sends int
	dispatcher, store, _ := dispatchTestSetup(&sends, RateLimitedError("slow", 45*time.Second))
	store.add("n1", false)
	dispatcher.drain(context.Background())
	row := store.rows["n1"]
	if row.dispatch != DispatchRetry || row.code != CodeProviderRateLimited {
		t.Fatalf("retry: %+v", row)
	}
	if row.sendStarted {
		t.Fatal("retry must clear the send-start marker")
	}
	if row.next == nil || time.Until(*row.next) < 40*time.Second {
		t.Fatalf("backoff must honor Retry-After: %v", row.next)
	}
}

// TestDispatcherSendStartedNoSend proves a claimed marker routes to
// ambiguous with zero provider I/O.
func TestDispatcherSendStartedNoSend(t *testing.T) {
	var sends int
	dispatcher, store, _ := dispatchTestSetup(&sends, nil)
	store.add("n1", true)
	dispatcher.drain(context.Background())
	if sends != 0 {
		t.Fatalf("marked claim must not send: %d", sends)
	}
	if row := store.rows["n1"]; row.dispatch != DispatchAmbiguous {
		t.Fatalf("ambiguous: %+v", row)
	}
}

// TestDispatcherStaleFinish proves an expired worker cannot alter the
// newer owner's result.
func TestDispatcherStaleFinish(t *testing.T) {
	var sends int
	store := newFakeDispatchStore()
	registry := NewRegistry()
	provider := &stubProvider{key: "whatsapp-main", send: func(context.Context, TemplateSendRequest) (SendResult, error) {
		sends++
		return SendResult{ProviderMessageID: "wamid.test1"}, nil
	}}
	if err := registry.Register("whatsapp-main", provider); err != nil {
		t.Fatal(err)
	}
	workerA := NewDispatcher(store, registry, SystemClock{}, "worker-A", nil)
	workerB := NewDispatcher(store, registry, SystemClock{}, "worker-B", nil)
	store.add("n1", false)
	claimedA, _, err := store.ClaimNotification(context.Background(), "worker-A", time.Minute, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	store.expire("n1")
	// Worker B claims generation 2 and completes acceptance.
	workerB.drain(context.Background())
	if sends != 1 {
		t.Fatalf("B sends once: %d", sends)
	}
	// A's late retry finish must not alter B's acceptance.
	result, err := store.FinishNotificationRetry(context.Background(),
		"n1", claimedA.LeaseOwner, claimedA.LeaseGeneration, time.Now().UTC(), CodeProviderRetry)
	if err != nil || result != FinishStale {
		t.Fatalf("stale: %v %v", result, err)
	}
	if row := store.rows["n1"]; row.dispatch != DispatchAccepted {
		t.Fatalf("B result stands: %+v", row)
	}
	_ = workerA
}

// TestDispatcherCrashAfterStart proves crash-after-send-start converges
// to ambiguous with exactly one provider send total.
func TestDispatcherCrashAfterStart(t *testing.T) {
	var sends int
	dispatcher, store, _ := dispatchTestSetup(&sends, nil)
	store.add("n1", false)
	// Worker A claims and marks send-start, then disappears.
	claimed, _, err := store.ClaimNotification(context.Background(), "worker-A", time.Minute, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkNotificationSendStarted(context.Background(),
		"n1", claimed.LeaseOwner, claimed.LeaseGeneration); err != nil {
		t.Fatal(err)
	}
	store.expire("n1")
	// Worker B (same dispatcher) reclaims after expiry: must not send.
	dispatcher.drain(context.Background())
	if sends != 0 {
		t.Fatalf("no second send: %d", sends)
	}
	if row := store.rows["n1"]; row.dispatch != DispatchAmbiguous {
		t.Fatalf("ambiguous: %+v", row)
	}
}

// TestDispatcherAmbiguousError proves provider-side unknown outcomes
// finish ambiguous without retry.
func TestDispatcherAmbiguousError(t *testing.T) {
	for _, err := range []error{
		AmbiguousError("unknown"),
		errors.New("infrastructure boom"),
	} {
		var sends int
		dispatcher, store, _ := dispatchTestSetup(&sends, err)
		store.add("n1", false)
		dispatcher.drain(context.Background())
		if sends != 1 {
			t.Fatalf("one attempt: %d", sends)
		}
		if row := store.rows["n1"]; row.dispatch != DispatchAmbiguous {
			t.Fatalf("ambiguous for %v: %+v", err, row)
		}
	}
}

// TestDispatcherCancelledAfterStart proves caller cancellation after
// send start leaves the marker for ambiguous convergence instead of
// finishing terminal work on a dead context.
func TestDispatcherCancelledAfterStart(t *testing.T) {
	store := newFakeDispatchStore()
	registry := NewRegistry()
	provider := &stubProvider{key: "whatsapp-main", send: func(ctx context.Context, _ TemplateSendRequest) (SendResult, error) {
		return SendResult{}, ctx.Err()
	}}
	if err := registry.Register("whatsapp-main", provider); err != nil {
		t.Fatal(err)
	}
	dispatcher := NewDispatcher(store, registry, SystemClock{}, "worker-A", nil)
	store.add("n1", false)
	claimed, _, err := store.ClaimNotification(context.Background(), "worker-A", time.Minute, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dispatcher.processOne(ctx, claimed)
	row := store.rows["n1"]
	if row.finishCalls != 0 {
		t.Fatalf("no finish on dead context: %+v", row)
	}
	if !row.sendStarted {
		t.Fatal("send-start marker must stand")
	}
	// After expiry the next claim converges to ambiguous, never sends.
	store.expire("n1")
	dispatcher.drain(context.Background())
	if row := store.rows["n1"]; row.dispatch != DispatchAmbiguous {
		t.Fatalf("ambiguous: %+v", row)
	}
}
