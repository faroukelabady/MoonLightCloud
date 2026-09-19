package http

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/MoonLightSoftware/MoonLightCloud/internal/apperr"
	"github.com/MoonLightSoftware/MoonLightCloud/internal/auth"
)

type deviceKey struct{}

// DeviceAuth authenticates `Authorization: Bearer <device_id>.<raw_secret>`.
// Secrets never appear in query params. Failures are 401 without enumeration
// detail. On success the device is in context and device_id joins logs.
func DeviceAuth(svc auth.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, secret, ok := parseBearer(r.Header.Get("Authorization"))
			if !ok {
				WriteError(w, r, apperr.New(apperr.Unauthorized, "missing device credential"))
				return
			}
			dev, err := svc.Authenticate(r.Context(), id, secret)
			if err != nil {
				WriteError(w, r, err)
				return
			}
			// device_id joins structured logs (debug; access log stays the
			// single info line per request). Never the secret.
			slog.Default().Debug("device authenticated",
				"request_id", RequestID(r), "device_id", dev.ID, "path", r.URL.Path)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), deviceKey{}, dev)))
		})
	}
}

// DeviceOf returns the authenticated device from context.
func DeviceOf(r *http.Request) (auth.Device, bool) {
	dev, ok := r.Context().Value(deviceKey{}).(auth.Device)
	return dev, ok
}

func parseBearer(h string) (id, secret string, ok bool) {
	h = strings.TrimSpace(h)
	if h == "" {
		return "", "", false
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", "", false
	}
	token := strings.TrimSpace(parts[1])
	dot := strings.LastIndex(token, ".")
	if dot <= 0 || dot == len(token)-1 {
		return "", "", false
	}
	id, secret = token[:dot], token[dot+1:]
	// Bounded, charset-safe: UUID text + 64 hex chars, nothing else.
	if len(id) > 64 || len(secret) > 256 {
		return "", "", false
	}
	return id, secret, true
}
