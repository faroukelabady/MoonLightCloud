package postgres

import (
	"context"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

// CommercePersistence owns provider mappings, durable reevaluation work and
// product mutation coordination. It does not own authoritative catalog state.
type CommercePersistence struct {
	pool    *pgxpool.Pool
	timeout time.Duration
}

var (
	_ commerce.ProductMappingRepository           = CommercePersistence{}
	_ commerce.VariantMappingTransitionRepository = CommercePersistence{}
	_ commerce.ProductReevaluationStore           = CommercePersistence{}
	_ commerce.MediaMappingRepository             = CommercePersistence{}
	_ commerce.ProductSyncCoordinator             = CommercePersistence{}
)

func NewCommercePersistence(pool *pgxpool.Pool, timeout time.Duration) CommercePersistence {
	return CommercePersistence{pool: pool, timeout: timeout}
}
func (d CommercePersistence) ctx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d.timeout)
}
