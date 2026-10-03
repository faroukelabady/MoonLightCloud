package shopify

import (
	"fmt"
	"strings"
	"testing"
)

// Focused 2026-10 contract oracle, independent of the production documents.
// Sources are pinned in ADR-0044. Unknown schema arguments/input fields fail.
func validateWireContract(document string, vars map[string]any) error {
	compact := strings.Join(strings.Fields(document), " ")
	fail := func() error { return fmt.Errorf("invalid Shopify 2026-10 contract") }
	switch operationName(document) {
	case "MoonlightMetafieldsSet":
		if !strings.Contains(compact, "$metafields: [MetafieldsSetInput!]!") || !strings.Contains(compact, "metafieldsSet(metafields: $metafields)") {
			return fail()
		}
		fields, ok := vars["metafields"].([]any)
		if !ok || len(fields) == 0 {
			return fail()
		}
		for _, raw := range fields {
			f, ok := raw.(map[string]any)
			if !ok || f["ownerId"] == nil || f["namespace"] == nil || f["key"] == nil || f["value"] == nil {
				return fail()
			}
		}
	case "MoonlightPublish", "MoonlightUnpublish":
		if !strings.Contains(compact, "$input: [PublicationInput!]!") || strings.Contains(compact, "publicationId: $publicationId") || strings.Contains(compact, "publishable { id }") || !strings.Contains(compact, "input: $input") || !strings.Contains(compact, "... on Product { id }") {
			return fail()
		}
		values, ok := vars["input"].([]any)
		if !ok || len(values) != 1 {
			return fail()
		}
		input, ok := values[0].(map[string]any)
		if !ok || len(input) != 1 || input["publicationId"] == nil {
			return fail()
		}
	case "MoonlightManagedVariantUpdate":
		if !strings.Contains(compact, "$variants: [ProductVariantsBulkInput!]!") {
			return fail()
		}
		values, ok := vars["variants"].([]any)
		if !ok || len(values) != 1 {
			return fail()
		}
		input, ok := values[0].(map[string]any)
		if !ok || input["sku"] != nil {
			return fail()
		}
		item, ok := input["inventoryItem"].(map[string]any)
		if !ok || item["sku"] == nil {
			return fail()
		}
		for key := range input {
			if key != "id" && key != "price" && key != "inventoryItem" {
				return fail()
			}
		}
	case "MoonlightProductCreate":
		if !strings.Contains(compact, "$input: ProductSetInput!") {
			return fail()
		}
		input, ok := vars["input"].(map[string]any)
		if !ok {
			return fail()
		}
		vs, ok := input["variants"].([]any)
		if !ok || len(vs) != 1 {
			return fail()
		}
		v, ok := vs[0].(map[string]any)
		if !ok || v["sku"] == nil {
			return fail()
		}
	case "MoonlightProductUpdate":
		if !strings.Contains(compact, "$input: ProductInput!") || !strings.Contains(compact, "productUpdate(input: $input)") {
			return fail()
		}
	case "MoonlightInventorySet":
		if !strings.Contains(compact, "$input: InventorySetQuantitiesInput!") || !strings.Contains(compact, "@idempotent(key: $idempotencyKey)") {
			return fail()
		}
		input, ok := vars["input"].(map[string]any)
		if !ok || input["name"] != "available" || input["quantities"] == nil {
			return fail()
		}
	case "MoonlightInventoryActivate":
		if !strings.Contains(compact, "available: 0") {
			return fail()
		}
	}
	return nil
}

func TestR1RejectOriginalContracts(t *testing.T) {
	for _, document := range []string{
		strings.Replace(docMetafieldsSet, "MetafieldsSetInput", "MetafieldInput", 1),
		strings.Replace(docPublish, "input: $input", "publicationId: $publicationId", 1),
		strings.Replace(docUnpublish, "... on Product { id }", "id", 1),
	} {
		if validateWireContract(document, map[string]any{}) == nil {
			t.Fatal("original invalid contract accepted")
		}
	}
	invalid := map[string]any{"variants": []any{map[string]any{"id": "gid://shopify/ProductVariant/1", "sku": "PAP-001", "price": "1.00"}}}
	if validateWireContract(docManagedVariantUpdate, invalid) == nil {
		t.Fatal("original top-level SKU input accepted")
	}
}
