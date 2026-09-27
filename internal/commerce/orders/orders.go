// Package orders owns the provider-neutral Cloud online-order domain:
// normalized order snapshots, canonical statuses, exact money parsing,
// semantic fingerprints, webhook delivery validation, durable
// reconciliation orchestration, and read models. WooCommerce specifics
// live in internal/commerce/woocommerce; MoonLight stays authoritative
// for catalog, inventory, and sales. See ADR-0033.
package orders

import (
	"fmt"
	"strings"
	"time"
)

// CanonicalStatus is the provider-neutral order lifecycle vocabulary.
// Unknown provider statuses map to StatusUnknown with the raw string
// preserved; MoonLight never guesses plugin semantics.
type CanonicalStatus string

const (
	StatusPending    CanonicalStatus = "PENDING"
	StatusProcessing CanonicalStatus = "PROCESSING"
	StatusOnHold     CanonicalStatus = "ON_HOLD"
	StatusCompleted  CanonicalStatus = "COMPLETED"
	StatusCancelled  CanonicalStatus = "CANCELLED"
	StatusRefunded   CanonicalStatus = "REFUNDED"
	StatusFailed     CanonicalStatus = "FAILED"
	StatusUnknown    CanonicalStatus = "UNKNOWN"
	StatusDeleted    CanonicalStatus = "DELETED"
)

// WebhookInsertOutcome makes delivery dedupe explicit: inserted or
// identical-duplicate. Contradictions surface as apperr Conflict, never
// a boolean.
type WebhookInsertOutcome int

const (
	// WebhookInserted means the delivery row was created.
	WebhookInserted WebhookInsertOutcome = iota + 1
	// WebhookDuplicateIdentical means the same delivery identity,
	// payload hash, topic, and order ID already exist: no new work.
	WebhookDuplicateIdentical
)

// WebhookTopic is the supported Woo order lifecycle trigger set.
type WebhookTopic string

const (
	TopicOrderCreated WebhookTopic = "order.created"
	TopicOrderUpdated WebhookTopic = "order.updated"
	TopicOrderDeleted WebhookTopic = "order.deleted"
)

// ParseWebhookTopic validates the Woo topic header value.
func ParseWebhookTopic(value string) (WebhookTopic, error) {
	switch WebhookTopic(strings.TrimSpace(value)) {
	case TopicOrderCreated, TopicOrderUpdated, TopicOrderDeleted:
		return WebhookTopic(strings.TrimSpace(value)), nil
	}
	return "", fmt.Errorf("unsupported webhook topic %q", value)
}

// IsDeletion reports whether the topic is a provider lifecycle delete.
func (t WebhookTopic) IsDeletion() bool { return t == TopicOrderDeleted }

// MapWooStatus maps core Woo order statuses deterministically. Unknown
// or custom statuses (including core "trash", which is not a lifecycle
// state MoonLight models) map to StatusUnknown with the raw preserved.
func MapWooStatus(status string) CanonicalStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "pending":
		return StatusPending
	case "processing":
		return StatusProcessing
	case "on-hold", "on_hold":
		return StatusOnHold
	case "completed":
		return StatusCompleted
	case "cancelled", "canceled":
		return StatusCancelled
	case "refunded":
		return StatusRefunded
	case "failed":
		return StatusFailed
	default:
		return StatusUnknown
	}
}

// Money is an exact minor-unit amount. No floats.
type Money struct {
	Currency    string
	AmountMinor int64
}

// ParseMinorUnits parses Woo decimal money strings ("13.00", "650.25",
// "0") into exact int64 minor units for two-decimal currencies. No
// floating point; overflow and excess precision are typed failures.
func ParseMinorUnits(value string, currency string) (int64, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, fmt.Errorf("empty money value")
	}
	negative := false
	if strings.HasPrefix(trimmed, "-") {
		negative = true
		trimmed = trimmed[1:]
	} else if strings.HasPrefix(trimmed, "+") {
		trimmed = trimmed[1:]
	}
	parts := strings.SplitN(trimmed, ".", 3)
	if len(parts) > 2 {
		return 0, fmt.Errorf("malformed money %q", value)
	}
	intPart, fracPart := parts[0], ""
	if len(parts) == 2 {
		fracPart = parts[1]
	}
	if intPart == "" || fracPart == "" && len(parts) == 2 {
		return 0, fmt.Errorf("malformed money %q", value)
	}
	for _, digit := range intPart + fracPart {
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("malformed money %q", value)
		}
	}
	if len(fracPart) > 2 {
		return 0, fmt.Errorf("money %q exceeds minor-unit precision", value)
	}
	for len(fracPart) < 2 {
		fracPart += "0"
	}
	combined := strings.TrimLeft(intPart+fracPart, "0")
	if combined == "" {
		return 0, nil
	}
	// int64 caps near 9.2e18 (19 digits); reject longer outright.
	if len(combined) > 19 {
		return 0, fmt.Errorf("money %q overflows int64", value)
	}
	var amount int64
	for _, digit := range combined {
		amount = amount*10 + int64(digit-'0')
		if amount < 0 {
			return 0, fmt.Errorf("money %q overflows int64", value)
		}
	}
	if negative {
		amount = -amount
	}
	_ = currency
	return amount, nil
}

// Address is a normalized billing/shipping address. Only fulfillment
// fields are retained; nothing provider-opaque.
type Address struct {
	Kind      string
	FirstName string
	LastName  string
	Company   string
	Address1  string
	Address2  string
	City      string
	State     string
	Postcode  string
	Country   string
	Email     string
	Phone     string
}

// Customer holds normalized fulfillment contact fields.
type Customer struct {
	FirstName string
	LastName  string
	Email     string
	Phone     string
}

// OrderLine is one normalized provider-neutral order line with the
// resolved MoonLight product identity when a generic mapping proves it.
type OrderLine struct {
	ExternalLineID    int64
	ExternalProductID string
	VariationID       int64
	SKU               string
	Name              string
	Quantity          int64
	SubtotalMinor     int64
	SubtotalTaxMinor  int64
	TotalMinor        int64
	TotalTaxMinor     int64
	MoonlightProduct  *string
	Mapped            bool
	UnsupportedReason string
}

// OrderSnapshot is the normalized immutable provider order state used
// for projection. It carries no webhook delivery identity, no retry
// metadata, and no raw provider JSON.
type OrderSnapshot struct {
	ProviderKey        string
	ExternalOrderID    string
	OrderNumber        string
	ProviderStatus     string
	Canonical          CanonicalStatus
	ProviderDeleted    bool
	Currency           string
	DiscountMinor      int64
	ShippingMinor      int64
	CartTaxMinor       int64
	TotalTaxMinor      int64
	TotalMinor         int64
	PricesIncludeTax   bool
	CreatedAt          time.Time
	ModifiedAt         time.Time
	PaidAt             *time.Time
	CompletedAt        *time.Time
	PaymentMethod      string
	PaymentMethodTitle string
	Customer           Customer
	Billing            Address
	Shipping           Address
	Lines              []OrderLine
	MappingComplete    bool
	UnmappedLines      int
}
