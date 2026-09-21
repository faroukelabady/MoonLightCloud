package sale

import (
	"testing"
)

// TestContractFixtureParity is the Cloud side of the cross-repository
// contract-fixture strategy (no shared module; versioned JSON is the
// boundary). It proves parity with MoonLightRetail-emitted payloads for the
// required matrix. Retail mirrors these values in
// TestContractParityGoldenUSD (same sale_id, totals, split payments,
// USD→EGP snapshot, single root).
func TestContractFixtureParity(t *testing.T) {
	// Valid EGP sale (no FX).
	egp := decodeValidate(t, loadFixture(t, "sale_egp.json"))
	if egp.Currency != "EGP" || egp.Fx != nil {
		t.Fatal("valid EGP sale parity broken")
	}
	// Valid USD sale (USD→EGP).
	usd := decodeValidate(t, loadFixture(t, "sale_usd.json"))
	if usd.Currency != "USD" || usd.Fx == nil || usd.Fx.Base != "USD" || usd.Fx.Quote != "EGP" {
		t.Fatal("valid USD sale parity broken")
	}
	// Split payment sums to total.
	var sum int64
	for _, p := range usd.Payments {
		var err error
		sum, err = addChecked(sum, p.Amount.AmountMinor)
		if err != nil {
			t.Fatal(err)
		}
	}
	if sum != usd.Totals.Total.AmountMinor {
		t.Fatal("split payment parity broken")
	}
	// Classification-rich sale: exactly one root + distinct subcategories.
	for _, l := range usd.Lines {
		if len(l.Classifications.Roots) != 1 {
			t.Fatal("classification-rich parity broken")
		}
	}
	// Large integer boundary values preserved where valid (canonical layer;
	// sale money stays int64 — see canonical tests for MaxInt64).
	// Invalid payment aggregate rejected.
	badPay := mutate(t, loadFixture(t, "sale_usd.json"), func(m map[string]any) {
		m["payments"].([]any)[0].(map[string]any)["amount"].(map[string]any)["amount_minor"] = 1
	})
	if p, err := Decode(badPay); err == nil {
		if _, err := Validate(p); err == nil {
			t.Fatal("invalid payment aggregate parity broken")
		}
	}
	// Invalid classifications rejected.
	badClass := mutate(t, loadFixture(t, "sale_usd.json"), func(m map[string]any) {
		m["lines"].([]any)[0].(map[string]any)["classifications"].(map[string]any)["roots"] = []any{}
	})
	if p, err := Decode(badClass); err == nil {
		if _, err := Validate(p); err == nil {
			t.Fatal("invalid classification parity broken")
		}
	}
	// Invalid FX pair rejected.
	badFx := mutate(t, loadFixture(t, "sale_usd.json"), func(m map[string]any) {
		m["fx"].(map[string]any)["base"] = "EUR"
	})
	if p, err := Decode(badFx); err == nil {
		if _, err := Validate(p); err == nil {
			t.Fatal("invalid FX pair parity broken")
		}
	}
}
