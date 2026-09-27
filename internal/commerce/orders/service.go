package orders

import (
	"context"
	"log/slog"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// OrderStore is the durable boundary for webhook inbox state and order
// projection. One ReconcileProjectedOrder call is one atomic
// compare-and-swap transaction.
type OrderStore interface {
	ReconcileProjectedOrder(ctx context.Context, snapshot OrderSnapshot, fingerprint [32]byte) (revision int64, changed bool, err error)
	LoadProjectedOrder(ctx context.Context, providerKey, externalOrderID string) (OrderSnapshot, int64, bool, error)
}

// ReconcileResult is the structured outcome of one order reconciliation.
type ReconcileResult struct {
	ProviderKey     string
	ExternalOrderID string
	Revision        int64
	Changed         bool
	Canonical       CanonicalStatus
	MappingComplete bool
	UnmappedLines   int
	ProviderDeleted bool
}

// OrderService reconciles provider orders into MoonLight projections.
// It performs no scheduling: the webhook processor and the sync-order
// CLI both call into it synchronously.
type OrderService struct {
	providers *commerce.Registry
	store     OrderStore
	log       *slog.Logger
}

// NewOrderService wires reconciliation over a provider registry and a
// durable order store.
func NewOrderService(providers *commerce.Registry, store OrderStore, log *slog.Logger) *OrderService {
	return &OrderService{providers: providers, store: store, log: log}
}

// ReconcileOrder fetches the provider's current order and converges the
// MoonLight projection to it. An already-current provider state is an
// idempotent no-op; a changed state advances the revision atomically.
// Errors are classified for the caller: BlockedError is terminal,
// Temporary/RateLimited provider failures are retryable, anything
// infrastructure-shaped should be retried by the caller.
func (s *OrderService) ReconcileOrder(ctx context.Context, providerKey, externalOrderID string) (ReconcileResult, error) {
	key, err := commerce.ValidateProviderKey(providerKey)
	if err != nil {
		return ReconcileResult{}, apperr.New(apperr.InvalidInput, err.Error())
	}
	canonicalID, err := CanonicalExternalOrderID(externalOrderID)
	if err != nil {
		return ReconcileResult{}, &BlockedError{Code: CodeOrderInvalid, Message: "invalid external order id"}
	}
	provider, err := s.providers.Get(key)
	if err != nil {
		return ReconcileResult{}, err
	}
	orderProvider, err := AsOrderProvider(provider)
	if err != nil {
		return ReconcileResult{}, err
	}
	snapshot, err := orderProvider.GetOrder(ctx, canonicalID)
	if err != nil {
		return ReconcileResult{}, err
	}
	if snapshot.ExternalOrderID != canonicalID || snapshot.ProviderKey != string(key) {
		return ReconcileResult{}, &BlockedError{Code: CodeOrderConflict, Message: "order identity mismatch"}
	}
	revision, changed, err := s.store.ReconcileProjectedOrder(ctx, snapshot, Fingerprint(snapshot))
	if err != nil {
		return ReconcileResult{}, err
	}
	s.logInfo("order reconciled", "provider", string(key), "order", canonicalID,
		"revision", revision, "changed", changed, "status", string(snapshot.Canonical))
	return ReconcileResult{
		ProviderKey: string(key), ExternalOrderID: canonicalID,
		Revision: revision, Changed: changed, Canonical: snapshot.Canonical,
		MappingComplete: snapshot.MappingComplete, UnmappedLines: snapshot.UnmappedLines,
	}, nil
}

// ReconcileDeletion marks the provider-deleted lifecycle state while
// preserving last-known order data. For never-observed orders it writes
// a minimal tombstone with no fabricated customer or financial data.
// Repeated deletes are idempotent: unchanged semantics mean no new
// revision and no duplicate history.
func (s *OrderService) ReconcileDeletion(ctx context.Context, providerKey, externalOrderID string) (ReconcileResult, error) {
	key, err := commerce.ValidateProviderKey(providerKey)
	if err != nil {
		return ReconcileResult{}, apperr.New(apperr.InvalidInput, err.Error())
	}
	canonicalID, err := CanonicalExternalOrderID(externalOrderID)
	if err != nil {
		return ReconcileResult{}, &BlockedError{Code: CodeOrderInvalid, Message: "invalid external order id"}
	}
	snapshot, revision, found, err := s.store.LoadProjectedOrder(ctx, string(key), canonicalID)
	if err != nil {
		return ReconcileResult{}, err
	}
	if !found {
		now := time.Now().UTC()
		snapshot = OrderSnapshot{
			ProviderKey: string(key), ExternalOrderID: canonicalID,
			Canonical: StatusDeleted, ProviderDeleted: true,
			CreatedAt: now, ModifiedAt: now,
			Billing: Address{Kind: "billing"}, Shipping: Address{Kind: "shipping"},
		}
		_ = revision
	} else {
		// Preserve the loaded ModifiedAt: deletion delivery time is
		// transport metadata, not order state, so repeat deletes stay
		// fingerprint-identical instead of churning revisions.
		snapshot.ProviderDeleted = true
		snapshot.Canonical = StatusDeleted
	}
	revision, changed, err := s.store.ReconcileProjectedOrder(ctx, snapshot, Fingerprint(snapshot))
	if err != nil {
		return ReconcileResult{}, err
	}
	s.logInfo("order deletion reconciled", "provider", string(key), "order", canonicalID,
		"revision", revision, "changed", changed)
	return ReconcileResult{
		ProviderKey: string(key), ExternalOrderID: canonicalID,
		Revision: revision, Changed: changed, Canonical: StatusDeleted,
		MappingComplete: snapshot.MappingComplete, UnmappedLines: snapshot.UnmappedLines,
		ProviderDeleted: true,
	}, nil
}

func (s *OrderService) logInfo(msg string, args ...any) {
	if s.log != nil {
		s.log.Info(msg, args...)
	}
}
