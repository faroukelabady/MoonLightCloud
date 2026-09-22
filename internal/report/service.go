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
type CurrencyTotal struct {
	Currency        string `json:"currency"`
	SubtotalMinor   int64  `json:"subtotal_minor"`
	DiscountMinor   int64  `json:"discount_minor"`
	TaxMinor        int64  `json:"tax_minor"`
	SalesTotalMinor int64  `json:"sales_total_minor"`
	// LineCostMinor sums historical line cost snapshots where present.
	// It is a cost snapshot total, not a profit/margin basis.
	LineCostMinor int64 `json:"line_cost_minor"`
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
	// It does NOT mean Retail is fully synchronized.
	CloudProjectionComplete bool `json:"cloud_projection_complete"`
}

// Summary is the sales summary response.
type Summary struct {
	GeneratedAt      time.Time       `json:"generated_at"`
	Timezone         string          `json:"timezone"`
	Period           PeriodMeta      `json:"period"`
	TransactionCount int64           `json:"transaction_count"`
	UnitsSold        int64           `json:"units_sold"`
	CurrencyTotals   []CurrencyTotal `json:"currency_totals"`
	PaymentTotals    []PaymentTotal  `json:"payment_totals"`
	Freshness        Freshness       `json:"freshness"`
}

// DailyRow is one store-local calendar date (ascending in Daily).
type DailyRow struct {
	Date           string          `json:"date"`
	Transactions   int64           `json:"transactions"`
	Units          int64           `json:"units"`
	CurrencyTotals []CurrencyTotal `json:"currency_totals"`
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
	// (cashier, channel) only.
	Transactions int64 `json:"transactions,omitempty"`
	Units        int64 `json:"units"`
	// LineSales holds per-currency line snapshot totals (line-level
	// dimensions only; omitted otherwise).
	LineSales []LineSaleTotal `json:"line_sales,omitempty"`
	// CurrencyTotals holds exact Sale-header aggregates (cashier and
	// channel dimensions only; omitted otherwise). The element type has
	// no cost field by design: header dimensions do not compute cost.
	CurrencyTotals []SaleCurrencyTotal `json:"currency_totals,omitempty"`
}

