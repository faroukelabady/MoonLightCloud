package http

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalogadmin"
)

// CatalogAdminHandlers serves both planes of Phase 16 catalog control:
//   - dashboard: authenticated operator creates/reads/cancels typed
//     Store-scoped commands (session cookie, never device tokens);
//   - device: authenticated Retail pulls due targets and reports
//     outcomes over its outbound channel (device credential only).
//
// No handler mutates catalog projections or calls commerce providers:
// command creation persists intent; convergence is observed later via
// normal Retail sync events.
type CatalogAdminHandlers struct {
	Svc *catalogadmin.Service
	Log interface {
		Info(string, ...any)
	}
}

func (h *CatalogAdminHandlers) actorOf(r *http.Request) string {
	if user, ok := r.Context().Value(dashboardUserKey).(string); ok && user != "" {
		return user
	}
	return "dashboard-operator"
}

// CreateCommand serves POST /api/v1/dashboard/catalog-admin/commands.
// Typed server-side request models only: no arbitrary type/payload
// endpoint exists anywhere.
func (h *CatalogAdminHandlers) CreateCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64*1024+1))
	if err != nil || int64(len(raw)) > 64*1024 {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "request body too large"))
		return
	}
	var req struct {
		StoreID          string         `json:"store_id"`
		Type             string         `json:"type"`
		EntityID         string         `json:"entity_id"`
		ExpectedRevision int64          `json:"expected_revision"`
		Payload          map[string]any `json:"payload"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "invalid command request"))
		return
	}
	payload, err := json.Marshal(req.Payload)
	if err != nil || len(req.Payload) == 0 {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "invalid command payload"))
		return
	}
	view, err := h.Svc.Create(r.Context(), h.actorOf(r), req.StoreID, req.Type, req.EntityID, req.ExpectedRevision, payload)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.Log.Info("catalog admin command created", "type", req.Type, "store", req.StoreID)
	writeJSON(w, http.StatusOK, view)
}

// GetCommand serves GET /api/v1/dashboard/catalog-admin/commands/{id}?store_id=.
func (h *CatalogAdminHandlers) GetCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	q := r.URL.Query()
	withPayload := q.Get("payload") == "1"
	var view catalogadmin.CommandView
	var err error
	if withPayload {
		view, err = h.Svc.GetWithPayload(r.Context(), q.Get("store_id"), r.PathValue("id"))
	} else {
		view, err = h.Svc.Get(r.Context(), q.Get("store_id"), r.PathValue("id"))
	}
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// ListCommands serves GET /api/v1/dashboard/catalog-admin/commands with
// bounded Store-scoped filters and keyset pagination. The opaque cursor
// continues from the last returned row; concurrent inserts never shift
// already-returned pages (no duplicates, no skips).
func (h *CatalogAdminHandlers) ListCommands(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	q := r.URL.Query()
	limit := atoiBounded(q.Get("limit"), 20, 1, 100)
	views, next, err := h.Svc.List(r.Context(), q.Get("store_id"), q.Get("type"), q.Get("entity_id"), q.Get("status"), limit, q.Get("cursor"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"commands": views, "next_cursor": next})
}

// CancelCommand serves POST /api/v1/dashboard/catalog-admin/commands/{id}/cancel.
func (h *CatalogAdminHandlers) CancelCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	q := r.URL.Query()
	view, err := h.Svc.Cancel(r.Context(), q.Get("store_id"), r.PathValue("id"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// PollCatalogCommands serves GET /api/v1/device-control/catalog-commands.
// The device identity comes from middleware; targets for other Stores
// or revoked devices never deliver here.
func (h *CatalogAdminHandlers) PollCatalogCommands(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	dev, ok := DeviceOf(r)
	if !ok {
		WriteError(w, r, apperr.New(apperr.Unauthorized, "missing device credential"))
		return
	}
	limit := atoiBounded(r.URL.Query().Get("limit"), 10, 1, 50)
	due, err := h.Svc.Poll(r.Context(), dev.ID, limit)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	type wire struct {
		TargetID    string `json:"target_id"`
		CommandID   string `json:"command_id"`
		CommandType string `json:"command_type"`
		Version     int    `json:"version"`
		StoreID     string `json:"store_id"`
		Payload     []byte `json:"payload"`
		PayloadHash string `json:"payload_hash"`
	}
	out := make([]wire, 0, len(due))
	for _, t := range due {
		out = append(out, wire{
			TargetID: t.TargetID, CommandID: t.CommandID,
			CommandType: t.Type, Version: t.Version,
			StoreID: t.StoreID, Payload: t.Payload, PayloadHash: t.PayloadHash,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"commands": out})
}

// ReportCatalogResult serves POST /api/v1/device-control/catalog-commands/{target_id}/result.
// The target is bound server-side to the calling device: wrong-device
// ACKs are rejected and change nothing.
func (h *CatalogAdminHandlers) ReportCatalogResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	dev, ok := DeviceOf(r)
	if !ok {
		WriteError(w, r, apperr.New(apperr.Unauthorized, "missing device credential"))
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4*1024))
	if err != nil {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "invalid result report"))
		return
	}
	var req struct {
		Status       string `json:"status"`
		ResultCode   string `json:"result_code"`
		EntityID     string `json:"entity_id"`
		PreRevision  int64  `json:"pre_revision"`
		PostRevision int64  `json:"post_revision"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "invalid result report"))
		return
	}
	if err := h.Svc.ReportOutcome(r.Context(), dev.ID, r.PathValue("target_id"),
		req.Status, req.ResultCode, req.EntityID, req.PreRevision, req.PostRevision); err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// atoiBounded parses an optional integer query value with defaults.
