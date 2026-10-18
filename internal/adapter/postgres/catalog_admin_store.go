package postgres

import (
	"context"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalogadmin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CatalogAdmin persists immutable command intent, target transitions and
// Store-scoped projection reads. Retail remains the business-state authority.
type CatalogAdmin struct {
	pool    *pgxpool.Pool
	timeout time.Duration
}

var _ catalogadmin.Store = CatalogAdmin{}

// NewCatalogAdmin shares the application's pool without owning its lifecycle.
func NewCatalogAdmin(pool *pgxpool.Pool, timeout time.Duration) CatalogAdmin {
	return CatalogAdmin{pool: pool, timeout: timeout}
}

func (d CatalogAdmin) ctx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d.timeout)
}
