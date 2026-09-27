package orders

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// stubOrderProvider serves canned snapshots without network.
type stubOrderProvider struct {
	key      commerce.ProviderKey
	snapshot OrderSnapshot
	err      error
	calls    int
}

func (s *stubOrderProvider) Key() commerce.ProviderKey { return s.key }

func (s *stubOrderProvider) UpsertProduct(_ context.Context, _ commerce.ProductUpsertRequest) (commerce.ProductUpsertResult, error) {
	return commerce.ProductUpsertResult{}, errors.New("not implemented")
}

func (s *stubOrderProvider) SetInventory(_ context.Context, _ commerce.InventoryUpdateRequest) error {
	return errors.New("not implemented")
}

func (s *stubOrderProvider) GetOrder(_ context.Context, _ string) (OrderSnapshot, error) {
	s.calls++
	if s.err != nil {
		return OrderSnapshot{}, s.err
	}
	return s.snapshot, nil
}

// stubOrderStore is an in-memory projection store with a trivial
// generation fence: the newest begun generation wins, mirroring the
// production contract for service-level tests.
type stubOrderStore struct {
	orders map[string]OrderSnapshot
	revs   map[string]int64
	gens   map[string]ReconcileGeneration
	begun  map[string]ReconcileGeneration
}

func stubKey(provider, order string) string { return provider + "/" + order }

func (s *stubOrderStore) BeginOrderReconcile(_ context.Context, providerKey, externalOrderID string) (ReconcileGeneration, error) {
	if s.begun == nil {
		s.begun = map[string]ReconcileGeneration{}
	}
	key := stubKey(providerKey, externalOrderID)
	s.begun[key]++
	return s.begun[key], nil
}

func (s *stubOrderStore) ReconcileProjectedOrder(_ context.Context, snapshot OrderSnapshot, fingerprint [32]byte, generation ReconcileGeneration) (ReconcileOutcome, error) {
	key := stubKey(snapshot.ProviderKey, snapshot.ExternalOrderID)
	if s.gens == nil {
		s.gens = map[string]ReconcileGeneration{}
	}
	if generation < s.gens[key] {
		return ReconcileOutcome{Superseded: true}, nil
	}
	s.gens[key] = generation
	existing, ok := s.orders[key]
	if !ok {
		if s.orders == nil {
			s.orders = map[string]OrderSnapshot{}
			s.revs = map[string]int64{}
		}
		s.orders[key] = snapshot
		s.revs[key] = 1
		return ReconcileOutcome{Revision: 1, Changed: true}, nil
	}
	if Fingerprint(existing) == fingerprint {
		return ReconcileOutcome{Revision: s.revs[key]}, nil
	}
	s.orders[key] = snapshot
	s.revs[key]++
	return ReconcileOutcome{Revision: s.revs[key], Changed: true}, nil
}

func (s *stubOrderStore) LoadProjectedOrder(_ context.Context, providerKey, externalOrderID string) (OrderSnapshot, int64, bool, error) {
	key := stubKey(providerKey, externalOrderID)
	snapshot, ok := s.orders[key]
	if !ok {
		return OrderSnapshot{}, 0, false, nil
	}
	return snapshot, s.revs[key], true, nil
}

func orderTestService(snapshot OrderSnapshot, err error) (*OrderService, *stubOrderProvider, *stubOrderStore) {
	provider := &stubOrderProvider{key: "website", snapshot: snapshot, err: err}
	registry := commerce.NewRegistry()
	if rerr := registry.Register("website", provider); rerr != nil {
		panic(rerr)
	}
	store := &stubOrderStore{}
	return NewOrderService(registry, store, nil), provider, store
}

func TestReconcileOrderProjects(t *testing.T) {
	ctx := context.Background()
	snapshot := baseSnapshot()
	snapshot.ProviderKey = "website"
	service, provider, _ := orderTestService(snapshot, nil)
	result, err := service.ReconcileOrder(ctx, "website", "100")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.Revision != 1 || !result.Changed || result.Canonical != StatusProcessing {
		t.Fatalf("result: %+v", result)
	}
	if provider.calls != 1 {
		t.Fatal("one provider read")
	}
	// Same state again is an idempotent no-op.
	second, err := service.ReconcileOrder(ctx, "website", "100")
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != 1 || second.Changed {
		t.Fatalf("no-op: %+v", second)
	}
}

