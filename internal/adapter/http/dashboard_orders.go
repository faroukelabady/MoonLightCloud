package http

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

// DashboardOrderHandlers serves read-only Online Orders operator views.
// No mutation endpoints: fulfillment actions do not exist in Phase 6C.
type DashboardOrderHandlers struct {
	reader orders.OrderReader
	log    *slog.Logger
}

// NewDashboardOrderHandlers wires order reads.
func NewDashboardOrderHandlers(reader orders.OrderReader, log *slog.Logger) DashboardOrderHandlers {
	return DashboardOrderHandlers{reader: reader, log: log}
}

// Orders serves GET /api/v1/dashboard/orders with bounded filters:
// provider, canonical status, limit, Store scope, and an opaque cursor.
// The cursor binds all filters including Store scope. Responses carry
// next_cursor (null on the final page) for keyset traversal.
func (h DashboardOrderHandlers) Orders(w http.ResponseWriter, r *http.Request) {
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
	if limit <= 0 || limit > 100 {
		WriteError(w, r, apperr.New(apperr.InvalidInput, "invalid limit"))
		return
	}
	provider := r.URL.Query().Get("provider")
	status := r.URL.Query().Get("status")
	if status != "" {
		valid := false
		for _, canonical := range []orders.CanonicalStatus{
			orders.StatusPending, orders.StatusProcessing, orders.StatusOnHold,
			orders.StatusCompleted, orders.StatusCancelled, orders.StatusRefunded,
			orders.StatusFailed, orders.StatusUnknown, orders.StatusDeleted,
		} {
			if orders.CanonicalStatus(status) == canonical {
				valid = true
				break
			}
		}
		if !valid {
			WriteError(w, r, apperr.New(apperr.InvalidInput, "invalid status"))
			return
		}
	}
	scope, err := report.ParseStoreScope(r.URL.Query().Get("store_id"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	storeID := ""
	if scope != nil {
		storeID = scope.StoreID
	}
	var cursor *orders.OrderCursor
	if v := r.URL.Query().Get("cursor"); v != "" {
		decoded, err := decodeOrderCursor(v, provider, status, storeID)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		cursor = decoded
	}
	var page orders.OrderPage
	var counts []orders.OrderStatusCount
	if scope != nil {
		page, err = h.reader.ListOrderPageForStore(r.Context(), scope.StoreID, provider, status, limit, cursor)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		counts, err = h.reader.CountOrdersByStatusForStore(r.Context(), scope.StoreID, provider)
	} else {
		page, err = h.reader.ListOrderPage(r.Context(), provider, status, limit, cursor)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		counts, err = h.reader.CountOrdersByStatus(r.Context(), provider)
	}
	if err != nil {
		WriteError(w, r, err)
		return
	}
	inbox, err := h.reader.OrderInboxStats(r.Context())
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var nextCursor *string
	if page.Next != nil {
		encoded, err := encodeOrderCursor(page.Next, provider, status, storeID)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		nextCursor = &encoded
	}
	h.log.Info("dashboard served", "request_id", RequestID(r),
		"endpoint", "dashboard_orders",
		"store_id", storeID,
		"duration_ms", time.Since(start).Milliseconds())
	writeJSON(w, http.StatusOK, map[string]any{
		"orders": page.Items, "next_cursor": nextCursor,
		"status_counts": counts, "webhook_inbox": inbox,
		"store_id": storeOrNull(storeID),
	})
}

// OrderDetail serves GET /api/v1/dashboard/orders/{provider_key}/{external_order_id}.
// With ?store_id=, the root read is Store-gated: wrong-Store and missing
// rows are indistinguishable NotFound, so no cross-Store order/customer
// data can leak through this surface.
func (h DashboardOrderHandlers) OrderDetail(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	scope, err := report.ParseStoreScope(r.URL.Query().Get("store_id"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var detail orders.OrderDetail
	if scope != nil {
		detail, err = h.reader.GetOrderDetailForStore(r.Context(),
			scope.StoreID, r.PathValue("provider_key"), r.PathValue("external_order_id"))
	} else {
		detail, err = h.reader.GetOrderDetail(r.Context(),
			r.PathValue("provider_key"), r.PathValue("external_order_id"))
	}
	if err != nil {
		WriteError(w, r, err)
		return
	}
	h.log.Info("dashboard served", "request_id", RequestID(r),
		"endpoint", "dashboard_order_detail",
		"duration_ms", time.Since(start).Milliseconds())
	writeJSON(w, http.StatusOK, detail)
}

// storeOrNull renders the applied Store scope for responses (UUID string
// or JSON null for the global scope).
func storeOrNull(storeID string) any {
	if storeID == "" {
		return nil
	}
	return storeID
}
