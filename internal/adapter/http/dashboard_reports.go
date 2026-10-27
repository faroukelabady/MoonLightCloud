package http

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
	"github.com/faroukelabady/MoonLightCloud/internal/salesreport"
)

// WithPrintableReports adds the Reports page reads (sales report and
// stocktaking). Without it those routes are not registered.
func (h DashboardDataHandlers) WithPrintableReports(svc salesreport.Service) DashboardDataHandlers {
	h.printable = &svc
	return h
}

func pageParam(raw string, fallback int) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, apperr.New(apperr.InvalidInput, "detail_offset and detail_limit must be integers")
	}
	return n, nil
}

// SalesReport serves GET /api/v1/dashboard/reports/sales: every section
// of the printable sales report for the period, shown in currency=EGP
// (default), USD, or all (each currency separately).
func (h DashboardDataHandlers) SalesReport(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	q := r.URL.Query()
	display, err := salesreport.ParseDisplay(q.Get("currency"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	req, err := h.rep.ParseRequest(q.Get("period"), q.Get("from_date"), q.Get("to_date"), "")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	if req.Store, err = report.ParseStoreScope(q.Get("store_id")); err != nil {
		WriteError(w, r, err)
		return
	}
	offset, err := pageParam(q.Get("detail_offset"), 0)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	limit, err := pageParam(q.Get("detail_limit"), 50)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	filter, err := salesreport.ParseFilter(q.Get("root_category_ids"), q.Get("subcategory_ids"), q.Get("tag_ids"), q.Get("payment_method"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	res, err := h.printable.Sales(r.Context(), req, display, filter, offset, limit)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.observe(r, "report_sales", req, start)
	writeJSON(w, http.StatusOK, res)
}

// StockReport serves GET /api/v1/dashboard/reports/stocktaking: current
// stock valued at EGP cost and price, for counting and zakat.
func (h DashboardDataHandlers) StockReport(w http.ResponseWriter, r *http.Request) {
	scope, err := report.ParseStoreScope(r.URL.Query().Get("store_id"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	storeID := ""
	if scope != nil {
		storeID = scope.StoreID
	}
	res, err := h.printable.Stock(r.Context(), storeID, time.Now().UTC())
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.log.Info("dashboard served", "request_id", RequestID(r), "endpoint", "report_stocktaking", "store_id", storeID)
	writeJSON(w, http.StatusOK, res)
}
