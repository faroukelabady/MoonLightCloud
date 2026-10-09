package humanauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2id production parameters (ADR-0053): 64 MiB memory, 3 passes,
// parallelism 2, 16-byte salt from crypto/rand, 32-byte key. Encoded as a
// PHC string carrying its own parameters so they can be raised later: a
// successful login with weaker stored parameters rehashes transparently.
type Argon2Params struct {
	Memory  uint32 // KiB
	Time    uint32
	Threads uint8
	KeyLen  uint32
	SaltLen uint32
}

// CurrentArgon2 is the parameter set new hashes use.
var CurrentArgon2 = Argon2Params{Memory: 64 * 1024, Time: 3, Threads: 2, KeyLen: 32, SaltLen: 16}

// Password policy: length only, no composition theatre. Measured in
// Unicode code points; input is never truncated (over-long is rejected).
const (
	MinPasswordRunes = 12
	MaxPasswordRunes = 256
	MaxPasswordBytes = 1024
)

// ErrWeakPassword rejects a password outside the policy.
var ErrWeakPassword = errors.New("password does not meet the policy")

// ValidatePassword enforces the policy: valid UTF-8, 12..256 code points,
// at most 1024 bytes, not all whitespace.
func ValidatePassword(password string) error {
	if !utf8.ValidString(password) || len(password) > MaxPasswordBytes {
		return ErrWeakPassword
	}
	n := utf8.RuneCountInString(password)
	if n < MinPasswordRunes || n > MaxPasswordRunes || strings.TrimSpace(password) == "" {
		return ErrWeakPassword
	}
	return nil
}

// HashPassword returns an Argon2id PHC string under p.
func HashPassword(password string, p Argon2Params) (string, error) {
	if password == "" {
		return "", ErrWeakPassword
	}
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// parsedHash is a validated PHC string.
type parsedHash struct {
	params Argon2Params
	salt   []byte
	key    []byte
}

// parsePHC validates every field with hard bounds; anything malformed or
// out of range is rejected (no panic, no unbounded work).
func parsePHC(phc string) (parsedHash, bool) {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != fmt.Sprintf("v=%d", argon2.Version) {
		return parsedHash{}, false
	}
	var mem, t, threads uint32
	if n, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &threads); err != nil || n != 3 {
		return parsedHash{}, false
	}
	if parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", mem, t, threads) {
		return parsedHash{}, false
	}
	if mem < 8*1024 || mem > 1<<20 || t < 1 || t > 20 || threads < 1 || threads > 16 {
		return parsedHash{}, false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return parsedHash{}, false
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) < 16 || len(key) > 64 {
		return parsedHash{}, false
	}
	return parsedHash{params: Argon2Params{Memory: mem, Time: t, Threads: uint8(threads), KeyLen: uint32(len(key)), SaltLen: uint32(len(salt))},
		salt: salt, key: key}, true
}

// VerifyPassword checks password against a stored PHC hash in constant
// time. Malformed hashes fail closed.
func VerifyPassword(password, phc string) bool {
	if len(password) > MaxPasswordBytes {
		return false
	}
	h, ok := parsePHC(phc)
	if !ok {
		return false
	}
	got := argon2.IDKey([]byte(password), h.salt, h.params.Time, h.params.Memory, h.params.Threads, h.params.KeyLen)
	return subtle.ConstantTimeCompare(got, h.key) == 1
}

// NeedsRehash reports a stored hash weaker than the current parameters.
func NeedsRehash(phc string, current Argon2Params) bool {
	h, ok := parsePHC(phc)
	if !ok {
		return true
	}
	p := h.params
	return p.Memory < current.Memory || p.Time < current.Time || p.Threads != current.Threads ||
		p.KeyLen < current.KeyLen || p.SaltLen < current.SaltLen
}

// dummyHash equalizes timing for unknown accounts: a login for a
// non-existent user still performs one full Argon2id verification.
var (
	dummyOnce sync.Once
	dummyHash string
)

// burnPasswordCheck performs a verification against the dummy hash (built
// once, lazily, with the current parameters).
func burnPasswordCheck(password string) {
	dummyOnce.Do(func() {
		dummyHash, _ = HashPassword("moonlight-timing-equalizer-not-a-credential", CurrentArgon2)
	})
	_ = VerifyPassword(password, dummyHash)
}
