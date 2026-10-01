package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"unicode/utf8"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/store"
)

// StoreRegistrar is the narrow registration seam for the HTTP layer,
// implemented by the postgres adapter.
type StoreRegistrar interface {
	RegisterStore(ctx context.Context, deviceID string, request store.RegistrationRequest) (store.RegistrationResult, error)
}

// StoreRegistration binds the authenticated device to the presented
// Store (Phase 9A). Requires JSON content type; body is tightly bounded
// (identity + short metadata, never blobs). Outcomes: 200 bound,
// 400 invalid, 401 unauthenticated, 409 binding conflict, 503 transient.
func StoreRegistration(registrar StoreRegistrar) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dev, ok := DeviceOf(r)
		if !ok {
			WriteError(w, r, apperr.New(apperr.Unauthorized, "missing device credential"))
			return
		}
		if r.Method != http.MethodPost {
			WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
			return
		}
		if ct := r.Header.Get("Content-Type"); !isJSON(ct) {
			WriteError(w, r, apperr.New(apperr.UnsupportedMedia, "content type must be application/json"))
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64*1024))
		if err != nil {
			WriteError(w, r, apperr.New(apperr.TooLarge, "registration body exceeds the size limit"))
			return
		}
		if !utf8.Valid(body) {
			WriteError(w, r, apperr.New(apperr.InvalidInput, "registration body must be valid UTF-8"))
			return
		}
		var request store.RegistrationRequest
		dec := json.NewDecoder(bytes.NewReader(body))
		if err := dec.Decode(&request); err != nil {
			WriteError(w, r, apperr.New(apperr.InvalidInput, "registration body must be a JSON object"))
			return
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			WriteError(w, r, apperr.New(apperr.InvalidInput, "registration body must contain one JSON object"))
			return
		}
		result, err := registrar.RegisterStore(r.Context(), dev.ID, request)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		slog.Default().Info("store registration",
			"request_id", RequestID(r), "device_id", dev.ID, "store_id", result.StoreID)
		writeJSON(w, http.StatusOK, map[string]any{
			"store_id":     result.StoreID,
			"display_name": result.DisplayName,
			"timezone":     result.Timezone,
		})
	}
}
