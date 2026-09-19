package auth

import (
	"strings"
	"testing"
)

func TestHashVerifyRoundtrip(t *testing.T) {
	h := NewHasher("pepper")
	raw, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 64 {
		t.Fatalf("want 64 hex chars, got %d", len(raw))
	}
	hash, salt, err := h.Hash(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !h.Verify(raw, hash, salt) {
		t.Fatal("valid secret must verify")
	}
	if h.Verify(raw+"x", hash, salt) {
		t.Fatal("tampered secret must not verify")
	}
	if h.Verify("wrong", hash, salt) {
		t.Fatal("wrong secret must not verify")
	}
}

func TestHashNeverStoresPlaintext(t *testing.T) {
	h := NewHasher("")
	raw, _ := GenerateSecret()
	hash, salt, _ := h.Hash(raw)
	if strings.Contains(string(hash), raw) || strings.Contains(string(salt), raw) {
		t.Fatal("hash/salt must not contain the raw secret")
	}
	if len(salt) != 16 {
		t.Fatalf("want 16-byte salt, got %d", len(salt))
	}
}

func TestSecretsUnique(t *testing.T) {
	a, _ := GenerateSecret()
	b, _ := GenerateSecret()
	if a == b {
		t.Fatal("secrets must differ")
	}
	h := NewHasher("p")
	ha, sa, _ := h.Hash(a)
	hb, sb, _ := h.Hash(a)
	if string(ha) == string(hb) {
		t.Fatal("same secret must hash differently (random salt)")
	}
	_ = sa
	_ = sb
}

func TestPepperIsolation(t *testing.T) {
	raw, _ := GenerateSecret()
	h1, h2 := NewHasher("one"), NewHasher("two")
	hash, salt, _ := h1.Hash(raw)
	if h2.Verify(raw, hash, salt) {
		t.Fatal("different pepper must not verify")
	}
}
