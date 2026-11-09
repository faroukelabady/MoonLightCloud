package postgres

import (
	"context"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/salesreport"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var _ salesreport.Repository = Reporting{}

// printableScope maps "" (every Store) to SQL NULL and a Store ID to its UUID.
func printableScope(storeID string) (pgtype.UUID, error) {
	if storeID == "" {
		return pgtype.UUID{}, nil
	}
	return scopedStoreUUID(storeID)
}

func int8Ptr(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	n := v.Int64
	return &n
}

// SalesReportFacts reads every fact of one sales report in a single
// read-only snapshot, so the sections always agree with each other.
func (d Reporting) SalesReportFacts(ctx context.Context, storeID string, startUTC, endUTC, asOf time.Time, withUSDRates bool) (salesreport.Facts, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	store, err := printableScope(storeID)
	if err != nil {
		return salesreport.Facts{}, err
	}
	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return salesreport.Facts{}, reportErr("sales report", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	start, end := pgTime(startUTC), pgTime(endUTC)
	var f salesreport.Facts

	sales, err := q.PrintableReportSales(ctx, sqlcgen.PrintableReportSalesParams{StartUtc: start, EndUtc: end, WithUsdRates: withUSDRates, StoreID: store})
	if err != nil {
		return f, reportErr("sales report sales", err)
	}
	for _, r := range sales {
		f.Sales = append(f.Sales, salesreport.Sale{
			ID: uuidString(r.SaleID), Number: r.SaleNumber, OccurredAt: r.OccurredAt.Time, Currency: r.Currency,
			Subtotal: r.SubtotalMinor, Discount: r.DiscountMinor, Tax: r.TaxMinor, Total: r.TotalMinor,
			FxMicrorate: r.FxRateMicrorate.Int64, InPeriod: r.InPeriod, USDRateMicrorate: r.UsdRateMicrorate,
		})
	}
	latest, err := q.PrintableReportLatestUSDRate(ctx, sqlcgen.PrintableReportLatestUSDRateParams{AsOf: pgTime(asOf), StoreID: store})
	if err != nil {
		return f, reportErr("sales report usd rate", err)
	}
	if len(latest) == 1 {
		f.LatestUSDRate = &salesreport.Rate{Microrate: latest[0].RateMicrorate, EffectiveAt: latest[0].EffectiveAt.Time, Source: latest[0].Source}
	}
	lines, err := q.PrintableReportSaleLines(ctx, sqlcgen.PrintableReportSaleLinesParams{StartUtc: start, EndUtc: end, StoreID: store})
	if err != nil {
		return f, reportErr("sales report lines", err)
	}
	for _, r := range lines {
		product := ""
		if r.ProductID.Valid {
			product = uuidString(r.ProductID)
		}
		f.Lines = append(f.Lines, salesreport.Line{
			SaleID: uuidString(r.SaleID), ItemID: uuidString(r.SaleItemID), ProductID: product,
			SKU: r.Sku, ProductName: r.ProductName, Quantity: int64(r.Quantity),
			LineTotal: r.LineTotalMinor, UnitCost: int8Ptr(r.CostMinor),
			UnitPrice: r.UnitPriceMinor, CatalogPrice: saleCatalogPrice(r.SaleCurrency, r.VariantPriceEgpMinor, r.VariantPriceUsdMinor), Attributes: saleAttributeSnapshot(r.VariantAttributes),
		})
	}
	classes, err := q.PrintableReportLineClassifications(ctx, sqlcgen.PrintableReportLineClassificationsParams{StartUtc: start, EndUtc: end, StoreID: store})
	if err != nil {
		return f, reportErr("sales report classifications", err)
	}
	for _, r := range classes {
		f.Classifications = append(f.Classifications, salesreport.Classification{
			SaleID: uuidString(r.SaleID), ItemID: uuidString(r.SaleItemID), Kind: r.ClassificationKind,
			ID: uuidString(r.ClassificationID), NameAr: r.NameAr, NameEn: r.NameEn,
		})
	}
	tags, err := q.PrintableReportLineTags(ctx, sqlcgen.PrintableReportLineTagsParams{StartUtc: start, EndUtc: end, StoreID: store})
	if err != nil {
		return f, reportErr("sales report tags", err)
	}
	for _, r := range tags {
		f.Tags = append(f.Tags, salesreport.Tag{
			SaleID: uuidString(r.SaleID), ItemID: uuidString(r.SaleItemID), ID: uuidString(r.TagID), NameAr: r.NameAr, NameEn: r.NameEn,
		})
	}
	payments, err := q.PrintableReportSalePayments(ctx, sqlcgen.PrintableReportSalePaymentsParams{StartUtc: start, EndUtc: end, StoreID: store})
	if err != nil {
		return f, reportErr("sales report payments", err)
	}
	for _, r := range payments {
		f.Payments = append(f.Payments, salesreport.Payment{SaleID: uuidString(r.SaleID), Method: r.Method, Amount: r.AmountMinor, Change: r.ChangeMinor})
	}
	returns, err := q.PrintableReportReturns(ctx, sqlcgen.PrintableReportReturnsParams{StartUtc: start, EndUtc: end, StoreID: store})
	if err != nil {
		return f, reportErr("sales report returns", err)
	}
	for _, r := range returns {
		f.Returns = append(f.Returns, salesreport.Return{
			ID: uuidString(r.ReturnRefundID), SaleID: uuidString(r.SaleID), OccurredAt: r.OccurredAt.Time,
			Currency: r.Currency, Tax: r.TaxRefundedMinor, Total: r.RefundTotalMinor,
		})
	}
	returnLines, err := q.PrintableReportReturnLines(ctx, sqlcgen.PrintableReportReturnLinesParams{StartUtc: start, EndUtc: end, StoreID: store})
	if err != nil {
		return f, reportErr("sales report return lines", err)
	}
	for _, r := range returnLines {
		f.ReturnLines = append(f.ReturnLines, salesreport.ReturnLine{
			ReturnID: uuidString(r.ReturnRefundID), SaleID: uuidString(r.SaleID), SaleLineID: uuidString(r.OriginalSaleLineID),
			Quantity: int64(r.Quantity), Restocked: r.Restocked, Refund: r.RefundMinor, Cost: int8Ptr(r.CostMinor),
		})
	}
	returnPayments, err := q.PrintableReportReturnPayments(ctx, sqlcgen.PrintableReportReturnPaymentsParams{StartUtc: start, EndUtc: end, StoreID: store})
	if err != nil {
		return f, reportErr("sales report refund payments", err)
	}
	for _, r := range returnPayments {
		f.ReturnPayments = append(f.ReturnPayments, salesreport.ReturnPayment{ReturnID: uuidString(r.ReturnRefundID), Method: r.Method, Amount: r.AmountMinor})
	}
	if f.Shop, err = printableShop(ctx, q, store); err != nil {
		return f, err
	}
	return f, nil
}

// StockItems reads the stocktaking sheet and the letterhead.
func (d Reporting) StockItems(ctx context.Context, storeID string) ([]salesreport.StockItem, *salesreport.Shop, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	store, err := printableScope(storeID)
	if err != nil {
		return nil, nil, err
	}
	q := sqlcgen.New(d.pool)
	rows, err := q.PrintableStockItems(ctx, store)
	if err != nil {
		return nil, nil, reportErr("stocktaking", err)
	}
	items := make([]salesreport.StockItem, 0, len(rows))
	for _, r := range rows {
		item := salesreport.StockItem{
			VariantID: uuidString(r.VariantID), SKU: r.Sku, ProductID: uuidString(r.ProductID),
			ProductNameAr: r.ProductNameAr, ProductNameEn: r.ProductNameEn,
			VariantNameAr: r.VariantNameAr, VariantNameEn: r.VariantNameEn,
			CategoryID: uuidString(r.CategoryID), CategoryNameAr: r.CategoryNameAr, CategoryNameEn: r.CategoryNameEn,
			Active: r.Active, Quantity: r.Quantity, UnitCost: int8Ptr(r.UnitCostMinor),
		}
		if r.HasPrice {
			price := r.UnitPriceMinor
			item.UnitPrice = &price
		}
		items = append(items, item)
	}
	shop, err := printableShop(ctx, q, store)
	if err != nil {
		return nil, nil, err
	}
	return items, shop, nil
}

func printableShop(ctx context.Context, q *sqlcgen.Queries, store pgtype.UUID) (*salesreport.Shop, error) {
	rows, err := q.PrintableReportShop(ctx, store)
	if err != nil {
		return nil, reportErr("report letterhead", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	r := rows[0]
	return &salesreport.Shop{NameAr: r.ShopNameAr, NameEn: r.ShopNameEn, AddressAr: r.ShopAddressAr, AddressEn: r.ShopAddressEn, Phone: r.ShopPhone}, nil
}
