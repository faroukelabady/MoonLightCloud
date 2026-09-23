package dashboard

import (
	"net"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	k, err := SessionKey([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSessionRoundtrip(t *testing.T) {
	key := testKey(t)
	now := time.Now()
	tok, err := IssueSession(key, "operator", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	user, err := VerifySession(key, tok, now)
	if err != nil || user != "operator" {
		t.Fatalf("roundtrip: %v %q", err, user)
	}
}

func TestSessionTamperAndExpiry(t *testing.T) {
	key := testKey(t)
	now := time.Now()
	tok, _ := IssueSession(key, "operator", now.Add(time.Hour))
	if _, err := VerifySession(key, tok+"x", now); err == nil {
		t.Fatal("tampered token must fail")
	}
	other, _ := SessionKey([]byte("fedcba9876543210fedcba9876543210"))
	if _, err := VerifySession(other, tok, now); err == nil {
		t.Fatal("wrong key must fail")
	}
	if _, err := VerifySession(key, tok, now.Add(2*time.Hour)); err == nil {
		t.Fatal("expired session must fail")
	}
}

func TestSessionExpiryBoundary(t *testing.T) {
	key := testKey(t)
	base := time.Unix(1_700_000_000, 0).UTC()
	exp := base.Add(time.Hour)
	tok, err := IssueSession(key, "operator", exp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifySession(key, tok, exp.Add(-time.Second)); err != nil {
		t.Fatalf("before expiry must pass: %v", err)
	}
	if _, err := VerifySession(key, tok, exp); err == nil {
		t.Fatal("exact expiry instant must fail")
	}
	if _, err := VerifySession(key, tok, exp.Add(time.Second)); err == nil {
		t.Fatal("after expiry must fail")
	}
	if _, err := VerifySession(key, "not-base64!!!", base); err == nil {
		t.Fatal("malformed token must fail")
	}
}

func TestPasswordHashVerify(t *testing.T) {
	hash, err := HashPassword("correct-horse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$") {
		t.Fatalf("PHC shape: %s", hash[:20])
	}
	if !VerifyPassword("correct-horse", hash) {
		t.Fatal("valid password must verify")
	}
	if VerifyPassword("wrong-horse", hash) {
		t.Fatal("wrong password must fail")
	}
	for _, bad := range []string{"", "plaintext", "$argon2id$v=19$bad", "$bcrypt$v=1$x$y"} {
		if VerifyPassword("x", bad) {
			t.Fatalf("malformed hash must fail: %q", bad)
		}
	}
	if _, err := HashPassword(""); err == nil {
		t.Fatal("empty password must fail")
	}
}

func TestLoginLimiter(t *testing.T) {
	l := NewLoginLimiter()
	now := time.Now()
	for i := 0; i < LoginMaxFailures; i++ {
		if l.Blocked("1.2.3.4", now) {
			t.Fatalf("blocked early at %d", i)
		}
		l.RecordFailure("1.2.3.4", now)
	}
	if !l.Blocked("1.2.3.4", now) {
		t.Fatal("must block after max failures")
	}
	if l.Blocked("5.6.7.8", now) {
		t.Fatal("other IPs unaffected")
	}
	l.RecordSuccess("1.2.3.4")
	if l.Blocked("1.2.3.4", now) {
		t.Fatal("success must reset")
	}
	// Window expiry.
	for i := 0; i < LoginMaxFailures; i++ {
		l.RecordFailure("9.9.9.9", now)
	}
	if !l.Blocked("9.9.9.9", now) {
		t.Fatal("must block")
	}
	if l.Blocked("9.9.9.9", now.Add(LoginWindow+time.Minute)) {
		t.Fatal("window must expire")
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
