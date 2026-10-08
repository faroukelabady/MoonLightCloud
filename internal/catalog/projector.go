package catalog

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
)

// Processing statuses mirror the shared sync_event_processing CHECK
// constraint (one table, five processor names).
const (
	ProcPending   = "pending"
	ProcRetry     = "retry"
	ProcBlocked   = "blocked"
	ProcProcessed = "processed"
)

// Processor names for durable catalog processing state. Independent from
// sale/return projection while reusing the generic processing mechanism.
const (
	ProcessorCategoryProjectionV1 = "catalog_category_projection.v1"
	ProcessorTagProjectionV1      = "catalog_tag_projection.v1"
	ProcessorProductProjectionV1  = "catalog_product_projection.v1"
	// Phase 5B: independent policy stream with its own processing identity.
	ProcessorProductSalesPolicyProjectionV1 = "catalog_product_sales_policy_projection.v1"
	// Phase 5C: independent inventory stream with its own processing identity.
	ProcessorProductInventoryProjectionV1 = "inventory_product_projection.v1"
	// Phase 15: independent ONLINE product-option (frame configuration)
	// stream with its own processing identity.
	ProcessorProductConfigurationProjectionV1 = "catalog_product_configuration_projection.v1"
	// Phase 17: independent variant catalog stream (variant_revision)
	// with its own processing identity.
	ProcessorProductVariantProjectionV1 = "catalog_product_variant_projection.v1"
	// Phase 17: independent variant inventory stream (inventory_revision)
	// with its own processing identity.
	ProcessorProductVariantInventoryProjectionV1 = "inventory_product_variant_projection.v1"
	// Phase 17-R2: independent structural-type stream (type_revision)
	// with its own processing identity.
	ProcessorProductTypeProjectionV1 = "catalog_product_type_projection.v1"
)

// Outcome of one projection attempt.
type Outcome int

const (
	OutcomeProcessed Outcome = iota + 1
	OutcomeAlready
	OutcomeBlocked
	OutcomeNotDue
	OutcomeRetryable
)

// ProjectResult carries the outcome plus stable diagnostics.
type ProjectResult struct {
	Outcome   Outcome
	ErrorCode string
}

// EventRecord is the immutable inbox row a catalog projector consumes.
type EventRecord struct {
	EventID      string
	DeviceID     string
	CredentialID *string
	EventType    string
	OccurredAt   time.Time
	ReceivedAt   time.Time
	Payload      json.RawMessage
	// StoreID is the trusted ingress Store context (sync_events.store_id),
	// nil for legacy/unbound events. Projectors copy it verbatim; they
	// never re-derive Store ownership from bindings or payloads.
	StoreID *string
}

// Stats reuses the frozen sale processing-stats shape: one generic
// operational model across processors.
type Stats = sale.Stats

// Store is the durable projector boundary, implemented by the postgres
// adapter. One Project* call is one atomic claim+project transaction.
type Store interface {
	PendingCatalogEvents(ctx context.Context, processor, eventType string, limit int, asOf time.Time) ([]string, error)
	// NextCatalogRetry reports the earliest future durable retry time for
	// the processor (found=false when none). A wake hint only.
	NextCatalogRetry(ctx context.Context, processor, eventType string, asOf time.Time) (time.Time, bool, error)
	// RearmBlockedProducts flips graph-dependent blocked Product events
	// back to pending when the relevant graph has advanced since the
	// block decision (durable R2 fallback; no-op when nothing qualifies).
	RearmBlockedProducts(ctx context.Context) error
	ProjectCategory(ctx context.Context, event EventRecord, now time.Time) (ProjectResult, error)
	ProjectTag(ctx context.Context, event EventRecord, now time.Time) (ProjectResult, error)
	ProjectProduct(ctx context.Context, event EventRecord, now time.Time) (ProjectResult, error)
	ProjectProductSalesPolicy(ctx context.Context, event EventRecord, now time.Time) (ProjectResult, error)
	ProjectProductInventory(ctx context.Context, event EventRecord, now time.Time) (ProjectResult, error)
	ProjectProductConfigurations(ctx context.Context, event EventRecord, now time.Time) (ProjectResult, error)
	ProjectProductVariant(ctx context.Context, event EventRecord, now time.Time) (ProjectResult, error)
	ProjectProductVariantInventory(ctx context.Context, event EventRecord, now time.Time) (ProjectResult, error)
	ProjectProductType(ctx context.Context, event EventRecord, now time.Time) (ProjectResult, error)
	LoadCatalogEvent(ctx context.Context, eventID string) (EventRecord, bool, error)
	ProcessingStats(ctx context.Context, processor string) (Stats, error)
	ResetProcessing(ctx context.Context, processor, eventID string) error
}

