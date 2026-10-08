package sale

import (
	"encoding/json"
	"strings"
	"testing"
)

// v3 validation: every v1+v2 invariant inherited (via v2 conversion,
// tags included) plus the frozen per-line variant snapshot rules. v1/v2
// code and fixtures are untouched.

func loadV3Base(t *testing.T) map[string]any {
	t.Helper()
	raw, err := json.Marshal(loadV2Base(t))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, entry := range m["lines"].([]any) {
		line := entry.(map[string]any)
		line["variant_id"] = "bbbbbbbb-0000-4000-8000-000000000001"
		line["variant_sku"] = "NFT-BLU-TRD"
		line["variant_attributes"] = []any{
			map[string]any{
				"definition_code": "color", "value_code": "blue",
				"name_ar": "أزرق", "name_en": "Blue",
				"definition_name_ar": "اللون", "definition_name_en": "Color",
			},
		}
		price := int64(125000)
		line["variant_price_egp_cents"] = price
	}
	return m
}

func decodeV3Map(t *testing.T, m map[string]any) (PayloadV3, error) {
	t.Helper()
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return DecodeV3(raw)
}

func validateV3Map(t *testing.T, m map[string]any) (ValidatedV3, error) {
	t.Helper()
	p, err := decodeV3Map(t, m)
	if err != nil {
		return ValidatedV3{}, err
	}
	return ValidateV3(p)
}

func TestV3ProductTypeCodeBounds(t *testing.T) {
	for _, length := range []int{0, 1, 32, 33, 64} {
		code := strings.Repeat("a", length)
		t.Run("code_"+code, func(t *testing.T) {
			payload := loadV3Base(t)
			for _, entry := range payload["lines"].([]any) {
				line := entry.(map[string]any)
				line["product_type_id"] = "10000000-0000-4000-8000-000000000001"
				line["product_type_code"] = code
				line["product_type_name_ar"] = "نوع"
				line["product_type_name_en"] = "Type"
			}
			_, err := validateV3Map(t, payload)
			if length >= 1 && length <= 32 {
				if err != nil {
					t.Fatalf("supported Type code rejected: %v", err)
				}
			} else if err == nil {
				t.Fatal("out-of-bound Type code accepted")
			}
		})
	}
	t.Run("generic_variant_code_retains_64", func(t *testing.T) {
		payload := loadV3Base(t)
		line := payload["lines"].([]any)[0].(map[string]any)
		line["variant_attributes"].([]any)[0].(map[string]any)["definition_code"] = strings.Repeat("a", 64)
		if _, err := validateV3Map(t, payload); err != nil {
			t.Fatalf("Type bound changed generic Variant code compatibility: %v", err)
		}
	})
}