func TestReconcileOrderUnknownProvider(t *testing.T) {
	service, _, _ := orderTestService(baseSnapshot(), nil)
	if _, err := service.ReconcileOrder(context.Background(), "missing", "100"); !commerce.UnknownProvider(err) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestReconcileOrderNoCapability(t *testing.T) {
	registry := commerce.NewRegistry()
	plain := &stubPlainProvider{key: "website"}
	if err := registry.Register("website", plain); err != nil {
		t.Fatal(err)
	}
	service := NewOrderService(registry, &stubOrderStore{}, nil)
	if _, err := service.ReconcileOrder(context.Background(), "website", "100"); err == nil {
		t.Fatal("capability required")
	} else {
		var appErr *apperr.Error
		if !errors.As(err, &appErr) {
			t.Fatalf("typed: %v", err)
		}
	}
}

type stubPlainProvider struct{ key commerce.ProviderKey }

func (s *stubPlainProvider) Key() commerce.ProviderKey { return s.key }
func (s *stubPlainProvider) UpsertProduct(_ context.Context, _ commerce.ProductUpsertRequest) (commerce.ProductUpsertResult, error) {
	return commerce.ProductUpsertResult{}, nil
}
func (s *stubPlainProvider) SetInventory(_ context.Context, _ commerce.InventoryUpdateRequest) error {
	return nil
}

func TestReconcileOrderTemporary(t *testing.T) {
	service, _, _ := orderTestService(baseSnapshot(), commerce.TemporaryError("timeout"))
	_, err := service.ReconcileOrder(context.Background(), "website", "100")
	var providerErr *commerce.ProviderError
	if !errors.As(err, &providerErr) || !providerErr.Retryable() {
		t.Fatalf("retryable: %v", err)
	}
}

func TestReconcileDeletionTombstone(t *testing.T) {
	ctx := context.Background()
	notFound := &BlockedError{Code: CodeOrderNotFound, Message: "gone"}
	provider := &stubOrderProvider{key: "website", err: notFound}
	registry := commerce.NewRegistry()
	if err := registry.Register("website", provider); err != nil {
		t.Fatal(err)
	}
	service := NewOrderService(registry, &stubOrderStore{}, nil)
	// Unseen order: minimal tombstone at rev 1.
	result, err := service.ReconcileDeletion(ctx, "website", "100")
	if err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	if result.Revision != 1 || !result.Changed || !result.ProviderDeleted || result.Canonical != StatusDeleted {
		t.Fatalf("tombstone: %+v", result)
	}
	// Repeat delete: no churn.
	again, err := service.ReconcileDeletion(ctx, "website", "100")
	if err != nil {
		t.Fatal(err)
	}
	if again.Revision != 1 || again.Changed {
		t.Fatalf("idempotent delete: %+v", again)
	}
}

func TestReconcileDeletionPreservesData(t *testing.T) {
	ctx := context.Background()
	snapshot := baseSnapshot()
	snapshot.ProviderKey = "website"
	provider := &stubOrderProvider{key: "website", snapshot: snapshot}
	registry := commerce.NewRegistry()
	if err := registry.Register("website", provider); err != nil {
		t.Fatal(err)
	}
	store := &stubOrderStore{}
	service := NewOrderService(registry, store, nil)
	if _, err := service.ReconcileOrder(ctx, "website", "100"); err != nil {
		t.Fatal(err)
	}
	// Provider now confirms absence: tombstone preserves last-known data.
	provider.snapshot = OrderSnapshot{}
	provider.err = &BlockedError{Code: CodeOrderNotFound, Message: "gone"}
	result, err := service.ReconcileDeletion(ctx, "website", "100")
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != 2 || !result.ProviderDeleted {
		t.Fatalf("delete advances: %+v", result)
	}
	stored, _, found, err := store.LoadProjectedOrder(ctx, "website", "100")
	if err != nil || !found {
		t.Fatal("stored")
	}
	if stored.TotalMinor != snapshot.TotalMinor || len(stored.Lines) != 1 || !stored.ProviderDeleted {
		t.Fatalf("last-known preserved: %+v", stored)
	}
}

// TestReconcileLogsContainNoPII forces provider and reconciliation
// errors with distinctive synthetic PII in the snapshot and asserts
// captured structured logs carry none of it.
func TestReconcileLogsContainNoPII(t *testing.T) {
	var logs strings.Builder
	handler := slog.NewTextHandler(&logs, nil)
	logger := slog.New(handler)
	snapshot := baseSnapshot()
	snapshot.ProviderKey = "website"
	snapshot.Customer = Customer{FirstName: "PII-Firstname", LastName: "PII-Lastname", Email: "pii-log-probe@example.com", Phone: "+202222222222"}
	snapshot.Billing = Address{Kind: "billing", Address1: "9 PII Log Street"}
	provider := &stubOrderProvider{key: "website", err: commerce.TemporaryError("outage for pii-log-probe@example.com")}
	registry := commerce.NewRegistry()
	if err := registry.Register("website", provider); err != nil {
		t.Fatal(err)
	}
	service := NewOrderService(registry, &stubOrderStore{}, logger)
	if _, err := service.ReconcileOrder(context.Background(), "website", "100"); err == nil {
		t.Fatal("expected failure")
	}
	for _, forbidden := range []string{
		"PII-Firstname", "PII-Lastname", "pii-log-probe@example.com", "+202222222222", "9 PII Log Street",
	} {
		if strings.Contains(logs.String(), forbidden) {
			t.Fatalf("PII in logs %q: %s", forbidden, logs.String())
		}
	}
}
