package sale

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

func loadFixture(t *testing.T, name string) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return json.RawMessage(raw)
}

func decodeValidate(t *testing.T, raw json.RawMessage) Validated {
	t.Helper()
	p, err := Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	v, err := Validate(p)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	return v
}

func TestGoldenUSD(t *testing.T) {
	// Mirrors MoonLightRetail TestBuildSaleFinalizedUSD semantics exactly.
	v := decodeValidate(t, loadFixture(t, "sale_usd.json"))
	if v.SaleID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("bad sale id: %s", v.SaleID)
	}
	if v.Fx == nil || v.Fx.RateMicrorate != 52000000 || v.Fx.Rate != "52.000000" {
		t.Fatalf("fx snapshot not preserved: %+v", v.Fx)
	}
	if v.Totals.Total.AmountMinor != 1250 || len(v.Payments) != 2 || len(v.Lines) != 1 {
		t.Fatalf("bad totals/payments/lines: %+v", v.Totals)
	}
}

func TestGoldenEGPNoFx(t *testing.T) {
	v := decodeValidate(t, loadFixture(t, "sale_egp.json"))
	if v.Fx != nil {
		t.Fatal("EGP sale must carry no fx snapshot")
	}
	if v.Currency != "EGP" || v.Totals.Total.AmountMinor != 200000 {
		t.Fatalf("bad egp sale: %+v", v.Totals)
	}
}

func kindOf(err error) apperr.Kind {
	for err != nil {
		if ae, ok := err.(*apperr.Error); ok {
			return ae.Kind
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return apperr.Internal
		}
		err = u.Unwrap()
	}
	return apperr.Internal
}

