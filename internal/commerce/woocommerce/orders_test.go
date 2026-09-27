package woocommerce

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
)

func wooOrderFixture(id int, status string) map[string]any {
	return map[string]any{
		"number":               "1001",
		"status":               status,
		"currency":             "EGP",
		"date_created_gmt":     "2026-09-27T10:00:00",
		"date_modified_gmt":    "2026-09-27T11:00:00",
		"date_paid_gmt":        "2026-09-27T10:05:00",
		"date_completed_gmt":   "",
		"discount_total":       "0.00",
		"shipping_total":       "30.00",
		"cart_tax":             "0.00",
		"total_tax":            "0.00",
		"total":                "680.00",
		"prices_include_tax":   false,
		"payment_method":       "cod",
		"payment_method_title": "Cash on delivery",
		"billing": map[string]any{
			"first_name": "أحمد", "last_name": "محمد", "company": "",
			"address_1": "١٢ شارع النيل", "address_2": "", "city": "القاهرة",
			"state": "", "postcode": "11511", "country": "EG",
			"email": "ahmed@example.com", "phone": "+201000000000",
		},
		"shipping": map[string]any{
			"first_name": "أحمد", "last_name": "محمد", "company": "",
			"address_1": "١٢ شارع النيل", "address_2": "", "city": "القاهرة",
			"state": "", "postcode": "11511", "country": "EG",
		},
		"line_items": []any{
			map[string]any{
				"id": float64(1), "name": "بردية توت", "product_id": float64(500),
				"variation_id": float64(0), "quantity": float64(2),
				"subtotal": "1300.00", "subtotal_tax": "0.00",
				"total": "1300.00", "total_tax": "0.00", "sku": "PAP-001",
			},
		},
	}
}

func TestWooGetOrderExact(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	harness.preloadOrder(100, wooOrderFixture(100, "processing"))
	provider := testProvider(t, harness)
	snapshot, err := provider.GetOrder(context.Background(), "100")
	if err != nil {
		t.Fatalf("get order: %v", err)
	}
	if snapshot.ExternalOrderID != "100" || snapshot.OrderNumber != "1001" {
		t.Fatalf("identity: %+v", snapshot)
	}
	if snapshot.ProviderStatus != "processing" || snapshot.Canonical != orders.StatusProcessing {
		t.Fatalf("status: %+v", snapshot)
	}
	if snapshot.Currency != "EGP" || snapshot.TotalMinor != 68000 || snapshot.ShippingMinor != 3000 {
		t.Fatalf("money: %+v", snapshot)
	}
	if snapshot.PaidAt == nil {
		t.Fatal("paid at must parse")
	}
	if snapshot.CompletedAt != nil {
		t.Fatalf("completed at: %+v", snapshot.CompletedAt)
	}
	if snapshot.PaymentMethod != "cod" || snapshot.PaymentMethodTitle != "Cash on delivery" {
		t.Fatalf("payment: %+v", snapshot)
	}
	if snapshot.Customer.FirstName != "أحمد" || snapshot.Customer.Email != "ahmed@example.com" {
		t.Fatalf("customer: %+v", snapshot.Customer)
	}
	if snapshot.Billing.City != "القاهرة" || snapshot.Shipping.Address1 != "١٢ شارع النيل" {
		t.Fatalf("addresses: %+v", snapshot)
	}
	if len(snapshot.Lines) != 1 {
		t.Fatalf("lines: %+v", snapshot.Lines)
	}
	line := snapshot.Lines[0]
	if line.ExternalLineID != 1 || line.ExternalProductID != "500" || line.Quantity != 2 ||
		line.TotalMinor != 130000 || line.SKU != "PAP-001" || line.Name != "بردية توت" {
		t.Fatalf("line: %+v", line)
	}
	if line.Mapped || line.MoonlightProduct != nil {
		t.Fatal("resolution happens at projection, not in the adapter")
	}
	// Correct endpoint with auth.
	requests := harness.recorded()
	if len(requests) != 1 || requests[0].Method != http.MethodGet ||
		requests[0].Path != "/wp-json/wc/v3/orders/100" || !requests[0].HasAuth {
		t.Fatalf("endpoint: %+v", requests)
	}
}

