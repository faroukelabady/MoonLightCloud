package returnrefund

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestOpenAPIEventModel validates the api/openapi.yaml event contract for
// sale.return_refund.finalized.v1: the discriminator yields exactly one
// branch, real fixtures match the return branch only, and malformed returns
// are rejected by the modeled contract.
func TestOpenAPIEventModel(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	for _, want := range []string{
		"sale.return_refund.finalized.v1: '#/components/schemas/ReturnRefundFinalizedV1Event'",
		"ReturnRefundFinalizedV1Event:",
		"ReturnRefundFinalizedV1:",
		"enum: [sale.return_refund.finalized.v1]",
		"enum: [return, void]",
	} {
		if !strings.Contains(spec, want) {
			t.Fatalf("openapi must contain %q", want)
		}
	}

	matches := 0
	for _, name := range []string{"return_partial.json", "return_full.json"} {
		fix := loadFixture(t, name)
		p, err := Decode(fix)
		if err != nil {
			t.Fatalf("%s: must decode as ReturnRefundFinalizedV1: %v", name, err)
		}
		if _, err := Validate(p); err != nil {
			t.Fatalf("%s: must validate as ReturnRefundFinalizedV1: %v", name, err)
		}
		var m map[string]any
		if err := json.Unmarshal(fix, &m); err != nil {
			t.Fatal(err)
		}
		matches++
		_ = m
	}
	if matches != 2 {
		t.Fatal("both return fixtures must match the ReturnRefundFinalizedV1 branch")
	}
	bad := mutate(t, loadFixture(t, "return_partial.json"), func(m map[string]any) {
		m["lines"] = []any{}
	})
	if p, err := Decode(bad); err == nil {
		if _, err := Validate(p); err == nil {
			t.Fatal("malformed return must be rejected by the modeled contract")
		}
	}
}
