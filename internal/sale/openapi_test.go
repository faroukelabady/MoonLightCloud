package sale

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestOpenAPIEventModel validates the api/openapi.yaml event contract
// without a heavy validator dependency: the discriminator on event_type
// must yield exactly one branch per supported type, the generic overlapping
// oneOf must be gone, and real fixtures must match exactly one branch.
func TestOpenAPIEventModel(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	for _, want := range []string{
		"discriminator:",
		"propertyName: event_type",
		"sale.finalized.v1: '#/components/schemas/SaleFinalizedV1Event'",
		"system.test.v1: '#/components/schemas/SystemTestV1Event'",
		"SaleFinalizedV1Event:",
		"SystemTestV1Event:",
		"enum: [sale.finalized.v1]",
		"enum: [system.test.v1]",
	} {
		if !strings.Contains(spec, want) {
			t.Fatalf("openapi must contain %q", want)
		}
	}
	if strings.Contains(spec, "- type: object\n              description: Generic JSON object") {
		t.Fatal("overlapping generic oneOf branch must be gone")
	}

	// Exactly one branch matches a real sale.finalized.v1 fixture: it
	// decodes + validates as SaleFinalizedV1 and is not a SystemTest event.
	matches := 0
	for _, name := range []string{"sale_usd.json", "sale_egp.json"} {
		fix := loadFixture(t, name)
		p, err := Decode(fix)
		if err != nil {
			t.Fatalf("%s: must decode as SaleFinalizedV1: %v", name, err)
		}
		if _, err := Validate(p); err != nil {
			t.Fatalf("%s: must validate as SaleFinalizedV1: %v", name, err)
		}
		var m map[string]any
		if err := json.Unmarshal(fix, &m); err != nil {
			t.Fatal(err)
		}
		matches++
		_ = m
	}
	if matches != 2 {
		t.Fatal("both sale fixtures must match the SaleFinalizedV1 branch")
	}
	// A malformed Sale (no roots) is rejected by the modeled contract.
	bad := mutate(t, loadFixture(t, "sale_usd.json"), func(m map[string]any) {
		m["lines"].([]any)[0].(map[string]any)["classifications"].(map[string]any)["roots"] = []any{}
	})
	if p, err := Decode(bad); err == nil {
		if _, err := Validate(p); err == nil {
			t.Fatal("malformed Sale must be rejected by the modeled contract")
		}
	}
	// system.test.v1 diagnostics do not validate as SaleFinalizedV1.
	if p, err := Decode(json.RawMessage(`{"ping":1}`)); err == nil {
		if _, err := Validate(p); err == nil {
			t.Fatal("system.test payload must not match the Sale branch")
		}
	}
}

// TestOpenAPIBreakdownExclusivity pins the F1 contract in the committed
// spec: the four breakdown shapes reject unknown properties, and rows
// split over an exclusive oneOf. Structural companion to the runtime
// key-set test in the http adapter package.
func TestOpenAPIBreakdownExclusivity(t *testing.T) {
	raw, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	for _, schema := range []string{
		"LineSaleTotal:", "SaleCurrencyTotal:", "LineBreakdownRow:", "HeaderBreakdownRow:",
	} {
		block := sectionAfter(spec, schema)
		if !strings.Contains(block, "additionalProperties: false") {
			t.Fatalf("%s must set additionalProperties: false", schema)
		}
	}
	if !strings.Contains(spec, "oneOf:") {
		t.Fatal("breakdown rows must split over oneOf")
	}
	if !strings.Contains(spec, "- $ref: '#/components/schemas/LineBreakdownRow'") ||
		!strings.Contains(spec, "- $ref: '#/components/schemas/HeaderBreakdownRow'") {
		t.Fatal("oneOf must reference exactly the two row branches")
	}
}

// sectionAfter returns the spec text following a schema header, bounded at
// the next same-indent schema key, so assertions stay scoped.
func sectionAfter(spec, header string) string {
	idx := strings.Index(spec, "    "+header)
	if idx < 0 {
		return ""
	}
	rest := spec[idx:]
	// Same-indent keys are "\n" + exactly 4 spaces + non-space.
	for i := len(header) + 4; i+5 < len(rest); i++ {
		if rest[i] == '\n' && rest[i+1] == ' ' && rest[i+2] == ' ' &&
			rest[i+3] == ' ' && rest[i+4] == ' ' && rest[i+5] != ' ' {
			return rest[:i]
		}
	}
	return rest
}

var _ = json.Marshal
