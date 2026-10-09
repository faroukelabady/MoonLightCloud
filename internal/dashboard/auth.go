// Package dashboard owns the Phase 3B operator dashboard backend: dashboard
// read services composing the frozen ReportingService and read-only BFF
// helpers. Business totals always come from Go services reusing
// ReportingService data; the Svelte frontend never computes authoritative
// financial values.
//
// Human authentication moved to internal/humanauth in Phase 18 R1
// (ADR-0053). The Phase 3A REPORTING_API_TOKEN is never exposed to
// browsers.
package dashboard

import (
	"crypto/sha256"
	"fmt"
	"net"
	"strings"

	"golang.org/x/crypto/hkdf"
)

// DeriveKey derives a 32-byte domain-separated key from the server pepper
// (HKDF-SHA256): no extra secret to configure or leak.
func DeriveKey(pepper []byte, info string) ([]byte, error) {
	if len(pepper) == 0 {
		return nil, fmt.Errorf("pepper required for key derivation")
	}
	out := make([]byte, 32)
	kdf := hkdf.New(sha256.New, pepper, nil, []byte(info))
	if _, err := kdf.Read(out); err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}
	return out, nil
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
