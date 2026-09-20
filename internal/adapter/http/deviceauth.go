package http

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
)

type ctxKey string

const (
	deviceKey     ctxKey = "device"
	credentialKey ctxKey = "credential"
)

// Credential token format (documented contract):
//
//	Authorization: Bearer <device-id>.<credential-id>.<secret-hex>
//
// Three dot-separated parts, no ambiguity: UUIDs and hex secrets never
// contain dots. Secrets travel only in the header, never in query params.
// Failures are 401 without enumeration detail. On success the device and
// credential are in context and their IDs join debug logs.
func DeviceAuth(svc auth.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			deviceID, credID, secret, ok := parseBearer(r.Header.Get("Authorization"))
			if !ok {
				WriteError(w, r, apperr.New(apperr.Unauthorized, "missing device credential"))
				return
			}
			dev, cred, err := svc.Authenticate(r.Context(), deviceID, credID, secret)
			if err != nil {
				WriteError(w, r, err)
				return
			}
			// IDs join structured logs at debug; never the secret.
			slog.Default().Debug("device authenticated",
				"request_id", RequestID(r), "device_id", dev.ID,
				"credential_id", cred.ID, "path", r.URL.Path)
			ctx := context.WithValue(r.Context(), deviceKey, dev)
			ctx = context.WithValue(ctx, credentialKey, cred)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// DeviceOf returns the authenticated device from context.
func DeviceOf(r *http.Request) (auth.Device, bool) {
	dev, ok := r.Context().Value(deviceKey).(auth.Device)
	return dev, ok
}

// CredentialOf returns the authenticated credential from context.
func CredentialOf(r *http.Request) (auth.Credential, bool) {
	cred, ok := r.Context().Value(credentialKey).(auth.Credential)
	return cred, ok
}

func parseBearer(h string) (deviceID, credID, secret string, ok bool) {
	h = strings.TrimSpace(h)
	if h == "" {
		return "", "", "", false
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", "", "", false
	}
	segs := strings.Split(strings.TrimSpace(parts[1]), ".")
	if len(segs) != 3 {
		return "", "", "", false
	}
	deviceID, credID, secret = segs[0], segs[1], segs[2]
	if deviceID == "" || credID == "" || secret == "" {
		return "", "", "", false
	}
	// Bounded, charset-safe: UUID text + 64 hex chars, nothing else.
	if len(deviceID) > 64 || len(credID) > 64 || len(secret) > 256 {
		return "", "", "", false
	}
	return deviceID, credID, secret, true
}