// Backoff reuses the frozen sale backoff policy (5s x 2^attempt, 1h cap,
// overflow-safe) so all processors share one retry schedule.
func Backoff(attempt int) time.Duration { return sale.Backoff(attempt) }

// Projector is the in-process PostgreSQL-backed worker for one catalog
// processor. Wake model: local Notify after ingestion, a wake when another
// catalog projector makes progress (dependency landed), a one-shot wake at
// the earliest scheduled durable retry, plus a periodic durable safety
// scan and a startup scan. Wakes are hints only. Multi-instance safe via
// row-level claiming; no external locks, no broker.
type Projector struct {
	store     Store
	processor string
	eventType string
	// rearmBlocked enables the durable blocked-row fallback in drain.
	// Only the product processor has graph-dependent blocked states;
	// category/tag blocks are always terminal-deterministic.
	rearmBlocked bool
	project      func(ctx context.Context, event EventRecord, now time.Time) (ProjectResult, error)
	load         func(ctx context.Context, eventID string) (EventRecord, bool, error)
	clock        clock.Clock
	log          *slog.Logger
	wake         chan struct{}
	interval     time.Duration
	batchSize    int
	// onProgress runs after a drain pass that processed at least one
	// event, so dependents waiting on this stream retry promptly.
	onProgress func()
}

// DefaultScanInterval is the durable safety-net period.
const DefaultScanInterval = 30 * time.Second

// minRetryWake bounds scheduled-retry wakes. The drain also yields when
// discovery only repeats deferred work, including disagreement about due times.
const minRetryWake = 500 * time.Millisecond

// WakeOnProgress registers fn to run after any drain pass that processed
// at least one event. Call before Run. fn must not block (Notify is
// non-blocking and idempotent).
func (p *Projector) WakeOnProgress(fn func()) {
	p.onProgress = fn
}

// NewCategoryProjector wires one canonical Category stream. Pending discovery
// includes the explicitly supported v1/v2 versions under the same processor.
func NewCategoryProjector(s Store, c clock.Clock, log *slog.Logger) *Projector {
	return newProjector(s, ProcessorCategoryProjectionV1, EventCategorySnapshotV1, s.ProjectCategory, c, log)
}

// NewTagProjector wires the tag worker.
func NewTagProjector(s Store, c clock.Clock, log *slog.Logger) *Projector {
	return newProjector(s, ProcessorTagProjectionV1, EventTagSnapshotV1, s.ProjectTag, c, log)
}

// NewProductProjector wires the product worker with the durable
// blocked-row fallback (R2): graph advancement re-arms
// CATALOG_INVALID_RELATION blocks even if the post-commit reset failed,
// was lost to a crash, or never ran.
func NewProductProjector(s Store, c clock.Clock, log *slog.Logger) *Projector {
	p := newProjector(s, ProcessorProductProjectionV1, EventProductSnapshotV1, s.ProjectProduct, c, log)
	p.rearmBlocked = true
	return p
}

