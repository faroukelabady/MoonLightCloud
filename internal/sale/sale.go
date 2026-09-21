// Package sale owns the sale.finalized.v1 Cloud DTO, its strict validator,
// and the asynchronous projector. Field names and types mirror the
// MoonLightRetail Phase-2A immutable DTO exactly (read-only contract, never
// a shared module). No invented fields: no historical tags, no bilingual
// product-name snapshots — v1 carries one product_name and no tags.
package sale

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// EventSaleFinalizedV1 is the registered event type.
const EventSaleFinalizedV1 = "sale.finalized.v1"

// Processor name for durable processing state.
const ProcessorSaleProjectionV1 = "sale_projection.v1"

// Supported currencies for v1 (desktop-enforced).
const (
	CurrencyEGP = "EGP"
	CurrencyUSD = "USD"
)

// Payment methods the desktop sale path emits.
const (
	MethodCash = "cash"
	MethodCard = "card"
)

// FX microrate scale and bound mirror the desktop contract exactly:
// rate string decimal, rate_microrate = rate × 1_000_000, max 999999.999999.
const (
	fxMicrorateScale int64 = 1000000
	maxMicrorate     int64 = 999999999999
)

// Money is one integer-minor-units amount with its currency. Decoding into
// int64 rejects float JSON (1250.50) and overflow at parse time.
type Money struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

// FxSnapshot preserves the historical microrate snapshot. RateMicrorate is
// authoritative; Rate is the exact decimal string, validated to match.
type FxSnapshot struct {
	Base          string `json:"base"`
	Quote         string `json:"quote"`
	Rate          string `json:"rate"`
	RateMicrorate int64  `json:"rate_microrate"`
}

// ShopSnapshot is the exact 7-field historical shop snapshot.
type ShopSnapshot struct {
	NameAR          string `json:"name_ar"`
	NameEN          string `json:"name_en"`
	AddressAR       string `json:"address_ar"`
	AddressEN       string `json:"address_en"`
	Phone           string `json:"phone"`
	ReceiptFooterAR string `json:"receipt_footer_ar"`
	ReceiptFooterEN string `json:"receipt_footer_en"`
}

// ActorSnapshot carries the cashier snapshot. IDs stay local operational
// identities; no PIN/password data exists in this event by contract.
type ActorSnapshot struct {
	CashierID   *string `json:"cashier_id"`
	CashierName *string `json:"cashier_name"`
}

// ClassificationSnapshot is one historical bilingual category snapshot.
type ClassificationSnapshot struct {
	CategoryID string `json:"category_id"`
	NameAR     string `json:"name_ar"`
	NameEN     string `json:"name_en"`
}

// LineClassifications groups root + subcategory snapshots of one line.
type LineClassifications struct {
	Roots         []ClassificationSnapshot `json:"roots"`
	Subcategories []ClassificationSnapshot `json:"subcategories"`
}

// SaleLine is one immutable historical sale line.
type SaleLine struct {
	SaleItemID      string              `json:"sale_item_id"`
	ProductID       *string             `json:"product_id"`
	VariantID       *string             `json:"variant_id"`
	SKU             string              `json:"sku"`
	ProductName     string              `json:"product_name"`
	WidthCM         *int                `json:"width_cm"`
	HeightCM        *int                `json:"height_cm"`
	Quantity        int                 `json:"quantity"`
	UnitPrice       Money               `json:"unit_price"`
	Cost            *Money              `json:"cost"`
	LineTotal       Money               `json:"line_total"`
	Classifications LineClassifications `json:"classifications"`
}

// EventPayment is one payment contribution snapshot.
type EventPayment struct {
	Method         string  `json:"method"`
	Amount         Money   `json:"amount"`
	ChangeGiven    Money   `json:"change_given"`
	TransactionRef *string `json:"transaction_ref"`
}

// SaleTotals carries the authoritative sale arithmetic.
type SaleTotals struct {
	Subtotal Money `json:"subtotal"`
	Discount Money `json:"discount"`
	Tax      Money `json:"tax"`
	Total    Money `json:"total"`
}

// Payload is the sale.finalized.v1 payload object.
type Payload struct {
	SaleID     string         `json:"sale_id"`
	SaleNumber string         `json:"sale_number"`
	Channel    string         `json:"channel"`
	OccurredAt string         `json:"occurred_at"`
	PaidAt     string         `json:"paid_at"`
	Shop       ShopSnapshot   `json:"shop"`
	Actor      ActorSnapshot  `json:"actor"`
	Currency   string         `json:"currency"`
	Fx         *FxSnapshot    `json:"fx"`
	Totals     SaleTotals     `json:"totals"`
	Lines      []SaleLine     `json:"lines"`
	Payments   []EventPayment `json:"payments"`
}

