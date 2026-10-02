package telegram

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
)

// fakeTelegramStore is an in-memory DispatchStore with real fencing
// rules: finishes require owner+generation on non-terminal rows.
// Seeded from enqueue-time snapshots, mirroring the durable table.
type fakeTelegramStore struct {
	mu   sync.Mutex
	rows map[string]*fakeTelegramRow
}

type fakeTelegramRow struct {
	claimed     notifications.ClaimedNotification
	dispatch    notifications.DispatchStatus
	code        string
	wamid       string
	sendStarted bool
	notBefore   time.Time
}

func newFakeTelegramStore() *fakeTelegramStore {
	return &fakeTelegramStore{rows: map[string]*fakeTelegramRow{}}
}

func (s *fakeTelegramStore) add(id string, sendStarted bool) {
	s.addRow(id, sendStarted, "telegram-main", "@operations",
		"operational_alert_open_v1", map[string]string{"alert_body": "node offline"},
		RendererBodyV1, []string{"alert_body"})
}

func (s *fakeTelegramStore) addRow(id string, sendStarted bool, providerKey, recipient, templateKey string, params map[string]string, extName string, order []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[id] = &fakeTelegramRow{
		claimed: notifications.ClaimedNotification{
			ID: id, ProviderKey: providerKey, IdempotencyKey: "tg-intent:" + id,
			Recipient: recipient, TemplateKey: templateKey, Locale: "ar",
			Parameters:        params,
			ExtTemplateName:   extName,
			ExtLanguageCode:   "ar",
			ExtParameterOrder: order,
			Dispatch:          notifications.DispatchPending,
		},
		dispatch:    notifications.DispatchPending,
		sendStarted: sendStarted,
	}
}

func (s *fakeTelegramStore) ClaimNotification(_ context.Context, owner string, _ time.Duration, now time.Time) (notifications.ClaimedNotification, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.rows {
		if row.dispatch != notifications.DispatchPending && row.dispatch != notifications.DispatchRetry {
			continue
		}
		if row.claimed.LeaseOwner != "" {
			continue
		}
		if row.dispatch == notifications.DispatchRetry && row.notBefore.After(now) {
			continue
		}
		row.claimed.LeaseOwner = owner
		row.claimed.LeaseGeneration++
		row.claimed.AttemptCount++
		row.claimed.SendStarted = row.sendStarted
		return row.claimed, true, nil
	}
	return notifications.ClaimedNotification{}, false, nil
}

func (s *fakeTelegramStore) MarkNotificationSendStarted(_ context.Context, id, owner string, generation int64) (notifications.FinishResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.rows[id]
	if row.claimed.LeaseOwner != owner || row.claimed.LeaseGeneration != generation {
		return notifications.FinishStale, nil
	}
	row.sendStarted = true
	return notifications.FinishApplied, nil
}

