package postgres

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
)

// pgIntToString renders exact minor units for the BFF string-money
// contract (never floats, never JS arithmetic inputs).
func pgIntToString(v int64) string { return strconv.FormatInt(v, 10) }

// Dashboard repository implementation on Devices (shared pool + timeouts).

func (d Reporting) DashboardNormalizedSummary(ctx context.Context, startUTC, endUTC time.Time) (dashboard.NormalizedSummaryRow, error) {
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

func (d Reporting) DashboardNormalizedSummaryForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time) (dashboard.NormalizedSummaryRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := scopedStoreUUID(storeID)
	if err != nil {
		return dashboard.NormalizedSummaryRow{}, err
	}
	r, err := sqlcgen.New(d.pool).DashboardNormalizedSummaryForStore(ctx, sqlcgen.DashboardNormalizedSummaryForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), StoreID: suid,
	})
	if err != nil {
		return dashboard.NormalizedSummaryRow{}, reportErr("dashboard normalized summary", err)
	}
	return dashboard.NormalizedSummaryRow{
		Transactions: r.Transactions, Units: r.Units, Normalized: r.NormalizedTotal,
		USDSales: r.UsdSales, USDMissingFx: r.UsdMissingFx,
	}, nil
}

func (d Reporting) DashboardNormalizedDaily(ctx context.Context, startUTC, endUTC time.Time, timezone string) ([]dashboard.NormalizedDailyRow, error) {
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
			Date: r.Day, Transactions: r.Transactions, Units: r.Units,
			Normalized: r.NormalizedTotal, USDMissingFx: r.UsdMissingFx,
		})
	}
	return out, nil
}

func (d Reporting) DashboardNormalizedDailyForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, timezone string) ([]dashboard.NormalizedDailyRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := scopedStoreUUID(storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).DashboardNormalizedDailyForStore(ctx, sqlcgen.DashboardNormalizedDailyForStoreParams{
		Timezone: timezone, StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), StoreID: suid,
	})
	if err != nil {
		return nil, reportErr("dashboard normalized daily", err)
	}
	out := make([]dashboard.NormalizedDailyRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, dashboard.NormalizedDailyRow{
			Date: r.Day, Transactions: r.Transactions, Units: r.Units,
			Normalized: r.NormalizedTotal, USDMissingFx: r.UsdMissingFx,
		})
	}
	return out, nil
}

func (d Reporting) DashboardLatestFx(ctx context.Context, startUTC, endUTC time.Time) (dashboard.LatestFxRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	r, err := sqlcgen.New(d.pool).DashboardLatestFx(ctx, sqlcgen.DashboardLatestFxParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC),
	})
	if err != nil {
		return dashboard.LatestFxRow{}, reportErr("dashboard latest fx", err)
	}
	out := dashboard.LatestFxRow{DistinctRates: r.DistinctRates, USDSales: r.UsdSales}
	if r.LatestMicrorate.Valid {
		v := r.LatestMicrorate.Int64
		out.Microrate = &v
		// The decimal text is derived from the exact microrate (ADR-0055
		// dropped the duplicate fx_rate column); it matches the validated
		// event's fx.rate spelling.
		s := formatMicrorate(v)
		out.Rate = &s
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

func (d Reporting) DashboardLatestFxForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time) (dashboard.LatestFxRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := scopedStoreUUID(storeID)
	if err != nil {
		return dashboard.LatestFxRow{}, err
	}
	r, err := sqlcgen.New(d.pool).DashboardLatestFxForStore(ctx, sqlcgen.DashboardLatestFxForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), StoreID: suid,
	})
	if err != nil {
		return dashboard.LatestFxRow{}, reportErr("dashboard latest fx", err)
	}
	out := dashboard.LatestFxRow{DistinctRates: r.DistinctRates, USDSales: r.UsdSales}
	if r.LatestMicrorate.Valid {
		v := r.LatestMicrorate.Int64
		out.Microrate = &v
		// The decimal text is derived from the exact microrate (ADR-0055
		// dropped the duplicate fx_rate column); it matches the validated
		// event's fx.rate spelling.
		s := formatMicrorate(v)
		out.Rate = &s
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

func (d Reporting) DashboardBranches(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]dashboard.BranchRowRaw, error) {
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

func (d Reporting) DashboardBranchesForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]dashboard.BranchRowRaw, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := scopedStoreUUID(storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).DashboardBranchesForStore(ctx, sqlcgen.DashboardBranchesForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency, StoreID: suid,
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

func (d Reporting) DashboardProductsNormalized(ctx context.Context, startUTC, endUTC time.Time) ([]dashboard.NormalizedProductRowRaw, error) {
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
		var sid *string
		if r.StoreID.Valid {
			v := uuidString(r.StoreID)
			sid = &v
		}
		out = append(out, dashboard.NormalizedProductRowRaw{
			ProductID: pid, SKU: r.Sku, ProductName: r.ProductName,
			Units: r.Units, Normalized: r.Normalized, MissingFx: r.MissingFx,
			StoreID: sid,
		})
	}
	return out, nil
}

func (d Reporting) DashboardProductsNormalizedForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time) ([]dashboard.NormalizedProductRowRaw, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := scopedStoreUUID(storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).DashboardProductsNormalizedForStore(ctx, sqlcgen.DashboardProductsNormalizedForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), StoreID: suid,
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
		var sid *string
		if r.StoreID.Valid {
			v := uuidString(r.StoreID)
			sid = &v
		}
		out = append(out, dashboard.NormalizedProductRowRaw{
			ProductID: pid, SKU: r.Sku, ProductName: r.ProductName,
			Units: r.Units, Normalized: r.Normalized, MissingFx: r.MissingFx,
			StoreID: sid,
		})
	}
	return out, nil
}

func (d Reporting) DashboardCategoriesNormalized(ctx context.Context, startUTC, endUTC time.Time, kind string) ([]dashboard.NormalizedCategoryRowRaw, error) {
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

func (d Reporting) DashboardCategoriesNormalizedForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, kind string) ([]dashboard.NormalizedCategoryRowRaw, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := scopedStoreUUID(storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).DashboardCategoriesNormalizedForStore(ctx, sqlcgen.DashboardCategoriesNormalizedForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Kind: kind, StoreID: suid,
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

func (d Reporting) DashboardRecentActivity(ctx context.Context, limit int) ([]dashboard.ActivityItem, error) {
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

func (d Reporting) DashboardRecentActivityForStore(ctx context.Context, storeID string, limit int) ([]dashboard.ActivityItem, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := scopedStoreUUID(storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).DashboardRecentActivityForStore(ctx, sqlcgen.DashboardRecentActivityForStoreParams{
		StoreID: suid, LimitN: int32(limit),
	})
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

func (d Reporting) DashboardLatestSales(ctx context.Context, limit int) ([]dashboard.LatestSale, error) {
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

func (d Reporting) DashboardLatestSalesForStore(ctx context.Context, storeID string, limit int) ([]dashboard.LatestSale, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := scopedStoreUUID(storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).DashboardLatestSalesForStore(ctx, sqlcgen.DashboardLatestSalesForStoreParams{
		StoreID: suid, LimitN: int32(limit),
	})
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

func (d Reporting) DashboardNormalizedRefundsSummary(ctx context.Context, startUTC, endUTC time.Time) (dashboard.NormalizedRefundSummaryRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	r, err := sqlcgen.New(d.pool).DashboardNormalizedRefundsSummary(ctx, sqlcgen.DashboardNormalizedRefundsSummaryParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC),
	})
	if err != nil {
		return dashboard.NormalizedRefundSummaryRow{}, reportErr("normalized refunds summary", err)
	}
	return dashboard.NormalizedRefundSummaryRow{
		Transactions: r.Transactions, Units: r.Units, Normalized: r.NormalizedRefund,
		USDReturns: r.UsdReturns, USDMissingFx: r.UsdMissingFx,
	}, nil
}

func (d Reporting) DashboardNormalizedRefundsSummaryForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time) (dashboard.NormalizedRefundSummaryRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := scopedStoreUUID(storeID)
	if err != nil {
		return dashboard.NormalizedRefundSummaryRow{}, err
	}
	r, err := sqlcgen.New(d.pool).DashboardNormalizedRefundsSummaryForStore(ctx, sqlcgen.DashboardNormalizedRefundsSummaryForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), StoreID: suid,
	})
	if err != nil {
		return dashboard.NormalizedRefundSummaryRow{}, reportErr("normalized refunds summary", err)
	}
	return dashboard.NormalizedRefundSummaryRow{
		Transactions: r.Transactions, Units: r.Units, Normalized: r.NormalizedRefund,
		USDReturns: r.UsdReturns, USDMissingFx: r.UsdMissingFx,
	}, nil
}

func (d Reporting) DashboardNormalizedRefundsDaily(ctx context.Context, startUTC, endUTC time.Time, timezone string) ([]dashboard.NormalizedRefundDailyRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).DashboardNormalizedRefundsDaily(ctx, sqlcgen.DashboardNormalizedRefundsDailyParams{
		Timezone: timezone, StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC),
	})
	if err != nil {
		return nil, reportErr("normalized refunds daily", err)
	}
	out := make([]dashboard.NormalizedRefundDailyRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, dashboard.NormalizedRefundDailyRow{
			Date: r.Day, Transactions: r.Transactions, Units: r.Units,
			Normalized: r.NormalizedRefund, USDMissingFx: r.UsdMissingFx,
		})
	}
	return out, nil
}