func mutate(t *testing.T, raw json.RawMessage, fn func(map[string]any)) json.RawMessage {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	fn(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestInvalidSales(t *testing.T) {
	base := loadFixture(t, "sale_usd.json")
	cases := []struct {
		name string
		fn   func(map[string]any)
	}{
		{"float money", func(m map[string]any) {
			m["totals"].(map[string]any)["total"].(map[string]any)["amount_minor"] = 1250.50
		}},
		{"wrong currency", func(m map[string]any) { m["currency"] = "EUR" }},
		{"line currency mismatch", func(m map[string]any) {
			m["lines"].([]any)[0].(map[string]any)["unit_price"].(map[string]any)["currency"] = "EGP"
		}},
		{"zero quantity", func(m map[string]any) {
			m["lines"].([]any)[0].(map[string]any)["quantity"] = 0
		}},
		{"negative discount", func(m map[string]any) {
			m["totals"].(map[string]any)["discount"].(map[string]any)["amount_minor"] = -5
		}},
		{"line total mismatch", func(m map[string]any) {
			m["lines"].([]any)[0].(map[string]any)["line_total"].(map[string]any)["amount_minor"] = 999
		}},
		{"subtotal mismatch", func(m map[string]any) {
			m["totals"].(map[string]any)["subtotal"].(map[string]any)["amount_minor"] = 1
		}},
		{"total mismatch", func(m map[string]any) {
			m["totals"].(map[string]any)["total"].(map[string]any)["amount_minor"] = 1
		}},
		{"bad method", func(m map[string]any) {
			m["payments"].([]any)[0].(map[string]any)["method"] = "crypto"
		}},
		{"zero amount", func(m map[string]any) {
			m["payments"].([]any)[0].(map[string]any)["amount"].(map[string]any)["amount_minor"] = 0
		}},
		{"channel online", func(m map[string]any) { m["channel"] = "ONLINE" }},
		{"fx on egp", func(m map[string]any) {
			// Rewrite every currency to EGP but keep the fx block.
			var fix func(v any)
			fix = func(v any) {
				switch t := v.(type) {
				case map[string]any:
					if c, ok := t["currency"].(string); ok && c == "USD" {
						t["currency"] = "EGP"
					}
					for _, v := range t {
						fix(v)
					}
				case []any:
					for _, v := range t {
						fix(v)
					}
				}
			}
			m["currency"] = "EGP"
			fix(m["totals"])
			fix(m["lines"])
			fix(m["payments"])
		}},
		{"fx microrate mismatch", func(m map[string]any) {
			m["fx"].(map[string]any)["rate_microrate"] = 52000001
		}},
		{"fx rate garbage", func(m map[string]any) {
			m["fx"].(map[string]any)["rate"] = "lots"
		}},
		{"missing fx on usd", func(m map[string]any) { delete(m, "fx") }},
		{"no roots", func(m map[string]any) {
			m["lines"].([]any)[0].(map[string]any)["classifications"].(map[string]any)["roots"] = []any{}
		}},
		{"bad category id", func(m map[string]any) {
			m["lines"].([]any)[0].(map[string]any)["classifications"].(map[string]any)["roots"].([]any)[0].(map[string]any)["category_id"] = "x"
		}},
		{"empty lines", func(m map[string]any) { m["lines"] = []any{} }},
		{"empty payments", func(m map[string]any) { m["payments"] = []any{} }},
		{"negative width", func(m map[string]any) {
			m["lines"].([]any)[0].(map[string]any)["width_cm"] = -3
		}},
		{"paid before occurred", func(m map[string]any) {
			m["paid_at"] = "2026-09-19T10:00:00Z"
		}},
		{"bad sale id", func(m map[string]any) { m["sale_id"] = "nope" }},
	}
	for _, tc := range cases {
		raw := mutate(t, base, tc.fn)
		p, err := Decode(raw)
		if err != nil {
			continue // decode-level rejection is also a pass
		}
		if _, err := Validate(p); err == nil {
			t.Fatalf("%s: must fail validation", tc.name)
		} else if kindOf(err) != apperr.Unprocessable {
			t.Fatalf("%s: want 422, got %v", tc.name, err)
		}
	}
}

func TestMicrorate(t *testing.T) {
	for in, want := range map[string]int64{
		"52": 52000000, "52.000000": 52000000, "52.5": 52500000,
		"0.000001": 1, "999999.999999": 999999999999,
	} {
		got, err := parseMicrorate(in)
		if err != nil || got != want {
			t.Fatalf("%q: want %d, got %d (%v)", in, want, got, err)
		}
	}
	for _, bad := range []string{"", "-1", "+2", "0", "abc", "1000000", "52.0000005x"} {
		if _, err := parseMicrorate(bad); err == nil {
			t.Fatalf("%q: must fail", bad)
		}
	}
	// Half-up rounding at the 7th digit mirrors desktop.
	got, err := parseMicrorate("52.1234567")
	if err != nil || got != 52123457 {
		t.Fatalf("rounding: got %d (%v)", got, err)
	}
}

func TestNoCardSecretsRequired(t *testing.T) {
	// v1 has no PAN/CVV/provider-secret concepts; assert the fixture and
	// DTO carry none, so projection can never persist them.
	raw := string(loadFixture(t, "sale_usd.json"))
	for _, banned := range []string{"pan", "cvv", "cvc", "card_number", "expiry", "provider_secret", "api_key"} {
		if strings.Contains(strings.ToLower(raw), `"`+banned+`"`) {
			t.Fatalf("fixture must not contain %q", banned)
		}
	}
}

func TestBackoff(t *testing.T) {
	if Backoff(0) != 5*time.Second {
		t.Fatalf("attempt 0: %v", Backoff(0))
	}
	if Backoff(10) != time.Hour {
		t.Fatalf("cap: %v", Backoff(10))
	}
	if Backoff(2) != 20*time.Second {
		t.Fatalf("attempt 2: %v", Backoff(2))
	}
}

func TestMinimalArabicOnlyShopAccepted(t *testing.T) {
	// R60: the Retail-valid minimal shop (Arabic name only, blank English
	// name and phone) validates for sales. Shared CheckShopSnapshot keeps
	// sale/return parity; the mechanical OpenAPI gate covers the schema.
	raw := mutate(t, loadFixture(t, "sale_egp.json"), func(m map[string]any) {
		m["shop"] = map[string]any{"name_ar": "متجر", "name_en": "", "address_ar": "",
			"address_en": "", "phone": "", "receipt_footer_ar": "", "receipt_footer_en": ""}
	})
	p, err := Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, err := Validate(p); err != nil {
		t.Fatalf("minimal shop must validate: %v", err)
	}
}
