package businessreports

import (
	"context"
	"log/slog"
	"time"
)

// Planner materializes due schedule slots into runs. It performs short
// row-locked transactions only: no report computation, no provider
// I/O, no notification calls inside planner work. Catch-up processes
// oldest-first, one slot per schedule per materialization, across
// ticks for unbounded backlogs.
type Planner struct {
	schedules SchedulePlannerStore
	clock     Clock
	loc       *time.Location
	log       *slog.Logger
	wake      chan struct{}
	interval  time.Duration
	batch     int32
}

// NewPlanner wires the planner over the atomic schedule store.
func NewPlanner(schedules SchedulePlannerStore, clock Clock, loc *time.Location, interval time.Duration, batch int32, log *slog.Logger) *Planner {
	return &Planner{
		schedules: schedules, clock: clock, loc: loc, log: log,
		wake: make(chan struct{}, 1), interval: interval, batch: batch,
	}
}

// Notify wakes the planner after schedule changes. Non-blocking.
func (p *Planner) Notify() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Run scans on startup, on wake, and on interval until cancellation.
func (p *Planner) Run(ctx context.Context) {
	p.tick(ctx)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
			p.tick(ctx)
		case <-ticker.C:
			p.tick(ctx)
		}
	}
}

// tick materializes one bounded batch of due slots: oldest due first,
// one slot per schedule. A tick returns after the batch even when more
// slots remain due; the next tick continues without skipping.
// TickForTest runs one bounded planner pass. Test hook only:
// production uses Run with wakeups and intervals.
func (p *Planner) TickForTest(ctx context.Context) {
	p.tick(ctx)
}

func (p *Planner) tick(ctx context.Context) {
	due, err := p.schedules.ClaimDueSchedules(ctx, p.batch)
	if err != nil {
		p.logError("report planner claim failed", "err", err.Error())
		return
	}
	for _, schedule := range due {
		if ctx.Err() != nil {
			return
		}
		outcome, err := p.schedules.MaterializeNextSlot(ctx, schedule.ID, p.clock.Now(), p.loc)
		if err != nil {
			p.logError("report slot materialization failed",
				"schedule", schedule.ID, "err", err.Error())
			continue
		}
		if outcome.Ran {
			p.logInfo("report slot materialized",
				"schedule", schedule.ID, "slot", outcome.SlotDate,
				"run", outcome.RunID, "created", outcome.Created)
		}
	}
}

func (p *Planner) logInfo(msg string, args ...any) {
	if p.log != nil {
		p.log.Info(msg, args...)
	}
}

func (p *Planner) logError(msg string, args ...any) {
	if p.log != nil {
		p.log.Error(msg, args...)
	}
}
