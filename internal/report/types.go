package report

import (
	"context"
	"time"
)

// Breakdown dimensions (fixed enum; handlers switch, queries stay static).
const (
	DimensionProduct      = "product"
	DimensionProductType  = "product_type"
	DimensionRootCategory = "root_category"
	DimensionSubcategory  = "subcategory"
	DimensionTag          = "tag"
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
	GeneratedAt time.Time  `json:"generated_at"`
	Timezone    string     `json:"timezone"`
	Period      PeriodMeta `json:"period"`
	// StoreID echoes the applied ownership scope: the Store UUID, or
	// null for the global scope. Consumers must never mistake scoped
	// data for global data.
	StoreID                *string         `json:"store_id"`
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
	// StoreID echoes the applied ownership scope (UUID or null global).
	StoreID   *string    `json:"store_id"`
	Days      []DailyRow `json:"days"`
	Freshness Freshness  `json:"freshness"`
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
	// ProductType fields (dimension=product_type only; frozen sale-time
	// snapshot labels, never current catalog joins).
	ProductTypeID     *string `json:"product_type_id,omitempty"`
	ProductTypeCode   *string `json:"product_type_code,omitempty"`
	ProductTypeNameAR *string `json:"product_type_name_ar,omitempty"`
	ProductTypeNameEN *string `json:"product_type_name_en,omitempty"`
	// Category fields.
	ClassificationKind *string `json:"classification_kind,omitempty"`
	ClassificationID   *string `json:"classification_id,omitempty"`
	NameAR             *string `json:"name_ar,omitempty"`
	NameEN             *string `json:"name_en,omitempty"`
	// Tag fields (dimension=tag only).
	TagID   *string `json:"tag_id,omitempty"`
	TagSlug *string `json:"tag_slug,omitempty"`
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
	GeneratedAt time.Time  `json:"generated_at"`
	Timezone    string     `json:"timezone"`
	Period      PeriodMeta `json:"period"`
	Dimension   string     `json:"dimension"`
	// StoreID echoes the applied ownership scope (UUID or null global).
	StoreID   *string        `json:"store_id"`
	Rows      []BreakdownRow `json:"rows"`
	Freshness Freshness      `json:"freshness"`
}

// VariantAttributeSnapshot is one frozen sale-time option attribute of a
// purchased variant (00036): identity codes plus the bilingual labels
// EXACTLY as they read at sale time. Display history only; identity is
// (DefinitionCode, ValueCode).
type VariantAttributeSnapshot struct {
	DefinitionCode   string  `json:"definition_code"`
	ValueCode        string  `json:"value_code"`
	NameAR           string  `json:"name_ar"`
	NameEN           *string `json:"name_en,omitempty"`
	DefinitionNameAR string  `json:"definition_name_ar"`
	DefinitionNameEN *string `json:"definition_name_en,omitempty"`
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
	VariantRow struct {
		VariantID *string
		// VariantSKU and VariantAttributes are the FROZEN sale-time
		// variant snapshot (00036 / sale.finalized.v3), served exactly as
		// sold: later catalog label renames can never move these rows.
		// NULL/absent for lines without variant data (v1/v2 coexistence).
		VariantSKU        *string
		VariantAttributes []VariantAttributeSnapshot
		ProductID         *string
		SKU               string
		ProductName       string
		Units             int64
		Currency          string
		LineSales         int64
		LineCost          int64
	}
	// ProductTypeRow is one frozen sale-time type bucket (00038): the
	// type id/code/labels exactly as sold. NULL type groups legacy
	// pre-R2 lines.
	ProductTypeRow struct {
		ProductTypeID     *string
		ProductTypeCode   *string
		ProductTypeNameAR *string
		ProductTypeNameEN *string
		Units             int64
		Currency          string
		LineSales         int64
		LineCost          int64
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
	TagRow struct {
		ID       string
		Slug     string
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
	RefundProductTypeRow struct {
		ProductTypeID     *string
		ProductTypeCode   *string
		ProductTypeNameAR *string
		ProductTypeNameEN *string
		Units             int64
		Currency          string
		Refund            int64
		ReturnedCost      int64
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
	RefundTagRow struct {
		ID           string
		Slug         string
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
	SalesByVariant(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]VariantRow, error)
	SalesByProductType(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]ProductTypeRow, error)
	SalesByRootCategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]CategoryRow, error)
	SalesBySubcategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]CategoryRow, error)
	SalesByTag(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]TagRow, error)
	SalesByCashier(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]CashierRow, error)
	SalesByChannel(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]ChannelRow, error)
	SalesProjectionFreshness(ctx context.Context) (FreshnessRow, error)
	RefundsSummary(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundSummaryRow, error)
	RefundsDaily(ctx context.Context, startUTC, endUTC time.Time, currency, timezone string) ([]RefundDailyRow, error)
	RefundsByProduct(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundProductRow, error)
	RefundsByProductType(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundProductTypeRow, error)
	RefundsByRootCategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundCategoryRow, error)
	RefundsBySubcategory(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundCategoryRow, error)
	RefundsByTag(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundTagRow, error)
	RefundsByCashier(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundCashierRow, error)
	RefundsByChannel(ctx context.Context, startUTC, endUTC time.Time, currency string) ([]RefundChannelRow, error)
	ReturnProjectionFreshness(ctx context.Context) (ReturnFreshnessRow, error)
	// Phase 9B store-scoped read isolation. Same frozen row shapes
	// restricted to one proven Store. Internal until 9D: no service,
	// HTTP, or dashboard change; unfiltered methods keep global behavior.
	SalesSummaryForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]SummaryRow, error)
	SalesDailyForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency, timezone string) ([]DailyRowRaw, error)
	SalesByProductForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]ProductRow, error)
	SalesByVariantForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]VariantRow, error)
	SalesByProductTypeForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]ProductTypeRow, error)
	SalesByRootCategoryForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]CategoryRow, error)
	SalesBySubcategoryForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]CategoryRow, error)
	SalesByTagForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]TagRow, error)
	RefundsSummaryForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]RefundSummaryRow, error)
	RefundsDailyForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency, timezone string) ([]RefundDailyRow, error)
	RefundsByProductForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]RefundProductRow, error)
	RefundsByProductTypeForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]RefundProductTypeRow, error)
	RefundsByRootCategoryForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]RefundCategoryRow, error)
	RefundsBySubcategoryForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]RefundCategoryRow, error)
	RefundsByTagForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]RefundTagRow, error)
	SalesPaymentsForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]PaymentRow, error)
	SalesByCashierForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]CashierRow, error)
	SalesByChannelForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]ChannelRow, error)
	RefundsByCashierForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]RefundCashierRow, error)
	RefundsByChannelForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]RefundChannelRow, error)
}
