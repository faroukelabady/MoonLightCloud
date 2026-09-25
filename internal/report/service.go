package report

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
)

// Breakdown dimensions (fixed enum; handlers switch, queries stay static).
const (
	DimensionProduct      = "product"
	DimensionRootCategory = "root_category"
	DimensionSubcategory  = "subcategory"
	DimensionCashier      = "cashier"
	DimensionChannel      = "channel"
)

// Supported currencies for v1 reporting (no new currencies in Phase 3A).
var supportedCurrencies = map[string]bool{"EGP": true, "USD": true}

// CurrencyTotal is one currency bucket for summary and daily responses,
// where extended historical line cost is computed and therefore truthful.
// Breakdown rows never use this type (see LineSaleTotal/SaleCurrencyTotal).
// EGP and USD are never summed: no FX conversion rule exists for reporting.
// Signed fields (net, never clamped): a refund-heavy period reports
// negative net — never use the nonnegative intake Money schema here.
type CurrencyTotal struct {
	Currency        string `json:"currency"`
	SubtotalMinor   int64  `json:"subtotal_minor"`
	DiscountMinor   int64  `json:"discount_minor"`
	TaxMinor        int64  `json:"tax_minor"`
	SalesTotalMinor int64  `json:"sales_total_minor"`
	// LineCostMinor sums historical line cost snapshots where present.
	// It is a cost snapshot total, not a profit/margin basis.
	LineCostMinor int64 `json:"line_cost_minor"`
	// RefundTotalMinor sums authoritative return reversals in period.
	RefundTotalMinor int64 `json:"refund_total_minor"`
	// NetSalesMinor is SalesTotalMinor minus RefundTotalMinor (signed).
	NetSalesMinor int64 `json:"net_sales_minor"`
	// ReturnedUnits counts returned units in period.
	ReturnedUnits int64 `json:"returned_units"`
	// ReturnedCostMinor sums extended historical return-line costs.
	ReturnedCostMinor int64 `json:"returned_cost_minor"`
	// NetCostMinor is LineCostMinor minus ReturnedCostMinor (signed).
	NetCostMinor int64 `json:"net_cost_minor"`
}

// SaleCurrencyTotal is one currency bucket for Sale-header breakdown rows
// (cashier, channel): exact header subtotal/discount/tax/sales_total.
// It deliberately carries no cost field — header dimensions do not compute
// historical cost, and a zero would falsely claim zero cost. Summary and
// daily responses (which do compute extended cost) keep CurrencyTotal.
type SaleCurrencyTotal struct {
	Currency        string `json:"currency"`
	SubtotalMinor   int64  `json:"subtotal_minor"`
	DiscountMinor   int64  `json:"discount_minor"`
	TaxMinor        int64  `json:"tax_minor"`
	SalesTotalMinor int64  `json:"sales_total_minor"`
	// RefundTotalMinor sums return reversals attributed to this header
	// row (original sale's cashier/channel); net is derivable.
	// Signed: never clamped.
	RefundTotalMinor int64 `json:"refund_total_minor"`
	// NetSalesMinor is SalesTotalMinor minus RefundTotalMinor (signed).
	NetSalesMinor int64 `json:"net_sales_minor"`
}

// PaymentTotal is tender by method. Secondary to finalized Sale totals:
// revenue is never redefined from payment rows.
type PaymentTotal struct {
	Method      string `json:"method"`
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
	ChangeMinor int64  `json:"change_minor"`
}

// PeriodMeta describes exactly what was queried.
type PeriodMeta struct {
	Kind              string `json:"kind"`
	Timezone          string `json:"timezone"`
	StartLocal        string `json:"start_local"`
	EndLocalExclusive string `json:"end_local_exclusive"`
	StartUTC          string `json:"start_utc"`
	EndUTC            string `json:"end_utc"`
}

// Freshness is honest Cloud-side projection metadata. It proves nothing
// about unsent Desktop outbox events: Cloud cannot observe those.
type Freshness struct {
	// LatestSaleEventReceivedAt is the newest accepted sale event (transport).
	LatestSaleEventReceivedAt *time.Time `json:"latest_sale_event_received_at"`
	// LatestProjectedSaleOccurredAt is the newest projected business time.
	LatestProjectedSaleOccurredAt *time.Time `json:"latest_projected_sale_occurred_at"`
	ProjectionBacklogCount        int64      `json:"projection_backlog_count"`
	BlockedSaleEventCount         int64      `json:"blocked_sale_event_count"`
	// CloudProjectionComplete is narrowly true when every accepted sale
	// event reached terminal processed/blocked state with no backlog.
	// It does NOT mean Retail is fully synchronized, and it says nothing
	// about returns: see ReturnProjectionComplete.
	CloudProjectionComplete bool `json:"cloud_projection_complete"`
	// LatestReturnEventReceivedAt is the newest accepted return event.
	LatestReturnEventReceivedAt *time.Time `json:"latest_return_event_received_at"`
	// LatestProjectedReturnOccurredAt is the newest projected return time.
	LatestProjectedReturnOccurredAt *time.Time `json:"latest_projected_return_occurred_at"`
	// ReturnBacklogCount is accepted returns without terminal state.
	ReturnBacklogCount int64 `json:"return_backlog_count"`
	// ReturnBlockedCount is terminally blocked returns.
	ReturnBlockedCount int64 `json:"return_blocked_count"`
	// ReturnProjectionComplete is true when every accepted return event
	// reached terminal processed/blocked state with no backlog.
	ReturnProjectionComplete bool `json:"return_projection_complete"`
}

