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
}

// Stats reuses the frozen sale processing-stats shape: one generic
// operational model across processors.
type Stats = sale.Stats

// Store is the durable projector boundary, implemented by the postgres
// adapter. One Project* call is one atomic claim+project transaction.
type Store interface {
	PendingCatalogEvents(ctx context.Context, processor, eventType string, limit int) ([]string, error)
	ProjectCategory(ctx context.Context, event EventRecord, now time.Time) (ProjectResult, error)
	ProjectTag(ctx context.Context, event EventRecord, now time.Time) (ProjectResult, error)
	ProjectProduct(ctx context.Context, event EventRecord, now time.Time) (ProjectResult, error)
	LoadCatalogEvent(ctx context.Context, eventID string) (EventRecord, bool, error)
	ProcessingStats(ctx context.Context, processor string) (Stats, error)
	ResetProcessing(ctx context.Context, processor, eventID string) error
}

// Backoff reuses the frozen sale backoff policy (5s x 2^attempt, 1h cap,
// overflow-safe) so all processors share one retry schedule.
func Backoff(attempt int) time.Duration { return sale.Backoff(attempt) }

// Projector is the in-process PostgreSQL-backed worker for one catalog
// processor. Wake model: local Notify after ingestion plus a periodic
// durable safety scan plus a startup scan. Multi-instance safe via
// row-level claiming; no external locks, no broker.
type Projector struct {
	store     Store
	processor string
	eventType string
	project   func(ctx context.Context, event EventRecord, now time.Time) (ProjectResult, error)
	load      func(ctx context.Context, eventID string) (EventRecord, bool, error)
	clock     clock.Clock
	log       *slog.Logger
	wake      chan struct{}
	interval  time.Duration
	batchSize int
}

// DefaultScanInterval is the durable safety-net period.
const DefaultScanInterval = 30 * time.Second

// NewCategoryProjector wires the category worker.
func NewCategoryProjector(s Store, c clock.Clock, log *slog.Logger) *Projector {
	return newProjector(s, ProcessorCategoryProjectionV1, EventCategorySnapshotV1, s.ProjectCategory, c, log)
}

// NewTagProjector wires the tag worker.
func NewTagProjector(s Store, c clock.Clock, log *slog.Logger) *Projector {
	return newProjector(s, ProcessorTagProjectionV1, EventTagSnapshotV1, s.ProjectTag, c, log)
}

// NewProductProjector wires the product worker.
func NewProductProjector(s Store, c clock.Clock, log *slog.Logger) *Projector {
	return newProjector(s, ProcessorProductProjectionV1, EventProductSnapshotV1, s.ProjectProduct, c, log)
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

// Run drains pending work on startup, on wake, and on interval until ctx
// is cancelled.
func (p *Projector) Run(ctx context.Context) {
	p.drain(ctx)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
			p.drain(ctx)
		case <-ticker.C:
			p.drain(ctx)
		}
	}
}

func (p *Projector) drain(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		ids, err := p.store.PendingCatalogEvents(ctx, p.processor, p.eventType, p.batchSize)
		if err != nil {
			p.log.Error("catalog projection scan failed", "processor", p.processor, "err", err.Error())
			return
		}
		if len(ids) == 0 {
			return
		}
		for _, id := range ids {
			if ctx.Err() != nil {
				return
			}
			p.projectOnce(ctx, id)
		}
	}
}

func (p *Projector) projectOnce(ctx context.Context, eventID string) {
	start := p.clock.Now()
	rec, ok, err := p.load(ctx, eventID)
	if err != nil {
		p.log.Error("catalog projection load failed", "event_id", eventID, "err", err.Error())
		return
	}
	if !ok {
		p.log.Error("catalog projection event missing", "event_id", eventID)
		return
	}
	res, err := p.project(ctx, rec, p.clock.Now())
	if err != nil {
		p.log.Error("catalog projection attempt failed",
			"event_id", eventID, "err", err.Error(),
			"duration_ms", p.clock.Now().Sub(start).Milliseconds())
		return
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
