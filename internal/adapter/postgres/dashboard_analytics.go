package postgres

import (
	"context"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
)

// Phase 12 operational analytics + catalog health reads. All three are
// READ-ONLY durable-state aggregations: no provider is contacted, no
// mutation barrier is settled/cleared/retried, no Store adoption or
// reconciliation ever runs from these paths. Optional filters are empty
// strings (storeID "" = global ALL+legacy; providerKey "" = all durable
// providers; reason "" = all reasons).

func (d Devices) DashboardOrderAnalytics(ctx context.Context, providerKey, storeID, currency string, startUTC, endUTC time.Time) ([]dashboard.OrderAnalyticsRowRaw, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).DashboardOrderAnalytics(ctx, sqlcgen.DashboardOrderAnalyticsParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC),
		ProviderKey: providerKey, StoreID: storeID, Currency: currency,
	})
	if err != nil {
		return nil, reportErr("order analytics", err)
	}
	out := make([]dashboard.OrderAnalyticsRowRaw, 0, len(rows))
	for _, r := range rows {
		out = append(out, dashboard.OrderAnalyticsRowRaw{
			ProviderKey: r.ProviderKey, CanonicalStatus: r.CanonicalStatus,
			Currency: r.Currency, Orders: r.OrdersCount, ValueMinor: r.ValueMinor,
		})
	}
	return out, nil
}

func (d Devices) CatalogHealthSummaryRows(ctx context.Context, storeID, providerKey string) ([]dashboard.CatalogHealthCountRaw, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).CatalogHealthSummary(ctx, sqlcgen.CatalogHealthSummaryParams{
		StoreID: storeID, ProviderKey: providerKey,
	})
	if err != nil {
		return nil, reportErr("catalog health summary", err)
	}
	out := make([]dashboard.CatalogHealthCountRaw, 0, len(rows))
	for _, r := range rows {
		out = append(out, dashboard.CatalogHealthCountRaw{ReasonCode: r.ReasonCode, Products: r.Products})
	}
	return out, nil
}

func (d Devices) CatalogHealthDetailRows(ctx context.Context, storeID, providerKey, reason string, limit int) ([]dashboard.CatalogHealthRowRaw, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).CatalogHealthDetail(ctx, sqlcgen.CatalogHealthDetailParams{
		StoreID: storeID, ProviderKey: providerKey, Reason: reason, LimitN: int32(limit),
	})
	if err != nil {
		return nil, reportErr("catalog health detail", err)
	}
	out := make([]dashboard.CatalogHealthRowRaw, 0, len(rows))
	for _, r := range rows {
		out = append(out, dashboard.CatalogHealthRowRaw{
			ReasonCode: r.ReasonCode, ProviderKey: r.ProviderKey,
			ProductID: uuidPtr(r.ProductID), SKU: r.Sku, Name: r.Name,
			StoreID: uuidPtr(r.StoreID),
		})
	}
	return out, nil
}

func (d Devices) CatalogHealthProviders(ctx context.Context, storeID string) ([]string, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).CatalogHealthProviders(ctx, storeID)
	if err != nil {
		return nil, reportErr("catalog health providers", err)
	}
	return rows, nil
}
