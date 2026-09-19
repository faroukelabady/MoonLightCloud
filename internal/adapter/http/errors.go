// Package http is the inbound adapter: routes, middleware, handlers.
// It maps apperr codes to the stable error envelope and never exposes
// SQL, stack traces, paths, secrets, or raw pgx errors.
package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/MoonLightSoftware/MoonLightCloud/internal/apperr"
)

// Envelope is the stable error contract.
type Envelope struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody carries code + safe message.
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// WriteError maps classified errors to status + envelope. Unknown errors
// become INTERNAL with a generic message; details stay in server logs.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	code := "INTERNAL"
	status := http.StatusInternalServerError
	msg := "internal error"
	var ae *apperr.Error
	if errors.As(err, &ae) {
		code = ae.Code()
		msg = ae.Message
		switch ae.Kind {
		case apperr.InvalidInput:
			status = http.StatusBadRequest
		case apperr.NotFound:
			status = http.StatusNotFound
		case apperr.Unauthorized:
			status = http.StatusUnauthorized
		case apperr.Forbidden:
			status = http.StatusForbidden
		case apperr.Conflict:
			status = http.StatusConflict
		case apperr.Unavailable:
			status = http.StatusServiceUnavailable
		}
		if ae.Kind == apperr.Internal && ae.Err != nil {
			slog.Default().Error("request failed",
				"request_id", RequestID(r), "path", r.URL.Path, "err", ae.Err.Error())
		}
	}
	writeJSON(w, status, Envelope{ErrorBody{Code: code, Message: msg}})
}

// NotFound and MethodNotAllowed keep the envelope on unmatched routes.
func NotFound(w http.ResponseWriter, r *http.Request) {
	WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
}
