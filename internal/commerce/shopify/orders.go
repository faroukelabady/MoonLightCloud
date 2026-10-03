package shopify

import (
	"context"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
)

// GetOrder reads one Shopify order and normalizes it into the frozen
// provider-neutral OrderSnapshot (Phase 11 order direction: Shopify →
// MoonLightCloud read/projection only). The order API is read-only for
// MoonLight: no orderCreate/orderUpdate/fulfillment/refund writes ever
// run here. Fetches are bounded (single order, fixed line-item page).
//
// Money uses the shop-money side of every MoneyBag exclusively
// (never mixed with presentmentMoney), parsed to exact int64 minor units
// with the frozen exact parser (no floats, >2^53 exact).
func (p *ShopifyProvider) GetOrder(ctx context.Context, externalOrderID string) (orders.OrderSnapshot, error) {
	canonical, err := orders.CanonicalExternalOrderID(externalOrderID)
	if err != nil {
		return orders.OrderSnapshot{}, &orders.BlockedError{
			Code: orders.CodeOrderInvalid, Message: "invalid external order id"}
	}
	orderGID := FormatGID(ResourceOrder, canonical)
	var out orderQueryResponse
	if err := p.client.do(ctx, docOrder, map[string]any{"id": orderGID}, &out); err != nil {
		// Transport/status failures surface classified and retryable;
		// only a null order projection proves absence. A transient
		// failure must never read as deletion.
		return orders.OrderSnapshot{}, err
	}
	if out.Order == nil {
		return orders.OrderSnapshot{}, &orders.BlockedError{
			Code: orders.CodeOrderNotFound, Message: "shopify order not found"}
	}
	// Identity: the response must name the requested order. A different
	// identity is a conflict, never a silent projection. The legacy
	// resource id (when present) must agree with the GID suffix — the two
	// are documented to name the same number, so disagreement is a
	// contradiction and fails closed.
	responseID, err := CanonicalExternalID(out.Order.ID, ResourceOrder)
	if err != nil || responseID != canonical {
		return orders.OrderSnapshot{}, &orders.BlockedError{
			Code: orders.CodeOrderConflict, Message: "order identity mismatch"}
	}
	if legacy := strings.TrimSpace(out.Order.LegacyResourceID); legacy != "" {
		if canonicalLegacy, err := CanonicalDecimalID(legacy); err != nil || canonicalLegacy != responseID {
			return orders.OrderSnapshot{}, &orders.BlockedError{
				Code: orders.CodeOrderConflict, Message: "order identity mismatch"}
		}
	}
	return p.normalizeOrder(canonical, out.Order)
}

// parseShopMoney converts one shop-money bag to exact minor units,
// refusing bags that name a currency other than the order's single
// currency (shop-money side only; presentment money is never read).
func parseShopMoney(bag gqlMoneyBag, currency string) (int64, error) {
	if currencyCode := strings.ToUpper(strings.TrimSpace(bag.ShopMoney.CurrencyCode)); currencyCode != "" && currencyCode != currency {
		return 0, errMixedCurrency
	}
	return orders.ParseMinorUnits(bag.ShopMoney.Amount, currency)
}

