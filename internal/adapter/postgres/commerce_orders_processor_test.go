package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
)

// stubCommerceOrderProvider serves scripted order snapshots without network.
type stubCommerceOrderProvider struct {
	key       commerce.ProviderKey
	snapshots map[string]orders.OrderSnapshot
	errs      map[string]error
	calls     int
}

func (s *stubCommerceOrderProvider) Key() commerce.ProviderKey { return s.key }

func (s *stubCommerceOrderProvider) UpsertProduct(_ context.Context, _ commerce.ProductUpsertRequest) (commerce.ProductUpsertResult, error) {
	return commerce.ProductUpsertResult{}, errors.New("not implemented")
}

func (s *stubCommerceOrderProvider) SetInventory(_ context.Context, _ commerce.InventoryUpdateRequest) error {
	return errors.New("not implemented")
}

func (s *stubCommerceOrderProvider) GetOrder(_ context.Context, externalOrderID string) (orders.OrderSnapshot, error) {
	s.calls++
	if err, ok := s.errs[externalOrderID]; ok {
		return orders.OrderSnapshot{}, err
	}
	snapshot, ok := s.snapshots[externalOrderID]
	if !ok {
		return orders.OrderSnapshot{}, &orders.BlockedError{Code: orders.CodeOrderNotFound, Message: "no such order"}
	}
	return snapshot, nil
}

// orderProcessorEnv wires a real store + real service + stub provider.
func orderProcessorEnv(env *saleEnv, provider *stubCommerceOrderProvider) *orders.Processor {
	registry := commerce.NewRegistry()
	if err := registry.Register(provider.key, provider); err != nil {
		panic(err)
	}
	store := catalogStore(env)
	service := orders.NewOrderService(registry, store, nilLogger())
	return orders.NewProcessor(store, service, orders.SystemClock{}, "test-worker", nilLogger())
}

func runProcessorBriefly(t *testing.T, processor *orders.Processor) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); processor.Run(ctx) }()
	// Startup drain runs immediately; give retries/backoff-free passes
	// time to settle, then stop.
	time.Sleep(1500 * time.Millisecond)
	cancel()
	<-done
}

// insertTestWebhook persists one delivery directly for processor tests.
func insertTestWebhook(t *testing.T, env *saleEnv, provider, delivery, topic, external string, body []byte) {
	t.Helper()
	sum := sha256.Sum256(body)
	outcome, err := catalogStore(env).InsertOrderWebhookEvent(context.Background(),
		commerce.ProviderKey(provider), delivery, orders.WebhookTopic(topic), external, sum[:], nil)
	if err != nil || outcome != orders.WebhookInserted {
		t.Fatalf("insert webhook: %v %v", outcome, err)
	}
}

