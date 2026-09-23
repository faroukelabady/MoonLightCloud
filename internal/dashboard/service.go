package dashboard

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
)

// Money string emission: dashboard BFF money fields are exact decimal
// strings (never JSON floats, never JS arithmetic inputs). Charts convert
// via SafeChartNumber, which refuses unsafe integers instead of rounding.
func minorString(v int64) string { return strconv.FormatInt(v, 10) }

// NormalizedTotal is the All-mode EGP total: native EGP plus each USD sale
// converted with its own historical FX snapshot. Truncation-free exactness
// comes from SQL numeric math; this DTO only transports the result.
type NormalizedTotal struct {
	NormalizedTotalMinor string `json:"normalized_total_minor"`
	Transactions         int64  `json:"transactions"`
	Units                int64  `json:"units"`
	USDSaleCount         int64  `json:"usd_sale_count"`
}

// FxInfo is truthful historical FX presentation (never a live rate).
type FxInfo struct {
	HasUSD            bool    `json:"has_usd"`
	LatestRate        *string `json:"latest_rate,omitempty"`
	LatestMicrorate   *string `json:"latest_rate_microrate,omitempty"`
	LatestOccurredAt  *string `json:"latest_occurred_at,omitempty"`
	MinMicrorate      *string `json:"min_rate_microrate,omitempty"`
	MaxMicrorate      *string `json:"max_rate_microrate,omitempty"`
	MultipleRatesUsed bool    `json:"multiple_rates_used"`
}

// BranchRow groups by the full historical shop snapshot tuple + channel.
// No Cloud shop catalog exists; identity is the snapshot itself.
type BranchRow struct {
	ShopNameAR      string `json:"shop_name_ar"`
	ShopNameEN      string `json:"shop_name_en"`
	ShopAddressAR   string `json:"shop_address_ar"`
	ShopAddressEN   string `json:"shop_address_en"`
	ShopPhone       string `json:"shop_phone"`
	Channel         string `json:"channel"`
	Currency        string `json:"currency"`
	Transactions    int64  `json:"transactions"`
	Units           int64  `json:"units"`
	SubtotalMinor   string `json:"subtotal_minor"`
	DiscountMinor   string `json:"discount_minor"`
	TaxMinor        string `json:"tax_minor"`
	SalesTotalMinor string `json:"sales_total_minor"`
}

// NormalizedProductRow is one product ranked by normalized EGP line value.
type NormalizedProductRow struct {
	ProductID       *string `json:"product_id,omitempty"`
	SKU             string  `json:"sku"`
	ProductName     string  `json:"product_name"`
	Units           int64   `json:"units"`
	NormalizedMinor string  `json:"normalized_minor"`
}

// NormalizedCategoryRow mirrors products for category facets (facet
// semantics preserved: subcategory rows may overlap).
type NormalizedCategoryRow struct {
	Kind            string `json:"kind"`
	ID              string `json:"classification_id"`
	NameAR          string `json:"name_ar"`
	NameEN          string `json:"name_en"`
	Units           int64  `json:"units"`
	NormalizedMinor string `json:"normalized_minor"`
}

// NormalizedDay is one Cairo date with its normalized EGP total.
type NormalizedDay struct {
	Date            string `json:"date"`
	Transactions    int64  `json:"transactions"`
	NormalizedMinor string `json:"normalized_minor"`
}

// ActivityItem is one Cloud-side event for the recent feed. Sources are
// authoritative inbox/processing rows only — never invented business
// events (no inventory/order/refund activity exists in Phase 3B).
type ActivityItem struct {
	Kind       string  `json:"kind"`
	EventID    string  `json:"event_id"`
	EventType  string  `json:"event_type"`
	Timestamp  string  `json:"timestamp"`
	DeviceName *string `json:"device_name,omitempty"`
	Detail     *string `json:"detail,omitempty"`
}

// LatestSale is a finalized Sale header for the latest-sales fallback
// (used where the design anticipates orders; there is no Order lifecycle).
type LatestSale struct {
	SaleID      string  `json:"sale_id"`
	SaleNumber  string  `json:"sale_number"`
	Channel     string  `json:"channel"`
	OccurredAt  string  `json:"occurred_at"`
	Currency    string  `json:"currency"`
	TotalMinor  string  `json:"total_minor"`
	CashierID   *string `json:"cashier_id,omitempty"`
	CashierName *string `json:"cashier_name,omitempty"`
}

