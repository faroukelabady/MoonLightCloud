package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
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
func (d Devices) SalesSummary(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.SummaryRow, error) {
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

func (d Devices) SalesPayments(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.PaymentRow, error) {
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

func (d Devices) SalesDaily(ctx context.Context, startUTC, endUTC time.Time, currency, timezone string) ([]report.DailyRowRaw, error) {
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

func (d Devices) SalesByProduct(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.ProductRow, error) {
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

func categoryRows(kind string, ctx context.Context, d Devices, startUTC, endUTC time.Time, currency string) ([]report.CategoryRow, error) {
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

func (d Devices) SalesByRootCategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.CategoryRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	return categoryRows("root", ctx, d, startUTC, endUTC, currency)
}

func (d Devices) SalesBySubcategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.CategoryRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	return categoryRows("subcategory", ctx, d, startUTC, endUTC, currency)
}

func (d Devices) SalesByCashier(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.CashierRow, error) {
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

func (d Devices) SalesByChannel(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.ChannelRow, error) {
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

func (d Devices) SalesProjectionFreshness(ctx context.Context) (report.FreshnessRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	r, err := sqlcgen.New(d.pool).ReportFreshness(ctx, sale.ProcessorSaleProjectionV1)
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
func (d Devices) RefundsSummary(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.RefundSummaryRow, error) {
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

func (d Devices) RefundsDaily(ctx context.Context, startUTC, endUTC time.Time, currency, timezone string) ([]report.RefundDailyRow, error) {
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

func (d Devices) RefundsByProduct(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.RefundProductRow, error) {
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

func refundCategoryRows(kind string, ctx context.Context, d Devices, startUTC, endUTC time.Time, currency string) ([]report.RefundCategoryRow, error) {
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

func (d Devices) RefundsByRootCategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.RefundCategoryRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	return refundCategoryRows("root", ctx, d, startUTC, endUTC, currency)
}

func (d Devices) RefundsBySubcategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.RefundCategoryRow, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	return refundCategoryRows("subcategory", ctx, d, startUTC, endUTC, currency)
}

func (d Devices) RefundsByCashier(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.RefundCashierRow, error) {
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

func (d Devices) RefundsByChannel(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]report.RefundChannelRow, error) {
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

func (d Devices) ReturnProjectionFreshness(ctx context.Context) (report.ReturnFreshnessRow, error) {
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