// Summary is the sales summary response. TransactionCount and UnitsSold
// stay finalized-sale-only; return activity rides in the return-specific
// counters and per-currency buckets (never conflated).
type Summary struct {
	GeneratedAt            time.Time       `json:"generated_at"`
	Timezone               string          `json:"timezone"`
	Period                 PeriodMeta      `json:"period"`
	TransactionCount       int64           `json:"transaction_count"`
	UnitsSold              int64           `json:"units_sold"`
	ReturnTransactionCount int64           `json:"return_transaction_count"`
	UnitsReturned          int64           `json:"units_returned"`
	CurrencyTotals         []CurrencyTotal `json:"currency_totals"`
	PaymentTotals          []PaymentTotal  `json:"payment_totals"`
	Freshness              Freshness       `json:"freshness"`
}

// DailyRow is one store-local calendar date (ascending in Daily).
// Day-level transaction/unit counts stay sale-scoped; return activity
// rides in the return-specific counters and per-currency buckets.
type DailyRow struct {
	Date               string          `json:"date"`
	Transactions       int64           `json:"transactions"`
	Units              int64           `json:"units"`
	ReturnTransactions int64           `json:"return_transactions"`
	UnitsReturned      int64           `json:"units_returned"`
	CurrencyTotals     []CurrencyTotal `json:"currency_totals"`
}

// Daily is the daily series response.
type Daily struct {
	GeneratedAt time.Time  `json:"generated_at"`
	Timezone    string     `json:"timezone"`
	Period      PeriodMeta `json:"period"`
	Days        []DailyRow `json:"days"`
	Freshness   Freshness  `json:"freshness"`
}

// BreakdownRow is one dimension value. Money stays in per-currency
// buckets. Two mutually exclusive financial groups exist, populated by
// dimension family (never mixed in one row):
//   - line-level dimensions (product, root_category, subcategory) set
//     LineSales: pre-adjustment historical line snapshot amounts.
//     Sale-level discount/tax are never allocated to lines.
//   - Sale-header-level dimensions (cashier, channel) set CurrencyTotals:
//     exact header subtotal/discount/tax/sales_total aggregates.
//
// line_sales_minor therefore has one stable meaning everywhere it appears.
type BreakdownRow struct {
	Dimension string `json:"dimension"`
	// Product fields.
	ProductID   *string `json:"product_id,omitempty"`
	SKU         *string `json:"sku,omitempty"`
	ProductName *string `json:"product_name,omitempty"`
	// Category fields.
	ClassificationKind *string `json:"classification_kind,omitempty"`
	ClassificationID   *string `json:"classification_id,omitempty"`
	NameAR             *string `json:"name_ar,omitempty"`
	NameEN             *string `json:"name_en,omitempty"`
	// Cashier fields (explicit null bucket when unattributed).
	CashierID   *string `json:"cashier_id,omitempty"`
	CashierName *string `json:"cashier_name,omitempty"`
	// Channel field.
	Channel *string `json:"channel,omitempty"`
	// Transactions is populated for header-additive dimensions
	// (cashier, channel) only, counting finalized sales — never returns.
	Transactions int64 `json:"transactions,omitempty"`
	// ReturnTransactions counts return transactions for header-additive
	// dimensions; Units counts sold units, UnitsReturned returned units.
	ReturnTransactions int64 `json:"return_transactions,omitempty"`
	Units              int64 `json:"units"`
	UnitsReturned      int64 `json:"units_returned,omitempty"`
	// LineSales holds per-currency line snapshot totals (line-level
	// dimensions only; omitted otherwise).
	LineSales []LineSaleTotal `json:"line_sales,omitempty"`
	// CurrencyTotals holds exact Sale-header aggregates (cashier and
	// channel dimensions only; omitted otherwise). The element type has
	// no cost field by design: header dimensions do not compute cost.
	CurrencyTotals []SaleCurrencyTotal `json:"currency_totals,omitempty"`
}

// LineSaleTotal is one currency bucket of line snapshot money. Refund
// fields reverse the same attribution (net is derivable); signed, never
// clamped.
type LineSaleTotal struct {
	Currency              string `json:"currency"`
	LineSalesMinor        int64  `json:"line_sales_minor"`
	LineCostMinor         int64  `json:"line_cost_minor"`
	LineRefundMinor       int64  `json:"line_refund_minor"`
	LineReturnedCostMinor int64  `json:"line_returned_cost_minor"`
}

// Breakdown is the dimension response.
type Breakdown struct {
	GeneratedAt time.Time      `json:"generated_at"`
	Timezone    string         `json:"timezone"`
	Period      PeriodMeta     `json:"period"`
	Dimension   string         `json:"dimension"`
	Rows        []BreakdownRow `json:"rows"`
	Freshness   Freshness      `json:"freshness"`
}

