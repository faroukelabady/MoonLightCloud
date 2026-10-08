// Package sale v3 extension (Phase 17-R0): sale.finalized.v3 carries, per
// item, the immutable ProductVariant snapshot frozen AT SALE TIME — the
// variant SKU, its option attributes (codes + bilingual labels as sold),
// and the nullable sale-time variant pricing override. v1 (sale.go) and
// v2 (sale_v2.go) stay frozen and fully supported: v3 reuses every v1/v2
// invariant by converting to the v2 payload and running ValidateV2, then
// enforces the variant-snapshot rules on top.
//
// History law (ADR-0049): these snapshots are sourced ONLY from the
// event. No projection or read path may join current catalog state to
// populate or refresh them — a later catalog attribute-label rename can
// never rewrite what was sold.
package sale

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// EventSaleFinalizedV3 is the registered v3 event type.
const EventSaleFinalizedV3 = "sale.finalized.v3"

// Processor name for v3 durable processing state (separate from v1/v2 so
// each version tracks claim/retry/blocked independently).
const ProcessorSaleProjectionV3 = "sale_projection.v3"

// Sale variant snapshot bounds (mirror the catalog variant domain).
const (
	MaxVariantSnapshotAttributes    = 32
	maxVariantSnapshotCodeRunes     = 64
	maxVariantSnapshotLabelRunes    = 200
	maxProductTypeSnapshotCodeRunes = 32
)

// SaleLineVariantAttribute is one frozen sale-time option attribute of
// the purchased variant: identity codes plus the bilingual display labels
// EXACTLY as they read at sale time. Labels are display history only;
// identity is (definition_code, value_code).
type SaleLineVariantAttribute struct {
	DefinitionCode   string  `json:"definition_code"`
	ValueCode        string  `json:"value_code"`
	NameAR           string  `json:"name_ar"`
	NameEN           *string `json:"name_en,omitempty"`
	DefinitionNameAR string  `json:"definition_name_ar"`
	DefinitionNameEN *string `json:"definition_name_en,omitempty"`
}

// SaleLineV3 is one immutable historical sale line: every v2 field (tag
// snapshots included) plus the frozen variant snapshot. The snapshot is
// all-or-nothing: a line either carries variant_id + variant_sku +
// variant_attributes (and optionally the sale-time price override) or
// none of them — partial snapshots are rejected before ACK so stored
// history is always coherent.
type SaleLineV3 struct {
	SaleLineV2
	VariantSKU           string                     `json:"variant_sku,omitempty"`
	VariantAttributes    []SaleLineVariantAttribute `json:"variant_attributes,omitempty"`
	VariantPriceEGPCents *int64                     `json:"variant_price_egp_cents,omitempty"`
	VariantPriceUSDCents *int64                     `json:"variant_price_usd_cents,omitempty"`
	// Phase 17-R2: frozen sale-time ProductType snapshot (ADR-0050 §26).
	// All-or-nothing with each other (independent of the variant
	// snapshot): a line either carries the full type identity or none.
	ProductTypeID     *string `json:"product_type_id,omitempty"`
	ProductTypeCode   *string `json:"product_type_code,omitempty"`
	ProductTypeNameAR *string `json:"product_type_name_ar,omitempty"`
	ProductTypeNameEN *string `json:"product_type_name_en,omitempty"`
}

// PayloadV3 is the sale.finalized.v3 payload object: identical to v2 at
// the top level (lines are SaleLineV3).
type PayloadV3 struct {
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
	Lines      []SaleLineV3   `json:"lines"`
	Payments   []EventPayment `json:"payments"`
}

// ValidatedV3 is the parsed, validated v3 form used by the projector.
type ValidatedV3 struct {
	PayloadV3
	Validated
}

// DecodeV3 parses the canonical v3 payload bytes strictly. Unknown fields
// are tolerated per compatibility policy (duplicate keys were already
// rejected at the envelope layer); float money fails on int64 decode.
func DecodeV3(raw json.RawMessage) (PayloadV3, error) {
	var p PayloadV3
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&p); err != nil {
		return PayloadV3{}, apperr.New(apperr.Unprocessable, "sale payload is not a valid sale.finalized.v3 object")
	}
	if dec.More() {
		return PayloadV3{}, apperr.New(apperr.Unprocessable, "sale payload has trailing data")
	}
	return p, nil
}