func webhookStatus(t *testing.T, env *saleEnv, provider, delivery string) (string, int, string) {
	t.Helper()
	var status, code string
	var attempts int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT status, attempt_count, COALESCE(last_error_code,'') FROM commerce_online_order_webhook_events WHERE provider_key=$1 AND delivery_id=$2`,
		provider, delivery).Scan(&status, &attempts, &code); err != nil {
		t.Fatal(err)
	}
	return status, attempts, code
}

// TestOrderWebhookDedupe proves identical redelivery is idempotent and
// conflicting bodies are rejected without overwriting.
func TestOrderWebhookDedupe(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	body := []byte(`{"id":300,"status":"pending"}`)
	sum := sha256.Sum256(body)
	outcome, err := store.InsertOrderWebhookEvent(ctx, "website", "delivery-1", orders.TopicOrderCreated, "300", sum[:], nil)
	if err != nil || outcome != orders.WebhookInserted {
		t.Fatalf("first: %v %v", outcome, err)
	}
	outcome, err = store.InsertOrderWebhookEvent(ctx, "website", "delivery-1", orders.TopicOrderCreated, "300", sum[:], nil)
	if err != nil || outcome != orders.WebhookDuplicateIdentical {
		t.Fatalf("duplicate: %v %v", outcome, err)
	}
	other := sha256.Sum256([]byte(`{"id":300,"status":"changed"}`))
	_, err = store.InsertOrderWebhookEvent(ctx, "website", "delivery-1", orders.TopicOrderCreated, "300", other[:], nil)
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Kind != apperr.Conflict {
		t.Fatalf("conflict: %v", err)
	}
}

// TestOrderProcessorOutOfOrder proves B-then-A webhooks converge on B:
// both triggers fetch current provider state, so late arrivals cannot
// regress the projection.
func TestOrderProcessorOutOfOrder(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	oldSnapshot := orderTestSnapshot("website", "301", orders.StatusPending, "pending", 10000)
	newSnapshot := orderTestSnapshot("website", "301", orders.StatusProcessing, "processing", 12000)
	newSnapshot.Lines = append(newSnapshot.Lines, orders.OrderLine{
		ExternalLineID: 2, ExternalProductID: "501", SKU: "PAP-2", Name: "Second", Quantity: 1, TotalMinor: 2000,
	})
	provider := &stubCommerceOrderProvider{key: "website", snapshots: map[string]orders.OrderSnapshot{"301": newSnapshot}}
	processor := orderProcessorEnv(env, provider)
	// B arrives first and projects the new state.
	insertTestWebhook(t, env, "website", "delivery-B", "order.updated", "301", []byte(`{"id":301,"v":"B"}`))
	runProcessorBriefly(t, processor)
	// A arrives later carrying an older body, but reconciliation reads
	// current provider state (B), so the projection stays at B.
	provider.snapshots["301"] = newSnapshot
	insertTestWebhook(t, env, "website", "delivery-A", "order.updated", "301", []byte(`{"id":301,"v":"A"}`))
	runProcessorBriefly(t, processor)
	stored, rev, _, err := catalogStore(env).LoadProjectedOrder(ctx, "website", "301")
	if err != nil {
		t.Fatal(err)
	}
	if stored.TotalMinor != 12000 || len(stored.Lines) != 2 || rev != 1 {
		t.Fatalf("newest wins without churn: %+v rev %d", stored, rev)
	}
	for _, delivery := range []string{"delivery-A", "delivery-B"} {
		if status, _, _ := webhookStatus(t, env, "website", delivery); status != "processed" {
			t.Fatalf("%s processed: %s", delivery, status)
		}
	}
	_ = oldSnapshot
}

// TestOrderProcessorTemporaryRetry proves provider outage retries with
// backoff and later converges without loss.
func TestOrderProcessorTemporaryRetry(t *testing.T) {
	env := openSaleEnv(t)
	provider := &stubCommerceOrderProvider{
		key:  "website",
		errs: map[string]error{"302": commerce.TemporaryError("outage")},
	}
	processor := orderProcessorEnv(env, provider)
	insertTestWebhook(t, env, "website", "delivery-1", "order.created", "302", []byte(`{"id":302}`))
	runProcessorBriefly(t, processor)
	status, attempts, code := webhookStatus(t, env, "website", "delivery-1")
	if status != "retry" || attempts < 1 || code != "ORDER_PROVIDER_RETRY" {
		t.Fatalf("retry: %s %d %q", status, attempts, code)
	}
	// Provider recovers: next pass projects and processes.
	snapshot := orderTestSnapshot("website", "302", orders.StatusPending, "pending", 5000)
	provider.errs = map[string]error{}
	provider.snapshots = map[string]orders.OrderSnapshot{"302": snapshot}
	forceWebhookDue(t, env, "website", "delivery-1")
	runProcessorBriefly(t, processor)
	if status, _, _ := webhookStatus(t, env, "website", "delivery-1"); status != "processed" {
		t.Fatalf("recovered: %s", status)
	}
	stored, rev, _, err := catalogStore(env).LoadProjectedOrder(context.Background(), "website", "302")
	if err != nil || rev != 1 || stored.TotalMinor != 5000 {
		t.Fatalf("projected: %+v %d %v", stored, rev, err)
	}
}

func forceWebhookDue(t *testing.T, env *saleEnv, provider, delivery string) {
	t.Helper()
	if _, err := env.pool.Exec(context.Background(),
		`UPDATE commerce_online_order_webhook_events SET next_attempt_at = NULL WHERE provider_key=$1 AND delivery_id=$2`,
		provider, delivery); err != nil {
		t.Fatal(err)
	}
}

// TestOrderProcessorBlockedPermanent proves terminal failures block with
// a machine-readable code and no secrets.
func TestOrderProcessorBlockedPermanent(t *testing.T) {
	env := openSaleEnv(t)
	provider := &stubCommerceOrderProvider{
		key:  "website",
		errs: map[string]error{"303": commerce.AuthenticationError("bad creds")},
	}
	processor := orderProcessorEnv(env, provider)
	insertTestWebhook(t, env, "website", "delivery-1", "order.created", "303", []byte(`{"id":303}`))
	runProcessorBriefly(t, processor)
	status, _, code := webhookStatus(t, env, "website", "delivery-1")
	if status != "blocked" || code == "" {
		t.Fatalf("blocked with code: %s %q", status, code)
	}
}

// TestOrderProcessorDeleteFlow proves delete tombstones preserve
// last-known data with one history transition.
func TestOrderProcessorDeleteFlow(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	snapshot := orderTestSnapshot("website", "304", orders.StatusProcessing, "processing", 9000)
	provider := &stubCommerceOrderProvider{key: "website", snapshots: map[string]orders.OrderSnapshot{"304": snapshot}}
	processor := orderProcessorEnv(env, provider)
	insertTestWebhook(t, env, "website", "delivery-1", "order.created", "304", []byte(`{"id":304}`))
	runProcessorBriefly(t, processor)
	// Provider confirms absence for the delete attempt: genuine deletion.
	provider.snapshots = map[string]orders.OrderSnapshot{}
	provider.errs = map[string]error{"304": &orders.BlockedError{Code: orders.CodeOrderNotFound, Message: "gone"}}
	insertTestWebhook(t, env, "website", "delivery-2", "order.deleted", "304", []byte(`{"id":304}`))
	runProcessorBriefly(t, processor)
	stored, rev, _, err := catalogStore(env).LoadProjectedOrder(ctx, "website", "304")
	if err != nil {
		t.Fatal(err)
	}
	if !stored.ProviderDeleted || stored.Canonical != orders.StatusDeleted || rev != 2 {
		t.Fatalf("tombstone: %+v rev %d", stored, rev)
	}
	if stored.TotalMinor != 9000 || len(stored.Lines) != 1 {
		t.Fatalf("last-known preserved: %+v", stored)
	}
	var history int
	if err := env.pool.QueryRow(ctx,
		`SELECT count(*) FROM commerce_online_order_status_history WHERE provider_key='website' AND external_order_id='304'`).Scan(&history); err != nil || history != 2 {
		t.Fatalf("two transitions: %d (%v)", history, err)
	}
}

// TestOrderProcessorDeleteBeforeCreate proves an unseen delete writes a
// minimal tombstone without fabricated data.
func TestOrderProcessorDeleteBeforeCreate(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	processor := orderProcessorEnv(env, &stubCommerceOrderProvider{key: "website"})
	insertTestWebhook(t, env, "website", "delivery-1", "order.deleted", "305", []byte(`{"id":305}`))
	runProcessorBriefly(t, processor)
	stored, rev, found, err := catalogStore(env).LoadProjectedOrder(ctx, "website", "305")
	if err != nil || !found {
		t.Fatalf("tombstone: %v %v", found, err)
	}
	if !stored.ProviderDeleted || rev != 1 || stored.TotalMinor != 0 || len(stored.Lines) != 0 {
		t.Fatalf("minimal tombstone: %+v rev %d", stored, rev)
	}
	if status, _, _ := webhookStatus(t, env, "website", "delivery-1"); status != "processed" {
		t.Fatalf("processed: %s", status)
	}
}

// TestOrderProcessorCrashReplay proves a committed projection with an
// unmarked event replays idempotently: no duplicate revision or history.
func TestOrderProcessorCrashReplay(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	snapshot := orderTestSnapshot("website", "306", orders.StatusPending, "pending", 4000)
	processor := orderProcessorEnv(env, &stubCommerceOrderProvider{
		key: "website", snapshots: map[string]orders.OrderSnapshot{"306": snapshot},
	})
	insertTestWebhook(t, env, "website", "delivery-1", "order.created", "306", []byte(`{"id":306}`))
	runProcessorBriefly(t, processor)
	// Crash simulation: projection committed, finish lost. Rewind the
	// event to pending as if the status update never committed.
	if _, err := env.pool.Exec(ctx,
		`UPDATE commerce_online_order_webhook_events SET status='pending', lease_owner=NULL, lease_until=NULL WHERE provider_key='website' AND delivery_id='delivery-1'`); err != nil {
		t.Fatal(err)
	}
	runProcessorBriefly(t, processor)
	stored, rev, _, err := catalogStore(env).LoadProjectedOrder(ctx, "website", "306")
	if err != nil || rev != 1 || stored.TotalMinor != 4000 {
		t.Fatalf("idempotent replay: %+v %d %v", stored, rev, err)
	}
	var history int
	if err := env.pool.QueryRow(ctx,
		`SELECT count(*) FROM commerce_online_order_status_history WHERE provider_key='website' AND external_order_id='306'`).Scan(&history); err != nil || history != 1 {
		t.Fatalf("one history row: %d (%v)", history, err)
	}
	if status, _, _ := webhookStatus(t, env, "website", "delivery-1"); status != "processed" {
		t.Fatalf("eventually processed: %s", status)
	}
}

// TestOrderProcessorLeaseRecovery proves an abandoned claim becomes
// eligible after lease expiry with no manual update.
func TestOrderProcessorLeaseRecovery(t *testing.T) {
	env := openSaleEnv(t)
	snapshot := orderTestSnapshot("website", "307", orders.StatusPending, "pending", 4000)
	processor := orderProcessorEnv(env, &stubCommerceOrderProvider{
		key: "website", snapshots: map[string]orders.OrderSnapshot{"307": snapshot},
	})
	insertTestWebhook(t, env, "website", "delivery-1", "order.created", "307", []byte(`{"id":307}`))
	// Worker A claims then disappears: simulate by setting a short past
	// lease directly after a manual claim window.
	store := catalogStore(env)
	_, claimed, err := store.ClaimOrderWebhookEvent(context.Background(), "worker-A", time.Hour, time.Now().UTC())
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	// Expire the lease as if worker A crashed long ago.
	if _, err := env.pool.Exec(context.Background(),
		`UPDATE commerce_online_order_webhook_events SET lease_until = now() - interval '1 minute' WHERE provider_key='website' AND delivery_id='delivery-1'`); err != nil {
		t.Fatal(err)
	}
	runProcessorBriefly(t, processor)
	if status, _, _ := webhookStatus(t, env, "website", "delivery-1"); status != "processed" {
		t.Fatalf("lease recovery: %s", status)
	}
}

// TestOrderProcessorConcurrentWorkers races two workers over one event:
// exactly one semantic result, one revision, one history transition.
func TestOrderProcessorConcurrentWorkers(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	snapshot := orderTestSnapshot("website", "308", orders.StatusPending, "pending", 4000)
	provider := &stubCommerceOrderProvider{key: "website", snapshots: map[string]orders.OrderSnapshot{"308": snapshot}}
	registry := commerce.NewRegistry()
	if err := registry.Register("website", provider); err != nil {
		t.Fatal(err)
	}
	store := catalogStore(env)
	newWorker := func(owner string) *orders.Processor {
		return orders.NewProcessor(store,
			orders.NewOrderService(registry, store, nilLogger()),
			orders.SystemClock{}, owner, nilLogger())
	}
	insertTestWebhook(t, env, "website", "delivery-1", "order.created", "308", []byte(`{"id":308}`))
	start := make(chan struct{})
	done := make(chan struct{}, 2)
	for _, owner := range []string{"worker-A", "worker-B"} {
		go func(owner string) {
			defer func() { done <- struct{}{} }()
			<-start
			workerCtx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			newWorker(owner).Run(workerCtx)
		}(owner)
	}
	close(start)
	<-done
	<-done
	stored, rev, _, err := store.LoadProjectedOrder(ctx, "website", "308")
	if err != nil || rev != 1 || stored.TotalMinor != 4000 {
		t.Fatalf("one semantic result: %+v %d %v", stored, rev, err)
	}
	var history int
	if err := env.pool.QueryRow(ctx,
		`SELECT count(*) FROM commerce_online_order_status_history WHERE provider_key='website' AND external_order_id='308'`).Scan(&history); err != nil || history != 1 {
		t.Fatalf("one transition: %d (%v)", history, err)
	}
	if status, _, _ := webhookStatus(t, env, "website", "delivery-1"); status != "processed" {
		t.Fatalf("processed: %s", status)
	}
}

// TestOrderProcessorRateLimitedRetry proves 429 with Retry-After
// schedules the retry at the provider hint.
func TestOrderProcessorRateLimitedRetry(t *testing.T) {
	env := openSaleEnv(t)
	rateErr := commerce.RateLimitedError("slow", 90*time.Second)
	provider := &stubCommerceOrderProvider{key: "website", errs: map[string]error{"309": rateErr}}
	processor := orderProcessorEnv(env, provider)
	insertTestWebhook(t, env, "website", "delivery-1", "order.created", "309", []byte(`{"id":309}`))
	before := time.Now().UTC()
	runProcessorBriefly(t, processor)
	status, _, code := webhookStatus(t, env, "website", "delivery-1")
	if status != "retry" || code != "ORDER_PROVIDER_RETRY" {
		t.Fatalf("retry: %s %q", status, code)
	}
	var next time.Time
	if err := env.pool.QueryRow(context.Background(),
		`SELECT next_attempt_at FROM commerce_online_order_webhook_events WHERE provider_key='website' AND delivery_id='delivery-1'`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if delay := next.Sub(before); delay < 80*time.Second || delay > 100*time.Second {
		t.Fatalf("hint respected: %v", delay)
	}
}

// TestWebhookDeliveryIdentityConflict proves same delivery ID with the
// same payload hash but contradictory topic or order identity is a
// conflict that leaves the original row unchanged, while the same
// delivery ID under another provider stays independent.
func TestWebhookDeliveryIdentityConflict(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	body := []byte(`{"id":960}`)
	sum := sha256.Sum256(body)
	outcome, err := store.InsertOrderWebhookEvent(ctx, "website", "delivery-9",
		orders.TopicOrderCreated, "960", sum[:], nil)
	if err != nil || outcome != orders.WebhookInserted {
		t.Fatalf("first: %v %v", outcome, err)
	}
	// Same hash, different topic: conflict.
	_, err = store.InsertOrderWebhookEvent(ctx, "website", "delivery-9",
		orders.TopicOrderDeleted, "960", sum[:], nil)
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Kind != apperr.Conflict {
		t.Fatalf("topic conflict: %v", err)
	}
	// Same hash and topic, different order identity: conflict.
	_, err = store.InsertOrderWebhookEvent(ctx, "website", "delivery-9",
		orders.TopicOrderCreated, "961", sum[:], nil)
	if !errors.As(err, &appErr) || appErr.Kind != apperr.Conflict {
		t.Fatalf("order conflict: %v", err)
	}
	// Original row unchanged.
	var topic, external string
	var hash []byte
	if err := env.pool.QueryRow(ctx,
		`SELECT topic, external_order_id, payload_hash FROM commerce_online_order_webhook_events WHERE provider_key='website' AND delivery_id='delivery-9'`).Scan(&topic, &external, &hash); err != nil {
		t.Fatal(err)
	}
	if topic != "order.created" || external != "960" || string(hash) != string(sum[:]) {
		t.Fatalf("original intact: %s %s", topic, external)
	}
	// Same delivery ID under another provider is independent.
	outcome, err = store.InsertOrderWebhookEvent(ctx, "shop2", "delivery-9",
		orders.TopicOrderCreated, "960", sum[:], nil)
	if err != nil || outcome != orders.WebhookInserted {
		t.Fatalf("cross-provider: %v %v", outcome, err)
	}
}
