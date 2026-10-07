package catalogadmin

import (
	"fmt"
	"testing"
)

// Phase 17-R0 (R13) command-payload parity: the EXACT variant command
// payload literals MoonLightRetail's dispatcher consumes
// (MoonLightRetail internal/service/admincommand/variant_commands_test.go)
// must be creatable here — the same mutation intent is accepted by Cloud's
// creation-time validation and dispatched by Retail's apply path. Any
// drift in field names, money-as-digit-strings, or revision keys breaks
// one side's suite.
func TestPhase17R0CrossRepoCommandPayloadParity(t *testing.T) {
	storeID := "77777777-0000-4000-8000-0000000000f1"
	variantID := "77777777-0000-4000-8000-000000000002"
	productID := "77777777-0000-4000-8000-000000000001"

	variantPayload := fmt.Sprintf(`{
		"variant_id": %q, "sku": "PAP-VAR-CMD-1",
		"is_active": true, "position": 1,
		"price_egp_cents": "123456", "price_usd_cents": null,
		"attributes": [{"definition_code": "color", "value_code": "blue",
			"name_ar": "أزرق", "name_en": "Blue",
			"definition_name_ar": "اللون", "definition_name_en": "Color", "position": 0}],
		"expected_variant_revision": 1
	}`, variantID)
	if _, err := ValidateNewCommand(TypeProductVariantUpdateV1, storeID,
		variantID, 1, []byte(variantPayload)); err != nil {
		t.Fatalf("variant update command must be creatable: %v", err)
	}

	setPayload := fmt.Sprintf(`{
		"product_id": %q,
		"variants": [
			{"sku": "PAP-SET-1", "is_active": true, "position": 0,
			 "price_egp_cents": "70000",
			 "attributes": [{"definition_code": "color", "value_code": "blue",
				"name_ar": "أزرق", "name_en": "Blue",
				"definition_name_ar": "اللون", "definition_name_en": "Color", "position": 0}]},
			{"sku": "PAP-SET-2", "is_active": true, "position": 1,
			 "price_egp_cents": "80000",
			 "attributes": [{"definition_code": "color", "value_code": "gold",
				"name_ar": "ذهبي", "name_en": "Gold",
				"definition_name_ar": "اللون", "definition_name_en": "Color", "position": 0}]}
		],
		"expected_catalog_revision": 1
	}`, productID)
	if _, err := ValidateNewCommand(TypeProductVariantsUpdateV1, storeID,
		productID, 1, []byte(setPayload)); err != nil {
		t.Fatalf("variant set command must be creatable: %v", err)
	}

	relabelPayload := fmt.Sprintf(`{
		"variant_id": %q,
		"attributes": [{"definition_code": "color", "value_code": "blue",
			"name_ar": "أزرق ملكي", "name_en": "Royal Blue",
			"definition_name_ar": "اللون", "definition_name_en": "Color", "position": 0}],
		"expected_variant_revision": 2
	}`, variantID)
	if _, err := ValidateNewCommand(TypeVariantAttributesUpdateV1, storeID,
		variantID, 2, []byte(relabelPayload)); err != nil {
		t.Fatalf("attributes relabel command must be creatable: %v", err)
	}

	// Money as JSON numbers is refused at creation (float64 can silently
	// corrupt large prices) — the digit-string rule both sides enforce.
	badMoney := `{
		"variant_id": "` + variantID + `", "sku": "PAP-VAR-CMD-1",
		"is_active": true,
		"price_egp_cents": 123456,
		"expected_variant_revision": 1
	}`
	if _, err := ValidateNewCommand(TypeProductVariantUpdateV1, storeID,
		variantID, 1, []byte(badMoney)); err == nil {
		t.Fatal("numeric money must be refused")
	}
}