func TestWooGetOrderUnknownStatus(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	fixture := wooOrderFixture(101, "awaiting-pickup-custom")
	harness.preloadOrder(101, fixture)
	provider := testProvider(t, harness)
	snapshot, err := provider.GetOrder(context.Background(), "101")
	if err != nil {
		t.Fatalf("unknown status still ingests: %v", err)
	}
	if snapshot.ProviderStatus != "awaiting-pickup-custom" || snapshot.Canonical != orders.StatusUnknown {
		t.Fatalf("status: %+v", snapshot)
	}
}

func TestWooGetOrderFailures(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	provider := testProvider(t, harness)
	// Missing order → typed not-found (blocked, never a silent tombstone).
	_, err := provider.GetOrder(context.Background(), "999")
	if code, blocked := orders.IsBlocked(err); !blocked || code != orders.CodeOrderNotFound {
		t.Fatalf("404: %v", err)
	}
	// Invalid external ID → blocked invalid.
	if _, err := provider.GetOrder(context.Background(), "nope"); err == nil {
		t.Fatal("invalid id must fail")
	} else if code, blocked := orders.IsBlocked(err); !blocked || code != orders.CodeOrderInvalid {
		t.Fatalf("invalid: %v", err)
	}
	// Identity mismatch → conflict (served via intercept so the
	// response ID genuinely differs from the request).
	harness.setIntercept(func(record wooRecordedRequest) (int, any, bool) {
		return http.StatusOK, map[string]any{"id": float64(103), "status": "pending"}, true
	})
	_, err = provider.GetOrder(context.Background(), "102")
	harness.setIntercept(nil)
	if code, blocked := orders.IsBlocked(err); !blocked || code != orders.CodeOrderConflict {
		t.Fatalf("mismatch: %v", err)
	}
	// Transport failures keep taxonomy for retry routing.
	harness.fail(http.MethodGet, "/wp-json/wc/v3/orders/104", http.StatusServiceUnavailable,
		map[string]any{"code": "test_boom", "message": "down"})
	_, err = provider.GetOrder(context.Background(), "104")
	var providerErr *commerce.ProviderError
	if !asCommerceProviderError(err, &providerErr) || !providerErr.Retryable() {
		t.Fatalf("retryable: %v", err)
	}
}

