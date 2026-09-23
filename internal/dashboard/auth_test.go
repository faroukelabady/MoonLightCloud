package dashboard

import (
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
	if _, err := VerifySession(key, "not-base64!!!", now); err == nil {
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
