package salesreport

import (
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// Display currencies. All keeps each currency separate; EGP and USD show
// every Sale in that currency.
const (
	DisplayAll = "all"
	DisplayEGP = "EGP"
	DisplayUSD = "USD"
)

// ErrRateRequired is returned when an EGP Sale must be shown in USD but
// its Store has never synced a USD Sale carrying a rate.
const ErrRateRequired = "EXCHANGE_RATE_REQUIRED"

const microrateScale = 1_000_000

// Money is an exact minor-unit amount, emitted as a JSON string like the
// rest of the dashboard API.
type Money int64

// MarshalJSON keeps large values exact in JavaScript.
func (m Money) MarshalJSON() ([]byte, error) {
	return []byte(`"` + strconv.FormatInt(int64(m), 10) + `"`), nil
}

// SummaryRow is the business total for one report currency. Profit is
// sales after refunds, less tax kept, less the cost of goods not restocked.
type SummaryRow struct {
	Currency         string `json:"currency"`
	SalesCount       int64  `json:"sales_count"`
	ItemsSold        int64  `json:"items_sold"`
	Gross            Money  `json:"gross_minor"`
	Discount         Money  `json:"discount_minor"`
	Tax              Money  `json:"tax_minor"`
	Sales            Money  `json:"sales_minor"`
	Cost             Money  `json:"cost_minor"`
	MissingCostItems int64  `json:"missing_cost_items"`
	ReturnCount      int64  `json:"return_count"`
	ReturnedItems    int64  `json:"returned_items"`
	Refund           Money  `json:"refund_minor"`
	RefundedTax      Money  `json:"refunded_tax_minor"`
	RestockedCost    Money  `json:"restocked_cost_minor"`
	NetSales         Money  `json:"net_sales_minor"`
	NetTax           Money  `json:"net_tax_minor"`
	NetRevenue       Money  `json:"net_revenue_minor"`
	NetCost          Money  `json:"net_cost_minor"`
	Profit           Money  `json:"profit_minor"`
}

// DailyRow is one local calendar day in one report currency.
type DailyRow struct {
	Date             string `json:"date"`
	Currency         string `json:"currency"`
	SalesCount       int64  `json:"sales_count"`
	ItemsSold        int64  `json:"items_sold"`
	Sales            Money  `json:"sales_minor"`
	Cost             Money  `json:"cost_minor"`
	MissingCostItems int64  `json:"missing_cost_items"`
	ReturnedItems    int64  `json:"returned_items"`
	Refund           Money  `json:"refund_minor"`
	NetSales         Money  `json:"net_sales_minor"`
}

// GroupRow is one category, subcategory, tag or product. Line totals are
// attributed in full to every group the line belongs to.
type GroupRow struct {
	ID               string `json:"id"`
	SKU              string `json:"sku,omitempty"`
	NameAr           string `json:"name_ar"`
	NameEn           string `json:"name_en"`
	Currency         string `json:"currency"`
	ItemsSold        int64  `json:"items_sold"`
	ReturnedItems    int64  `json:"returned_items"`
	NetItems         int64  `json:"net_items"`
	Sales            Money  `json:"sales_minor"`
	Cost             Money  `json:"cost_minor"`
	MissingCostItems int64  `json:"missing_cost_items"`
	Refund           Money  `json:"refund_minor"`
	NetSales         Money  `json:"net_sales_minor"`
}

// PaymentRow is one payment method in one report currency.
type PaymentRow struct {
	Method       string `json:"method"`
	Currency     string `json:"currency"`
	PaymentCount int64  `json:"payment_count"`
	Amount       Money  `json:"amount_minor"`
	Refund       Money  `json:"refund_minor"`
	Net          Money  `json:"net_minor"`
}

// SaleRow is one Sale on the details table.
type SaleRow struct {
	SaleID           string    `json:"sale_id"`
	SaleNumber       string    `json:"sale_number"`
	OccurredAt       time.Time `json:"occurred_at"`
	Currency         string    `json:"currency"`
	Products         string    `json:"products"`
	SKUs             string    `json:"skus"`
	RootNamesAr      string    `json:"root_names_ar"`
	RootNamesEn      string    `json:"root_names_en"`
	SubNamesAr       string    `json:"subcategory_names_ar"`
	SubNamesEn       string    `json:"subcategory_names_en"`
	ItemsSold        int64     `json:"items_sold"`
	PaymentMethods   []string  `json:"payment_methods"`
	Cost             Money     `json:"cost_minor"`
	MissingCostItems int64     `json:"missing_cost_items"`
	Total            Money     `json:"total_minor"`
	// OriginalCurrency and OriginalTotal are the Sale as recorded;
	// RateMicrorate is the USD->EGP rate that converted it into the report
	// currency, null when it is shown in its own currency.
	OriginalCurrency string `json:"original_currency"`
	OriginalTotal    Money  `json:"original_total_minor"`
	RateMicrorate    *Money `json:"rate_microrate"`
}

// CurrencyMixRow totals the Sales recorded in one currency: as recorded,
// and in the report currency.
type CurrencyMixRow struct {
	Currency       string `json:"currency"`
	SalesCount     int64  `json:"sales_count"`
	Total          Money  `json:"total_minor"`
	ReportCurrency string `json:"report_currency"`
	ReportTotal    Money  `json:"report_total_minor"`
}

// RateView is one USD->EGP rate and when it took effect. Source is
// "cloud" for a rate set in Cloud settings and "sale" for the snapshot a
// synced USD Sale carries.
type RateView struct {
	Microrate   Money     `json:"rate_microrate"`
	EffectiveAt time.Time `json:"effective_at"`
	Source      string    `json:"source"`
}

// ExchangeRate explains the USD rates behind a report: the rate in effect
// when it was made (rates change daily, so older Sales may differ), and
// the lowest and highest rate its Sales used. Each is null when unknown.
type ExchangeRate struct {
	Latest    *RateView `json:"latest"`
	PeriodMin *Money    `json:"period_min_microrate"`
	PeriodMax *Money    `json:"period_max_microrate"`
}

// BaseSummary is the whole period in EGP, whatever the display currency:
// the figure extra costs are subtracted from.
type BaseSummary struct {
	Sales            Money `json:"sales_minor"`
	NetSales         Money `json:"net_sales_minor"`
	Profit           Money `json:"profit_minor"`
	MissingCostItems int64 `json:"missing_cost_items"`
}

// Sections is the computed body of a sales report.
type Sections struct {
	Summary           []SummaryRow     `json:"summary"`
	Base              BaseSummary      `json:"base"`
	Daily             []DailyRow       `json:"daily"`
	Categories        []GroupRow       `json:"categories"`
	Subcategories     []GroupRow       `json:"subcategories"`
	Tags              []GroupRow       `json:"tags"`
	Products          []GroupRow       `json:"products"`
	Payments          []PaymentRow     `json:"payments"`
	Sales             []SaleRow        `json:"sales"`
	SalesTotalCount   int              `json:"sales_total_count"`
	UnclassifiedItems int64            `json:"unclassified_items"`
	Options           FilterOptions    `json:"filter_options"`
	CurrencyMix       []CurrencyMixRow `json:"currency_mix"`
	ExchangeRate      ExchangeRate     `json:"exchange_rate"`
}

// conversion turns one Sale's amounts into the report currency:
// amount * num / den, rounded half away from zero. rate is the USD->EGP
// microrate behind it (0 when the Sale keeps its own currency).
type conversion struct {
	num, den int64
	currency string
	rate     int64
}

func conversionFor(display string, s Sale) (conversion, error) {
	switch {
	case display == DisplayEGP && s.Currency == DisplayUSD:
		if s.FxMicrorate <= 0 {
			return conversion{}, apperr.New(apperr.Internal, "projection integrity: USD sale without FX snapshot")
		}
		return conversion{num: s.FxMicrorate, den: microrateScale, currency: DisplayEGP, rate: s.FxMicrorate}, nil
	case display == DisplayUSD && s.Currency == DisplayEGP:
		if s.USDRateMicrorate <= 0 {
			return conversion{}, apperr.New(apperr.Conflict, ErrRateRequired)
		}
		return conversion{num: microrateScale, den: s.USDRateMicrorate, currency: DisplayUSD, rate: s.USDRateMicrorate}, nil
	default:
		return conversion{num: 1, den: 1, currency: s.Currency}, nil
	}
}

// builder accumulates with checked int64 arithmetic; the first overflow
// fails the whole report instead of wrapping.
type builder struct {
	overflow bool
}

func (b *builder) add(dst *Money, v int64) {
	sum := int64(*dst) + v
	if (v > 0 && sum < int64(*dst)) || (v < 0 && sum > int64(*dst)) {
		b.overflow = true
	}
	*dst = Money(sum)
}

func (b *builder) mul(a, n int64) int64 {
	if a == 0 || n == 0 {
		return 0
	}
	if a > math.MaxInt64/n || a < math.MinInt64/n {
		b.overflow = true
		return 0
	}
	return a * n
}

func (b *builder) convert(c conversion, amount int64) int64 {
	if c.num == c.den {
		return amount
	}
	product := new(big.Int).Mul(big.NewInt(amount), big.NewInt(c.num))
	den := big.NewInt(c.den)
	quo, rem := new(big.Int).QuoRem(product, den, new(big.Int))
	// Half away from zero: |2*rem| >= den rounds the magnitude up.
	if new(big.Int).Abs(new(big.Int).Lsh(rem, 1)).Cmp(den) >= 0 {
		quo.Add(quo, big.NewInt(int64(product.Sign())))
	}
	if !quo.IsInt64() {
		b.overflow = true
		return 0
	}
	return quo.Int64()
}

type lineKey struct{ sale, item string }

type groupKey struct{ id, currency string }

type groupSet struct {
	rows  map[groupKey]*GroupRow
	order []groupKey
}

func newGroupSet() *groupSet { return &groupSet{rows: map[groupKey]*GroupRow{}} }

func (g *groupSet) get(id, currency, nameAr, nameEn, sku string) *GroupRow {
	k := groupKey{id, currency}
	if row, ok := g.rows[k]; ok {
		return row
	}
	row := &GroupRow{ID: id, Currency: currency, NameAr: nameAr, NameEn: nameEn, SKU: sku}
	g.rows[k] = row
	g.order = append(g.order, k)
	return row
}

// sorted ranks by net sales, then name, then identity, so ties are stable.
func (g *groupSet) sorted() []GroupRow {
	out := make([]GroupRow, 0, len(g.order))
	for _, k := range g.order {
		row := *g.rows[k]
		row.NetItems = row.ItemsSold - row.ReturnedItems
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].NetSales != out[j].NetSales {
			return out[i].NetSales > out[j].NetSales
		}
		if out[i].NameAr != out[j].NameAr {
			return out[i].NameAr < out[j].NameAr
		}
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Currency < out[j].Currency
	})
	return out
}

