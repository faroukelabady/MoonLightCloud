package report

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/google/uuid"
)

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
	// Store scopes ownership: nil = all Stores + legacy (global, backward
	// compatible). Non-nil selects exactly one proven Store; legacy NULL
	// rows never match a specific scope.
	Store *StoreScope
	// Limit bounds Top-N breakdown responses after the deterministic
	// ranking (0 = unbounded, the backward-compatible default). It never
	// changes ranking semantics: rows are sorted exactly as before and
	// then truncated, so equal rows keep their stable order.
	Limit int
	now   time.Time
}

// MaxTopNLimit is the largest accepted breakdown limit. Top-N surfaces
// are bounded (Phase 12); the documented default is 10.
const MaxTopNLimit = 100

// ParseLimit validates the optional Top-N limit query value. Empty
// selects the unbounded backward-compatible behavior (0). Anything else
// must be an explicit integer within 1..100: malformed, zero, negative
// and over-maximum values are explicit 400s (never silently clamped).
func ParseLimit(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		// Whitespace-only junk is not "absent": limits are explicit.
		return 0, apperr.New(apperr.InvalidInput, "limit must be an integer within [1, 100]")
	}
	parsed, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0, apperr.New(apperr.InvalidInput, "limit must be an integer within [1, 100]")
	}
	if parsed < 1 || parsed > MaxTopNLimit {
		return 0, apperr.New(apperr.InvalidInput, "limit must be within [1, 100]")
	}
	return parsed, nil
}

// applyLimit truncates deterministically ranked rows to the requested
// bound. Ranking order is already final at this point (complete identity
// tie-breakers mean two rows never compare equal), so truncation is
// stable across reruns and pagination.
func applyLimit(rows []BreakdownRow, limit int) []BreakdownRow {
	if limit <= 0 || len(rows) <= limit {
		return rows
	}
	return rows[:limit]
}

// StoreScope is the canonical report ownership selector shared by HTTP
// handlers (dashboard session + token APIs): one parsing model, no
// per-handler rule drift.
type StoreScope struct {
	// StoreID is a validated Store UUID string. Empty means no selection
	// (global); StoreID selection never includes legacy NULL rows.
	StoreID string
}

// ParseStoreScope validates the optional store_id query value. Empty
// selects global behavior (backward compatible). A malformed UUID is a
// 400; an unknown but well-formed UUID passes through and yields empty
// results (never a silent fallback to global).
func ParseStoreScope(storeID string) (*StoreScope, error) {
	trimmed := strings.TrimSpace(storeID)
	if trimmed == "" {
		return nil, nil
	}
	if _, err := uuid.Parse(trimmed); err != nil {
		return nil, apperr.New(apperr.InvalidInput, "store_id must be a UUID")
	}
	return &StoreScope{StoreID: trimmed}, nil
}

// storeIDOrNil renders the applied scope for responses: the Store UUID
// pointer, or nil (JSON null) for the global scope.
func (r Request) StoreIDOrNil() *string {
	if !r.Scoped() {
		return nil
	}
	id := r.Store.StoreID
	return &id
}

// Scoped reports whether a specific Store is selected.
func (r Request) Scoped() bool { return r.Store != nil && r.Store.StoreID != "" }

// ScopeStoreID returns the selected Store UUID or "" when global.
func (r Request) ScopeStoreID() string {
	if !r.Scoped() {
		return ""
	}
	return r.Store.StoreID
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
	case DimensionProduct, DimensionProductType, DimensionRootCategory, DimensionSubcategory, DimensionTag, DimensionCashier, DimensionChannel:
		return d, nil
	default:
		return "", apperr.New(apperr.InvalidInput,
			fmt.Sprintf("unsupported dimension %q: want product|product_type|root_category|subcategory|cashier|channel", d))
	}
}

