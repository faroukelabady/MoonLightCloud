package postgres

import (
	"context"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

// CatalogProjection owns current-state projection, structural transactions and
// catalog reads. In-transaction commerce queue writes remain part of projection.
type CatalogProjection struct {
	pool    *pgxpool.Pool
	timeout time.Duration
}

var (
	_ catalog.Store      = CatalogProjection{}
	_ catalog.Repository = CatalogProjection{}
)

func NewCatalogProjection(pool *pgxpool.Pool, timeout time.Duration) CatalogProjection {
	return CatalogProjection{pool: pool, timeout: timeout}
}
func (d CatalogProjection) ctx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d.timeout)
}
