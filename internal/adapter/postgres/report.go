package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// reportErr classifies reporting storage failures: connection-level and
// cancellation/timeout failures are transient (503, retryable); everything
// else (overflow casts, unexpected planner errors) is a 500. Driver
// internals never cross the boundary (see redact).
func reportErr(op string, err error) error {
	var ce *pgconn.ConnectError
	if errors.As(err, &ce) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return apperr.Wrap(apperr.Unavailable, op+" temporarily unavailable", redact(err))
	}
	return apperr.Wrap(apperr.Internal, op, redact(err))
}

// Reporting implements report.Repository with static aggregate queries.
// Currency ” means all currencies (rows stay bucketed; never summed).
func (d Reporting) SalesSummary(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.SummaryRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ReportSalesSummary(ctx, sqlcgen.ReportSalesSummaryParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency,
	})
	if err != nil {
		return nil, reportErr("sales summary", err)
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

func (d Reporting) SalesPayments(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.PaymentRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ReportSalesPayments(ctx, sqlcgen.ReportSalesPaymentsParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency,
	})
	if err != nil {
		return nil, reportErr("sales payments", err)
	}
	out := make([]report.PaymentRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, report.PaymentRow{
			Method: r.Method, Currency: r.Currency, Amount: r.Amount, Change: r.Change,
		})
	}
	return out, nil
}

