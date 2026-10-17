package postgres

import "testing"

func TestFormatMicrorateExact(t *testing.T) {
	for _, tc := range []struct {
		value int64
		want  string
	}{
		{1, "0.000001"},
		{1_000_000, "1.000000"},
		{52_123_456, "52.123456"},
		{9_223_372_036_854_775_807, "9223372036854.775807"},
	} {
		if got := formatMicrorate(tc.value); got != tc.want {
			t.Fatalf("%d: got %s, want %s", tc.value, got, tc.want)
		}
	}
}
