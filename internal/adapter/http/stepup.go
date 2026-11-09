package http

import (
	"errors"
	"net/http"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalogadmin"
	"github.com/faroukelabady/MoonLightCloud/internal/humanauth"
)

// requireFreshStepUp is shared by sensitive catalog and device actions.
// Confirmation consumes one authenticator step; it is never a reusable flag.
func requireFreshStepUp(w http.ResponseWriter, r *http.Request, verifier StepUpVerifier, clientIP func(*http.Request) string, code, commandType string) bool {
	code = strings.TrimSpace(code)
	if code == "" {
		WriteError(w, r, apperr.New(apperr.Forbidden, catalogadmin.CodeMFARequired))
		return false
	}
	a, ok := AuthOf(r)
	if !ok || verifier == nil {
		WriteError(w, r, apperr.New(apperr.Forbidden, catalogadmin.CodeMFARequired))
		return false
	}
	ip := ""
	if clientIP != nil {
		ip = clientIP(r)
	}
	switch err := verifier.ConfirmStepUp(r.Context(), a, code, ip, commandType); {
	case err == nil:
		return true
	case errors.Is(err, humanauth.ErrThrottled):
		w.Header().Set("Retry-After", "60")
		WriteError(w, r, apperr.New(apperr.TooManyRequests, "RATE_LIMITED"))
	case errors.Is(err, humanauth.ErrInvalidMFACode), errors.Is(err, humanauth.ErrStage):
		WriteError(w, r, apperr.New(apperr.Forbidden, catalogadmin.CodeMFAInvalid))
	default:
		WriteError(w, r, apperr.Wrap(apperr.Internal, "internal error", err))
	}
	return false
}
