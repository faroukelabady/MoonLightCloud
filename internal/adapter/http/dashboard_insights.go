package http

import (
	"net/http"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
)

// WithInsights enables the sync page reads (sync summary, sale detail,
// image store status).
func (h DashboardDataHandlers) WithInsights(svc dashboard.Insights) DashboardDataHandlers {
	h.insights = &svc
	return h
}

// SyncSummary serves GET /api/v1/dashboard/sync-summary: events received in
// the period per kind, and the newest arrival of each kind.
func (h DashboardDataHandlers) SyncSummary(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if h.insights == nil {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	req, err := h.parse(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	out, err := h.insights.SyncSummary(r.Context(), req)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.observe(r, "dashboard_sync_summary", req, start)
	writeJSON(w, http.StatusOK, out)
}

// SaleDetail serves GET /api/v1/dashboard/sales/{id}: one sale of the
// selected scope with its lines, prices and recorded costs.
func (h DashboardDataHandlers) SaleDetail(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if h.insights == nil {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	req, err := h.parse(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	out, err := h.insights.SaleDetail(r.Context(), req, r.PathValue("id"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.observe(r, "dashboard_sale_detail", req, start)
	writeJSON(w, http.StatusOK, out)
}

// MediaStatus serves GET /api/v1/dashboard/media-status: whether the image
// store answers, and the image files of the selected scope.
func (h DashboardDataHandlers) MediaStatus(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if h.insights == nil {
		WriteError(w, r, apperr.New(apperr.NotFound, "not found"))
		return
	}
	req, err := h.parse(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	out, err := h.insights.MediaStatus(r.Context(), req)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.observe(r, "dashboard_media_status", req, start)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}
