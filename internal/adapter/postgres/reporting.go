package postgres

import (
	"context"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Reporting owns read-only financial and dashboard projection queries.
// It has no device lifecycle or canonical business mutation methods.
type Reporting struct {
	pool    *pgxpool.Pool
	timeout time.Duration
}

var _ report.Repository = Reporting{}
var _ dashboard.Repository = Reporting{}

// NewReporting shares the application's pool; callers retain its lifecycle.
func NewReporting(pool *pgxpool.Pool, timeout time.Duration) Reporting {
	return Reporting{pool: pool, timeout: timeout}
}

func (d Reporting) ctx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d.timeout)
}
