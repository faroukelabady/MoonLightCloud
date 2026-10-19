package dashboard

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/projection"
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

// ProcessingReader exposes diagnostics without granting projection mutation.
type ProcessingReader interface {
	ProcessingStats(context.Context, string) (projection.Stats, error)
}

// Service composes frozen ReportingService data with dashboard-only
// presentation values (FX normalization). No business rule is
// reimplemented: native totals flow through untouched.
type Service struct {
	reports    report.Service
	repo       Repository
	processing ProcessingReader
	clock      clock.Clock
}

// NewService wires the dashboard service.
func NewService(reports report.Service, repo Repository, processing ProcessingReader, c clock.Clock) Service {
	return Service{reports: reports, repo: repo, processing: processing, clock: c}
}

// Overview assembles summary + normalized total + FX info. A USD sale
// without an FX snapshot is a projection-integrity error, not silently
// droppable revenue.

func (s Service) Overview(ctx context.Context, req report.Request) (Overview, error) {
	reader := ReaderFor(s.repo, req.Store)
	sum, err := s.reports.Summary(ctx, req)
	if err != nil {
		return Overview{}, err
	}
	norm, err := reader.DashboardNormalizedSummary(ctx, req.Period.StartUTC, req.Period.EndUTC)
	if err != nil {
		return Overview{}, err
	}
	if norm.USDMissingFx > 0 {
		return Overview{}, apperr.New(apperr.Internal, "projection integrity: USD sale without FX snapshot")
	}
	refundNorm, err := reader.DashboardNormalizedRefundsSummary(ctx, req.Period.StartUTC, req.Period.EndUTC)
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
	fx, err := s.fxInfo(ctx, req, reader)
	if err != nil {
		return Overview{}, err
	}
	nativeRows, err := reader.SalesSummary(ctx, req.Period.StartUTC, req.Period.EndUTC, "")
	if err != nil {
		return Overview{}, err
	}
	return Overview{
		GeneratedAt: req.GeneratedAt(), Timezone: req.Period.Timezone,
		Period:  sum.Period,
		StoreID: req.StoreIDOrNil(),
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
	reader := ReaderFor(s.repo, req.Store)
	switch mode {
	case "all", "EGP", "USD":
	default:
		return DailyResponse{}, apperr.New(apperr.InvalidInput, "unsupported daily mode: want all|EGP|USD")
	}
	if mode == "all" {
		rows, err := reader.DashboardNormalizedDaily(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Period.Timezone)
		if err != nil {
			return DailyResponse{}, err
		}
		refundRows, err := reader.DashboardNormalizedRefundsDaily(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Period.Timezone)
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
			Timezone: req.Period.Timezone, Period: periodMeta(req), StoreID: req.StoreIDOrNil()}
		for _, d := range order {
			out.Days = append(out.Days, *byDay[d])
		}
		return out, nil
	}
	native, err := reader.SalesDaily(ctx, req.Period.StartUTC, req.Period.EndUTC, mode, req.Period.Timezone)
	if err != nil {
		return DailyResponse{}, err
	}
	refundNative, err := reader.RefundsDaily(ctx, req.Period.StartUTC, req.Period.EndUTC, mode, req.Period.Timezone)
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
		Timezone: req.Period.Timezone, Period: periodMeta(req), StoreID: req.StoreIDOrNil()}
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
	reader := ReaderFor(s.repo, req.Store)
	rows, err := reader.DashboardProductsNormalized(ctx, req.Period.StartUTC, req.Period.EndUTC)
	if err != nil {
		return nil, err
	}
	refundRows, err := reader.DashboardProductsNormalizedRefunds(ctx, req.Period.StartUTC, req.Period.EndUTC)
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
		if row.StoreID == nil && r.StoreID != nil {
			store := *r.StoreID
			row.StoreID = &store
		}
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
		if row.StoreID == nil && r.StoreID != nil {
			store := *r.StoreID
			row.StoreID = &store
		}
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
	reader := ReaderFor(s.repo, req.Store)
	if kind != report.DimensionRootCategory && kind != report.DimensionSubcategory {
		return nil, apperr.New(apperr.InvalidInput, "unsupported normalized category dimension")
	}
	rows, err := reader.DashboardCategoriesNormalized(ctx, req.Period.StartUTC, req.Period.EndUTC, kindName(kind))
	if err != nil {
		return nil, err
	}
	refundRows, err := reader.DashboardCategoriesNormalizedRefunds(ctx, req.Period.StartUTC, req.Period.EndUTC, kindName(kind))
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
	reader := ReaderFor(s.repo, req.Store)
	rows, err := reader.DashboardBranches(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
	if err != nil {
		return nil, err
	}
	refundRows, err := reader.DashboardReturnBranches(ctx, req.Period.StartUTC, req.Period.EndUTC, req.Currency)
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
	stats, err := s.processing.ProcessingStats(ctx, sale.ProcessorSaleProjectionV3)
	if err != nil {
		return SyncHealthItem{}, err
	}
	retStats, err := s.processing.ProcessingStats(ctx, returnrefund.ProcessorReturnProjectionV1)
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
func (s Service) RecentActivity(ctx context.Context, req report.Request, limit int) ([]ActivityItem, error) {
	reader := ReaderFor(s.repo, req.Store)
	if limit < 1 || limit > 100 {
		return nil, apperr.New(apperr.InvalidInput, "limit must be within [1, 100]")
	}
	return reader.DashboardRecentActivity(ctx, limit)
}

// LatestSales returns the newest finalized sales (orders fallback).
func (s Service) LatestSales(ctx context.Context, req report.Request, limit int) ([]LatestSale, error) {
	reader := ReaderFor(s.repo, req.Store)
	if limit < 1 || limit > 100 {
		return nil, apperr.New(apperr.InvalidInput, "limit must be within [1, 100]")
	}
	return reader.DashboardLatestSales(ctx, limit)
}

func (s Service) fxInfo(ctx context.Context, req report.Request, reader Reader) (FxInfo, error) {
	row, err := reader.DashboardLatestFx(ctx, req.Period.StartUTC, req.Period.EndUTC)
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

// ---- Phase 12: operational online-order analytics (NOT financial) ----

// ---- Phase 12: catalog health (durable, read-only) ----

// ---- Phase 12: Top Tags (canonical breakdown, exact string money) ----
