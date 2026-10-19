package postgres

import (
	"context"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

// CommerceOrders owns webhook fencing, order projections/history and their
// read models. Provider networking and catalog mappings remain separate.
type CommerceOrders struct {
	pool    *pgxpool.Pool
	timeout time.Duration
}

var (
	_ orders.OrderStore   = CommerceOrders{}
	_ orders.OrderReader  = CommerceOrders{}
	_ orders.WebhookStore = CommerceOrders{}
)

// NewCommerceOrders shares the application-owned pool and query timeout.
func NewCommerceOrders(pool *pgxpool.Pool, timeout time.Duration) CommerceOrders {
	return CommerceOrders{pool: pool, timeout: timeout}
}
func (d CommerceOrders) ctx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d.timeout)
}
