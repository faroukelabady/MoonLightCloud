package postgres

import (
	"context"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
	"github.com/jackc/pgx/v5/pgtype"
)

func scopedStore(op, storeID string) (pgtype.UUID, error) {
	uid, err := parseUUID(storeID)
	if err != nil {
		return pgtype.UUID{}, reportErr(op, err)
	}
	return uid, nil
}

func (d Reporting) SalesSummaryForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]report.SummaryRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := scopedStore("sales summary for store", storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).ReportSalesSummaryForStore(ctx, sqlcgen.ReportSalesSummaryForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency, StoreID: uid,
	})
	if err != nil {
		return nil, reportErr("sales summary for store", err)
	}
	out := make([]report.SummaryRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, report.SummaryRow{
			Currency: r.Currency, Transactions: r.Transactions, Units: r.Units,
			Subtotal: r.Subtotal, Discount: r.Discount, Tax: r.Tax,
			SalesTotal: r.SalesTotal, LineCost: r.LineCost,
		})
	}
	return out, nil
}

func (d Reporting) SalesDailyForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency, timezone string) ([]report.DailyRowRaw, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := scopedStore("sales daily for store", storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).ReportSalesDailyForStore(ctx, sqlcgen.ReportSalesDailyForStoreParams{
		Timezone: timezone, StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency, StoreID: uid,
	})
	if err != nil {
		return nil, reportErr("sales daily for store", err)
	}
	out := make([]report.DailyRowRaw, 0, len(rows))
	for _, r := range rows {
		out = append(out, report.DailyRowRaw{
			Date: r.Day, Currency: r.Currency, Transactions: r.Transactions, Units: r.Units,
			Subtotal: r.Subtotal, Discount: r.Discount, Tax: r.Tax,
			SalesTotal: r.SalesTotal, LineCost: r.LineCost,
		})
	}
	return out, nil
}

func (d Reporting) SalesByProductForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]report.ProductRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := scopedStore("sales by product for store", storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).ReportSalesByProductForStore(ctx, sqlcgen.ReportSalesByProductForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency, StoreID: uid,
	})
	if err != nil {
		return nil, reportErr("sales by product for store", err)
	}
	out := make([]report.ProductRow, 0, len(rows))
	for _, r := range rows {
		var pid *string
		if r.ProductID.Valid {
			s := uuidString(r.ProductID)
			pid = &s
		}
		out = append(out, report.ProductRow{
			ProductID: pid, SKU: r.Sku, ProductName: r.ProductName, Units: r.Units,
			Currency: r.Currency, LineSales: r.LineSales, LineCost: r.LineCost,
		})
	}
	return out, nil
}

func scopedCategoryRows(kind, op, storeID string, ctx context.Context, d Reporting, startUTC, endUTC time.Time, currency string) ([]report.CategoryRow, error) {
	uid, err := scopedStore(op, storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).ReportSalesByCategoryForStore(ctx, sqlcgen.ReportSalesByCategoryForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Kind: kind, Currency: currency, StoreID: uid,
	})
	if err != nil {
		return nil, reportErr(op, err)
	}
	out := make([]report.CategoryRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, report.CategoryRow{
			Kind: r.Kind, ID: uuidString(r.ID), NameAR: r.NameAr, NameEN: r.NameEn,
			Units: r.Units, Currency: r.Currency, Sales: r.Sales, Cost: r.Cost,
		})
	}
	return out, nil
}

func (d Reporting) SalesByRootCategoryForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]report.CategoryRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	return scopedCategoryRows("root", "sales by category for store", storeID, ctx, d, startUTC, endUTC, currency)
}

func (d Reporting) SalesBySubcategoryForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]report.CategoryRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	return scopedCategoryRows("subcategory", "sales by category for store", storeID, ctx, d, startUTC, endUTC, currency)
}

func (d Reporting) SalesByTagForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]report.TagRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := scopedStore("sales by tag for store", storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).ReportSalesByTagForStore(ctx, sqlcgen.ReportSalesByTagForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency, StoreID: uid,
	})
	if err != nil {
		return nil, reportErr("sales by tag for store", err)
	}
	out := make([]report.TagRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, report.TagRow{
			ID: uuidString(r.ID), Slug: r.Slug, NameAR: r.NameAr, NameEN: r.NameEn,
			Units: r.Units, Currency: r.Currency, Sales: r.Sales, Cost: r.Cost,
		})
	}
	return out, nil
}

func (d Reporting) RefundsSummaryForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]report.RefundSummaryRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := scopedStore("refunds summary for store", storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).ReportRefundsSummaryForStore(ctx, sqlcgen.ReportRefundsSummaryForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency, StoreID: uid,
	})
	if err != nil {
		return nil, reportErr("refunds summary for store", err)
	}
	out := make([]report.RefundSummaryRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, report.RefundSummaryRow{
			Currency: r.Currency, Transactions: r.Transactions, Units: r.Units,
			GrossRefunded: r.GrossRefunded, DiscountRefunded: r.DiscountRefunded,
			TaxRefunded: r.TaxRefunded, RefundTotal: r.RefundTotal, ReturnedCost: r.ReturnedCost,
		})
	}
	return out, nil
}