// normalizeOrder maps the trusted Shopify order projection into the
// frozen OrderSnapshot. Provider status truth is preserved raw in
// ProviderStatus; canonical mapping is deterministic (MapShopifyStatus).
func (p *ShopifyProvider) normalizeOrder(canonicalID string, remote *gqlOrder) (orders.OrderSnapshot, error) {
	currency := strings.ToUpper(strings.TrimSpace(remote.CurrencyCode))
	if currency == "" {
		return orders.OrderSnapshot{}, &orders.BlockedError{
			Code: orders.CodeOrderInvalid, Message: "order currency missing"}
	}
	money := func(bag gqlMoneyBag) (int64, error) {
		return parseShopMoney(bag, currency)
	}
	total, err := money(remote.TotalPriceSet)
	if err != nil {
		return orders.OrderSnapshot{}, &orders.BlockedError{
			Code: orders.CodeOrderInvalid, Message: "invalid order money"}
	}
	shipping, err := money(remote.TotalShippingPriceSet)
	if err != nil {
		return orders.OrderSnapshot{}, &orders.BlockedError{
			Code: orders.CodeOrderInvalid, Message: "invalid order money"}
	}
	tax, err := money(remote.TotalTaxSet)
	if err != nil {
		return orders.OrderSnapshot{}, &orders.BlockedError{
			Code: orders.CodeOrderInvalid, Message: "invalid order money"}
	}
	discount, err := money(remote.TotalDiscountsSet)
	if err != nil {
		return orders.OrderSnapshot{}, &orders.BlockedError{
			Code: orders.CodeOrderInvalid, Message: "invalid order money"}
	}

	createdAt, err := parseShopifyTime(remote.CreatedAt)
	if err != nil {
		return orders.OrderSnapshot{}, &orders.BlockedError{
			Code: orders.CodeOrderInvalid, Message: "invalid order timestamp"}
	}
	modifiedAt, err := parseShopifyTime(remote.UpdatedAt)
	if err != nil {
		return orders.OrderSnapshot{}, &orders.BlockedError{
			Code: orders.CodeOrderInvalid, Message: "invalid order timestamp"}
	}
	var paidAt *time.Time
	if remote.FullyPaid || strings.EqualFold(remote.DisplayFinancialStatus, "PAID") {
		if remote.ProcessedAt != "" {
			if processed, err := parseShopifyTime(remote.ProcessedAt); err == nil {
				paidAt = &processed
			}
		}
	}
	var completedAt *time.Time
	if remote.ClosedAt != "" {
		if closed, err := parseShopifyTime(remote.ClosedAt); err == nil {
			completedAt = &closed
		}
	}
	cancelled := remote.CancelledAt != ""
	canonical := MapShopifyStatus(remote.DisplayFinancialStatus, remote.DisplayFulfillmentStatus, cancelled)
	providerStatus := remote.DisplayFinancialStatus + "/" + remote.DisplayFulfillmentStatus

	snapshot := orders.OrderSnapshot{
		ProviderKey:        string(p.key),
		ExternalOrderID:    canonicalID,
		OrderNumber:        bounded(remote.Name, 64),
		ProviderStatus:     bounded(providerStatus, 64),
		Canonical:          canonical,
		ProviderDeleted:    false,
		Currency:           currency,
		DiscountMinor:      discount,
		ShippingMinor:      shipping,
		CartTaxMinor:       tax,
		TotalTaxMinor:      tax,
		TotalMinor:         total,
		PricesIncludeTax:   remote.TaxesIncluded,
		CreatedAt:          createdAt,
		ModifiedAt:         modifiedAt,
		PaidAt:             paidAt,
		CompletedAt:        completedAt,
		PaymentMethod:      bounded(strings.Join(remote.PaymentGatewayNames, ","), 100),
		PaymentMethodTitle: bounded(strings.Join(remote.PaymentGatewayNames, ", "), 100),
	}

	if remote.Customer != nil {
		snapshot.Customer = orders.Customer{
			FirstName: bounded(remote.Customer.FirstName, 100),
			LastName:  bounded(remote.Customer.LastName, 100),
			Email:     bounded(remote.Customer.Email, 200),
			Phone:     bounded(remote.Customer.Phone, 50),
		}
	}
	snapshot.Billing = normalizeAddress("billing", remote.BillingAddress, remote)
	snapshot.Shipping = normalizeAddress("shipping", remote.ShippingAddress, remote)

	for _, line := range remote.LineItems.Nodes {
		normalized, err := normalizeLine(line, currency)
		if err != nil {
			return orders.OrderSnapshot{}, err
		}
		snapshot.Lines = append(snapshot.Lines, normalized)
	}
	// Product resolution and Store derivation run at projection time
	// over the frozen generic mapping repository.
	snapshot.UnmappedLines = len(snapshot.Lines)
	snapshot.MappingComplete = len(snapshot.Lines) == 0
	return snapshot, nil
}

