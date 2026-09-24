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
	Units           int64  `json:"units"`
	NormalizedMinor string `json:"normalized_minor"`
}

// DailyDay is one unified daily row: normalized EGP in All mode, native
// bucket amounts in EGP/USD modes. The contract metadata makes relabeling
// impossible: display_currency + normalized travel with the data.
type DailyDay struct {
	Date         string `json:"date"`
	Transactions int64  `json:"transactions"`
	Units        int64  `json:"units"`
	AmountMinor  string `json:"amount_minor"`
}

// DailyResponse wraps daily rows with explicit mode metadata.
type DailyResponse struct {
	Timezone        string            `json:"timezone"`
	Period          report.PeriodMeta `json:"period"`
	Mode            string            `json:"mode"`
	DisplayCurrency string            `json:"display_currency"`
	Normalized      bool              `json:"normalized"`
	Days            []DailyDay        `json:"days"`
}

// ModeAverage is a server-computed average transaction value: truncating
// integer division of the mode total by the mode transaction count
// (documented, deterministic, identical rule in every mode).
type ModeAverage struct {
	Transactions int64  `json:"transactions"`
	Units        int64  `json:"units"`
	AverageMinor string `json:"average_minor"`
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

// Overview is the dashboard landing response: frozen summary data mapped
// into string-money DTOs plus presentation-normalized values. Native
// totals flow through untouched (same numbers, decimal-string encoding).
type Overview struct {
	GeneratedAt time.Time         `json:"generated_at"`
	Timezone    string            `json:"timezone"`
	Period      report.PeriodMeta `json:"period"`
	Summary     SummaryDTO        `json:"summary"`
	Normalized  NormalizedTotal   `json:"normalized"`
	// Averages carries per-mode server-computed average transaction values
	// (truncating integer division; identical rule in every mode).
	Averages OverviewAverages `json:"averages"`
	Fx       FxInfo           `json:"fx"`
}

// OverviewAverages holds one average per currency mode. Absent currencies
// report zero transactions with a zero average (never null, never mixed).
type OverviewAverages struct {
	All ModeAverage `json:"all"`
	EGP ModeAverage `json:"egp"`
	USD ModeAverage `json:"usd"`
}

// SummaryBucket is one currency bucket with exact decimal-string money.
type SummaryBucket struct {
	Currency        string `json:"currency"`
	SubtotalMinor   string `json:"subtotal_minor"`
	DiscountMinor   string `json:"discount_minor"`
	TaxMinor        string `json:"tax_minor"`
	SalesTotalMinor string `json:"sales_total_minor"`
	LineCostMinor   string `json:"line_cost_minor"`
}

// SummaryPayment mirrors report.PaymentTotal with string money.
type SummaryPayment struct {
	Method      string `json:"method"`
	Currency    string `json:"currency"`
	AmountMinor string `json:"amount_minor"`
	ChangeMinor string `json:"change_minor"`
}

// SummaryDTO mirrors report.Summary with string-backed money.
type SummaryDTO struct {
	TransactionCount int64            `json:"transaction_count"`
	UnitsSold        int64            `json:"units_sold"`
	CurrencyTotals   []SummaryBucket  `json:"currency_totals"`
	PaymentTotals    []SummaryPayment `json:"payment_totals"`
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
		Units        int64
		Normalized   int64
		USDMissingFx int64
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
	nativeRows, err := s.repo.SalesSummary(ctx, req.Period.StartUTC, req.Period.EndUTC, "")
	if err != nil {
		return Overview{}, err
	}
	return Overview{
		GeneratedAt: req.GeneratedAt(), Timezone: req.Period.Timezone,
		Period:  sum.Period,
		Summary: toSummaryDTO(sum),
		Normalized: NormalizedTotal{
			NormalizedTotalMinor: minorString(norm.Normalized),
			Transactions:         norm.Transactions,
			Units:                norm.Units,
			USDSaleCount:         norm.USDSales,
		},
		Averages: overviewAverages(nativeRows, norm),
		Fx:       fx,
	}, nil
}

// overviewAverages computes per-mode averages server-side: All from the
// normalized total, EGP/USD from native per-currency rows (own transaction
// counts — never the all-currency count). Truncating integer division
// throughout (documented); zero transactions average to zero.
func overviewAverages(native []report.SummaryRow, norm NormalizedSummaryRow) OverviewAverages {
	out := OverviewAverages{
		All: ModeAverage{
			Transactions: norm.Transactions,
			Units:        norm.Units,
			AverageMinor: minorString(divTrunc(norm.Normalized, norm.Transactions)),
		},
		EGP: ModeAverage{AverageMinor: "0"},
		USD: ModeAverage{AverageMinor: "0"},
	}
	for _, b := range native {
		a := ModeAverage{Transactions: b.Transactions, Units: b.Units, AverageMinor: minorString(divTrunc(b.SalesTotal, b.Transactions))}
		if b.Currency == "EGP" {
			out.EGP = a
		} else if b.Currency == "USD" {
			out.USD = a
		}
	}
	return out
}

func divTrunc(total, txns int64) int64 {
	if txns <= 0 {
		return 0
	}
	return total / txns
}

// toSummaryDTO maps the frozen summary to string-money DTOs (same numbers,
// decimal-string encoding).
func toSummaryDTO(sum report.Summary) SummaryDTO {
	out := SummaryDTO{
		TransactionCount: sum.TransactionCount,
		UnitsSold:        sum.UnitsSold,
		CurrencyTotals:   make([]SummaryBucket, 0, len(sum.CurrencyTotals)),
		PaymentTotals:    make([]SummaryPayment, 0, len(sum.PaymentTotals)),
	}
	for _, b := range sum.CurrencyTotals {
		out.CurrencyTotals = append(out.CurrencyTotals, SummaryBucket{
			Currency: b.Currency, SubtotalMinor: minorString(b.SubtotalMinor),
			DiscountMinor: minorString(b.DiscountMinor), TaxMinor: minorString(b.TaxMinor),
			SalesTotalMinor: minorString(b.SalesTotalMinor), LineCostMinor: minorString(b.LineCostMinor),
		})
	}
	for _, p := range sum.PaymentTotals {
		out.PaymentTotals = append(out.PaymentTotals, SummaryPayment{
			Method: p.Method, Currency: p.Currency,
			AmountMinor: minorString(p.AmountMinor), ChangeMinor: minorString(p.ChangeMinor),
		})
	}
	return out
}

// Daily returns the unified daily series for one currency mode:
// all = normalized EGP per Cairo date; EGP/USD = native buckets only.
// Native modes never convert; All mode fails loudly on missing FX.
func (s Service) Daily(ctx context.Context, req report.Request, mode string) (DailyResponse, error) {
	switch mode {
	case "all", "EGP", "USD":
	default:
		return DailyResponse{}, apperr.New(apperr.InvalidInput, "unsupported daily mode: want all|EGP|USD")
	}
	if mode == "all" {
		rows, err := s.repo.DashboardNormalizedDaily(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Period.Timezone)
		if err != nil {
			return DailyResponse{}, err
		}
		out := DailyResponse{Mode: "all", DisplayCurrency: "EGP", Normalized: true, Days: []DailyDay{},
			Timezone: req.Period.Timezone, Period: periodMeta(req)}
		for _, r := range rows {
			if r.USDMissingFx > 0 {
				return DailyResponse{}, apperr.New(apperr.Internal, "projection integrity: USD sale without FX snapshot")
			}
			out.Days = append(out.Days, DailyDay{
				Date: r.Date, Transactions: r.Transactions, Units: r.Units,
				AmountMinor: minorString(r.Normalized),
			})
		}
		return out, nil
	}
	native, err := s.repo.SalesDaily(ctx, req.Period.StartUTC, req.Period.EndUTC, mode, req.Period.Timezone)
	if err != nil {
		return DailyResponse{}, err
	}
	out := DailyResponse{Mode: mode, DisplayCurrency: mode, Normalized: false, Days: []DailyDay{},
		Timezone: req.Period.Timezone, Period: periodMeta(req)}
	for _, r := range native {
		out.Days = append(out.Days, DailyDay{
			Date: r.Date, Transactions: r.Transactions, Units: r.Units,
			AmountMinor: minorString(r.SalesTotal),
		})
	}
	return out, nil
}

// periodMeta renders period metadata (same shape as report.PeriodMeta).
func periodMeta(req report.Request) report.PeriodMeta {
	const layout = "2006-01-02T15:04:05.999999999Z07:00"
	p := req.Period
	return report.PeriodMeta{
		Kind: p.Kind, Timezone: p.Timezone,
		StartLocal: p.StartLocal.Format(layout), EndLocalExclusive: p.EndLocalExclusive.Format(layout),
		StartUTC: p.StartUTC.Format(layout), EndUTC: p.EndUTC.Format(layout),
	}
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
// Diagnostics are allowlisted: only known error codes with mapped bilingual
// operator labels cross into browser JSON. Raw stored messages (which may
// contain connection strings, SQL, or payload fragments) stay server-side
// in logs, diagnostic tables, and admin tooling.
type SyncHealthItem struct {
	Freshness        report.Freshness `json:"freshness"`
	QueueCount       int64            `json:"queue_count"`
	PendingCount     int64            `json:"pending_count"`
	ProcessedCount   int64            `json:"processed_count"`
	BlockedCount     int64            `json:"blocked_count"`
	RetryCount       int64            `json:"retry_count"`
	OldestPendingAt  *string          `json:"oldest_pending_at,omitempty"`
	LastErrorEvent   string           `json:"last_error_event,omitempty"`
	LastErrorCode    string           `json:"last_error_code,omitempty"`
	LastErrorLabelAR string           `json:"last_error_label_ar,omitempty"`
	LastErrorLabelEN string           `json:"last_error_label_en,omitempty"`
}

// SyncHealth assembles freshness plus processing-state counts.
// QueueCount is pending + retry counted exactly once (single source:
// the combined pending/retry count, never pending_count + retry_count).
func (s Service) SyncHealth(ctx context.Context) (SyncHealthItem, error) {
	fresh, err := s.freshness(ctx)
	if err != nil {
		return SyncHealthItem{}, err
	}
	stats, err := s.saleStore.ProcessingStats(ctx, sale.ProcessorSaleProjectionV1)
	if err != nil {
		return SyncHealthItem{}, err
	}
	ar, en := safeDiagnostic(stats.LastErrorCode)
	out := SyncHealthItem{
		Freshness:        fresh,
		QueueCount:       stats.PendingCount,
		PendingCount:     stats.Counts[sale.ProcPending],
		ProcessedCount:   stats.Counts[sale.ProcProcessed],
		BlockedCount:     stats.Counts[sale.ProcBlocked],
		RetryCount:       stats.Counts[sale.ProcRetry],
		LastErrorEvent:   stats.LastErrorEvent,
		LastErrorCode:    stats.LastErrorCode,
		LastErrorLabelAR: ar,
		LastErrorLabelEN: en,
	}
	if stats.OldestPending != nil {
		out.OldestPendingAt = ptrStr(stats.OldestPending.Format(time.RFC3339))
	}
	return out, nil
}

// safeDiagnostic maps known processing error codes to safe bilingual
// operator labels. Unknown codes get a generic message; the raw stored
// message never crosses into browser JSON (it may contain connection
// strings, SQL, or payload fragments).
func safeDiagnostic(code string) (ar, en string) {
	switch code {
	case "":
		return "", ""
	case "SALE_ID_CONFLICT":
		return "تعارض مبيعات: حدثان بنفس رقم البيع", "Sale conflict: two events share one sale ID"
	case "OWNERSHIP_INTEGRITY":
		return "خطأ سلامة الملكية", "Ownership integrity error"
	case "VALIDATION_FAILED":
		return "حدث غير صالح", "Invalid event data"
	case "PROJECTION_FAILED":
		return "فشل مؤقت في المعالجة", "Transient projection failure"
	case "EVENT_MISSING":
		return "حدث مفقود", "Missing source event"
	default:
		return "حدث خطأ أثناء المعالجة", "A processing error occurred"
	}
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
