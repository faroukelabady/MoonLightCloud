package postgres

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
)

func notificationTestMapping(t *testing.T, env *saleEnv, provider string) {
	t.Helper()
	if err := catalogStore(env).UpsertTemplateMapping(context.Background(), notifications.TemplateMapping{
		ProviderKey: provider, TemplateKey: "operator_test_v1", Locale: "ar",
		ExternalTemplateName: "moonlight_operator_test_ar", ExternalLanguageCode: "ar",
		ParameterNames: []string{"name", "message"}, Enabled: true,
	}); err != nil {
		t.Fatalf("mapping: %v", err)
	}
}

func notificationTestIntent(provider, key string) notifications.EnqueueIntent {
	return notifications.EnqueueIntent{
		ProviderKey: provider, IdempotencyKey: key,
		Recipient: "201012345678", TemplateKey: "operator_test_v1", Locale: "ar",
		Parameters:      map[string]string{"name": "Moon Light", "message": "hello"},
		ExtTemplateName: "moonlight_operator_test_ar", ExtLanguageCode: "ar",
		ExtParameterOrder: []string{"name", "message"},
	}
}

func enqueueNotification(t *testing.T, env *saleEnv, intent notifications.EnqueueIntent) string {
	t.Helper()
	id, outcome, err := catalogStore(env).EnqueueNotification(context.Background(), intent)
	if err != nil || outcome != notifications.EnqueueInserted {
		t.Fatalf("enqueue: %v %v", outcome, err)
	}
	return id
}

func claimNotification(t *testing.T, env *saleEnv, owner string) notifications.ClaimedNotification {
	t.Helper()
	claimed, ok, err := catalogStore(env).ClaimNotification(
		context.Background(), owner, 5*time.Minute, time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	return claimed
}

// TestNotificationEnqueueIdempotency proves same key+payload dedupes
// and same key+different payload conflicts without overwriting.
func TestNotificationEnqueueIdempotency(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	intent := notificationTestIntent("whatsapp-main", "key-1")
	first, outcome, err := store.EnqueueNotification(ctx, intent)
	if err != nil || outcome != notifications.EnqueueInserted {
		t.Fatalf("first: %v %v", outcome, err)
	}
	second, outcome, err := store.EnqueueNotification(ctx, intent)
	if err != nil || outcome != notifications.EnqueueDuplicateIdentical || second != first {
		t.Fatalf("duplicate: %v %v %v", second, outcome, err)
	}
	changed := intent
	changed.Parameters = map[string]string{"name": "Moon Light", "message": "other"}
	if _, _, err := store.EnqueueNotification(ctx, changed); err == nil {
		t.Fatal("conflicting payload must fail")
	}
	// Original row untouched by the conflict.
	stored, found, err := store.LookupByProviderMessage(ctx, "whatsapp-main", "wamid-never")
	if err != nil || found || stored != "" {
		t.Fatalf("lookup: %v %v %v", stored, found, err)
	}
}

// TestNotificationClaimLease proves lease generations fence stale
// workers: expiry → reclaim gen+1 → terminal stands.
func TestNotificationClaimLease(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	id := enqueueNotification(t, env, notificationTestIntent("whatsapp-main", "key-1"))
	claimedA := claimNotification(t, env, "worker-A")
	if claimedA.ID != id || claimedA.LeaseGeneration != 1 {
		t.Fatalf("gen1: %+v", claimedA)
	}
	if _, ok, err := store.ClaimNotification(ctx, "worker-B", 5*time.Minute, time.Now().UTC()); err != nil || ok {
		t.Fatalf("held lease must not claim: %v %v", ok, err)
	}
	if _, err := env.pool.Exec(ctx,
		`UPDATE notification_messages SET lease_until = now() - interval '1 minute' WHERE id = $1`, mustPgID(id)); err != nil {
		t.Fatal(err)
	}
	claimedB := claimNotification(t, env, "worker-B")
	if claimedB.LeaseGeneration != 2 {
		t.Fatalf("gen2: %+v", claimedB)
	}
	result, err := store.FinishNotificationAccepted(ctx, id, claimedB.LeaseOwner, claimedB.LeaseGeneration, "wamid.1")
	if err != nil || result != notifications.FinishApplied {
		t.Fatalf("accept: %v %v", result, err)
	}
	result, err = store.FinishNotificationRetry(ctx, id,
		claimedA.LeaseOwner, claimedA.LeaseGeneration, time.Now().UTC().Add(time.Minute), "x")
	if err != nil || result != notifications.FinishStale {
		t.Fatalf("stale: %v %v", result, err)
	}
	found, ok, err := store.LookupByProviderMessage(ctx, "whatsapp-main", "wamid.1")
	if err != nil || !ok || found != id {
		t.Fatalf("correlation: %v %v %v", found, ok, err)
	}
}

// TestNotificationSendStartMarker proves the durable marker survives
// for crash recovery: marked claims route to ambiguous, and retry
// finishes clear the marker.
func TestNotificationSendStartMarker(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	id := enqueueNotification(t, env, notificationTestIntent("whatsapp-main", "key-1"))
	claimed := claimNotification(t, env, "worker-A")
	result, err := store.MarkNotificationSendStarted(ctx, id, claimed.LeaseOwner, claimed.LeaseGeneration)
	if err != nil || result != notifications.FinishApplied {
		t.Fatalf("mark: %v %v", result, err)
	}
	if _, err := env.pool.Exec(ctx,
		`UPDATE notification_messages SET lease_until = now() - interval '1 minute' WHERE id = $1`, mustPgID(id)); err != nil {
		t.Fatal(err)
	}
	reclaimed := claimNotification(t, env, "worker-B")
	if !reclaimed.SendStarted {
		t.Fatal("marker must be visible on reclaim")
	}
	result, err = store.FinishNotificationAmbiguous(ctx, id,
		reclaimed.LeaseOwner, reclaimed.LeaseGeneration, notifications.CodeSendAmbiguous)
	if err != nil || result != notifications.FinishApplied {
		t.Fatalf("ambiguous: %v %v", result, err)
	}
}

// TestNotificationConcurrentEnqueue proves 16 identical racers converge
// on one notification, and contradictory racers conflict.
func TestNotificationConcurrentEnqueue(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	intent := notificationTestIntent("whatsapp-main", "race-key")
	var wg sync.WaitGroup
	ids := make(chan string, 16)
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, _, err := store.EnqueueNotification(ctx, intent)
			ids <- id
			errs <- err
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("identical racers must all succeed: %v", err)
		}
	}
	if len(seen) != 1 {
		t.Fatalf("one notification: %v", seen)
	}
	// Contradictory payload under the same key conflicts.
	changed := intent
	changed.Parameters = map[string]string{"name": "x", "message": "y"}
	if _, _, err := store.EnqueueNotification(ctx, changed); err == nil {
		t.Fatal("conflict must fail")
	}
}

