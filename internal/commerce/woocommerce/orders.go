package woocommerce

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
)

// Woo order currencies with exact minor-unit semantics MoonLight
// supports. Anything else blocks reconciliation explicitly.
var wooOrderCurrencies = map[string]bool{"EGP": true, "USD": true}

type wooOrderAddress struct {
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

type wooOrderLine struct {
	ID          json.Number `json:"id"`
	Name        string      `json:"name"`
	ProductID   json.Number `json:"product_id"`
	VariationID json.Number `json:"variation_id"`
	Quantity    json.Number `json:"quantity"`
	Subtotal    string      `json:"subtotal"`
	SubtotalTax string      `json:"subtotal_tax"`
	Total       string      `json:"total"`
	TotalTax    string      `json:"total_tax"`
	SKU         string      `json:"sku"`
}

type wooOrderResponse struct {
	ID                 json.Number     `json:"id"`
	Number             string          `json:"number"`
	Status             string          `json:"status"`
	Currency           string          `json:"currency"`
	DateCreatedGmt     string          `json:"date_created_gmt"`
	DateModifiedGmt    string          `json:"date_modified_gmt"`
	DatePaidGmt        string          `json:"date_paid_gmt"`
	DateCompletedGmt   string          `json:"date_completed_gmt"`
	DiscountTotal      string          `json:"discount_total"`
	ShippingTotal      string          `json:"shipping_total"`
	CartTax            string          `json:"cart_tax"`
	TotalTax           string          `json:"total_tax"`
	Total              string          `json:"total"`
	PricesIncludeTax   bool            `json:"prices_include_tax"`
	PaymentMethod      string          `json:"payment_method"`
	PaymentMethodTitle string          `json:"payment_method_title"`
	Billing            wooOrderAddress `json:"billing"`
	Shipping           wooOrderAddress `json:"shipping"`
	LineItems          []wooOrderLine  `json:"line_items"`
}

// GetOrder implements orders.CommerceOrderProvider: fetch the provider's
// current order and normalize it. The frozen product client (HTTPS,
// auth, bounds, classification) is reused unchanged.
func (p *WooCommerceProvider) GetOrder(ctx context.Context, externalOrderID string) (orders.OrderSnapshot, error) {
	id, err := parseWooID(externalOrderID)
	if err != nil {
		return orders.OrderSnapshot{}, &orders.BlockedError{Code: orders.CodeOrderInvalid, Message: "invalid external order id"}
	}
	var raw wooOrderResponse
	_, err = p.client.do(ctx, "GET", "/orders/"+strconv.FormatInt(id, 10), nil, nil, &raw)
	if err != nil {
		return orders.OrderSnapshot{}, mapOrderReadError(err)
	}
	responseID, err := wooJSONID(raw.ID)
	if err != nil {
		return orders.OrderSnapshot{}, &orders.BlockedError{Code: orders.CodeOrderInvalid, Message: "order response missing identity"}
	}
	if responseID != id {
		return orders.OrderSnapshot{}, &orders.BlockedError{Code: orders.CodeOrderConflict, Message: "order identity mismatch"}
	}
	return normalizeWooOrder(p.key, canonicalExternalID(id), raw)
}

func asCommerceProviderError(err error, target **commerce.ProviderError) bool {
	return errors.As(err, target)
}

// mapOrderReadError translates transport outcomes for reconciliation:
// 404 is a typed not-found condition (blocked: no silent tombstone from
// an ordinary GET unless the topic proves deletion); other provider
// failures keep their taxonomy for retry/blocked routing.
func mapOrderReadError(err error) error {
	var providerErr *commerce.ProviderError
	if asCommerceProviderError(err, &providerErr) {
		// 404 from the frozen client classifies as Conflict; for order
		// reads it means the provider has no such order.
		if providerErr.Kind == commerce.ErrorConflict {
			return &orders.BlockedError{Code: orders.CodeOrderNotFound, Message: "provider order not found"}
		}
		return err
	}
	return err
}

// normalizeWooOrder converts one verified Woo order into the
// provider-neutral snapshot. Product resolution happens at projection
// time against the generic mapping table, never here.
func normalizeWooOrder(key commerce.ProviderKey, externalOrderID string, raw wooOrderResponse) (orders.OrderSnapshot, error) {
	fail := func(format string, args ...any) (orders.OrderSnapshot, error) {
		return orders.OrderSnapshot{}, &orders.BlockedError{Code: orders.CodeOrderInvalid, Message: fmt.Sprintf(format, args...)}
	}
	currency := strings.ToUpper(strings.TrimSpace(raw.Currency))
	if !wooOrderCurrencies[currency] {
		return orders.OrderSnapshot{}, &orders.BlockedError{Code: orders.CodeOrderCurrencyUnsupported, Message: fmt.Sprintf("unsupported order currency %q", currency)}
	}
	money := func(value string, what string) (int64, error) {
		amount, err := orders.ParseMinorUnits(value, currency)
		if err != nil {
			return 0, &orders.BlockedError{Code: orders.CodeOrderInvalid, Message: fmt.Sprintf("invalid %s", what)}
		}
		return amount, nil
	}
	created, err := parseWooTime(raw.DateCreatedGmt, "created")
	if err != nil {
		return fail("%v", err)
	}
	modified, err := parseWooTime(raw.DateModifiedGmt, "modified")
	if err != nil {
		return fail("%v", err)
	}
	paid, err := parseOptionalWooTime(raw.DatePaidGmt)
	if err != nil {
		return fail("%v", err)
	}
	completed, err := parseOptionalWooTime(raw.DateCompletedGmt)
	if err != nil {
		return fail("%v", err)
	}
	discount, err := money(raw.DiscountTotal, "discount total")
	if err != nil {
		return orders.OrderSnapshot{}, err
	}
	shipping, err := money(raw.ShippingTotal, "shipping total")
	if err != nil {
		return orders.OrderSnapshot{}, err
	}
	cartTax, err := money(raw.CartTax, "cart tax")
	if err != nil {
		return orders.OrderSnapshot{}, err
	}
	totalTax, err := money(raw.TotalTax, "total tax")
	if err != nil {
		return orders.OrderSnapshot{}, err
	}
	total, err := money(raw.Total, "total")
	if err != nil {
		return orders.OrderSnapshot{}, err
	}
	snapshot := orders.OrderSnapshot{
		ProviderKey: string(key), ExternalOrderID: externalOrderID,
		OrderNumber:    strings.TrimSpace(raw.Number),
		ProviderStatus: strings.TrimSpace(raw.Status),
		Canonical:      orders.MapWooStatus(raw.Status),
		Currency:       currency,
		DiscountMinor:  discount, ShippingMinor: shipping,
		CartTaxMinor: cartTax, TotalTaxMinor: totalTax, TotalMinor: total,
		PricesIncludeTax: raw.PricesIncludeTax,
		CreatedAt:        created, ModifiedAt: modified, PaidAt: paid, CompletedAt: completed,
		PaymentMethod:      strings.TrimSpace(raw.PaymentMethod),
		PaymentMethodTitle: strings.TrimSpace(raw.PaymentMethodTitle),
		Customer: orders.Customer{
			FirstName: strings.TrimSpace(raw.Billing.FirstName),
			LastName:  strings.TrimSpace(raw.Billing.LastName),
			Email:     strings.TrimSpace(raw.Billing.Email),
			Phone:     strings.TrimSpace(raw.Billing.Phone),
		},
		Billing:  normalizeWooAddress("billing", raw.Billing),
		Shipping: normalizeWooAddress("shipping", raw.Shipping),
	}
	for position, item := range raw.LineItems {
		line, err := normalizeWooLine(item, currency, position)
		if err != nil {
			return orders.OrderSnapshot{}, err
		}
		snapshot.Lines = append(snapshot.Lines, line)
	}
	// Resolution has not run yet: every line starts unmapped here; the
	// projector fills MoonlightProduct/Mapped and recomputes completeness.
	snapshot.UnmappedLines = len(snapshot.Lines)
	snapshot.MappingComplete = len(snapshot.Lines) == 0
	return snapshot, nil
}

func normalizeWooAddress(kind string, raw wooOrderAddress) orders.Address {
	return orders.Address{
		Kind:      kind,
		FirstName: strings.TrimSpace(raw.FirstName), LastName: strings.TrimSpace(raw.LastName),
		Company:  strings.TrimSpace(raw.Company),
		Address1: strings.TrimSpace(raw.Address1), Address2: strings.TrimSpace(raw.Address2),
		City: strings.TrimSpace(raw.City), State: strings.TrimSpace(raw.State),
		Postcode: strings.TrimSpace(raw.Postcode), Country: strings.TrimSpace(raw.Country),
		Email: strings.TrimSpace(raw.Email), Phone: strings.TrimSpace(raw.Phone),
	}
}

func normalizeWooLine(item wooOrderLine, currency string, position int) (orders.OrderLine, error) {
	fail := func(format string, args ...any) (orders.OrderLine, error) {
		return orders.OrderLine{}, &orders.BlockedError{Code: orders.CodeOrderInvalid, Message: fmt.Sprintf(format, args...)}
	}
	lineID, err := wooJSONID(item.ID)
	if err != nil || lineID <= 0 {
		return fail("line %d has invalid line id", position)
	}
	quantity, err := wooJSONInt(item.Quantity)
	if err != nil || quantity <= 0 {
		return fail("line %d has invalid quantity", position)
	}
	money := func(value string, what string) (int64, error) {
		amount, err := orders.ParseMinorUnits(value, currency)
		if err != nil {
			return 0, &orders.BlockedError{Code: orders.CodeOrderInvalid, Message: fmt.Sprintf("line %d invalid %s", position, what)}
		}
		return amount, nil
	}
	subtotal, err := money(item.Subtotal, "subtotal")
	if err != nil {
		return orders.OrderLine{}, err
	}
	subtotalTax, err := money(item.SubtotalTax, "subtotal tax")
	if err != nil {
		return orders.OrderLine{}, err
	}
	total, err := money(item.Total, "total")
	if err != nil {
		return orders.OrderLine{}, err
	}
	totalTax, err := money(item.TotalTax, "total tax")
	if err != nil {
		return orders.OrderLine{}, err
	}
	line := orders.OrderLine{
		ExternalLineID: lineID,
		SKU:            strings.TrimSpace(item.SKU),
		Name:           strings.TrimSpace(item.Name),
		Quantity:       quantity,
		SubtotalMinor:  subtotal, SubtotalTaxMinor: subtotalTax,
		TotalMinor: total, TotalTaxMinor: totalTax,
	}
	if productID, err := wooJSONID(item.ProductID); err == nil && productID > 0 {
		line.ExternalProductID = strconv.FormatInt(productID, 10)
	}
	if variationID, err := wooJSONID(item.VariationID); err == nil && variationID > 0 {
		line.VariationID = variationID
		line.UnsupportedReason = "variation"
	}
	if line.Name == "" {
		return fail("line %d has no name", position)
	}
	return line, nil
}

// wooJSONID reads a Woo integer identity from decoded JSON numbers,
// strings, or floats without floating-point math.
func wooJSONID(value json.Number) (int64, error) {
	return parseWooID(value.String())
}

// wooJSONInt reads an integer quantity from decoded JSON.
func wooJSONInt(value json.Number) (int64, error) {
	trimmed := strings.TrimSpace(value.String())
	if trimmed == "" {
		return 0, fmt.Errorf("empty quantity")
	}
	if strings.ContainsAny(trimmed, ".eE") {
		return 0, fmt.Errorf("non-integer quantity %q", value.String())
	}
	return strconv.ParseInt(trimmed, 10, 64)
}

// parseWooTime prefers exact GMT timestamps, accepting RFC3339 or plain
// site-clock ISO8601 interpreted as UTC.
func parseWooTime(value, what string) (time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}, fmt.Errorf("missing order %s time", what)
	}
	if parsed, err := time.Parse(time.RFC3339, trimmed); err == nil {
		return parsed.UTC(), nil
	}
	if parsed, err := time.ParseInLocation("2006-01-02T15:04:05", trimmed, time.UTC); err == nil {
		return parsed.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("invalid order %s time %q", what, value)
}

func parseOptionalWooTime(value string) (*time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parsed, err := parseWooTime(value, "optional")
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}