// SummaryRow, DailyRowRaw, etc. are repository row shapes (currency-split).
type (
	SummaryRow struct {
		Currency     string
		Transactions int64
		Units        int64
		Subtotal     int64
		Discount     int64
		Tax          int64
		SalesTotal   int64
		LineCost     int64
	}
	PaymentRow struct {
		Method   string
		Currency string
		Amount   int64
		Change   int64
	}
	DailyRowRaw struct {
		Date         string
		Currency     string
		Transactions int64
		Units        int64
		Subtotal     int64
		Discount     int64
		Tax          int64
		SalesTotal   int64
		LineCost     int64
	}
	ProductRow struct {
		ProductID   *string
		SKU         string
		ProductName string
		Units       int64
		Currency    string
		LineSales   int64
		LineCost    int64
	}
	CategoryRow struct {
		Kind     string
		ID       string
		NameAR   string
		NameEN   string
		Units    int64
		Currency string
		Sales    int64
		Cost     int64
	}
	CashierRow struct {
		CashierID   *string
		CashierName *string
		Units       int64
		// Header-level metrics per cashier (channel/cashier are additive:
		// one sale has one channel and one attribution state).
		Transactions int64
		Currency     string
		Subtotal     int64
		Discount     int64
		Tax          int64
		SalesTotal   int64
	}
	ChannelRow struct {
		Channel      string
		Transactions int64
		Units        int64
		Currency     string
		Subtotal     int64
		Discount     int64
		Tax          int64
		SalesTotal   int64
	}
	FreshnessRow struct {
		LatestReceivedAt *time.Time
		LatestOccurredAt *time.Time
		Backlog          int64
		Blocked          int64
	}
	// Refund reporting rows mirror the sale shapes over the return
	// projection (windowed on return occurred_at). Money is reversal
	// economics; counts are return activity, never sale activity.
	RefundSummaryRow struct {
		Currency         string
		Transactions     int64
		Units            int64
		GrossRefunded    int64
		DiscountRefunded int64
		TaxRefunded      int64
		RefundTotal      int64
		ReturnedCost     int64
	}
	RefundDailyRow struct {
		Date         string
		Currency     string
		Transactions int64
		Units        int64
		RefundTotal  int64
		ReturnedCost int64
	}
	RefundProductRow struct {
		ProductID    *string
		SKU          string
		ProductName  string
		Units        int64
		Currency     string
		Refund       int64
		ReturnedCost int64
	}
	RefundCategoryRow struct {
		Kind         string
		ID           string
		NameAR       string
		NameEN       string
		Units        int64
		Currency     string
		Refund       int64
		ReturnedCost int64
	}
	RefundCashierRow struct {
		// Cashier attribution follows the ORIGINAL sale (net performance);
		// the Return-processing actor is retained historically for
		// audit/future use but is not currently exposed in dashboard
		// activity.
		CashierID    *string
		CashierName  *string
		Transactions int64
		Units        int64
		Currency     string
		RefundTotal  int64
	}
	RefundChannelRow struct {
		// Channel attribution follows the original sale channel.
		Channel      string
		Transactions int64
		Units        int64
		Currency     string
		RefundTotal  int64
	}
	ReturnFreshnessRow struct {
		LatestReceivedAt *time.Time
		LatestOccurredAt *time.Time
		Backlog          int64
		Blocked          int64
	}
)

// Repository is the reporting storage boundary, implemented by the
// postgres adapter with static parameterized sqlc queries. One explicit
// operation per report need — never QueryReport(dimension string).
type Repository interface {
	SalesSummary(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]SummaryRow, error)
	SalesPayments(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]PaymentRow, error)
	SalesDaily(ctx context.Context, startUTC, endUTC time.Time, currency, timezone string) ([]DailyRowRaw, error)
	SalesByProduct(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]ProductRow, error)
	SalesByRootCategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]CategoryRow, error)
	SalesBySubcategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]CategoryRow, error)
	SalesByCashier(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]CashierRow, error)
	SalesByChannel(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]ChannelRow, error)
	SalesProjectionFreshness(ctx context.Context) (FreshnessRow, error)
	RefundsSummary(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundSummaryRow, error)
	RefundsDaily(ctx context.Context, startUTC, endUTC time.Time, currency, timezone string) ([]RefundDailyRow, error)
	RefundsByProduct(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundProductRow, error)
	RefundsByRootCategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundCategoryRow, error)
	RefundsBySubcategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundCategoryRow, error)
	RefundsByCashier(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundCashierRow, error)
	RefundsByChannel(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundChannelRow, error)
	ReturnProjectionFreshness(ctx context.Context) (ReturnFreshnessRow, error)
}

// Service is the reusable reporting authority for dashboards and jobs.
type Service struct {
	repo  Repository
	clock clock.Clock
	loc   *time.Location
}

// NewService wires the reporting service with the store timezone.
func NewService(r Repository, c clock.Clock, loc *time.Location) Service {
	return Service{repo: r, clock: c, loc: loc}
}

// Request is a validated report request (one captured now for all math).
type Request struct {
	Period   Period
	Currency string // "" means all currencies, separately bucketed
	now      time.Time
}

// GeneratedAt returns the single captured request instant.
func (r Request) GeneratedAt() time.Time { return r.now }

