package http

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
)

type stubOrderReader struct {
	summaries []orders.OrderSummary
	detail    orders.OrderDetail
	counts    []orders.OrderStatusCount
	inbox     orders.WebhookQueueStats
	err       error
}

func (s *stubOrderReader) ListOrderSummaries(_ context.Context, _, _ string, _ int, _ *orders.OrderCursor) ([]orders.OrderSummary, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.summaries, nil
}

func (s *stubOrderReader) ListOrderPage(_ context.Context, _, _ string, limit int, _ *orders.OrderCursor) (orders.OrderPage, error) {
	if s.err != nil {
		return orders.OrderPage{}, s.err
	}
	items := s.summaries
	if len(items) > limit {
		items = items[:limit]
	}
	return orders.OrderPage{Items: items}, nil
}

func (s *stubOrderReader) GetOrderDetail(_ context.Context, _, _ string) (orders.OrderDetail, error) {
	if s.err != nil {
		return orders.OrderDetail{}, s.err
	}
	return s.detail, nil
}

func (s *stubOrderReader) CountOrdersByStatus(_ context.Context, _ string) ([]orders.OrderStatusCount, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.counts, nil
}

func (s *stubOrderReader) OrderInboxStats(_ context.Context) (orders.WebhookQueueStats, error) {
	if s.err != nil {
		return orders.WebhookQueueStats{}, s.err
	}
	return s.inbox, nil
}

// Phase 9C Store-scoped reads: handler contract tests use the global
// surface only; scoped reads are covered against real PostgreSQL.
func (s *stubOrderReader) ListOrderSummariesForStore(_ context.Context, _ string, _, _ string, _ int, _ *orders.OrderCursor) ([]orders.OrderSummary, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.summaries, nil
}

func (s *stubOrderReader) ListOrderPageForStore(_ context.Context, _ string, _, _ string, limit int, _ *orders.OrderCursor) (orders.OrderPage, error) {
	if s.err != nil {
		return orders.OrderPage{}, s.err
	}
	items := s.summaries
	if len(items) > limit {
		items = items[:limit]
	}
	return orders.OrderPage{Items: items}, nil
}

func (s *stubOrderReader) GetOrderDetailForStore(_ context.Context, _, _, _ string) (orders.OrderDetail, error) {
	if s.err != nil {
		return orders.OrderDetail{}, s.err
	}
	return s.detail, nil
}

func (s *stubOrderReader) CountOrdersByStatusForStore(_ context.Context, _, _ string) ([]orders.OrderStatusCount, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.counts, nil
}

func testOrderLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func orderHandlers() DashboardOrderHandlers {
	return NewDashboardOrderHandlers(&stubOrderReader{
		summaries: []orders.OrderSummary{
			{
				ProviderKey: "website", ExternalOrderID: "800", OrderNumber: "800",
				ProviderStatus: "processing", CanonicalStatus: "PROCESSING",
				Currency: "EGP", TotalMinor: "9007199254740993",
				CreatedAt: "2026-09-27T10:00:00Z", ModifiedAt: "2026-09-27T11:00:00Z",
				CustomerName: "A B", MappingComplete: false, UnmappedLineCount: 1,
			},
		},
		detail: orders.OrderDetail{
			Summary: orders.OrderSummary{
				ProviderKey: "website", ExternalOrderID: "800", TotalMinor: "9007199254740993",
				CanonicalStatus: "PROCESSING", MappingComplete: false, UnmappedLineCount: 1,
			},
			Lines: []orders.OrderLineView{
				{ExternalLineID: 1, ExternalProductID: "999", SKU: "FOREIGN", Name: "Foreign", Quantity: 1, TotalMinor: "9007199254740993"},
			},
			StatusHistory: []orders.OrderStatusEvent{
				{OrderRevision: 1, ProviderStatus: "processing", CanonicalStatus: "PROCESSING"},
			},
		},
		counts: []orders.OrderStatusCount{{CanonicalStatus: "PROCESSING", Total: 1}},
	}, testOrderLog())
}

func TestDashboardOrdersList(t *testing.T) {
	handlers := orderHandlers()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/orders", nil)
	recorder := httptest.NewRecorder()
	handlers.Orders(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{
		`"total_minor":"9007199254740993"`,
		`"canonical_status":"PROCESSING"`,
		`"unmapped_line_count":1`,
		`"status_counts"`,
		`"webhook_inbox"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
	// Invalid status filter is rejected, never passed to storage.
	bad := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/orders?status=BOGUS", nil)
	recorder = httptest.NewRecorder()
	handlers.Orders(recorder, bad)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("400, got %d", recorder.Code)
	}
}

func TestDashboardOrderDetail(t *testing.T) {
	handlers := orderHandlers()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/orders/website/800", nil)
	request.SetPathValue("provider_key", "website")
	request.SetPathValue("external_order_id", "800")
	recorder := httptest.NewRecorder()
	handlers.OrderDetail(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{
		`"sku":"FOREIGN"`, `"status_history"`, `"moonlight_product_id":null`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
}

func TestDashboardOrderNotFound(t *testing.T) {
	handlers := NewDashboardOrderHandlers(&stubOrderReader{err: apperr.New(apperr.NotFound, "order not found")}, testOrderLog())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard/orders/website/999", nil)
	request.SetPathValue("provider_key", "website")
	request.SetPathValue("external_order_id", "999")
	recorder := httptest.NewRecorder()
	handlers.OrderDetail(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("404, got %d", recorder.Code)
	}
}
