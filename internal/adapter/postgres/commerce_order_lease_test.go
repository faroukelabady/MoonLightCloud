package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
)

// claimForTest leases one event and returns its fencing token.
func claimForTest(t *testing.T, env *saleEnv, owner string) orders.ClaimedWebhookEvent {
	t.Helper()
	claim, ok, err := catalogStore(env).ClaimOrderWebhookEvent(
		context.Background(), owner, 5*time.Minute, time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	return claim
}

func finishForTest(t *testing.T, env *saleEnv, claim orders.ClaimedWebhookEvent, status string) (orders.FinishResult, string, int) {
	t.Helper()
	result, err := catalogStore(env).FinishOrderWebhookEvent(
		context.Background(), claim, status, nil, status == "processed", "")
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	var final, code string
	var attempts int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT status, attempt_count, COALESCE(last_error_code,'') FROM commerce_online_order_webhook_events WHERE provider_key=$1 AND delivery_id=$2`,
		claim.ProviderKey, claim.DeliveryID).Scan(&final, &attempts, &code); err != nil {
		t.Fatal(err)
	}
	return result, final, attempts
}

func insertLeaseWebhook(t *testing.T, env *saleEnv, delivery string) {
	t.Helper()
	insertTestWebhook(t, env, "website", delivery, "order.created", "930", []byte(`{"id":930}`))
}

// TestLeaseStaleFinishMatrix proves fenced completion: the current
// owner transitions freely while stale owners affect zero rows.
func TestLeaseStaleFinishMatrix(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	insertLeaseWebhook(t, env, "delivery-1")

	// A claims generation 1 and stalls; the lease expires.
	claimA := claimForTest(t, env, "worker-A")
	if claimA.LeaseGeneration != 1 || claimA.LeaseOwner != "worker-A" {
		t.Fatalf("gen1 token: %+v", claimA)
	}
	if _, err := env.pool.Exec(ctx,
		`UPDATE commerce_online_order_webhook_events SET lease_until = now() - interval '1 minute' WHERE provider_key='website' AND delivery_id='delivery-1'`); err != nil {
		t.Fatal(err)
	}
	// B claims generation 2 and finishes processed.
	claimB := claimForTest(t, env, "worker-B")
	if claimB.LeaseGeneration != 2 {
		t.Fatalf("gen2 token: %+v", claimB)
	}
	result, final, attempts := finishForTest(t, env, claimB, "processed")
	if result != orders.FinishApplied || final != "processed" || attempts != 2 {
		t.Fatalf("B finish: %v %s %d", result, final, attempts)
	}
	// A's stale retry is a no-op: state, attempts, and schedule untouched.
	result, final, attempts = finishForTest(t, env, claimA, "retry")
	if result != orders.FinishStale {
		t.Fatalf("stale result: %v", result)
	}
	if final != "processed" || attempts != 2 {
		t.Fatalf("terminal protected: %s %d", final, attempts)
	}
	var next *time.Time
	var code string
	if err := env.pool.QueryRow(ctx,
		`SELECT next_attempt_at, COALESCE(last_error_code,'') FROM commerce_online_order_webhook_events WHERE provider_key='website' AND delivery_id='delivery-1'`).Scan(&next, &code); err != nil {
		t.Fatal(err)
	}
	if next != nil || code != "" {
		t.Fatalf("stale owner changed nothing: %v %q", next, code)
	}
}

// TestLeaseBlockedProtected proves a stale owner cannot overwrite a
// blocked terminal state either.
func TestLeaseBlockedProtected(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	insertLeaseWebhook(t, env, "delivery-2")
	claimA := claimForTest(t, env, "worker-A")
	if _, err := env.pool.Exec(ctx,
		`UPDATE commerce_online_order_webhook_events SET lease_until = now() - interval '1 minute' WHERE provider_key='website' AND delivery_id='delivery-2'`); err != nil {
		t.Fatal(err)
	}
	claimB := claimForTest(t, env, "worker-B")
	store := catalogStore(env)
	result, err := store.FinishOrderWebhookEvent(ctx, claimB, "blocked", nil, false, "ORDER_INVALID")
	if err != nil || result != orders.FinishApplied {
		t.Fatalf("B blocked: %v %v", result, err)
	}
	result, final, _ := finishForTest(t, env, claimA, "processed")
	if result != orders.FinishStale || final != "blocked" {
		t.Fatalf("stale cannot overwrite blocked: %v %s", result, final)
	}
}

// TestLeaseCurrentOwnerRetry proves the valid current worker still
// transitions to retry with schedule and code intact.
func TestLeaseCurrentOwnerRetry(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	insertLeaseWebhook(t, env, "delivery-3")
	claim := claimForTest(t, env, "worker-A")
	store := catalogStore(env)
	next := time.Now().UTC().Add(time.Minute).Truncate(time.Second)
	result, err := store.FinishOrderWebhookEvent(ctx, claim, "retry", &next, false, "ORDER_PROVIDER_RETRY")
	if err != nil || result != orders.FinishApplied {
		t.Fatalf("current retry: %v %v", result, err)
	}
	var final, code string
	var stored *time.Time
	if err := env.pool.QueryRow(ctx,
		`SELECT status, next_attempt_at, COALESCE(last_error_code,'') FROM commerce_online_order_webhook_events WHERE provider_key='website' AND delivery_id='delivery-3'`).Scan(&final, &stored, &code); err != nil {
		t.Fatal(err)
	}
	if final != "retry" || code != "ORDER_PROVIDER_RETRY" || stored == nil {
		t.Fatalf("retry row: %s %v %q", final, stored, code)
	}
}

// TestLeaseSameProcessReclaim proves generations fence even one process:
// reclaiming after expiry yields a new token, and the old token is dead.
func TestLeaseSameProcessReclaim(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	insertLeaseWebhook(t, env, "delivery-4")
	first := claimForTest(t, env, "worker-A")
	if _, err := env.pool.Exec(ctx,
		`UPDATE commerce_online_order_webhook_events SET lease_until = now() - interval '1 minute' WHERE provider_key='website' AND delivery_id='delivery-4'`); err != nil {
		t.Fatal(err)
	}
	second := claimForTest(t, env, "worker-A")
	if second.LeaseGeneration != first.LeaseGeneration+1 {
		t.Fatalf("monotonic: %+v %+v", first, second)
	}
	result, final, _ := finishForTest(t, env, first, "retry")
	if result != orders.FinishStale || final == "retry" {
		t.Fatalf("old generation fenced: %v %s", result, final)
	}
}

// TestStaleWorkerCombined is the strongest F-01+F-02 closure: A stalls
// everywhere while B completes everything, then A resumes with both a
// stale order generation and a stale lease.
func TestStaleWorkerCombined(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	stateA := orderTestSnapshot("website", "940", orders.StatusPending, "pending", 10000)
	stateB := orderTestSnapshot("website", "940", orders.StatusProcessing, "processing", 12000)
	provider := &barrierOrderProvider{
		key: "website", stateA: stateA, stateB: stateB,
		releaseA: make(chan struct{}), aReturned: make(chan struct{}),
	}
	registry := commerce.NewRegistry()
	if err := registry.Register("website", provider); err != nil {
		t.Fatal(err)
	}
	service := orders.NewOrderService(registry, store, nilLogger())

	// A claims the delivery, begins the order generation, then stalls
	// inside provider GET.
	insertTestWebhook(t, env, "website", "delivery-A", "order.updated", "940", []byte(`{"id":940}`))
	genA, err := store.BeginOrderReconcile(ctx, "website", "940")
	if err != nil {
		t.Fatal(err)
	}
	claimA, ok, err := store.ClaimOrderWebhookEvent(ctx, "worker-A", 5*time.Minute, time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("A claim: %v %v", ok, err)
	}
	aDone := make(chan error, 1)
	go func() {
		snapshot, err := provider.GetOrder(ctx, "940")
		if err != nil {
			aDone <- err
			return
		}
		_, err = store.ReconcileProjectedOrder(ctx, snapshot, orders.Fingerprint(snapshot), genA)
		aDone <- err
	}()
	time.Sleep(200 * time.Millisecond)

	// A's lease expires. B claims generation 2, reconciles live B, and
	// finishes the delivery as processed.
	if _, err := env.pool.Exec(ctx,
		`UPDATE commerce_online_order_webhook_events SET lease_until = now() - interval '1 minute' WHERE provider_key='website' AND delivery_id='delivery-A'`); err != nil {
		t.Fatal(err)
	}
	bResult, err := service.ReconcileOrder(ctx, "website", "940")
	if err != nil || bResult.Superseded {
		t.Fatalf("B live: %+v %v", bResult, err)
	}
	claimB, ok, err := store.ClaimOrderWebhookEvent(ctx, "worker-B", 5*time.Minute, time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("B claim: %v %v", ok, err)
	}
	finished, err := store.FinishOrderWebhookEvent(ctx, claimB, "processed", nil, true, "")
	if err != nil || finished != orders.FinishApplied {
		t.Fatalf("B finish: %v %v", finished, err)
	}

	// A resumes: stale projection superseded, stale finish a no-op.
	close(provider.releaseA)
	<-provider.aReturned
	if err := <-aDone; err != nil {
		t.Fatalf("A projection must supersede cleanly: %v", err)
	}
	finished, err = store.FinishOrderWebhookEvent(ctx, claimA, "retry", nil, false, "ORDER_PROVIDER_RETRY")
	if err != nil {
		t.Fatalf("A finish must not error: %v", err)
	}
	if finished != orders.FinishStale {
		t.Fatalf("A finish stale: %v", finished)
	}

	// Final state: B's order, processed event, no resurrection.
	stored, rev, _, err := store.LoadProjectedOrder(ctx, "website", "940")
	if err != nil || rev != 1 || stored.TotalMinor != 12000 {
		t.Fatalf("final order B: %+v %d %v", stored, rev, err)
	}
	status, attempts, code := webhookStatus(t, env, "website", "delivery-A")
	if status != "processed" || attempts != 2 || code != "" {
		t.Fatalf("final event processed: %s %d %q", status, attempts, code)
	}
}
