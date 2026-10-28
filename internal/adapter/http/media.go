package http

import (
	"io"
	"log/slog"
	"mime"
	"net/http"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/media"
)

// MediaUpload stores one content-addressed product image for the
// authenticated device (ADR-0056): 201 stored, 200 already_stored.
func MediaUpload(svc media.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dev, ok := DeviceOf(r)
		if !ok {
			WriteError(w, r, apperr.New(apperr.Unauthorized, "missing device credential"))
			return
		}
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			WriteError(w, r, apperr.New(apperr.UnsupportedMedia, "content type must be image/jpeg or image/png"))
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, catalog.MaxMediaBytes))
		if err != nil {
			WriteError(w, r, apperr.New(apperr.TooLarge, "image exceeds the size limit"))
			return
		}
		hash := r.PathValue("sha256")
		created, blob, err := svc.Upload(r.Context(), dev.ID, hash, contentType, body)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		// Hash and size only: image bytes never reach logs.
		slog.Default().Info("media uploaded", "request_id", RequestID(r), "device_id", dev.ID,
			"sha256", hash, "bytes", blob.ByteSize, "created", created)
		status, outcome := http.StatusOK, "already_stored"
		if created {
			status, outcome = http.StatusCreated, "stored"
		}
		writeJSON(w, status, map[string]any{"sha256": hash, "status": outcome})
	}
}
