package http

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/fleetupdate"
)

// UpdateHandlers serves the Phase 18 release registry, fleet rollout
// control (operator session) and the device update channel (device
// credential). Device identity always comes from authentication; no
// endpoint accepts a command, script, path or executable.
type UpdateHandlers struct {
	Svc *fleetupdate.Service
	Now func() time.Time
}

const maxReleaseImportBytes = 160 * 1024

func (h *UpdateHandlers) now() time.Time {
	if h.Now != nil {
		return h.Now().UTC()
	}
	return time.Now().UTC()
}

// operatorOf is the authenticated human's login, recorded as the actor of
// operator actions. Every caller is behind HumanAuth.Guard.
func operatorOf(r *http.Request) string {
	if p, ok := PrincipalOf(r); ok && p.Login != "" {
		return p.Login
	}
	return "unknown-operator"
}

func decodeStrictBody(r *http.Request, limit int64, out any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return apperr.New(apperr.InvalidInput, "request body too large")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return apperr.New(apperr.InvalidInput, "invalid request body")
	}
	if dec.More() {
		return apperr.New(apperr.InvalidInput, "invalid request body")
	}
	return nil
}

// ImportRelease serves POST /api/v1/dashboard/releases. The body is the
// signed envelope exactly as produced by release tooling plus operational
// artifact locations; identity, hashes and signature are never typed.
func (h *UpdateHandlers) ImportRelease(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Envelope     json.RawMessage   `json:"envelope"`
		ArtifactURLs map[string]string `json:"artifact_urls"`
	}
	if err := decodeStrictBody(r, maxReleaseImportBytes, &req); err != nil {
		WriteError(w, r, err)
		return
	}
	view, created, err := h.Svc.ImportRelease(r.Context(), operatorOf(r), compactJSON(req.Envelope), req.ArtifactURLs)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, view)
}

// compactJSON returns the envelope bytes; an envelope supplied as a JSON
// string (the file's literal content) is unwrapped.
func compactJSON(raw json.RawMessage) []byte {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []byte(s)
	}
	return raw
}

// ListReleases serves GET /api/v1/dashboard/releases.
func (h *UpdateHandlers) ListReleases(w http.ResponseWriter, r *http.Request) {
	before, _ := strconv.ParseInt(r.URL.Query().Get("before_sequence"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := h.Svc.ListReleases(r.Context(), before, limit)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"releases": rows})
}