// ValidateV3 enforces every v2 invariant (via conversion, tags included)
// plus the frozen variant-snapshot rules:
//
//   - the variant snapshot is all-or-nothing per line: variant_sku and
//     variant_attributes appear together with variant_id (and only then);
//   - variant_sku is 1..64 chars without control whitespace;
//   - variant_attributes is a bounded array (≤ 32) of unique
//     definition_code entries with bounded codes and bilingual labels;
//   - price overrides are non-negative int64 minor units present only
//     with a variant identity.
func ValidateV3(p PayloadV3) (ValidatedV3, error) {
	fail := func(format string, args ...any) (ValidatedV3, error) {
		return ValidatedV3{}, apperr.New(apperr.Unprocessable, "invalid sale.finalized.v3: "+fmt.Sprintf(format, args...))
	}
	// Convert to v2 (variant snapshots dropped) to reuse the frozen
	// v1+v2 validators verbatim.
	v2 := PayloadV2{
		SaleID: p.SaleID, SaleNumber: p.SaleNumber, Channel: p.Channel,
		OccurredAt: p.OccurredAt, PaidAt: p.PaidAt, Shop: p.Shop, Actor: p.Actor,
		Currency: p.Currency, Fx: p.Fx, Totals: p.Totals, Payments: p.Payments,
	}
	for _, line := range p.Lines {
		v2.Lines = append(v2.Lines, line.SaleLineV2)
	}
	valid, err := ValidateV2(v2)
	if err != nil {
		// Re-label the version while preserving the underlying reason.
		return ValidatedV3{}, apperr.New(apperr.Unprocessable, "invalid sale.finalized.v3: "+err.Error())
	}
	for i := range p.Lines {
		line := &p.Lines[i]
		hasType := line.ProductTypeID != nil || line.ProductTypeCode != nil ||
			line.ProductTypeNameAR != nil || line.ProductTypeNameEN != nil
		if hasType {
			if line.ProductTypeID == nil || line.ProductTypeCode == nil ||
				line.ProductTypeNameAR == nil || line.ProductTypeNameEN == nil {
				return fail("lines[%d] product type snapshot must carry id, code and both labels", i)
			}
			if !isUUID(*line.ProductTypeID) {
				return fail("lines[%d].product_type_id must be a UUID", i)
			}
			if !validSnapshotCode(*line.ProductTypeCode) || utf8.RuneCountInString(*line.ProductTypeCode) > maxProductTypeSnapshotCodeRunes {
				return fail("lines[%d].product_type_code must be 1..32 chars without control whitespace", i)
			}
			if !validSnapshotLabel(*line.ProductTypeNameAR) {
				return fail("lines[%d].product_type_name_ar must be 1..200 runes", i)
			}
			if !validSnapshotLabel(*line.ProductTypeNameEN) {
				return fail("lines[%d].product_type_name_en must be 1..200 runes", i)
			}
		}
		hasSKU := line.VariantSKU != ""
		hasAttrs := line.VariantAttributes != nil
		if line.VariantID == nil {
			if hasSKU || hasAttrs {
				return fail("lines[%d] variant snapshot requires variant_id", i)
			}
			if line.VariantPriceEGPCents != nil || line.VariantPriceUSDCents != nil {
				return fail("lines[%d] variant price override requires variant_id", i)
			}
			continue
		}
		if !hasSKU || !hasAttrs {
			return fail("lines[%d] variant_id requires variant_sku and variant_attributes", i)
		}
		if !validSnapshotCode(line.VariantSKU) {
			return fail("lines[%d].variant_sku must be 1..64 chars without control whitespace", i)
		}
		if len(line.VariantAttributes) > MaxVariantSnapshotAttributes {
			return fail("lines[%d].variant_attributes must carry at most %d entries", i, MaxVariantSnapshotAttributes)
		}
		seen := map[string]bool{}
		for j := range line.VariantAttributes {
			attr := &line.VariantAttributes[j]
			if !validSnapshotCode(attr.DefinitionCode) {
				return fail("lines[%d].variant_attributes[%d].definition_code must be 1..64 chars without control whitespace", i, j)
			}
			if seen[attr.DefinitionCode] {
				return fail("lines[%d] duplicates variant attribute definition_code", i)
			}
			seen[attr.DefinitionCode] = true
			if !validSnapshotCode(attr.ValueCode) {
				return fail("lines[%d].variant_attributes[%d].value_code must be 1..64 chars without control whitespace", i, j)
			}
			if !validSnapshotLabel(attr.NameAR) {
				return fail("lines[%d].variant_attributes[%d].name_ar must be 1..200 runes", i, j)
			}
			if !validSnapshotLabel(attr.DefinitionNameAR) {
				return fail("lines[%d].variant_attributes[%d].definition_name_ar must be 1..200 runes", i, j)
			}
			if !validOptionalSnapshotLabel(attr.NameEN) {
				return fail("lines[%d].variant_attributes[%d].name_en must be at most 200 runes", i, j)
			}
			if !validOptionalSnapshotLabel(attr.DefinitionNameEN) {
				return fail("lines[%d].variant_attributes[%d].definition_name_en must be at most 200 runes", i, j)
			}
		}
		if line.VariantPriceEGPCents != nil && *line.VariantPriceEGPCents < 0 {
			return fail("lines[%d].variant_price_egp_cents must not be negative", i)
		}
		if line.VariantPriceUSDCents != nil && *line.VariantPriceUSDCents < 0 {
			return fail("lines[%d].variant_price_usd_cents must not be negative", i)
		}
	}
	return ValidatedV3{PayloadV3: p, Validated: valid.Validated}, nil
}

// containsControlWhitespace reports the rejected control whitespace set.
func containsControlWhitespace(s string) bool {
	for _, r := range s {
		if r == '\r' || r == '\n' || r == '\t' {
			return true
		}
	}
	return false
}

// validSnapshotCode bounds snapshot identity codes (definition_code /
// value_code / variant_sku): 1..64 bytes without control whitespace
// (mirror of the catalog variant domain bounds).
func validSnapshotCode(code string) bool {
	if code == "" || len(code) > maxVariantSnapshotCodeRunes {
		return false
	}
	return !containsControlWhitespace(code)
}

// validSnapshotLabel bounds required snapshot display labels (1..200
// runes, non-blank).
func validSnapshotLabel(name string) bool {
	trimmed := strings.TrimSpace(name)
	return trimmed != "" && utf8.RuneCountInString(trimmed) <= maxVariantSnapshotLabelRunes
}

// validOptionalSnapshotLabel bounds optional snapshot display labels.
func validOptionalSnapshotLabel(name *string) bool {
	if name == nil {
		return true
	}
	return utf8.RuneCountInString(*name) <= maxVariantSnapshotLabelRunes
}
