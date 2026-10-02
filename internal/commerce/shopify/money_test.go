package shopify

import "testing"

// Phase 11 §38/§167: exact money. Integer arithmetic only, no floats,
// values beyond 2^53 preserved exactly.

func TestFormatMinorUnitsExact(t *testing.T) {
	cases := []struct {
		minor int64
		want  string
	}{
		{0, "0.00"},
		{1, "0.01"},
		{65000, "650.00"},
		{65025, "650.25"},
		{1, "0.01"},
		// beyond JavaScript safe integer range, exact
		{9007199254740993, "90071992547409.93"},
		{9223372036854775807, "92233720368547758.07"},
	}
	for _, tc := range cases {
		got, err := FormatMinorUnits(tc.minor)
		if err != nil || got != tc.want {
			t.Fatalf("FormatMinorUnits(%d) = %q, %v; want %q", tc.minor, got, err, tc.want)
		}
	}
}

func TestFormatMinorUnitsRejectsNegative(t *testing.T) {
	if _, err := FormatMinorUnits(-1); err == nil {
		t.Fatal("negative price accepted")
	}
}

func TestParseMoneyStringExactRoundTrip(t *testing.T) {
	for _, minor := range []int64{0, 1, 65000, 9007199254740993, 9223372036854775807} {
		formatted, err := FormatMinorUnits(minor)
		if err != nil {
			t.Fatal(err)
		}
		back, err := ParseMoneyString(formatted, "EGP")
		if err != nil || back != minor {
			t.Fatalf("round trip %d -> %q -> %d, %v", minor, formatted, back, err)
		}
	}
}

func TestParseMoneyStringRejectsGarbage(t *testing.T) {
	for _, raw := range []string{"", "abc", "1.234", "1.2.3", "-5.00", "92233720368547758070"} {
		if _, err := ParseMoneyString(raw, "EGP"); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}