func TestWooOrderMoneyMatrix(t *testing.T) {
	base := func(total string) map[string]any {
		fixture := wooOrderFixture(110, "pending")
		fixture["total"] = total
		return fixture
	}
	cases := []struct {
		name    string
		total   string
		want    int64
		wantErr bool
	}{
		{"zero", "0.00", 0, false},
		{"exact", "13.00", 1300, false},
		{"piastres", "650.25", 65025, false},
		{"large", "90071992547409.93", 9007199254740993, false},
		{"overflow", "99999999999999999999.00", 0, true},
		{"precision", "10.123", 0, true},
		{"malformed", "abc", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			harness := newWooHarness(t, testConsumerKey, testConsumerSec)
			harness.preloadOrder(110, base(tc.total))
			provider := testProvider(t, harness)
			snapshot, err := provider.GetOrder(context.Background(), "110")
			if tc.wantErr {
				if _, blocked := orders.IsBlocked(err); !blocked {
					t.Fatalf("blocked: %v %v", snapshot, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.TotalMinor != tc.want {
				t.Fatalf("total: %d want %d", snapshot.TotalMinor, tc.want)
			}
		})
	}
}

func TestWooOrderCurrencyGate(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	fixture := wooOrderFixture(111, "pending")
	fixture["currency"] = "EUR"
	harness.preloadOrder(111, fixture)
	provider := testProvider(t, harness)
	_, err := provider.GetOrder(context.Background(), "111")
	if code, blocked := orders.IsBlocked(err); !blocked || code != orders.CodeOrderCurrencyUnsupported {
		t.Fatalf("currency: %v", err)
	}
}

func TestWooOrderLineMatrix(t *testing.T) {
	line := func(overrides map[string]any) map[string]any {
		base := map[string]any{
			"id": float64(1), "name": "بردية", "product_id": float64(500),
			"variation_id": float64(0), "quantity": float64(1),
			"subtotal": "650.00", "subtotal_tax": "0.00",
			"total": "650.00", "total_tax": "0.00", "sku": "PAP-001",
		}
		for key, value := range overrides {
			base[key] = value
		}
		return base
	}
	newHarness := func(t *testing.T, lines []any) (*wooHarness, *WooCommerceProvider) {
		harness := newWooHarness(t, testConsumerKey, testConsumerSec)
		fixture := wooOrderFixture(112, "pending")
		fixture["line_items"] = lines
		harness.preloadOrder(112, fixture)
		return harness, testProvider(t, harness)
	}
	t.Run("variation preserved unmapped", func(t *testing.T) {
		harness, provider := newHarness(t, []any{line(map[string]any{"variation_id": float64(7)})})
		_ = harness
		snapshot, err := provider.GetOrder(context.Background(), "112")
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Lines) != 1 || snapshot.Lines[0].VariationID != 7 ||
			snapshot.Lines[0].UnsupportedReason != "variation" {
			t.Fatalf("variation: %+v", snapshot.Lines)
		}
	})
	t.Run("zero quantity blocked", func(t *testing.T) {
		_, provider := newHarness(t, []any{line(map[string]any{"quantity": float64(0)})})
		if _, err := provider.GetOrder(context.Background(), "112"); err == nil {
			t.Fatal("zero quantity must fail")
		} else if code, blocked := orders.IsBlocked(err); !blocked || code != orders.CodeOrderInvalid {
			t.Fatalf("invalid: %v", err)
		}
	})
	t.Run("unmapped manual product retained", func(t *testing.T) {
		_, provider := newHarness(t, []any{line(map[string]any{"product_id": float64(0), "sku": "MANUAL"})})
		snapshot, err := provider.GetOrder(context.Background(), "112")
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Lines) != 1 || snapshot.Lines[0].ExternalProductID != "" || snapshot.Lines[0].Mapped {
			t.Fatalf("manual line: %+v", snapshot.Lines)
		}
	})
}

