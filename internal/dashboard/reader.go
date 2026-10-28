package dashboard

import (
	"context"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
	"time"
)

// Reader combines explicit financial and presentation reads within one selected
// scope. Global freshness and already-scoped operational analytics stay separate.
type Reader interface {
	report.Reader
	DashboardNormalizedSummary(ctx context.Context, startUTC, endUTC time.Time) (NormalizedSummaryRow, error)
	DashboardNormalizedDaily(ctx context.Context, startUTC, endUTC time.Time, timezone string) ([]NormalizedDailyRow, error)
	DashboardLatestFx(ctx context.Context, startUTC, endUTC time.Time) (LatestFxRow, error)
	DashboardBranches(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]BranchRowRaw, error)
	DashboardProductsNormalized(ctx context.Context, startUTC, endUTC time.Time) ([]NormalizedProductRowRaw, error)
	DashboardCategoriesNormalized(ctx context.Context, startUTC, endUTC time.Time, kind string) ([]NormalizedCategoryRowRaw, error)
	DashboardRecentActivity(ctx context.Context, limit int) ([]ActivityItem, error)
	DashboardLatestSales(ctx context.Context, limit int) ([]LatestSale, error)
	DashboardNormalizedRefundsSummary(ctx context.Context, startUTC, endUTC time.Time) (NormalizedRefundSummaryRow, error)
	DashboardNormalizedRefundsDaily(ctx context.Context, startUTC, endUTC time.Time, timezone string) ([]NormalizedRefundDailyRow, error)
	DashboardProductsNormalizedRefunds(ctx context.Context, startUTC, endUTC time.Time) ([]NormalizedRefundProductRowRaw, error)
	DashboardCategoriesNormalizedRefunds(ctx context.Context, startUTC, endUTC time.Time, kind string) ([]NormalizedRefundCategoryRowRaw, error)
	DashboardReturnBranches(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]ReturnBranchRowRaw, error)
	DashboardProfit(ctx context.Context, startUTC, endUTC time.Time) ([]ProfitRowRaw, error)
}

// ReaderFor selects the scope already authorized by the HTTP boundary. It does
// not authorize users, normalize nonempty IDs or replace adapter validation.
func ReaderFor(repo Repository, scope *report.StoreScope) Reader {
	if scope == nil || scope.StoreID == "" {
		return repo
	}
	return storeReader{Reader: report.ReaderFor(repo, scope), repo: repo, storeID: scope.StoreID}
}

type storeReader struct {
	report.Reader
	repo    Repository
	storeID string
}

func (r storeReader) DashboardProfit(ctx context.Context, startUTC, endUTC time.Time) ([]ProfitRowRaw, error) {
	return r.repo.DashboardProfitForStore(ctx, r.storeID, startUTC, endUTC)
}

func (r storeReader) DashboardNormalizedSummary(ctx context.Context, startUTC, endUTC time.Time) (NormalizedSummaryRow, error) {
	return r.repo.DashboardNormalizedSummaryForStore(ctx, r.storeID, startUTC, endUTC)
}

func (r storeReader) DashboardNormalizedDaily(ctx context.Context, startUTC, endUTC time.Time, timezone string) ([]NormalizedDailyRow, error) {
	return r.repo.DashboardNormalizedDailyForStore(ctx, r.storeID, startUTC, endUTC, timezone)
}

func (r storeReader) DashboardLatestFx(ctx context.Context, startUTC, endUTC time.Time) (LatestFxRow, error) {
	return r.repo.DashboardLatestFxForStore(ctx, r.storeID, startUTC, endUTC)
}

func (r storeReader) DashboardBranches(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]BranchRowRaw, error) {
	return r.repo.DashboardBranchesForStore(ctx, r.storeID, startUTC, endUTC, currency)
}

func (r storeReader) DashboardProductsNormalized(ctx context.Context, startUTC, endUTC time.Time) ([]NormalizedProductRowRaw, error) {
	return r.repo.DashboardProductsNormalizedForStore(ctx, r.storeID, startUTC, endUTC)
}

func (r storeReader) DashboardCategoriesNormalized(ctx context.Context, startUTC, endUTC time.Time, kind string) ([]NormalizedCategoryRowRaw, error) {
	return r.repo.DashboardCategoriesNormalizedForStore(ctx, r.storeID, startUTC, endUTC, kind)
}

func (r storeReader) DashboardRecentActivity(ctx context.Context, limit int) ([]ActivityItem, error) {
	return r.repo.DashboardRecentActivityForStore(ctx, r.storeID, limit)
}

func (r storeReader) DashboardLatestSales(ctx context.Context, limit int) ([]LatestSale, error) {
	return r.repo.DashboardLatestSalesForStore(ctx, r.storeID, limit)
}

func (r storeReader) DashboardNormalizedRefundsSummary(ctx context.Context, startUTC, endUTC time.Time) (NormalizedRefundSummaryRow, error) {
	return r.repo.DashboardNormalizedRefundsSummaryForStore(ctx, r.storeID, startUTC, endUTC)
}

func (r storeReader) DashboardNormalizedRefundsDaily(ctx context.Context, startUTC, endUTC time.Time, timezone string) ([]NormalizedRefundDailyRow, error) {
	return r.repo.DashboardNormalizedRefundsDailyForStore(ctx, r.storeID, startUTC, endUTC, timezone)
}

func (r storeReader) DashboardProductsNormalizedRefunds(ctx context.Context, startUTC, endUTC time.Time) ([]NormalizedRefundProductRowRaw, error) {
	return r.repo.DashboardProductsNormalizedRefundsForStore(ctx, r.storeID, startUTC, endUTC)
}

func (r storeReader) DashboardCategoriesNormalizedRefunds(ctx context.Context, startUTC, endUTC time.Time, kind string) ([]NormalizedRefundCategoryRowRaw, error) {
	return r.repo.DashboardCategoriesNormalizedRefundsForStore(ctx, r.storeID, startUTC, endUTC, kind)
}

func (r storeReader) DashboardReturnBranches(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]ReturnBranchRowRaw, error) {
	return r.repo.DashboardReturnBranchesForStore(ctx, r.storeID, startUTC, endUTC, currency)
}
