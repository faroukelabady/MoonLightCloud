package http

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
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
	req, err := h.rep.ParseRequest(q.Get("period"), q.Get("from_date"), q.Get("to_date"), q.Get("currency"))
	if err != nil {
		return req, err
	}
	// One consistent scope model for every dashboard read: empty selects
	// global behavior (backward compatible); malformed UUIDs 400 here so
	// no handler invents its own empty/unknown semantics.
	scope, err := report.ParseStoreScope(q.Get("store_id"))
	if err != nil {
		return req, err
	}
	req.Store = scope
	return req, nil
}

func (h DashboardDataHandlers) observe(r *http.Request, name string, req report.Request, start time.Time) {
	h.log.Info("dashboard served",
		"request_id", RequestID(r),
		"endpoint", name,
		"period", req.Period.Kind,
		"store_id", req.ScopeStoreID(),
		"duration_ms", time.Since(start).Milliseconds(),
	)
}

// limitSlice applies the optional explicit Top-N bound (0 = unbounded).
// Ranking already happened server-side; this only truncates.
func limitSlice[T any](rows []T, limit int) []T {
	if limit > 0 && len(rows) > limit {
		return rows[:limit]
	}
	return rows
}

// scopeJSON echoes the applied Store scope in dashboard responses so
// operators (and the frontend) can never mistake scoped data for global
// data: the Store UUID, or null for the global scope.
func scopeJSON(req report.Request) map[string]any {
	if req.Scoped() {
		return map[string]any{"store_id": req.ScopeStoreID()}
	}
	return map[string]any{"store_id": nil}
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

// Daily serves GET /api/v1/dashboard/daily?mode=all|EGP|USD.
// All returns normalized EGP per Cairo date; EGP/USD return native buckets
// only. The contract metadata (mode, display_currency, normalized) makes
// mislabeling impossible.
func (h DashboardDataHandlers) Daily(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	req, err := h.parse(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	modeName := r.URL.Query().Get("mode")
	if modeName == "" {
		modeName = "all"
	}
	res, err := h.dash.Daily(r.Context(), req, modeName)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.observe(r, "dashboard_daily_"+res.Mode, req, start)
	writeJSON(w, http.StatusOK, res)
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
	limit, err := report.ParseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	req.Limit = limit
	if mode(r) == "all" {
		rows, err := h.dash.ProductsNormalized(r.Context(), req)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		rows = limitSlice(rows, limit)
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, map[string]any{
				"product_id": row.ProductID, "sku": row.SKU, "product_name": row.ProductName,
				"units": row.Units, "units_returned": row.UnitsReturned,
				"amount_minor": row.NormalizedMinor,
				"refund_minor": row.RefundNormalizedMinor, "net_minor": row.NetNormalizedMinor,
				"store_id": row.StoreID,
			})
		}
		h.observe(r, "dashboard_products_normalized", req, start)
		writeJSON(w, http.StatusOK, map[string]any{
			"timezone": req.Period.Timezone, "period": periodMetaJSON(req),
			"mode": "all", "unit": "EGP-normalized", "rows": out,
			"store_id": scopeOrNull(req),
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
		var amount, refund string
		for _, b := range row.LineSales {
			if b.Currency == currency {
				amount = minorString(b.LineSalesMinor)
				refund = minorString(b.LineRefundMinor)
			}
		}
		net, err := netMinor(amount, refund)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out = append(out, map[string]any{
			"product_id": row.ProductID, "sku": strOrEmpty(row.SKU),
			"product_name": strOrEmpty(row.ProductName),
			"units":        row.Units, "units_returned": row.UnitsReturned,
			"amount_minor": amount, "refund_minor": refund, "net_minor": net,
		})
	}
	h.observe(r, "dashboard_products_native", req, start)
	writeJSON(w, http.StatusOK, map[string]any{
		"timezone": req.Period.Timezone, "period": periodMetaJSON(req),
		"mode": "native", "unit": currency, "rows": out,
		"store_id": scopeOrNull(req),
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
	limit, err := report.ParseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	req.Limit = limit
	if mode(r) == "all" {
		rows, err := h.dash.CategoriesNormalized(r.Context(), req, kind)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		rows = limitSlice(rows, limit)
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, map[string]any{
				"kind": row.Kind, "classification_id": row.ID,
				"name_ar": row.NameAR, "name_en": row.NameEN,
				"units": row.Units, "units_returned": row.UnitsReturned,
				"amount_minor": row.NormalizedMinor,
				"refund_minor": row.RefundNormalizedMinor, "net_minor": row.NetNormalizedMinor,
			})
		}
		h.observe(r, "dashboard_categories_normalized", req, start)
		writeJSON(w, http.StatusOK, map[string]any{
			"timezone": req.Period.Timezone, "period": periodMetaJSON(req),
			"mode": "all", "unit": "EGP-normalized", "kind": kind, "rows": out,
			"store_id": scopeOrNull(req),
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
		var amount, refund string
		for _, b := range row.LineSales {
			if b.Currency == currency {
				amount = minorString(b.LineSalesMinor)
				refund = minorString(b.LineRefundMinor)
			}
		}
		net, err := netMinor(amount, refund)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		out = append(out, map[string]any{
			"kind": row.Dimension, "classification_id": strOrEmpty(row.ClassificationID),
			"name_ar": strOrEmpty(row.NameAR), "name_en": strOrEmpty(row.NameEN),
			"units": row.Units, "units_returned": row.UnitsReturned,
			"amount_minor": amount, "refund_minor": refund, "net_minor": net,
		})
	}
	h.observe(r, "dashboard_categories_native", req, start)
	writeJSON(w, http.StatusOK, map[string]any{
		"timezone": req.Period.Timezone, "period": periodMetaJSON(req),
		"mode": "native", "unit": currency, "kind": kind, "rows": out,
		"store_id": scopeOrNull(req),
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
		"store_id": scopeOrNull(req),
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
	scope, err := report.ParseStoreScope(r.URL.Query().Get("store_id"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	items, err := h.dash.RecentActivity(r.Context(), report.Request{Store: scope}, limit)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.log.Info("dashboard served", "request_id", RequestID(r),
		"endpoint", "dashboard_activity",
		"duration_ms", time.Since(start).Milliseconds())
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "store_id": scopeOrNullReq(r)})
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
	scope, err := report.ParseStoreScope(r.URL.Query().Get("store_id"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	items, err := h.dash.LatestSales(r.Context(), report.Request{Store: scope}, limit)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.log.Info("dashboard served", "request_id", RequestID(r),
		"endpoint", "dashboard_latest_sales",
		"duration_ms", time.Since(start).Milliseconds())
	writeJSON(w, http.StatusOK, map[string]any{"sales": items, "store_id": scopeOrNullReq(r)})
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

// netMinor subtracts exact minor-unit strings with overflow failure (net
// may be negative, but must never wrap). Empty means zero.
func netMinor(gross, refund string) (string, error) {
	parse := func(s string) (int64, error) {
		if s == "" {
			return 0, nil
		}
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return 0, apperr.New(apperr.Internal, "net arithmetic overflow")
		}
		return v, nil
	}
	g, err := parse(gross)
	if err != nil {
		return "", err
	}
	r, err := parse(refund)
	if err != nil {
		return "", err
	}
	const maxInt64 = int64(^uint64(0) >> 1)
	const minInt64 = -maxInt64 - 1
	if (r > 0 && g < minInt64+r) || (r < 0 && g > maxInt64+r) {
		return "", apperr.New(apperr.Internal, "net arithmetic overflow")
	}
	return strconv.FormatInt(g-r, 10), nil
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

// scopeOrNull renders the applied Store scope for map responses.
func scopeOrNull(req report.Request) any {
	if req.Scoped() {
		return req.ScopeStoreID()
	}
	return nil
}

// scopeOrNullReq re-parses the Store scope for handlers without a
// period request (echo only; the service call already validated it).
func scopeOrNullReq(r *http.Request) any {
	scope, err := report.ParseStoreScope(r.URL.Query().Get("store_id"))
	if err != nil || scope == nil {
		return nil
	}
	return scope.StoreID
}

// Tags serves GET /api/v1/dashboard/tags — the bounded Top Tags surface.
// It ranks through the CANONICAL report breakdown (historical Tag
// snapshots; the frozen financial query is the only ranking authority):
// rows are grouped by canonical historical Tag identity (never merged by
// slug across Stores), ranked deterministically and truncated to the
// requested limit. Tag totals OVERLAP by design (a sale line tagged A+B
// contributes its full attributable amount to both rows) and must never
// be summed to derive business revenue — the UI states this explicitly.
func (h DashboardDataHandlers) Tags(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	req, err := h.parse(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	if req.Limit, err = report.ParseLimit(r.URL.Query().Get("limit")); err != nil {
		WriteError(w, r, err)
		return
	}
	res, err := h.rep.Breakdown(r.Context(), req, report.DimensionTag)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.observe(r, "tags", req, start)
	// Dashboard exact-money representation: minor units as strings (large
	// int64 values must never become unsafe JSON numbers).
	writeJSON(w, http.StatusOK, dashboard.TagListFrom(res))
}

// OrderAnalytics serves GET /api/v1/dashboard/orders/summary — the
// operational online-vs-store comparison source. Provider orders are
// operational commerce truth and are NEVER combined with canonical
// finalized Retail Sales (a provider order may duplicate business
// activity already represented by a finalized Sale).
func (h DashboardDataHandlers) OrderAnalytics(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	req, err := h.parse(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	res, err := h.dash.OrderAnalytics(r.Context(), req, r.URL.Query().Get("provider_key"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.observe(r, "order_analytics", req, start)
	writeJSON(w, http.StatusOK, res)
}

// CatalogHealth serves GET /api/v1/dashboard/catalog-health — bounded,
// read-only, durable-state diagnostics. Zero provider calls, zero writes,
// zero adoption, zero barrier settlement (Phase 12 §44/§57/§66/§169).
func (h DashboardDataHandlers) CatalogHealth(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	q := r.URL.Query()
	// Catalog health is CURRENT catalog/integration state (never
	// historical Sale snapshots): the report period does not scope it.
	// The selector's period/currency are accepted for validation
	// consistency but do not filter health rows.
	period := q.Get("period")
	if period == "" {
		period = "today"
	}
	req, err := h.rep.ParseRequest(period, q.Get("from_date"), q.Get("to_date"), q.Get("currency"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	if req.Store, err = report.ParseStoreScope(q.Get("store_id")); err != nil {
		WriteError(w, r, err)
		return
	}
	limit := 0
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		if limit, err = report.ParseLimit(raw); err != nil {
			WriteError(w, r, err)
			return
		}
	}
	res, err := h.dash.CatalogHealth(r.Context(), req, q.Get("provider_key"), q.Get("reason"), limit)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.observe(r, "catalog_health", req, start)
	writeJSON(w, http.StatusOK, res)
}
