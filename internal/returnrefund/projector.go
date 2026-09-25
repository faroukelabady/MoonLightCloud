package returnrefund

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
)

// Processing statuses mirror the shared sync_event_processing CHECK
// constraint (same values as the sale processor; one table, two
// processor names).
const (
	ProcPending   = "pending"
	ProcRetry     = "retry"
	ProcBlocked   = "blocked"
	ProcProcessed = "processed"
)

// Processor name for durable return processing state. Independent from
// sale_projection.v1 while reusing the generic processing mechanism.
const ProcessorReturnProjectionV1 = "return_refund_projection.v1"

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

// EventRecord is the immutable inbox row the projector consumes.
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
// operational model across processors, so operator tooling and the postgres
// adapter stay unified.
type Stats = sale.Stats

// Store is the durable projector boundary, implemented by the postgres
// adapter. One ProjectReturn call arbitrates durably, then projects one
// return atomically.
type Store interface {
	PendingReturnEvents(ctx context.Context, processor string, limit int) ([]string, error)
	ProjectReturn(ctx context.Context, event EventRecord, now time.Time) (ProjectResult, error)
	LoadReturnEvent(ctx context.Context, eventID string) (EventRecord, bool, error)
	ProcessingStats(ctx context.Context, processor string) (Stats, error)
	ResetProcessing(ctx context.Context, processor, eventID string) error
}

// Backoff reuses the frozen sale backoff policy (5s x 2^attempt, 1h cap,
// overflow-safe) so both processors share one retry schedule.
func Backoff(attempt int) time.Duration { return sale.Backoff(attempt) }

// Projector is the in-process PostgreSQL-backed worker. Wake model: local
// Notify after ingestion plus a periodic durable safety scan plus a startup
// scan — restarts always rediscover pending work, no in-memory signal
// required. Multi-instance safe via row-level claiming (INSERT +
// SELECT FOR UPDATE); no external locks, no broker.
type Projector struct {
	store     Store
	clock     clock.Clock
	log       *slog.Logger
	wake      chan struct{}
	interval  time.Duration
	batchSize int
}

// DefaultScanInterval is the durable safety-net period. Ingestion wakes
// projection immediately; the scan only covers lost wakes and restarts.
const DefaultScanInterval = 30 * time.Second

// NewProjector wires the worker.
func NewProjector(s Store, c clock.Clock, log *slog.Logger) *Projector {
	return &Projector{store: s, clock: c, log: log, wake: make(chan struct{}, 1), interval: DefaultScanInterval, batchSize: 25}
}

// Notify wakes the projector after ingestion. Non-blocking and idempotent:
// a pending wake coalesces, the scan is authoritative anyway.
func (p *Projector) Notify() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Run drains pending work on startup, on wake, and on interval until ctx
// is cancelled. It stops claiming new work on cancellation and returns so
// the application lifecycle can close the pool.
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

// drain projects due events until none remain or ctx ends.
func (p *Projector) drain(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		ids, err := p.store.PendingReturnEvents(ctx, ProcessorReturnProjectionV1, p.batchSize)
		if err != nil {
			p.log.Error("return projection scan failed", "processor", ProcessorReturnProjectionV1, "err", err.Error())
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

// projectOnce loads the immutable event and runs the atomic projection.
// Missing events (deleted inbox rows, should not happen) are skipped with
// an error log; per-event outcomes are already persisted by the store.
func (p *Projector) projectOnce(ctx context.Context, eventID string) {
	start := p.clock.Now()
	rec, ok, err := p.store.LoadReturnEvent(ctx, eventID)
	if err != nil {
		p.log.Error("return projection load failed", "event_id", eventID, "err", err.Error())
		return
	}
	if !ok {
		p.log.Error("return projection event missing", "event_id", eventID)
		return
	}
	res, err := p.store.ProjectReturn(ctx, rec, p.clock.Now())
	if err != nil {
		p.log.Error("return projection attempt failed",
			"event_id", eventID, "err", err.Error(),
			"duration_ms", p.clock.Now().Sub(start).Milliseconds())
		return
	}
	switch res.Outcome {
	case OutcomeBlocked:
		p.log.Warn("return projection blocked",
			"event_id", eventID, "error_code", res.ErrorCode,
			"duration_ms", p.clock.Now().Sub(start).Milliseconds())
	default:
		p.log.Info("return projection outcome",
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
