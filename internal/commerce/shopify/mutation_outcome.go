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
	// Phase 15 §101: bundle-surface mutations settle under the SAME
	// frozen definitive-evidence standard (F2): an acknowledged response
	// with the primary resource present and no user errors is the only
	// release evidence.
	case docFrameComponentSet:
		root, primary = "productSet", "product"
	case docBundleUpdate:
		root, primary = "productBundleUpdate", "productBundleOperation"
	case docBundleCreate:
		// Phase 15-R2 F06: the bundle-create mutation settles under the
		// same definitive-evidence standard as every other mutation.
		root, primary = "productBundleCreate", "productBundleOperation"
	case docVariantPrices:
		root, primary = "productVariantsBulkUpdate", "productVariants"
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
