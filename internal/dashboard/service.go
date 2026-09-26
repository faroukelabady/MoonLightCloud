package dashboard

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
	"github.com/faroukelabady/MoonLightCloud/internal/returnrefund"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
)

// Money string emission: dashboard BFF money fields are exact decimal
// strings (never JSON floats, never JS arithmetic inputs). Charts convert
// via SafeChartNumber, which refuses unsafe integers instead of rounding.
func minorString(v int64) string { return strconv.FormatInt(v, 10) }

// subCheckedI64 subtracts with overflow failure (net may be negative, but
// must never wrap).
func subCheckedI64(a, b int64) (int64, error) {
	const maxInt64 = int64(^uint64(0) >> 1)
	const minInt64 = -maxInt64 - 1
	if b > 0 && a < minInt64+b {
		return 0, apperr.New(apperr.Internal, "net arithmetic overflow")
	}
	if b < 0 && a > maxInt64+b {
		return 0, apperr.New(apperr.Internal, "net arithmetic overflow")
	}
	return a - b, nil
}

// NormalizedTotal is the All-mode EGP total: native EGP plus each USD sale
// converted with its own historical FX snapshot. Truncation-free exactness
// comes from SQL numeric math; this DTO only transports the result.
// Refund/net companions reverse with each return's own historical FX the
// same way; net may be negative and is never clamped.
type NormalizedTotal struct {
	NormalizedTotalMinor  string `json:"normalized_total_minor"`
	NormalizedRefundMinor string `json:"normalized_refund_minor"`
	NormalizedNetMinor    string `json:"normalized_net_minor"`
	Transactions          int64  `json:"transactions"`
	Units                 int64  `json:"units"`
	ReturnTransactions    int64  `json:"return_transactions"`
	UnitsReturned         int64  `json:"units_returned"`
	USDSaleCount          int64  `json:"usd_sale_count"`
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
	ShopNameAR         string `json:"shop_name_ar"`
	ShopNameEN         string `json:"shop_name_en"`
	ShopAddressAR      string `json:"shop_address_ar"`
	ShopAddressEN      string `json:"shop_address_en"`
	ShopPhone          string `json:"shop_phone"`
	Channel            string `json:"channel"`
	Currency           string `json:"currency"`
	Transactions       int64  `json:"transactions"`
	Units              int64  `json:"units"`
	ReturnTransactions int64  `json:"return_transactions"`
	UnitsReturned      int64  `json:"units_returned"`
	SubtotalMinor      string `json:"subtotal_minor"`
	DiscountMinor      string `json:"discount_minor"`
	TaxMinor           string `json:"tax_minor"`
	SalesTotalMinor    string `json:"sales_total_minor"`
	RefundTotalMinor   string `json:"refund_total_minor"`
	ReturnedCostMinor  string `json:"returned_cost_minor"`
}

// NormalizedProductRow is one product ranked by normalized EGP NET line
// value (gross minus refunds, each normalized with its own historical FX).
// The net ranking is explicit in the label contract; gross-only consumers
// must not reuse this row as a gross ranking.
type NormalizedProductRow struct {
	ProductID             *string `json:"product_id,omitempty"`
	SKU                   string  `json:"sku"`
	ProductName           string  `json:"product_name"`
	Units                 int64   `json:"units"`
	UnitsReturned         int64   `json:"units_returned"`
	NormalizedMinor       string  `json:"normalized_minor"`
	RefundNormalizedMinor string  `json:"refund_normalized_minor"`
	NetNormalizedMinor    string  `json:"net_normalized_minor"`
}