// NewProductSalesPolicyProjector wires the Phase 5B policy worker. Policy
// blocks are terminal-deterministic (no graph waits), so no re-arm hook:
// stale/conflict/out-of-order cases resolve through revision semantics.
func NewProductSalesPolicyProjector(s Store, c clock.Clock, log *slog.Logger) *Projector {
	return newProjector(s, ProcessorProductSalesPolicyProjectionV1, EventProductSalesPolicySnapshotV1, s.ProjectProductSalesPolicy, c, log)
}

// NewProductInventoryProjector wires the Phase 5C inventory worker.
// Inventory blocks are terminal-deterministic (product dependency is the
// only wait); no re-arm hook.
func NewProductInventoryProjector(s Store, c clock.Clock, log *slog.Logger) *Projector {
	return newProjector(s, ProcessorProductInventoryProjectionV1, EventInventoryProductSnapshotV1, s.ProjectProductInventory, c, log)
}

// NewProductConfigurationsProjector projects the Phase 15 ONLINE
// product-option stream (Retail-authoritative frame configurations).
func NewProductConfigurationsProjector(s Store, c clock.Clock, log *slog.Logger) *Projector {
	return newProjector(s, ProcessorProductConfigurationProjectionV1, EventProductConfigurationSnapshotV1, s.ProjectProductConfigurations, c, log)
}

// NewProductVariantProjector projects the Phase 17 variant catalog
// stream (variant_revision gated). Variant blocks are terminal-
// deterministic (the product dependency is the only wait); no re-arm hook.
func NewProductVariantProjector(s Store, c clock.Clock, log *slog.Logger) *Projector {
	return newProjector(s, ProcessorProductVariantProjectionV1, EventProductVariantSnapshotV1, s.ProjectProductVariant, c, log)
}

// NewProductVariantInventoryProjector projects the Phase 17 variant
// inventory stream (inventory_revision gated).
func NewProductVariantInventoryProjector(s Store, c clock.Clock, log *slog.Logger) *Projector {
	return newProjector(s, ProcessorProductVariantInventoryProjectionV1, EventInventoryProductVariantSnapshotV1, s.ProjectProductVariantInventory, c, log)
}

// NewProductTypeProjector projects the Phase 17-R2 structural-type
// stream (type_revision gated). Type blocks are terminal-deterministic
// (no product dependency); no re-arm hook.
func NewProductTypeProjector(s Store, c clock.Clock, log *slog.Logger) *Projector {
	return newProjector(s, ProcessorProductTypeProjectionV1, EventProductTypeSnapshotV1, s.ProjectProductType, c, log)
}

func newProjector(s Store, processor, eventType string,
	project func(ctx context.Context, event EventRecord, now time.Time) (ProjectResult, error),
	c clock.Clock, log *slog.Logger) *Projector {
	return &Projector{store: s, processor: processor, eventType: eventType,
		project: project, load: s.LoadCatalogEvent,
		clock: c, log: log, wake: make(chan struct{}, 1),
		interval: DefaultScanInterval, batchSize: 25}
}

// Notify wakes the projector after ingestion. Non-blocking and idempotent.
func (p *Projector) Notify() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Run drains pending work on startup, on wake, at the earliest scheduled
// retry, and on interval until ctx is cancelled.
func (p *Projector) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	retry := time.NewTimer(p.interval)
	retry.Stop()
	defer retry.Stop()
	p.cycle(ctx, retry)
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
			p.cycle(ctx, retry)
		case <-ticker.C:
			p.cycle(ctx, retry)
		case <-retry.C:
			p.cycle(ctx, retry)
		}
	}
}

// cycle drains due work, wakes dependents after progress, and re-arms the
// scheduled-retry wake.
func (p *Projector) cycle(ctx context.Context, retry *time.Timer) {
	if p.drain(ctx) && p.onProgress != nil {
		p.onProgress()
	}
	p.armRetry(ctx, retry)
}