func (d Reporting) RefundsDailyForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency, timezone string) ([]report.RefundDailyRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := scopedStore("refunds daily for store", storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).ReportRefundsDailyForStore(ctx, sqlcgen.ReportRefundsDailyForStoreParams{
		Timezone: timezone, StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency, StoreID: uid,
	})
	if err != nil {
		return nil, reportErr("refunds daily for store", err)
	}
	out := make([]report.RefundDailyRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, report.RefundDailyRow{
			Date: r.Day, Currency: r.Currency, Transactions: r.Transactions, Units: r.Units,
			RefundTotal: r.RefundTotal, ReturnedCost: r.ReturnedCost,
		})
	}
	return out, nil
}

func (d Reporting) RefundsByProductForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]report.RefundProductRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := scopedStore("refunds by product for store", storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).ReportRefundsByProductForStore(ctx, sqlcgen.ReportRefundsByProductForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency, StoreID: uid,
	})
	if err != nil {
		return nil, reportErr("refunds by product for store", err)
	}
	out := make([]report.RefundProductRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, report.RefundProductRow{
			ProductID: uuidPtr(r.ProductID), SKU: r.Sku, ProductName: r.ProductName,
			Units: r.Units, Currency: r.Currency, Refund: r.Refund, ReturnedCost: r.ReturnedCost,
		})
	}
	return out, nil
}

func (d Reporting) RefundsByProductTypeForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]report.RefundProductTypeRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := scopedStore("refunds by product type for store", storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).ReportRefundsByProductTypeForStore(ctx, sqlcgen.ReportRefundsByProductTypeForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency, StoreID: uid,
	})
	if err != nil {
		return nil, reportErr("refunds by product type for store", err)
	}
	out := make([]report.RefundProductTypeRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, report.RefundProductTypeRow{
			ProductTypeID: uuidPtr(r.ProductTypeID), ProductTypeCode: textPtr(r.ProductTypeCode),
			ProductTypeNameAR: textPtr(r.ProductTypeNameAr), ProductTypeNameEN: textPtr(r.ProductTypeNameEn),
			Units: r.Units, Currency: r.Currency, Refund: r.Refund, ReturnedCost: r.ReturnedCost,
		})
	}
	return out, nil
}

func scopedRefundCategoryRows(kind, op, storeID string, ctx context.Context, d Reporting, startUTC, endUTC time.Time, currency string) ([]report.RefundCategoryRow, error) {
	uid, err := scopedStore(op, storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).ReportRefundsByCategoryForStore(ctx, sqlcgen.ReportRefundsByCategoryForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Kind: kind, Currency: currency, StoreID: uid,
	})
	if err != nil {
		return nil, reportErr(op, err)
	}
	out := make([]report.RefundCategoryRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, report.RefundCategoryRow{
			Kind: r.Kind, ID: uuidString(r.ID), NameAR: r.NameAr, NameEN: r.NameEn,
			Units: r.Units, Currency: r.Currency, Refund: r.Refund, ReturnedCost: r.ReturnedCost,
		})
	}
	return out, nil
}

func (d Reporting) RefundsByRootCategoryForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]report.RefundCategoryRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	return scopedRefundCategoryRows("root", "refunds by category for store", storeID, ctx, d, startUTC, endUTC, currency)
}

func (d Reporting) RefundsBySubcategoryForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]report.RefundCategoryRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	return scopedRefundCategoryRows("subcategory", "refunds by category for store", storeID, ctx, d, startUTC, endUTC, currency)
}

func (d Reporting) RefundsByTagForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]report.RefundTagRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := scopedStore("refunds by tag for store", storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).ReportRefundsByTagForStore(ctx, sqlcgen.ReportRefundsByTagForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency, StoreID: uid,
	})
	if err != nil {
		return nil, reportErr("refunds by tag for store", err)
	}
	out := make([]report.RefundTagRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, report.RefundTagRow{
			ID: uuidString(r.ID), Slug: r.Slug, NameAR: r.NameAr, NameEN: r.NameEn,
			Units: r.Units, Currency: r.Currency, Refund: r.Refund, ReturnedCost: r.ReturnedCost,
		})
	}
	return out, nil
}

func (d Reporting) SalesPaymentsForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]report.PaymentRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := scopedStore("sales payments for store", storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).ReportSalesPaymentsForStore(ctx, sqlcgen.ReportSalesPaymentsForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency, StoreID: uid,
	})
	if err != nil {
		return nil, reportErr("sales payments for store", err)
	}
	out := make([]report.PaymentRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, report.PaymentRow{
			Method: r.Method, Currency: r.Currency, Amount: r.Amount, Change: r.Change,
		})
	}
	return out, nil
}

