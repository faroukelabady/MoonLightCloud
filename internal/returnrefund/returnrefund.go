// Package returnrefund owns the sale.return_refund.finalized.v1 Cloud DTO
// and its strict ingestion-time validator. Field names and types mirror the
// MoonLightRetail Phase-4A immutable DTO exactly (read-only contract, never
// a shared module). Phase 4A validates and durably accepts the event only:
// no projection, no reporting changes. Cumulative business validation
// (over-return across events, Sale linkage resolution) belongs to Phase 4B
// projection; transport validates event-local invariants exclusively.
package returnrefund

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// EventReturnRefundFinalizedV1 is the registered event type.
const EventReturnRefundFinalizedV1 = "sale.return_refund.finalized.v1"

// Supported currencies for v1 (desktop-enforced, inherited from the sale).
const (
	CurrencyEGP = "EGP"
	CurrencyUSD = "USD"
)

// Correction kinds the desktop return path emits.
const (
	KindReturn = "return"
	KindVoid   = "void"
)

// Refund methods the desktop return path emits. store_credit is a sale
// payment method only and is rejected here.
const (
	MethodCash  = "cash"
	MethodCard  = "card"
	MethodOther = "other"
)

// Return reasons mirror the desktop allowlist exactly. A new reason
// requires a new event version, never silent reinterpretation.
var validReasons = map[string]bool{
	"customer_changed_mind": true,
	"damaged_item":          true,
	"wrong_item":            true,
	"cashier_mistake":       true,
	"duplicate_sale":        true,
	"wrong_products":        true,
	"wrong_payment":         true,
	"other":                 true,
}

// FX microrate scale and bound mirror the desktop contract exactly.
const (
	fxMicrorateScale int64 = 1000000
	maxMicrorate     int64 = 999999999999
)

// Money is one integer-minor-units amount with its currency. Decoding into
// int64 rejects float JSON and overflow at parse time.
type Money struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
}

// FxSnapshot preserves the original Sale's historical microrate snapshot.
// RateMicrorate is authoritative; Rate is the exact decimal string.
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

// ActorSnapshot carries the handling-user snapshot. IDs stay local
// operational identities; no PIN/password data exists by contract.
type ActorSnapshot struct {
	UserID   *string `json:"user_id"`
	UserName *string `json:"user_name"`
}

// ReturnTotals carries the authoritative header-level refund arithmetic.
type ReturnTotals struct {
	Gross       Money `json:"gross"`
	Discount    Money `json:"discount"`
	Tax         Money `json:"tax"`
	RefundTotal Money `json:"refund_total"`
}

// ReturnLine is one immutable returned-line snapshot linked to the
// original sale line.
type ReturnLine struct {
	OriginalSaleLineID string  `json:"original_sale_line_id"`
	ProductID          *string `json:"product_id"`
	Quantity           int     `json:"quantity"`
	Restocked          bool    `json:"restocked"`
	Gross              Money   `json:"gross"`
	Discount           Money   `json:"discount"`
	Tax                Money   `json:"tax"`
	Refund             Money   `json:"refund"`
	Cost               *Money  `json:"cost"`
}

// RefundPayment is one refund contribution snapshot. Refunds pay out
// exactly: there is no change_given.
type RefundPayment struct {
	Method         string  `json:"method"`
	Amount         Money   `json:"amount"`
	TransactionRef *string `json:"transaction_ref"`
}

// Payload is the sale.return_refund.finalized.v1 payload object.
type Payload struct {
	ReturnRefundID string          `json:"return_refund_id"`
	ReturnNumber   string          `json:"return_number"`
	Kind           string          `json:"kind"`
	Reason         string          `json:"reason"`
	Note           *string         `json:"note"`
	SaleID         string          `json:"sale_id"`
	SaleNumber     string          `json:"sale_number"`
	SaleEventID    *string         `json:"sale_event_id"`
	Channel        string          `json:"channel"`
	OccurredAt     string          `json:"occurred_at"`
	Shop           ShopSnapshot    `json:"shop"`
	Actor          ActorSnapshot   `json:"actor"`
	Currency       string          `json:"currency"`
	Fx             *FxSnapshot     `json:"fx"`
	Totals         ReturnTotals    `json:"totals"`
	Lines          []ReturnLine    `json:"lines"`
	Refunds        []RefundPayment `json:"refunds"`
}

