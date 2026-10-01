package http

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
)

// pagingOrderReader is a positional in-memory OrderReader for handler
// pagination tests: it honors limit/cursor like keyset pages so the
// handler, cursor codec, and response contract are exercised without a
// database (keyset truth itself is proven in PostgreSQL suites).
type pagingOrderReader struct {
	items []orders.OrderSummary
}

func (f *pagingOrderReader) ListOrderSummaries(_ context.Context, _, _ string, _ int, _ *orders.OrderCursor) ([]orders.OrderSummary, error) {
	return f.items, nil
}

func (f *pagingOrderReader) ListOrderPage(_ context.Context, _, _ string, limit int, cursor *orders.OrderCursor) (orders.OrderPage, error) {
	start := 0
	if cursor != nil {
		for i, item := range f.items {
			if item.ProviderKey == cursor.ProviderKey && item.ExternalOrderID == cursor.ExternalOrderID {
				start = i + 1
				break
			}
		}
	}
	end := start + limit
	page := orders.OrderPage{}
	if end >= len(f.items) {
		page.Items = f.items[start:]
		return page, nil
	}
	page.Items = f.items[start:end]
	last := page.Items[len(page.Items)-1]
	created, err := time.Parse(time.RFC3339, last.CreatedAt)
	if err != nil {
		return orders.OrderPage{}, err
	}
	page.Next = &orders.OrderCursor{CreatedAt: created, ProviderKey: last.ProviderKey, ExternalOrderID: last.ExternalOrderID}
	return page, nil
}

func (f *pagingOrderReader) GetOrderDetail(_ context.Context, _, _ string) (orders.OrderDetail, error) {
	return orders.OrderDetail{}, nil
}

func (f *pagingOrderReader) CountOrdersByStatus(_ context.Context, _ string) ([]orders.OrderStatusCount, error) {
	return nil, nil
}

func (f *pagingOrderReader) OrderInboxStats(_ context.Context) (orders.WebhookQueueStats, error) {
	return orders.WebhookQueueStats{}, nil
}

// Phase 9C Store-scoped reads: paging contract tests use the global
// surface only; scoped reads are covered against real PostgreSQL.
func (f *pagingOrderReader) ListOrderSummariesForStore(_ context.Context, _ string, _, _ string, _ int, _ *orders.OrderCursor) ([]orders.OrderSummary, error) {
	return f.items, nil
}

func (f *pagingOrderReader) ListOrderPageForStore(_ context.Context, _ string, _, _ string, limit int, _ *orders.OrderCursor) (orders.OrderPage, error) {
	items := f.items
	if len(items) > limit {
		items = items[:limit]
	}
	return orders.OrderPage{Items: items}, nil
}

func (f *pagingOrderReader) GetOrderDetailForStore(_ context.Context, _, _, _ string) (orders.OrderDetail, error) {
	return orders.OrderDetail{}, nil
}

func (f *pagingOrderReader) CountOrdersByStatusForStore(_ context.Context, _, _ string) ([]orders.OrderStatusCount, error) {
	return nil, nil
}

func pagingFixture(count int) []orders.OrderSummary {
	items := make([]orders.OrderSummary, 0, count)
	for i := 0; i < count; i++ {
		items = append(items, orders.OrderSummary{
			ProviderKey: "website", ExternalOrderID: fmt.Sprintf("07%03d", i),
			OrderNumber: fmt.Sprintf("07%03d", i), ProviderStatus: "processing",
			CanonicalStatus: "PROCESSING", Currency: "EGP", TotalMinor: "68000",
			CreatedAt:    time.Date(2026, 9, 27, 12, 0, count-i, 0, time.UTC).Format(time.RFC3339),
			ModifiedAt:   "2026-09-27T12:00:00Z",
			CustomerName: "A B", MappingComplete: true,
		})
	}
	return items
}

func getOrders(t *testing.T, handler DashboardOrderHandlers, target string) (int, map[string]any) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	recorder := httptest.NewRecorder()
	handler.Orders(recorder, request)
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", target, err)
	}
	return recorder.Code, body
}

