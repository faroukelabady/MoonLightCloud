package http

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

// DashboardDataHandlers serves authenticated BFF reads. Each handler parses
// the frozen period contract, delegates to services, and emits dashboard
// DTOs. No financial math lives here.
type DashboardDataHandlers struct {
	dash dashboard.Service
	rep  report.Service
	log  *slog.Logger
}

// NewDashboardDataHandlers wires BFF data handlers.
func NewDashboardDataHandlers(dash dashboard.Service, rep report.Service, log *slog.Logger) DashboardDataHandlers {
	return DashboardDataHandlers{dash: dash, rep: rep, log: log}
}

func (h DashboardDataHandlers) parse(r *http.Request) (report.Request, error) {
	q := r.URL.Query()
	return h.rep.ParseRequest(q.Get("period"), q.Get("from_date"), q.Get("to_date"), q.Get("currency"))
}

func (h DashboardDataHandlers) observe(r *http.Request, name string, req report.Request, start time.Time) {
	h.log.Info("dashboard served",
		"request_id", RequestID(r),
		"endpoint", name,
		"period", req.Period.Kind,
		"duration_ms", time.Since(start).Milliseconds(),
	)
}

// Overview serves GET /api/v1/dashboard/overview.
func (h DashboardDataHandlers) Overview(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	req, err := h.parse(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	res, err := h.dash.Overview(r.Context(), req)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.observe(r, "dashboard_overview", req, start)
	writeJSON(w, http.StatusOK, res)
}

// Daily serves GET /api/v1/dashboard/daily (normalized trend).
func (h DashboardDataHandlers) Daily(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	req, err := h.parse(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	days, err := h.dash.DailyNormalized(r.Context(), req)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.observe(r, "dashboard_daily", req, start)
	writeJSON(w, http.StatusOK, map[string]any{
		"timezone": req.Period.Timezone,
		"period":   periodMetaJSON(req),
		"days":     days,
	})
}

// Products serves GET /api/v1/dashboard/products?mode=all|native.
// One row contract either way: {product_id, sku, product_name, units,
// amount_minor}. All mode normalizes with historical FX server-side;
// native mode selects the requested currency bucket (EGP|USD via period
// currency, defaulting sensibly for display).
func (h DashboardDataHandlers) Products(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	req, err := h.parse(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	if mode(r) == "all" {
		rows, err := h.dash.ProductsNormalized(r.Context(), req)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, map[string]any{
				"product_id": row.ProductID, "sku": row.SKU, "product_name": row.ProductName,
				"units": row.Units, "amount_minor": row.NormalizedMinor,
			})
		}
		h.observe(r, "dashboard_products_normalized", req, start)
		writeJSON(w, http.StatusOK, map[string]any{
			"timezone": req.Period.Timezone, "period": periodMetaJSON(req),
			"mode": "all", "unit": "EGP-normalized", "rows": out,
		})
		return
	}
	res, err := h.rep.Breakdown(r.Context(), req, report.DimensionProduct)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	currency := req.Currency
	if currency == "" {
		currency = "EGP"
	}
	out := make([]map[string]any, 0, len(res.Rows))
	for _, row := range res.Rows {
		var amount string
		for _, b := range row.LineSales {
			if b.Currency == currency {
				amount = minorString(b.LineSalesMinor)
			}
		}
		out = append(out, map[string]any{
			"product_id": row.ProductID, "sku": strOrEmpty(row.SKU),
			"product_name": strOrEmpty(row.ProductName),
			"units":        row.Units, "amount_minor": amount,
		})
	}
	h.observe(r, "dashboard_products_native", req, start)
	writeJSON(w, http.StatusOK, map[string]any{
		"timezone": req.Period.Timezone, "period": periodMetaJSON(req),
		"mode": "native", "unit": currency, "rows": out,
	})
}

// Categories serves GET /api/v1/dashboard/categories?kind=root|subcategory&mode=all|native.
// Unified row contract: {kind, id, name_ar, name_en, units, amount_minor}
// with server-side normalization in All mode, native bucket selection
// otherwise. Facet semantics preserved (no allocation, no merging).
func (h DashboardDataHandlers) Categories(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	req, err := h.parse(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind != report.DimensionRootCategory && kind != report.DimensionSubcategory {
		kind = report.DimensionRootCategory
	}
	if mode(r) == "all" {
		rows, err := h.dash.CategoriesNormalized(r.Context(), req, kind)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, map[string]any{
				"kind": row.Kind, "classification_id": row.ID,
				"name_ar": row.NameAR, "name_en": row.NameEN,
				"units": row.Units, "amount_minor": row.NormalizedMinor,
			})
		}
		h.observe(r, "dashboard_categories_normalized", req, start)
		writeJSON(w, http.StatusOK, map[string]any{
			"timezone": req.Period.Timezone, "period": periodMetaJSON(req),
			"mode": "all", "unit": "EGP-normalized", "kind": kind, "rows": out,
		})
		return
	}
	res, err := h.rep.Breakdown(r.Context(), req, kind)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	currency := req.Currency
	if currency == "" {
		currency = "EGP"
	}
	out := make([]map[string]any, 0, len(res.Rows))
	for _, row := range res.Rows {
		var amount string
		for _, b := range row.LineSales {
			if b.Currency == currency {
				amount = minorString(b.LineSalesMinor)
			}
		}
		out = append(out, map[string]any{
			"kind": row.Dimension, "classification_id": strOrEmpty(row.ClassificationID),
			"name_ar": strOrEmpty(row.NameAR), "name_en": strOrEmpty(row.NameEN),
			"units": row.Units, "amount_minor": amount,
		})
	}
	h.observe(r, "dashboard_categories_native", req, start)
	writeJSON(w, http.StatusOK, map[string]any{
		"timezone": req.Period.Timezone, "period": periodMetaJSON(req),
		"mode": "native", "unit": currency, "kind": kind, "rows": out,
	})
}

