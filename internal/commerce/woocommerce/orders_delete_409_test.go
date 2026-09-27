package woocommerce

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
)

// recordingOrderStore is a minimal orders.OrderStore that records every
// projection attempt. It never supersedes: these tests prove error
// routing, not fencing (fenced in R1 suites).
type recordingOrderStore struct {
	mu          sync.Mutex
	projections []orders.OrderSnapshot
}

func (s *recordingOrderStore) BeginOrderReconcile(_ context.Context, _, _ string) (orders.ReconcileGeneration, error) {
	return 1, nil
}

func (s *recordingOrderStore) ReconcileProjectedOrder(_ context.Context, snapshot orders.OrderSnapshot, _ [32]byte, _ orders.ReconcileGeneration) (orders.ReconcileOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.projections = append(s.projections, snapshot)
	return orders.ReconcileOutcome{Revision: 1, Changed: true}, nil
}

func (s *recordingOrderStore) LoadProjectedOrder(_ context.Context, _, _ string) (orders.OrderSnapshot, int64, bool, error) {
	return orders.OrderSnapshot{}, 0, false, nil
}

func (s *recordingOrderStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.projections)
}

func deleteChainService(t *testing.T, harness *wooHarness, store *recordingOrderStore) *orders.OrderService {
	t.Helper()
	registry := commerce.NewRegistry()
	if err := registry.Register("website", testProvider(t, harness)); err != nil {
		t.Fatal(err)
	}
	return orders.NewOrderService(registry, store, nil)
}

// TestOrderDeleteBlockedOn409 is the H-6C-001 blocker: an authentic
// HTTP 409 behind order.deleted must block as Conflict — never
// tombstone, never mutate.
func TestOrderDeleteBlockedOn409(t *testing.T) {
	ctx := context.Background()
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	store := &recordingOrderStore{}
	service := deleteChainService(t, harness, store)
	harness.fail(http.MethodGet, "/wp-json/wc/v3/orders/600", http.StatusConflict,
		map[string]any{"code": "test_conflict", "message": "Clash."})
	_, err := service.ReconcileDeletion(ctx, "website", "600")
	var providerErr *commerce.ProviderError
	if !asCommerceProviderError(err, &providerErr) || providerErr.Kind != commerce.ErrorConflict {
		t.Fatalf("delete+409 must block as Conflict: %v", err)
	}
	if store.count() != 0 {
		t.Fatalf("delete+409 must not project: %d projections", store.count())
	}
}

// TestOrderDeleteTombstonesOn404 proves genuine deletion still works:
// HTTP 404 behind order.deleted tombstones with last-known data
// preserved by the service layer.
func TestOrderDeleteTombstonesOn404(t *testing.T) {
	ctx := context.Background()
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	store := &recordingOrderStore{}
	service := deleteChainService(t, harness, store)
	// Order 999 is absent from the harness: real HTTP 404.
	result, err := service.ReconcileDeletion(ctx, "website", "999")
	if err != nil {
		t.Fatalf("delete+404 must tombstone: %v", err)
	}
	if !result.ProviderDeleted || result.Canonical != orders.StatusDeleted {
		t.Fatalf("tombstone: %+v", result)
	}
	if store.count() != 1 || !store.projections[0].ProviderDeleted {
		t.Fatalf("one deleted projection: %+v", store.projections)
	}
}

// TestOrderDeleteKeepsLiveOn200 proves a stale delete behind a live
// provider order reconciles live with no tombstone.
func TestOrderDeleteKeepsLiveOn200(t *testing.T) {
	ctx := context.Background()
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	harness.preloadOrder(601, wooOrderFixture(601, "processing"))
	store := &recordingOrderStore{}
	service := deleteChainService(t, harness, store)
	result, err := service.ReconcileDeletion(ctx, "website", "601")
	if err != nil {
		t.Fatalf("delete+live must reconcile: %v", err)
	}
	if result.ProviderDeleted || result.Canonical != orders.StatusProcessing {
		t.Fatalf("live retained: %+v", result)
	}
}

// TestOrderReconcileBlockedOn409 proves created/updated behind a 409
// blocks as Conflict with zero projection mutation.
func TestOrderReconcileBlockedOn409(t *testing.T) {
	ctx := context.Background()
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	store := &recordingOrderStore{}
	service := deleteChainService(t, harness, store)
	harness.fail(http.MethodGet, "/wp-json/wc/v3/orders/602", http.StatusConflict,
		map[string]any{"code": "test_conflict", "message": "Clash."})
	_, err := service.ReconcileOrder(ctx, "website", "602")
	var providerErr *commerce.ProviderError
	if !asCommerceProviderError(err, &providerErr) || providerErr.Kind != commerce.ErrorConflict {
		t.Fatalf("reconcile+409 must block as Conflict: %v", err)
	}
	if store.count() != 0 {
		t.Fatalf("reconcile+409 must not project: %d projections", store.count())
	}
}