// TestDashboardOrdersPagination proves first/continuation/final pages,
// malformed and oversized cursors, and filter-bound cursor rejection.
func TestDashboardOrdersPagination(t *testing.T) {
	handler := NewDashboardOrderHandlers(&pagingOrderReader{items: pagingFixture(25)}, testOrderLog())
	code, first := getOrders(t, handler, "/api/v1/dashboard/orders")
	if code != http.StatusOK {
		t.Fatalf("page1: %d", code)
	}
	if got := len(first["orders"].([]any)); got != 20 {
		t.Fatalf("page1 rows: %d", got)
	}
	cursor, ok := first["next_cursor"].(string)
	if !ok || cursor == "" {
		t.Fatalf("page1 cursor: %v", first["next_cursor"])
	}
	code, second := getOrders(t, handler, "/api/v1/dashboard/orders?cursor="+cursor)
	if code != http.StatusOK {
		t.Fatalf("page2: %d", code)
	}
	if got := len(second["orders"].([]any)); got != 5 {
		t.Fatalf("page2 rows: %d", got)
	}
	if second["next_cursor"] != nil {
		t.Fatalf("final page must omit cursor: %v", second["next_cursor"])
	}
	seen := map[string]bool{}
	for _, page := range []map[string]any{first, second} {
		for _, row := range page["orders"].([]any) {
			id := row.(map[string]any)["external_order_id"].(string)
			if seen[id] {
				t.Fatalf("duplicate %s", id)
			}
			seen[id] = true
		}
	}
	if len(seen) != 25 {
		t.Fatalf("unique rows: %d", len(seen))
	}
	for _, target := range []string{
		"/api/v1/dashboard/orders?cursor=!!!not-base64!!!",
		"/api/v1/dashboard/orders?cursor=" + strings.Repeat("A", 513),
		"/api/v1/dashboard/orders?cursor=" + cursor + "&provider=other",
		"/api/v1/dashboard/orders?limit=bogus",
		"/api/v1/dashboard/orders?limit=101",
	} {
		if code, _ := getOrders(t, handler, target); code != http.StatusBadRequest {
			t.Fatalf("%s: want 400, got %d", target, code)
		}
	}
}

// TestDashboardOrdersMaxLimit proves the configured maximum page still
// continues instead of imposing a hard accessibility ceiling.
func TestDashboardOrdersMaxLimit(t *testing.T) {
	handler := NewDashboardOrderHandlers(&pagingOrderReader{items: pagingFixture(101)}, testOrderLog())
	code, first := getOrders(t, handler, "/api/v1/dashboard/orders?limit=100")
	if code != http.StatusOK {
		t.Fatalf("max page: %d", code)
	}
	if got := len(first["orders"].([]any)); got != 100 {
		t.Fatalf("max rows: %d", got)
	}
	if _, ok := first["next_cursor"].(string); !ok {
		t.Fatalf("max page must continue: %v", first["next_cursor"])
	}
}

// TestOrderCursorCodec proves token round-trip canonicality, filter
// binding, and the PII/secret-free payload shape.
func TestOrderCursorCodec(t *testing.T) {
	cursor := &orders.OrderCursor{
		CreatedAt:   time.Date(2026, 9, 27, 11, 0, 0, 123456000, time.UTC),
		ProviderKey: "website", ExternalOrderID: "901",
	}
	token, err := encodeOrderCursor(cursor, "website", "PROCESSING")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeOrderCursor(token, "website", "PROCESSING")
	if err != nil {
		t.Fatalf("round-trip: %v", err)
	}
	if !decoded.CreatedAt.Equal(cursor.CreatedAt) || decoded.ProviderKey != "website" || decoded.ExternalOrderID != "901" {
		t.Fatalf("round-trip: %+v", decoded)
	}
	if _, err := decodeOrderCursor(token, "other", "PROCESSING"); err == nil {
		t.Fatal("filter mismatch must fail")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatal(err)
	}
	payload := string(raw)
	for _, forbidden := range []string{"ahmed", "@example.com", "+201", "Cairo", "68000", "secret", "ck_", "cs_"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("cursor leaks %q: %s", forbidden, payload)
		}
	}
}
