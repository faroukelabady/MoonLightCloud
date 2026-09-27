package woocommerce

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// TestWooErrorMatrix proves deterministic status classification through
// the real client against the TLS harness.
func TestWooErrorMatrix(t *testing.T) {
	wooErr := func(code, message string, status int) map[string]any {
		return map[string]any{"code": code, "message": message, "data": map[string]any{"status": status}}
	}
	cases := []struct {
		name      string
		status    int
		body      any
		wantKind  commerce.ErrorKind
		wantRetry bool
	}{
		{"400 validation", http.StatusBadRequest, wooErr("rest_invalid_param", "Invalid parameter.", 400), commerce.ErrorValidation, false},
		{"422 validation", http.StatusUnprocessableEntity, wooErr("woocommerce_rest_invalid", "No.", 422), commerce.ErrorValidation, false},
		{"duplicate sku conflict", http.StatusBadRequest, wooErr("product_invalid_sku", "Invalid or duplicated SKU.", 400), commerce.ErrorConflict, false},
		{"401 auth", http.StatusUnauthorized, wooErr("woocommerce_rest_authentication_error", "Consumer key is missing.", 401), commerce.ErrorAuthentication, false},
		{"403 auth", http.StatusForbidden, wooErr("woocommerce_rest_forbidden", "Sorry.", 403), commerce.ErrorAuthentication, false},
		{"404 mapped conflict", http.StatusNotFound, wooErr("woocommerce_rest_term_invalid", "Resource doesn't exist.", 404), commerce.ErrorConflict, false},
		{"408 temporary", http.StatusRequestTimeout, wooErr("test_timeout", "Slow.", 408), commerce.ErrorTemporary, true},
		{"409 conflict", http.StatusConflict, wooErr("test_conflict", "Clash.", 409), commerce.ErrorConflict, false},
		{"429 rate limited", http.StatusTooManyRequests, wooErr("test_limit", "Slow down.", 429), commerce.ErrorRateLimited, true},
		{"500 temporary", http.StatusInternalServerError, wooErr("test_boom", "Boom.", 500), commerce.ErrorTemporary, true},
		{"502 temporary", http.StatusBadGateway, wooErr("test_bg", "Bad.", 502), commerce.ErrorTemporary, true},
		{"503 temporary", http.StatusServiceUnavailable, wooErr("test_un", "Down.", 503), commerce.ErrorTemporary, true},
		{"empty body 500", http.StatusInternalServerError, nil, commerce.ErrorTemporary, true},
		{"non-json 500", http.StatusInternalServerError, "not json at all {{{", commerce.ErrorTemporary, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			harness := newWooHarness(t, testConsumerKey, testConsumerSec)
			harness.preload(500, map[string]any{
				"sku": "PAP-001",
				"meta_data": []any{
					map[string]any{"key": "_moonlight_product_id", "value": "mx-1"},
					map[string]any{"key": "_moonlight_provider_key", "value": testProviderKey},
				},
			})
			// Fail the metadata PUT: exercises classification on the
			// update path with valid ownership.
			harness.fail(http.MethodPut, "/wp-json/wc/v3/products/500", tc.status, tc.body)
			provider := testProvider(t, harness)
			product := testProduct("mx-1")
			_, err := provider.UpsertProduct(context.Background(),
				testUpsertReq(product, true, &commerce.ProviderProductRef{ExternalProductID: "500"}))
			var providerErr *commerce.ProviderError
			if !asProviderError(err, &providerErr) {
				t.Fatalf("classified: %v", err)
			}
			if providerErr.Kind != tc.wantKind || providerErr.Retryable() != tc.wantRetry {
				t.Fatalf("got %v retryable=%v, want %v retryable=%v (%v)",
					providerErr.Kind, providerErr.Retryable(), tc.wantKind, tc.wantRetry, err)
			}
			if strings.Contains(providerErr.Error(), testConsumerSec) || strings.Contains(providerErr.Error(), testConsumerKey) {
				t.Fatalf("secret in error: %v", err)
			}
		})
	}
}

// TestWooMalformedSuccess proves a 2xx without a usable product id is a
// safe temporary failure (the remote operation may have occurred).
func TestWooMalformedSuccess(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	harness.setIntercept(func(record wooRecordedRequest) (int, any, bool) {
		if record.Method == http.MethodPost {
			return http.StatusOK, map[string]any{"name": "no id here"}, true
		}
		return 0, nil, false
	})
	provider := testProvider(t, harness)
	_, err := provider.UpsertProduct(context.Background(), testUpsertReq(testProduct("mal-1"), true, nil))
	var providerErr *commerce.ProviderError
	if !asProviderError(err, &providerErr) || !providerErr.Retryable() {
		t.Fatalf("malformed success: %v", err)
	}
}

