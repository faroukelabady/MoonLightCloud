package postgres

import (
	"context"
	"github.com/faroukelabady/MoonLightCloud/internal/projection"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

// SaleProjection owns its domain projection transactions and durable arbitration.
// The pool and operation timeout remain application-owned.
type SaleProjection struct {
	pool    *pgxpool.Pool
	timeout time.Duration
}

var _ sale.Store = SaleProjection{}

func NewSaleProjection(pool *pgxpool.Pool, timeout time.Duration) SaleProjection {
	return SaleProjection{pool: pool, timeout: timeout}
}
func (d SaleProjection) ctx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d.timeout)
}
func (d SaleProjection) ProcessingStats(ctx context.Context, processor string) (projection.Stats, error) {
	return NewProjectionState(d.pool, d.timeout).ProcessingStats(ctx, processor)
}
func (d SaleProjection) ResetProcessing(ctx context.Context, processor, eventID string) error {
	return NewProjectionState(d.pool, d.timeout).ResetProcessing(ctx, processor, eventID)
}