func atoiBounded(raw string, def, min, max int) int {
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// Retail announces catalog_admin_commands_v1. Old Retail never calls.
// ReportCapabilities serves POST /api/v1/device-control/capabilities:
// Retail announces catalog_admin_commands_v1. Old Retail never calls.
func (h *CatalogAdminHandlers) ReportCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	dev, ok := DeviceOf(r)
	if !ok {
		WriteError(w, r, apperr.New(apperr.Unauthorized, "missing device credential"))
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4*1024))
	if err != nil {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "invalid capabilities"))
		return
	}
	var req struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &req); err != nil || len(req.Capabilities) > 16 {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "invalid capabilities"))
		return
	}
	capable, err := h.Svc.ReportCapabilities(r.Context(), dev.ID, req.Capabilities)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"catalog_admin_commands_v1": capable})
}

// AdminConfigurations serves GET /api/v1/dashboard/catalog-admin/products/{id}/configurations.
func (h *CatalogAdminHandlers) AdminConfigurations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	rows, err := h.Svc.AdminConfigurations(r.Context(), r.URL.Query().Get("store_id"), r.PathValue("id"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"configurations": rows})
}

// AdminProducts serves GET /api/v1/dashboard/catalog-admin/products.
func (h *CatalogAdminHandlers) AdminProducts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	q := r.URL.Query()
	rows, next, err := h.Svc.AdminProducts(r.Context(), q.Get("store_id"), q.Get("search"), q.Get("cursor"), atoiBounded(q.Get("limit"), 20, 1, 100))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"products": rows, "next_cursor": next})
}

// AdminProductDetail serves GET /api/v1/dashboard/catalog-admin/products/{id}.
func (h *CatalogAdminHandlers) AdminProductDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	detail, err := h.Svc.AdminProduct(r.Context(), r.URL.Query().Get("store_id"), r.PathValue("id"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// AdminCategories serves GET /api/v1/dashboard/catalog-admin/categories.
func (h *CatalogAdminHandlers) AdminCategories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	rows, err := h.Svc.AdminCategories(r.Context(), r.URL.Query().Get("store_id"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"categories": rows})
}

// AdminTags serves GET /api/v1/dashboard/catalog-admin/tags.
func (h *CatalogAdminHandlers) AdminTags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	rows, err := h.Svc.AdminTags(r.Context(), r.URL.Query().Get("store_id"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": rows})
}
