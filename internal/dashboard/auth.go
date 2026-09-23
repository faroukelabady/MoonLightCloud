// Package dashboard owns the Phase 3B operator dashboard backend: human
// session authentication, dashboard read services composing the frozen
// ReportingService, and read-only BFF helpers. Business totals always come
// from Go services reusing ReportingService data; the Svelte frontend never
// computes authoritative financial values.
//
// The Phase 3A REPORTING_API_TOKEN is never exposed here: browsers
// authenticate with a server-managed HttpOnly session cookie only.
package dashboard

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/hkdf"
)

// Session and credential policy (documented, minimal, no IAM platform).
const (
	// DefaultSessionTTL bounds operator sessions.
	DefaultSessionTTL = 12 * time.Hour
	// MinSessionTTL / MaxSessionTTL bound configuration.
	MinSessionTTL = 15 * time.Minute
	MaxSessionTTL = 7 * 24 * time.Hour
	// LoginMaxFailures / LoginWindow define the process-local login rate
	// limit per client IP. Excess yields 429, never account detail.
	LoginMaxFailures = 10
	LoginWindow      = 5 * time.Minute
	// SessionCookie is the only browser credential. REPORTING_API_TOKEN
	// must never appear in cookies, storage, HTML, JS, or responses.
	SessionCookie = "mlc_dash_session"
)

// Credentials are the configured operator login (username + Argon2id hash).
type Credentials struct {
	Username     string
	PasswordHash string
}

// SessionKeys derives the HMAC session key from the device pepper via HKDF
// (domain-separated): no extra session secret to configure or leak.
func SessionKey(pepper []byte) ([]byte, error) {
	if len(pepper) == 0 {
		return nil, fmt.Errorf("pepper required for session keys")
	}
	out := make([]byte, 32)
	kdf := hkdf.New(sha256.New, pepper, nil, []byte("moonlight-cloud/dashboard-session-v1"))
	if _, err := kdf.Read(out); err != nil {
		return nil, fmt.Errorf("derive session key: %w", err)
	}
	return out, nil
}

// IssueSession mints username|expiry|mac (base64url). Stateless: logout
// clears the cookie; theft window is bounded by the short expiry.
func IssueSession(key []byte, username string, expiry time.Time) (string, error) {
	if len(key) != 32 || username == "" || strings.ContainsAny(username, "|") {
		return "", fmt.Errorf("invalid session parameters")
	}
	payload := username + "|" + strconv.FormatInt(expiry.Unix(), 10)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	token := payload + "|" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString([]byte(token)), nil
}

// VerifySession checks MAC (constant-time), shape, and expiry. Returns the
// username. Generic failure: callers must not distinguish reasons.
func VerifySession(key []byte, token string, now time.Time) (string, error) {
	fail := fmt.Errorf("invalid session")
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", fail
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 || parts[0] == "" {
		return "", fail
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(parts[0] + "|" + parts[1]))
	want, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", fail
	}
	if subtle.ConstantTimeCompare(mac.Sum(nil), want) != 1 {
		return "", fail
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || now.Unix() >= exp {
		return "", fail
	}
	return parts[0], nil
}

// argon2id parameters (OWASP-ish, no exotic tuning).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// HashPassword generates a PHC string ($argon2id$v=19$m=...,t=...,p=...$..$..)
// for provisioning DASHBOARD_PASSWORD_HASH. Never log the password.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("password required")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash)), nil
}

// VerifyPassword checks a password against a PHC hash in constant time.
// Unknown/malformed hashes fail closed (false, never panic).
func VerifyPassword(password, phc string) bool {
	parts := strings.Split(phc, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var mem, t, threads uint32
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &threads); err != nil {
		return false
	}
	if mem == 0 || mem > 1<<22 || t == 0 || t > 100 || threads == 0 || threads > 32 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 || len(salt) > 64 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 || len(want) > 128 {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, t, mem, uint8(threads), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ClientIP resolves the login rate-limit identity. Default: the direct
// TCP peer; forwarded headers are ignored. When the peer matches a trusted
// proxy CIDR, the client is derived from X-Forwarded-For with the standard
// right-to-left algorithm: walk from the rightmost entry (added by the
// closest proxy) leftward, skipping trusted proxies; the first untrusted
// entry is the client. Malformed chains fall back to the peer (never to a
// spoofed value). No Railway or cloud ranges are trusted implicitly.
func ClientIP(remoteAddr, forwardedFor string, trusted []net.IPNet) string {
	peer := remoteAddr
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		peer = host
	}
	peerIP := net.ParseIP(strings.TrimSpace(peer))
	if peerIP == nil || !ipTrusted(peerIP, trusted) {
		return peer
	}
	chain := parseForwardedChain(forwardedFor)
	if len(chain) == 0 {
		return peer
	}
	for i := len(chain) - 1; i >= 0; i-- {
		if !ipTrusted(chain[i], trusted) {
			return chain[i].String()
		}
	}
	return chain[0].String()
}

func ipTrusted(ip net.IP, trusted []net.IPNet) bool {
	for _, cidr := range trusted {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

func parseForwardedChain(header string) []net.IP {
	var out []net.IP
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		// Strip optional port and surrounding quotes/brackets.
		part = strings.Trim(part, "\"'")
		if host, _, err := net.SplitHostPort(part); err == nil {
			part = host
		}
		part = strings.Trim(part, "[]")
		if ip := net.ParseIP(part); ip != nil {
			out = append(out, ip)
		} else {
			return nil // malformed chain: caller falls back to peer
		}
	}
	return out
}

type LoginLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
	max      int
}

// NewLoginLimiter builds the limiter (test seam for counts).
func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{failures: map[string][]time.Time{}, max: 10000}
}

// Blocked reports whether ip exceeded LoginMaxFailures within LoginWindow.
func (l *LoginLimiter) Blocked(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.recentLocked(ip, now)
	return len(recent) >= LoginMaxFailures
}

// RecordFailure notes a failed attempt; RecordSuccess clears the IP.
func (l *LoginLimiter) RecordFailure(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures[ip] = append(l.recentLocked(ip, now), now)
	if len(l.failures) > l.max {
		for k := range l.failures {
			delete(l.failures, k)
			break
		}
	}
}

// RecordSuccess clears failures after a valid login.
func (l *LoginLimiter) RecordSuccess(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, ip)
}

func (l *LoginLimiter) recentLocked(ip string, now time.Time) []time.Time {
	cutoff := now.Add(-LoginWindow)
	kept := l.failures[ip][:0]
	for _, t := range l.failures[ip] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	l.failures[ip] = kept
	return kept
}
