package orders

import (
	"context"
	"log/slog"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// ReconcileGeneration is a per-order concurrency fence token: a
// later-started reconciliation supersedes earlier-started ones for the
// same provider order. It is operational metadata, never the semantic
// order revision, and never exposed publicly.
type ReconcileGeneration int64

// LeaseGeneration is a per-delivery claim token: each successful webhook
// claim increments it, so a stale worker can never finalize an event
// owned by a newer lease.
type LeaseGeneration int64

// ReconcileOutcome is the result of one generation-checked projection.
type ReconcileOutcome struct {
	Revision   int64
	Changed    bool
	Superseded bool
}

// OrderStore is the durable boundary for webhook inbox state and order
// projection. One ReconcileProjectedOrder call is one atomic
// fence-check-and-swap transaction.
type OrderStore interface {
	BeginOrderReconcile(ctx context.Context, providerKey, externalOrderID string) (ReconcileGeneration, error)
	ReconcileProjectedOrder(ctx context.Context, snapshot OrderSnapshot, fingerprint [32]byte, generation ReconcileGeneration) (ReconcileOutcome, error)
	LoadProjectedOrder(ctx context.Context, providerKey, externalOrderID string) (OrderSnapshot, int64, bool, error)
}

// ReconcileResult is the structured outcome of one order reconciliation.
type ReconcileResult struct {
	ProviderKey     string
	ExternalOrderID string
	Revision        int64
	Changed         bool
	Superseded      bool
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
// MoonLight projection to it under a fresh reconciliation generation.
// A generation superseded by a later-started reconciliation returns
// Superseded=true without mutating state. Errors are classified for
// the caller: BlockedError is terminal, Temporary/RateLimited provider
// failures are retryable, infrastructure errors should be retried.
func (s *OrderService) ReconcileOrder(ctx context.Context, providerKey, externalOrderID string) (ReconcileResult, error) {
	key, canonicalID, orderProvider, err := s.resolveOrderProvider(providerKey, externalOrderID)
	if err != nil {
		return ReconcileResult{}, err
	}
	generation, err := s.store.BeginOrderReconcile(ctx, string(key), canonicalID)
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
	return s.projectCurrent(ctx, key, canonicalID, snapshot, generation)
}

// ReconcileDeletion converges one order to provider-confirmed deletion:
// obtain a generation, check CURRENT provider state, and tombstone only
// on confirmed absence. A live provider order wins over the delete
// event; transient provider failures retry without tombstoning.
func (s *OrderService) ReconcileDeletion(ctx context.Context, providerKey, externalOrderID string) (ReconcileResult, error) {
	key, canonicalID, orderProvider, err := s.resolveOrderProvider(providerKey, externalOrderID)
	if err != nil {
		return ReconcileResult{}, err
	}
	generation, err := s.store.BeginOrderReconcile(ctx, string(key), canonicalID)
	if err != nil {
		return ReconcileResult{}, err
	}
	snapshot, err := orderProvider.GetOrder(ctx, canonicalID)
	if err == nil {
		if snapshot.ExternalOrderID != canonicalID || snapshot.ProviderKey != string(key) {
			return ReconcileResult{}, &BlockedError{Code: CodeOrderConflict, Message: "order identity mismatch"}
		}
		return s.projectCurrent(ctx, key, canonicalID, snapshot, generation)
	}
	if code, blocked := IsBlocked(err); blocked && code == CodeOrderNotFound {
		return s.projectDeleted(ctx, key, canonicalID, generation)
	}
	return ReconcileResult{}, err
}

// resolveOrderProvider validates identity inputs and resolves the
// order-capable provider instance.
func (s *OrderService) resolveOrderProvider(providerKey, externalOrderID string) (commerce.ProviderKey, string, CommerceOrderProvider, error) {
	key, err := commerce.ValidateProviderKey(providerKey)
	if err != nil {
		return "", "", nil, apperr.New(apperr.InvalidInput, err.Error())
	}
	canonicalID, err := CanonicalExternalOrderID(externalOrderID)
	if err != nil {
		return "", "", nil, &BlockedError{Code: CodeOrderInvalid, Message: "invalid external order id"}
	}
	provider, err := s.providers.Get(key)
	if err != nil {
		return "", "", nil, err
	}
	orderProvider, err := AsOrderProvider(provider)
	if err != nil {
		return "", "", nil, err
	}
	return key, canonicalID, orderProvider, nil
}

// projectCurrent converges the projection to a live snapshot under the
// caller's generation token.
func (s *OrderService) projectCurrent(ctx context.Context, key commerce.ProviderKey, canonicalID string, snapshot OrderSnapshot, generation ReconcileGeneration) (ReconcileResult, error) {
	outcome, err := s.store.ReconcileProjectedOrder(ctx, snapshot, Fingerprint(snapshot), generation)
	if err != nil {
		return ReconcileResult{}, err
	}
	s.logInfo("order reconciled", "provider", string(key), "order", canonicalID,
		"generation", int64(generation), "revision", outcome.Revision,
		"changed", outcome.Changed, "superseded", outcome.Superseded,
		"status", string(snapshot.Canonical))
	return ReconcileResult{
		ProviderKey: string(key), ExternalOrderID: canonicalID,
		Revision: outcome.Revision, Changed: outcome.Changed, Superseded: outcome.Superseded,
		Canonical:       snapshot.Canonical,
		MappingComplete: snapshot.MappingComplete, UnmappedLines: snapshot.UnmappedLines,
	}, nil
}

// projectDeleted converges the projection to provider-confirmed deletion
// under the caller's generation token, preserving last-known data or
// writing a minimal tombstone for unseen orders.
func (s *OrderService) projectDeleted(ctx context.Context, key commerce.ProviderKey, canonicalID string, generation ReconcileGeneration) (ReconcileResult, error) {
	snapshot, _, found, err := s.store.LoadProjectedOrder(ctx, string(key), canonicalID)
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
	} else {
		// Preserve the loaded ModifiedAt: deletion delivery time is
		// transport metadata, so repeat deletes stay identical.
		snapshot.ProviderDeleted = true
		snapshot.Canonical = StatusDeleted
	}
	outcome, err := s.store.ReconcileProjectedOrder(ctx, snapshot, Fingerprint(snapshot), generation)
	if err != nil {
		return ReconcileResult{}, err
	}
	s.logInfo("order deletion reconciled", "provider", string(key), "order", canonicalID,
		"generation", int64(generation), "revision", outcome.Revision,
		"changed", outcome.Changed, "superseded", outcome.Superseded)
	return ReconcileResult{
		ProviderKey: string(key), ExternalOrderID: canonicalID,
		Revision: outcome.Revision, Changed: outcome.Changed, Superseded: outcome.Superseded,
		Canonical:       StatusDeleted,
		MappingComplete: snapshot.MappingComplete, UnmappedLines: snapshot.UnmappedLines,
		ProviderDeleted: true,
	}, nil
}

func (s *OrderService) logInfo(msg string, args ...any) {
	if s.log != nil {
		s.log.Info(msg, args...)
	}
}