// Validated is the parsed, validated form used by the projector.
type Validated struct {
	Payload
	Occurred time.Time
	Paid     time.Time
}

// Decode parses the canonical payload bytes strictly: unknown fields are
// tolerated per compatibility policy, but duplicate keys were already
// rejected at the envelope layer and float money fails on int64 decode.
func Decode(raw json.RawMessage) (Payload, error) {
	var p Payload
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&p); err != nil {
		return Payload{}, apperr.New(apperr.Unprocessable, "sale payload is not a valid sale.finalized.v1 object")
	}
	if dec.More() {
		return Payload{}, apperr.New(apperr.Unprocessable, "sale payload has trailing data")
	}
	return p, nil
}

// Validate enforces every invariant the desktop sale path guarantees —
// nothing invented. Checked integer arithmetic throughout; any overflow,
// float, or inconsistency is 422 before ACK.
func Validate(p Payload) (Validated, error) {
	fail := func(format string, args ...any) (Validated, error) {
		return Validated{}, apperr.New(apperr.Unprocessable, "invalid sale.finalized.v1: "+fmt.Sprintf(format, args...))
	}
	if !isUUID(p.SaleID) {
		return fail("sale_id must be a UUID")
	}
	if len(p.SaleNumber) == 0 || len(p.SaleNumber) > 64 {
		return fail("sale_number must be 1..64 chars")
	}
	if p.Channel != "STORE" {
		return fail("channel must be STORE for v1")
	}
	occurred, err := parseTime(p.OccurredAt)
	if err != nil {
		return fail("occurred_at must be RFC3339")
	}
	paid, err := parseTime(p.PaidAt)
	if err != nil {
		return fail("paid_at must be RFC3339")
	}
	if paid.Before(occurred) {
		return fail("paid_at must not precede occurred_at")
	}
	if p.Currency != CurrencyEGP && p.Currency != CurrencyUSD {
		return fail("currency must be EGP or USD")
	}
	if err := checkShop(p.Shop); err != nil {
		return fail("%v", err)
	}
	if err := checkFx(p.Fx, p.Currency); err != nil {
		return fail("%v", err)
	}
	money := func(m Money, field string) error {
		if m.Currency != p.Currency {
			return fmt.Errorf("%s currency must match sale currency", field)
		}
		return nil
	}
	for _, m := range []struct {
		v Money
		f string
	}{
		{p.Totals.Subtotal, "totals.subtotal"}, {p.Totals.Discount, "totals.discount"},
		{p.Totals.Tax, "totals.tax"}, {p.Totals.Total, "totals.total"},
	} {
		if err := money(m.v, m.f); err != nil {
			return fail("%v", err)
		}
		if m.v.AmountMinor < 0 {
			return fail("%s must not be negative", m.f)
		}
	}
	if len(p.Lines) == 0 {
		return fail("lines must not be empty")
	}
	if len(p.Payments) == 0 {
		return fail("payments must not be empty")
	}
	seenItems := map[string]bool{}
	var subtotal int64
	for i := range p.Lines {
		line := &p.Lines[i]
		if !isUUID(line.SaleItemID) {
			return fail("lines[%d].sale_item_id must be a UUID", i)
		}
		if seenItems[line.SaleItemID] {
			return fail("duplicate sale_item_id %s", line.SaleItemID)
		}
		seenItems[line.SaleItemID] = true
		if line.SKU == "" || len(line.SKU) > 128 {
			return fail("lines[%d].sku must be 1..128 chars", i)
		}
		if line.ProductName == "" {
			return fail("lines[%d].product_name is required", i)
		}
		for name, id := range map[string]*string{"product_id": line.ProductID, "variant_id": line.VariantID} {
			if id != nil && !isUUID(*id) {
				return fail("lines[%d].%s must be a UUID", i, name)
			}
		}
		for name, dim := range map[string]*int{"width_cm": line.WidthCM, "height_cm": line.HeightCM} {
			if dim != nil && *dim < 1 {
				return fail("lines[%d].%s must be a positive whole centimeter", i, name)
			}
		}
		if line.Quantity < 1 || line.Quantity > math.MaxInt32 {
			return fail("lines[%d].quantity must be within [1, 2^31-1]", i)
		}
		for _, m := range []struct {
			v Money
			f string
		}{
			{line.UnitPrice, "unit_price"}, {line.LineTotal, "line_total"},
		} {
			if err := money(m.v, fmt.Sprintf("lines[%d].%s", i, m.f)); err != nil {
				return fail("%v", err)
			}
			if m.v.AmountMinor < 0 {
				return fail("lines[%d].%s must not be negative", i, m.f)
			}
		}
		if line.Cost != nil {
			if err := money(*line.Cost, fmt.Sprintf("lines[%d].cost", i)); err != nil {
				return fail("%v", err)
			}
			if line.Cost.AmountMinor < 0 {
				return fail("lines[%d].cost must not be negative", i)
			}
		}
		// Desktop guarantee: line_total = quantity × unit_price.
		want, err := mulChecked(int64(line.Quantity), line.UnitPrice.AmountMinor)
		if err != nil {
			return fail("lines[%d] quantity × unit_price overflows", i)
		}
		if line.LineTotal.AmountMinor != want {
			return fail("lines[%d].line_total must equal quantity × unit_price", i)
		}
		subtotal, err = addChecked(subtotal, line.LineTotal.AmountMinor)
		if err != nil {
			return fail("subtotal overflows")
		}
		if len(line.Classifications.Roots) != 1 {
			return fail("lines[%d] must carry exactly one root classification", i)
		}
		if err := checkLineClassifications(&line.Classifications, i); err != nil {
			return fail("%v", err)
		}
	}
	// Desktop guarantee: subtotal = Σ lines; total = max(0, subtotal+tax-discount).
	if p.Totals.Subtotal.AmountMinor != subtotal {
		return fail("totals.subtotal must equal the sum of line totals")
	}
	base, err := addChecked(subtotal, p.Totals.Tax.AmountMinor)
	if err != nil {
		return fail("subtotal + tax overflows")
	}
	var wantTotal int64
	if p.Totals.Discount.AmountMinor >= base {
		wantTotal = 0
	} else {
		wantTotal = base - p.Totals.Discount.AmountMinor
	}
	if p.Totals.Total.AmountMinor != wantTotal {
		return fail("totals.total must equal max(0, subtotal + tax - discount)")
	}
	for i := range p.Payments {
		pay := &p.Payments[i]
		if pay.Method != MethodCash && pay.Method != MethodCard {
			return fail("payments[%d].method must be cash or card", i)
		}
		if err := money(pay.Amount, fmt.Sprintf("payments[%d].amount", i)); err != nil {
			return fail("%v", err)
		}
		if err := money(pay.ChangeGiven, fmt.Sprintf("payments[%d].change_given", i)); err != nil {
			return fail("%v", err)
		}
		if pay.Amount.AmountMinor < 1 {
			return fail("payments[%d].amount must be positive", i)
		}
		if pay.ChangeGiven.AmountMinor < 0 {
			return fail("payments[%d].change_given must not be negative", i)
		}
	}
	// Payment parity with MoonLightRetail Checkout (service/sale/service.go
	// buildPayments): aggregate SUM(amount) must equal the authoritative
	// total within ±1 minor unit. Change is NOT netted — Retail sums
	// AmountCents only and ignores ChangeGivenCents in the balance check.
	// Checked integer arithmetic; overflow rejects before ACK.
	if err := checkPaymentAggregate(p.Payments, p.Totals.Total.AmountMinor); err != nil {
		return fail("%v", err)
	}
	return Validated{Payload: p, Occurred: occurred, Paid: paid}, nil
}

