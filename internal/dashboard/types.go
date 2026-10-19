package dashboard

import (
	"context"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

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
	// StoreID identifies the owning Store so same-SKU rows from
	// different Stores stay visually distinct. Null only when the
	// underlying row predates Store attribution (should not happen for
	// projected products; treated as unscoped).
	StoreID *string `json:"store_id"`
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
	Timezone string            `json:"timezone"`
	Period   report.PeriodMeta `json:"period"`
	// StoreID echoes the applied ownership scope (UUID or null global).
	StoreID         *string    `json:"store_id"`
	Mode            string     `json:"mode"`
	DisplayCurrency string     `json:"display_currency"`
	Normalized      bool       `json:"normalized"`
	Days            []DailyDay `json:"days"`
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
	// StoreID echoes the applied ownership scope (UUID or null global).
	StoreID    *string         `json:"store_id"`
	Summary    SummaryDTO      `json:"summary"`
	Normalized NormalizedTotal `json:"normalized"`
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
	Reader
	report.Repository
	// Static Store-specific adapter/tooling compatibility reads.
	// Services select Reader once; global reads keep ALL+legacy behavior.
	DashboardNormalizedSummaryForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time) (NormalizedSummaryRow, error)
	DashboardNormalizedDailyForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, timezone string) ([]NormalizedDailyRow, error)
	DashboardLatestFxForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time) (LatestFxRow, error)
	DashboardBranchesForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]BranchRowRaw, error)
	DashboardProductsNormalizedForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time) ([]NormalizedProductRowRaw, error)
	DashboardCategoriesNormalizedForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, kind string) ([]NormalizedCategoryRowRaw, error)
	DashboardRecentActivityForStore(ctx context.Context, storeID string, limit int) ([]ActivityItem, error)
	DashboardLatestSalesForStore(ctx context.Context, storeID string, limit int) ([]LatestSale, error)
	DashboardNormalizedRefundsSummaryForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time) (NormalizedRefundSummaryRow, error)
	DashboardNormalizedRefundsDailyForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, timezone string) ([]NormalizedRefundDailyRow, error)
	DashboardProductsNormalizedRefundsForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time) ([]NormalizedRefundProductRowRaw, error)
	DashboardCategoriesNormalizedRefundsForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, kind string) ([]NormalizedRefundCategoryRowRaw, error)
	DashboardReturnBranchesForStore(ctx context.Context, storeID string, startUTC, endUTC time.Time, currency string) ([]ReturnBranchRowRaw, error)
	// Phase 12 operational analytics + catalog health (read-only, durable
	// state only). storeID "" = global (ALL + legacy); providerKey "" =
	// all durable providers. Never contacts a provider, never mutates.
	DashboardOrderAnalytics(ctx context.Context, providerKey, storeID, currency string, startUTC, endUTC time.Time) ([]OrderAnalyticsRowRaw, error)
	CatalogHealthProviders(ctx context.Context, storeID string) ([]string, error)
	CatalogHealthSummaryRows(ctx context.Context, storeID, providerKey string) ([]CatalogHealthCountRaw, error)
	CatalogHealthDetailRows(ctx context.Context, storeID, providerKey, reason string, limit int) ([]CatalogHealthRowRaw, error)
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
		StoreID     *string
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
		StoreID     *string
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