// armRetry schedules a one-shot wake at the earliest durable retry so a
// dependency wait or transient conflict re-runs when it falls due rather
// than at the next safety scan. Failures leave the safety scan in charge.
func (p *Projector) armRetry(ctx context.Context, retry *time.Timer) {
	retry.Stop()
	if ctx.Err() != nil {
		return
	}
	next, ok, err := p.store.NextCatalogRetry(ctx, p.processor, p.eventType, p.clock.Now())
	if err != nil {
		p.log.Error("catalog retry schedule scan failed", "processor", p.processor, "err", err.Error())
		return
	}
	if !ok {
		return
	}
	wait := next.Sub(p.clock.Now())
	if wait < minRetryWake {
		wait = minRetryWake
	}
	if wait >= p.interval {
		return
	}
	retry.Reset(wait)
}

// drain projects every due event and reports whether any was processed.
func (p *Projector) drain(ctx context.Context) bool {
	progressed := false
	deferred := make(map[string]struct{})
	for {
		if ctx.Err() != nil {
			return progressed
		}
		if p.rearmBlocked {
			// Durable fallback BEFORE discovery so re-armed rows are
			// picked up in the same pass. Strictly monotonic predicate:
			// steady state finds nothing and costs one cheap query.
			if err := p.store.RearmBlockedProducts(ctx); err != nil {
				p.log.Error("catalog re-arm scan failed", "processor", p.processor, "err", err.Error())
				return progressed
			}
		}
		ids, err := p.store.PendingCatalogEvents(ctx, p.processor, p.eventType, p.batchSize, p.clock.Now())
		if err != nil {
			p.log.Error("catalog projection scan failed", "processor", p.processor, "err", err.Error())
			return progressed
		}
		if len(ids) == 0 {
			return progressed
		}
		attempted := false
		for _, id := range ids {
			if ctx.Err() != nil {
				return progressed
			}
			if _, seen := deferred[id]; seen {
				continue
			}
			attempted = true
			switch p.projectOnce(ctx, id) {
			case OutcomeProcessed:
				progressed = true
			case OutcomeAlready, OutcomeBlocked:
				// Terminal rows leave discovery even without a newly
				// projected dependency. Keep draining subsequent batches.
			default:
				// Retryable/not-due work gets at most one attempt in this
				// drain. Keep discovering other work: a successfully
				// scheduled retry may no longer occupy the next batch.
				deferred[id] = struct{}{}
			}
		}
		if !attempted {
			// PostgreSQL discovery and application claiming can disagree
			// about whether a retry is due. Retrying the same unchanged
			// batch here would bypass every outer timer/backoff. Yield to
			// the retry wake, notifications or durable safety scan instead.
			return progressed
		}
	}
}

func (p *Projector) projectOnce(ctx context.Context, eventID string) Outcome {
	start := p.clock.Now()
	rec, ok, err := p.load(ctx, eventID)
	if err != nil {
		p.log.Error("catalog projection load failed", "event_id", eventID, "err", err.Error())
		return OutcomeRetryable
	}
	if !ok {
		p.log.Error("catalog projection event missing", "event_id", eventID)
		return OutcomeRetryable
	}
	res, err := p.project(ctx, rec, p.clock.Now())
	if err != nil {
		p.log.Error("catalog projection attempt failed",
			"event_id", eventID, "err", err.Error(),
			"duration_ms", p.clock.Now().Sub(start).Milliseconds())
		return OutcomeRetryable
	}
	switch res.Outcome {
	case OutcomeBlocked:
		p.log.Warn("catalog projection blocked",
			"event_id", eventID, "error_code", res.ErrorCode,
			"duration_ms", p.clock.Now().Sub(start).Milliseconds())
	default:
		p.log.Info("catalog projection outcome",
			"event_id", eventID, "outcome", outcomeString(res.Outcome),
			"duration_ms", p.clock.Now().Sub(start).Milliseconds())
	}
	return res.Outcome
}

func outcomeString(o Outcome) string {
	switch o {
	case OutcomeProcessed:
		return "processed"
	case OutcomeAlready:
		return "already"
	case OutcomeBlocked:
		return "blocked"
	case OutcomeNotDue:
		return "not_due"
	default:
		return "retryable"
	}
}
