package http

import (
	"net/http"
	"strings"
)

// The Phase 3B single-operator stateless session (DASHBOARD_USERNAME /
// DASHBOARD_PASSWORD_HASH, HMAC cookie) was replaced in Phase 18 R1 by
// server-side human sessions with roles, Store memberships and MFA
// (humanauth.go, ADR-0053). Only the origin comparison remains here.

// sameOrigin compares an Origin/Referer value with the request host.
func sameOrigin(origin string, r *http.Request) bool {
	// Compare scheme://host (no path/query). Missing scheme defaults to the
	// request scheme; ports must match exactly.
	lower := strings.ToLower(origin)
	if i := strings.Index(lower, "://"); i >= 0 {
		lower = lower[i+3:]
	}
	if i := strings.IndexByte(lower, '/'); i >= 0 {
		lower = lower[:i]
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := strings.ToLower(r.Host)
	// Accept "scheme://host" or bare "host" forms.
	return lower == host || lower == scheme+"://"+host
}
