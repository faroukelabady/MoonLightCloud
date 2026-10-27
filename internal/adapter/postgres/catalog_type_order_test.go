package postgres

import (
	"encoding/json"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

func TestProductTypeEqualRevisionDimensionSet(t *testing.T) {
	env := openSaleEnv(t)
	typeID := "20000000-0000-4000-8000-000000000001"
	payload := func(dimensions []string) string {
		t.Helper()
		var p map[string]any
		if err := json.Unmarshal([]byte(typePayload(typeID, "custom", 1)), &p); err != nil {
			t.Fatal(err)
		}
		p["dimensions"] = dimensions
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	for i, tc := range []struct {
		name       string
		dimensions []string
		blocked    bool
	}{
		{"initial display order", []string{"painting_style", "color"}, false},
		{"identical repeat", []string{"painting_style", "color"}, false},
		{"same set different order", []string{"color", "painting_style"}, false},
		{"different set same revision", []string{"color"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eventID := variantEventID(98000 + i)
			ingestCatalog(t, env, eventID, catalog.EventProductTypeSnapshotV1, "2026-09-20T10:00:00Z", payload(tc.dimensions))
			res := projectCatalogOnce(t, env, eventID)
			if tc.blocked {
				if res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrCatalogRevisionConflict {
					t.Fatalf("changed set must remain a conflict: %+v", res)
				}
			} else if res.Outcome != catalog.OutcomeProcessed && res.Outcome != catalog.OutcomeAlready {
				t.Fatalf("equivalent set must converge: %+v", res)
			}
		})
	}
}
