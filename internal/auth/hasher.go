package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
)

// Rationale (threat model: docs/security/threat-model.md): device secrets are
// 256-bit crypto-random tokens, not human passwords. Slow password hashes
// (bcrypt/argon2) add nothing against a 256-bit secret and cost latency on
// every API call. The correct design is a salted keyed hash:
// SHA-256(salt || secret || pepper) with a per-device 128-bit salt stored
// beside the hash and an optional server-side pepper from DEVICE_SECRET_PEPPER.
// A stolen database alone does not yield usable secrets; verification uses
// constant-time comparison. Raw secrets never persist, never log.
const (
	secretBytes = 32
	saltBytes   = 16
)

// Hasher hashes and verifies high-entropy device secrets.
type Hasher struct {
	pepper string
}

// NewHasher builds a hasher. Pepper may be empty in development; production
// config validation requires it.
func NewHasher(pepper string) Hasher { return Hasher{pepper: pepper} }

// GenerateSecret returns a fresh 256-bit secret hex-encoded (64 chars).
func GenerateSecret() (string, error) {
	var b [secretBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// Hash binds a raw secret to a fresh random salt.
func (h Hasher) Hash(rawSecret string) (hash, salt []byte, err error) {
	var s [saltBytes]byte
	if _, err := rand.Read(s[:]); err != nil {
		return nil, nil, fmt.Errorf("generate salt: %w", err)
	}
	return h.hashWithSalt(rawSecret, s[:]), s[:], nil
}

// Verify recomputes the hash in constant time.
func (h Hasher) Verify(rawSecret string, hash, salt []byte) bool {
	candidate := h.hashWithSalt(rawSecret, salt)
	if len(candidate) != len(hash) {
		return false
	}
	return subtle.ConstantTimeCompare(candidate, hash) == 1
}

func (h Hasher) hashWithSalt(rawSecret string, salt []byte) []byte {
	if h.pepper == "" {
		sum := sha256.Sum256(append(append([]byte{}, salt...), rawSecret...))
		return sum[:]
	}
	mac := hmac.New(sha256.New, []byte(h.pepper))
	mac.Write(salt)
	mac.Write([]byte(rawSecret))
	return mac.Sum(nil)
}
