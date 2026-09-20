package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
)

// Final construction (ADR-0015, threat model: docs/security/threat-model.md).
// Device secrets are 256-bit crypto-random tokens, not human passwords, so
// slow password KDFs add latency with no gain at 256-bit entropy.
//
//	raw secret:  32 random bytes, shown hex-encoded (64 chars)
//	salt:        16 random bytes per credential (not secret)
//	pepper:      32 bytes server-side HMAC key, config only, never in DB
//	verifier v1: HMAC-SHA256(key=pepper, message=salt || hex_secret)
//
// Raw secrets, peppers, and tokens never persist and never log.
// Verification is constant-time. A stolen database alone yields nothing
// usable: every verifier needs the pepper to test candidates.
//
// Legacy verifier v0 (Phase 1A ambiguity, pre-production only): rows
// migrated by 00002 keep their original bytes and verify with the exact
// Phase-1A construction — SHA-256(salt||secret) without pepper, or
// HMAC-SHA256(key=pepper_string, salt||secret) with the then-configured
// pepper. See VerifyLegacyV0. No new v0 row is ever created.
const (
	secretBytes = 32
	saltBytes   = 16
	pepperBytes = 32
)

// Hasher hashes and verifies device secrets with the current pepper.
type Hasher struct {
	pepper []byte
}

// NewHasher builds a v1 hasher. The pepper must be exactly 32 bytes;
// length is enforced so misconfiguration fails fast, never silently weak.
func NewHasher(pepper []byte) (Hasher, error) {
	if len(pepper) != pepperBytes {
		return Hasher{}, fmt.Errorf("pepper must be %d bytes, got %d", pepperBytes, len(pepper))
	}
	return Hasher{pepper: pepper}, nil
}

// GenerateSecret returns a fresh 256-bit secret hex-encoded (64 chars).
func GenerateSecret() (string, error) {
	var b [secretBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// Hash binds a raw hex secret to a fresh random salt: HMAC-SHA256.
func (h Hasher) Hash(rawSecret string) (verifier, salt []byte, err error) {
	var s [saltBytes]byte
	if _, err := rand.Read(s[:]); err != nil {
		return nil, nil, fmt.Errorf("generate salt: %w", err)
	}
	return h.hashWithSalt(rawSecret, s[:]), s[:], nil
}

// Verify recomputes the v1 verifier in constant time.
func (h Hasher) Verify(rawSecret string, verifier, salt []byte) bool {
	candidate := h.hashWithSalt(rawSecret, salt)
	if len(candidate) != len(verifier) {
		return false
	}
	return subtle.ConstantTimeCompare(candidate, verifier) == 1
}

func (h Hasher) hashWithSalt(rawSecret string, salt []byte) []byte {
	mac := hmac.New(sha256.New, h.pepper)
	mac.Write(salt)
	mac.Write([]byte(rawSecret))
	return mac.Sum(nil)
}

// VerifyLegacyV0 verifies a Phase-1A verifier with the exact legacy
// construction: plain SHA-256 when pepperString is empty, keyed HMAC with
// the raw pepper string otherwise. Constant-time. Used only for rows
// migrated with verifier_version=0; never for new credentials.
func VerifyLegacyV0(rawSecret string, verifier, salt []byte, pepperString string) bool {
	var candidate []byte
	if pepperString == "" {
		sum := sha256.Sum256(append(append([]byte{}, salt...), rawSecret...))
		candidate = sum[:]
	} else {
		mac := hmac.New(sha256.New, []byte(pepperString))
		mac.Write(salt)
		mac.Write([]byte(rawSecret))
		candidate = mac.Sum(nil)
	}
	if len(candidate) != len(verifier) {
		return false
	}
	return subtle.ConstantTimeCompare(candidate, verifier) == 1
}
