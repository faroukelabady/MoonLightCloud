package catalog

import (
	"math"
	"testing"
)

func TestComputeProductAvailabilityMatrix(t *testing.T) {
	cap := func(v int) *int { return &v }
	cases := []struct {
		name                    string
		active, found           bool
		sellOnline, policyFound bool
		cap                     *int
		stock                   int
		inventoryFound          bool
		wantOnline              int
		wantReady               bool
	}{
		{"uncapped", true, true, true, true, nil, 10, true, 10, true},
		{"cap above stock", true, true, true, true, cap(10), 4, true, 4, true},
		{"cap below stock", true, true, true, true, cap(3), 10, true, 3, true},
		{"zero cap", true, true, true, true, cap(0), 10, true, 0, true},
		{"online disabled", true, true, false, true, nil, 10, true, 0, true},
		{"product inactive", false, true, true, true, nil, 10, true, 0, true},
		{"zero stock", true, true, true, true, nil, 0, true, 0, true},
		{"missing policy", true, true, false, false, nil, 10, true, 0, false},
		{"missing inventory", true, true, true, true, nil, 0, false, 0, false},
		{"missing product", false, false, false, false, nil, 0, false, 0, false},
		// Negative stock is not producer-valid (Retail CHECK >= 0); the
		// clamp keeps external exposure safe if it ever arrived.
		{"negative clamped", true, true, true, true, nil, -3, true, 0, true},
		{"negative with cap", true, true, true, true, cap(5), -3, true, 0, true},
		{"max stock uncapped", true, true, true, true, nil, math.MaxInt32, true, math.MaxInt32, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ready := ComputeProductAvailability(
				tc.active, tc.found, tc.sellOnline, tc.policyFound, tc.cap,
				tc.stock, tc.inventoryFound,
			)
			if got != tc.wantOnline || ready != tc.wantReady {
				t.Fatalf("got (%d,%v), want (%d,%v)", got, ready, tc.wantOnline, tc.wantReady)
			}
		})
	}
}
