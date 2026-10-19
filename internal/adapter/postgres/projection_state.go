package postgres

import (
	"context"
	"errors"
	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/projection"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

// ProjectionState owns shared diagnostics and explicit manual retry SQL.
// Business projection transactions remain with their domain adapters.
type ProjectionState struct {
	pool    *pgxpool.Pool
	timeout time.Duration
}

func NewProjectionState(pool *pgxpool.Pool, timeout time.Duration) ProjectionState {
	return ProjectionState{pool: pool, timeout: timeout}
}
func (d ProjectionState) ctx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d.timeout)
}

// ProcessingStats returns operator visibility: counts by status, oldest
// pending age, and the latest error. Safe values only.
func (d ProjectionState) ProcessingStats(ctx context.Context, processor string) (projection.Stats, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	q := sqlcgen.New(d.pool)
	stats := projection.Stats{Counts: map[string]int64{}}
	rows, err := q.ProcessingStatus(ctx, processor)
	if err != nil {
		return projection.Stats{}, apperr.Wrap(apperr.Internal, "processing stats", redact(err))
	}
	for _, r := range rows {
		stats.Counts[r.Status] = r.Total
	}
	age, err := q.OldestPendingAge(ctx, processor)
	if err != nil {
		return projection.Stats{}, apperr.Wrap(apperr.Internal, "processing stats", redact(err))
	}
	stats.PendingCount = age.Total
	if age.Oldest.Valid {
		t := age.Oldest.Time
		stats.OldestPending = &t
	}
	last, err := q.LastProcessingError(ctx, processor)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return projection.Stats{}, apperr.Wrap(apperr.Internal, "processing stats", redact(err))
		}
	} else {
		stats.LastErrorEvent = uuidString(last.EventID)
		stats.LastErrorCode = last.LastErrorCode.String
		stats.LastErrorMsg = last.LastErrorMessage.String
	}
	return stats, nil
}

// ResetProcessing returns a retry/blocked event to pending for manual
// recovery. The immutable source event is untouched.
func (d ProjectionState) ResetProcessing(ctx context.Context, processor, eventID string) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(eventID)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "event_id must be a UUID")
	}
	if err := sqlcgen.New(d.pool).ResetProcessing(ctx, sqlcgen.ResetProcessingParams{
		EventID: uid, Processor: processor,
	}); err != nil {
		return apperr.Wrap(apperr.Internal, "reset processing", redact(err))
	}
	return nil
}
