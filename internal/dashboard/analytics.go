package dashboard

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

// OrderAnalyticsRowRaw is one durable online-order aggregate row.
type OrderAnalyticsRowRaw struct {
	ProviderKey     string
	CanonicalStatus string
	Currency        string
	Orders          int64
	ValueMinor      int64
}

// OrderCurrencyTotal is one currency bucket of operational order value.
// Money is exact minor units serialized as a string (the established
// exact-money representation); currencies are never summed together.
type OrderCurrencyTotal struct {
	Currency         string `json:"currency"`
	Orders           int64  `json:"orders"`
	ValueMinor       string `json:"value_minor"`
	ActiveOrders     int64  `json:"active_orders"`
	ActiveValueMinor string `json:"active_value_minor"`
}

// OrderStatusCount is one canonical-status bucket of operational orders.
type OrderStatusCount struct {
	CanonicalStatus string `json:"canonical_status"`
	Orders          int64  `json:"orders"`
}

// OrderProviderCount is one provider's operational order total (one row
// per provider+currency; providers never merge into one flag).
type OrderProviderCount struct {
	ProviderKey string `json:"provider_key"`
	Currency    string `json:"currency"`
	Orders      int64  `json:"orders"`
	ValueMinor  string `json:"value_minor"`
}

// OrderAnalytics is the operational provider-order aggregate. It is
// NEVER combined with canonical finalized Retail Sales: provider orders
// are operational commerce truth and may duplicate business activity
// already represented by a finalized Sale.
type OrderAnalytics struct {
	GeneratedAt    time.Time            `json:"generated_at"`
	StoreID        *string              `json:"store_id"`
	ProviderKey    string               `json:"provider_key"`
	ActiveStatuses []string             `json:"active_statuses"`
	CurrencyTotals []OrderCurrencyTotal `json:"currency_totals"`
	StatusCounts   []OrderStatusCount   `json:"status_counts"`
	ProviderTotals []OrderProviderCount `json:"provider_totals"`
}

// ActiveOrderStatuses is the exact canonical-status set counted in the
// "active operational value" metrics. Cancelled/deleted/refunded/failed/
// unknown orders are excluded from active value but always visible in the
// status breakdown — no single invented "sales" number.
var ActiveOrderStatuses = []string{"pending", "processing", "on_hold", "completed"}

func activeOrderStatus(status string) bool {
	for _, s := range ActiveOrderStatuses {
		if s == status {
			return true
		}
	}
	return false
}

// CatalogHealthCountRaw is one summary count row.
type CatalogHealthCountRaw struct {
	ReasonCode string
	Products   int64
}

// CatalogHealthRowRaw is one bounded diagnostic detail row.
type CatalogHealthRowRaw struct {
	ReasonCode  string
	ProviderKey string
	ProductID   *string
	SKU         string
	Name        string
	StoreID     *string
}

// CatalogHealthCount is one stable health reason code with its count.
type CatalogHealthCount struct {
	ReasonCode string `json:"reason_code"`
	Products   int64  `json:"products"`
}

// CatalogHealthItem is one bounded health detail row (IDs and stable
// codes only — never raw provider errors, credentials, PII or SQL).
type CatalogHealthItem struct {
	ReasonCode  string  `json:"reason_code"`
	ProviderKey string  `json:"provider_key"`
	ProductID   *string `json:"product_id,omitempty"`
	SKU         string  `json:"sku,omitempty"`
	Name        string  `json:"name,omitempty"`
	StoreID     *string `json:"store_id,omitempty"`
}