func (s *fakeTelegramStore) finish(id, owner string, generation int64, dispatch notifications.DispatchStatus, code, wamid string, clearMarker bool) (notifications.FinishResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.rows[id]
	if row.claimed.LeaseOwner != owner || row.claimed.LeaseGeneration != generation {
		return notifications.FinishStale, nil
	}
	if row.dispatch != notifications.DispatchPending && row.dispatch != notifications.DispatchRetry {
		return notifications.FinishStale, nil
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
	return notifications.FinishApplied, nil
}

func (s *fakeTelegramStore) FinishNotificationAccepted(_ context.Context, id, owner string, generation int64, wamid string) (notifications.FinishResult, error) {
	return s.finish(id, owner, generation, notifications.DispatchAccepted, "", wamid, false)
}

func (s *fakeTelegramStore) FinishNotificationRetry(_ context.Context, id, owner string, generation int64, next time.Time, code string) (notifications.FinishResult, error) {
	res, err := s.finish(id, owner, generation, notifications.DispatchRetry, code, "", true)
	if err == nil && res == notifications.FinishApplied {
		s.mu.Lock()
		s.rows[id].notBefore = next
		s.mu.Unlock()
	}
	return res, err
}

// makeDue forces a retry row claimable regardless of its backoff.
func (s *fakeTelegramStore) makeDue(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[id].notBefore = time.Time{}
}

func (s *fakeTelegramStore) FinishNotificationBlocked(_ context.Context, id, owner string, generation int64, code string) (notifications.FinishResult, error) {
	return s.finish(id, owner, generation, notifications.DispatchBlocked, code, "", false)
}

func (s *fakeTelegramStore) FinishNotificationAmbiguous(_ context.Context, id, owner string, generation int64, code string) (notifications.FinishResult, error) {
	return s.finish(id, owner, generation, notifications.DispatchAmbiguous, code, "", false)
}

func (s *fakeTelegramStore) state(id string) (notifications.DispatchStatus, string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.rows[id]
	return row.dispatch, row.code, row.wamid
}

func telegramDispatcher(store *fakeTelegramStore, harness *botHarness, owner string) *notifications.Dispatcher {
	registry := notifications.NewRegistry()
	provider, err := NewProvider(testBotConfig(harness), harness.server.Client())
	if err != nil {
		panic(err)
	}
	if err := registry.Register(provider.Key(), provider); err != nil {
		panic(err)
	}
	return notifications.NewDispatcher(store, registry, notifications.SystemClock{}, owner, slog.Default())
}

// TestTelegramDispatcherAccepts proves the canonical path: one claim,
// one provider send with the snapshot body, accepted with a
// chat-qualified identity, never resent on redrain.
func TestTelegramDispatcherAccepts(t *testing.T) {
	harness := newBotHarness(t)
	store := newFakeTelegramStore()
	store.add("tg-1", false)
	telegramDispatcher(store, harness, "worker-A").DrainForTest(context.Background())
	dispatch, code, wamid := store.state("tg-1")
	if dispatch != notifications.DispatchAccepted {
		t.Fatalf("dispatch %q code %q", dispatch, code)
	}
	if wamid == "" || len(harness.recorded()) != 1 {
		t.Fatalf("one send with identity, got %q (%d requests)", wamid, harness.count())
	}
	text := harness.recorded()[0].Body["text"]
	if text != "node offline" {
		t.Fatalf("snapshot body must travel verbatim: %v", text)
	}
	// Redrain: accepted rows are never resent.
	telegramDispatcher(store, harness, "worker-A").DrainForTest(context.Background())
	if harness.count() != 1 {
		t.Fatalf("accepted must never resend: %d requests", harness.count())
	}
}

// TestTelegramDispatcherRace proves fenced concurrency: many workers,
// one claim, exactly one remote send.
func TestTelegramDispatcherRace(t *testing.T) {
	harness := newBotHarness(t)
	store := newFakeTelegramStore()
	store.add("tg-race", false)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			telegramDispatcher(store, harness, "worker-race").DrainForTest(context.Background())
		}()
	}
	wg.Wait()
	if harness.count() != 1 {
		t.Fatalf("one remote call, got %d", harness.count())
	}
	if dispatch, _, _ := store.state("tg-race"); dispatch != notifications.DispatchAccepted {
		t.Fatalf("dispatch: %q", dispatch)
	}
}

// TestTelegramDispatcher429Retry proves flood-control handling: first
// 429 with retry_after schedules one retry, the later success accepts.
// Two provider requests total, no busy loop.
func TestTelegramDispatcher429Retry(t *testing.T) {
	harness := newBotHarness(t)
	calls := 0
	harness.script = func(_ botRecordedRequest) (int, any, map[string]string) {
		calls++
		if calls == 1 {
			return 429, botFailure(429, "Too Many Requests", map[string]any{"retry_after": 45}), nil
		}
		return 200, botSuccess(5, 9), nil
	}
	store := newFakeTelegramStore()
	store.add("tg-429", false)
	dispatcher := telegramDispatcher(store, harness, "worker-A")
	dispatcher.DrainForTest(context.Background())
	if dispatch, code, _ := store.state("tg-429"); dispatch != notifications.DispatchRetry || code != notifications.CodeProviderRateLimited {
		t.Fatalf("first must schedule retry: %q %q", dispatch, code)
	}
	if harness.count() != 1 {
		t.Fatalf("one attempt before backoff, got %d", harness.count())
	}
	// While backing off, redrains claim nothing: no busy loop.
	dispatcher.DrainForTest(context.Background())
	if harness.count() != 1 {
		t.Fatalf("backoff must not busy-loop: %d requests", harness.count())
	}
	store.makeDue("tg-429")
	dispatcher.DrainForTest(context.Background())
	if dispatch, _, wamid := store.state("tg-429"); dispatch != notifications.DispatchAccepted || wamid != "5:9" {
		t.Fatalf("retry must accept: %q %q", dispatch, wamid)
	}
	if harness.count() != 2 {
		t.Fatalf("two total requests, got %d", harness.count())
	}
}

