package catalogadmin

import (
	"encoding/json"
	"testing"
)

func TestConvergenceExpectationFieldSemantics(t *testing.T) {
	usd := "0"
	row := AdminProductVariant{VariantID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", SKU: "Raw SKU", CombinationKey: "color=blue", IsActive: true, Position: 2, PriceEGPMinor: new("9007199254740993"), PriceUSDMinor: &usd}
	for _, tc := range []struct {
		name, payload string
		want          bool
	}{
		{"omitted", "{}", true}, {"trim SKU", `{"sku":" Raw SKU "}`, true}, {"invalid SKU", `{"sku":null}`, false},
		{"combination", `{"combination_key":" color=blue "}`, true}, {"invalid combination", `{"combination_key":7}`, false},
		{"active", `{"is_active":true}`, true}, {"inactive", `{"is_active":false}`, false}, {"null active", `{"is_active":null}`, false},
		{"deleted false", `{"deleted":false}`, true}, {"wrong deleted", `{"deleted":"false"}`, false},
		{"position", `{"position":2}`, true}, {"string position", `{"position":"2"}`, false}, {"fraction", `{"position":2.5}`, false},
		{"large exact money", `{"price_egp_cents":" 009007199254740993 "}`, true},
		{"numeric money", `{"price_egp_cents":9007199254740993}`, false}, {"null EGP", `{"price_egp_cents":null}`, false},
		{"USD zero", `{"price_usd_cents":"00"}`, true}, {"clear present USD", `{"price_usd_cents":null}`, false},
		{"empty attributes", `{"attributes":[]}`, true}, {"null attributes", `{"attributes":null}`, false}, {"invalid attribute", `{"attributes":[null]}`, false},
		{"unknown fields ignored", `{"future":{"money":false}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var payload map[string]any
			if err := json.Unmarshal([]byte(tc.payload), &payload); err != nil {
				t.Fatal(err)
			}
			if got := variantStateMatches(row, payload); got != tc.want {
				t.Fatalf("match=%v want %v", got, tc.want)
			}
		})
	}
	row.PriceUSDMinor = nil
	if !variantStateMatches(row, map[string]any{"price_usd_cents": nil}) {
		t.Fatal("null clears USD")
	}
}

func TestConvergenceAttributeLabelCompatibility(t *testing.T) {
	row := AdminProductVariantAttribute{DefinitionCode: "color", ValueCode: "blue", NameAR: "أزرق", NameEN: "", DefinitionNameAR: "", DefinitionNameEN: "", Position: 2}
	for _, tc := range []struct {
		name, payload string
		want          bool
	}{
		{"wrong optional text remains blank", `[{"definition_code":" color ","value_code":"blue","name_ar":" أزرق ","name_en":7}]`, true},
		{"name required by equality", `[{"definition_code":"color","value_code":"blue"}]`, false},
		{"optional position", `[{"definition_code":"color","value_code":"blue","name_ar":"أزرق","position":2}]`, true},
		{"wrong position type", `[{"definition_code":"color","value_code":"blue","name_ar":"أزرق","position":"2"}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var payload []any
			if err := json.Unmarshal([]byte(tc.payload), &payload); err != nil {
				t.Fatal(err)
			}
			if got := attributesMatch([]AdminProductVariantAttribute{row}, payload); got != tc.want {
				t.Fatalf("match=%v want %v", got, tc.want)
			}
		})
	}
}
func TestConvergenceIdentityPriority(t *testing.T) {
	row := AdminProductVariant{VariantID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", CombinationKey: "color=blue"}
	for _, tc := range []struct {
		payload map[string]any
		want    bool
	}{
		{map[string]any{"id": "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"}, true},
		{map[string]any{"id": "other", "combination_key": "color=blue"}, false},
		{map[string]any{"id": nil, "combination_key": " color=blue "}, true},
		{map[string]any{"sku": "any"}, false},
	} {
		if got := sameVariantIdentity(row, tc.payload); got != tc.want {
			t.Fatalf("identity %v =%v want %v", tc.payload, got, tc.want)
		}
	}
}

func TestEGPExpectationDistinguishesInheritanceFromZero(t *testing.T) {
	inherited, ok := decodeVariantExpectation(map[string]any{"price_egp_cents": nil})
	if !ok || !inherited.matches(AdminProductVariant{}) {
		t.Fatal("null EGP should match inheritance")
	}
	if inherited.matches(AdminProductVariant{PriceEGPMinor: new("0")}) {
		t.Fatal("zero is not inheritance")
	}
	zero, ok := decodeVariantExpectation(map[string]any{"price_egp_cents": "0"})
	if !ok || !zero.matches(AdminProductVariant{PriceEGPMinor: new("0")}) || zero.matches(AdminProductVariant{}) {
		t.Fatal("zero override must stay explicit")
	}
}