// TestWooGetOrderStatusMatrix proves exact HTTP status semantics for
// order reads through the real client: only 404 becomes
// ORDER_NOT_FOUND; 409 stays Conflict and can never read as absence.
func TestWooGetOrderStatusMatrix(t *testing.T) {
	newProvider := func(t *testing.T) (*WooCommerceProvider, *wooHarness) {
		t.Helper()
		harness := newWooHarness(t, testConsumerKey, testConsumerSec)
		return testProvider(t, harness), harness
	}
	wooErr := func(code, message string) map[string]any {
		return map[string]any{"code": code, "message": message}
	}
	t.Run("404 not found", func(t *testing.T) {
		provider, _ := newProvider(t)
		_, err := provider.GetOrder(context.Background(), "999")
		if code, blocked := orders.IsBlocked(err); !blocked || code != orders.CodeOrderNotFound {
			t.Fatalf("404 must be ORDER_NOT_FOUND: %v", err)
		}
	})
	t.Run("409 conflict is not absence", func(t *testing.T) {
		provider, harness := newProvider(t)
		harness.fail(http.MethodGet, "/wp-json/wc/v3/orders/500", http.StatusConflict,
			wooErr("test_conflict", "Clash."))
		_, err := provider.GetOrder(context.Background(), "500")
		var providerErr *commerce.ProviderError
		if !asCommerceProviderError(err, &providerErr) || providerErr.Kind != commerce.ErrorConflict {
			t.Fatalf("409 must stay generic Conflict: %v", err)
		}
		if code, blocked := orders.IsBlocked(err); blocked && code == orders.CodeOrderNotFound {
			t.Fatal("409 must never read as ORDER_NOT_FOUND")
		}
	})
	t.Run("400 validation", func(t *testing.T) {
		provider, harness := newProvider(t)
		harness.fail(http.MethodGet, "/wp-json/wc/v3/orders/501", http.StatusBadRequest,
			wooErr("rest_no_route", "No route."))
		_, err := provider.GetOrder(context.Background(), "501")
		var providerErr *commerce.ProviderError
		if !asCommerceProviderError(err, &providerErr) || providerErr.Kind != commerce.ErrorValidation {
			t.Fatalf("400 must be Validation: %v", err)
		}
	})
	t.Run("401 authentication", func(t *testing.T) {
		provider, harness := newProvider(t)
		harness.fail(http.MethodGet, "/wp-json/wc/v3/orders/502", http.StatusUnauthorized,
			wooErr("rest_forbidden", "Denied."))
		_, err := provider.GetOrder(context.Background(), "502")
		var providerErr *commerce.ProviderError
		if !asCommerceProviderError(err, &providerErr) || providerErr.Kind != commerce.ErrorAuthentication {
			t.Fatalf("401 must be Authentication: %v", err)
		}
	})
	t.Run("408 temporary", func(t *testing.T) {
		provider, harness := newProvider(t)
		harness.fail(http.MethodGet, "/wp-json/wc/v3/orders/503", http.StatusRequestTimeout,
			wooErr("timeout", "Slow."))
		_, err := provider.GetOrder(context.Background(), "503")
		var providerErr *commerce.ProviderError
		if !asCommerceProviderError(err, &providerErr) || !providerErr.Retryable() {
			t.Fatalf("408 must be retryable: %v", err)
		}
	})
	t.Run("422 validation", func(t *testing.T) {
		provider, harness := newProvider(t)
		harness.fail(http.MethodGet, "/wp-json/wc/v3/orders/504", http.StatusUnprocessableEntity,
			wooErr("rest_invalid_param", "Bad param."))
		_, err := provider.GetOrder(context.Background(), "504")
		var providerErr *commerce.ProviderError
		if !asCommerceProviderError(err, &providerErr) || providerErr.Kind != commerce.ErrorValidation {
			t.Fatalf("422 must be Validation: %v", err)
		}
	})
	t.Run("429 rate limited", func(t *testing.T) {
		provider, harness := newProvider(t)
		harness.failHeaders(http.MethodGet, "/wp-json/wc/v3/orders/505", http.StatusTooManyRequests,
			wooErr("throttled", "Slow down."), map[string]string{"Retry-After": "7"})
		_, err := provider.GetOrder(context.Background(), "505")
		var providerErr *commerce.ProviderError
		if !asCommerceProviderError(err, &providerErr) || providerErr.Kind != commerce.ErrorRateLimited {
			t.Fatalf("429 must be RateLimited: %v", err)
		}
	})
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			provider, harness := newProvider(t)
			harness.fail(http.MethodGet, "/wp-json/wc/v3/orders/506", status,
				wooErr("test_boom", "Down."))
			_, err := provider.GetOrder(context.Background(), "506")
			var providerErr *commerce.ProviderError
			if !asCommerceProviderError(err, &providerErr) || !providerErr.Retryable() {
				t.Fatalf("%d must be retryable: %v", status, err)
			}
		})
	}
	t.Run("network failure temporary", func(t *testing.T) {
		provider, harness := newProvider(t)
		harness.server.Close()
		_, err := provider.GetOrder(context.Background(), "507")
		var providerErr *commerce.ProviderError
		if !asCommerceProviderError(err, &providerErr) || !providerErr.Retryable() {
			t.Fatalf("network failure must be retryable: %v", err)
		}
	})
	t.Run("caller cancellation preserved", func(t *testing.T) {
		provider, _ := newProvider(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := provider.GetOrder(ctx, "508")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation must propagate: %v", err)
		}
	})
}
