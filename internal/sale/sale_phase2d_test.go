package sale

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// HIGH-02: payment aggregate mirrors Retail buildPayments (sum(amount) ±1,
// change ignored, checked arithmetic). All cases reject before ACK (422).
func TestPaymentAggregateParity(t *testing.T) {
	setPayments := func(m map[string]any, amounts []int64, changes []int64) {
		pays := make([]any, len(amounts))
		for i := range amounts {
			ch := int64(0)
			if i < len(changes) {
				ch = changes[i]
			}
			pays[i] = map[string]any{
				"method":          "cash",
				"amount":          map[string]any{"amount_minor": amounts[i], "currency": "USD"},
				"change_given":    map[string]any{"amount_minor": ch, "currency": "USD"},
				"transaction_ref": nil,
			}
		}
		m["payments"] = pays
	}
	// Fixture total is 1250. Base valid split: 1000+250.
	valid := []struct {
		name    string
		amounts []int64
		changes []int64
	}{
		{"single exact", []int64{1250}, nil},
		{"split exact", []int64{1000, 250}, nil},
		{"plus one tolerance", []int64{1251}, nil},
		{"minus one tolerance", []int64{1249}, nil},
		{"split plus one", []int64{1000, 251}, nil},
		{"change ignored", []int64{1250}, []int64{5000}},
		{"change with split", []int64{1000, 250}, []int64{100, 200}},
	}
	for _, tc := range valid {
		raw := mutate(t, loadFixture(t, "sale_usd.json"), func(m map[string]any) {
			setPayments(m, tc.amounts, tc.changes)
		})
		p, err := Decode(raw)
		if err != nil {
			t.Fatalf("%s: decode: %v", tc.name, err)
		}
		if _, err := Validate(p); err != nil {
			t.Fatalf("%s: must validate: %v", tc.name, err)
		}
	}
	invalid := []struct {
		name    string
		amounts []int64
		changes []int64
	}{
		{"under by two", []int64{1248}, nil},
		{"over by two", []int64{1252}, nil},
		{"underpayment", []int64{1000}, nil},
		{"overpayment", []int64{2000}, nil},
		{"split under", []int64{600, 600}, nil},
		{"split over", []int64{700, 600}, nil},
	}
	for _, tc := range invalid {
		raw := mutate(t, loadFixture(t, "sale_usd.json"), func(m map[string]any) {
			setPayments(m, tc.amounts, tc.changes)
		})
		p, err := Decode(raw)
		if err != nil {
			continue
		}
		if _, err := Validate(p); err == nil {
			t.Fatalf("%s: must fail validation", tc.name)
		} else if kindOf(err) != apperr.Unprocessable {
			t.Fatalf("%s: want 422, got %v", tc.name, err)
		}
	}
	// Sum overflow rejects.
	raw := mutate(t, loadFixture(t, "sale_usd.json"), func(m map[string]any) {
		setPayments(m, []int64{math.MaxInt64, 1}, nil)
	})
	if p, err := Decode(raw); err == nil {
		if _, err := Validate(p); err == nil {
			t.Fatal("payment sum overflow must fail")
		}
	}
	// Strict DTO: fractional money never decodes.
	for _, bad := range []string{
		`{"amount_minor": 1.5, "currency": "USD"}`,
		`{"amount_minor": 1.0, "currency": "USD"}`,
		`{"amount_minor": "1250", "currency": "USD"}`,
	} {
		raw := mutate(t, loadFixture(t, "sale_usd.json"), func(m map[string]any) {
			var amt map[string]any
			if err := json.Unmarshal([]byte(bad), &amt); err != nil {
				t.Fatal(err)
			}
			m["payments"].([]any)[0].(map[string]any)["amount"] = amt
		})
		if p, err := Decode(raw); err == nil {
			if _, err := Validate(p); err == nil {
				t.Fatalf("%s: fractional/string money must fail", bad)
			}
		}
	}
}