// TestTelegramDispatcherPermanentBlock proves invalid destinations
// block after exactly one attempt with no auto-retry.
func TestTelegramDispatcherPermanentBlock(t *testing.T) {
	harness := newBotHarness(t)
	harness.script = func(_ botRecordedRequest) (int, any, map[string]string) {
		return 403, botFailure(403, "Forbidden: bot was blocked by the user", nil), nil
	}
	store := newFakeTelegramStore()
	store.add("tg-blocked", false)
	dispatcher := telegramDispatcher(store, harness, "worker-A")
	dispatcher.DrainForTest(context.Background())
	if dispatch, code, _ := store.state("tg-blocked"); dispatch != notifications.DispatchBlocked || code != notifications.CodeProviderValidation {
		t.Fatalf("must block with validation code: %q %q", dispatch, code)
	}
	dispatcher.DrainForTest(context.Background())
	if harness.count() != 1 {
		t.Fatalf("blocked must never retry: %d requests", harness.count())
	}
}

// TestTelegramDispatcherAmbiguousNoResend proves the critical
// invariant: an uncertain send becomes ambiguous and a restart
// performs no duplicate send.
func TestTelegramDispatcherAmbiguousNoResend(t *testing.T) {
	harness := newBotHarness(t)
	harness.hangup = true
	store := newFakeTelegramStore()
	store.add("tg-amb", false)
	telegramDispatcher(store, harness, "worker-A").DrainForTest(context.Background())
	if dispatch, code, _ := store.state("tg-amb"); dispatch != notifications.DispatchAmbiguous || code != notifications.CodeSendAmbiguous {
		t.Fatalf("must ambiguate: %q %q", dispatch, code)
	}
	// Restart after ambiguity: no provider resend.
	telegramDispatcher(store, harness, "worker-B").DrainForTest(context.Background())
	if harness.count() != 1 {
		t.Fatalf("ambiguous must never auto-resend: %d requests", harness.count())
	}
}

// TestTelegramDispatcherCrashAfterSendStart proves the durable marker:
// a row whose prior attempt began without a safe outcome converges
// to ambiguous with zero provider I/O.
func TestTelegramDispatcherCrashAfterSendStart(t *testing.T) {
	harness := newBotHarness(t)
	store := newFakeTelegramStore()
	store.add("tg-crash", true)
	telegramDispatcher(store, harness, "worker-B").DrainForTest(context.Background())
	if dispatch, _, _ := store.state("tg-crash"); dispatch != notifications.DispatchAmbiguous {
		t.Fatalf("crash marker must ambiguate: %q", dispatch)
	}
	if harness.count() != 0 {
		t.Fatalf("zero provider I/O after crash marker, got %d", harness.count())
	}
}

// TestTelegramDispatcherConcurrentRows proves different rows dispatch
// independently: no global Telegram serialization.
func TestTelegramDispatcherConcurrentRows(t *testing.T) {
	harness := newBotHarness(t)
	store := newFakeTelegramStore()
	store.add("tg-a", false)
	store.add("tg-b", false)
	telegramDispatcher(store, harness, "worker-A").DrainForTest(context.Background())
	if harness.count() != 2 {
		t.Fatalf("two rows, two sends: %d", harness.count())
	}
	for _, id := range []string{"tg-a", "tg-b"} {
		if dispatch, _, _ := store.state(id); dispatch != notifications.DispatchAccepted {
			t.Fatalf("%s: %q", id, dispatch)
		}
	}
}
