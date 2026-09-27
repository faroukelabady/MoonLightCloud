package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
)

// scriptedOrderProvider serves scripted GET behavior for delete-race tests.
type scriptedOrderProvider struct {
	key commerce.ProviderKey
	mu  sync.Mutex
	get func(ctx context.Context, id string) (orders.OrderSnapshot, error)
}

func (s *scriptedOrderProvider) Key() commerce.ProviderKey { return s.key }

func (s *scriptedOrderProvider) UpsertProduct(_ context.Context, _ commerce.ProductUpsertRequest) (commerce.ProductUpsertResult, error) {
	return commerce.ProductUpsertResult{}, nil
}

func (s *scriptedOrderProvider) SetInventory(_ context.Context, _ commerce.InventoryUpdateRequest) error {
	return nil
}

func (s *scriptedOrderProvider) GetOrder(ctx context.Context, id string) (orders.OrderSnapshot, error) {
	s.mu.Lock()
	fn := s.get
	s.mu.Unlock()
	return fn(ctx, id)
}

func deleteTestService(env *saleEnv, provider *scriptedOrderProvider) *orders.OrderService {
	registry := commerce.NewRegistry()
	if err := registry.Register("website", provider); err != nil {
		panic(err)
	}
	return orders.NewOrderService(registry, catalogStore(env), nilLogger())
}

func notFoundOrder() error {
	return &orders.BlockedError{Code: orders.CodeOrderNotFound, Message: "gone"}
}

// TestDeleteDelayedAfterLive is the F-03 gate: an authentic but delayed
// delete arrives after live B committed. The processor GETs current
// state, finds B alive, and retains it with no DELETED transition.
func TestDeleteDelayedAfterLive(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	live := orderTestSnapshot("website", "950", orders.StatusProcessing, "processing", 12000)
	provider := &scriptedOrderProvider{key: "website"}
	provider.get = func(_ context.Context, _ string) (orders.OrderSnapshot, error) { return live, nil }
	service := deleteTestService(env, provider)

	first, err := service.ReconcileOrder(ctx, "website", "950")
	if err != nil || first.Revision != 1 {
		t.Fatalf("live commits: %+v %v", first, err)
	}
	// Old delete D processed last performs its provider check first.
	deleted, err := service.ReconcileDeletion(ctx, "website", "950")
	if err != nil {
		t.Fatalf("delete reconciles: %v", err)
	}
	stored, rev, _, err := catalogStore(env).LoadProjectedOrder(ctx, "website", "950")
	if err != nil {
		t.Fatal(err)
	}
	if stored.ProviderDeleted || stored.Canonical != orders.StatusProcessing || rev != 1 {
		t.Fatalf("live retained: %+v rev %d", stored, rev)
	}
	if deleted.ProviderDeleted || deleted.Canonical != orders.StatusProcessing {
		t.Fatalf("delete result live: %+v", deleted)
	}
	var history int
	if err := env.pool.QueryRow(ctx,
		`SELECT count(*) FROM commerce_online_order_status_history WHERE provider_key='website' AND external_order_id='950'`).Scan(&history); err != nil || history != 1 {
		t.Fatalf("no DELETED transition: %d (%v)", history, err)
	}
}

// TestDelete404Race proves a delete whose 404 arrives after a newer
// live reconcile cannot tombstone: its generation is stale.
func TestDelete404Race(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	live := orderTestSnapshot("website", "951", orders.StatusProcessing, "processing", 12000)
	release := make(chan struct{})
	returned := make(chan struct{})
	provider := &scriptedOrderProvider{key: "website"}
	var mu sync.Mutex
	calls := 0
	provider.get = func(_ context.Context, _ string) (orders.OrderSnapshot, error) {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		if call == 1 {
			<-release
			close(returned)
			return orders.OrderSnapshot{}, notFoundOrder()
		}
		return live, nil
	}
	service := deleteTestService(env, provider)

	deleteDone := make(chan orders.ReconcileResult, 1)
	deleteErr := make(chan error, 1)
	go func() {
		result, err := service.ReconcileDeletion(ctx, "website", "951")
		deleteDone <- result
		deleteErr <- err
	}()
	time.Sleep(200 * time.Millisecond)
	// Live reconcile starts later and commits while D is stuck in GET.
	liveResult, err := service.ReconcileOrder(ctx, "website", "951")
	if err != nil || liveResult.Revision != 1 {
		t.Fatalf("live commits: %+v %v", liveResult, err)
	}
	close(release)
	<-returned
	dResult := <-deleteDone
	if err := <-deleteErr; err != nil {
		t.Fatalf("delete resolves: %v", err)
	}
	if !dResult.Superseded || dResult.Changed {
		t.Fatalf("delete superseded: %+v", dResult)
	}
	stored, rev, _, err := store.LoadProjectedOrder(ctx, "website", "951")
	if err != nil || rev != 1 || stored.ProviderDeleted || stored.TotalMinor != 12000 {
		t.Fatalf("final live B: %+v %d %v", stored, rev, err)
	}
}