// lineFacts are a line's converted amounts in its report currency.
type lineFacts struct {
	line     Line
	currency string
	total    int64
	cost     int64
	hasCost  bool
}

// Filter keeps whole Sales, as MoonLightRetail does: a Sale stays when it
// has a payment of Payment (if set) and one of its lines matches every
// set of root categories, subcategories and tags given. Returns follow
// their Sale.
type Filter struct {
	RootIDs        []string `json:"root_category_ids"`
	SubcategoryIDs []string `json:"subcategory_ids"`
	TagIDs         []string `json:"tag_ids"`
	Payment        string   `json:"payment_method"`
}

// Options shape one report: display is all, EGP or USD; Loc decides
// calendar days; Offset/Limit page the Sales details; ExcludeTax shows
// sales and refunds without tax (the business does not use tax).
type Options struct {
	Display    string
	Loc        *time.Location
	Offset     int
	Limit      int
	Filter     Filter
	ExcludeTax bool
}

// Build computes every report section.
func Build(f Facts, o Options) (Sections, error) {
	display, loc, offset, limit := o.Display, o.Loc, o.Offset, o.Limit
	b := &builder{}
	sales := map[string]Sale{}
	conv := map[string]conversion{}
	for _, s := range f.Sales {
		c, err := conversionFor(display, s)
		if err != nil {
			return Sections{}, err
		}
		sales[s.ID] = s
		conv[s.ID] = c
	}

	roots := map[lineKey][]Classification{}
	subs := map[lineKey][]Classification{}
	for _, c := range f.Classifications {
		k := lineKey{c.SaleID, c.ItemID}
		if c.Kind == "root" {
			roots[k] = append(roots[k], c)
		} else {
			subs[k] = append(subs[k], c)
		}
	}
	tags := map[lineKey][]Tag{}
	for _, t := range f.Tags {
		k := lineKey{t.SaleID, t.ItemID}
		tags[k] = append(tags[k], t)
	}
	keep := keptSales(f, o.Filter, roots, subs, tags)
	// withoutTax drops tax from a total when the business does not use tax.
	withoutTax := func(total, tax int64) int64 {
		if !o.ExcludeTax {
			return total
		}
		return max(total-tax, 0)
	}

	summary := map[string]*SummaryRow{}
	sum := func(currency string) *SummaryRow {
		if row, ok := summary[currency]; ok {
			return row
		}
		row := &SummaryRow{Currency: currency}
		summary[currency] = row
		return row
	}
	type dayKey struct{ date, currency string }
	daily := map[dayKey]*DailyRow{}
	day := func(at time.Time, currency string) *DailyRow {
		k := dayKey{at.In(loc).Format("2006-01-02"), currency}
		if row, ok := daily[k]; ok {
			return row
		}
		row := &DailyRow{Date: k.date, Currency: currency}
		daily[k] = row
		return row
	}
	categories, subcategories, tagRows, products := newGroupSet(), newGroupSet(), newGroupSet(), newGroupSet()
	groupsOf := func(k lineKey, currency string, l Line) []*GroupRow {
		var out []*GroupRow
		for _, c := range roots[k] {
			out = append(out, categories.get(c.ID, currency, c.NameAr, c.NameEn, ""))
		}
		for _, c := range subs[k] {
			out = append(out, subcategories.get(c.ID, currency, c.NameAr, c.NameEn, ""))
		}
		for _, t := range tags[k] {
			out = append(out, tagRows.get(t.ID, currency, t.NameAr, t.NameEn, ""))
		}
		id := l.ProductID
		if id == "" {
			id = "sku:" + l.SKU
		}
		return append(out, products.get(id, currency, l.ProductName, l.ProductName, l.SKU))
	}

	// Sale headers.
	details := []SaleRow{}
	detailIndex := map[string]int{}
	mix := map[string]*CurrencyMixRow{}
	var rates ExchangeRate
	for _, s := range f.Sales {
		if !s.InPeriod || !keep[s.ID] {
			continue
		}
		c := conv[s.ID]
		native := withoutTax(s.Total, s.Tax)
		m, ok := mix[s.Currency]
		if !ok {
			m = &CurrencyMixRow{Currency: s.Currency, ReportCurrency: c.currency}
			mix[s.Currency] = m
		}
		m.SalesCount++
		b.add(&m.Total, native)
		// A USD Sale always used its own snapshot; an EGP Sale used a
		// rate only when it is shown in USD.
		used := c.rate
		if used == 0 && s.Currency == DisplayUSD {
			used = s.FxMicrorate
		}
		if used > 0 {
			rates.widen(used)
		}
		row := sum(c.currency)
		row.SalesCount++
		b.add(&row.Gross, b.convert(c, s.Subtotal))
		b.add(&row.Discount, b.convert(c, s.Discount))
		if !o.ExcludeTax {
			b.add(&row.Tax, b.convert(c, s.Tax))
		}
		total := b.convert(c, native)
		b.add(&row.Sales, total)
		b.add(&m.ReportTotal, total)
		d := day(s.OccurredAt, c.currency)
		d.SalesCount++
		b.add(&d.Sales, total)
		detailIndex[s.ID] = len(details)
		detail := SaleRow{SaleID: s.ID, SaleNumber: s.Number, OccurredAt: s.OccurredAt,
			Currency: c.currency, Total: Money(total), PaymentMethods: []string{},
			OriginalCurrency: s.Currency, OriginalTotal: Money(native)}
		if c.rate > 0 {
			rate := Money(c.rate)
			detail.RateMicrorate = &rate
		}
		details = append(details, detail)
	}

	// Sale lines: every line is kept for returns; only in-period lines count as sold.
	lines := map[lineKey]lineFacts{}
	var unclassified int64
	for _, l := range f.Lines {
		s, ok := sales[l.SaleID]
		if !ok {
			continue
		}
		c := conv[l.SaleID]
		lf := lineFacts{line: l, currency: c.currency, total: b.convert(c, l.LineTotal)}
		if l.UnitCost != nil {
			lf.cost, lf.hasCost = b.convert(c, b.mul(*l.UnitCost, l.Quantity)), true
		}
		k := lineKey{l.SaleID, l.ItemID}
		lines[k] = lf
		if !s.InPeriod || !keep[l.SaleID] {
			continue
		}
		missing := int64(0)
		if !lf.hasCost {
			missing = l.Quantity
		}
		row := sum(c.currency)
		row.ItemsSold += l.Quantity
		b.add(&row.Cost, lf.cost)
		row.MissingCostItems += missing
		d := day(s.OccurredAt, c.currency)
		d.ItemsSold += l.Quantity
		b.add(&d.Cost, lf.cost)
		d.MissingCostItems += missing
		if len(roots[k]) == 0 {
			unclassified += l.Quantity
		}
		for _, g := range groupsOf(k, c.currency, l) {
			g.ItemsSold += l.Quantity
			b.add(&g.Sales, lf.total)
			b.add(&g.NetSales, lf.total)
			b.add(&g.Cost, lf.cost)
			g.MissingCostItems += missing
		}
		detail := &details[detailIndex[l.SaleID]]
		detail.ItemsSold += l.Quantity
		b.add(&detail.Cost, lf.cost)
		detail.MissingCostItems += missing
		detail.Products = joinName(detail.Products, l.ProductName)
		detail.SKUs = joinName(detail.SKUs, l.SKU)
		for _, r := range roots[k] {
			detail.RootNamesAr = joinUnique(detail.RootNamesAr, r.NameAr)
			detail.RootNamesEn = joinUnique(detail.RootNamesEn, firstNonEmpty(r.NameEn, r.NameAr))
		}
		for _, r := range subs[k] {
			detail.SubNamesAr = joinUnique(detail.SubNamesAr, r.NameAr)
			detail.SubNamesEn = joinUnique(detail.SubNamesEn, firstNonEmpty(r.NameEn, r.NameAr))
		}
	}

	// Payments.
	payments := map[groupKey]*PaymentRow{}
	payment := func(method, currency string) *PaymentRow {
		k := groupKey{method, currency}
		if row, ok := payments[k]; ok {
			return row
		}
		row := &PaymentRow{Method: method, Currency: currency}
		payments[k] = row
		return row
	}
	for _, p := range f.Payments {
		c, ok := conv[p.SaleID]
		if !ok || !sales[p.SaleID].InPeriod || !keep[p.SaleID] {
			continue
		}
		applied := b.convert(c, p.Amount-p.Change)
		row := payment(p.Method, c.currency)
		row.PaymentCount++
		b.add(&row.Amount, applied)
		b.add(&row.Net, applied)
		if i, ok := detailIndex[p.SaleID]; ok && !contains(details[i].PaymentMethods, p.Method) {
			details[i].PaymentMethods = append(details[i].PaymentMethods, p.Method)
		}
	}

	// Returns use the original Sale's conversion, so a refund offsets
	// exactly what the Sale contributed.
	returnConv := map[string]conversion{}
	for _, r := range f.Returns {
		c, ok := conv[r.SaleID]
		if !ok {
			return Sections{}, apperr.New(apperr.Internal, "projection integrity: return without its sale")
		}
		if !keep[r.SaleID] {
			continue
		}
		returnConv[r.ID] = c
		refund := b.convert(c, withoutTax(r.Total, r.Tax))
		row := sum(c.currency)
		row.ReturnCount++
		b.add(&row.Refund, refund)
		if !o.ExcludeTax {
			b.add(&row.RefundedTax, b.convert(c, r.Tax))
		}
		d := day(r.OccurredAt, c.currency)
		b.add(&d.Refund, refund)
	}
	returnDay := map[string]time.Time{}
	for _, r := range f.Returns {
		returnDay[r.ID] = r.OccurredAt
	}
	for _, rl := range f.ReturnLines {
		c, ok := returnConv[rl.ReturnID]
		if !ok {
			continue
		}
		row := sum(c.currency)
		row.ReturnedItems += rl.Quantity
		if rl.Restocked && rl.Cost != nil {
			b.add(&row.RestockedCost, b.convert(c, *rl.Cost))
		}
		day(returnDay[rl.ReturnID], c.currency).ReturnedItems += rl.Quantity
		k := lineKey{rl.SaleID, rl.SaleLineID}
		lf, ok := lines[k]
		if !ok {
			continue
		}
		refund := b.convert(c, rl.Refund)
		for _, g := range groupsOf(k, c.currency, lf.line) {
			g.ReturnedItems += rl.Quantity
			b.add(&g.Refund, refund)
			b.add(&g.NetSales, -refund)
		}
	}
	for _, rp := range f.ReturnPayments {
		c, ok := returnConv[rp.ReturnID]
		if !ok {
			continue
		}
		refund := b.convert(c, rp.Amount)
		row := payment(rp.Method, c.currency)
		b.add(&row.Refund, refund)
		b.add(&row.Net, -refund)
	}

	out := Sections{
		Categories: categories.sorted(), Subcategories: subcategories.sorted(),
		Tags: tagRows.sorted(), Products: products.sorted(),
		UnclassifiedItems: unclassified,
		Options:           filterOptions(f),
		CurrencyMix:       []CurrencyMixRow{},
		ExchangeRate:      rates,
	}
	for _, currency := range []string{DisplayEGP, DisplayUSD} {
		if row, ok := mix[currency]; ok {
			out.CurrencyMix = append(out.CurrencyMix, *row)
		}
	}
	for _, currency := range []string{DisplayEGP, DisplayUSD} {
		row, ok := summary[currency]
		if !ok {
			continue
		}
		b.add(&row.NetSales, int64(row.Sales))
		b.add(&row.NetSales, -int64(row.Refund))
		b.add(&row.NetTax, int64(row.Tax))
		b.add(&row.NetTax, -int64(row.RefundedTax))
		b.add(&row.NetRevenue, int64(row.NetSales))
		b.add(&row.NetRevenue, -int64(row.NetTax))
		b.add(&row.NetCost, int64(row.Cost))
		b.add(&row.NetCost, -int64(row.RestockedCost))
		b.add(&row.Profit, int64(row.NetRevenue))
		b.add(&row.Profit, -int64(row.NetCost))
		out.Summary = append(out.Summary, *row)
	}
	out.Daily = make([]DailyRow, 0, len(daily))
	for _, row := range daily {
		b.add(&row.NetSales, int64(row.Sales))
		b.add(&row.NetSales, -int64(row.Refund))
		out.Daily = append(out.Daily, *row)
	}
	sort.Slice(out.Daily, func(i, j int) bool {
		if out.Daily[i].Date != out.Daily[j].Date {
			return out.Daily[i].Date < out.Daily[j].Date
		}
		return out.Daily[i].Currency < out.Daily[j].Currency
	})
	out.Payments = make([]PaymentRow, 0, len(payments))
	for _, row := range payments {
		out.Payments = append(out.Payments, *row)
	}
	methodRank := map[string]int{"cash": 0, "card": 1, "other": 2}
	sort.Slice(out.Payments, func(i, j int) bool {
		if out.Payments[i].Method != out.Payments[j].Method {
			return methodRank[out.Payments[i].Method] < methodRank[out.Payments[j].Method]
		}
		return out.Payments[i].Currency < out.Payments[j].Currency
	})
	// Sales arrive newest first; the details table pages through them.
	out.SalesTotalCount = len(details)
	if offset > len(details) {
		offset = len(details)
	}
	end := len(details)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}
	out.Sales = details[offset:end]
	if b.overflow {
		return Sections{}, errOverflow()
	}
	return out, nil
}