// TestNotificationMessageIDCollision proves a duplicate provider
// message ID fails safely without reassigning ownership: the second
// row keeps its send-start marker and stays pending for ambiguous
// convergence instead of stealing the first notification's identity.
func TestNotificationMessageIDCollision(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	first := enqueueNotification(t, env, notificationTestIntent("whatsapp-main", "key-1"))
	second := enqueueNotification(t, env, notificationTestIntent("whatsapp-main", "key-2"))
	claimed := claimNotification(t, env, "worker-A")
	if claimed.ID != first {
		t.Fatalf("claim order: %v %v", claimed.ID, first)
	}
	result, err := store.FinishNotificationAccepted(ctx, first, claimed.LeaseOwner, claimed.LeaseGeneration, "wamid.dup")
	if err != nil || result != notifications.FinishApplied {
		t.Fatalf("first accept: %v %v", result, err)
	}
	if _, err := env.pool.Exec(ctx,
		`UPDATE notification_messages SET lease_until = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	claimed2 := claimNotification(t, env, "worker-B")
	if claimed2.ID != second {
		t.Fatalf("second claim: %v", claimed2.ID)
	}
	if _, err := store.MarkNotificationSendStarted(ctx, second, claimed2.LeaseOwner, claimed2.LeaseGeneration); err != nil {
		t.Fatal(err)
	}
	_, err = store.FinishNotificationAccepted(ctx, second, claimed2.LeaseOwner, claimed2.LeaseGeneration, "wamid.dup")
	if err == nil {
		t.Fatal("duplicate provider message ID must fail")
	}
	var dispatch, wamid string
	var marker bool
	if err := env.pool.QueryRow(ctx,
		`SELECT dispatch_status, COALESCE(provider_message_id,''), send_started_at IS NOT NULL
		 FROM notification_messages WHERE id = $1`, mustPgID(second)).Scan(&dispatch, &wamid, &marker); err != nil {
		t.Fatal(err)
	}
	if dispatch != "pending" || wamid != "" || !marker {
		t.Fatalf("second row safe: %s %q marker=%v", dispatch, wamid, marker)
	}
	var owner string
	if err := env.pool.QueryRow(ctx,
		`SELECT provider_message_id FROM notification_messages WHERE id = $1`,
		mustPgID(first)).Scan(&owner); err != nil || owner != "wamid.dup" {
		t.Fatalf("first row keeps identity: %q %v", owner, err)
	}
}

// TestNotificationDeliveryOrdering proves timestamp-primary ordering
// with same-timestamp precedence, duplicate dedupe, and UNKNOWN
// history-only behavior on real PostgreSQL.
func TestNotificationDeliveryOrdering(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	id := enqueueNotification(t, env, notificationTestIntent("whatsapp-main", "key-1"))
	claimed := claimNotification(t, env, "worker-A")
	if result, err := store.FinishNotificationAccepted(ctx, id, claimed.LeaseOwner, claimed.LeaseGeneration, "wamid.9"); err != nil || result != notifications.FinishApplied {
		t.Fatalf("accept: %v %v", result, err)
	}
	base := time.Now().UTC().Truncate(time.Second)
	at := func(offset int64) *time.Time {
		parsed := base.Add(time.Duration(offset) * time.Second)
		return &parsed
	}
	apply := func(raw string, seconds int64) notifications.DeliveryOutcome {
		t.Helper()
		event := notifications.DeliveryEvent{
			ProviderMessageID: "wamid.9", RawStatus: raw,
			Canonical:         notifications.MapProviderStatus(raw),
			ProviderTimestamp: at(seconds),
		}
		event.Fingerprint = notifications.DeliveryEventFingerprint(
			"whatsapp-main", event.ProviderMessageID, event.RawStatus,
			event.Canonical, event.ProviderTimestamp, "")
		outcome, err := store.ApplyDeliveryStatus(ctx, id, event)
		if err != nil {
			t.Fatalf("apply %s: %v", raw, err)
		}
		return outcome
	}
	current := func() (string, bool) {
		t.Helper()
		var status string
		var hasAt bool
		if err := env.pool.QueryRow(ctx,
			`SELECT delivery_status, delivery_status_at IS NOT NULL FROM notification_messages WHERE id = $1`,
			mustPgID(id)).Scan(&status, &hasAt); err != nil {
			t.Fatal(err)
		}
		return status, hasAt
	}
	// Out-of-order arrival: read T3, sent T1, delivered T2 → READ.
	if outcome := apply("read", 300); !outcome.HistoryInserted || !outcome.CurrentAdvanced {
		t.Fatalf("read: %+v", outcome)
	}
	if outcome := apply("sent", 100); !outcome.HistoryInserted || outcome.CurrentAdvanced {
		t.Fatalf("older sent history-only: %+v", outcome)
	}
	if outcome := apply("delivered", 200); !outcome.HistoryInserted || outcome.CurrentAdvanced {
		t.Fatalf("older delivered history-only: %+v", outcome)
	}
	if status, _ := current(); status != "READ" {
		t.Fatalf("current READ: %s", status)
	}
	// Duplicate read: neither history nor churn.
	if outcome := apply("read", 300); outcome.HistoryInserted || outcome.CurrentAdvanced {
		t.Fatalf("duplicate: %+v", outcome)
	}
	// Failed at T4 advances; dispatch stays accepted (asserted below).
	if outcome := apply("failed", 400); !outcome.CurrentAdvanced {
		t.Fatalf("failed: %+v", outcome)
	}
	if status, _ := current(); status != "FAILED" {
		t.Fatalf("current FAILED: %s", status)
	}
	// UNKNOWN records history only.
	unknown := notifications.DeliveryEvent{
		ProviderMessageID: "wamid.9", RawStatus: "future_status_x",
		Canonical: notifications.DeliveryUnknown, ProviderTimestamp: at(500),
	}
	unknown.Fingerprint = notifications.DeliveryEventFingerprint(
		"whatsapp-main", unknown.ProviderMessageID, unknown.RawStatus,
		unknown.Canonical, unknown.ProviderTimestamp, "")
	outcome, err := store.ApplyDeliveryStatus(ctx, id, unknown)
	if err != nil || !outcome.HistoryInserted || outcome.CurrentAdvanced {
		t.Fatalf("unknown: %+v %v", outcome, err)
	}
	var dispatch, wamid string
	if err := env.pool.QueryRow(ctx,
		`SELECT dispatch_status, provider_message_id FROM notification_messages WHERE id = $1`,
		mustPgID(id)).Scan(&dispatch, &wamid); err != nil || dispatch != "accepted" || wamid != "wamid.9" {
		t.Fatalf("dispatch intact: %s %s %v", dispatch, wamid, err)
	}
	var history int
	if err := env.pool.QueryRow(ctx,
		`SELECT count(*) FROM notification_delivery_status_history WHERE notification_id = $1`,
		mustPgID(id)).Scan(&history); err != nil || history != 5 {
		t.Fatalf("history rows: %d %v", history, err)
	}
}

// TestNotificationTemplateMapping proves mapping CRUD shape and lookup
// isolation.
func TestNotificationTemplateMapping(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	notificationTestMapping(t, env, "whatsapp-main")
	mapping, found, err := store.GetTemplateMapping(ctx, "whatsapp-main", "operator_test_v1", "ar")
	if err != nil || !found || mapping.ExternalTemplateName != "moonlight_operator_test_ar" || !mapping.Enabled {
		t.Fatalf("lookup: %+v %v %v", mapping, found, err)
	}
	if _, found, err := store.GetTemplateMapping(ctx, "whatsapp-main", "operator_test_v1", "en"); err != nil || found {
		t.Fatalf("locale isolation: %v %v", found, err)
	}
	if _, found, err := store.GetTemplateMapping(ctx, "other", "operator_test_v1", "ar"); err != nil || found {
		t.Fatalf("provider isolation: %v %v", found, err)
	}
	disabled := mapping
	disabled.Enabled = false
	if err := store.UpsertTemplateMapping(ctx, disabled); err != nil {
		t.Fatal(err)
	}
	updated, found, err := store.GetTemplateMapping(ctx, "whatsapp-main", "operator_test_v1", "ar")
	if err != nil || !found || updated.Enabled {
		t.Fatalf("disable: %+v %v %v", updated, found, err)
	}
	listed, err := store.ListTemplateMappings(ctx, "whatsapp-main")
	if err != nil || len(listed) != 1 {
		t.Fatalf("list: %+v %v", listed, err)
	}
}

// TestNotificationQueueStats proves operational counts and oldest age.
func TestNotificationQueueStats(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	enqueueNotification(t, env, notificationTestIntent("whatsapp-main", "key-1"))
	enqueueNotification(t, env, notificationTestIntent("whatsapp-main", "key-2"))
	stats, err := store.NotificationQueueStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 2 || stats.Retry != 0 || stats.Accepted != 0 || stats.Blocked != 0 || stats.Ambiguous != 0 {
		t.Fatalf("counts: %+v", stats)
	}
	if stats.OldestPending == nil {
		t.Fatal("oldest pending must be set")
	}
	fresh := openSaleEnv(t)
	none, err := catalogStore(fresh).NotificationQueueStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if none.Pending != 0 || none.OldestPending != nil {
		t.Fatalf("empty queue: %+v", none)
	}
}

// scriptNotificationProvider is a scripted notifications provider for
// dispatcher integration tests over the real store.
type scriptNotificationProvider struct {
	key   notifications.ProviderKey
	calls atomic.Int32
	send  func(ctx context.Context, req notifications.TemplateSendRequest) (notifications.SendResult, error)
}

func (s *scriptNotificationProvider) Key() notifications.ProviderKey { return s.key }

func (s *scriptNotificationProvider) SendTemplate(ctx context.Context, req notifications.TemplateSendRequest) (notifications.SendResult, error) {
	s.calls.Add(1)
	return s.send(ctx, req)
}

func (s *scriptNotificationProvider) count() int { return int(s.calls.Load()) }

func notificationDispatcher(t *testing.T, store *scriptDispatchStoreView, provider *scriptNotificationProvider, owner string) *notifications.Dispatcher {
	t.Helper()
	registry := notifications.NewRegistry()
	if err := registry.Register("whatsapp-main", provider); err != nil {
		t.Fatal(err)
	}
	return notifications.NewDispatcher(store.iface(), registry, notifications.SystemClock{}, owner, nil)
}

// scriptDispatchStoreView exposes the real Devices store as the
// dispatcher boundary. Devices already implements it; this documents
// the seam for fault-injection wrappers.
type scriptDispatchStoreView struct {
	store interface {
		notifications.DispatchStore
	}
}

func (v *scriptDispatchStoreView) iface() notifications.DispatchStore { return v.store }

// TestNotificationDispatcherRace proves two dispatchers racing one
// pending notification produce exactly one provider send.
func TestNotificationDispatcherRace(t *testing.T) {
	env := openSaleEnv(t)
	store := catalogStore(env)
	enqueueNotification(t, env, notificationTestIntent("whatsapp-main", "key-1"))
	provider := &scriptNotificationProvider{key: "whatsapp-main", send: func(context.Context, notifications.TemplateSendRequest) (notifications.SendResult, error) {
		return notifications.SendResult{ProviderMessageID: "wamid.race1"}, nil
	}}
	view := &scriptDispatchStoreView{store: store}
	workerA := notificationDispatcher(t, view, provider, "worker-A")
	workerB := notificationDispatcher(t, view, provider, "worker-B")
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, worker := range []*notifications.Dispatcher{workerA, workerB} {
		wg.Add(1)
		go func(d *notifications.Dispatcher) {
			defer wg.Done()
			<-start
			d.DrainForTest(context.Background())
		}(worker)
	}
	close(start)
	wg.Wait()
	if provider.count() != 1 {
		t.Fatalf("one send: %d", provider.count())
	}
}

// TestNotificationDispatcherCrashBeforeSend proves a claim without a
// send-start marker is safely reclaimable and sent exactly once.
func TestNotificationDispatcherCrashBeforeSend(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	id := enqueueNotification(t, env, notificationTestIntent("whatsapp-main", "key-1"))
	claimed, ok, err := store.ClaimNotification(ctx, "worker-A", 5*time.Minute, time.Now().UTC())
	if err != nil || !ok || claimed.ID != id {
		t.Fatalf("claim: %v %v", ok, err)
	}
	// Crash before send-start: expire the lease without marking.
	if _, err := env.pool.Exec(ctx,
		`UPDATE notification_messages SET lease_until = now() - interval '1 minute' WHERE id = $1`, mustPgID(id)); err != nil {
		t.Fatal(err)
	}
	provider := &scriptNotificationProvider{key: "whatsapp-main", send: func(context.Context, notifications.TemplateSendRequest) (notifications.SendResult, error) {
		return notifications.SendResult{ProviderMessageID: "wamid.crash1"}, nil
	}}
	view := &scriptDispatchStoreView{store: store}
	notificationDispatcher(t, view, provider, "worker-B").DrainForTest(ctx)
	if provider.count() != 1 {
		t.Fatalf("one send: %d", provider.count())
	}
	var dispatch string
	if err := env.pool.QueryRow(ctx,
		`SELECT dispatch_status FROM notification_messages WHERE id = $1`, mustPgID(id)).Scan(&dispatch); err != nil || dispatch != "accepted" {
		t.Fatalf("accepted: %s %v", dispatch, err)
	}
}

// failAcceptStore fails the first accepted persistence to prove
// accept+DB-failure converges to ambiguous without resending.
type failAcceptStore struct {
	notifications.DispatchStore
	mu      sync.Mutex
	fail568 bool
	failed  bool
}

func (s *failAcceptStore) FinishNotificationAccepted(ctx context.Context, id, owner string, generation int64, wamid string) (notifications.FinishResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.failed {
		s.failed = true
		return notifications.FinishStale, errInjectedDB()
	}
	return s.DispatchStore.FinishNotificationAccepted(ctx, id, owner, generation, wamid)
}

func errInjectedDB() error { return errInjectedDBValue }

type errInjectedDBType string

func (e errInjectedDBType) Error() string { return "injected db failure" }

var errInjectedDBValue = errInjectedDBType("injected db failure")

// TestNotificationDispatcherAcceptDBFailure proves provider-accepted
// but persistence-failed converges to ambiguous with no second send.
func TestNotificationDispatcherAcceptDBFailure(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	id := enqueueNotification(t, env, notificationTestIntent("whatsapp-main", "key-1"))
	provider := &scriptNotificationProvider{key: "whatsapp-main", send: func(context.Context, notifications.TemplateSendRequest) (notifications.SendResult, error) {
		return notifications.SendResult{ProviderMessageID: "wamid.dbfail1"}, nil
	}}
	registry := notifications.NewRegistry()
	if err := registry.Register("whatsapp-main", provider); err != nil {
		t.Fatal(err)
	}
	faulty := &failAcceptStore{DispatchStore: store}
	worker := notifications.NewDispatcher(faulty, registry, notifications.SystemClock{}, "worker-A", nil)
	worker.DrainForTest(ctx)
	if provider.count() != 1 {
		t.Fatalf("one send: %d", provider.count())
	}
	// Lease expiry lets the next cycle observe the standing marker.
	if _, err := env.pool.Exec(ctx,
		`UPDATE notification_messages SET lease_until = now() - interval '1 minute' WHERE id = $1`, mustPgID(id)); err != nil {
		t.Fatal(err)
	}
	// Next cycle must not resend: the marker stands, outcome unknown.
	worker.DrainForTest(ctx)
	if provider.count() != 1 {
		t.Fatalf("no second send: %d", provider.count())
	}
	var dispatch string
	if err := env.pool.QueryRow(ctx,
		`SELECT dispatch_status FROM notification_messages WHERE id = $1`, mustPgID(id)).Scan(&dispatch); err != nil || dispatch != "ambiguous" {
		t.Fatalf("ambiguous: %s %v", dispatch, err)
	}
}

// TestNotificationDispatcherRetryThenAccept proves an explicit 429
// schedules backoff and the next due attempt sends to acceptance.
func TestNotificationDispatcherRetryThenAccept(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	id := enqueueNotification(t, env, notificationTestIntent("whatsapp-main", "key-1"))
	provider := &scriptNotificationProvider{key: "whatsapp-main"}
	provider.send = func(context.Context, notifications.TemplateSendRequest) (notifications.SendResult, error) {
		if provider.count() == 1 {
			return notifications.SendResult{}, notifications.RateLimitedError("slow", 0)
		}
		return notifications.SendResult{ProviderMessageID: "wamid.retry1"}, nil
	}
	registry := notifications.NewRegistry()
	if err := registry.Register("whatsapp-main", provider); err != nil {
		t.Fatal(err)
	}
	worker := notifications.NewDispatcher(store, registry, notifications.SystemClock{}, "worker-A", nil)
	worker.DrainForTest(ctx)
	var dispatch string
	if err := env.pool.QueryRow(ctx,
		`SELECT dispatch_status FROM notification_messages WHERE id = $1`, mustPgID(id)).Scan(&dispatch); err != nil || dispatch != "retry" {
		t.Fatalf("retry: %s %v", dispatch, err)
	}
	if _, err := env.pool.Exec(ctx,
		`UPDATE notification_messages SET next_attempt_at = now() - interval '1 second' WHERE id = $1`, mustPgID(id)); err != nil {
		t.Fatal(err)
	}
	worker.DrainForTest(ctx)
	if provider.count() != 2 {
		t.Fatalf("two attempts: %d", provider.count())
	}
	if err := env.pool.QueryRow(ctx,
		`SELECT dispatch_status FROM notification_messages WHERE id = $1`, mustPgID(id)).Scan(&dispatch); err != nil || dispatch != "accepted" {
		t.Fatalf("accepted: %s %v", dispatch, err)
	}
}

// TestNotificationMappingSnapshotImmutable proves a mapping edit after
// enqueue cannot silently transform the queued message: the claim
// carries the enqueue-time snapshot.
func TestNotificationMappingSnapshotImmutable(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	notificationTestMapping(t, env, "whatsapp-main")
	enqueueNotification(t, env, notificationTestIntent("whatsapp-main", "key-1"))
	edited := notifications.TemplateMapping{
		ProviderKey: "whatsapp-main", TemplateKey: "operator_test_v1", Locale: "ar",
		ExternalTemplateName: "changed_name", ExternalLanguageCode: "ar",
		ParameterNames: []string{"name", "message"}, Enabled: true,
	}
	if err := store.UpsertTemplateMapping(ctx, edited); err != nil {
		t.Fatal(err)
	}
	claimed := claimNotification(t, env, "worker-A")
	if claimed.ExtTemplateName != "moonlight_operator_test_ar" {
		t.Fatalf("snapshot must stand: %q", claimed.ExtTemplateName)
	}
}

// TestNotificationConcurrentIdempotencyConflict proves same-key
// contradictory racers resolve to exactly one winner plus conflicts,
// never a silent overwrite.
func TestNotificationConcurrentIdempotencyConflict(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	var wg sync.WaitGroup
	type enqueueResult struct {
		id      string
		outcome notifications.EnqueueOutcome
		err     error
	}
	results := make(chan enqueueResult, 2)
	intents := []notifications.EnqueueIntent{
		notificationTestIntent("whatsapp-main", "fight-key"),
		func() notifications.EnqueueIntent {
			changed := notificationTestIntent("whatsapp-main", "fight-key")
			changed.Parameters = map[string]string{"name": "x", "message": "y"}
			return changed
		}(),
	}
	for _, intent := range intents {
		wg.Add(1)
		go func(intent notifications.EnqueueIntent) {
			defer wg.Done()
			id, outcome, err := store.EnqueueNotification(ctx, intent)
			results <- enqueueResult{id: id, outcome: outcome, err: err}
		}(intent)
	}
	wg.Wait()
	close(results)
	var inserted, conflicts int
	for result := range results {
		if result.err != nil {
			conflicts++
			continue
		}
		if result.outcome == notifications.EnqueueInserted {
			inserted++
		}
	}
	if inserted != 1 || conflicts != 1 {
		t.Fatalf("one winner one conflict: inserted=%d conflicts=%d", inserted, conflicts)
	}
}

// TestNotificationConcurrentStatusApplies proves concurrent callbacks
// converge deterministically: one history per unique event, current
// follows provider timestamps.
func TestNotificationConcurrentStatusApplies(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	id := enqueueNotification(t, env, notificationTestIntent("whatsapp-main", "key-1"))
	claimed := claimNotification(t, env, "worker-A")
	if result, err := store.FinishNotificationAccepted(ctx, id, claimed.LeaseOwner, claimed.LeaseGeneration, "wamid.race"); err != nil || result != notifications.FinishApplied {
		t.Fatalf("accept: %v %v", result, err)
	}
	base := time.Now().UTC().Truncate(time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses := []string{"sent", "delivered", "read"}
			raw := statuses[i%3]
			offset := int64((i%3)*10 + 5)
			at := base.Add(time.Duration(offset) * time.Second)
			event := notifications.DeliveryEvent{
				ProviderMessageID: "wamid.race", RawStatus: raw,
				Canonical:         notifications.MapProviderStatus(raw),
				ProviderTimestamp: &at,
			}
			event.Fingerprint = notifications.DeliveryEventFingerprint(
				"whatsapp-main", event.ProviderMessageID, event.RawStatus,
				event.Canonical, event.ProviderTimestamp, "")
			if _, err := store.ApplyDeliveryStatus(ctx, id, event); err != nil {
				t.Errorf("apply: %v", err)
			}
		}(i)
	}
	wg.Wait()
	var status string
	var history int
	if err := env.pool.QueryRow(ctx,
		`SELECT delivery_status FROM notification_messages WHERE id = $1`, mustPgID(id)).Scan(&status); err != nil {
		t.Fatal(err)
	}
	// Latest timestamp (read at +25s) must win regardless of arrival.
	if status != "READ" {
		t.Fatalf("current READ: %s", status)
	}
	if err := env.pool.QueryRow(ctx,
		`SELECT count(*) FROM notification_delivery_status_history WHERE notification_id = $1`,
		mustPgID(id)).Scan(&history); err != nil || history != 3 {
		t.Fatalf("one history per unique event: %d %v", history, err)
	}
}

// TestNotificationAcceptedBaselineOrdering proves local ACCEPTED
// yields to provider callbacks: same-second, earlier-than-persistence,
// concurrent initial, same-second precedence, and FAILED-first.
func TestNotificationAcceptedBaselineOrdering(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	apply := func(id, wamid, raw string, at time.Time) notifications.DeliveryOutcome {
		t.Helper()
		event := notifications.DeliveryEvent{
			ProviderMessageID: wamid, RawStatus: raw,
			Canonical:         notifications.MapProviderStatus(raw),
			ProviderTimestamp: &at,
		}
		event.Fingerprint = notifications.DeliveryEventFingerprint(
			"whatsapp-main", event.ProviderMessageID, event.RawStatus,
			event.Canonical, event.ProviderTimestamp, "")
		outcome, err := store.ApplyDeliveryStatus(ctx, id, event)
		if err != nil {
			t.Fatalf("apply %s: %v", raw, err)
		}
		return outcome
	}
	current := func(id string) (string, time.Time) {
		t.Helper()
		var status string
		var at time.Time
		if err := env.pool.QueryRow(ctx,
			`SELECT delivery_status, delivery_status_at FROM notification_messages WHERE id = $1`,
			mustPgID(id)).Scan(&status, &at); err != nil {
			t.Fatal(err)
		}
		return status, at
	}
	t.Run("same second advances", func(t *testing.T) {
		id := enqueueNotification(t, env, notificationTestIntent("whatsapp-main", "base-1"))
		claimed := claimNotification(t, env, "worker-A")
		if result, err := store.FinishNotificationAccepted(ctx, id, claimed.LeaseOwner, claimed.LeaseGeneration, "wamid.base1"); err != nil || result != notifications.FinishApplied {
			t.Fatalf("accept: %v %v", result, err)
		}
		// Provider second-granularity callback at/below local accept time.
		var acceptedAt time.Time
		if err := env.pool.QueryRow(ctx,
			`SELECT delivery_status_at FROM notification_messages WHERE id = $1`,
			mustPgID(id)).Scan(&acceptedAt); err != nil {
			t.Fatal(err)
		}
		providerSecond := acceptedAt.Truncate(time.Second)
		outcome := apply(id, "wamid.base1", "delivered", providerSecond)
		if !outcome.CurrentAdvanced {
			t.Fatal("same-second DELIVERED must advance")
		}
		status, at := current(id)
		if status != "DELIVERED" || !at.Equal(providerSecond) {
			t.Fatalf("DELIVERED at provider ts: %s %v", status, at)
		}
	})
	t.Run("earlier provider timestamp advances", func(t *testing.T) {
		id := enqueueNotification(t, env, notificationTestIntent("whatsapp-main", "base-2"))
		claimed := claimNotification(t, env, "worker-A")
		if result, err := store.FinishNotificationAccepted(ctx, id, claimed.LeaseOwner, claimed.LeaseGeneration, "wamid.base2"); err != nil || result != notifications.FinishApplied {
			t.Fatalf("accept: %v %v", result, err)
		}
		earlier := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
		if outcome := apply(id, "wamid.base2", "sent", earlier); !outcome.CurrentAdvanced {
			t.Fatal("predated SENT must establish provider state")
		}
		if status, _ := current(id); status != "SENT" {
			t.Fatalf("SENT: %s", status)
		}
		// Normal ordering resumes after the baseline yields.
		later := earlier.Add(time.Minute)
		if outcome := apply(id, "wamid.base2", "delivered", later); !outcome.CurrentAdvanced {
			t.Fatal("subsequent DELIVERED must advance")
		}
	})
	t.Run("failed first advances without resend", func(t *testing.T) {
		id := enqueueNotification(t, env, notificationTestIntent("whatsapp-main", "base-3"))
		claimed := claimNotification(t, env, "worker-A")
		if result, err := store.FinishNotificationAccepted(ctx, id, claimed.LeaseOwner, claimed.LeaseGeneration, "wamid.base3"); err != nil || result != notifications.FinishApplied {
			t.Fatalf("accept: %v %v", result, err)
		}
		at := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
		if outcome := apply(id, "wamid.base3", "failed", at); !outcome.CurrentAdvanced {
			t.Fatal("first FAILED must advance")
		}
		var dispatch string
		if err := env.pool.QueryRow(ctx,
			`SELECT dispatch_status FROM notification_messages WHERE id = $1`,
			mustPgID(id)).Scan(&dispatch); err != nil || dispatch != "accepted" {
			t.Fatalf("dispatch stays accepted: %s %v", dispatch, err)
		}
	})
}

// TestNotificationPacedLifecycle proves held-response identity flows
// end to end: persisted ID correlates sent→delivered→read, failed
// correlates without resend, and accepted dispatch never re-sends.
func TestNotificationPacedLifecycle(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	provider := &scriptNotificationProvider{key: "whatsapp-main", send: func(context.Context, notifications.TemplateSendRequest) (notifications.SendResult, error) {
		// Adapter contract for held_for_quality_assessment: valid ID
		// returned, dispatch accepted, no resend.
		return notifications.SendResult{ProviderMessageID: "wamid.paced1"}, nil
	}}
	registry := notifications.NewRegistry()
	if err := registry.Register("whatsapp-main", provider); err != nil {
		t.Fatal(err)
	}
	id := enqueueNotification(t, env, notificationTestIntent("whatsapp-main", "paced-1"))
	worker := notifications.NewDispatcher(store, registry, notifications.SystemClock{}, "worker-A", nil)
	worker.DrainForTest(ctx)
	if provider.count() != 1 {
		t.Fatalf("one send: %d", provider.count())
	}
	var dispatch, wamid string
	if err := env.pool.QueryRow(ctx,
		`SELECT dispatch_status, provider_message_id FROM notification_messages WHERE id = $1`,
		mustPgID(id)).Scan(&dispatch, &wamid); err != nil || dispatch != "accepted" || wamid != "wamid.paced1" {
		t.Fatalf("paced accepted: %s %q %v", dispatch, wamid, err)
	}
	base := time.Now().UTC().Truncate(time.Second)
	apply := func(raw string, offset int64) {
		t.Helper()
		at := base.Add(time.Duration(offset) * time.Second)
		event := notifications.DeliveryEvent{
			ProviderMessageID: "wamid.paced1", RawStatus: raw,
			Canonical:         notifications.MapProviderStatus(raw),
			ProviderTimestamp: &at,
		}
		event.Fingerprint = notifications.DeliveryEventFingerprint(
			"whatsapp-main", event.ProviderMessageID, event.RawStatus,
			event.Canonical, event.ProviderTimestamp, "")
		if _, err := store.ApplyDeliveryStatus(ctx, id, event); err != nil {
			t.Fatalf("apply %s: %v", raw, err)
		}
	}
	apply("sent", 10)
	apply("delivered", 20)
	apply("read", 30)
	var delivery string
	if err := env.pool.QueryRow(ctx,
		`SELECT delivery_status FROM notification_messages WHERE id = $1`,
		mustPgID(id)).Scan(&delivery); err != nil || delivery != "READ" {
		t.Fatalf("paced READ: %s %v", delivery, err)
	}
	// Accepted dispatch is terminal for sending: later drains send nothing.
	worker.DrainForTest(ctx)
	if provider.count() != 1 {
		t.Fatalf("no second send: %d", provider.count())
	}
	// Correlation survives across store handles (restart equivalent).
	lookup, found, err := catalogStore(env).LookupByProviderMessage(ctx, "whatsapp-main", "wamid.paced1")
	if err != nil || !found || lookup != id {
		t.Fatalf("restart correlation: %v %v %v", lookup, found, err)
	}
}