func (d Reporting) SalesByCashierForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]report.CashierRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := scopedStore("sales by cashier for store", storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).ReportSalesByCashierForStore(ctx, sqlcgen.ReportSalesByCashierForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency, StoreID: uid,
	})
	if err != nil {
		return nil, reportErr("sales by cashier for store", err)
	}
	out := make([]report.CashierRow, 0, len(rows))
	for _, r := range rows {
		var id, name *string
		if r.CashierID.Valid {
			s := r.CashierID.String
			id = &s
		}
		if r.CashierName.Valid {
			s := r.CashierName.String
			name = &s
		}
		out = append(out, report.CashierRow{
			CashierID: id, CashierName: name, Units: r.Units, Transactions: r.Transactions,
			Currency: r.Currency, Subtotal: r.Subtotal, Discount: r.Discount,
			Tax: r.Tax, SalesTotal: r.SalesTotal,
		})
	}
	return out, nil
}

func (d Reporting) SalesByChannelForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]report.ChannelRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := scopedStore("sales by channel for store", storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).ReportSalesByChannelForStore(ctx, sqlcgen.ReportSalesByChannelForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency, StoreID: uid,
	})
	if err != nil {
		return nil, reportErr("sales by channel for store", err)
	}
	out := make([]report.ChannelRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, report.ChannelRow{
			Channel: r.Channel, Transactions: r.Transactions, Units: r.Units,
			Currency: r.Currency, Subtotal: r.Subtotal, Discount: r.Discount,
			Tax: r.Tax, SalesTotal: r.SalesTotal,
		})
	}
	return out, nil
}

func (d Reporting) RefundsByCashierForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]report.RefundCashierRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := scopedStore("refunds by cashier for store", storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).ReportRefundsByCashierForStore(ctx, sqlcgen.ReportRefundsByCashierForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency, StoreID: uid,
	})
	if err != nil {
		return nil, reportErr("refunds by cashier for store", err)
	}
	out := make([]report.RefundCashierRow, 0, len(rows))
	for _, r := range rows {
		var id, name *string
		if r.CashierID.Valid {
			s := r.CashierID.String
			id = &s
		}
		if r.CashierName.Valid {
			s := r.CashierName.String
			name = &s
		}
		out = append(out, report.RefundCashierRow{
			CashierID: id, CashierName: name, Units: r.Units,
			Currency: r.Currency, Transactions: r.Transactions, RefundTotal: r.RefundTotal,
		})
	}
	return out, nil
}

func (d Reporting) RefundsByChannelForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]report.RefundChannelRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := scopedStore("refunds by channel for store", storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).ReportRefundsByChannelForStore(ctx, sqlcgen.ReportRefundsByChannelForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency, StoreID: uid,
	})
	if err != nil {
		return nil, reportErr("refunds by channel for store", err)
	}
	out := make([]report.RefundChannelRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, report.RefundChannelRow{
			Channel: r.Channel, Transactions: r.Transactions, Units: r.Units,
			Currency: r.Currency, RefundTotal: r.RefundTotal,
		})
	}
	return out, nil
}

// SalesByVariantForStore is SalesByVariant restricted to one proven
// Store (Phase 9B scope semantics).
func (d Reporting) SalesByVariantForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]report.VariantRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := scopedStore("sales by variant for store", storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).ReportSalesByVariantForStore(ctx, sqlcgen.ReportSalesByVariantForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency, StoreID: uid,
	})
	if err != nil {
		return nil, reportErr("sales by variant for store", err)
	}
	out := make([]report.VariantRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, variantReportToDomain(variantReportRow{
			VariantID: r.VariantID, VariantSku: r.VariantSku,
			VariantAttributes: r.VariantAttributes, ProductID: r.ProductID, Sku: r.Sku,
			ProductName: r.ProductName, Currency: r.Currency,
			Units: r.Units, LineSales: r.LineSales, LineCost: r.LineCost,
		}))
	}
	return out, nil
}

// SalesByProductTypeForStore is SalesByProductType restricted to one Store.
func (d Reporting) SalesByProductTypeForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]report.ProductTypeRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := scopedStore("sales by product type for store", storeID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).ReportSalesByProductTypeForStore(ctx, sqlcgen.ReportSalesByProductTypeForStoreParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency, StoreID: uid,
	})
	if err != nil {
		return nil, reportErr("sales by product type for store", err)
	}
	out := make([]report.ProductTypeRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, report.ProductTypeRow{
			ProductTypeID: uuidPtr(r.ProductTypeID), ProductTypeCode: textPtr(r.ProductTypeCode),
			ProductTypeNameAR: textPtr(r.ProductTypeNameAr), ProductTypeNameEN: textPtr(r.ProductTypeNameEn),
			Units: r.Units, Currency: r.Currency, LineSales: r.LineSales, LineCost: r.LineCost,
		})
	}
	return out, nil
}
