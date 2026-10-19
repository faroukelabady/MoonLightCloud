package catalogadmin

import (
	"context"
	"encoding/json"
	"strings"
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
			wanted, valid := decodeVariantExpectation(entry)
			if !valid {
				return false
			}
			found := false
			for _, row := range rows {
				if matched[row.VariantID] {
					continue
				}
				if wanted.sameIdentity(row) && wanted.matches(row) {
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
	return (variantExpectation{identityID: payloadText(entry, "id"), identityKey: payloadText(entry, "combination_key")}).sameIdentity(row)
}

// Decode known wire fields once; absent fields remain unconstrained.
func variantStateMatches(row AdminProductVariant, entry map[string]any) bool {
	wanted, valid := decodeVariantExpectation(entry)
	return valid && wanted.matches(row)
}

func attributesMatch(projected []AdminProductVariantAttribute, desired []any) bool {
	wanted, valid := decodeAttributeExpectations(desired)
	return valid && attributeExpectationsMatch(projected, wanted)
}

func payloadText(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

// Compare exact decimal strings after the same normalization as Retail.
func minorTextEqual(stored string, requested any) bool {
	value, valid := minorExpectation(requested)
	return valid && stored == value
}