func TestV3ProductTypeSnapshotIndependentOfVariant(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]any)
		valid  bool
	}{
		{"complete", func(map[string]any) {}, true},
		{"code_1", func(line map[string]any) { line["product_type_code"] = "a" }, true},
		{"code_32", func(line map[string]any) { line["product_type_code"] = strings.Repeat("a", 32) }, true},
		{"absent", func(line map[string]any) {
			for _, field := range []string{"product_type_id", "product_type_code", "product_type_name_ar", "product_type_name_en"} {
				delete(line, field)
			}
		}, true},
		{"code_empty", func(line map[string]any) { line["product_type_code"] = "" }, false},
		{"code_33", func(line map[string]any) { line["product_type_code"] = strings.Repeat("a", 33) }, false},
		{"code_64", func(line map[string]any) { line["product_type_code"] = strings.Repeat("a", 64) }, false},
		{"code_control", func(line map[string]any) { line["product_type_code"] = "book\n" }, false},
		{"partial_id", func(line map[string]any) { delete(line, "product_type_id") }, false},
		{"partial_code", func(line map[string]any) { delete(line, "product_type_code") }, false},
		{"partial_ar", func(line map[string]any) { delete(line, "product_type_name_ar") }, false},
		{"partial_en", func(line map[string]any) { delete(line, "product_type_name_en") }, false},
		{"invalid_uuid", func(line map[string]any) { line["product_type_id"] = "not-a-uuid" }, false},
		{"blank_ar", func(line map[string]any) { line["product_type_name_ar"] = " " }, false},
		{"blank_en", func(line map[string]any) { line["product_type_name_en"] = "" }, false},
		{"oversized_ar", func(line map[string]any) { line["product_type_name_ar"] = strings.Repeat("ع", 201) }, false},
		{"oversized_en", func(line map[string]any) { line["product_type_name_en"] = strings.Repeat("a", 201) }, false},
	}
	for _, variant := range []bool{false, true} {
		group := "without_variant"
		if variant {
			group = "with_variant"
		}
		t.Run(group, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					payload := loadV3Base(t)
					line := payload["lines"].([]any)[0].(map[string]any)
					if !variant {
						for _, field := range []string{"variant_id", "variant_sku", "variant_attributes", "variant_price_egp_cents", "variant_price_usd_cents"} {
							delete(line, field)
						}
					}
					line["product_type_id"] = "10000000-0000-4000-8000-000000000001"
					line["product_type_code"] = "book"
					line["product_type_name_ar"] = "كتاب"
					line["product_type_name_en"] = "Book"
					tc.mutate(line)
					_, err := validateV3Map(t, payload)
					if tc.valid && err != nil {
						t.Fatalf("valid optional ProductType snapshot rejected: %v", err)
					}
					if !tc.valid && err == nil {
						t.Fatal("invalid ProductType snapshot accepted")
					}
				})
			}
		})
	}
}

func TestDecodeV3Valid(t *testing.T) {
	valid, err := validateV3Map(t, loadV3Base(t))
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(valid.Lines) != 1 {
		t.Fatal("one line")
	}
	line := valid.Lines[0]
	if line.VariantSKU != "NFT-BLU-TRD" || line.VariantID == nil {
		t.Fatal("variant snapshot verbatim")
	}
	if len(line.VariantAttributes) != 1 ||
		line.VariantAttributes[0].DefinitionCode != "color" ||
		line.VariantAttributes[0].NameAR != "أزرق" ||
		line.VariantAttributes[0].DefinitionNameEN == nil ||
		*line.VariantAttributes[0].DefinitionNameEN != "Color" {
		t.Fatalf("attributes verbatim: %+v", line.VariantAttributes)
	}
	if line.VariantPriceEGPCents == nil || *line.VariantPriceEGPCents != 125000 {
		t.Fatalf("price override: %+v", line.VariantPriceEGPCents)
	}
	if line.VariantPriceUSDCents != nil {
		t.Fatal("absent usd override stays nil")
	}
	// v2 tag snapshots still ride along and validate.
	if len(line.Tags) != 1 || line.Tags[0].Slug != "horse" {
		t.Fatalf("tags preserved: %+v", line.Tags)
	}
}

// The variant snapshot is all-or-nothing per line: partial snapshots are
// rejected before ACK so stored history is always coherent.
func TestV3VariantSnapshotAllOrNothing(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(line map[string]any)
	}{
		{"variant_id without sku/attrs", func(line map[string]any) {
			delete(line, "variant_sku")
			delete(line, "variant_attributes")
		}},
		{"variant_id without attrs", func(line map[string]any) { delete(line, "variant_attributes") }},
		{"variant_id without sku", func(line map[string]any) { delete(line, "variant_sku") }},
		{"sku without variant_id", func(line map[string]any) { delete(line, "variant_id") }},
		{"attrs without variant_id", func(line map[string]any) {
			delete(line, "variant_id")
			delete(line, "variant_sku")
		}},
		{"price override without variant_id", func(line map[string]any) {
			delete(line, "variant_id")
			delete(line, "variant_sku")
			delete(line, "variant_attributes")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := loadV3Base(t)
			tc.mutate(m["lines"].([]any)[0].(map[string]any))
			if _, err := validateV3Map(t, m); err == nil {
				t.Fatal("partial variant snapshot must be rejected")
			}
		})
	}
	// Lines WITHOUT variant data are truthful NULL snapshots (valid).
	t.Run("no snapshot at all", func(t *testing.T) {
		m := loadV3Base(t)
		line := m["lines"].([]any)[0].(map[string]any)
		delete(line, "variant_id")
		delete(line, "variant_sku")
		delete(line, "variant_attributes")
		delete(line, "variant_price_egp_cents")
		if _, err := validateV3Map(t, m); err != nil {
			t.Fatalf("snapshot-less line must validate: %v", err)
		}
	})
	// An empty attribute array is a captured-empty snapshot (valid).
	t.Run("captured empty attributes", func(t *testing.T) {
		m := loadV3Base(t)
		m["lines"].([]any)[0].(map[string]any)["variant_attributes"] = []any{}
		if _, err := validateV3Map(t, m); err != nil {
			t.Fatalf("empty attributes must validate: %v", err)
		}
	})
}

