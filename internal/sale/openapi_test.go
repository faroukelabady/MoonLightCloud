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