// Validated is the parsed, validated form accepted into the inbox.
type Validated struct {
	Payload
	Occurred time.Time
}

// Decode parses the canonical payload bytes strictly: unknown fields are
// tolerated per compatibility policy (same as sale.finalized.v1), but
// duplicate keys were already rejected at the envelope layer and float
// money fails on int64 decode.
func Decode(raw json.RawMessage) (Payload, error) {
	var p Payload
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&p); err != nil {
		return Payload{}, apperr.New(apperr.Unprocessable, "return payload is not a valid sale.return_refund.finalized.v1 object")
	}
	if dec.More() {
		return Payload{}, apperr.New(apperr.Unprocessable, "return payload has trailing data")
	}
	return p, nil
}

// Validate enforces every event-local invariant the desktop return path
// guarantees. Checked integer arithmetic throughout; any overflow, float,
// or inconsistency is 422 before ACK. Cumulative validation (totals across
// events, Sale projection linkage) is Phase 4B projection, never transport.
func Validate(p Payload) (Validated, error) {
	fail := func(format string, args ...any) (Validated, error) {
		return Validated{}, apperr.New(apperr.Unprocessable, "invalid sale.return_refund.finalized.v1: "+fmt.Sprintf(format, args...))
	}
	if !isUUID(p.ReturnRefundID) {
		return fail("return_refund_id must be a UUID")
	}
	if len(p.ReturnNumber) == 0 || len(p.ReturnNumber) > 64 {
		return fail("return_number must be 1..64 chars")
	}
	if p.Kind != KindReturn && p.Kind != KindVoid {
		return fail("kind must be return or void")
	}
	if !validReasons[p.Reason] {
		return fail("reason is not a supported v1 reason")
	}
	if p.Note != nil && utf8.RuneCountInString(strings.TrimSpace(*p.Note)) > 500 {
		return fail("note must be at most 500 chars")
	}
	if !isUUID(p.SaleID) {
		return fail("sale_id must be a UUID")
	}
	if len(p.SaleNumber) == 0 || len(p.SaleNumber) > 64 {
		return fail("sale_number must be 1..64 chars")
	}
	if p.SaleEventID != nil && !isUUID(*p.SaleEventID) {
		return fail("sale_event_id must be a UUID")
	}
	if p.Channel != "STORE" {
		return fail("channel must be STORE for v1")
	}
	occurred, err := parseTime(p.OccurredAt)
	if err != nil {
		return fail("occurred_at must be RFC3339")
	}
	if p.Currency != CurrencyEGP && p.Currency != CurrencyUSD {
		return fail("currency must be EGP or USD")
	}
	if err := checkShop(p.Shop); err != nil {
		return fail("%v", err)
	}
	if err := checkActor(p.Actor); err != nil {
		return fail("%v", err)
	}
	if err := checkFx(p.Fx, p.Currency); err != nil {
		return fail("%v", err)
	}
	money := func(m Money, field string) error {
		if m.Currency != p.Currency {
			return fmt.Errorf("%s currency must match return currency", field)
		}
		return nil
	}
	for _, m := range []struct {
		v Money
		f string
	}{
		{p.Totals.Gross, "totals.gross"}, {p.Totals.Discount, "totals.discount"},
		{p.Totals.Tax, "totals.tax"}, {p.Totals.RefundTotal, "totals.refund_total"},
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
	seenLines := map[string]bool{}
	var gross, discount, tax, refund int64
	for i := range p.Lines {
		line := &p.Lines[i]
		if !isUUID(line.OriginalSaleLineID) {
			return fail("lines[%d].original_sale_line_id must be a UUID", i)
		}
		if seenLines[line.OriginalSaleLineID] {
			return fail("duplicate original_sale_line_id %s", line.OriginalSaleLineID)
		}
		seenLines[line.OriginalSaleLineID] = true
		if line.ProductID != nil && !isUUID(*line.ProductID) {
			return fail("lines[%d].product_id must be a UUID", i)
		}
		if line.Quantity < 1 || line.Quantity > math.MaxInt32 {
			return fail("lines[%d].quantity must be within [1, 2^31-1]", i)
		}
		for _, m := range []struct {
			v Money
			f string
		}{
			{line.Gross, "gross"}, {line.Discount, "discount"},
			{line.Tax, "tax"}, {line.Refund, "refund"},
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
		var err error
		if gross, err = addChecked(gross, line.Gross.AmountMinor); err != nil {
			return fail("gross overflows")
		}
		if discount, err = addChecked(discount, line.Discount.AmountMinor); err != nil {
			return fail("discount overflows")
		}
		if tax, err = addChecked(tax, line.Tax.AmountMinor); err != nil {
			return fail("tax overflows")
		}
		if refund, err = addChecked(refund, line.Refund.AmountMinor); err != nil {
			return fail("refund overflows")
		}
	}
	// Desktop guarantee: header breakdowns are exact sums of line values.
	if p.Totals.Gross.AmountMinor != gross {
		return fail("totals.gross must equal the sum of line gross values")
	}
	if p.Totals.Discount.AmountMinor != discount {
		return fail("totals.discount must equal the sum of line discount values")
	}
	if p.Totals.Tax.AmountMinor != tax {
		return fail("totals.tax must equal the sum of line tax values")
	}
	if p.Totals.RefundTotal.AmountMinor != refund {
		return fail("totals.refund_total must equal the sum of line refund values")
	}
	// Desktop guarantee: refund payments sum exactly to the authoritative
	// refund total — no tolerance, unlike sale payments. Empty refunds are
	// valid only for a zero refund total.
	if p.Totals.RefundTotal.AmountMinor == 0 {
		if len(p.Refunds) != 0 {
			return fail("refunds must be empty for a zero refund total")
		}
		return Validated{Payload: p, Occurred: occurred}, nil
	}
	if len(p.Refunds) == 0 {
		return fail("refunds must not be empty for a nonzero refund total")
	}
	var paid int64
	for i := range p.Refunds {
		pay := &p.Refunds[i]
		if pay.Method != MethodCash && pay.Method != MethodCard && pay.Method != MethodOther {
			return fail("refunds[%d].method must be cash, card, or other", i)
		}
		if err := money(pay.Amount, fmt.Sprintf("refunds[%d].amount", i)); err != nil {
			return fail("%v", err)
		}
		if pay.Amount.AmountMinor < 1 {
			return fail("refunds[%d].amount must be positive", i)
		}
		var err error
		if paid, err = addChecked(paid, pay.Amount.AmountMinor); err != nil {
			return fail("refunds aggregate overflows")
		}
	}
	if paid != p.Totals.RefundTotal.AmountMinor {
		return fail("refunds must equal the authoritative refund total")
	}
	return Validated{Payload: p, Occurred: occurred}, nil
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

func checkActor(a ActorSnapshot) error {
	if a.UserID != nil && !isUUID(*a.UserID) {
		return fmt.Errorf("actor.user_id must be a UUID")
	}
	if a.UserName != nil && strings.TrimSpace(*a.UserName) == "" {
		return fmt.Errorf("actor.user_name must not be blank")
	}
	return nil
}

// checkFx enforces the same historical snapshot rule as sales: EGP returns
// never carry FX; USD returns require the exact USD→EGP pair from the
// original Sale snapshot. Phase 4B reverses with this snapshot, never a
// live rate.
func checkFx(fx *FxSnapshot, currency string) error {
	if currency == CurrencyEGP {
		if fx != nil {
			return fmt.Errorf("fx must be absent for EGP returns")
		}
		return nil
	}
	if fx == nil {
		return fmt.Errorf("fx is required for USD returns")
	}
	if fx.Base != CurrencyUSD || fx.Quote != CurrencyEGP {
		return fmt.Errorf("fx pair must be USD→EGP for USD returns")
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

func addChecked(a, b int64) (int64, error) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, fmt.Errorf("overflow")
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, fmt.Errorf("overflow")
	}
	return a + b, nil
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
