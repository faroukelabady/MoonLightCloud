package catalogadmin

import (
	"strings"

	"github.com/google/uuid"
)

// These are projection expectations, not canonical command payloads. Absence
// leaves a field unconstrained; a present null USD explicitly clears its value.
// Durable payload bytes and hashes never use these local representations.
type variantExpectation struct {
	identityID, identityKey string
	sku, combination        *string
	active, deleted         *bool
	position                *float64
	egp                     *string
	usdPresent              bool
	usd                     *string
	attributesPresent       bool
	attributes              []attributeExpectation
	egpPresent              bool
}

type attributeExpectation struct {
	definitionCode, valueCode                          string
	nameAR, nameEN, definitionNameAR, definitionNameEN string
	position                                           *float64
}

func optionalExpectation[T any](entry map[string]any, key string) (*T, bool) {
	raw, present := entry[key]
	if !present {
		return nil, true
	}
	value, ok := raw.(T)
	return &value, ok
}

func decodeVariantExpectation(entry map[string]any) (variantExpectation, bool) {
	wanted := variantExpectation{identityID: payloadText(entry, "id"), identityKey: payloadText(entry, "combination_key")}
	var ok bool
	if wanted.sku, ok = optionalExpectation[string](entry, "sku"); !ok {
		return wanted, false
	}
	if wanted.sku != nil {
		*wanted.sku = strings.TrimSpace(*wanted.sku)
	}
	if wanted.combination, ok = optionalExpectation[string](entry, "combination_key"); !ok {
		return wanted, false
	}
	if wanted.combination != nil {
		*wanted.combination = strings.TrimSpace(*wanted.combination)
	}
	if wanted.active, ok = optionalExpectation[bool](entry, "is_active"); !ok {
		return wanted, false
	}
	if wanted.deleted, ok = optionalExpectation[bool](entry, "deleted"); !ok {
		return wanted, false
	}
	if wanted.position, ok = optionalExpectation[float64](entry, "position"); !ok {
		return wanted, false
	}
	if raw, present := entry["price_egp_cents"]; present {
		wanted.egpPresent = true
		if raw != nil {
			value, valid := minorExpectation(raw)
			if !valid {
				return wanted, false
			}
			wanted.egp = &value
		}
	}

	if raw, present := entry["price_usd_cents"]; present {
		wanted.usdPresent = true
		if raw != nil {
			value, valid := minorExpectation(raw)
			if !valid {
				return wanted, false
			}
			wanted.usd = &value
		}
	}
	if raw, present := entry["attributes"]; present {
		wanted.attributesPresent = true
		list, valid := raw.([]any)
		if !valid {
			return wanted, false
		}
		if wanted.attributes, valid = decodeAttributeExpectations(list); !valid {
			return wanted, false
		}
	}
	return wanted, true
}

func (wanted variantExpectation) sameIdentity(row AdminProductVariant) bool {
	if wanted.identityID != "" {
		parsed := wanted.identityID
		if canonical, err := uuid.Parse(parsed); err == nil {
			parsed = canonical.String()
		}
		return parsed == row.VariantID
	}
	return wanted.identityKey != "" && wanted.identityKey == row.CombinationKey
}

func (wanted variantExpectation) matches(row AdminProductVariant) bool {
	if wanted.sku != nil && *wanted.sku != row.SKU {
		return false
	}
	if wanted.combination != nil && *wanted.combination != row.CombinationKey {
		return false
	}
	if wanted.active != nil && *wanted.active != row.IsActive {
		return false
	}
	if wanted.deleted != nil && *wanted.deleted != row.Deleted {
		return false
	}
	if wanted.position != nil && *wanted.position != float64(row.Position) {
		return false
	}
	if wanted.egpPresent {
		if wanted.egp == nil {
			if row.PriceEGPMinor != nil {
				return false
			}
		} else if row.PriceEGPMinor == nil || *wanted.egp != *row.PriceEGPMinor {
			return false
		}
	}
	if wanted.usdPresent {
		if wanted.usd == nil {
			if row.PriceUSDMinor != nil {
				return false
			}
		} else if row.PriceUSDMinor == nil || *wanted.usd != *row.PriceUSDMinor {
			return false
		}
	}
	return !wanted.attributesPresent || attributeExpectationsMatch(row.Attributes, wanted.attributes)
}

func decodeAttributeExpectations(desired []any) ([]attributeExpectation, bool) {
	out := make([]attributeExpectation, 0, len(desired))
	for _, item := range desired {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		position, ok := optionalExpectation[float64](entry, "position")
		if !ok {
			return nil, false
		}
		out = append(out, attributeExpectation{
			definitionCode: payloadText(entry, "definition_code"), valueCode: payloadText(entry, "value_code"),
			nameAR: payloadText(entry, "name_ar"), nameEN: payloadText(entry, "name_en"),
			definitionNameAR: payloadText(entry, "definition_name_ar"), definitionNameEN: payloadText(entry, "definition_name_en"), position: position,
		})
	}
	return out, true
}

func attributeExpectationsMatch(projected []AdminProductVariantAttribute, desired []attributeExpectation) bool {
	if len(desired) != len(projected) {
		return false
	}
	byCode := make(map[string]AdminProductVariantAttribute, len(projected))
	for _, attr := range projected {
		byCode[attr.DefinitionCode] = attr
	}
	for _, wanted := range desired {
		row, ok := byCode[wanted.definitionCode]
		if !ok || wanted.valueCode != row.ValueCode || wanted.nameAR != row.NameAR || wanted.nameEN != row.NameEN || wanted.definitionNameAR != row.DefinitionNameAR || wanted.definitionNameEN != row.DefinitionNameEN {
			return false
		}
		if wanted.position != nil && *wanted.position != float64(row.Position) {
			return false
		}
	}
	return true
}

// Exact digit-string normalization mirrors the existing Retail parser. In
// particular this never decodes money as float64 or changes a durable payload.
func minorExpectation(requested any) (string, bool) {
	value, ok := requested.(string)
	if !ok {
		return "", false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return "", false
		}
	}
	value = strings.TrimLeft(value, "0")
	if value == "" {
		value = "0"
	}
	return value, true
}