// CatalogHealthReasonCodes is the stable health vocabulary (labels may
// evolve; codes are the API contract). Phase 17-R0 retired the
// product-level CATALOG_MISSING_SKU: Product carries no SKU authority
// anywhere (ADR-0049) and catalog_products.sku is gone (00035) —
// VARIANT_MISSING_SKU is the single SKU health code.
var CatalogHealthReasonCodes = []string{
	"CATALOG_MISSING_CATEGORY",
	"AVAILABILITY_NOT_READY",
	"COMMERCE_MAPPING_MISSING",
	"COMMERCE_SYNC_AMBIGUOUS",
	"COMMERCE_STORE_CONFLICT",
	// Informational (Phase 13 §116): intentional Category suppression —
	// never a catalog-health failure.
	"CATEGORY_ONLINE_DISABLED",
	// Phase 17 variant health (SKU/inventory ownership on ProductVariant).
	"PRODUCT_NO_ACTIVE_VARIANTS",
	"PRODUCT_NO_SELLABLE_VARIANT",
	"VARIANT_DUPLICATE_COMBINATION",
	"VARIANT_MISSING_SKU",
	"VARIANT_MAPPING_MISSING",
	// Informational (Phase 17): deliberate variant inactivity/tombstone
	// on an online-eligible product — never a catalog-health failure.
	"VARIANT_INTENTIONALLY_OFFLINE",
}

// CatalogHealth is the diagnostic catalog/integration surface. It reports
// durable MoonLight evidence only: no live provider reads, no provider
// writes, no Store adoption, no barrier settlement, no auto-fix.
type CatalogHealth struct {
	GeneratedAt     time.Time            `json:"generated_at"`
	StoreID         *string              `json:"store_id"`
	ProviderKey     string               `json:"provider_key"`
	ReasonCodes     []string             `json:"reason_codes"`
	Counts          []CatalogHealthCount `json:"counts"`
	Detail          []CatalogHealthItem  `json:"detail"`
	Providers       []string             `json:"providers"`
	DetailLimit     int                  `json:"detail_limit"`
	DetailTruncated bool                 `json:"detail_truncated"`
}

// OrderAnalytics aggregates durable online-order state over the report
// window. Provider filter narrows (never falls back to all); Store scope
// follows the Phase 9 model (specific Store excludes other Stores and
// legacy NULL; global keeps ALL+legacy order semantics).
func (s Service) OrderAnalytics(ctx context.Context, req report.Request, providerKey string) (OrderAnalytics, error) {
	providerKey = strings.TrimSpace(providerKey)
	if providerKey != "" {
		if !validProviderKey(providerKey) {
			return OrderAnalytics{}, apperr.New(apperr.InvalidInput, "provider_key must match ^[a-z0-9][a-z0-9_-]{0,63}$")
		}
	}
	rows, err := s.repo.DashboardOrderAnalytics(ctx, providerKey, req.ScopeStoreID(), req.Currency, req.Period.StartUTC, req.Period.EndUTC)
	if err != nil {
		return OrderAnalytics{}, err
	}
	out := OrderAnalytics{
		GeneratedAt: req.GeneratedAt(), StoreID: req.StoreIDOrNil(), ProviderKey: providerKey,
		ActiveStatuses: ActiveOrderStatuses,
		CurrencyTotals: []OrderCurrencyTotal{}, StatusCounts: []OrderStatusCount{}, ProviderTotals: []OrderProviderCount{},
	}
	type bucket struct {
		orders, value, activeOrders, activeValue int64
	}
	currencies := map[string]*bucket{}
	sizes := map[string]int64{}
	providers := map[string]map[string]*bucket{}
	for _, r := range rows {
		if r.Currency == "" {
			continue
		}
		b := currencies[r.Currency]
		if b == nil {
			b = &bucket{}
			currencies[r.Currency] = b
		}
		if err := addAnalytics(&b.orders, r.Orders); err != nil {
			return OrderAnalytics{}, err
		}
		if err := addAnalytics(&b.value, r.ValueMinor); err != nil {
			return OrderAnalytics{}, err
		}
		if activeOrderStatus(r.CanonicalStatus) {
			if err := addAnalytics(&b.activeOrders, r.Orders); err != nil {
				return OrderAnalytics{}, err
			}
			if err := addAnalytics(&b.activeValue, r.ValueMinor); err != nil {
				return OrderAnalytics{}, err
			}
		}
		count := sizes[r.CanonicalStatus]
		if err := addAnalytics(&count, r.Orders); err != nil {
			return OrderAnalytics{}, err
		}
		sizes[r.CanonicalStatus] = count
		pb := providers[r.ProviderKey]
		if pb == nil {
			pb = map[string]*bucket{}
			providers[r.ProviderKey] = pb
		}
		p := pb[r.Currency]
		if p == nil {
			p = &bucket{}
			pb[r.Currency] = p
		}
		if err := addAnalytics(&p.orders, r.Orders); err != nil {
			return OrderAnalytics{}, err
		}
		if err := addAnalytics(&p.value, r.ValueMinor); err != nil {
			return OrderAnalytics{}, err
		}
	}
	for _, currency := range sortedKeys(currencies) {
		b := currencies[currency]
		out.CurrencyTotals = append(out.CurrencyTotals, OrderCurrencyTotal{
			Currency: currency, Orders: b.orders, ValueMinor: strconv.FormatInt(b.value, 10),
			ActiveOrders: b.activeOrders, ActiveValueMinor: strconv.FormatInt(b.activeValue, 10),
		})
	}
	for _, status := range sortedKeys(sizes) {
		out.StatusCounts = append(out.StatusCounts, OrderStatusCount{CanonicalStatus: status, Orders: sizes[status]})
	}
	for _, provider := range sortedKeys(providers) {
		for _, currency := range sortedKeys(providers[provider]) {
			b := providers[provider][currency]
			out.ProviderTotals = append(out.ProviderTotals, OrderProviderCount{
				ProviderKey: provider, Currency: currency,
				Orders: b.orders, ValueMinor: strconv.FormatInt(b.value, 10),
			})
		}
	}
	return out, nil
}

