package postgres

import (
	"context"
	"strconv"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
)

// pgIntToString renders exact minor units for the BFF string-money
// contract (never floats, never JS arithmetic inputs).
func pgIntToString(v int64) string { return strconv.FormatInt(v, 10) }

// Dashboard repository implementation on Devices (shared pool + timeouts).

func (d Devices) DashboardNormalizedSummary(ctx context.Context, startUTC, endUTC time.Time) (dashboard.NormalizedSummaryRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	r, err := sqlcgen.New(d.pool).DashboardNormalizedSummary(ctx, sqlcgen.DashboardNormalizedSummaryParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC),
	})
	if err != nil {
		return dashboard.NormalizedSummaryRow{}, reportErr("dashboard normalized summary", err)
	}
	return dashboard.NormalizedSummaryRow{
		Transactions: r.Transactions, Units: r.Units, Normalized: r.NormalizedTotal,
		USDSales: r.UsdSales, USDMissingFx: r.UsdMissingFx,
	}, nil
}

func (d Devices) DashboardNormalizedDaily(ctx context.Context, startUTC, endUTC time.Time, timezone string) ([]dashboard.NormalizedDailyRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).DashboardNormalizedDaily(ctx, sqlcgen.DashboardNormalizedDailyParams{
		Timezone: timezone, StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC),
	})
	if err != nil {
		return nil, reportErr("dashboard normalized daily", err)
	}
	out := make([]dashboard.NormalizedDailyRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, dashboard.NormalizedDailyRow{
			Date: r.Day, Transactions: r.Transactions, Normalized: r.NormalizedTotal,
		})
	}
	return out, nil
}

func (d Devices) DashboardLatestFx(ctx context.Context, startUTC, endUTC time.Time) (dashboard.LatestFxRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	r, err := sqlcgen.New(d.pool).DashboardLatestFx(ctx, sqlcgen.DashboardLatestFxParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC),
	})
	if err != nil {
		return dashboard.LatestFxRow{}, reportErr("dashboard latest fx", err)
	}
	out := dashboard.LatestFxRow{DistinctRates: r.DistinctRates, USDSales: r.UsdSales}
	if r.LatestRate.Valid {
		s := r.LatestRate.String
		out.Rate = &s
	}
	if r.LatestMicrorate.Valid {
		v := r.LatestMicrorate.Int64
		out.Microrate = &v
	}
	if r.LatestOccurred.Valid {
		t := r.LatestOccurred.Time
		out.OccurredAt = &t
	}
	// -1 sentinel means "no USD sales" (microrates are always > 0).
	if r.MinMicrorate > 0 {
		v := r.MinMicrorate
		out.MinMicrorate = &v
	}
	if r.MaxMicrorate > 0 {
		v := r.MaxMicrorate
		out.MaxMicrorate = &v
	}
	return out, nil
}

func (d Devices) DashboardBranches(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]dashboard.BranchRowRaw, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).DashboardBranches(ctx, sqlcgen.DashboardBranchesParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency,
	})
	if err != nil {
		return nil, reportErr("dashboard branches", err)
	}
	out := make([]dashboard.BranchRowRaw, 0, len(rows))
	for _, r := range rows {
		out = append(out, dashboard.BranchRowRaw{
			Channel:    r.Channel,
			ShopNameAR: r.ShopNameAr, ShopNameEN: r.ShopNameEn,
			ShopAddressAR: r.ShopAddressAr, ShopAddressEN: r.ShopAddressEn,
			ShopPhone:           r.ShopPhone,
			ShopReceiptFooterAR: r.ShopReceiptFooterAr, ShopReceiptFooterEN: r.ShopReceiptFooterEn,
			Currency: r.Currency, Transactions: r.Transactions, Units: r.Units,
			Subtotal: r.Subtotal, Discount: r.Discount, Tax: r.Tax, Total: r.SalesTotal,
		})
	}
	return out, nil
}

func (d Devices) DashboardProductsNormalized(ctx context.Context, startUTC, endUTC time.Time) ([]dashboard.NormalizedProductRowRaw, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).DashboardProductsNormalized(ctx, sqlcgen.DashboardProductsNormalizedParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC),
	})
	if err != nil {
		return nil, reportErr("dashboard products", err)
	}
	out := make([]dashboard.NormalizedProductRowRaw, 0, len(rows))
	for _, r := range rows {
		var pid *string
		if r.ProductID.Valid {
			s := uuidString(r.ProductID)
			pid = &s
		}
		out = append(out, dashboard.NormalizedProductRowRaw{
			ProductID: pid, SKU: r.Sku, ProductName: r.ProductName,
			Units: r.Units, Normalized: r.Normalized, MissingFx: r.MissingFx,
		})
	}
	return out, nil
}

func (d Devices) DashboardCategoriesNormalized(ctx context.Context, startUTC, endUTC time.Time, kind string) ([]dashboard.NormalizedCategoryRowRaw, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).DashboardCategoriesNormalized(ctx, sqlcgen.DashboardCategoriesNormalizedParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Kind: kind,
	})
	if err != nil {
		return nil, reportErr("dashboard categories", err)
	}
	out := make([]dashboard.NormalizedCategoryRowRaw, 0, len(rows))
	for _, r := range rows {
		out = append(out, dashboard.NormalizedCategoryRowRaw{
			Kind: r.Kind, ID: uuidString(r.ID), NameAR: r.NameAr, NameEN: r.NameEn,
			Units: r.Units, Normalized: r.Normalized, MissingFx: r.MissingFx,
		})
	}
	return out, nil
}

func (d Devices) DashboardRecentActivity(ctx context.Context, limit int) ([]dashboard.ActivityItem, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).DashboardRecentActivity(ctx, int32(limit))
	if err != nil {
		return nil, reportErr("dashboard activity", err)
	}
	out := make([]dashboard.ActivityItem, 0, len(rows))
	for _, r := range rows {
		item := dashboard.ActivityItem{
			Kind: r.Kind, EventID: uuidString(r.EventID), EventType: r.EventType,
			Timestamp: r.Ts.Time.Format(time.RFC3339),
		}
		if r.DeviceName.Valid {
			s := r.DeviceName.String
			item.DeviceName = &s
		}
		if r.Detail.Valid {
			s := r.Detail.String
			item.Detail = &s
		}
		out = append(out, item)
	}
	return out, nil
}

func (d Devices) DashboardLatestSales(ctx context.Context, limit int) ([]dashboard.LatestSale, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).DashboardLatestSales(ctx, int32(limit))
	if err != nil {
		return nil, reportErr("dashboard latest sales", err)
	}
	out := make([]dashboard.LatestSale, 0, len(rows))
	for _, r := range rows {
		item := dashboard.LatestSale{
			SaleID: uuidString(r.SaleID), SaleNumber: r.SaleNumber, Channel: r.Channel,
			OccurredAt: r.OccurredAt.Time.Format(time.RFC3339),
			Currency:   r.Currency, TotalMinor: pgIntToString(r.TotalMinor),
		}
		if r.CashierID.Valid {
			s := r.CashierID.String
			item.CashierID = &s
		}
		if r.CashierName.Valid {
			s := r.CashierName.String
			item.CashierName = &s
		}
		out = append(out, item)
	}
	return out, nil
}
