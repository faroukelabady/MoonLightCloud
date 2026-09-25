package postgres

import (
	"math"
	"testing"
)

// Pure arithmetic boundary tests for the cumulative guards (no DB):
// the guards must fail closed on overflow so a wrapped sum can never
// bypass the Sale ceiling.
func TestCumulativeWithinCeilingBoundaries(t *testing.T) {
	cases := []struct {
		name      string
		committed int64
		candidate int64
		ceiling   int64
		want      bool
	}{
		{"zero", 0, 0, 0, true},
		{"exact ceiling passes", 150000, 50000, 200000, true},
		{"one minor over blocks", 150000, 50001, 200000, false},
		{"empty ceiling with nonzero candidate blocks", 0, 1, 0, false},
		{"maxint64 sum at ceiling passes", math.MaxInt64 - 1, 1, math.MaxInt64, true},
		{"maxint64 overflow blocks", math.MaxInt64, 1, math.MaxInt64, false},
		{"maxint64 committed plus anything blocks", math.MaxInt64, math.MaxInt64, math.MaxInt64, false},
		{"candidate alone overflows ceiling type range blocks", 0, math.MaxInt64, math.MaxInt64 - 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := cumulativeWithinCeiling(tc.committed, tc.candidate, tc.ceiling); got != tc.want {
				t.Fatalf("cumulativeWithinCeiling(%d,%d,%d) = %v, want %v",
					tc.committed, tc.candidate, tc.ceiling, got, tc.want)
			}
		})
	}
}

func TestAddInt64Checked(t *testing.T) {
	if _, err := addInt64(math.MaxInt64, 1); err == nil {
		t.Fatal("MaxInt64+1 must overflow")
	}
	if _, err := addInt64(math.MinInt64, -1); err == nil {
		t.Fatal("MinInt64-1 must overflow")
	}
	if v, err := addInt64(math.MaxInt64, 0); err != nil || v != math.MaxInt64 {
		t.Fatalf("MaxInt64+0 = %d, %v", v, err)
	}
}