// Branches serves GET /api/v1/dashboard/branches (native buckets).
func (h DashboardDataHandlers) Branches(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	req, err := h.parse(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	rows, err := h.dash.Branches(r.Context(), req)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.observe(r, "dashboard_branches", req, start)
	writeJSON(w, http.StatusOK, map[string]any{
		"timezone": req.Period.Timezone, "period": periodMetaJSON(req), "rows": rows,
	})
}

// SyncHealth serves GET /api/v1/dashboard/sync-health.
func (h DashboardDataHandlers) SyncHealth(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	req, err := h.rep.ParseRequest(report.PeriodToday, "", "", "")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	_ = req
	health, err := h.dash.SyncHealth(r.Context())
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.log.Info("dashboard served", "request_id", RequestID(r),
		"endpoint", "dashboard_sync_health",
		"duration_ms", time.Since(start).Milliseconds())
	writeJSON(w, http.StatusOK, health)
}

// Activity serves GET /api/v1/dashboard/activity?limit=.
func (h DashboardDataHandlers) Activity(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			WriteError(w, r, apperr.New(apperr.InvalidInput, "invalid limit"))
			return
		}
		limit = n
	}
	items, err := h.dash.RecentActivity(r.Context(), limit)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.log.Info("dashboard served", "request_id", RequestID(r),
		"endpoint", "dashboard_activity",
		"duration_ms", time.Since(start).Milliseconds())
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// LatestSales serves GET /api/v1/dashboard/sales/latest (orders fallback).
func (h DashboardDataHandlers) LatestSales(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			WriteError(w, r, apperr.New(apperr.InvalidInput, "invalid limit"))
			return
		}
		limit = n
	}
	items, err := h.dash.LatestSales(r.Context(), limit)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.log.Info("dashboard served", "request_id", RequestID(r),
		"endpoint", "dashboard_latest_sales",
		"duration_ms", time.Since(start).Milliseconds())
	writeJSON(w, http.StatusOK, map[string]any{"sales": items})
}

// mode returns the currency presentation mode (default native).
func mode(r *http.Request) string {
	if r.URL.Query().Get("mode") == "all" {
		return "all"
	}
	return "native"
}

func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// minorString renders exact minor units for BFF string-money fields.
func minorString(v int64) string {
	return strconv.FormatInt(v, 10)
}

// periodMetaJSON renders period metadata for bespoke dashboard responses
// (same shape as report.PeriodMeta).
func periodMetaJSON(req report.Request) map[string]any {
	const layout = "2006-01-02T15:04:05.999999999Z07:00"
	return map[string]any{
		"kind": req.Period.Kind, "timezone": req.Period.Timezone,
		"start_local":         req.Period.StartLocal.Format(layout),
		"end_local_exclusive": req.Period.EndLocalExclusive.Format(layout),
		"start_utc":           req.Period.StartUTC.Format(layout),
		"end_utc":             req.Period.EndUTC.Format(layout),
	}
}