// TestWooTimeout proves client timeouts surface as temporary failures.
func TestWooTimeout(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	harness.setIntercept(func(record wooRecordedRequest) (int, any, bool) {
		time.Sleep(2 * time.Second)
		return http.StatusOK, map[string]any{"id": float64(500)}, true
	})
	cfg := testConfig(harness)
	cfg.HTTPTimeout = 200 * time.Millisecond
	provider, err := NewWooCommerceProvider(cfg, harness.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.UpsertProduct(context.Background(), testUpsertReq(testProduct("to-1"), true, nil))
	var providerErr *commerce.ProviderError
	if !asProviderError(err, &providerErr) || !providerErr.Retryable() {
		t.Fatalf("timeout: %v", err)
	}
}

// TestWooContextCancel proves caller cancellation is preserved unwrapped.
func TestWooContextCancel(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	harness.setIntercept(func(record wooRecordedRequest) (int, any, bool) {
		time.Sleep(2 * time.Second)
		return http.StatusOK, map[string]any{"id": float64(500)}, true
	})
	provider := testProvider(t, harness)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := provider.UpsertProduct(ctx, testUpsertReq(testProduct("cx-1"), true, nil))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation preserved: %v", err)
	}
	err = provider.SetInventory(ctx, testInventoryReq("cx-1", "500", 1, true))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("inventory cancellation: %v", err)
	}
}

// TestWooRetryAfterParsing proves delta-seconds and HTTP-date handling
// with bounds and graceful garbage.
func TestWooRetryAfterParsing(t *testing.T) {
	if got := parseRetryAfter("30"); got != 30*time.Second {
		t.Fatalf("delta: %v", got)
	}
	future := time.Now().Add(90 * time.Second).UTC().Format(time.RFC1123)
	if got := parseRetryAfter(future); got <= 0 || got > 91*time.Second {
		t.Fatalf("date: %v", got)
	}
	if got := parseRetryAfter("not-a-time"); got != 0 {
		t.Fatalf("garbage: %v", got)
	}
	if got := parseRetryAfter("-5"); got != maxRetryAfter {
		t.Fatalf("negative bounded: %v", got)
	}
	if got := parseRetryAfter("999999999"); got != maxRetryAfter {
		t.Fatalf("absurd bounded: %v", got)
	}
	// A 429 carrying Retry-After reaches provider metadata end to end.
	harness3 := newWooHarness(t, testConsumerKey, testConsumerSec)
	harness3.preload(500, map[string]any{
		"sku": "PAP-001",
		"meta_data": []any{
			map[string]any{"key": "_moonlight_product_id", "value": "rl-2"},
			map[string]any{"key": "_moonlight_provider_key", "value": testProviderKey},
		},
	})
	harness3.failHeaders(http.MethodPut, "/wp-json/wc/v3/products/500", http.StatusTooManyRequests,
		map[string]any{"code": "test_limit", "message": "Slow.", "data": map[string]any{"status": 429}},
		map[string]string{"Retry-After": "45"})
	provider3 := testProvider(t, harness3)
	_, err := provider3.UpsertProduct(context.Background(),
		testUpsertReq(testProduct("rl-2"), true, &commerce.ProviderProductRef{ExternalProductID: "500"}))
	var limited *commerce.ProviderError
	if !asProviderError(err, &limited) || limited.Kind != commerce.ErrorRateLimited {
		t.Fatalf("rate limited: %v", err)
	}
	if after, ok := limited.GetRetryAfter(); !ok || after != 45*time.Second {
		t.Fatalf("retry-after metadata: %v %v", after, ok)
	}
}

// TestWooResponseBodyBound proves oversized responses fail bounded.
func TestWooResponseBodyBound(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	harness.setIntercept(func(record wooRecordedRequest) (int, any, bool) {
		if record.Method == http.MethodGet {
			return http.StatusOK, map[string]any{"padding": strings.Repeat("x", 2*1024*1024)}, true
		}
		return 0, nil, false
	})
	provider := testProvider(t, harness)
	_, err := provider.UpsertProduct(context.Background(), testUpsertReq(testProduct("big-1"), true, nil))
	var providerErr *commerce.ProviderError
	if !asProviderError(err, &providerErr) || !providerErr.Retryable() {
		t.Fatalf("bounded failure: %v", err)
	}
	if strings.Contains(providerErr.Error(), strings.Repeat("x", 100)) {
		t.Fatal("body must not leak into the error")
	}
}
