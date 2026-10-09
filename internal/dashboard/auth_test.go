package dashboard

import (
	"bytes"
	"net"
	"testing"
)

// TestDeriveKeyIsDomainSeparated: one pepper, distinct stable keys per
// purpose; an empty pepper fails closed.
func TestDeriveKeyIsDomainSeparated(t *testing.T) {
	pepper := bytes.Repeat([]byte{7}, 32)
	a, err := DeriveKey(pepper, "a")
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := DeriveKey(pepper, "a")
	b, _ := DeriveKey(pepper, "b")
	if len(a) != 32 || !bytes.Equal(a, a2) || bytes.Equal(a, b) {
		t.Fatal("keys must be 32 bytes, stable, and domain separated")
	}
	if _, err := DeriveKey(nil, "a"); err == nil {
		t.Fatal("empty pepper must fail")
	}
}

func TestClientIPMatrix(t *testing.T) {
	mustCIDRs := func(ss []string) []net.IPNet {
		var out []net.IPNet
		for _, s := range ss {
			_, n, err := net.ParseCIDR(s)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, *n)
		}
		return out
	}
	trusted := mustCIDRs([]string{"10.0.0.0/8", "2001:db8::/32"})
	cases := []struct {
		name    string
		remote  string
		xff     string
		trusted []net.IPNet
		want    string
	}{
		{"direct IPv4", "1.2.3.4:1234", "", nil, "1.2.3.4"},
		{"direct IPv6", "[2001:db8::5]:443", "", nil, "2001:db8::5"},
		{"untrusted peer ignores spoofed XFF", "9.9.9.9:1", "1.2.3.4", nil, "9.9.9.9"},
		{"trusted proxy single client", "10.1.2.3:999", "1.2.3.4", trusted, "1.2.3.4"},
		{"trusted proxy multiple hops use nearest untrusted", "10.1.2.3:999", "1.2.3.4, 10.9.9.9", trusted, "1.2.3.4"},
		{"all-trusted chain returns leftmost", "10.1.2.3:9", "10.4.4.4, 10.5.5.5", trusted, "10.4.4.4"},
		{"malformed XFF falls back to peer", "10.1.2.3:9", "not-an-ip", trusted, "10.1.2.3"},
		{"empty XFF falls back to peer", "10.1.2.3:9", "", trusted, "10.1.2.3"},
		{"IPv6 via trusted proxy", "2001:db8::1:443", "2001:db8:abcd::9", trusted, "2001:db8:abcd::9"},
		{"quoted XFF entry", "10.1.2.3:9", "\"1.2.3.4\"", trusted, "1.2.3.4"},
	}
	for _, tc := range cases {
		if got := ClientIP(tc.remote, tc.xff, tc.trusted); got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}
