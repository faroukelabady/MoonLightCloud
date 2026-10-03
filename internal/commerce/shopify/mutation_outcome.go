package shopify

import "encoding/json"

// Completion evidence must belong to the requested mutation. A bare HTTP200,
// empty result or malformed userErrors array must never erase send evidence.
func mutationOutcomeComplete(document string, data json.RawMessage) bool {
	root, primary := "", ""
	switch document {
	case docProductCreate:
		root, primary = "productSet", "product"
	case docProductUpdate:
		root, primary = "productUpdate", "product"
	case docManagedVariantUpdate:
		root, primary = "productVariantsBulkUpdate", "productVariants"
	case docMetafieldsSet:
		root, primary = "metafieldsSet", "metafields"
	case docPublish:
		root, primary = "publishablePublish", "publishable"
	case docUnpublish:
		root, primary = "publishableUnpublish", "publishable"
	case docInventoryActivate:
		root, primary = "inventoryActivate", "inventoryLevel"
	case docInventorySet:
		root, primary = "inventorySetQuantities", "inventoryAdjustmentGroup"
	default:
		return false
	}
	var envelope map[string]map[string]json.RawMessage
	if json.Unmarshal(data, &envelope) != nil || envelope[root] == nil {
		return false
	}
	payload := envelope[root]
	raw, ok := payload["userErrors"]
	if !ok || string(raw) == "null" {
		return false
	}
	var errors []json.RawMessage
	if json.Unmarshal(raw, &errors) != nil {
		return false
	}
	if len(errors) > 0 {
		for _, raw := range errors {
			var e struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(raw, &e) != nil || e.Message == "" {
				return false
			}
		}
		return true
	}
	raw, ok = payload[primary]
	if !ok || string(raw) == "null" {
		return false
	}
	if primary == "productVariants" || primary == "metafields" {
		var rows []json.RawMessage
		return json.Unmarshal(raw, &rows) == nil
	}
	var identity struct {
		ID string `json:"id"`
	}
	return json.Unmarshal(raw, &identity) == nil && identity.ID != ""
}
