package commerce

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Phase 13 §83: the generic durable commerce re-evaluation mechanism.
//
// Discovery proved no commerce scheduling existed (publication was
// manual CLI only), so this is the smallest generic durable mechanism
// that satisfies the fan-out invariants:
//
//   - provider-neutral: rows are Product-keyed; the worker simply runs
//     the existing CommerceService.SyncProduct for every registered
//     provider (Woo and Shopify converge independently; one provider
//     failure never blocks the other's durable progress);
//   - Product-keyed and coalescing: one row per Product (primary key),
//     so shared-DAG paths collapse to one logical re-evaluation;
//   - Store-safe: rows carry the Product's proven Store;
//   - restart-safe: enqueue commits atomically with the projection; a
//     crash at any point leaves durable rows that a later worker picks
//     up (leased claims return abandoned work automatically);
//   - multi-instance safe: atomic SKIP-LOCKED claim with a lease, plus
//     the existing per-product commerce coordination lock underneath;
//   - always re-reads CURRENT desired state at execution time, so rapid
//     policy toggles converge to the latest committed state and a stale
//     operation cannot erase newer durable intent;
//   - bounded: fixed batch size, sequential per-Product processing.
//
// Respected frozen semantics: unresolved commerce mutation barriers
// surface as COMMERCE_MUTATION_UNCERTAIN and the request is retried
// later (never bypassed, never blindly resent); ambiguous provider
// outcomes stay governed by the frozen provider rules.

// ProductReevaluation is one durable Product re-evaluation request.
type ProductReevaluation struct {
	ProductID         string
	StoreID           *string
	Reason            string
	Attempts          int32
	ClaimedGeneration int64
	LeaseGeneration   int64
	LeaseToken        string
	LeaseUntil        time.Time
}

// ProductReevaluationStore is the durable queue boundary.
type ProductReevaluationStore interface {
	ClaimProductReevaluations(ctx context.Context, limit int, leaseUntil time.Time) ([]ProductReevaluation, error)
	CompleteProductReevaluation(ctx context.Context, claim ProductReevaluation) (bool, error)
	RetryProductReevaluation(ctx context.Context, claim ProductReevaluation, next time.Time, code string) (bool, error)
}

// ReevaluationWorker tunables (same shape as the projection/order
// workers: wake on enqueue, bounded scan cadence as a safety net).
const (
	ReevaluationInterval = 15 * time.Second
	ReevaluationBatch    = 25
	ReevaluationLease    = 5 * time.Minute
)

// ReevaluationWorker drains durable Product re-evaluation requests.
type ReevaluationWorker struct {
	store    ProductReevaluationStore
	service  *CommerceService
	registry *Registry
	now      func() time.Time
	log      *slog.Logger
	wake     chan struct{}
}

// NewReevaluationWorker wires the worker over the durable queue and the
// generic commerce orchestration.
func NewReevaluationWorker(store ProductReevaluationStore, service *CommerceService, registry *Registry, now func() time.Time, log *slog.Logger) *ReevaluationWorker {
	return &ReevaluationWorker{
		store: store, service: service, registry: registry,
		now: now, log: log, wake: make(chan struct{}, 1),
	}
}

// Notify wakes the worker after a projector enqueues work. Non-blocking.
func (w *ReevaluationWorker) Notify() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run drains on startup, on wake, and on interval until cancellation.
func (w *ReevaluationWorker) Run(ctx context.Context) {
	w.drain(ctx)
	ticker := time.NewTicker(ReevaluationInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
			w.drain(ctx)
		case <-ticker.C:
			w.drain(ctx)
		}
	}
}

func (w *ReevaluationWorker) drain(ctx context.Context) {
	claimed, err := w.store.ClaimProductReevaluations(ctx, ReevaluationBatch, w.now().Add(ReevaluationLease))
	if err != nil {
		w.log.Error("commerce reevaluation claim failed", "err", err.Error())
		return
	}
	for _, request := range claimed {
		if ctx.Err() != nil {
			return
		}
		w.processOne(ctx, request)
	}
}

// processOne re-evaluates one Product against EVERY registered provider
// in one pass (§78): each provider call is an independent durable
// convergence, so one provider's failure never blocks another provider's
// progress — and never rolls it back. The request completes only when
// every provider converged; any failure (including an unresolved
// mutation barrier, which is retried — never bypassed, never blindly
// resent) schedules a bounded retry for the remaining work.
func (w *ReevaluationWorker) processOne(ctx context.Context, request ProductReevaluation) {
	keys := w.registry.List()
	failed := ""
	for _, key := range keys {
		// A batch member may wait behind slow provider work. It must be
		// reclaimed rather than starting another operation after expiry.
		if ctx.Err() != nil || !w.now().Before(request.LeaseUntil) {
			return
		}
		if _, err := w.service.SyncProduct(ctx, string(key), request.ProductID); err != nil {
			if failed == "" {
				failed = errorCode(err)
			}
			w.log.Info("commerce reevaluation retryable",
				"provider", string(key), "product", request.ProductID,
				"reason", request.Reason, "code", errorCode(err))
		}
	}
	if failed == "" {
		if _, err := w.store.CompleteProductReevaluation(ctx, request); err != nil {
			w.log.Error("commerce reevaluation complete failed", "product", request.ProductID, "err", err.Error())
		}
		return
	}
	next := w.now().Add(reevaluationBackoff(int(request.Attempts)))
	if _, err := w.store.RetryProductReevaluation(ctx, request, next, failed); err != nil {
		w.log.Error("commerce reevaluation retry failed", "product", request.ProductID, "err", err.Error())
	}
}

// reevaluationBackoff spaces retries: 5s doubling per attempt, capped at
// one hour (the projection retry convention). Bounded — never a hot
// loop, never unbounded.
func reevaluationBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := 5 * time.Second
	for i := 0; i < attempt && delay < time.Hour; i++ {
		delay *= 2
	}
	if delay > time.Hour {
		delay = time.Hour
	}
	return delay
}

// errorCode renders a stable, bounded diagnostic code for the queue row
// (operator diagnostics only: stable taxonomy values, never payloads,
// provider messages or secrets). An unresolved mutation barrier
// classifies as PROVIDER_conflict here and is retried later — barriers
// are never bypassed and never blindly resent.
func errorCode(err error) string {
	if err == nil {
		return ""
	}
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		return "PROVIDER_" + string(providerErr.Kind)
	}
	return "COMMERCE_REEVALUATION_ERROR"
}