func (d Reporting) SalesDaily(ctx context.Context, startUTC, endUTC time.Time, currency, timezone string) ([]report.DailyRowRaw, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ReportSalesDaily(ctx, sqlcgen.ReportSalesDailyParams{
		Timezone: timezone, StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency,
	})
	if err != nil {
		return nil, reportErr("sales daily", err)
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

func (d Reporting) SalesByProduct(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.ProductRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ReportSalesByProduct(ctx, sqlcgen.ReportSalesByProductParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency,
	})
	if err != nil {
		return nil, reportErr("sales by product", err)
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

func categoryRows(kind string, ctx context.Context, d Reporting, startUTC, endUTC time.Time, currency string) ([]report.CategoryRow, error) {
	rows, err := sqlcgen.New(d.pool).ReportSalesByCategory(ctx, sqlcgen.ReportSalesByCategoryParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Kind: kind, Currency: currency,
	})
	if err != nil {
		return nil, reportErr("sales by category", err)
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

func (d Reporting) SalesByRootCategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.CategoryRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	return categoryRows("root", ctx, d, startUTC, endUTC, currency)
}

func (d Reporting) SalesBySubcategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.CategoryRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	return categoryRows("subcategory", ctx, d, startUTC, endUTC, currency)
}

func (d Reporting) SalesByTag(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.TagRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ReportSalesByTag(ctx, sqlcgen.ReportSalesByTagParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency,
	})
	if err != nil {
		return nil, reportErr("sales by tag", err)
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

func (d Reporting) RefundsByTag(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.RefundTagRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ReportRefundsByTag(ctx, sqlcgen.ReportRefundsByTagParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency,
	})
	if err != nil {
		return nil, reportErr("refunds by tag", err)
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

func (d Reporting) SalesByCashier(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.CashierRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ReportSalesByCashier(ctx, sqlcgen.ReportSalesByCashierParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency,
	})
	if err != nil {
		return nil, reportErr("sales by cashier", err)
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

func (d Reporting) SalesByChannel(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.ChannelRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ReportSalesByChannel(ctx, sqlcgen.ReportSalesByChannelParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency,
	})
	if err != nil {
		return nil, reportErr("sales by channel", err)
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

func (d Reporting) SalesProjectionFreshness(ctx context.Context) (report.FreshnessRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	r, err := sqlcgen.New(d.pool).ReportFreshness(ctx)
	if err != nil {
		return report.FreshnessRow{}, reportErr("projection freshness", err)
	}
	out := report.FreshnessRow{Backlog: r.Backlog, Blocked: r.Blocked}
	if r.LatestReceived.Valid {
		t := r.LatestReceived.Time
		out.LatestReceivedAt = &t
	}
	if r.LatestOccurred.Valid {
		t := r.LatestOccurred.Time
		out.LatestOccurredAt = &t
	}
	return out, nil
}

func uuidPtr(u pgtype.UUID) *string {
	if !u.Valid {
		return nil
	}
	s := uuidString(u)
	return &s
}

func textPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	s := t.String
	return &s
}

// Refunds reporting implements the additive reversal side of
// report.Repository. Windows run on return occurred_at; native EGP/USD
// buckets stay separate; overflow fails via the numeric casts.
func (d Reporting) RefundsSummary(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.RefundSummaryRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ReportRefundsSummary(ctx, sqlcgen.ReportRefundsSummaryParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency,
	})
	if err != nil {
		return nil, reportErr("refunds summary", err)
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

func (d Reporting) RefundsDaily(ctx context.Context, startUTC, endUTC time.Time, currency, timezone string) ([]report.RefundDailyRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ReportRefundsDaily(ctx, sqlcgen.ReportRefundsDailyParams{
		Timezone: timezone, StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency,
	})
	if err != nil {
		return nil, reportErr("refunds daily", err)
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

func (d Reporting) RefundsByProduct(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.RefundProductRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ReportRefundsByProduct(ctx, sqlcgen.ReportRefundsByProductParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency,
	})
	if err != nil {
		return nil, reportErr("refunds by product", err)
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

func (d Reporting) RefundsByProductType(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.RefundProductTypeRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ReportRefundsByProductType(ctx, sqlcgen.ReportRefundsByProductTypeParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency,
	})
	if err != nil {
		return nil, reportErr("refunds by product type", err)
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

func refundCategoryRows(kind string, ctx context.Context, d Reporting, startUTC, endUTC time.Time, currency string) ([]report.RefundCategoryRow, error) {
	rows, err := sqlcgen.New(d.pool).ReportRefundsByCategory(ctx, sqlcgen.ReportRefundsByCategoryParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Kind: kind, Currency: currency,
	})
	if err != nil {
		return nil, reportErr("refunds by category", err)
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

func (d Reporting) RefundsByRootCategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.RefundCategoryRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	return refundCategoryRows("root", ctx, d, startUTC, endUTC, currency)
}

func (d Reporting) RefundsBySubcategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.RefundCategoryRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	return refundCategoryRows("subcategory", ctx, d, startUTC, endUTC, currency)
}

func (d Reporting) RefundsByCashier(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.RefundCashierRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ReportRefundsByCashier(ctx, sqlcgen.ReportRefundsByCashierParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency,
	})
	if err != nil {
		return nil, reportErr("refunds by cashier", err)
	}
	out := make([]report.RefundCashierRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, report.RefundCashierRow{
			CashierID: textPtr(r.CashierID), CashierName: textPtr(r.CashierName),
			Transactions: r.Transactions, Units: r.Units,
			Currency: r.Currency, RefundTotal: r.RefundTotal,
		})
	}
	return out, nil
}

func (d Reporting) RefundsByChannel(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.RefundChannelRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ReportRefundsByChannel(ctx, sqlcgen.ReportRefundsByChannelParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency,
	})
	if err != nil {
		return nil, reportErr("refunds by channel", err)
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

func (d Reporting) ReturnProjectionFreshness(ctx context.Context) (report.ReturnFreshnessRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	r, err := sqlcgen.New(d.pool).ReportReturnFreshness(ctx)
	if err != nil {
		return report.ReturnFreshnessRow{}, reportErr("return projection freshness", err)
	}
	out := report.ReturnFreshnessRow{Backlog: r.Backlog, Blocked: r.Blocked}
	if r.LatestReceived.Valid {
		t := r.LatestReceived.Time
		out.LatestReceivedAt = &t
	}
	if r.LatestOccurred.Valid {
		t := r.LatestOccurred.Time
		out.LatestOccurredAt = &t
	}
	return out, nil
}

// Phase 9B store-scoped read isolation. Same frozen row shapes restricted
// to one proven Store. StoreID must be a valid Store UUID; legacy NULL
// rows never match. No service/HTTP change: internal until 9D.

// ---- Phase 17 optional variant breakdown ----

// variantReportRow flattens the identical variant breakdown row shape
// both queries return. VariantSku/VariantAttributes are the frozen
// sale-time snapshot columns (00036) — never joined from catalog.
type variantReportRow struct {
	VariantID         pgtype.UUID
	VariantSku        pgtype.Text
	VariantAttributes []byte
	ProductID         pgtype.UUID
	Sku               string
	ProductName       string
	Currency          string
	Units             int64
	LineSales         int64
	LineCost          int64
}

func variantReportToDomain(r variantReportRow) report.VariantRow {
	var vid, pid, vsku *string
	if r.VariantID.Valid {
		s := uuidString(r.VariantID)
		vid = &s
	}
	if r.ProductID.Valid {
		s := uuidString(r.ProductID)
		pid = &s
	}
	if r.VariantSku.Valid {
		s := r.VariantSku.String
		vsku = &s
	}
	var attrs []report.VariantAttributeSnapshot
	if len(r.VariantAttributes) > 0 {
		if err := json.Unmarshal(r.VariantAttributes, &attrs); err != nil {
			// Stored JSONB is projection-owned and validated at ingest;
			// a parse failure here is an internal invariant breach, not
			// caller input. Surface no labels, never a crash.
			attrs = nil
		}
	}
	return report.VariantRow{
		VariantID: vid, VariantSKU: vsku, VariantAttributes: attrs,
		ProductID: pid, SKU: r.Sku, ProductName: r.ProductName,
		Units: r.Units, Currency: r.Currency, LineSales: r.LineSales, LineCost: r.LineCost,
	}
}

// SalesByVariant groups sale lines by their FROZEN per-line variant
// identity/labels (Phase 17 + 17-R0). History never joins the
// current-state variant projection: a later catalog label rename can
// never move these rows.
func (d Reporting) SalesByVariant(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.VariantRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ReportSalesByVariant(ctx, sqlcgen.ReportSalesByVariantParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency,
	})
	if err != nil {
		return nil, reportErr("sales by variant", err)
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

// SalesByProductType groups sale lines by their FROZEN per-line type
// snapshot (Phase 17-R2, 00038). History never joins the current-state
// type projection: renames/reassignments never move rows.
func (d Reporting) SalesByProductType(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.ProductTypeRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ReportSalesByProductType(ctx, sqlcgen.ReportSalesByProductTypeParams{
		StartUtc: pgTime(startUTC), EndUtc: pgTime(endUTC), Currency: currency,
	})
	if err != nil {
		return nil, reportErr("sales by product type", err)
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
