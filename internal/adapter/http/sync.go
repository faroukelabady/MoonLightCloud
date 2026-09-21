package http

import (
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/sync"
)

// SyncBatch ingests a validated batch for the authenticated device.
// Requires Content-Type: application/json. Body is bounded by the sync
// limit; envelope validation decides 400/403/409/413/422. onIngested wakes
// the projector (non-blocking); durability never depends on the wake.
func SyncBatch(svc sync.Service, onIngested func()) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dev, ok := DeviceOf(r)
		if !ok {
			WriteError(w, r, apperr.New(apperr.Unauthorized, "missing device credential"))
			return
		}
		cred, ok := CredentialOf(r)
		if !ok {
			WriteError(w, r, apperr.New(apperr.Unauthorized, "missing device credential"))
			return
		}
		if ct := r.Header.Get("Content-Type"); !isJSON(ct) {
			WriteError(w, r, apperr.New(apperr.UnsupportedMedia, "content type must be application/json"))
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, sync.MaxSyncBodyBytes))
		if err != nil {
			WriteError(w, r, apperr.New(apperr.TooLarge, "batch body exceeds the size limit"))
			return
		}
		res, err := svc.Ingest(r.Context(), dev.ID, cred.ID, body)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		if onIngested != nil {
			onIngested()
		}
		// Structured outcome fields; never full payloads (commercially
		// sensitive future data stays out of logs).
		slog.Default().Info("sync batch ingested",
			"request_id", RequestID(r), "device_id", dev.ID,
			"event_count", len(res.Events),
			"accepted", countStatus(res, sync.StatusAccepted),
			"duplicates", countStatus(res, sync.StatusAlreadyAccepted),
		)
		writeJSON(w, http.StatusOK, res)
	}
}

// SyncCapabilities reports protocol support for desktop compatibility.
func SyncCapabilities(svc sync.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, svc.Capabilities())
	}
}

func isJSON(ct string) bool {
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = ct[:i]
	}
	return strings.TrimSpace(strings.ToLower(ct)) == "application/json"
}

func countStatus(res sync.BatchResult, status string) int {
	n := 0
	for _, e := range res.Events {
		if e.Status == status {
			n++
		}
	}
	return n
}
