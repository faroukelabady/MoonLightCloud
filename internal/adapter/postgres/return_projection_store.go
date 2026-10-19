package postgres

import (
	"context"
	"github.com/faroukelabady/MoonLightCloud/internal/projection"
	"github.com/faroukelabady/MoonLightCloud/internal/returnrefund"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

// ReturnProjection owns its domain projection transactions and durable arbitration.
// The pool and operation timeout remain application-owned.
type ReturnProjection struct {
	pool    *pgxpool.Pool
	timeout time.Duration
}

var _ returnrefund.Store = ReturnProjection{}

func NewReturnProjection(pool *pgxpool.Pool, timeout time.Duration) ReturnProjection {
	return ReturnProjection{pool: pool, timeout: timeout}
}
func (d ReturnProjection) ctx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d.timeout)
}
func (d ReturnProjection) ProcessingStats(ctx context.Context, processor string) (projection.Stats, error) {
	return NewProjectionState(d.pool, d.timeout).ProcessingStats(ctx, processor)
}
func (d ReturnProjection) ResetProcessing(ctx context.Context, processor, eventID string) error {
	return NewProjectionState(d.pool, d.timeout).ResetProcessing(ctx, processor, eventID)
}