// MED-01: exactly one root per line; duplicate IDs rejected.
func TestClassificationParity(t *testing.T) {
	root := func(id string) map[string]any {
		return map[string]any{"category_id": id, "name_ar": "جذر", "name_en": "Root"}
	}
	sub := func(id string) map[string]any {
		return map[string]any{"category_id": id, "name_ar": "فرعي", "name_en": "Sub"}
	}
	r1 := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	r2 := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	s1 := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	s2 := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	cases := []struct {
		name    string
		roots   []any
		subs    []any
		wantErr bool
	}{
		{"zero roots", []any{}, []any{}, true},
		{"one root", []any{root(r1)}, []any{}, false},
		{"two roots", []any{root(r1), root(r2)}, []any{}, true},
		{"duplicate root id", []any{root(r1), root(r1)}, []any{}, true},
		{"duplicate subcategory id", []any{root(r1)}, []any{sub(s1), sub(s1)}, true},
		{"root id reused as subcategory", []any{root(r1)}, []any{sub(r1)}, true},
		{"subcategory id reused as root", []any{root(s1)}, []any{sub(s1)}, true},
		{"valid multiple distinct subcategories", []any{root(r1)}, []any{sub(s1), sub(s2)}, false},
	}
	for _, tc := range cases {
		raw := mutate(t, loadFixture(t, "sale_usd.json"), func(m map[string]any) {
			line := m["lines"].([]any)[0].(map[string]any)["classifications"].(map[string]any)
			line["roots"] = tc.roots
			line["subcategories"] = tc.subs
		})
		p, err := Decode(raw)
		if err != nil {
			if !tc.wantErr {
				t.Fatalf("%s: decode: %v", tc.name, err)
			}
			continue
		}
		_, err = Validate(p)
		if tc.wantErr && err == nil {
			t.Fatalf("%s: must fail validation", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("%s: must validate: %v", tc.name, err)
		}
	}
}

// MED-04: FX pair validation.
func TestFxPairValidation(t *testing.T) {
	egpNoFx := decodeValidate(t, loadFixture(t, "sale_egp.json"))
	if egpNoFx.Currency != "EGP" || egpNoFx.Fx != nil {
		t.Fatal("EGP + no FX valid baseline broken")
	}
	cases := []struct {
		name    string
		base    string
		fixture string
		mut     func(m map[string]any)
		wantErr bool
	}{
		{"EGP no FX valid", "egp", "sale_egp.json", func(m map[string]any) {}, false},
		{"USD USD/EGP valid", "usd", "sale_usd.json", func(m map[string]any) {}, false},
		{"USD EUR/USD invalid", "usd", "sale_usd.json", func(m map[string]any) {
			m["fx"].(map[string]any)["base"] = "EUR"
		}, true},
		{"USD EGP/USD invalid", "usd", "sale_usd.json", func(m map[string]any) {
			m["fx"].(map[string]any)["base"] = "EGP"
			m["fx"].(map[string]any)["quote"] = "USD"
		}, true},
		{"USD blank pair invalid", "usd", "sale_usd.json", func(m map[string]any) {
			m["fx"].(map[string]any)["base"] = ""
			m["fx"].(map[string]any)["quote"] = ""
		}, true},
		{"USD microrate mismatch invalid", "usd", "sale_usd.json", func(m map[string]any) {
			m["fx"].(map[string]any)["rate_microrate"] = 52000001
		}, true},
		{"USD zero rate invalid", "usd", "sale_usd.json", func(m map[string]any) {
			m["fx"].(map[string]any)["rate"] = "0.000000"
			m["fx"].(map[string]any)["rate_microrate"] = int64(0)
		}, true},
	}
	_ = egpNoFx
	for _, tc := range cases {
		raw := mutate(t, loadFixture(t, tc.fixture), tc.mut)
		p, err := Decode(raw)
		if err != nil {
			if !tc.wantErr {
				t.Fatalf("%s: decode: %v", tc.name, err)
			}
			continue
		}
		_, err = Validate(p)
		if tc.wantErr && err == nil {
			t.Fatalf("%s: must fail", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("%s: must validate: %v", tc.name, err)
		}
		if tc.wantErr && err != nil && kindOf(err) != apperr.Unprocessable {
			t.Fatalf("%s: want 422, got %v", tc.name, err)
		}
	}
	// EGP + FX invalid (Retail never emits FX for EGP).
	raw := mutate(t, loadFixture(t, "sale_egp.json"), func(m map[string]any) {
		m["fx"] = map[string]any{"base": "USD", "quote": "EGP", "rate": "52.000000", "rate_microrate": int64(52000000)}
	})
	if p, err := Decode(raw); err == nil {
		if _, err := Validate(p); err == nil {
			t.Fatal("EGP + FX must be invalid")
		}
	}
}
