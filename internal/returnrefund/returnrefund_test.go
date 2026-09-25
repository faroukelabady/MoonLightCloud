package returnrefund

import (
	"encoding/json"
	"os"
	"testing"

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

func TestGoldenPartialReturn(t *testing.T) {
	v := decodeValidate(t, loadFixture(t, "return_partial.json"))
	if v.ReturnRefundID != "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" {
		t.Fatalf("bad return id: %s", v.ReturnRefundID)
	}
	if v.Kind != "return" || v.Currency != "USD" {
		t.Fatalf("bad kind/currency: %+v", v.Payload)
	}
	if v.Fx == nil || v.Fx.Base != "USD" || v.Fx.Quote != "EGP" || v.Fx.RateMicrorate != 52000000 {
		t.Fatalf("fx snapshot not preserved: %+v", v.Fx)
	}
	if v.SaleEventID == nil || *v.SaleEventID != "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee" {
		t.Fatalf("sale event lineage not preserved: %+v", v.SaleEventID)
	}
	if v.Totals.RefundTotal.AmountMinor != 1250 || len(v.Refunds) != 2 || len(v.Lines) != 1 {
		t.Fatalf("bad totals/refunds/lines: %+v", v.Totals)
	}
	var paid int64
	for _, r := range v.Refunds {
		paid += r.Amount.AmountMinor
	}
	if paid != 1250 {
		t.Fatalf("split refunds must sum exactly: %d", paid)
	}
}

func TestGoldenFullVoid(t *testing.T) {
	v := decodeValidate(t, loadFixture(t, "return_full.json"))
	if v.Kind != "void" || v.Currency != "EGP" || v.Fx != nil {
		t.Fatalf("bad void: %+v", v.Payload)
	}
	if v.Totals.RefundTotal.AmountMinor != 2900 || len(v.Lines) != 2 {
		t.Fatalf("bad void totals/lines: %+v", v.Totals)
	}
}

func TestInvalidReturns(t *testing.T) {
	base := loadFixture(t, "return_partial.json")
	cases := []struct {
		name string
		fn   func(map[string]any)
	}{
		{"float money", func(m map[string]any) {
			m["totals"].(map[string]any)["refund_total"].(map[string]any)["amount_minor"] = 1250.50
		}},
		{"bad kind", func(m map[string]any) { m["kind"] = "exchange" }},
		{"bad reason", func(m map[string]any) { m["reason"] = "changed_mind_later" }},
		{"bad return id", func(m map[string]any) { m["return_refund_id"] = "nope" }},
		{"bad sale id", func(m map[string]any) { m["sale_id"] = "nope" }},
		{"bad sale event id", func(m map[string]any) { m["sale_event_id"] = "nope" }},
		{"wrong currency", func(m map[string]any) { m["currency"] = "EUR" }},
		{"refund currency mismatch", func(m map[string]any) {
			m["refunds"].([]any)[0].(map[string]any)["amount"].(map[string]any)["currency"] = "EGP"
		}},
		{"zero quantity", func(m map[string]any) {
			m["lines"].([]any)[0].(map[string]any)["quantity"] = 0
		}},
		{"negative refund", func(m map[string]any) {
			m["lines"].([]any)[0].(map[string]any)["refund"].(map[string]any)["amount_minor"] = -5
		}},
		{"header refund mismatch", func(m map[string]any) {
			m["totals"].(map[string]any)["refund_total"].(map[string]any)["amount_minor"] = 1
		}},
		{"header gross mismatch", func(m map[string]any) {
			m["totals"].(map[string]any)["gross"].(map[string]any)["amount_minor"] = 1
		}},
		{"refund payments mismatch", func(m map[string]any) {
			m["refunds"].([]any)[0].(map[string]any)["amount"].(map[string]any)["amount_minor"] = 1
		}},
		{"bad method", func(m map[string]any) {
			m["refunds"].([]any)[0].(map[string]any)["method"] = "store_credit"
		}},
		{"zero amount", func(m map[string]any) {
			m["refunds"].([]any)[0].(map[string]any)["amount"].(map[string]any)["amount_minor"] = 0
		}},
		{"duplicate original line", func(m map[string]any) {
			lines := m["lines"].([]any)
			m["lines"] = append(lines, lines[0])
		}},
		{"bad original line id", func(m map[string]any) {
			m["lines"].([]any)[0].(map[string]any)["original_sale_line_id"] = "x"
		}},
		{"channel online", func(m map[string]any) { m["channel"] = "ONLINE" }},
		{"fx on egp", func(m map[string]any) {
			m["currency"] = "EGP"
			m["fx"] = map[string]any{"base": "USD", "quote": "EGP", "rate": "52.000000", "rate_microrate": 52000000}
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
			fix(m["totals"])
			fix(m["lines"])
			fix(m["refunds"])
		}},
		{"fx pair mismatch", func(m map[string]any) {
			m["fx"].(map[string]any)["base"] = "EUR"
		}},
		{"fx microrate mismatch", func(m map[string]any) {
			m["fx"].(map[string]any)["rate_microrate"] = 52000001
		}},
		{"missing fx on usd", func(m map[string]any) { delete(m, "fx") }},
		{"empty lines", func(m map[string]any) { m["lines"] = []any{} }},
		{"empty refunds nonzero", func(m map[string]any) { m["refunds"] = []any{} }},
		{"note too long", func(m map[string]any) {
			long := ""
			for i := 0; i < 600; i++ {
				long += "x"
			}
			m["note"] = long
		}},
		{"bad actor id", func(m map[string]any) {
			m["actor"].(map[string]any)["user_id"] = "x"
		}},
		{"missing occurred", func(m map[string]any) { m["occurred_at"] = "yesterday" }},
		{"bad return number", func(m map[string]any) { m["return_number"] = "" }},
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

func TestZeroRefundEmptyPayments(t *testing.T) {
	raw := mutate(t, loadFixture(t, "return_full.json"), func(m map[string]any) {
		var zero func(v any)
		zero = func(v any) {
			switch t := v.(type) {
			case map[string]any:
				if a, ok := t["amount_minor"]; ok {
					_ = a
					t["amount_minor"] = 0
				}
				for _, v := range t {
					zero(v)
				}
			case []any:
				for _, v := range t {
					zero(v)
				}
			}
		}
		zero(m["totals"])
		zero(m["lines"])
		m["refunds"] = []any{}
	})
	p, err := Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, err := Validate(p); err != nil {
		t.Fatalf("zero refund with empty payments must validate: %v", err)
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
	for _, bad := range []string{"", "-1", "+2", "0", "abc", "1000000"} {
		if _, err := parseMicrorate(bad); err == nil {
			t.Fatalf("%q: must fail", bad)
		}
	}
}

func TestMinimalArabicOnlyShopAccepted(t *testing.T) {
	// R60: the Retail-valid minimal shop (Arabic name only) validates for
	// returns. The mechanical OpenAPI gate covers the schema side.
	raw := mutate(t, loadFixture(t, "return_full.json"), func(m map[string]any) {
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

func TestExtendedHistoricalCostSemantics(t *testing.T) {
	// R05: cost is extended historical cost (unit 123 × qty 3 = 369).
	cost := map[string]any{"amount_minor": 369, "currency": "USD"}
	raw := mutate(t, loadFixture(t, "return_partial.json"), func(m map[string]any) {
		line := m["lines"].([]any)[0].(map[string]any)
		line["quantity"] = 3
		line["cost"] = cost
	})
	p, err := Decode(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	v, err := Validate(p)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if v.Lines[0].Cost == nil || v.Lines[0].Cost.AmountMinor != 369 {
		t.Fatalf("extended cost must survive as 369: %+v", v.Lines[0].Cost)
	}
}