func (d Reporting) DashboardNormalizedRefundsDailyForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, timezone string) ([]dashboard.NormalizedRefundDailyRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := scopedStoreUUID(storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).DashboardNormalizedRefundsDailyForStore(ctx, sqlcgen.DashboardNormalizedRefundsDailyForStoreParams{
		Timezone: timezone, StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), StoreID: suid,
	})
	if err != nil {
		return nil, reportErr("normalized refunds daily", err)
	}
	out := make([]dashboard.NormalizedRefundDailyRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, dashboard.NormalizedRefundDailyRow{
			Date: r.Day, Transactions: r.Transactions, Units: r.Units,
			Normalized: r.NormalizedRefund, USDMissingFx: r.UsdMissingFx,
		})
	}
	return out, nil
}

func (d Reporting) DashboardProductsNormalizedRefunds(ctx context.Context, startUTC, endUTC time.Time) ([]dashboard.NormalizedRefundProductRowRaw, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).DashboardProductsNormalizedRefunds(ctx, sqlcgen.DashboardProductsNormalizedRefundsParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC),
	})
	if err != nil {
		return nil, reportErr("normalized product refunds", err)
	}
	out := make([]dashboard.NormalizedRefundProductRowRaw, 0, len(rows))
	for _, r := range rows {
		var sid *string
		if r.StoreID.Valid {
			v := uuidString(r.StoreID)
			sid = &v
		}
		out = append(out, dashboard.NormalizedRefundProductRowRaw{
			ProductID: uuidPtr(r.ProductID), SKU: r.Sku, ProductName: r.ProductName,
			Units: r.Units, Normalized: r.NormalizedRefund, MissingFx: r.MissingFx,
			StoreID: sid,
		})
	}
	return out, nil
}

func (d Reporting) DashboardProductsNormalizedRefundsForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time) ([]dashboard.NormalizedRefundProductRowRaw, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := scopedStoreUUID(storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).DashboardProductsNormalizedRefundsForStore(ctx, sqlcgen.DashboardProductsNormalizedRefundsForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), StoreID: suid,
	})
	if err != nil {
		return nil, reportErr("normalized product refunds", err)
	}
	out := make([]dashboard.NormalizedRefundProductRowRaw, 0, len(rows))
	for _, r := range rows {
		var sid *string
		if r.StoreID.Valid {
			v := uuidString(r.StoreID)
			sid = &v
		}
		out = append(out, dashboard.NormalizedRefundProductRowRaw{
			ProductID: uuidPtr(r.ProductID), SKU: r.Sku, ProductName: r.ProductName,
			Units: r.Units, Normalized: r.NormalizedRefund, MissingFx: r.MissingFx,
			StoreID: sid,
		})
	}
	return out, nil
}

