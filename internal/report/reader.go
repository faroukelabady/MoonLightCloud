package report

import (
	"context"
	"time"
)

// Reader is the explicit financial read surface after a request's authorized
// scope has been selected. It cannot switch Stores between component reads.
type Reader interface {
	SalesSummary(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]SummaryRow, error)
	SalesPayments(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]PaymentRow, error)
	SalesDaily(ctx context.Context, startUTC, endUTC time.Time, currency, timezone string) ([]DailyRowRaw, error)
	SalesByProduct(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]ProductRow, error)
	SalesByVariant(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]VariantRow, error)
	SalesByProductType(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]ProductTypeRow, error)
	SalesByRootCategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]CategoryRow, error)
	SalesBySubcategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]CategoryRow, error)
	SalesByTag(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]TagRow, error)
	SalesByCashier(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]CashierRow, error)
	SalesByChannel(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]ChannelRow, error)
	RefundsSummary(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundSummaryRow, error)
	RefundsDaily(ctx context.Context, startUTC, endUTC time.Time, currency, timezone string) ([]RefundDailyRow, error)
	RefundsByProduct(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundProductRow, error)
	RefundsByProductType(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundProductTypeRow, error)
	RefundsByRootCategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundCategoryRow, error)
	RefundsBySubcategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundCategoryRow, error)
	RefundsByTag(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundTagRow, error)
	RefundsByCashier(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundCashierRow, error)
	RefundsByChannel(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundChannelRow, error)
}

// ReaderFor selects scope only; HTTP authorization remains before report
// construction. Nil and explicitly blank scopes retain global+legacy behavior.
// Nonempty values are passed unchanged to the existing Store-specific adapter,
// including its UUID validation; unknown Stores never fall back to global.
func ReaderFor(repo Repository, scope *StoreScope) Reader {
	if scope == nil || scope.StoreID == "" {
		return repo
	}
	return storeReader{repo: repo, storeID: scope.StoreID}
}

type storeReader struct {
	repo    Repository
	storeID string
}

func (r storeReader) SalesSummary(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]SummaryRow, error) {
	return r.repo.SalesSummaryForStore(ctx, r.storeID, startUTC, endUTC, currency)
}

func (r storeReader) SalesPayments(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]PaymentRow, error) {
	return r.repo.SalesPaymentsForStore(ctx, r.storeID, startUTC, endUTC, currency)
}

func (r storeReader) SalesDaily(ctx context.Context, startUTC, endUTC time.Time, currency, timezone string) ([]DailyRowRaw, error) {
	return r.repo.SalesDailyForStore(ctx, r.storeID, startUTC, endUTC, currency, timezone)
}

func (r storeReader) SalesByProduct(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]ProductRow, error) {
	return r.repo.SalesByProductForStore(ctx, r.storeID, startUTC, endUTC, currency)
}

func (r storeReader) SalesByVariant(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]VariantRow, error) {
	return r.repo.SalesByVariantForStore(ctx, r.storeID, startUTC, endUTC, currency)
}

func (r storeReader) SalesByProductType(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]ProductTypeRow, error) {
	return r.repo.SalesByProductTypeForStore(ctx, r.storeID, startUTC, endUTC, currency)
}

func (r storeReader) SalesByRootCategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]CategoryRow, error) {
	return r.repo.SalesByRootCategoryForStore(ctx, r.storeID, startUTC, endUTC, currency)
}

func (r storeReader) SalesBySubcategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]CategoryRow, error) {
	return r.repo.SalesBySubcategoryForStore(ctx, r.storeID, startUTC, endUTC, currency)
}

func (r storeReader) SalesByTag(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]TagRow, error) {
	return r.repo.SalesByTagForStore(ctx, r.storeID, startUTC, endUTC, currency)
}

func (r storeReader) SalesByCashier(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]CashierRow, error) {
	return r.repo.SalesByCashierForStore(ctx, r.storeID, startUTC, endUTC, currency)
}

func (r storeReader) SalesByChannel(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]ChannelRow, error) {
	return r.repo.SalesByChannelForStore(ctx, r.storeID, startUTC, endUTC, currency)
}

func (r storeReader) RefundsSummary(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundSummaryRow, error) {
	return r.repo.RefundsSummaryForStore(ctx, r.storeID, startUTC, endUTC, currency)
}

func (r storeReader) RefundsDaily(ctx context.Context, startUTC, endUTC time.Time, currency, timezone string) ([]RefundDailyRow, error) {
	return r.repo.RefundsDailyForStore(ctx, r.storeID, startUTC, endUTC, currency, timezone)
}

func (r storeReader) RefundsByProduct(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundProductRow, error) {
	return r.repo.RefundsByProductForStore(ctx, r.storeID, startUTC, endUTC, currency)
}

func (r storeReader) RefundsByProductType(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundProductTypeRow, error) {
	return r.repo.RefundsByProductTypeForStore(ctx, r.storeID, startUTC, endUTC, currency)
}

func (r storeReader) RefundsByRootCategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundCategoryRow, error) {
	return r.repo.RefundsByRootCategoryForStore(ctx, r.storeID, startUTC, endUTC, currency)
}

func (r storeReader) RefundsBySubcategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundCategoryRow, error) {
	return r.repo.RefundsBySubcategoryForStore(ctx, r.storeID, startUTC, endUTC, currency)
}

func (r storeReader) RefundsByTag(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundTagRow, error) {
	return r.repo.RefundsByTagForStore(ctx, r.storeID, startUTC, endUTC, currency)
}

func (r storeReader) RefundsByCashier(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundCashierRow, error) {
	return r.repo.RefundsByCashierForStore(ctx, r.storeID, startUTC, endUTC, currency)
}

func (r storeReader) RefundsByChannel(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundChannelRow, error) {
	return r.repo.RefundsByChannelForStore(ctx, r.storeID, startUTC, endUTC, currency)
}

// These two selectors are business dimensions, independent of Store scope.
func salesByCategory(ctx context.Context, reader Reader, req Request, root bool) ([]CategoryRow, error) {
	if root {
		return reader.SalesByRootCategory(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	}
	return reader.SalesBySubcategory(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
}
func refundsByCategory(ctx context.Context, reader Reader, req Request, root bool) ([]RefundCategoryRow, error) {
	if root {
		return reader.RefundsByRootCategory(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	}
	return reader.RefundsBySubcategory(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
}