// checkPaymentAggregate mirrors Retail buildPayments exactly: sum of
// payment amounts (change ignored) within ±1 of the sale total.
func checkPaymentAggregate(payments []EventPayment, total int64) error {
	var sum int64
	for i := range payments {
		var err error
		sum, err = addChecked(sum, payments[i].Amount.AmountMinor)
		if err != nil {
			return fmt.Errorf("payments aggregate overflows")
		}
	}
	diff, err := subChecked(sum, total)
	if err != nil {
		return fmt.Errorf("payments aggregate overflows")
	}
	if diff < -1 || diff > 1 {
		return fmt.Errorf("payments must equal the authoritative total within ±1 minor unit")
	}
	return nil
}

func checkShop(s ShopSnapshot) error {
	for name, v := range map[string]string{
		"name_ar": s.NameAR, "name_en": s.NameEN, "phone": s.Phone,
	} {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("shop.%s is required", name)
		}
	}
	return nil
}

// checkLineClassifications validates one line's full classification
// snapshot (P2D-MED-03): exactly one root is enforced by the caller; all
// category IDs must be unique across roots AND subcategories combined — a
// category may not appear as both root and subcategory on one line.
func checkLineClassifications(c *LineClassifications, line int) error {
	seen := make(map[string]string, len(c.Roots)+len(c.Subcategories))
	check := func(list []ClassificationSnapshot, group string) error {
		for j := range list {
			item := &list[j]
			if !isUUID(item.CategoryID) {
				return fmt.Errorf("lines[%d].%s[%d].category_id must be a UUID", line, group, j)
			}
			if prev, dup := seen[item.CategoryID]; dup {
				return fmt.Errorf("lines[%d] duplicate category_id %s across %s and %s", line, item.CategoryID, prev, group)
			}
			seen[item.CategoryID] = group
			if strings.TrimSpace(item.NameAR) == "" || strings.TrimSpace(item.NameEN) == "" {
				return fmt.Errorf("lines[%d].%s[%d] names are required", line, group, j)
			}
		}
		return nil
	}
	if err := check(c.Roots, "roots"); err != nil {
		return err
	}
	return check(c.Subcategories, "subcategories")
}