// NormalizedCategoryRow mirrors products for category facets (facet
// semantics preserved: subcategory rows may overlap).
type NormalizedCategoryRow struct {
	Kind                  string `json:"kind"`
	ID                    string `json:"classification_id"`
	NameAR                string `json:"name_ar"`
	NameEN                string `json:"name_en"`
	Units                 int64  `json:"units"`
	UnitsReturned         int64  `json:"units_returned"`
	NormalizedMinor       string `json:"normalized_minor"`
	RefundNormalizedMinor string `json:"refund_normalized_minor"`
	NetNormalizedMinor    string `json:"net_normalized_minor"`
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
// Transactions/Units/Amount stay sale-scoped; return activity rides in the
// return-specific fields (net is derivable, never stored ambiguously).
type DailyDay struct {
	Date               string `json:"date"`
	Transactions       int64  `json:"transactions"`
	Units              int64  `json:"units"`
	AmountMinor        string `json:"amount_minor"`
	RefundMinor        string `json:"refund_minor"`
	ReturnTransactions int64  `json:"return_transactions"`
	UnitsReturned      int64  `json:"units_returned"`
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
// events. The feed carries sale kinds (accepted/projected/blocked) and
// return kinds (return_accepted/return_projected/return_blocked) from
// durable data. The return-processing actor snapshot is retained for
// audit but not exposed here; only safe fields (kind, event type, device
// name, error code) cross into browser JSON — never arbitrary notes.
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
	Currency          string `json:"currency"`
	SubtotalMinor     string `json:"subtotal_minor"`
	DiscountMinor     string `json:"discount_minor"`
	TaxMinor          string `json:"tax_minor"`
	SalesTotalMinor   string `json:"sales_total_minor"`
	LineCostMinor     string `json:"line_cost_minor"`
	RefundTotalMinor  string `json:"refund_total_minor"`
	NetSalesMinor     string `json:"net_sales_minor"`
	ReturnedUnits     int64  `json:"returned_units"`
	ReturnedCostMinor string `json:"returned_cost_minor"`
	NetCostMinor      string `json:"net_cost_minor"`
}

// SummaryPayment mirrors report.PaymentTotal with string money.
type SummaryPayment struct {
	Method      string `json:"method"`
	Currency    string `json:"currency"`
	AmountMinor string `json:"amount_minor"`
	ChangeMinor string `json:"change_minor"`
}

// SummaryDTO mirrors report.Summary with string-backed money.
// TransactionCount/UnitsSold stay sale-only; return activity rides in
// ReturnTransactionCount/UnitsReturned (never conflated).
type SummaryDTO struct {
	TransactionCount       int64            `json:"transaction_count"`
	UnitsSold              int64            `json:"units_sold"`
	ReturnTransactionCount int64            `json:"return_transaction_count"`
	UnitsReturned          int64            `json:"units_returned"`
	CurrencyTotals         []SummaryBucket  `json:"currency_totals"`
	PaymentTotals          []SummaryPayment `json:"payment_totals"`
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
	DashboardNormalizedRefundsSummary(ctx context.Context, startUTC, endUTC time.Time) (NormalizedRefundSummaryRow, error)
	DashboardNormalizedRefundsDaily(ctx context.Context, startUTC, endUTC time.Time, timezone string) ([]NormalizedRefundDailyRow, error)
	DashboardProductsNormalizedRefunds(ctx context.Context, startUTC, endUTC time.Time) ([]NormalizedRefundProductRowRaw, error)
	DashboardCategoriesNormalizedRefunds(ctx context.Context, startUTC, endUTC time.Time, kind string) ([]NormalizedRefundCategoryRowRaw, error)
	DashboardReturnBranches(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]ReturnBranchRowRaw, error)
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
	NormalizedRefundSummaryRow struct {
		Transactions int64
		Units        int64
		Normalized   int64
		USDReturns   int64
		USDMissingFx int64
	}
	NormalizedRefundDailyRow struct {
		Date         string
		Transactions int64
		Units        int64
		Normalized   int64
		USDMissingFx int64
	}
	NormalizedRefundProductRowRaw struct {
		ProductID   *string
		SKU         string
		ProductName string
		Units       int64
		Normalized  int64
		MissingFx   int64
	}
	NormalizedRefundCategoryRowRaw struct {
		Kind           string
		ID             string
		NameAR, NameEN string
		Units          int64
		Normalized     int64
		MissingFx      int64
	}
	ReturnBranchRowRaw struct {
		Channel                      string
		ShopNameAR, ShopNameEN       string
		ShopAddressAR, ShopAddressEN string
		ShopPhone                    string
		ShopReceiptFooterAR          string
		ShopReceiptFooterEN          string
		Currency                     string
		Transactions, Units          int64
		RefundTotal, ReturnedCost    int64
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
	refundNorm, err := s.repo.DashboardNormalizedRefundsSummary(ctx, req.Period.StartUTC, req.Period.EndUTC)
	if err != nil {
		return Overview{}, err
	}
	if refundNorm.USDMissingFx > 0 {
		return Overview{}, apperr.New(apperr.Internal, "projection integrity: USD return without FX snapshot")
	}
	netNorm, err := subCheckedI64(norm.Normalized, refundNorm.Normalized)
	if err != nil {
		return Overview{}, err
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
			NormalizedTotalMinor:  minorString(norm.Normalized),
			NormalizedRefundMinor: minorString(refundNorm.Normalized),
			NormalizedNetMinor:    minorString(netNorm),
			Transactions:          norm.Transactions,
			Units:                 norm.Units,
			ReturnTransactions:    refundNorm.Transactions,
			UnitsReturned:         refundNorm.Units,
			USDSaleCount:          norm.USDSales,
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

// toSummaryDTO maps the summary to string-money DTOs (same numbers,
// decimal-string encoding), including the additive refund/net fields.
func toSummaryDTO(sum report.Summary) SummaryDTO {
	out := SummaryDTO{
		TransactionCount:       sum.TransactionCount,
		UnitsSold:              sum.UnitsSold,
		ReturnTransactionCount: sum.ReturnTransactionCount,
		UnitsReturned:          sum.UnitsReturned,
		CurrencyTotals:         make([]SummaryBucket, 0, len(sum.CurrencyTotals)),
		PaymentTotals:          make([]SummaryPayment, 0, len(sum.PaymentTotals)),
	}
	for _, b := range sum.CurrencyTotals {
		out.CurrencyTotals = append(out.CurrencyTotals, SummaryBucket{
			Currency: b.Currency, SubtotalMinor: minorString(b.SubtotalMinor),
			DiscountMinor: minorString(b.DiscountMinor), TaxMinor: minorString(b.TaxMinor),
			SalesTotalMinor: minorString(b.SalesTotalMinor), LineCostMinor: minorString(b.LineCostMinor),
			RefundTotalMinor: minorString(b.RefundTotalMinor), NetSalesMinor: minorString(b.NetSalesMinor),
			ReturnedUnits:     b.ReturnedUnits,
			ReturnedCostMinor: minorString(b.ReturnedCostMinor), NetCostMinor: minorString(b.NetCostMinor),
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
		refundRows, err := s.repo.DashboardNormalizedRefundsDaily(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Period.Timezone)
		if err != nil {
			return DailyResponse{}, err
		}
		byDay := map[string]*DailyDay{}
		order := []string{}
		dayOf := func(date string) *DailyDay {
			day, ok := byDay[date]
			if !ok {
				day = &DailyDay{Date: date, AmountMinor: "0", RefundMinor: "0"}
				byDay[date] = day
				order = append(order, date)
			}
			return day
		}
		for _, r := range rows {
			if r.USDMissingFx > 0 {
				return DailyResponse{}, apperr.New(apperr.Internal, "projection integrity: USD sale without FX snapshot")
			}
			day := dayOf(r.Date)
			day.Transactions = r.Transactions
			day.Units = r.Units
			day.AmountMinor = minorString(r.Normalized)
		}
		for _, r := range refundRows {
			if r.USDMissingFx > 0 {
				return DailyResponse{}, apperr.New(apperr.Internal, "projection integrity: USD return without FX snapshot")
			}
			day := dayOf(r.Date)
			day.ReturnTransactions = r.Transactions
			day.UnitsReturned = r.Units
			day.RefundMinor = minorString(r.Normalized)
		}
		sort.Strings(order)
		out := DailyResponse{Mode: "all", DisplayCurrency: "EGP", Normalized: true, Days: []DailyDay{},
			Timezone: req.Period.Timezone, Period: periodMeta(req)}
		for _, d := range order {
			out.Days = append(out.Days, *byDay[d])
		}
		return out, nil
	}
	native, err := s.repo.SalesDaily(ctx, req.Period.StartUTC, req.Period.EndUTC, mode, req.Period.Timezone)
	if err != nil {
		return DailyResponse{}, err
	}
	refundNative, err := s.repo.RefundsDaily(ctx, req.Period.StartUTC, req.Period.EndUTC, mode, req.Period.Timezone)
	if err != nil {
		return DailyResponse{}, err
	}
	byDay := map[string]*DailyDay{}
	order := []string{}
	dayOf := func(date string) *DailyDay {
		day, ok := byDay[date]
		if !ok {
			day = &DailyDay{Date: date, AmountMinor: "0", RefundMinor: "0"}
			byDay[date] = day
			order = append(order, date)
		}
		return day
	}
	for _, r := range native {
		day := dayOf(r.Date)
		day.Transactions = r.Transactions
		day.Units = r.Units
		day.AmountMinor = minorString(r.SalesTotal)
	}
	for _, r := range refundNative {
		day := dayOf(r.Date)
		day.ReturnTransactions = r.Transactions
		day.UnitsReturned = r.Units
		day.RefundMinor = minorString(r.RefundTotal)
	}
	sort.Strings(order)
	out := DailyResponse{Mode: mode, DisplayCurrency: mode, Normalized: false, Days: []DailyDay{},
		Timezone: req.Period.Timezone, Period: periodMeta(req)}
	for _, d := range order {
		out.Days = append(out.Days, *byDay[d])
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

// ProductsNormalized ranks products by normalized EGP NET line value
// (gross minus refunds, each normalized with its own historical FX). The
// net ranking is explicit: gross-only consumers must not reuse it.
func (s Service) ProductsNormalized(ctx context.Context, req report.Request) ([]NormalizedProductRow, error) {
	rows, err := s.repo.DashboardProductsNormalized(ctx, req.Period.StartUTC, req.Period.EndUTC)
	if err != nil {
		return nil, err
	}
	refundRows, err := s.repo.DashboardProductsNormalizedRefunds(ctx, req.Period.StartUTC, req.Period.EndUTC)
	if err != nil {
		return nil, err
	}
	byKey := map[string]*NormalizedProductRow{}
	order := []string{}
	getRow := func(key string, fill func(*NormalizedProductRow)) *NormalizedProductRow {
		row, ok := byKey[key]
		if !ok {
			row = &NormalizedProductRow{NormalizedMinor: "0", RefundNormalizedMinor: "0", NetNormalizedMinor: "0"}
			fill(row)
			byKey[key] = row
			order = append(order, key)
		}
		return row
	}
	for _, r := range rows {
		if r.MissingFx > 0 {
			return nil, apperr.New(apperr.Internal, "projection integrity: USD line without FX snapshot")
		}
		row := getRow(productKey(r.ProductID, r.SKU, r.ProductName), func(row *NormalizedProductRow) {
			row.ProductID, row.SKU, row.ProductName = r.ProductID, r.SKU, r.ProductName
		})
		row.Units = r.Units
		row.NormalizedMinor = minorString(r.Normalized)
	}
	for _, r := range refundRows {
		if r.MissingFx > 0 {
			return nil, apperr.New(apperr.Internal, "projection integrity: USD return line without FX snapshot")
		}
		row := getRow(productKey(r.ProductID, r.SKU, r.ProductName), func(row *NormalizedProductRow) {
			row.ProductID, row.SKU, row.ProductName = r.ProductID, r.SKU, r.ProductName
		})
		row.UnitsReturned = r.Units
		row.RefundNormalizedMinor = minorString(r.Normalized)
	}
	out := make([]NormalizedProductRow, 0, len(byKey))
	for _, k := range order {
		row := byKey[k]
		net, err := subCheckedI64(minorInt(row.NormalizedMinor), minorInt(row.RefundNormalizedMinor))
		if err != nil {
			return nil, err
		}
		row.NetNormalizedMinor = minorString(net)
		out = append(out, *row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		// Numeric compare on exact NET values (lexicographic string
		// compare would misorder magnitudes); then units, then identity.
		if vi, vj := minorInt(out[i].NetNormalizedMinor), minorInt(out[j].NetNormalizedMinor); vi != vj {
			return vi > vj
		}
		if out[i].Units != out[j].Units {
			return out[i].Units > out[j].Units
		}
		return out[i].SKU < out[j].SKU
	})
	return out, nil
}

func productKey(productID *string, sku, name string) string {
	id := ""
	if productID != nil {
		id = *productID
	}
	return id + "\x00" + sku + "\x00" + name
}

// CategoriesNormalized mirrors products for root/subcategory facets,
// net-ranked with gross and refund visible. Facet semantics preserved:
// subcategory rows may overlap (never an additive partition).
func (s Service) CategoriesNormalized(ctx context.Context, req report.Request, kind string) ([]NormalizedCategoryRow, error) {
	if kind != report.DimensionRootCategory && kind != report.DimensionSubcategory {
		return nil, apperr.New(apperr.InvalidInput, "unsupported normalized category dimension")
	}
	rows, err := s.repo.DashboardCategoriesNormalized(ctx, req.Period.StartUTC, req.Period.EndUTC, kindName(kind))
	if err != nil {
		return nil, err
	}
	refundRows, err := s.repo.DashboardCategoriesNormalizedRefunds(ctx, req.Period.StartUTC, req.Period.EndUTC, kindName(kind))
	if err != nil {
		return nil, err
	}
	byKey := map[string]*NormalizedCategoryRow{}
	order := []string{}
	getRow := func(key string, fill func(*NormalizedCategoryRow)) *NormalizedCategoryRow {
		row, ok := byKey[key]
		if !ok {
			row = &NormalizedCategoryRow{NormalizedMinor: "0", RefundNormalizedMinor: "0", NetNormalizedMinor: "0"}
			fill(row)
			byKey[key] = row
			order = append(order, key)
		}
		return row
	}
	for _, r := range rows {
		if r.MissingFx > 0 {
			return nil, apperr.New(apperr.Internal, "projection integrity: USD line without FX snapshot")
		}
		row := getRow(categoryKey(r.Kind, r.ID, r.NameAR, r.NameEN), func(row *NormalizedCategoryRow) {
			row.Kind, row.ID, row.NameAR, row.NameEN = r.Kind, r.ID, r.NameAR, r.NameEN
		})
		row.Units = r.Units
		row.NormalizedMinor = minorString(r.Normalized)
	}
	for _, r := range refundRows {
		if r.MissingFx > 0 {
			return nil, apperr.New(apperr.Internal, "projection integrity: USD return line without FX snapshot")
		}
		row := getRow(categoryKey(r.Kind, r.ID, r.NameAR, r.NameEN), func(row *NormalizedCategoryRow) {
			row.Kind, row.ID, row.NameAR, row.NameEN = r.Kind, r.ID, r.NameAR, r.NameEN
		})
		row.UnitsReturned = r.Units
		row.RefundNormalizedMinor = minorString(r.Normalized)
	}
	out := make([]NormalizedCategoryRow, 0, len(byKey))
	for _, k := range order {
		row := byKey[k]
		net, err := subCheckedI64(minorInt(row.NormalizedMinor), minorInt(row.RefundNormalizedMinor))
		if err != nil {
			return nil, err
		}
		row.NetNormalizedMinor = minorString(net)
		out = append(out, *row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if vi, vj := minorInt(out[i].NetNormalizedMinor), minorInt(out[j].NetNormalizedMinor); vi != vj {
			return vi > vj
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func categoryKey(kind, id, ar, en string) string {
	return kind + "\x00" + id + "\x00" + ar + "\x00" + en
}

func kindName(dimension string) string {
	if dimension == report.DimensionRootCategory {
		return "root"
	}
	return "subcategory"
}

// Branches groups by full shop snapshot tuple + channel (native buckets).
// Refunds attribute to the historical SALE shop tuple (Phase 4A lineage
// guarantee); current shop settings are never consulted.
func (s Service) Branches(ctx context.Context, req report.Request) ([]BranchRow, error) {
	rows, err := s.repo.DashboardBranches(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	if err != nil {
		return nil, err
	}
	refundRows, err := s.repo.DashboardReturnBranches(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	if err != nil {
		return nil, err
	}
	byKey := map[string]*BranchRow{}
	order := []string{}
	keyOf := func(shopAR, shopEN, addrAR, addrEN, phone, footAR, footEN, channel, currency string) string {
		return shopAR + "\x00" + shopEN + "\x00" + addrAR + "\x00" + addrEN + "\x00" + phone +
			"\x00" + footAR + "\x00" + footEN + "\x00" + channel + "\x00" + currency
	}
	for _, r := range rows {
		key := keyOf(r.ShopNameAR, r.ShopNameEN, r.ShopAddressAR, r.ShopAddressEN, r.ShopPhone,
			r.ShopReceiptFooterAR, r.ShopReceiptFooterEN, r.Channel, r.Currency)
		byKey[key] = &BranchRow{
			ShopNameAR: r.ShopNameAR, ShopNameEN: r.ShopNameEN,
			ShopAddressAR: r.ShopAddressAR, ShopAddressEN: r.ShopAddressEN,
			ShopPhone: r.ShopPhone, Channel: r.Channel, Currency: r.Currency,
			Transactions: r.Transactions, Units: r.Units,
			SubtotalMinor: minorString(r.Subtotal), DiscountMinor: minorString(r.Discount),
			TaxMinor: minorString(r.Tax), SalesTotalMinor: minorString(r.Total),
			RefundTotalMinor: "0", ReturnedCostMinor: "0",
		}
		order = append(order, key)
	}
	for _, r := range refundRows {
		key := keyOf(r.ShopNameAR, r.ShopNameEN, r.ShopAddressAR, r.ShopAddressEN, r.ShopPhone,
			r.ShopReceiptFooterAR, r.ShopReceiptFooterEN, r.Channel, r.Currency)
		row, ok := byKey[key]
		if !ok {
			row = &BranchRow{
				ShopNameAR: r.ShopNameAR, ShopNameEN: r.ShopNameEN,
				ShopAddressAR: r.ShopAddressAR, ShopAddressEN: r.ShopAddressEN,
				ShopPhone: r.ShopPhone, Channel: r.Channel, Currency: r.Currency,
				SubtotalMinor: "0", DiscountMinor: "0", TaxMinor: "0", SalesTotalMinor: "0",
				RefundTotalMinor: "0", ReturnedCostMinor: "0",
			}
			byKey[key] = row
			order = append(order, key)
		}
		row.ReturnTransactions = r.Transactions
		row.UnitsReturned = r.Units
		row.RefundTotalMinor = minorString(r.RefundTotal)
		row.ReturnedCostMinor = minorString(r.ReturnedCost)
	}
	out := make([]BranchRow, 0, len(byKey))
	for _, k := range order {
		out = append(out, *byKey[k])
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
	Freshness              report.Freshness `json:"freshness"`
	QueueCount             int64            `json:"queue_count"`
	PendingCount           int64            `json:"pending_count"`
	ProcessedCount         int64            `json:"processed_count"`
	BlockedCount           int64            `json:"blocked_count"`
	RetryCount             int64            `json:"retry_count"`
	ReturnPendingCount     int64            `json:"return_pending_count"`
	ReturnProcessedCount   int64            `json:"return_processed_count"`
	ReturnBlockedCount     int64            `json:"return_blocked_count"`
	ReturnRetryCount       int64            `json:"return_retry_count"`
	ReturnLastErrorEvent   string           `json:"return_last_error_event,omitempty"`
	ReturnLastErrorCode    string           `json:"return_last_error_code,omitempty"`
	ReturnLastErrorLabelAR string           `json:"return_last_error_label_ar,omitempty"`
	ReturnLastErrorLabelEN string           `json:"return_last_error_label_en,omitempty"`
	OldestPendingAt        *string          `json:"oldest_pending_at,omitempty"`
	LastErrorEvent         string           `json:"last_error_event,omitempty"`
	LastErrorCode          string           `json:"last_error_code,omitempty"`
	LastErrorLabelAR       string           `json:"last_error_label_ar,omitempty"`
	LastErrorLabelEN       string           `json:"last_error_label_en,omitempty"`
}

// SyncHealth assembles freshness plus processing-state counts for both
// processors. Sale and return completeness stay separate metrics: sale
// fields keep their frozen meaning, return fields are additive.
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
	retStats, err := s.saleStore.ProcessingStats(ctx, returnrefund.ProcessorReturnProjectionV1)
	if err != nil {
		return SyncHealthItem{}, err
	}
	ar, en := safeDiagnostic(stats.LastErrorCode)
	retAR, retEN := safeDiagnostic(retStats.LastErrorCode)
	out := SyncHealthItem{
		Freshness:            fresh,
		QueueCount:           stats.PendingCount,
		PendingCount:         stats.Counts[sale.ProcPending],
		ProcessedCount:       stats.Counts[sale.ProcProcessed],
		BlockedCount:         stats.Counts[sale.ProcBlocked],
		RetryCount:           stats.Counts[sale.ProcRetry],
		LastErrorEvent:       stats.LastErrorEvent,
		LastErrorCode:        stats.LastErrorCode,
		LastErrorLabelAR:     ar,
		LastErrorLabelEN:     en,
		ReturnPendingCount:   retStats.Counts[returnrefund.ProcPending],
		ReturnProcessedCount: retStats.Counts[returnrefund.ProcProcessed],
		ReturnBlockedCount:   retStats.Counts[returnrefund.ProcBlocked],
		ReturnRetryCount:     retStats.Counts[returnrefund.ProcRetry],
		// Return diagnostics ride their own channel: a Return error must
		// never overwrite a concurrent Sale error (or vice versa).
		ReturnLastErrorEvent:   retStats.LastErrorEvent,
		ReturnLastErrorCode:    retStats.LastErrorCode,
		ReturnLastErrorLabelAR: retAR,
		ReturnLastErrorLabelEN: retEN,
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
	case "RETURN_REFUND_ID_CONFLICT":
		return "تعارض مرتجعات: حدثان بنفس رقم الإرجاع", "Return conflict: two events share one return ID"
	case "SALE_DEPENDENCY_WAIT":
		return "بانتظار البيع الأصلي", "Waiting for the original sale"
	case "RETURN_LINE_UNKNOWN":
		return "بند إرجاع غير معروف", "Return references an unknown sale line"
	case "RETURN_CURRENCY_MISMATCH":
		return "عملة الإرجاع مختلفة", "Return currency differs from sale"
	case "RETURN_FX_MISMATCH":
		return "سعر صرف الإرجاع مختلف", "Return FX differs from sale FX"
	case "CUMULATIVE_OVER_RETURN":
		return "تجاوز كمية الإرجاع", "Returns exceed the sold quantity"
	case "CUMULATIVE_REFUND_EXCEEDED":
		return "تجاوز مبلغ الاسترداد", "Refunds exceed the sale total"
	case "CATALOG_DEPENDENCY_WAIT":
		return "بانتظار عنصر كتالوج", "Waiting for a catalog dependency"
	case "CATALOG_REVISION_CONFLICT":
		return "تعارض مراجعة كتالوج", "Catalog revision conflict"
	case "CATALOG_CATEGORY_CYCLE":
		return "دورة فئات مرفوضة", "Category cycle rejected"
	case "CATALOG_CATEGORY_DEPTH":
		return "تجاوز عمق الفئات", "Category depth exceeded"
	case "CATALOG_INVALID_RELATION":
		return "علاقة كتالوج غير صالحة", "Invalid catalog relation"
	case "CATALOG_GRAPH_CONFLICT":
		return "تعارض الرسم البياني", "Category change conflicts with products"
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
	ret, err := s.repo.ReturnProjectionFreshness(ctx)
	if err != nil {
		return report.Freshness{}, err
	}
	return report.Freshness{
		LatestSaleEventReceivedAt:       row.LatestReceivedAt,
		LatestProjectedSaleOccurredAt:   row.LatestOccurredAt,
		ProjectionBacklogCount:          row.Backlog,
		BlockedSaleEventCount:           row.Blocked,
		CloudProjectionComplete:         row.Backlog == 0,
		LatestReturnEventReceivedAt:     ret.LatestReceivedAt,
		LatestProjectedReturnOccurredAt: ret.LatestOccurredAt,
		ReturnBacklogCount:              ret.Backlog,
		ReturnBlockedCount:              ret.Blocked,
		ReturnProjectionComplete:        ret.Backlog == 0,
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