// CatalogHealth reads bounded, truthful catalog/integration health from
// durable state only.
func (s Service) CatalogHealth(ctx context.Context, req report.Request, providerKey, reason string, limit int) (CatalogHealth, error) {
	providerKey = strings.TrimSpace(providerKey)
	if providerKey != "" && !validProviderKey(providerKey) {
		return CatalogHealth{}, apperr.New(apperr.InvalidInput, "provider_key must match ^[a-z0-9][a-z0-9_-]{0,63}$")
	}
	reason = strings.TrimSpace(reason)
	if reason != "" {
		valid := false
		for _, code := range CatalogHealthReasonCodes {
			if code == reason {
				valid = true
				break
			}
		}
		if !valid {
			return CatalogHealth{}, apperr.New(apperr.InvalidInput, "unsupported health reason code")
		}
	}
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > report.MaxTopNLimit {
		return CatalogHealth{}, apperr.New(apperr.InvalidInput, "limit must be within [1, 100]")
	}
	counts, err := s.repo.CatalogHealthSummaryRows(ctx, req.ScopeStoreID(), providerKey)
	if err != nil {
		return CatalogHealth{}, err
	}
	detail, err := s.repo.CatalogHealthDetailRows(ctx, req.ScopeStoreID(), providerKey, reason, limit+1)
	if err != nil {
		return CatalogHealth{}, err
	}
	providers, err := s.repo.CatalogHealthProviders(ctx, req.ScopeStoreID())
	if err != nil {
		return CatalogHealth{}, err
	}
	out := CatalogHealth{
		GeneratedAt: req.GeneratedAt(), StoreID: req.StoreIDOrNil(), ProviderKey: providerKey,
		ReasonCodes: CatalogHealthReasonCodes,
		Counts:      []CatalogHealthCount{}, Detail: []CatalogHealthItem{}, Providers: append([]string{}, providers...),
		DetailLimit: limit,
	}
	byCode := map[string]int64{}
	for _, c := range counts {
		byCode[c.ReasonCode] = c.Products
	}
	for _, code := range CatalogHealthReasonCodes {
		out.Counts = append(out.Counts, CatalogHealthCount{ReasonCode: code, Products: byCode[code]})
	}
	if len(detail) > limit {
		out.DetailTruncated = true
		detail = detail[:limit]
	}
	for _, d := range detail {
		out.Detail = append(out.Detail, CatalogHealthItem{
			ReasonCode: d.ReasonCode, ProviderKey: d.ProviderKey,
			ProductID: d.ProductID, SKU: d.SKU, Name: d.Name, StoreID: d.StoreID,
		})
	}
	return out, nil
}

