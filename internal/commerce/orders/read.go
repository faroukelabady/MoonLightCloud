package orders

import (
	"context"
	"strconv"
	"time"
)

// OrderSummary is one dashboard list row with exact string money.
type OrderSummary struct {
	ProviderKey       string `json:"provider_key"`
	ExternalOrderID   string `json:"external_order_id"`
	OrderNumber       string `json:"order_number"`
	ProviderStatus    string `json:"provider_status"`
	CanonicalStatus   string `json:"canonical_status"`
	Currency          string `json:"currency"`
	TotalMinor        string `json:"total_minor"`
	CreatedAt         string `json:"created_at"`
	ModifiedAt        string `json:"modified_at"`
	CustomerName      string `json:"customer_name"`
	MappingComplete   bool   `json:"mapping_complete"`
	UnmappedLineCount int    `json:"unmapped_line_count"`
	ProviderDeleted   bool   `json:"provider_deleted"`
	Revision          int64  `json:"revision"`
}

// OrderLineView is one dashboard line row.
type OrderLineView struct {
	ExternalLineID    int64   `json:"external_line_id"`
	ExternalProductID string  `json:"external_product_id"`
	VariationID       int64   `json:"variation_id"`
	SKU               string  `json:"sku"`
	Name              string  `json:"name"`
	Quantity          int64   `json:"quantity"`
	TotalMinor        string  `json:"total_minor"`
	MoonlightProduct  *string `json:"moonlight_product_id"`
	Mapped            bool    `json:"mapped"`
	// Phase 15 §127/§130: optional configuration selection snapshot —
	// null/absent for no-frame and legacy orders (backward compatible).
	ConfigurationID              *string `json:"configuration_id,omitempty"`
	FrameStyleCode               *string `json:"frame_style_code,omitempty"`
	FrameStyleNameAR             *string `json:"frame_style_name_ar,omitempty"`
	FrameStyleNameEN             *string `json:"frame_style_name_en,omitempty"`
	FrameColorCode               *string `json:"frame_color_code,omitempty"`
	FrameColorNameAR             *string `json:"frame_color_name_ar,omitempty"`
	FrameColorNameEN             *string `json:"frame_color_name_en,omitempty"`
	ConfigurationPriceDeltaMinor *string `json:"configuration_price_delta_minor,omitempty"`
	ProviderConfigurationID      string  `json:"provider_configuration_id,omitempty"`
	ConfigurationUnresolved      bool    `json:"configuration_unresolved,omitempty"`
}

// OrderAddressView is one dashboard address row.
type OrderAddressView struct {
	Kind      string `json:"kind"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Company   string `json:"company"`
	Address1  string `json:"address_1"`
	Address2  string `json:"address_2"`
	City      string `json:"city"`
	State     string `json:"state"`
	Postcode  string `json:"postcode"`
	Country   string `json:"country"`
	Email     string `json:"email"`
	Phone     string `json:"phone"`
}

// OrderStatusEvent is one dashboard status-history row.
type OrderStatusEvent struct {
	OrderRevision   int64  `json:"order_revision"`
	ProviderStatus  string `json:"provider_status"`
	CanonicalStatus string `json:"canonical_status"`
	ObservedAt      string `json:"observed_at"`
}

// OrderDetail is the full operator-visible order.
type OrderDetail struct {
	Summary OrderSummary `json:"summary"`
	// SelectionUnresolvedLines/SelectionComplete (Phase 15-R1 F13) are
	// the explicit selection-truthfulness companion to the frozen base
	// mapping_complete semantics: an unknown provider selection is never
	// presented as fully resolved. Backward compatible additive fields.
	SelectionUnresolvedLines int  `json:"selection_unresolved_lines"`
	SelectionComplete        bool `json:"selection_complete"`
	// StoreID echoes the applied ownership scope (UUID or null global).
	StoreID            *string            `json:"store_id"`
	DiscountMinor      string             `json:"discount_minor"`
	ShippingMinor      string             `json:"shipping_minor"`
	CartTaxMinor       string             `json:"cart_tax_minor"`
	TotalTaxMinor      string             `json:"total_tax_minor"`
	PricesIncludeTax   bool               `json:"prices_include_tax"`
	PaidAt             *string            `json:"paid_at"`
	CompletedAt        *string            `json:"completed_at"`
	PaymentMethod      string             `json:"payment_method"`
	PaymentMethodTitle string             `json:"payment_method_title"`
	CustomerFirstName  string             `json:"customer_first_name"`
	CustomerLastName   string             `json:"customer_last_name"`
	CustomerEmail      string             `json:"customer_email"`
	CustomerPhone      string             `json:"customer_phone"`
	Lines              []OrderLineView    `json:"lines"`
	Addresses          []OrderAddressView `json:"addresses"`
	StatusHistory      []OrderStatusEvent `json:"status_history"`
}

// OrderStatusCount is one status-bucket row.
type OrderStatusCount struct {
	CanonicalStatus string `json:"canonical_status"`
	Total           int64  `json:"total"`
}

// WebhookQueueStats is the operational webhook inbox summary.
type WebhookQueueStats struct {
	Pending       int64   `json:"pending"`
	Retry         int64   `json:"retry"`
	Blocked       int64   `json:"blocked"`
	OldestPending *string `json:"oldest_pending_at"`
}

// OrderReader is the dashboard read boundary over projected orders.
type OrderReader interface {
	ListOrderSummaries(ctx context.Context, provider, status string, limit int, cursor *OrderCursor) ([]OrderSummary, error)
	ListOrderPage(ctx context.Context, provider, status string, limit int, cursor *OrderCursor) (OrderPage, error)
	GetOrderDetail(ctx context.Context, providerKey, externalOrderID string) (OrderDetail, error)
	CountOrdersByStatus(ctx context.Context, provider string) ([]OrderStatusCount, error)
	OrderInboxStats(ctx context.Context) (WebhookQueueStats, error)
	// Phase 9C Store-scoped reads. Root ownership controls the whole
	// graph; legacy NULL rows never match. Internal until 9D: no HTTP
	// change; global reads keep ALL+legacy behavior.
	ListOrderSummariesForStore(ctx context.Context, storeID, provider, status string, limit int, cursor *OrderCursor) ([]OrderSummary, error)
	ListOrderPageForStore(ctx context.Context, storeID, provider, status string, limit int, cursor *OrderCursor) (OrderPage, error)
	GetOrderDetailForStore(ctx context.Context, storeID, providerKey, externalOrderID string) (OrderDetail, error)
	CountOrdersByStatusForStore(ctx context.Context, storeID, provider string) ([]OrderStatusCount, error)
}

// OrderCursor is a deterministic list cursor: created_at DESC,
// provider_key, external_order_id.
type OrderCursor struct {
	CreatedAt       time.Time
	ProviderKey     string
	ExternalOrderID string
}

// OrderPage is one dashboard list page with its continuation cursor.
// Next is nil on the final page.
type OrderPage struct {
	Items []OrderSummary
	Next  *OrderCursor
}

// MinorString renders exact minor units for JSON string-money fields.
func MinorString(v int64) string {
	return strconv.FormatInt(v, 10)
}
