package http

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

// ReportHandlers are thin: parse query params, delegate to ReportingService,
// map errors to the stable envelope. No business math lives here.
type ReportHandlers struct {
	svc report.Service
	log *slog.Logger
}

// NewReportHandlers wires report endpoints.
func NewReportHandlers(svc report.Service, log *slog.Logger) ReportHandlers {
	return ReportHandlers{svc: svc, log: log}
}

func (h ReportHandlers) parse(r *http.Request) (report.Request, error) {
	q := r.URL.Query()
	return h.svc.ParseRequest(q.Get("period"), q.Get("from_date"), q.Get("to_date"), q.Get("currency"))
}

func (h ReportHandlers) observe(r *http.Request, kind string, req report.Request, start time.Time, rows int) {
	h.log.Info("report served",
		"request_id", RequestID(r),
		"report", kind,
		"period", req.Period.Kind,
		"start_utc", req.Period.StartUTC.Format(time.RFC3339),
		"end_utc", req.Period.EndUTC.Format(time.RFC3339),
		"duration_ms", time.Since(start).Milliseconds(),
		"rows", rows,
	)
}

// SalesSummary serves GET /api/v1/reports/sales/summary.
func (h ReportHandlers) SalesSummary(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	req, err := h.parse(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	res, err := h.svc.Summary(r.Context(), req)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.observe(r, "sales_summary", req, start, len(res.CurrencyTotals))
	writeJSON(w, http.StatusOK, res)
}

// SalesDaily serves GET /api/v1/reports/sales/daily.
func (h ReportHandlers) SalesDaily(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	req, err := h.parse(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	res, err := h.svc.Daily(r.Context(), req)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.observe(r, "sales_daily", req, start, len(res.Days))
	writeJSON(w, http.StatusOK, res)
}

// SalesBreakdown serves GET /api/v1/reports/sales/breakdown?dimension=....
func (h ReportHandlers) SalesBreakdown(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	req, err := h.parse(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	dim, err := report.ParseDimension(r.URL.Query().Get("dimension"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	res, err := h.svc.Breakdown(r.Context(), req, dim)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.observe(r, "sales_breakdown_"+dim, req, start, len(res.Rows))
	writeJSON(w, http.StatusOK, res)
}