// Overview is the dashboard landing response: frozen summary plus
// presentation-normalized values. Native buckets come verbatim from
// ReportingService; only the normalized_* fields are dashboard-added.
type Overview struct {
	GeneratedAt time.Time         `json:"generated_at"`
	Timezone    string            `json:"timezone"`
	Period      report.PeriodMeta `json:"period"`
	Summary     report.Summary    `json:"summary"`
	Normalized  NormalizedTotal   `json:"normalized"`
	Fx          FxInfo            `json:"fx"`
}

// Repository is the dashboard storage boundary: the frozen reporting
// repository plus dashboard-only read operations (all static,
// parameterized, read-only aggregates).
type Repository interface {
	report.Repository
	DashboardNormalizedSummary(ctx context.Context, startUTC, endUTC time.Time) (NormalizedSummaryRow, error)
	DashboardNormalizedDaily(ctx context.Context, startUTC, endUTC time.Time, timezone string) ([]NormalizedDailyRow, error)
	DashboardLatestFx(ctx context.Context, startUTC, endUTC time.Time) (LatestFxRow, error)
	DashboardBranches(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]BranchRowRaw, error)
	DashboardProductsNormalized(ctx context.Context, startUTC, endUTC time.Time) ([]NormalizedProductRowRaw, error)
	DashboardCategoriesNormalized(ctx context.Context, startUTC, endUTC time.Time, kind string) ([]NormalizedCategoryRowRaw, error)
	DashboardRecentActivity(ctx context.Context, limit int) ([]ActivityItem, error)
	DashboardLatestSales(ctx context.Context, limit int) ([]LatestSale, error)
}

type (
	NormalizedSummaryRow struct {
		Transactions int64
		Units        int64
		Normalized   int64
		USDSales     int64
		USDMissingFx int64
	}
	NormalizedDailyRow struct {
		Date         string
		Transactions int64
		Normalized   int64
	}
	LatestFxRow struct {
		Rate          *string
		Microrate     *int64
		OccurredAt    *time.Time
		DistinctRates int64
		MinMicrorate  *int64
		MaxMicrorate  *int64
		USDSales      int64
	}
	BranchRowRaw struct {
		Channel                        string
		ShopNameAR, ShopNameEN         string
		ShopAddressAR, ShopAddressEN   string
		ShopPhone                      string
		ShopReceiptFooterAR            string
		ShopReceiptFooterEN            string
		Currency                       string
		Transactions, Units            int64
		Subtotal, Discount, Tax, Total int64
	}
	NormalizedProductRowRaw struct {
		ProductID   *string
		SKU         string
		ProductName string
		Units       int64
		Normalized  int64
		MissingFx   int64
	}
	NormalizedCategoryRowRaw struct {
		Kind           string
		ID             string
		NameAR, NameEN string
		Units          int64
		Normalized     int64
		MissingFx      int64
	}
)

// Service composes frozen ReportingService data with dashboard-only
// presentation values (FX normalization). No business rule is
// reimplemented: native totals flow through untouched.
type Service struct {
	reports   report.Service
	repo      Repository
	saleStore sale.Store
	clock     clock.Clock
}

// NewService wires the dashboard service.
func NewService(reports report.Service, repo Repository, saleStore sale.Store, c clock.Clock) Service {
	return Service{reports: reports, repo: repo, saleStore: saleStore, clock: c}
}

// Overview assembles summary + normalized total + FX info. A USD sale
// without an FX snapshot is a projection-integrity error, not silently
// droppable revenue.
func (s Service) Overview(ctx context.Context, req report.Request) (Overview, error) {
	sum, err := s.reports.Summary(ctx, req)
	if err != nil {
		return Overview{}, err
	}
	norm, err := s.repo.DashboardNormalizedSummary(ctx, req.Period.StartUTC, req.Period.EndUTC)
	if err != nil {
		return Overview{}, err
	}
	if norm.USDMissingFx > 0 {
		return Overview{}, apperr.New(apperr.Internal, "projection integrity: USD sale without FX snapshot")
	}
	fx, err := s.fxInfo(ctx, req)
	if err != nil {
		return Overview{}, err
	}
	return Overview{
		GeneratedAt: req.GeneratedAt(), Timezone: req.Period.Timezone,
		Period:  sum.Period,
		Summary: sum,
		Normalized: NormalizedTotal{
			NormalizedTotalMinor: minorString(norm.Normalized),
			Transactions:         norm.Transactions,
			Units:                norm.Units,
			USDSaleCount:         norm.USDSales,
		},
		Fx: fx,
	}, nil
}