// TestDeleteProviderOutcomes proves Temporary/RateLimited retry without
// tombstoning while terminal provider errors block without tombstoning.
func TestDeleteProviderOutcomes(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	live := orderTestSnapshot("website", "952", orders.StatusProcessing, "processing", 12000)
	if _, err := store.BeginOrderReconcile(ctx, "website", "952"); err != nil {
		t.Fatal(err)
	}
	if outcome, err := store.ReconcileProjectedOrder(ctx, live, orders.Fingerprint(live), 1); err != nil || outcome.Revision != 1 {
		t.Fatalf("baseline: %+v %v", outcome, err)
	}
	cases := []struct {
		name      string
		err       error
		wantRetry bool
	}{
		{"temporary", commerce.TemporaryError("outage"), true},
		{"rate limited", commerce.RateLimitedError("slow", time.Minute), true},
		{"auth", commerce.AuthenticationError("bad"), false},
		{"validation", commerce.ValidationError("bad"), false},
		{"conflict", commerce.ConflictError("clash"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &scriptedOrderProvider{key: "website"}
			provider.get = func(_ context.Context, _ string) (orders.OrderSnapshot, error) {
				return orders.OrderSnapshot{}, tc.err
			}
			service := deleteTestService(env, provider)
			_, err := service.ReconcileDeletion(ctx, "website", "952")
			if err == nil {
				t.Fatal("must fail")
			}
			var providerErr *commerce.ProviderError
			isProvider := errors.As(err, &providerErr)
			if tc.wantRetry && (!isProvider || !providerErr.Retryable()) {
				t.Fatalf("retryable: %v", err)
			}
			if !tc.wantRetry && isProvider && providerErr.Retryable() {
				t.Fatalf("terminal: %v", err)
			}
			stored, rev, _, err := store.LoadProjectedOrder(ctx, "website", "952")
			if err != nil || rev != 1 || stored.ProviderDeleted {
				t.Fatalf("no tombstone: %+v %d %v", stored, rev, err)
			}
		})
	}
}

// TestManualDeleteOrdering proves both directions: a later-started
// manual live sync supersedes an in-flight delete, and a later-started
// delete confirming absence wins over an earlier manual attempt.
func TestManualDeleteOrdering(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	live := orderTestSnapshot("website", "953", orders.StatusProcessing, "processing", 12000)
	provider := &scriptedOrderProvider{key: "website"}
	provider.get = func(_ context.Context, _ string) (orders.OrderSnapshot, error) { return live, nil }
	service := deleteTestService(env, provider)

	// Direction 1: manual live sync first, then delete with provider
	// still live → delete resolves live, no tombstone.
	if _, err := service.ReconcileOrder(ctx, "website", "953"); err != nil {
		t.Fatal(err)
	}
	deleted, err := service.ReconcileDeletion(ctx, "website", "953")
	if err != nil {
		t.Fatal(err)
	}
	if deleted.ProviderDeleted {
		t.Fatalf("live delete resolves live: %+v", deleted)
	}
	// Direction 2: provider goes absent; delete wins and tombstones.
	provider.get = func(_ context.Context, _ string) (orders.OrderSnapshot, error) {
		return orders.OrderSnapshot{}, notFoundOrder()
	}
	deleted, err = service.ReconcileDeletion(ctx, "website", "953")
	if err != nil {
		t.Fatal(err)
	}
	if !deleted.ProviderDeleted || deleted.Revision != 2 {
		t.Fatalf("absent tombstones: %+v", deleted)
	}
	stored, _, _, err := catalogStore(env).LoadProjectedOrder(ctx, "website", "953")
	if err != nil || !stored.ProviderDeleted || stored.TotalMinor != 12000 {
		t.Fatalf("tombstone retains data: %+v %v", stored, err)
	}
}
