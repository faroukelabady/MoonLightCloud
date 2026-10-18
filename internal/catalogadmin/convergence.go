package catalogadmin

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
)

// Wire shape is shared by byte budgeting and the HTTP encoder.
const MaxCatalogPollResponseBytes = 128 * 1024

type CommandWire struct {
	TargetID    string `json:"target_id"`
	CommandID   string `json:"command_id"`
	CommandType string `json:"command_type"`
	Version     int    `json:"version"`
	StoreID     string `json:"store_id"`
	Payload     []byte `json:"payload"`
	PayloadHash string `json:"payload_hash"`
}

func PollWire(due []DueTarget) []CommandWire {
	out := make([]CommandWire, 0, len(due))
	for _, t := range due {
		out = append(out, CommandWire{t.TargetID, t.CommandID, t.Type, t.Version, t.StoreID, t.Payload, t.PayloadHash})
	}
	return out
}

func CatalogPollBytes(due []DueTarget) int {
	b, _ := json.Marshal(map[string]any{"commands": PollWire(due)})
	return len(b) + 1
}

// A no-op may retain its revision. Require the requested values in projection,
// rather than treating a reported unchanged revision as proof of a change.
func (s *Service) noopProjected(ctx context.Context, view CommandView) bool {
	if len(view.Payload) == 0 {
		stored, err := s.store.GetCatalogAdminCommand(ctx, view.ID)
		if err != nil {
			return false
		}
		view.Payload = stored.Payload
	}
	var payload map[string]any
	if json.Unmarshal(view.Payload, &payload) != nil {
		return false
	}
	switch view.Type {
	case TypeProductOnlinePolicyUpdateV1:
		p, err := s.store.AdminProductDetail(ctx, view.StoreID, view.EntityID)
		wanted, ok := payload["sell_online"].(bool)
		return err == nil && ok && p.SellOnline == wanted
	case TypeCategoryOnlinePolicyUpdateV1:
		rows, err := s.store.AdminCategoryList(ctx, view.StoreID)
		if err != nil {
			return false
		}
		wanted, ok := payload["online_enabled"].(bool)
		if !ok {
			return false
		}
		for _, row := range rows {
			if row.CategoryID == view.EntityID {
				return row.OnlineEnabled == wanted
			}
		}
	case TypeProductVariantUpdateV1:
		row, err := s.store.AdminProductVariant(ctx, view.StoreID, view.EntityID)
		return err == nil && variantStateMatches(row, payload)
	case TypeVariantAttributesUpdateV1:
		row, err := s.store.AdminProductVariant(ctx, view.StoreID, view.EntityID)
		if err != nil {
			return false
		}
		desired, ok := payload["attributes"].([]any)
		if !ok {
			return false
		}
		return attributesMatch(row.Attributes, desired)
	case TypeProductVariantsUpdateV1:
		rows, err := s.store.AdminProductVariants(ctx, view.StoreID, view.EntityID)
		if err != nil {
			return false
		}
		desired, ok := payload["variants"].([]any)
		if !ok {
			return false
		}
		matched := make(map[string]bool, len(rows))
		for _, item := range desired {
			entry, ok := item.(map[string]any)
			if !ok {
				return false
			}
			found := false
			for _, row := range rows {
				if matched[row.VariantID] {
					continue
				}
				if sameVariantIdentity(row, entry) && variantStateMatches(row, entry) {
					matched[row.VariantID] = true
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		// Replace-set semantics: every projected variant must already be
		// requested for a genuine no-op.
		for _, row := range rows {
			if !matched[row.VariantID] {
				return false
			}
		}
		return true
	}
	return false
}

// sameVariantIdentity reports whether a bulk variants entry targets a
// projected variant by explicit ID or by normalized combination key.
func sameVariantIdentity(row AdminProductVariant, entry map[string]any) bool {
	if id := payloadText(entry, "id"); id != "" {
		parsed := id
		if canonical, err := uuid.Parse(id); err == nil {
			parsed = canonical.String()
		}
		return parsed == row.VariantID
	}
	if key := payloadText(entry, "combination_key"); key != "" {
		return key == row.CombinationKey
	}
	return false
}

// variantStateMatches verifies every requested variant field against the
// projection (fields absent from the payload are unconstrained). Money
// compares as exact decimal strings, never floats.
func variantStateMatches(row AdminProductVariant, entry map[string]any) bool {
	if raw, present := entry["sku"]; present {
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) != row.SKU {
			return false
		}
	}
	if raw, present := entry["combination_key"]; present {
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) != row.CombinationKey {
			return false
		}
	}
	if raw, present := entry["is_active"]; present {
		value, ok := raw.(bool)
		if !ok || value != row.IsActive {
			return false
		}
	}
	if raw, present := entry["deleted"]; present {
		value, ok := raw.(bool)
		if !ok || value != row.Deleted {
			return false
		}
	}
	if raw, present := entry["position"]; present {
		value, ok := raw.(float64)
		if !ok || value != float64(row.Position) {
			return false
		}
	}
	if raw, present := entry["price_egp_cents"]; present {
		if raw == nil || !minorTextEqual(row.PriceEGPMinor, raw) {
			return false
		}
	}
	if raw, present := entry["price_usd_cents"]; present {
		if raw == nil {
			if row.PriceUSDMinor != nil {
				return false
			}
		} else if row.PriceUSDMinor == nil || !minorTextEqual(*row.PriceUSDMinor, raw) {
			return false
		}
	}
	return attributesOptionalMatch(row.Attributes, entry)
}

// attributesOptionalMatch verifies the requested attribute list when
// present (absent = unconstrained).
func attributesOptionalMatch(projected []AdminProductVariantAttribute, entry map[string]any) bool {
	raw, present := entry["attributes"]
	if !present {
		return true
	}
	desired, ok := raw.([]any)
	if !ok {
		return false
	}
	return attributesMatch(projected, desired)
}

// attributesMatch verifies a full requested attribute list against the
// projection: same definition codes with identical value codes, labels
// and positions.
func attributesMatch(projected []AdminProductVariantAttribute, desired []any) bool {
	if len(desired) != len(projected) {
		return false
	}
	byCode := make(map[string]AdminProductVariantAttribute, len(projected))
	for _, attr := range projected {
		byCode[attr.DefinitionCode] = attr
	}
	for _, item := range desired {
		entry, ok := item.(map[string]any)
		if !ok {
			return false
		}
		row, ok := byCode[payloadText(entry, "definition_code")]
		if !ok {
			return false
		}
		if payloadText(entry, "value_code") != row.ValueCode ||
			!optionalTextEqual(&row.NameAR, entry["name_ar"]) ||
			!optionalTextEqual(&row.NameEN, entry["name_en"]) ||
			!optionalTextEqual(&row.DefinitionNameAR, entry["definition_name_ar"]) ||
			!optionalTextEqual(&row.DefinitionNameEN, entry["definition_name_en"]) {
			return false
		}
		if raw, present := entry["position"]; present {
			value, ok := raw.(float64)
			if !ok || value != float64(row.Position) {
				return false
			}
		}
	}
	return true
}

func optionalTextEqual(stored *string, requested any) bool {
	value, _ := requested.(string)
	value = strings.TrimSpace(value)
	if stored == nil {
		return value == ""
	}
	return *stored == value
}

func payloadText(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

// Compare exact decimal strings after the same whitespace/leading-zero
// normalization as Retail's integer parser; never use floating-point money.
func minorTextEqual(stored string, requested any) bool {
	value, ok := requested.(string)
	if !ok {
		return false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	value = strings.TrimLeft(value, "0")
	if value == "" {
		value = "0"
	}
	return stored == value
}