func (d Reporting) DashboardCategoriesNormalizedRefunds(ctx context.Context, startUTC, endUTC time.Time, kind string) ([]dashboard.NormalizedRefundCategoryRowRaw, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).DashboardCategoriesNormalizedRefunds(ctx, sqlcgen.DashboardCategoriesNormalizedRefundsParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Kind: kind,
	})
	if err != nil {
		return nil, reportErr("normalized category refunds", err)
	}
	out := make([]dashboard.NormalizedRefundCategoryRowRaw, 0, len(rows))
	for _, r := range rows {
		out = append(out, dashboard.NormalizedRefundCategoryRowRaw{
			Kind: r.Kind, ID: uuidString(r.ID), NameAR: r.NameAr, NameEN: r.NameEn,
			Units: r.Units, Normalized: r.NormalizedRefund, MissingFx: r.MissingFx,
		})
	}
	return out, nil
}

func (d Reporting) DashboardCategoriesNormalizedRefundsForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, kind string) ([]dashboard.NormalizedRefundCategoryRowRaw, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := scopedStoreUUID(storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).DashboardCategoriesNormalizedRefundsForStore(ctx, sqlcgen.DashboardCategoriesNormalizedRefundsForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Kind: kind, StoreID: suid,
	})
	if err != nil {
		return nil, reportErr("normalized category refunds", err)
	}
	out := make([]dashboard.NormalizedRefundCategoryRowRaw, 0, len(rows))
	for _, r := range rows {
		out = append(out, dashboard.NormalizedRefundCategoryRowRaw{
			Kind: r.Kind, ID: uuidString(r.ID), NameAR: r.NameAr, NameEN: r.NameEn,
			Units: r.Units, Normalized: r.NormalizedRefund, MissingFx: r.MissingFx,
		})
	}
	return out, nil
}

func (d Reporting) DashboardReturnBranches(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]dashboard.ReturnBranchRowRaw, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).DashboardReturnBranches(ctx, sqlcgen.DashboardReturnBranchesParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency,
	})
	if err != nil {
		return nil, reportErr("return branches", err)
	}
	out := make([]dashboard.ReturnBranchRowRaw, 0, len(rows))
	for _, r := range rows {
		out = append(out, dashboard.ReturnBranchRowRaw{
			Channel:    r.Channel,
			ShopNameAR: r.ShopNameAr, ShopNameEN: r.ShopNameEn,
			ShopAddressAR: r.ShopAddressAr, ShopAddressEN: r.ShopAddressEn,
			ShopPhone:           r.ShopPhone,
			ShopReceiptFooterAR: r.ShopReceiptFooterAr, ShopReceiptFooterEN: r.ShopReceiptFooterEn,
			Currency: r.Currency, Transactions: r.Transactions, Units: r.Units,
			RefundTotal: r.RefundTotal, ReturnedCost: r.ReturnedCost,
		})
	}
	return out, nil
}

func (d Reporting) DashboardReturnBranchesForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]dashboard.ReturnBranchRowRaw, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := scopedStoreUUID(storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).DashboardReturnBranchesForStore(ctx, sqlcgen.DashboardReturnBranchesForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency, StoreID: suid,
	})
	if err != nil {
		return nil, reportErr("return branches", err)
	}
	out := make([]dashboard.ReturnBranchRowRaw, 0, len(rows))
	for _, r := range rows {
		out = append(out, dashboard.ReturnBranchRowRaw{
			Channel:    r.Channel,
			ShopNameAR: r.ShopNameAr, ShopNameEN: r.ShopNameEn,
			ShopAddressAR: r.ShopAddressAr, ShopAddressEN: r.ShopAddressEn,
			ShopPhone:           r.ShopPhone,
			ShopReceiptFooterAR: r.ShopReceiptFooterAr, ShopReceiptFooterEN: r.ShopReceiptFooterEn,
			Currency: r.Currency, Transactions: r.Transactions, Units: r.Units,
			RefundTotal: r.RefundTotal, ReturnedCost: r.ReturnedCost,
		})
	}
	return out, nil
}

// formatMicrorate renders an exact microrate as the six-decimal rate text
// sale events carry (52000000 → "52.000000").
func formatMicrorate(v int64) string {
	return fmt.Sprintf("%d.%06d", v/1_000_000, v%1_000_000)
}