// ParseRequest validates query parameters and resolves the period.
func (s Service) ParseRequest(kind, fromDate, toDate, currency string) (Request, error) {
	if currency != "" && !supportedCurrencies[currency] {
		return Request{}, apperr.New(apperr.InvalidInput,
			fmt.Sprintf("unsupported currency %q: want EGP|USD", currency))
	}
	now := s.clock.Now()
	period, err := ResolvePeriod(kind, fromDate, toDate, now, s.loc)
	if err != nil {
		return Request{}, err
	}
	return Request{Period: period, Currency: currency, now: now}, nil
}

// ParseDimension validates the breakdown dimension enum (no dynamic SQL:
// callers switch on the returned constant).
func ParseDimension(d string) (string, error) {
	switch d {
	case DimensionProduct, DimensionRootCategory, DimensionSubcategory, DimensionCashier, DimensionChannel:
		return d, nil
	default:
		return "", apperr.New(apperr.InvalidInput,
			fmt.Sprintf("unsupported dimension %q: want product|root_category|subcategory|cashier|channel", d))
	}
}

// subChecked/subtracts with overflow failure (net may be negative, but
// must never wrap).
func subChecked(a, b int64) (int64, error) {
	const maxInt64 = int64(^uint64(0) >> 1)
	const minInt64 = -maxInt64 - 1
	if b > 0 && a < minInt64+b {
		return 0, fmt.Errorf("overflow")
	}
	if b < 0 && a > maxInt64+b {
		return 0, fmt.Errorf("overflow")
	}
	return a - b, nil
}

func addChecked(a, b int64) (int64, error) {
	const maxInt64 = int64(^uint64(0) >> 1)
	const minInt64 = -maxInt64 - 1
	if b > 0 && a > maxInt64-b {
		return 0, fmt.Errorf("overflow")
	}
	if b < 0 && a < minInt64-b {
		return 0, fmt.Errorf("overflow")
	}
	return a + b, nil
}

func metaOf(p Period, loc *time.Location) PeriodMeta {
	const layout = "2006-01-02T15:04:05.999999999Z07:00"
	return PeriodMeta{
		Kind: p.Kind, Timezone: p.Timezone,
		StartLocal: p.StartLocal.Format(layout), EndLocalExclusive: p.EndLocalExclusive.Format(layout),
		StartUTC: p.StartUTC.Format(layout), EndUTC: p.EndUTC.Format(layout),
	}
}

