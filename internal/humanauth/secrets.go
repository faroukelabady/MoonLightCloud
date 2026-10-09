package humanauth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 TOTP is defined over HMAC-SHA1; authenticator apps require it.
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP parameters (RFC 6238 defaults understood by every authenticator).
const (
	TOTPPeriod  = 30 * time.Second
	TOTPDigits  = 6
	TOTPWindow  = 1 // accept the adjacent step on each side (clock skew)
	totpSecretN = 20
)

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns 160 random bits (RFC 4226 recommended length).
func NewTOTPSecret() ([]byte, error) {
	secret := make([]byte, totpSecretN)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	return secret, nil
}

// EncodeTOTPSecret renders the secret for manual authenticator entry.
func EncodeTOTPSecret(secret []byte) string { return totpEncoding.EncodeToString(secret) }

// TOTPURI is the otpauth:// provisioning URI.
func TOTPURI(issuer, account string, secret []byte) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", EncodeTOTPSecret(secret))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(TOTPDigits))
	q.Set("period", fmt.Sprint(int(TOTPPeriod/time.Second)))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

// HOTP is RFC 4226 (HMAC-SHA1, dynamic truncation, 6 digits).
func HOTP(secret []byte, counter uint64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1_000_000)
}

// TOTPStep is the RFC 6238 time step of t.
func TOTPStep(t time.Time) uint64 { return uint64(t.Unix()) / uint64(TOTPPeriod/time.Second) }

// VerifyTOTP checks code against the steps around now and returns the
// matched step. A step <= lastUsedStep is a replay and never matches.
func VerifyTOTP(secret []byte, code string, now time.Time, lastUsedStep uint64) (uint64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != TOTPDigits {
		return 0, false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	current := TOTPStep(now)
	matched, ok := uint64(0), false
	for delta := -TOTPWindow; delta <= TOTPWindow; delta++ {
		step := int64(current) + int64(delta)
		if step < 0 || uint64(step) <= lastUsedStep {
			continue
		}
		if hmac.Equal([]byte(HOTP(secret, uint64(step))), []byte(code)) && !ok {
			matched, ok = uint64(step), true
		}
	}
	return matched, ok
}

// SecretBox seals TOTP secrets with AES-256-GCM under a key that lives
// outside the database (AUTH_MFA_ENCRYPTION_KEY). The user ID is bound as
// associated data, so a ciphertext copied to another account fails.
type SecretBox struct {
	aead    cipher.AEAD
	version int
}

// ErrMFAKey rejects a missing or malformed encryption key.
var ErrMFAKey = errors.New("AUTH_MFA_ENCRYPTION_KEY must be 32 bytes, base64 encoded")

// ErrSecretTampered rejects a ciphertext that fails authentication.
var ErrSecretTampered = errors.New("mfa secret failed authentication")

// NewSecretBox parses a base64 (std or url) 32-byte key.
func NewSecretBox(encodedKey string) (*SecretBox, error) {
	encodedKey = strings.TrimSpace(encodedKey)
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil {
		key, err = base64.RawURLEncoding.DecodeString(encodedKey)
	}
	if err != nil || len(key) != 32 {
		return nil, ErrMFAKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrMFAKey
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ErrMFAKey
	}
	return &SecretBox{aead: aead, version: 1}, nil
}

// KeyVersion is the stored key version.
func (b *SecretBox) KeyVersion() int { return b.version }

func aad(userID string) []byte { return []byte("moonlight-cloud/mfa-secret/v1:" + userID) }

// Seal encrypts secret for userID (nonce || ciphertext || tag).
func (b *SecretBox) Seal(userID string, secret []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return b.aead.Seal(nonce, nonce, secret, aad(userID)), nil
}

// Open decrypts and authenticates a sealed secret.
func (b *SecretBox) Open(userID string, sealed []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n+b.aead.Overhead() {
		return nil, ErrSecretTampered
	}
	plain, err := b.aead.Open(nil, sealed[:n], sealed[n:], aad(userID))
	if err != nil {
		return nil, ErrSecretTampered
	}
	return plain, nil
}

// NewToken returns a 256-bit random bearer value (base64url) and its
// SHA-256 digest; only the digest is ever stored.
func NewToken() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	return token, HashToken(token), nil
}

// HashToken digests a bearer value for lookup.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Recovery codes: 10 codes of 80 random bits each, shown once.
const RecoveryCodeCount = 10

var recoveryEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewRecoveryCodes returns display codes (XXXX-XXXX-XXXX-XXXX) and digests.
func NewRecoveryCodes() ([]string, [][]byte, error) {
	codes := make([]string, 0, RecoveryCodeCount)
	hashes := make([][]byte, 0, RecoveryCodeCount)
	for i := 0; i < RecoveryCodeCount; i++ {
		raw := make([]byte, 10)
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, err
		}
		plain := recoveryEncoding.EncodeToString(raw) // 16 chars
		codes = append(codes, plain[0:4]+"-"+plain[4:8]+"-"+plain[8:12]+"-"+plain[12:16])
		hashes = append(hashes, HashRecoveryCode(plain))
	}
	return codes, hashes, nil
}

// NormalizeRecoveryCode removes separators/whitespace and upper-cases.
func NormalizeRecoveryCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	return strings.NewReplacer("-", "", " ", "").Replace(code)
}

// HashRecoveryCode digests a normalized recovery code.
func HashRecoveryCode(code string) []byte {
	sum := sha256.Sum256([]byte("moonlight-cloud/recovery-code/v1:" + NormalizeRecoveryCode(code)))
	return sum[:]
}
