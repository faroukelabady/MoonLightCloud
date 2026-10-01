// Package sale v2 extension: sale.finalized.v2 adds immutable sale-time
// tag snapshots per line. v1 (sale.go) is frozen: its DTO, validator,
// projector flow, and tests are untouched. v2 reuses every v1 invariant
// by converting to the v1 payload and running Validate, then enforces
// tag rules on top. Unknown fields tolerated per compatibility policy,
// exactly like v1.
package sale

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog/slug"
	"github.com/google/uuid"
)

// EventSaleFinalizedV2 is the registered v2 event type.
const EventSaleFinalizedV2 = "sale.finalized.v2"

// Processor name for v2 durable processing state (separate from v1 so
// each version tracks claim/retry/blocked independently).
const ProcessorSaleProjectionV2 = "sale_projection.v2"

// SaleLineTag is one frozen sale-time tag snapshot on a line.
type SaleLineTag struct {
	TagID  string `json:"tag_id"`
	Slug   string `json:"slug"`
	NameAR string `json:"name_ar"`
	NameEN string `json:"name_en"`
}

// SaleLineV2 is one immutable historical sale line with tag snapshots.
type SaleLineV2 struct {
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
	Tags            []SaleLineTag       `json:"tags"`
}

// PayloadV2 is the sale.finalized.v2 payload object.
type PayloadV2 struct {
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
	Lines      []SaleLineV2   `json:"lines"`
	Payments   []EventPayment `json:"payments"`
}

// ValidatedV2 is the parsed, validated v2 form used by the projector.
type ValidatedV2 struct {
	PayloadV2
	Validated
}

// DecodeV2 parses the canonical v2 payload bytes strictly.
func DecodeV2(raw json.RawMessage) (PayloadV2, error) {
	var p PayloadV2
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&p); err != nil {
		return PayloadV2{}, apperr.New(apperr.Unprocessable, "sale payload is not a valid sale.finalized.v2 object")
	}
	if dec.More() {
		return PayloadV2{}, apperr.New(apperr.Unprocessable, "sale payload has trailing data")
	}
	return p, nil
}

// ValidateV2 enforces every v1 invariant (via conversion) plus tag rules.
// Tags are required as a non-null array per line (empty means captured empty); each entry
// needs a UUID tag_id, a bounded slug, and 1..200-rune display names.
func ValidateV2(p PayloadV2) (ValidatedV2, error) {
	fail := func(format string, args ...any) (ValidatedV2, error) {
		return ValidatedV2{}, apperr.New(apperr.Unprocessable, "invalid sale.finalized.v2: "+fmt.Sprintf(format, args...))
	}
	// Convert to v1 (tags dropped) to reuse the frozen validator verbatim.
	v1 := Payload{
		SaleID: p.SaleID, SaleNumber: p.SaleNumber, Channel: p.Channel,
		OccurredAt: p.OccurredAt, PaidAt: p.PaidAt, Shop: p.Shop, Actor: p.Actor,
		Currency: p.Currency, Fx: p.Fx, Totals: p.Totals, Payments: p.Payments,
	}
	for _, line := range p.Lines {
		v1.Lines = append(v1.Lines, SaleLine{
			SaleItemID: line.SaleItemID, ProductID: line.ProductID, VariantID: line.VariantID,
			SKU: line.SKU, ProductName: line.ProductName, WidthCM: line.WidthCM, HeightCM: line.HeightCM,
			Quantity: line.Quantity, UnitPrice: line.UnitPrice, Cost: line.Cost, LineTotal: line.LineTotal,
			Classifications: line.Classifications,
		})
	}
	valid, err := Validate(v1)
	if err != nil {
		// Re-label the version while preserving the underlying reason.
		return ValidatedV2{}, apperr.New(apperr.Unprocessable, "invalid sale.finalized.v2: "+err.Error())
	}
	for i := range p.Lines {
		if p.Lines[i].Tags == nil {
			return fail("lines[%d].tags must be an array", i)
		}
		seen := map[string]bool{}
		for j := range p.Lines[i].Tags {
			tag := &p.Lines[i].Tags[j]
			id, err := uuid.Parse(tag.TagID)
			if !isUUID(tag.TagID) || err != nil {
				return fail("lines[%d].tags[%d].tag_id must be a UUID", i, j)
			}
			canonicalID := id.String()
			if seen[canonicalID] {
				return fail("lines[%d] duplicates tag_id", i)
			}
			seen[canonicalID] = true
			if len(tag.Slug) > 64 || !slug.Valid(tag.Slug) {
				return fail("lines[%d].tags[%d].slug must match catalog policy", i, j)
			}
			if !validTagName(tag.NameAR) {
				return fail("lines[%d].tags[%d].name_ar must be 1..200 runes", i, j)
			}
			if !validTagName(tag.NameEN) {
				return fail("lines[%d].tags[%d].name_en must be 1..200 runes", i, j)
			}
		}
	}
	return ValidatedV2{PayloadV2: p, Validated: valid}, nil
}

func validTagName(name string) bool {
	trimmed := strings.TrimSpace(name)
	return trimmed != "" && len([]rune(trimmed)) <= 200
}