// Summary assembles the sales summary with currency buckets + freshness.
// Gross sale semantics are frozen; refund/net fields merge additively from
// the return projection (checked arithmetic, never clamped, net may be
// negative). TransactionCount/UnitsSold stay sale-only; return activity
// rides in ReturnTransactionCount/UnitsReturned.
func (s Service) Summary(ctx context.Context, req Request) (Summary, error) {
	rows, err := s.repo.SalesSummary(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	if err != nil {
		return Summary{}, err
	}
	pays, err := s.repo.SalesPayments(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	if err != nil {
		return Summary{}, err
	}
	refunds, err := s.repo.RefundsSummary(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	if err != nil {
		return Summary{}, err
	}
	fresh, err := s.freshness(ctx)
	if err != nil {
		return Summary{}, err
	}
	out := Summary{
		GeneratedAt: req.now, Timezone: req.Period.Timezone,
		Period: metaOf(req.Period, s.loc), Freshness: fresh,
		CurrencyTotals: []CurrencyTotal{}, PaymentTotals: []PaymentTotal{},
	}
	byCurrency := map[string]int{}
	for _, r := range rows {
		out.TransactionCount += r.Transactions
		out.UnitsSold += r.Units
		out.CurrencyTotals = append(out.CurrencyTotals, CurrencyTotal{
			Currency: r.Currency, SubtotalMinor: r.Subtotal, DiscountMinor: r.Discount,
			TaxMinor: r.Tax, SalesTotalMinor: r.SalesTotal, LineCostMinor: r.LineCost,
		})
		byCurrency[r.Currency] = len(out.CurrencyTotals) - 1
	}
	for _, r := range refunds {
		out.ReturnTransactionCount += r.Transactions
		out.UnitsReturned += r.Units
		i, ok := byCurrency[r.Currency]
		if !ok {
			out.CurrencyTotals = append(out.CurrencyTotals, CurrencyTotal{Currency: r.Currency})
			i = len(out.CurrencyTotals) - 1
			byCurrency[r.Currency] = i
		}
		bucket := &out.CurrencyTotals[i]
		bucket.RefundTotalMinor = r.RefundTotal
		bucket.ReturnedUnits = r.Units
		bucket.ReturnedCostMinor = r.ReturnedCost
	}
	for i := range out.CurrencyTotals {
		bucket := &out.CurrencyTotals[i]
		net, err := subChecked(bucket.SalesTotalMinor, bucket.RefundTotalMinor)
		if err != nil {
			return Summary{}, err
		}
		bucket.NetSalesMinor = net
		netCost, err := subChecked(bucket.LineCostMinor, bucket.ReturnedCostMinor)
		if err != nil {
			return Summary{}, err
		}
		bucket.NetCostMinor = netCost
	}
	for _, p := range pays {
		out.PaymentTotals = append(out.PaymentTotals, PaymentTotal{
			Method: p.Method, Currency: p.Currency, AmountMinor: p.Amount, ChangeMinor: p.Change,
		})
	}
	sortCurrencyTotals(out.CurrencyTotals)
	return out, nil
}

// Daily assembles the per-day series in ascending date order. Refund-only
// days create their own rows (a day with refunds but no sales is real
// activity); net may be negative and is never clamped.
func (s Service) Daily(ctx context.Context, req Request) (Daily, error) {
	rows, err := s.repo.SalesDaily(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency, req.Period.Timezone)
	if err != nil {
		return Daily{}, err
	}
	refundRows, err := s.repo.RefundsDaily(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency, req.Period.Timezone)
	if err != nil {
		return Daily{}, err
	}
	fresh, err := s.freshness(ctx)
	if err != nil {
		return Daily{}, err
	}
	out := Daily{
		GeneratedAt: req.now, Timezone: req.Period.Timezone,
		Period: metaOf(req.Period, s.loc), Freshness: fresh, Days: []DailyRow{},
	}
	byDay := map[string]int{}
	byBucket := map[string]map[string]int{}
	order := []string{}
	dayOf := func(date string) int {
		i, ok := byDay[date]
		if !ok {
			out.Days = append(out.Days, DailyRow{Date: date, CurrencyTotals: []CurrencyTotal{}})
			i = len(out.Days) - 1
			byDay[date] = i
			order = append(order, date)
			byBucket[date] = map[string]int{}
		}
		return i
	}
	bucketOf := func(date, currency string) *CurrencyTotal {
		di := dayOf(date)
		if bi, ok := byBucket[date][currency]; ok {
			return &out.Days[di].CurrencyTotals[bi]
		}
		out.Days[di].CurrencyTotals = append(out.Days[di].CurrencyTotals, CurrencyTotal{Currency: currency})
		bi := len(out.Days[di].CurrencyTotals) - 1
		byBucket[date][currency] = bi
		return &out.Days[di].CurrencyTotals[bi]
	}
	for _, r := range rows {
		di := dayOf(r.Date)
		out.Days[di].Transactions += r.Transactions
		out.Days[di].Units += r.Units
		bucket := bucketOf(r.Date, r.Currency)
		bucket.SubtotalMinor = r.Subtotal
		bucket.DiscountMinor = r.Discount
		bucket.TaxMinor = r.Tax
		bucket.SalesTotalMinor = r.SalesTotal
		bucket.LineCostMinor = r.LineCost
	}
	for _, r := range refundRows {
		di := dayOf(r.Date)
		out.Days[di].ReturnTransactions += r.Transactions
		out.Days[di].UnitsReturned += r.Units
		bucket := bucketOf(r.Date, r.Currency)
		bucket.RefundTotalMinor = r.RefundTotal
		bucket.ReturnedUnits = r.Units
		bucket.ReturnedCostMinor = r.ReturnedCost
	}
	sort.Strings(order)
	ordered := Daily{
		GeneratedAt: req.now, Timezone: req.Period.Timezone,
		Period: metaOf(req.Period, s.loc), Freshness: fresh, Days: []DailyRow{},
	}
	for _, d := range order {
		day := out.Days[byDay[d]]
		for i := range day.CurrencyTotals {
			bucket := &day.CurrencyTotals[i]
			net, err := subChecked(bucket.SalesTotalMinor, bucket.RefundTotalMinor)
			if err != nil {
				return Daily{}, err
			}
			bucket.NetSalesMinor = net
			netCost, err := subChecked(bucket.LineCostMinor, bucket.ReturnedCostMinor)
			if err != nil {
				return Daily{}, err
			}
			bucket.NetCostMinor = netCost
		}
		sortCurrencyTotals(day.CurrencyTotals)
		ordered.Days = append(ordered.Days, day)
	}
	return ordered, nil
}

// Breakdown assembles one fixed dimension via its static query.
func (s Service) Breakdown(ctx context.Context, req Request, dimension string) (Breakdown, error) {
	fresh, err := s.freshness(ctx)
	if err != nil {
		return Breakdown{}, err
	}
	out := Breakdown{
		GeneratedAt: req.now, Timezone: req.Period.Timezone,
		Period: metaOf(req.Period, s.loc), Dimension: dimension,
		Rows: []BreakdownRow{}, Freshness: fresh,
	}
	switch dimension {
	case DimensionProduct:
		rows, err := s.repo.SalesByProduct(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
		if err != nil {
			return Breakdown{}, err
		}
		refundRows, err := s.repo.RefundsByProduct(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
		if err != nil {
			return Breakdown{}, err
		}
		// Rows are heap-allocated (pointers stay valid); per-currency
		// entry indexes avoid dangling across LineSales appends.
		byKey := map[string]*BreakdownRow{}
		order := []string{}
		lineEntry := map[string]map[string]int{}
		getRow := func(key string, fill func(*BreakdownRow)) *BreakdownRow {
			row, ok := byKey[key]
			if !ok {
				row = &BreakdownRow{Dimension: dimension, LineSales: []LineSaleTotal{}}
				fill(row)
				byKey[key] = row
				order = append(order, key)
				lineEntry[key] = map[string]int{}
			}
			return row
		}
		entryOf := func(key, currency string) *LineSaleTotal {
			row := byKey[key]
			if ei, ok := lineEntry[key][currency]; ok {
				return &row.LineSales[ei]
			}
			row.LineSales = append(row.LineSales, LineSaleTotal{Currency: currency})
			ei := len(row.LineSales) - 1
			lineEntry[key][currency] = ei
			return &row.LineSales[ei]
		}
		for _, r := range rows {
			key := ptrStr(r.ProductID) + "\x00" + r.SKU + "\x00" + r.ProductName
			row := getRow(key, func(row *BreakdownRow) {
				row.ProductID, row.SKU, row.ProductName = r.ProductID, strPtr(r.SKU), strPtr(r.ProductName)
			})
			row.Units += r.Units
			entry := entryOf(key, r.Currency)
			entry.LineSalesMinor = r.LineSales
			entry.LineCostMinor = r.LineCost
		}
		for _, r := range refundRows {
			key := ptrStr(r.ProductID) + "\x00" + r.SKU + "\x00" + r.ProductName
			row := getRow(key, func(row *BreakdownRow) {
				row.ProductID, row.SKU, row.ProductName = r.ProductID, strPtr(r.SKU), strPtr(r.ProductName)
			})
			row.UnitsReturned += r.Units
			entry := entryOf(key, r.Currency)
			entry.LineRefundMinor = r.Refund
			entry.LineReturnedCostMinor = r.ReturnedCost
		}
		for _, row := range byKey {
			sort.Slice(row.LineSales, func(i, j int) bool { return row.LineSales[i].Currency < row.LineSales[j].Currency })
		}
		out.Rows = sortProductRows(byKey, order, req.Currency)
	case DimensionRootCategory, DimensionSubcategory:
		var rows []CategoryRow
		var refundRows []RefundCategoryRow
		if dimension == DimensionRootCategory {
			rows, err = s.repo.SalesByRootCategory(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
			if err != nil {
				return Breakdown{}, err
			}
			refundRows, err = s.repo.RefundsByRootCategory(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
		} else {
			rows, err = s.repo.SalesBySubcategory(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
			if err != nil {
				return Breakdown{}, err
			}
			refundRows, err = s.repo.RefundsBySubcategory(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
		}
		if err != nil {
			return Breakdown{}, err
		}
		byKey := map[string]*BreakdownRow{}
		order := []string{}
		lineEntry := map[string]map[string]int{}
		getRow := func(key string, fill func(*BreakdownRow)) *BreakdownRow {
			row, ok := byKey[key]
			if !ok {
				row = &BreakdownRow{Dimension: dimension, LineSales: []LineSaleTotal{}}
				fill(row)
				byKey[key] = row
				order = append(order, key)
				lineEntry[key] = map[string]int{}
			}
			return row
		}
		for _, r := range rows {
			// Snapshot identity: kind + id + both historical names.
			// Renamed snapshots of one category stay separate rows;
			// buckets merge only within exact snapshot identity.
			key := r.Kind + "\x00" + r.ID + "\x00" + r.NameAR + "\x00" + r.NameEN
			kind, id, ar, en := r.Kind, r.ID, r.NameAR, r.NameEN
			row := getRow(key, func(row *BreakdownRow) {
				row.ClassificationKind, row.ClassificationID = &kind, &id
				row.NameAR, row.NameEN = &ar, &en
			})
			row.Units += r.Units
			row.LineSales = append(row.LineSales, LineSaleTotal{
				Currency: r.Currency, LineSalesMinor: r.Sales, LineCostMinor: r.Cost})
			lineEntry[key][r.Currency] = len(row.LineSales) - 1
		}
		for _, r := range refundRows {
			key := r.Kind + "\x00" + r.ID + "\x00" + r.NameAR + "\x00" + r.NameEN
			kind, id, ar, en := r.Kind, r.ID, r.NameAR, r.NameEN
			row := getRow(key, func(row *BreakdownRow) {
				row.ClassificationKind, row.ClassificationID = &kind, &id
				row.NameAR, row.NameEN = &ar, &en
			})
			row.UnitsReturned += r.Units
			ei, ok := lineEntry[key][r.Currency]
			if !ok {
				row.LineSales = append(row.LineSales, LineSaleTotal{Currency: r.Currency})
				ei = len(row.LineSales) - 1
				lineEntry[key][r.Currency] = ei
			}
			row.LineSales[ei].LineRefundMinor = r.Refund
			row.LineSales[ei].LineReturnedCostMinor = r.ReturnedCost
		}
		for _, row := range byKey {
			sort.Slice(row.LineSales, func(i, j int) bool { return row.LineSales[i].Currency < row.LineSales[j].Currency })
		}
		out.Rows = sortCategoryRows(byKey, order, req.Currency)
	case DimensionCashier:
		rows, err := s.repo.SalesByCashier(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
		if err != nil {
			return Breakdown{}, err
		}
		refundRows, err := s.repo.RefundsByCashier(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
		if err != nil {
			return Breakdown{}, err
		}
		byKey := map[string]*BreakdownRow{}
		order := []string{}
		bucketOf := map[string]map[string]int{}
		getRow := func(key string, fill func(*BreakdownRow)) *BreakdownRow {
			row, ok := byKey[key]
			if !ok {
				row = &BreakdownRow{Dimension: dimension}
				fill(row)
				byKey[key] = row
				order = append(order, key)
				bucketOf[key] = map[string]int{}
			}
			return row
		}
		for _, r := range rows {
			// Explicit null bucket: unattributed sales group under "".
			// Refunds attribute to the ORIGINAL sale cashier (net
			// performance), never subtracted from another cashier; the
			// Return actor is retained in projection for audit/future
			// surfaces and is not currently exposed in dashboard activity.
			key := ptrStr(r.CashierID) + "\x00" + ptrStr(r.CashierName)
			row := getRow(key, func(row *BreakdownRow) {
				row.CashierID, row.CashierName = r.CashierID, r.CashierName
			})
			row.Units += r.Units
			row.Transactions += r.Transactions
			row.CurrencyTotals = append(row.CurrencyTotals, SaleCurrencyTotal{
				Currency: r.Currency, SubtotalMinor: r.Subtotal, DiscountMinor: r.Discount,
				TaxMinor: r.Tax, SalesTotalMinor: r.SalesTotal})
			bucketOf[key][r.Currency] = len(row.CurrencyTotals) - 1
		}
		for _, r := range refundRows {
			key := ptrStr(r.CashierID) + "\x00" + ptrStr(r.CashierName)
			row := getRow(key, func(row *BreakdownRow) {
				row.CashierID, row.CashierName = r.CashierID, r.CashierName
			})
			row.UnitsReturned += r.Units
			row.ReturnTransactions += r.Transactions
			ei, ok := bucketOf[key][r.Currency]
			if !ok {
				row.CurrencyTotals = append(row.CurrencyTotals, SaleCurrencyTotal{Currency: r.Currency})
				ei = len(row.CurrencyTotals) - 1
				bucketOf[key][r.Currency] = ei
			}
			row.CurrencyTotals[ei].RefundTotalMinor = r.RefundTotal
		}
		for _, row := range byKey {
			for i := range row.CurrencyTotals {
				net, err := subChecked(row.CurrencyTotals[i].SalesTotalMinor, row.CurrencyTotals[i].RefundTotalMinor)
				if err != nil {
					return Breakdown{}, err
				}
				row.CurrencyTotals[i].NetSalesMinor = net
			}
			sortSaleCurrencyTotals(row.CurrencyTotals)
		}
		out.Rows = sortCashierRows(byKey, order)
	case DimensionChannel:
		rows, err := s.repo.SalesByChannel(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
		if err != nil {
			return Breakdown{}, err
		}
		byKey := map[string]*BreakdownRow{}
		order := []string{}
		refundRows, err := s.repo.RefundsByChannel(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
		if err != nil {
			return Breakdown{}, err
		}
		bucketOf := map[string]map[string]int{}
		for _, r := range rows {
			row, ok := byKey[r.Channel]
			if !ok {
				ch := r.Channel
				row = &BreakdownRow{Dimension: dimension, Channel: &ch}
				byKey[r.Channel] = row
				order = append(order, r.Channel)
				bucketOf[r.Channel] = map[string]int{}
			}
			row.Units += r.Units
			row.Transactions += r.Transactions
			row.CurrencyTotals = append(row.CurrencyTotals, SaleCurrencyTotal{
				Currency: r.Currency, SubtotalMinor: r.Subtotal, DiscountMinor: r.Discount,
				TaxMinor: r.Tax, SalesTotalMinor: r.SalesTotal})
			bucketOf[r.Channel][r.Currency] = len(row.CurrencyTotals) - 1
		}
		for _, r := range refundRows {
			// Channel attribution follows the original sale channel.
			row, ok := byKey[r.Channel]
			if !ok {
				ch := r.Channel
				row = &BreakdownRow{Dimension: dimension, Channel: &ch}
				byKey[r.Channel] = row
				order = append(order, r.Channel)
				bucketOf[r.Channel] = map[string]int{}
			}
			row.UnitsReturned += r.Units
			row.ReturnTransactions += r.Transactions
			ei, ok := bucketOf[r.Channel][r.Currency]
			if !ok {
				row.CurrencyTotals = append(row.CurrencyTotals, SaleCurrencyTotal{Currency: r.Currency})
				ei = len(row.CurrencyTotals) - 1
				bucketOf[r.Channel][r.Currency] = ei
			}
			row.CurrencyTotals[ei].RefundTotalMinor = r.RefundTotal
		}
		for _, row := range byKey {
			for i := range row.CurrencyTotals {
				net, err := subChecked(row.CurrencyTotals[i].SalesTotalMinor, row.CurrencyTotals[i].RefundTotalMinor)
				if err != nil {
					return Breakdown{}, err
				}
				row.CurrencyTotals[i].NetSalesMinor = net
			}
			sortSaleCurrencyTotals(row.CurrencyTotals)
		}
		for _, k := range order {
			out.Rows = append(out.Rows, *byKey[k])
		}
		// Stable channel-code order.
		sort.Slice(out.Rows, func(i, j int) bool {
			return ptrStr(out.Rows[i].Channel) < ptrStr(out.Rows[j].Channel)
		})
	default:
		return Breakdown{}, apperr.New(apperr.InvalidInput, "unsupported dimension")
	}
	return out, nil
}

func (s Service) freshness(ctx context.Context) (Freshness, error) {
	row, err := s.repo.SalesProjectionFreshness(ctx)
	if err != nil {
		return Freshness{}, err
	}
	ret, err := s.repo.ReturnProjectionFreshness(ctx)
	if err != nil {
		return Freshness{}, err
	}
	return Freshness{
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

func sortCurrencyTotals(t []CurrencyTotal) {
	sort.Slice(t, func(i, j int) bool { return t[i].Currency < t[j].Currency })
}

// sortSaleCurrencyTotals orders header buckets alphabetically by currency.
func sortSaleCurrencyTotals(t []SaleCurrencyTotal) {
	sort.Slice(t, func(i, j int) bool { return t[i].Currency < t[j].Currency })
}

// sortLineSales orders nested line buckets alphabetically by currency.
// SQL GROUP BY order is never relied upon for response order.
func sortLineSales(t []LineSaleTotal) {
	sort.Slice(t, func(i, j int) bool { return t[i].Currency < t[j].Currency })
}

// Deterministic ordering: units descending, then identity tie-breakers.
// Channel rows arrive one per (channel, currency) and sort by channel code.
func sortProductRows(byKey map[string]*BreakdownRow, order []string, currencyScope string) []BreakdownRow {
	out := rowsInOrder(byKey, order)
	for i := range out {
		sortLineSales(out[i].LineSales)
	}
	// Net-desc ranking in a native currency scope (net = gross − refund for
	// that bucket; negatives sort naturally, never clamped). In the
	// all-currency scope cross-currency net is undefined (EGP and USD are
	// never summed), so the frozen units-desc order is preserved; dashboard
	// All mode ranks by normalized net instead.
	sort.SliceStable(out, func(i, j int) bool {
		if currencyScope != "" {
			ni, nj := bucketNet(out[i].LineSales, currencyScope), bucketNet(out[j].LineSales, currencyScope)
			if ni != nj {
				return ni > nj
			}
		}
		if out[i].Units != out[j].Units {
			return out[i].Units > out[j].Units
		}
		if ptrStr(out[i].ProductID) != ptrStr(out[j].ProductID) {
			return ptrStr(out[i].ProductID) < ptrStr(out[j].ProductID)
		}
		if ptrStr(out[i].SKU) != ptrStr(out[j].SKU) {
			return ptrStr(out[i].SKU) < ptrStr(out[j].SKU)
		}
		return ptrStr(out[i].ProductName) < ptrStr(out[j].ProductName)
	})
	return out
}

func sortCategoryRows(byKey map[string]*BreakdownRow, order []string, currencyScope string) []BreakdownRow {
	out := rowsInOrder(byKey, order)
	for i := range out {
		sortLineSales(out[i].LineSales)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if currencyScope != "" {
			ni, nj := bucketNet(out[i].LineSales, currencyScope), bucketNet(out[j].LineSales, currencyScope)
			if ni != nj {
				return ni > nj
			}
		}
		if out[i].Units != out[j].Units {
			return out[i].Units > out[j].Units
		}
		// Complete historical-identity tie-breaker: kind, ID, both names.
		// Two distinct rows never compare equal, so response order cannot
		// depend on SQL arrival order.
		if ptrStr(out[i].ClassificationKind) != ptrStr(out[j].ClassificationKind) {
			return ptrStr(out[i].ClassificationKind) < ptrStr(out[j].ClassificationKind)
		}
		if ptrStr(out[i].ClassificationID) != ptrStr(out[j].ClassificationID) {
			return ptrStr(out[i].ClassificationID) < ptrStr(out[j].ClassificationID)
		}
		if ptrStr(out[i].NameAR) != ptrStr(out[j].NameAR) {
			return ptrStr(out[i].NameAR) < ptrStr(out[j].NameAR)
		}
		return ptrStr(out[i].NameEN) < ptrStr(out[j].NameEN)
	})
	return out
}

func sortCashierRows(byKey map[string]*BreakdownRow, order []string) []BreakdownRow {
	out := rowsInOrder(byKey, order)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Units != out[j].Units {
			return out[i].Units > out[j].Units
		}
		if ptrStr(out[i].CashierID) != ptrStr(out[j].CashierID) {
			return ptrStr(out[i].CashierID) < ptrStr(out[j].CashierID)
		}
		return ptrStr(out[i].CashierName) < ptrStr(out[j].CashierName)
	})
	return out
}

func rowsInOrder(byKey map[string]*BreakdownRow, order []string) []BreakdownRow {
	// Deterministic: callers pass SQL-ordered keys (units desc, identity
	// tie-break); map assembly preserves that order here.
	out := make([]BreakdownRow, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return out
}

// bucketNet returns gross − refund for one currency bucket (missing bucket
// counts as zero). Both operands are nonnegative by schema, so the
// difference cannot overflow int64; negatives are valid and preserved.
func bucketNet(buckets []LineSaleTotal, currency string) int64 {
	for _, b := range buckets {
		if b.Currency == currency {
			return b.LineSalesMinor - b.LineRefundMinor
		}
	}
	return 0
}

func ptrStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func strPtr(s string) *string { v := s; return &v }