// normalizeLine maps one Shopify line item. External line identity is
// the canonical decimal line-item resource id. The external product
// identity is the parent Product resource id (never SKU, never the
// variant): order-line resolution joins commerce_product_mappings on
// (provider_key, external product id), which preserves Phase 9 Store
// ownership. Deleted/unavailable products leave the line unresolved but
// retained.
func normalizeLine(line gqlLineItem, currency string) (orders.OrderLine, error) {
	lineID, err := ParseGID(line.ID, ResourceLineItem)
	if err != nil {
		return orders.OrderLine{}, &orders.BlockedError{
			Code: orders.CodeOrderInvalid, Message: "invalid line item identity"}
	}
	parsedID, err := parseInt64(lineID)
	if err != nil {
		return orders.OrderLine{}, &orders.BlockedError{
			Code: orders.CodeOrderInvalid, Message: "invalid line item identity"}
	}
	subtotal, err := parseShopMoney(line.OriginalTotalSet, currency)
	if err != nil {
		return orders.OrderLine{}, &orders.BlockedError{
			Code: orders.CodeOrderInvalid, Message: "invalid line money"}
	}
	total, err := parseShopMoney(line.DiscountedTotalSet, currency)
	if err != nil {
		return orders.OrderLine{}, &orders.BlockedError{
			Code: orders.CodeOrderInvalid, Message: "invalid line money"}
	}
	var lineTax int64
	for _, taxLine := range line.TaxLines {
		amount, err := parseShopMoney(taxLine.PriceSet, currency)
		if err != nil {
			return orders.OrderLine{}, &orders.BlockedError{
				Code: orders.CodeOrderInvalid, Message: "invalid line money"}
		}
		lineTax += amount
	}
	externalProductID := ""
	if line.Product != nil && strings.TrimSpace(line.Product.ID) != "" {
		if productID, err := ParseGID(line.Product.ID, ResourceProduct); err == nil {
			externalProductID = productID
		}
		// A non-Product identity where a Product is required fails
		// closed as unresolved (never mapped to the wrong resource).
	}
	return orders.OrderLine{
		ExternalLineID:    parsedID,
		ExternalProductID: externalProductID,
		// VariationID stays 0: the frozen generic resolver refuses
		// variation lines, and Shopify order lines are keyed to the
		// parent Product identity (the MoonLight-managed variant is a
		// provider-internal detail of a one-variant product).
		SKU:           bounded(line.SKU, 100),
		Name:          bounded(line.Title, 200),
		Quantity:      line.Quantity,
		SubtotalMinor: subtotal,
		// Shopify exposes one tax figure per line; subtotal and total
		// tax carry the same exact value.
		SubtotalTaxMinor: lineTax,
		TotalMinor:       total,
		TotalTaxMinor:    lineTax,
	}, nil
}

// normalizeAddress maps one Shopify mailing address plus order-level
// contact fallback into the frozen fulfillment-only address shape.
func normalizeAddress(kind string, address *gqlAddress, remote *gqlOrder) orders.Address {
	normalized := orders.Address{Kind: kind}
	if address != nil {
		normalized.FirstName = bounded(address.FirstName, 100)
		normalized.LastName = bounded(address.LastName, 100)
		normalized.Company = bounded(address.Company, 100)
		normalized.Address1 = bounded(address.Address1, 200)
		normalized.Address2 = bounded(address.Address2, 200)
		normalized.City = bounded(address.City, 100)
		normalized.State = bounded(address.ProvinceCode, 100)
		normalized.Postcode = bounded(address.ZIP, 20)
		normalized.Country = bounded(address.CountryCodeV2, 2)
		normalized.Phone = bounded(address.Phone, 50)
	}
	if normalized.Email == "" {
		normalized.Email = bounded(remote.Email, 200)
	}
	if normalized.Phone == "" {
		normalized.Phone = bounded(remote.Phone, 50)
	}
	return normalized
}

// MapShopifyStatus maps Shopify display statuses into the frozen
// canonical vocabulary, deterministically and conservatively. Unknown
// values map to StatusUnknown with the raw string preserved in
// ProviderStatus (never guessed). Priority: cancelled > refunded >
// voided/expired payment > fulfillment hold > paid+fulfilled >
// partially refunded/paid > paid > pending > unknown.
func MapShopifyStatus(financialStatus, fulfillmentStatus string, cancelled bool) orders.CanonicalStatus {
	financial := strings.ToUpper(strings.TrimSpace(financialStatus))
	fulfillment := strings.ToUpper(strings.TrimSpace(fulfillmentStatus))
	switch {
	case cancelled:
		return orders.StatusCancelled
	case financial == "REFUNDED":
		return orders.StatusRefunded
	case financial == "VOIDED", financial == "EXPIRED":
		return orders.StatusFailed
	case financial == "PAID" && fulfillment == "FULFILLED":
		return orders.StatusCompleted
	case fulfillment == "ON_HOLD":
		return orders.StatusOnHold
	case financial == "PAID", financial == "PARTIALLY_REFUNDED":
		return orders.StatusProcessing
	case financial == "PENDING", financial == "AUTHORIZED", financial == "PARTIALLY_PAID":
		return orders.StatusPending
	default:
		return orders.StatusUnknown
	}
}

// parseShopifyTime parses RFC3339/ISO8601 Shopify timestamps to UTC.
func parseShopifyTime(raw string) (time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, errInvalidTime
	}
	if parsed, err := time.Parse(time.RFC3339Nano, trimmed); err == nil {
		return parsed.UTC(), nil
	}
	if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return parsed.UTC(), nil
	}
	return time.Time{}, errInvalidTime
}