// checkFx validates the historical snapshot: EGP sales never carry FX;
// USD sales require the exact USD→EGP pair the desktop emits
// (BuildSaleFinalized hardcodes Base USD / Quote EGP; the desktop only
// fetches GetLatestExchangeRate("USD","EGP")). A present snapshot must be
// internally exact — microrate equals the decimal rate × 1e6.
func checkFx(fx *FxSnapshot, currency string) error {
	if currency == CurrencyEGP {
		if fx != nil {
			return fmt.Errorf("fx must be absent for EGP sales")
		}
		return nil
	}
	if fx == nil {
		return fmt.Errorf("fx is required for USD sales")
	}
	if fx.Base != CurrencyUSD || fx.Quote != CurrencyEGP {
		return fmt.Errorf("fx pair must be USD→EGP for USD sales")
	}
	micro, err := parseMicrorate(fx.Rate)
	if err != nil {
		return fmt.Errorf("fx.rate invalid: %v", err)
	}
	if fx.RateMicrorate != micro {
		return fmt.Errorf("fx.rate_microrate must equal rate × 1000000")
	}
	return nil
}

// parseMicrorate mirrors the desktop decimal handling exactly (big.Rat,
// no floats): positive decimal, half-up at the 7th digit, ≤ 999999.999999.
func parseMicrorate(rate string) (int64, error) {
	text := strings.TrimSpace(rate)
	if text == "" {
		return 0, fmt.Errorf("empty rate")
	}
	if strings.HasPrefix(text, "-") || strings.HasPrefix(text, "+") {
		return 0, fmt.Errorf("rate must be unsigned")
	}
	rational, ok := new(big.Rat).SetString(text)
	if !ok || rational.Sign() <= 0 {
		return 0, fmt.Errorf("rate must be a positive decimal")
	}
	scaled := new(big.Rat).Mul(rational, big.NewRat(fxMicrorateScale, 1))
	// Round half-up: (2n+1)/2.
	num := scaled.Num()
	den := scaled.Denom()
	quot := new(big.Int).Quo(num, den)
	rem := new(big.Int).Rem(num, den)
	rem.Abs(rem)
	if new(big.Int).Mul(rem, big.NewInt(2)).Cmp(den.Abs(den)) >= 0 {
		quot.Add(quot, big.NewInt(1))
	}
	if !quot.IsInt64() {
		return 0, fmt.Errorf("rate out of range")
	}
	micro := quot.Int64()
	if micro <= 0 || micro > maxMicrorate {
		return 0, fmt.Errorf("rate out of range")
	}
	return micro, nil
}

func parseTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

func mulChecked(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	if a > math.MaxInt64/b {
		return 0, fmt.Errorf("overflow")
	}
	return a * b, nil
}

func addChecked(a, b int64) (int64, error) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, fmt.Errorf("overflow")
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, fmt.Errorf("overflow")
	}
	return a + b, nil
}

func subChecked(a, b int64) (int64, error) {
	if b > 0 && a < math.MinInt64+b {
		return 0, fmt.Errorf("overflow")
	}
	if b < 0 && a > math.MaxInt64+b {
		return 0, fmt.Errorf("overflow")
	}
	return a - b, nil
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
