package http

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalogadmin"
	"github.com/faroukelabady/MoonLightCloud/internal/humanauth"
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
	// StepUp checks the operator's fresh authenticator code before a
	// product, price or catalog change is queued. Nil refuses them all.
	StepUp StepUpVerifier
	// ClientIP resolves the caller address for the MFA throttle.
	ClientIP func(*http.Request) string
}

// StepUpVerifier is the humanauth step-up check.
type StepUpVerifier interface {
	ConfirmStepUp(ctx context.Context, a humanauth.Authenticated, code, clientIP, action string) error
}

func (h *CatalogAdminHandlers) actorOf(r *http.Request) string { return operatorOf(r) }

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
		// MFACode approves product, price and catalog changes. It is
		// checked and consumed here and never stored with the command.
		MFACode string `json:"mfa_code"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "invalid command request"))
		return
	}
	// ADR-0053: the command's Store must be one the human administers.
	if !RequireStore(w, r, req.StoreID) {
		return
	}
	payload, err := json.Marshal(req.Payload)
	if err != nil || len(req.Payload) == 0 {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "invalid command payload"))
		return
	}
	if catalogadmin.RequiresMFA(req.Type) && !h.stepUp(w, r, req.MFACode, req.Type) {
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

// stepUp checks the fresh authenticator code for a sensitive command and
// writes the refusal. Refusals are 403 (never 401): the session is still
// valid, only this change is not approved.
func (h *CatalogAdminHandlers) stepUp(w http.ResponseWriter, r *http.Request, code, commandType string) bool {
	return requireFreshStepUp(w, r, h.StepUp, h.ClientIP, code, commandType)
}

// Permissions serves GET /api/v1/dashboard/catalog-admin/permissions?store_id=:
// what the Store's devices allow Cloud to change, and which changes need
// an authenticator code.
func (h *CatalogAdminHandlers) Permissions(w http.ResponseWriter, r *http.Request) {
	storeID := r.URL.Query().Get("store_id")
	if !RequireStore(w, r, storeID) {
		return
	}
	p, err := h.Svc.Permissions(r.Context(), storeID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
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
	writeJSON(w, http.StatusOK, map[string]any{"commands": catalogadmin.PollWire(due)})
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

// AdminProductVariants serves GET /api/v1/dashboard/catalog-admin/products/{id}/variants
// (Phase 17: projected variant rows with attributes, prices, stock).
func (h *CatalogAdminHandlers) AdminProductVariants(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	rows, err := h.Svc.AdminProductVariants(r.Context(), r.URL.Query().Get("store_id"), r.PathValue("id"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"variants": rows})
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

// AdminProductTypes serves GET /api/v1/dashboard/catalog-admin/product-types.
func (h *CatalogAdminHandlers) AdminProductTypes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	q := r.URL.Query()
	rows, err := h.Svc.AdminProductTypes(r.Context(), q.Get("store_id"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"product_types": rows})
}

// AdminProductTypeDetail serves GET /api/v1/dashboard/catalog-admin/product-types/{id}.
func (h *CatalogAdminHandlers) AdminProductTypeDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	q := r.URL.Query()
	row, err := h.Svc.AdminProductType(r.Context(), q.Get("store_id"), r.PathValue("id"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"product_type": row})
}

// AdminMedia serves GET /api/v1/dashboard/catalog-admin/media/{sha256}
// (ADR-0056): one product image of the selected Store. The path is the
// content hash, so the response never changes and caches privately.
func (h *CatalogAdminHandlers) AdminMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	file, err := h.Svc.AdminMedia(r.Context(), r.URL.Query().Get("store_id"), r.PathValue("sha256"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", file.ContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(file.Content)))
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(file.Content)
}