// GetRelease serves GET /api/v1/dashboard/releases/{id}.
func (h *UpdateHandlers) GetRelease(w http.ResponseWriter, r *http.Request) {
	view, err := h.Svc.GetRelease(r.Context(), r.PathValue("id"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// SetReleaseStatus serves POST /api/v1/dashboard/releases/{id}/status.
func (h *UpdateHandlers) SetReleaseStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Status string `json:"status"`
	}
	if err := decodeStrictBody(r, 1024, &req); err != nil {
		WriteError(w, r, err)
		return
	}
	view, err := h.Svc.SetReleaseStatus(r.Context(), operatorOf(r), r.PathValue("id"), req.Status)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// CreateRollout serves POST /api/v1/dashboard/rollouts.
func (h *UpdateHandlers) CreateRollout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ReleaseID  string     `json:"release_id"`
		Scope      string     `json:"scope"`
		StoreID    string     `json:"store_id"`
		DeviceID   string     `json:"device_id"`
		Mode       string     `json:"mode"`
		Percentage int        `json:"percentage"`
		NotBefore  *time.Time `json:"not_before"`
	}
	if err := decodeStrictBody(r, 4096, &req); err != nil {
		WriteError(w, r, err)
		return
	}
	// ADR-0053: ALL-scope rollouts need an all-Stores human; Store and
	// device rollouts need membership in the named Store (the service
	// also proves the device is bound to it).
	if req.Scope == fleetupdate.ScopeAll {
		if !RequireAllStores(w, r) {
			return
		}
	} else if !RequireStore(w, r, req.StoreID) {
		return
	}
	view, err := h.Svc.CreateRollout(r.Context(), operatorOf(r), fleetupdate.RolloutRequest{ReleaseID: req.ReleaseID,
		Scope: req.Scope, StoreID: req.StoreID, DeviceID: req.DeviceID, Mode: req.Mode, Percentage: req.Percentage, NotBefore: req.NotBefore})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

// RolloutAction serves POST /api/v1/dashboard/rollouts/{id}/{action}.
func (h *UpdateHandlers) RolloutAction(w http.ResponseWriter, r *http.Request) {
	id, actor := r.PathValue("id"), operatorOf(r)
	if !h.canSeeRollout(w, r, id) {
		return
	}
	var (
		view fleetupdate.RolloutView
		err  error
	)
	switch r.PathValue("action") {
	case "start":
		view, err = h.Svc.StartRollout(r.Context(), actor, id)
	case "pause":
		view, err = h.Svc.PauseRollout(r.Context(), actor, id)
	case "resume":
		view, err = h.Svc.ResumeRollout(r.Context(), actor, id)
	case "cancel":
		view, err = h.Svc.CancelRollout(r.Context(), actor, id)
	case "percentage":
		var req struct {
			Percentage int `json:"percentage"`
		}
		if derr := decodeStrictBody(r, 256, &req); derr != nil {
			WriteError(w, r, derr)
			return
		}
		view, err = h.Svc.IncreasePercentage(r.Context(), actor, id, req.Percentage)
	default:
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// ListRollouts serves GET /api/v1/dashboard/rollouts.
func (h *UpdateHandlers) ListRollouts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	rows, next, err := h.Svc.ListRollouts(r.Context(), q.Get("store_id"), q.Get("cursor"), limit)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rollouts": rows, "next_cursor": next})
}

// GetRollout serves GET /api/v1/dashboard/rollouts/{id}.
func (h *UpdateHandlers) GetRollout(w http.ResponseWriter, r *http.Request) {
	if !h.canSeeRollout(w, r, r.PathValue("id")) {
		return
	}
	view, err := h.Svc.GetRollout(r.Context(), r.PathValue("id"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// ListTargets serves GET /api/v1/dashboard/rollouts/{id}/targets.
func (h *UpdateHandlers) ListTargets(w http.ResponseWriter, r *http.Request) {
	if !h.canSeeRollout(w, r, r.PathValue("id")) {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	rows, next, err := h.Svc.ListTargets(r.Context(), r.PathValue("id"), q.Get("store_id"), q.Get("cursor"), limit)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": rows, "next_cursor": next})
}

// TargetHistory serves GET /api/v1/dashboard/update-targets/{id}/history.
func (h *UpdateHandlers) TargetHistory(w http.ResponseWriter, r *http.Request) {
	rows, err := h.Svc.TargetHistory(r.Context(), r.PathValue("id"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": rows})
}

// Fleet serves GET /api/v1/dashboard/fleet.
func (h *UpdateHandlers) Fleet(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	rows, next, err := h.Svc.Fleet(r.Context(), q.Get("store_id"), q.Get("cursor"), limit)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": rows, "next_cursor": next})
}

// Audit serves GET /api/v1/dashboard/update-audit.
func (h *UpdateHandlers) Audit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before_id"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	rows, err := h.Svc.Audit(r.Context(), q.Get("store_id"), before, limit)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": rows})
}

// DeviceStatus serves POST /api/v1/device-control/update/status.
func (h *UpdateHandlers) DeviceStatus(w http.ResponseWriter, r *http.Request) {
	dev, ok := DeviceOf(r)
	if !ok {
		WriteError(w, r, apperr.New(apperr.Unauthorized, "device credential required"))
		return
	}
	var req fleetupdate.StatusReport
	if err := decodeStrictBody(r, 4096, &req); err != nil {
		WriteError(w, r, err)
		return
	}
	if err := h.Svc.RecordStatus(r.Context(), dev.ID, req); err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"server_time": h.now()})
}

// DeviceCommand serves GET /api/v1/device-control/update/command.
func (h *UpdateHandlers) DeviceCommand(w http.ResponseWriter, r *http.Request) {
	dev, ok := DeviceOf(r)
	if !ok {
		WriteError(w, r, apperr.New(apperr.Unauthorized, "device credential required"))
		return
	}
	cmd, err := h.Svc.PollCommand(r.Context(), dev.ID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"server_time": h.now(), "command": cmd})
}

// DeviceReport serves POST /api/v1/device-control/update/targets/{id}/report.
func (h *UpdateHandlers) DeviceReport(w http.ResponseWriter, r *http.Request) {
	dev, ok := DeviceOf(r)
	if !ok {
		WriteError(w, r, apperr.New(apperr.Unauthorized, "device credential required"))
		return
	}
	var req fleetupdate.TargetReport
	if err := decodeStrictBody(r, 4096, &req); err != nil {
		WriteError(w, r, err)
		return
	}
	ack, err := h.Svc.ReportTarget(r.Context(), dev.ID, r.PathValue("id"), req)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ack)
}

// canSeeRollout (ADR-0053) resolves a rollout's owning Store: all-Stores
// humans see every rollout; a Store-restricted human sees only Store/device
// rollouts of its own Stores (an ALL-scope rollout spans other Stores).
// Unknown rollouts answer 404 for everyone (no enumeration).
func (h *UpdateHandlers) canSeeRollout(w http.ResponseWriter, r *http.Request, id string) bool {
	p, _ := PrincipalOf(r)
	if p.AllStores {
		return true
	}
	view, err := h.Svc.GetRollout(r.Context(), id)
	if err != nil {
		WriteError(w, r, err)
		return false
	}
	if view.Scope == fleetupdate.ScopeAll || !p.CanAccessStore(view.StoreID) {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return false
	}
	return true
}
