package sync

import (
	"strings"
	"testing"
)

// HIGH-03 regression: 2^53+1 must not collapse to 2^53 (old float64 bug).
func TestCanonicalLargeIntegersExact(t *testing.T) {
	cases := []string{
		`{"v":9007199254740991}`,     // 2^53 - 1
		`{"v":9007199254740992}`,     // 2^53
		`{"v":9007199254740993}`,     // 2^53 + 1
		`{"v":9223372036854775807}`,  // MaxInt64
		`{"v":-9223372036854775808}`, // MinInt64
	}
	seen := map[string]string{}
	for _, in := range cases {
		out, err := canonicalJSON([]byte(in))
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		s := string(out)
		if prev, dup := seen[s]; dup {
			t.Fatalf("collision: %s and %s both canonicalize to %s", prev, in, s)
		}
		seen[s] = in
	}
	// Adjacent int64 values stay distinct.
	a, _ := canonicalJSON([]byte(`{"v":9223372036854775806}`))
	b, _ := canonicalJSON([]byte(`{"v":9223372036854775807}`))
	if string(a) == string(b) {
		t.Fatal("adjacent int64 values must not collide")
	}
	// Distinct large integers must hash distinctly.
	h1 := hashOf(t, `{"v":9007199254740992}`)
	h2 := hashOf(t, `{"v":9007199254740993}`)
	if h1 == h2 {
		t.Fatal("2^53 and 2^53+1 must not hash identically")
	}
}

func TestCanonicalNumericEquivalence(t *testing.T) {
	// Rule A: numerically equivalent spellings canonicalize identically.
	spellings := []string{`{"v":1}`, `{"v":1.0}`, `{"v":1e0}`, `{"v":10e-1}`, `{"v":1.00}`, `{"v":0.1e1}`}
	base, err := canonicalJSON([]byte(spellings[0]))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range spellings[1:] {
		out, err := canonicalJSON([]byte(s))
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if string(out) != string(base) {
			t.Fatalf("rule A: %s -> %s, want %s", s, out, base)
		}
	}
	// -0 normalizes to 0.
	z, _ := canonicalJSON([]byte(`{"v":-0}`))
	if string(z) != `{"v":0}` {
		t.Fatalf("-0 must normalize to 0, got %s", z)
	}
}

func TestCanonicalShape(t *testing.T) {
	// Key reorder + whitespace + nesting + unicode.
	a, err := canonicalJSON([]byte(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := canonicalJSON([]byte("{ \u0022a\u0022 : 1 , \u0022b\u0022 : 2 }"))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("reorder/whitespace/unicode must canonicalize identically: %s vs %s", a, b)
	}
	nested1, _ := canonicalJSON([]byte(`{"x":{"d":[3,2,1],"c":1},"y":"مرحبا"}`))
	nested2, _ := canonicalJSON([]byte(`{ "y" : "مرحبا" , "x" : { "c" : 1 , "d" : [3,2,1] } }`))
	if string(nested1) != string(nested2) {
		t.Fatal("nested reorder/whitespace must canonicalize identically")
	}
}

func TestCanonicalHostileAndInvalid(t *testing.T) {
	// Huge exponent: bounded rejection, never enormous allocation.
	if _, err := canonicalJSON([]byte(`{"v":1e1000000}`)); err == nil {
		t.Fatal("huge exponent must be rejected, not expanded")
	}
	// Invalid JSON numbers rejected.
	for _, bad := range []string{
		`{"v":01}`, `{"v":1.}`, `{"v":.5}`, `{"v":+1}`,
		`{"v":NaN}`, `{"v":Infinity}`, `{"v":1e}`, `{"v":--1}`,
	} {
		if _, err := canonicalJSON([]byte(bad)); err == nil {
			t.Fatalf("%s must be rejected", bad)
		}
	}
	// Non-object payloads rejected.
	for _, bad := range []string{`[1,2]`, `1`, `"x"`, `null`} {
		if _, err := canonicalJSON([]byte(bad)); err == nil {
			t.Fatalf("%s must be rejected (object required)", bad)
		}
	}
}

func TestNoFloat64InCanonicalPath(t *testing.T) {
	// 1.5 stays exact (not binary rounded) and distinct from 1 and 2.
	a, _ := canonicalJSON([]byte(`{"v":1.5}`))
	if string(a) != `{"v":1.5}` {
		t.Fatalf("1.5 must survive exactly, got %s", a)
	}
}

// MED-03: sub-microsecond occurred_at digits normalize so legitimate
// retries compare equal after the TIMESTAMPTZ round-trip.
func TestOccurredAtMicrosecondNormalization(t *testing.T) {
	id := "22222222-2222-7222-8222-222222222222"
	full := batchJSON(eventJSON(id, "system.test.v1", "2026-09-19T10:20:30.123456789Z", `{"a":1}`))
	trunc := batchJSON(eventJSON(id, "system.test.v1", "2026-09-19T10:20:30.123456Z", `{"a":1}`))
	b1, err := ParseBatch([]byte(full), testDevice, testNow)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := ParseBatch([]byte(trunc), testDevice, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if !b1.Events[0].OccurredAt.Equal(b2.Events[0].OccurredAt) {
		t.Fatalf("sub-microsecond digits must normalize: %v vs %v",
			b1.Events[0].OccurredAt, b2.Events[0].OccurredAt)
	}
}

func hashOf(t *testing.T, payload string) string {
	t.Helper()
	body := batchJSON(eventJSON(
		"22222222-2222-7222-8222-222222222222",
		"system.test.v1", "2026-09-19T10:20:30Z", payload))
	b, err := ParseBatch([]byte(body), testDevice, testNow)
	if err != nil {
		t.Fatal(err)
	}
	return string(b.Events[0].Hash)
}

var _ = strings.Contains