// widen extends the period's rate range to include microrate.
func (e *ExchangeRate) widen(microrate int64) {
	if e.PeriodMin == nil || Money(microrate) < *e.PeriodMin {
		v := Money(microrate)
		e.PeriodMin = &v
	}
	if e.PeriodMax == nil || Money(microrate) > *e.PeriodMax {
		v := Money(microrate)
		e.PeriodMax = &v
	}
}

// baseOf summarizes the period in EGP from the EGP-display sections.
func baseOf(egp Sections) BaseSummary {
	var base BaseSummary
	for _, row := range egp.Summary {
		base.Sales += row.Sales
		base.NetSales += row.NetSales
		base.Profit += row.Profit
		base.MissingCostItems += row.MissingCostItems
	}
	return base
}

func joinName(list, name string) string {
	if name == "" {
		return list
	}
	if list == "" {
		return name
	}
	return list + ", " + name
}

func joinUnique(list, name string) string {
	if name == "" {
		return list
	}
	for _, existing := range strings.Split(list, ", ") {
		if existing == name {
			return list
		}
	}
	return joinName(list, name)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// FilterOption is one category, subcategory or tag the period's Sales
// carry, offered as a report filter.
type FilterOption struct {
	ID     string `json:"id"`
	NameAr string `json:"name_ar"`
	NameEn string `json:"name_en"`
}

// FilterOptions lists the filter choices for the period, unfiltered.
type FilterOptions struct {
	Categories    []FilterOption `json:"categories"`
	Subcategories []FilterOption `json:"subcategories"`
	Tags          []FilterOption `json:"tags"`
}

func filterOptions(f Facts) FilterOptions {
	out := FilterOptions{Categories: []FilterOption{}, Subcategories: []FilterOption{}, Tags: []FilterOption{}}
	seen := map[string]bool{}
	add := func(list *[]FilterOption, kind, id, ar, en string) {
		if seen[kind+id] {
			return
		}
		seen[kind+id] = true
		*list = append(*list, FilterOption{ID: id, NameAr: ar, NameEn: firstNonEmpty(en, ar)})
	}
	for _, c := range f.Classifications {
		if c.Kind == "root" {
			add(&out.Categories, "r", c.ID, c.NameAr, c.NameEn)
		} else {
			add(&out.Subcategories, "s", c.ID, c.NameAr, c.NameEn)
		}
	}
	for _, t := range f.Tags {
		add(&out.Tags, "t", t.ID, t.NameAr, t.NameEn)
	}
	for _, list := range []*[]FilterOption{&out.Categories, &out.Subcategories, &out.Tags} {
		sort.Slice(*list, func(i, j int) bool {
			if (*list)[i].NameAr != (*list)[j].NameAr {
				return (*list)[i].NameAr < (*list)[j].NameAr
			}
			return (*list)[i].ID < (*list)[j].ID
		})
	}
	return out
}

// keptSales applies the Filter to every Sale in the sale set.
func keptSales(f Facts, filter Filter, roots, subs map[lineKey][]Classification, tags map[lineKey][]Tag) map[string]bool {
	keep := make(map[string]bool, len(f.Sales))
	for _, s := range f.Sales {
		keep[s.ID] = true
	}
	if filter.Payment != "" {
		paid := map[string]bool{}
		for _, p := range f.Payments {
			if p.Method == filter.Payment {
				paid[p.SaleID] = true
			}
		}
		for id := range keep {
			keep[id] = paid[id]
		}
	}
	if len(filter.RootIDs)+len(filter.SubcategoryIDs)+len(filter.TagIDs) == 0 {
		return keep
	}
	wanted := func(ids []string) map[string]bool {
		set := make(map[string]bool, len(ids))
		for _, id := range ids {
			set[id] = true
		}
		return set
	}
	rootSet, subSet, tagSet := wanted(filter.RootIDs), wanted(filter.SubcategoryIDs), wanted(filter.TagIDs)
	matches := func(set map[string]bool, ids []string) bool {
		if len(set) == 0 {
			return true
		}
		for _, id := range ids {
			if set[id] {
				return true
			}
		}
		return false
	}
	matched := map[string]bool{}
	for _, l := range f.Lines {
		k := lineKey{l.SaleID, l.ItemID}
		var r, s, t []string
		for _, c := range roots[k] {
			r = append(r, c.ID)
		}
		for _, c := range subs[k] {
			s = append(s, c.ID)
		}
		for _, tg := range tags[k] {
			t = append(t, tg.ID)
		}
		if matches(rootSet, r) && matches(subSet, s) && matches(tagSet, t) {
			matched[l.SaleID] = true
		}
	}
	for id := range keep {
		keep[id] = keep[id] && matched[id]
	}
	return keep
}