// LineSaleTotal is one currency bucket of line snapshot money.
type LineSaleTotal struct {
	Currency       string `json:"currency"`
	LineSalesMinor int64  `json:"line_sales_minor"`
	LineCostMinor  int64  `json:"line_cost_minor"`
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

func metaOf(p Period, loc *time.Location) PeriodMeta {
	const layout = "2006-01-02T15:04:05.999999999Z07:00"
	return PeriodMeta{
		Kind: p.Kind, Timezone: p.Timezone,
		StartLocal: p.StartLocal.Format(layout), EndLocalExclusive: p.EndLocalExclusive.Format(layout),
		StartUTC: p.StartUTC.Format(layout), EndUTC: p.EndUTC.Format(layout),
	}
}

// Summary assembles the sales summary with currency buckets + freshness.
func (s Service) Summary(ctx context.Context, req Request) (Summary, error) {
	rows, err := s.repo.SalesSummary(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	if err != nil {
		return Summary{}, err
	}
	pays, err := s.repo.SalesPayments(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
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
	for _, r := range rows {
		out.TransactionCount += r.Transactions
		out.UnitsSold += r.Units
		out.CurrencyTotals = append(out.CurrencyTotals, CurrencyTotal{
			Currency: r.Currency, SubtotalMinor: r.Subtotal, DiscountMinor: r.Discount,
			TaxMinor: r.Tax, SalesTotalMinor: r.SalesTotal, LineCostMinor: r.LineCost,
		})
	}
	for _, p := range pays {
		out.PaymentTotals = append(out.PaymentTotals, PaymentTotal{
			Method: p.Method, Currency: p.Currency, AmountMinor: p.Amount, ChangeMinor: p.Change,
		})
	}
	sortCurrencyTotals(out.CurrencyTotals)
	return out, nil
}

// Daily assembles the per-day series in ascending date order.
func (s Service) Daily(ctx context.Context, req Request) (Daily, error) {
	rows, err := s.repo.SalesDaily(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency, req.Period.Timezone)
	if err != nil {
		return Daily{}, err
	}
	fresh, err := s.freshness(ctx)
	if err != nil {
		return Daily{}, err
	}
	byDay := map[string]*DailyRow{}
	order := []string{}
	for _, r := range rows {
		day, ok := byDay[r.Date]
		if !ok {
			day = &DailyRow{Date: r.Date, CurrencyTotals: []CurrencyTotal{}}
			byDay[r.Date] = day
			order = append(order, r.Date)
		}
		day.Transactions += r.Transactions
		day.Units += r.Units
		day.CurrencyTotals = append(day.CurrencyTotals, CurrencyTotal{
			Currency: r.Currency, SubtotalMinor: r.Subtotal, DiscountMinor: r.Discount,
			TaxMinor: r.Tax, SalesTotalMinor: r.SalesTotal, LineCostMinor: r.LineCost,
		})
	}
	sort.Strings(order)
	out := Daily{
		GeneratedAt: req.now, Timezone: req.Period.Timezone,
		Period: metaOf(req.Period, s.loc), Freshness: fresh, Days: []DailyRow{},
	}
	for _, d := range order {
		sortCurrencyTotals(byDay[d].CurrencyTotals)
		out.Days = append(out.Days, *byDay[d])
	}
	return out, nil
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
		byKey := map[string]*BreakdownRow{}
		order := []string{}
		for _, r := range rows {
			key := ptrStr(r.ProductID) + "\x00" + r.SKU + "\x00" + r.ProductName
			row, ok := byKey[key]
			if !ok {
				row = &BreakdownRow{Dimension: dimension, ProductID: r.ProductID,
					SKU: strPtr(r.SKU), ProductName: strPtr(r.ProductName), LineSales: []LineSaleTotal{}}
				byKey[key] = row
				order = append(order, key)
			}
			row.Units += r.Units
			row.LineSales = append(row.LineSales, LineSaleTotal{
				Currency: r.Currency, LineSalesMinor: r.LineSales, LineCostMinor: r.LineCost})
		}
		out.Rows = sortProductRows(byKey, order)
	case DimensionRootCategory, DimensionSubcategory:
		var rows []CategoryRow
		if dimension == DimensionRootCategory {
			rows, err = s.repo.SalesByRootCategory(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
		} else {
			rows, err = s.repo.SalesBySubcategory(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
		}
		if err != nil {
			return Breakdown{}, err
		}
		byKey := map[string]*BreakdownRow{}
		order := []string{}
		for _, r := range rows {
			// Snapshot identity: kind + id + both historical names.
			// Renamed snapshots of one category stay separate rows;
			// buckets merge only within exact snapshot identity.
			key := r.Kind + "\x00" + r.ID + "\x00" + r.NameAR + "\x00" + r.NameEN
			row, ok := byKey[key]
			if !ok {
				kind, id, ar, en := r.Kind, r.ID, r.NameAR, r.NameEN
				row = &BreakdownRow{Dimension: dimension,
					ClassificationKind: &kind, ClassificationID: &id,
					NameAR: &ar, NameEN: &en, LineSales: []LineSaleTotal{}}
				byKey[key] = row
				order = append(order, key)
			}
			row.Units += r.Units
			row.LineSales = append(row.LineSales, LineSaleTotal{
				Currency: r.Currency, LineSalesMinor: r.Sales, LineCostMinor: r.Cost})
		}
		out.Rows = sortCategoryRows(byKey, order)
	case DimensionCashier:
		rows, err := s.repo.SalesByCashier(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
		if err != nil {
			return Breakdown{}, err
		}
		byKey := map[string]*BreakdownRow{}
		order := []string{}
		for _, r := range rows {
			// Explicit null bucket: unattributed sales group under "".
			key := ptrStr(r.CashierID) + "\x00" + ptrStr(r.CashierName)
			row, ok := byKey[key]
			if !ok {
				row = &BreakdownRow{Dimension: dimension,
					CashierID: r.CashierID, CashierName: r.CashierName}
				byKey[key] = row
				order = append(order, key)
			}
			row.Units += r.Units
			row.Transactions += r.Transactions
			row.CurrencyTotals = append(row.CurrencyTotals, SaleCurrencyTotal{
				Currency: r.Currency, SubtotalMinor: r.Subtotal, DiscountMinor: r.Discount,
				TaxMinor: r.Tax, SalesTotalMinor: r.SalesTotal})
		}
		for _, row := range byKey {
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
		for _, r := range rows {
			row, ok := byKey[r.Channel]
			if !ok {
				ch := r.Channel
				row = &BreakdownRow{Dimension: dimension, Channel: &ch}
				byKey[r.Channel] = row
				order = append(order, r.Channel)
			}
			row.Units += r.Units
			row.Transactions += r.Transactions
			row.CurrencyTotals = append(row.CurrencyTotals, SaleCurrencyTotal{
				Currency: r.Currency, SubtotalMinor: r.Subtotal, DiscountMinor: r.Discount,
				TaxMinor: r.Tax, SalesTotalMinor: r.SalesTotal})
		}
		for _, row := range byKey {
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
	return Freshness{
		LatestSaleEventReceivedAt:     row.LatestReceivedAt,
		LatestProjectedSaleOccurredAt: row.LatestOccurredAt,
		ProjectionBacklogCount:        row.Backlog,
		BlockedSaleEventCount:         row.Blocked,
		CloudProjectionComplete:       row.Backlog == 0,
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
func sortProductRows(byKey map[string]*BreakdownRow, order []string) []BreakdownRow {
	out := rowsInOrder(byKey, order)
	for i := range out {
		sortLineSales(out[i].LineSales)
	}
	sort.SliceStable(out, func(i, j int) bool {
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

func sortCategoryRows(byKey map[string]*BreakdownRow, order []string) []BreakdownRow {
	out := rowsInOrder(byKey, order)
	for i := range out {
		sortLineSales(out[i].LineSales)
	}
	sort.SliceStable(out, func(i, j int) bool {
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

func ptrStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func strPtr(s string) *string { v := s; return &v }
