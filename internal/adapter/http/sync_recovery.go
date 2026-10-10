package http

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/syncrecovery"
)

// SyncRecoveryHandlers serves the Phase 20 R2 Cloud-restore recovery
// protocol over the EXISTING authenticated device control channel.
//
// Every route requires a device credential (same middleware as sync
// and device control). There is no anonymous endpoint and no
// cross-device access: the device identity comes from the middleware,
// never from the request body.
type SyncRecoveryHandlers struct {
	Svc *syncrecovery.Service
	Log interface {
		Info(string, ...any)
	}
}

// maxRecoveryBody bounds every recovery request body.
const maxRecoveryBody = 256 * 1024

// RecoveryStatus serves GET /api/v1/device-control/recovery/status.
// The device learns whether reconciliation is required and why, plus
// how many devices of its Store are still unresolved.
func (h *SyncRecoveryHandlers) RecoveryStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	dev, ok := DeviceOf(r)
	if !ok {
		WriteError(w, r, apperr.New(apperr.Unauthorized, "missing device credential"))
		return
	}
	report, err := h.Svc.Status(r.Context(), dev.ID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// RecoveryVerify serves POST /api/v1/device-control/recovery/verify.
// The device posts one bounded page of retained event identities; the
// response says which Cloud is missing. Identity is (event_id,
// payload_hash) — never a count comparison.
func (h *SyncRecoveryHandlers) RecoveryVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	dev, ok := DeviceOf(r)
	if !ok {
		WriteError(w, r, apperr.New(apperr.Unauthorized, "missing device credential"))
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRecoveryBody))
	if err != nil {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "invalid recovery verification request"))
		return
	}
	var req syncrecovery.VerifyRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "invalid recovery verification request"))
		return
	}
	resp, err := h.Svc.Verify(r.Context(), dev.ID, req)
	if err != nil {
		WriteError(w, r, apperr.New(apperr.InvalidInput, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// RecoveryComplete serves POST /api/v1/device-control/recovery/complete.
// Clearing the requirement is per-device and durable: a Store is never
// marked reconciled while another device is still outstanding.
func (h *SyncRecoveryHandlers) RecoveryComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	dev, ok := DeviceOf(r)
	if !ok {
		WriteError(w, r, apperr.New(apperr.Unauthorized, "missing device credential"))
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxRecoveryBody))
	if err != nil {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "invalid recovery completion request"))
		return
	}
	var req struct {
		// LastMissing is how many identities the final verification
		// pass reported missing. Must be zero. The server's own
		// coverage ledger is the real completeness proof - a client
		// claim alone can never clear the flag.
		LastMissing int `json:"last_missing"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "invalid recovery completion request"))
		return
	}
	if err := h.Svc.Complete(r.Context(), dev.ID, req.LastMissing); err != nil {
		WriteError(w, r, apperr.New(apperr.InvalidInput, err.Error()))
		return
	}
	h.Log.Info("sync recovery completed", "device", dev.ID)
	writeJSON(w, http.StatusOK, map[string]any{"required": false})
}