func TestV3VariantSnapshotBounds(t *testing.T) {
	long := ""
	for i := 0; i < 65; i++ {
		long += "x"
	}
	many := []any{}
	for i := 0; i < 33; i++ {
		many = append(many, map[string]any{
			"definition_code": "d" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			"value_code":      "v", "name_ar": "أ", "definition_name_ar": "د",
		})
	}
	cases := []struct {
		name   string
		mutate func(line map[string]any)
	}{
		{"sku empty", func(line map[string]any) { line["variant_sku"] = "" }},
		{"sku too long", func(line map[string]any) { line["variant_sku"] = long }},
		{"sku control whitespace", func(line map[string]any) { line["variant_sku"] = "A\nB" }},
		{"too many attributes", func(line map[string]any) { line["variant_attributes"] = many }},
		{"duplicate definition_code", func(line map[string]any) {
			line["variant_attributes"] = []any{
				map[string]any{"definition_code": "color", "value_code": "blue", "name_ar": "أ", "definition_name_ar": "د"},
				map[string]any{"definition_code": "color", "value_code": "red", "name_ar": "أ", "definition_name_ar": "د"},
			}
		}},
		{"missing name_ar", func(line map[string]any) {
			line["variant_attributes"] = []any{
				map[string]any{"definition_code": "color", "value_code": "blue", "definition_name_ar": "د"},
			}
		}},
		{"blank definition_name_ar", func(line map[string]any) {
			line["variant_attributes"] = []any{
				map[string]any{"definition_code": "color", "value_code": "blue", "name_ar": "أ", "definition_name_ar": "  "},
			}
		}},
		{"negative egp override", func(line map[string]any) { line["variant_price_egp_cents"] = -1 }},
		{"negative usd override", func(line map[string]any) { line["variant_price_usd_cents"] = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := loadV3Base(t)
			tc.mutate(m["lines"].([]any)[0].(map[string]any))
			if _, err := validateV3Map(t, m); err == nil {
				t.Fatal("out-of-bounds variant snapshot must be rejected")
			}
		})
	}
}

// v3 inherits every frozen v1/v2 invariant: broken tag payloads and
// broken sale arithmetic reject exactly like v2.
func TestV3InheritsV1V2Invariants(t *testing.T) {
	badTag := loadV3Base(t)
	badTag["lines"].([]any)[0].(map[string]any)["tags"] = nil
	if _, err := validateV3Map(t, badTag); err == nil {
		t.Fatal("v3 must enforce the v2 tag array rule")
	}
	badTotal := loadV3Base(t)
	badTotal["totals"].(map[string]any)["total"].(map[string]any)["amount_minor"] = 1
	if _, err := validateV3Map(t, badTotal); err == nil {
		t.Fatal("v3 must enforce the v1 totals arithmetic")
	}
	// Error messages are re-labeled as v3.
	_, err := validateV3Map(t, badTotal)
	if err == nil || !strings.Contains(err.Error(), "sale.finalized.v3") {
		t.Fatalf("v3 error label: %v", err)
	}
}
