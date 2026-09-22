package http

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// ReportAuth guards report-read endpoints with the temporary
// REPORTING_API_TOKEN (Bearer). Device sync credentials are never valid
// here: devices are not human reporting clients. The token is compared in
// constant time and never logged. Empty configured token means development
// open mode (warned once at startup by the wiring layer).
func ReportAuth(token string) func(http.Handler) http.Handler {
	expected := []byte(token)
	open := len(expected) == 0
	var warned atomic.Bool
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if open {
				if warned.CompareAndSwap(false, true) {
					slog.Default().Warn("reporting API unauthenticated (development only)")
				}
				next.ServeHTTP(w, r)
				return
			}
			got, ok := parseReportBearer(r.Header.Get("Authorization"))
			if !ok || subtle.ConstantTimeCompare([]byte(got), expected) != 1 {
				WriteError(w, r, apperr.New(apperr.Unauthorized, "invalid reporting credential"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func parseReportBearer(h string) (string, bool) {
	h = strings.TrimSpace(h)
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", false
	}
	token := strings.TrimSpace(parts[1])
	if token == "" || len(token) > 512 {
		return "", false
	}
	return token, true
}
