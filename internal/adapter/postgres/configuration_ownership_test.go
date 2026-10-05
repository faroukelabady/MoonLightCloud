package postgres

// Phase 15-R1 F17: Cloud Store/configuration ownership through the real
// projector and real Store bindings (the event is registered in the
// production registry; the test harness mirrors that registration).
// Regression tests assert the DESIRED invariant.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

func configPayload(productID string, revision int64, entries []map[string]any) string {
	raw, _ := json.Marshal(map[string]any{
		"product_id": productID, "configurations": entries,
		"configuration_revision": revision,
	})
	return string(raw)
}

func configEntry(id, style, color string, egp int64, position int) map[string]any {
	return map[string]any{
		"configuration_id": id, "kind": "frame",
		"style_code": style, "style_name_ar": style + "-ar",
		"color_code": color, "color_name_ar": color + "-ar",
		"price_delta_egp_cents": egp, "enabled": true,
		"position": position, "configuration_revision": 1,
	}
}

// F17 case 1+2+4: Store B ingress against Store A's Product is blocked;
// reuse of A's configuration ID by B is blocked with A's row intact;
// same style/color with distinct IDs across Stores stays supported.
func TestConfigurationStoreOwnershipBoundary(t *testing.T) {
	f := openScopeFixture(t)
	seq := 0
	project := func(dev, cred, typ, payload string) catalog.ProjectResult {
		event := fmt.Sprintf("e5c015a0-0000-4000-8000-%012d", seq)
		seq++
		f.ingest(t, dev, cred, event, typ, payload)
		result := projectCatalogTerminal(t, f, event, typ)
		return result
	}
	requireOutcome := func(result catalog.ProjectResult, want catalog.Outcome) {
		if result.Outcome != want {
			t.Fatalf("outcome %+v want %v", result, want)
		}
	}

	productA := "22222222-0000-4000-8000-0000000000a1"
	productB := "22222222-0000-4000-8000-0000000000b1"
	catA := "aaaaaaaa-0000-4000-8000-0000000000a1"
	catB := "aaaaaaaa-0000-4000-8000-0000000000b1"

	// Store A owns Product A; Store B owns Product B (same SKU pattern,
	// distinct identities — existing same-SKU Store separation).
	requireOutcome(project(f.devA, f.credA, catalog.EventCategorySnapshotV1,
		categoryPayload(catA, "active", map[string]string{"en": "RootA"}, nil, 1)), catalog.OutcomeProcessed)
	requireOutcome(project(f.devB, f.credB, catalog.EventCategorySnapshotV1,
		categoryPayload(catB, "active", map[string]string{"en": "RootB"}, nil, 1)), catalog.OutcomeProcessed)
	requireOutcome(project(f.devA, f.credA, catalog.EventProductSnapshotV1,
		productPayload(productA, "ML-SHARED-SKU", "A", catA, nil, nil, 1)), catalog.OutcomeProcessed)
	requireOutcome(project(f.devB, f.credB, catalog.EventProductSnapshotV1,
		productPayload(productB, "ML-SHARED-SKU", "B", catB, nil, nil, 1)), catalog.OutcomeProcessed)

	configIDA := "11111111-0000-4000-8000-0000000000c1"

	// Legitimate same-Store create + update + replay.
	requireOutcome(project(f.devA, f.credA, catalog.EventProductConfigurationSnapshotV1,
		configPayload(productA, 1, []map[string]any{configEntry(configIDA, "classic", "black", 30000, 0)})), catalog.OutcomeProcessed)
	requireOutcome(project(f.devA, f.credA, catalog.EventProductConfigurationSnapshotV1,
		configPayload(productA, 1, []map[string]any{configEntry(configIDA, "classic", "black", 30000, 0)})), catalog.OutcomeProcessed)

	// F17 case 1: Store B submits configuration state for Store A's
	// Product → blocked; no A/B configuration mutation.
	requireOutcome(project(f.devB, f.credB, catalog.EventProductConfigurationSnapshotV1,
		configPayload(productA, 2, []map[string]any{configEntry(configIDA, "modern", "gold", 90000, 0)})), catalog.OutcomeBlocked)

	// F17 case 2: B's Product reuses A's configuration ID → blocked.
	requireOutcome(project(f.devB, f.credB, catalog.EventProductConfigurationSnapshotV1,
		configPayload(productB, 1, []map[string]any{configEntry(configIDA, "modern", "gold", 50000, 0)})), catalog.OutcomeBlocked)

	// A's original ownership and values unchanged.
	devices := NewDevices(f.pool, 5*time.Second)
	rows, err := devices.CatalogProductConfigurations(context.Background(), productA)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].StyleCode != "classic" || rows[0].PriceDeltaEGPCents != 30000 {
		t.Fatalf("Store A configuration mutated by foreign ingress: %+v", rows)
	}
	bRows, err := devices.CatalogProductConfigurations(context.Background(), productB)
	if err != nil {
		t.Fatal(err)
	}
	if len(bRows) != 0 {
		t.Fatalf("Store B gained a foreign-owned configuration: %+v", bRows)
	}

	// F17 case 4: same style/color with distinct IDs across Stores works.
	requireOutcome(project(f.devB, f.credB, catalog.EventProductConfigurationSnapshotV1,
		configPayload(productB, 1, []map[string]any{
			configEntry("11111111-0000-4000-8000-0000000000c2", "classic", "black", 40000, 0),
		})), catalog.OutcomeProcessed)
}