// scopedFetch routes one repository read through the selected Store
// scope. Specific scopes call the Store-scoped repository surface with
// the validated Store UUID; global requests keep the frozen methods.
func (s Service) scopedSalesSummary(ctx context.Context, req Request) ([]SummaryRow, error) {
	if req.Scoped() {
		return s.repo.SalesSummaryForStore(ctx, req.ScopeStoreID(), req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	}
	return s.repo.SalesSummary(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
}

func (s Service) scopedSalesPayments(ctx context.Context, req Request) ([]PaymentRow, error) {
	if req.Scoped() {
		return s.repo.SalesPaymentsForStore(ctx, req.ScopeStoreID(), req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	}
	return s.repo.SalesPayments(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
}

func (s Service) scopedRefundsSummary(ctx context.Context, req Request) ([]RefundSummaryRow, error) {
	if req.Scoped() {
		return s.repo.RefundsSummaryForStore(ctx, req.ScopeStoreID(), req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	}
	return s.repo.RefundsSummary(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
}

func (s Service) scopedSalesDaily(ctx context.Context, req Request, mode, timezone string) ([]DailyRowRaw, error) {
	if req.Scoped() {
		return s.repo.SalesDailyForStore(ctx, req.ScopeStoreID(), req.Period.StartUTC, req.Period.EndUTC, mode, timezone)
	}
	return s.repo.SalesDaily(ctx, req.Period.StartUTC, req.Period.EndUTC, mode, timezone)
}

func (s Service) scopedRefundsDaily(ctx context.Context, req Request, mode, timezone string) ([]RefundDailyRow, error) {
	if req.Scoped() {
		return s.repo.RefundsDailyForStore(ctx, req.ScopeStoreID(), req.Period.StartUTC, req.Period.EndUTC, mode, timezone)
	}
	return s.repo.RefundsDaily(ctx, req.Period.StartUTC, req.Period.EndUTC, mode, timezone)
}

func (s Service) scopedSalesByProduct(ctx context.Context, req Request) ([]ProductRow, error) {
	if req.Scoped() {
		return s.repo.SalesByProductForStore(ctx, req.ScopeStoreID(), req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	}
	return s.repo.SalesByProduct(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
}

func (s Service) scopedSalesByVariant(ctx context.Context, req Request) ([]VariantRow, error) {
	if req.Scoped() {
		return s.repo.SalesByVariantForStore(ctx, req.ScopeStoreID(), req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	}
	return s.repo.SalesByVariant(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
}

func (s Service) scopedSalesByProductType(ctx context.Context, req Request) ([]ProductTypeRow, error) {
	if req.Scoped() {
		return s.repo.SalesByProductTypeForStore(ctx, req.ScopeStoreID(), req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	}
	return s.repo.SalesByProductType(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
}

func (s Service) scopedRefundsByProductType(ctx context.Context, req Request) ([]RefundProductTypeRow, error) {
	if req.Scoped() {
		return s.repo.RefundsByProductTypeForStore(ctx, req.ScopeStoreID(), req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	}
	return s.repo.RefundsByProductType(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
}

// SalesByProductType returns the Phase 17-R2 type breakdown: sale lines
// grouped by their frozen per-line type snapshot (never current catalog).
func (s Service) SalesByProductType(ctx context.Context, req Request) ([]ProductTypeRow, error) {
	rows, err := s.scopedSalesByProductType(ctx, req)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// SalesByVariant returns the optional Phase 17 variant breakdown:
// sale lines grouped by their frozen per-line variant identity columns
// (append-only history — never joined to the current-state variant
// projection). Lines without a variant identity group under a NULL
// variant ID. Existing product/category/tag breakdowns are unchanged.
func (s Service) SalesByVariant(ctx context.Context, req Request) ([]VariantRow, error) {
	rows, err := s.scopedSalesByVariant(ctx, req)
	if err != nil {
		return nil, err
	}
	return applyVariantLimit(rows, req.Limit), nil
}

// applyVariantLimit keeps the largest rows by line sales when a limit is
// set (0 = unbounded), mirroring applyLimit.
func applyVariantLimit(rows []VariantRow, limit int) []VariantRow {
	if limit <= 0 || len(rows) <= limit {
		return rows
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].LineSales > rows[j].LineSales })
	return rows[:limit]
}

func (s Service) scopedRefundsByProduct(ctx context.Context, req Request) ([]RefundProductRow, error) {
	if req.Scoped() {
		return s.repo.RefundsByProductForStore(ctx, req.ScopeStoreID(), req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	}
	return s.repo.RefundsByProduct(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
}

func (s Service) scopedSalesByCategory(ctx context.Context, req Request, root bool) ([]CategoryRow, error) {
	if req.Scoped() {
		if root {
			return s.repo.SalesByRootCategoryForStore(ctx, req.ScopeStoreID(), req.Period.StartUTC, req.Period.EndUTC, req.Currency)
		}
		return s.repo.SalesBySubcategoryForStore(ctx, req.ScopeStoreID(), req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	}
	if root {
		return s.repo.SalesByRootCategory(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	}
	return s.repo.SalesBySubcategory(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
}

func (s Service) scopedRefundsByCategory(ctx context.Context, req Request, root bool) ([]RefundCategoryRow, error) {
	if req.Scoped() {
		if root {
			return s.repo.RefundsByRootCategoryForStore(ctx, req.ScopeStoreID(), req.Period.StartUTC, req.Period.EndUTC, req.Currency)
		}
		return s.repo.RefundsBySubcategoryForStore(ctx, req.ScopeStoreID(), req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	}
	if root {
		return s.repo.RefundsByRootCategory(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	}
	return s.repo.RefundsBySubcategory(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
}

func (s Service) scopedSalesByTag(ctx context.Context, req Request) ([]TagRow, error) {
	if req.Scoped() {
		return s.repo.SalesByTagForStore(ctx, req.ScopeStoreID(), req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	}
	return s.repo.SalesByTag(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
}

func (s Service) scopedRefundsByTag(ctx context.Context, req Request) ([]RefundTagRow, error) {
	if req.Scoped() {
		return s.repo.RefundsByTagForStore(ctx, req.ScopeStoreID(), req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	}
	return s.repo.RefundsByTag(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
}

func (s Service) scopedSalesByCashier(ctx context.Context, req Request) ([]CashierRow, error) {
	if req.Scoped() {
		return s.repo.SalesByCashierForStore(ctx, req.ScopeStoreID(), req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	}
	return s.repo.SalesByCashier(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
}

func (s Service) scopedRefundsByCashier(ctx context.Context, req Request) ([]RefundCashierRow, error) {
	if req.Scoped() {
		return s.repo.RefundsByCashierForStore(ctx, req.ScopeStoreID(), req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	}
	return s.repo.RefundsByCashier(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
}

func (s Service) scopedSalesByChannel(ctx context.Context, req Request) ([]ChannelRow, error) {
	if req.Scoped() {
		return s.repo.SalesByChannelForStore(ctx, req.ScopeStoreID(), req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	}
	return s.repo.SalesByChannel(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
}

func (s Service) scopedRefundsByChannel(ctx context.Context, req Request) ([]RefundChannelRow, error) {
	if req.Scoped() {
		return s.repo.RefundsByChannelForStore(ctx, req.ScopeStoreID(), req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	}
	return s.repo.RefundsByChannel(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
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
	rows, err := s.scopedSalesSummary(ctx, req)
	if err != nil {
		return Summary{}, err
	}
	pays, err := s.scopedSalesPayments(ctx, req)
	if err != nil {
		return Summary{}, err
	}
	refunds, err := s.scopedRefundsSummary(ctx, req)
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
		StoreID:        req.StoreIDOrNil(),
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
	rows, err := s.scopedSalesDaily(ctx, req, req.Currency, req.Period.Timezone)
	if err != nil {
		return Daily{}, err
	}
	refundRows, err := s.scopedRefundsDaily(ctx, req, req.Currency, req.Period.Timezone)
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
		StoreID: req.StoreIDOrNil(),
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

func ptrStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func strPtr(s string) *string { v := s; return &v }
