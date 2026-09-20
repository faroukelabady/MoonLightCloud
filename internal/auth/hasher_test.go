package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"strings"
	"testing"
)

var testPepper = bytes32('p')

func bytes32(c byte) []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = c
	}
	return b
}

func mustHasher(t *testing.T, pepper []byte) Hasher {
	t.Helper()
	h, err := NewHasher(pepper)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHasherRejectsBadPepperLength(t *testing.T) {
	if _, err := NewHasher([]byte("short")); err == nil {
		t.Fatal("short pepper must fail")
	}
	if _, err := NewHasher(make([]byte, 33)); err == nil {
		t.Fatal("long pepper must fail")
	}
	if _, err := NewHasher(nil); err == nil {
		t.Fatal("nil pepper must fail")
	}
}

func TestHashVerifyRoundtrip(t *testing.T) {
	h := mustHasher(t, testPepper)
	raw, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 64 {
		t.Fatalf("want 64 hex chars, got %d", len(raw))
	}
	verifier, salt, err := h.Hash(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Verify(raw, verifier, salt) {
		t.Fatal("valid secret must verify")
	}
	if h.Verify(raw+"x", verifier, salt) {
		t.Fatal("tampered secret must not verify")
	}
	if len(salt) != 16 || len(verifier) != 32 {
		t.Fatalf("want 16B salt + 32B verifier, got %d + %d", len(salt), len(verifier))
	}
}

func TestExactV1Construction(t *testing.T) {
	// verifier == HMAC-SHA256(key=pepper, message=salt || hex_secret).
	h := mustHasher(t, testPepper)
	raw, _ := GenerateSecret()
	verifier, salt, err := h.Hash(raw)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, testPepper)
	mac.Write(salt)
	mac.Write([]byte(raw))
	if string(mac.Sum(nil)) != string(verifier) {
		t.Fatal("verifier must equal HMAC-SHA256(pepper, salt || secret)")
	}
}

func TestHashNeverStoresPlaintext(t *testing.T) {
	h := mustHasher(t, testPepper)
	raw, _ := GenerateSecret()
	verifier, salt, _ := h.Hash(raw)
	if strings.Contains(string(verifier), raw) || strings.Contains(string(salt), raw) {
		t.Fatal("verifier/salt must not contain the raw secret")
	}
}

func TestSecretsUniqueAndSaltsUnique(t *testing.T) {
	a, _ := GenerateSecret()
	b, _ := GenerateSecret()
	if a == b {
		t.Fatal("secrets must differ")
	}
	h := mustHasher(t, testPepper)
	va, _, _ := h.Hash(a)
	vb, _, _ := h.Hash(a)
	if string(va) == string(vb) {
		t.Fatal("same secret must verify differently across salts (random salt)")
	}
}

func TestPepperIsolation(t *testing.T) {
	raw, _ := GenerateSecret()
	h1 := mustHasher(t, bytes32('1'))
	h2 := mustHasher(t, bytes32('2'))
	verifier, salt, _ := h1.Hash(raw)
	if h2.Verify(raw, verifier, salt) {
		t.Fatal("different pepper must not verify")
	}
}

func TestLegacyV0Parity(t *testing.T) {
	// Phase-1A construction, no pepper: SHA-256(salt || secret).
	raw, _ := GenerateSecret()
	salt := []byte("0123456789abcdef")
	sum := sha256.Sum256(append(append([]byte{}, salt...), raw...))
	if !VerifyLegacyV0(raw, sum[:], salt, "") {
		t.Fatal("legacy no-pepper vector must verify")
	}
	// Phase-1A with pepper string: HMAC-SHA256(key=pepper_string, salt||secret).
	mac := hmac.New(sha256.New, []byte("oldpepper"))
	mac.Write(salt)
	mac.Write([]byte(raw))
	if !VerifyLegacyV0(raw, mac.Sum(nil), salt, "oldpepper") {
		t.Fatal("legacy peppered vector must verify")
	}
	if VerifyLegacyV0(raw+"x", sum[:], salt, "") {
		t.Fatal("tampered secret must not verify under legacy")
	}
}