// validProviderKey mirrors the frozen provider-key vocabulary
// (commerce.ValidateProviderKey) without importing the commerce domain.
var providerKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func validProviderKey(key string) bool { return providerKeyPattern.MatchString(key) }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TagCurrencyBucket is one currency bucket of one Tag row's line
// snapshot money. Exact minor units as strings (the established
// exact-money representation); currencies are never summed.
type TagCurrencyBucket struct {
	Currency        string `json:"currency"`
	LineSalesMinor  string `json:"line_sales_minor"`
	LineRefundMinor string `json:"line_refund_minor"`
	NetMinor        string `json:"net_minor"`
}

// TagRow is one ranked historical Tag row. Identity is the canonical
// historical Tag snapshot identity (never merged by slug across Stores).
// Totals OVERLAP across rows (non-additive by design).
type TagRow struct {
	TagID         *string             `json:"tag_id,omitempty"`
	TagSlug       *string             `json:"tag_slug,omitempty"`
	NameAR        *string             `json:"name_ar,omitempty"`
	NameEN        *string             `json:"name_en,omitempty"`
	Units         int64               `json:"units"`
	UnitsReturned int64               `json:"units_returned"`
	Currencies    []TagCurrencyBucket `json:"currencies"`
}

// TagList is the bounded Top Tags response.
type TagList struct {
	GeneratedAt time.Time `json:"generated_at"`
	Timezone    string    `json:"timezone"`
	StoreID     *string   `json:"store_id"`
	Rows        []TagRow  `json:"rows"`
	// OverlapNote is the mandatory non-additivity disclosure.
	OverlapNote string `json:"overlap_note"`
}

// TagOverlapNote is the exact disclosure the UI must present: Tag totals
// overlap and must never be summed to derive total business revenue.
const TagOverlapNote = "Tag totals overlap and must not be summed to derive total business revenue."

// TagList converts one canonical Tag breakdown into the exact-money
// dashboard shape. Ranking order is preserved byte-for-byte from the
// report service (deterministic; complete identity tie-break).
func TagListFrom(res report.Breakdown) TagList {
	out := TagList{
		GeneratedAt: res.GeneratedAt, Timezone: res.Timezone, StoreID: res.StoreID,
		Rows: []TagRow{}, OverlapNote: TagOverlapNote,
	}
	for _, row := range res.Rows {
		tr := TagRow{TagID: row.TagID, TagSlug: row.TagSlug, NameAR: row.NameAR, NameEN: row.NameEN,
			Units: row.Units, UnitsReturned: row.UnitsReturned, Currencies: []TagCurrencyBucket{}}
		for _, bucket := range row.LineSales {
			net := bucket.LineSalesMinor - bucket.LineRefundMinor
			tr.Currencies = append(tr.Currencies, TagCurrencyBucket{
				Currency:        bucket.Currency,
				LineSalesMinor:  strconv.FormatInt(bucket.LineSalesMinor, 10),
				LineRefundMinor: strconv.FormatInt(bucket.LineRefundMinor, 10),
				NetMinor:        strconv.FormatInt(net, 10),
			})
		}
		out.Rows = append(out.Rows, tr)
	}
	return out
}

// addAnalytics fails before publishing any aggregate that cannot fit int64.
func addAnalytics(total *int64, value int64) error {
	const max = int64(^uint64(0) >> 1)
	const min = -max - 1
	if value > 0 && *total > max-value || value < 0 && *total < min-value {
		return apperr.New(apperr.Internal, "online analytics arithmetic overflow")
	}
	*total += value
	return nil
}