// DailyNormalized returns per-day normalized EGP values merged over the
// frozen daily series shape (dates ascending, Cairo-grouped).
func (s Service) DailyNormalized(ctx context.Context, req report.Request) ([]NormalizedDay, error) {
	rows, err := s.repo.DashboardNormalizedDaily(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Period.Timezone)
	if err != nil {
		return nil, err
	}
	out := make([]NormalizedDay, 0, len(rows))
	for _, r := range rows {
		out = append(out, NormalizedDay{
			Date: r.Date, Transactions: r.Transactions,
			NormalizedMinor: minorString(r.Normalized),
		})
	}
	return out, nil
}

// ProductsNormalized ranks products by normalized EGP line value.
func (s Service) ProductsNormalized(ctx context.Context, req report.Request) ([]NormalizedProductRow, error) {
	rows, err := s.repo.DashboardProductsNormalized(ctx, req.Period.StartUTC, req.Period.EndUTC)
	if err != nil {
		return nil, err
	}
	out := make([]NormalizedProductRow, 0, len(rows))
	for _, r := range rows {
		if r.MissingFx > 0 {
			return nil, apperr.New(apperr.Internal, "projection integrity: USD line without FX snapshot")
		}
		out = append(out, NormalizedProductRow{
			ProductID: r.ProductID, SKU: r.SKU, ProductName: r.ProductName,
			Units: r.Units, NormalizedMinor: minorString(r.Normalized),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		// Numeric compare on exact values (lexicographic string compare
		// would misorder magnitudes); then units, then identity.
		if vi, vj := minorInt(out[i].NormalizedMinor), minorInt(out[j].NormalizedMinor); vi != vj {
			return vi > vj
		}
		if out[i].Units != out[j].Units {
			return out[i].Units > out[j].Units
		}
		return out[i].SKU < out[j].SKU
	})
	return out, nil
}

// CategoriesNormalized mirrors products for root/subcategory facets.
func (s Service) CategoriesNormalized(ctx context.Context, req report.Request, kind string) ([]NormalizedCategoryRow, error) {
	if kind != report.DimensionRootCategory && kind != report.DimensionSubcategory {
		return nil, apperr.New(apperr.InvalidInput, "unsupported normalized category dimension")
	}
	rows, err := s.repo.DashboardCategoriesNormalized(ctx, req.Period.StartUTC, req.Period.EndUTC, kindName(kind))
	if err != nil {
		return nil, err
	}
	out := make([]NormalizedCategoryRow, 0, len(rows))
	for _, r := range rows {
		if r.MissingFx > 0 {
			return nil, apperr.New(apperr.Internal, "projection integrity: USD line without FX snapshot")
		}
		out = append(out, NormalizedCategoryRow{
			Kind: r.Kind, ID: r.ID, NameAR: r.NameAR, NameEN: r.NameEN,
			Units: r.Units, NormalizedMinor: minorString(r.Normalized),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if vi, vj := minorInt(out[i].NormalizedMinor), minorInt(out[j].NormalizedMinor); vi != vj {
			return vi > vj
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func kindName(dimension string) string {
	if dimension == report.DimensionRootCategory {
		return "root"
	}
	return "subcategory"
}

// Branches groups by full shop snapshot tuple + channel (native buckets).
func (s Service) Branches(ctx context.Context, req report.Request) ([]BranchRow, error) {
	rows, err := s.repo.DashboardBranches(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	if err != nil {
		return nil, err
	}
	out := make([]BranchRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, BranchRow{
			ShopNameAR: r.ShopNameAR, ShopNameEN: r.ShopNameEN,
			ShopAddressAR: r.ShopAddressAR, ShopAddressEN: r.ShopAddressEN,
			ShopPhone: r.ShopPhone, Channel: r.Channel, Currency: r.Currency,
			Transactions: r.Transactions, Units: r.Units,
			SubtotalMinor: minorString(r.Subtotal), DiscountMinor: minorString(r.Discount),
			TaxMinor: minorString(r.Tax), SalesTotalMinor: minorString(r.Total),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ShopNameEN != out[j].ShopNameEN {
			return out[i].ShopNameEN < out[j].ShopNameEN
		}
		if out[i].Channel != out[j].Channel {
			return out[i].Channel < out[j].Channel
		}
		return out[i].Currency < out[j].Currency
	})
	return out, nil
}

// SyncHealthItem is the dashboard sync-health card model: frozen
// freshness plus projector processing visibility. Wording stays honest:
// complete means Cloud has no backlog, never that Retail is synced.
type SyncHealthItem struct {
	Freshness        report.Freshness `json:"freshness"`
	PendingCount     int64            `json:"pending_count"`
	ProcessedCount   int64            `json:"processed_count"`
	BlockedCount     int64            `json:"blocked_count"`
	RetryCount       int64            `json:"retry_count"`
	OldestPendingAt  *string          `json:"oldest_pending_at,omitempty"`
	LastErrorEvent   string           `json:"last_error_event,omitempty"`
	LastErrorCode    string           `json:"last_error_code,omitempty"`
	LastErrorMessage string           `json:"last_error_message,omitempty"`
}

// SyncHealth assembles freshness plus processing-state counts.
func (s Service) SyncHealth(ctx context.Context) (SyncHealthItem, error) {
	fresh, err := s.freshness(ctx)
	if err != nil {
		return SyncHealthItem{}, err
	}
	stats, err := s.saleStore.ProcessingStats(ctx, sale.ProcessorSaleProjectionV1)
	if err != nil {
		return SyncHealthItem{}, err
	}
	out := SyncHealthItem{
		Freshness:        fresh,
		PendingCount:     stats.PendingCount,
		ProcessedCount:   stats.Counts[sale.ProcProcessed],
		BlockedCount:     stats.Counts[sale.ProcBlocked],
		RetryCount:       stats.Counts[sale.ProcRetry],
		LastErrorEvent:   stats.LastErrorEvent,
		LastErrorCode:    stats.LastErrorCode,
		LastErrorMessage: stats.LastErrorMsg,
	}
	if stats.OldestPending != nil {
		out.OldestPendingAt = ptrStr(stats.OldestPending.Format(time.RFC3339))
	}
	return out, nil
}
func (s Service) RecentActivity(ctx context.Context, limit int) ([]ActivityItem, error) {
	if limit < 1 || limit > 100 {
		return nil, apperr.New(apperr.InvalidInput, "limit must be within [1, 100]")
	}
	return s.repo.DashboardRecentActivity(ctx, limit)
}

// LatestSales returns the newest finalized sales (orders fallback).
func (s Service) LatestSales(ctx context.Context, limit int) ([]LatestSale, error) {
	if limit < 1 || limit > 100 {
		return nil, apperr.New(apperr.InvalidInput, "limit must be within [1, 100]")
	}
	return s.repo.DashboardLatestSales(ctx, limit)
}

func (s Service) fxInfo(ctx context.Context, req report.Request) (FxInfo, error) {
	row, err := s.repo.DashboardLatestFx(ctx, req.Period.StartUTC, req.Period.EndUTC)
	if err != nil {
		return FxInfo{}, err
	}
	out := FxInfo{HasUSD: row.USDSales > 0, MultipleRatesUsed: row.DistinctRates > 1}
	if row.Rate != nil {
		out.LatestRate = row.Rate
	}
	if row.Microrate != nil {
		out.LatestMicrorate = ptrStr(minorString(*row.Microrate))
	}
	if row.OccurredAt != nil {
		out.LatestOccurredAt = ptrStr(row.OccurredAt.Format(time.RFC3339))
	}
	if row.MinMicrorate != nil {
		out.MinMicrorate = ptrStr(minorString(*row.MinMicrorate))
	}
	if row.MaxMicrorate != nil {
		out.MaxMicrorate = ptrStr(minorString(*row.MaxMicrorate))
	}
	return out, nil
}

func (s Service) freshness(ctx context.Context) (report.Freshness, error) {
	row, err := s.repo.SalesProjectionFreshness(ctx)
	if err != nil {
		return report.Freshness{}, err
	}
	return report.Freshness{
		LatestSaleEventReceivedAt:     row.LatestReceivedAt,
		LatestProjectedSaleOccurredAt: row.LatestOccurredAt,
		ProjectionBacklogCount:        row.Backlog,
		BlockedSaleEventCount:         row.Blocked,
		CloudProjectionComplete:       row.Backlog == 0,
	}, nil
}

func ptrStr(s string) *string { v := s; return &v }

// minorInt parses an exact minor-unit string for presentation ordering.
// All values originate from bigint-cast SQL numerics, so parsing cannot
// fail; a failure would be a programming error, surfaced loudly.
func minorInt(s string) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		panic("dashboard: non-integer minor value " + s)
	}
	return v
}
