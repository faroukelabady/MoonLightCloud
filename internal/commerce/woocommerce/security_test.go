package woocommerce

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// Review credentials for the H regression battery. Synthetic values
// only; the secret exercises URL-escaped and Basic-token forms.
const (
	reviewKey    = "ck-review-secret-key"
	reviewSecret = "sk-review super/secret?=x"
)

func reviewProvider(t *testing.T, harness *wooHarness) *WooCommerceProvider {
	t.Helper()
	cfg := testConfig(harness)
	cfg.ConsumerKey = reviewKey
	cfg.ConsumerSecret = reviewSecret
	provider, err := NewWooCommerceProvider(cfg, harness.server.Client())
	if err != nil {
		t.Fatalf("provider: %v", err)
	}
	return provider
}

func reviewMappedProduct() (string, map[string]any) {
	return "review-1", map[string]any{
		"sku": "PAP-001",
		"meta_data": []any{
			map[string]any{"key": "_moonlight_product_id", "value": "review-1"},
			map[string]any{"key": "_moonlight_provider_key", "value": testProviderKey},
		},
	}
}

func assertSecretAbsent(t *testing.T, what, text string) {
	t.Helper()
	basic := base64.StdEncoding.EncodeToString([]byte(reviewKey + ":" + reviewSecret))
	for _, forbidden := range []string{
		reviewKey, reviewSecret,
		url.QueryEscape(reviewKey), url.QueryEscape(reviewSecret),
		basic, "Basic " + basic,
	} {
		if forbidden != "" && strings.Contains(text, forbidden) {
			t.Fatalf("%s leaks %q in: %q", what, forbidden, text)
		}
	}
}

// TestWooReflectedSecretRedacted proves a hostile Woo error message
// carrying the consumer secret never reaches operator-visible output,
// with classification preserved.
func TestWooReflectedSecretRedacted(t *testing.T) {
	harness := newWooHarness(t, reviewKey, reviewSecret)
	productID, preloaded := reviewMappedProduct()
	harness.preload(500, preloaded)
	harness.fail(http.MethodPut, "/wp-json/wc/v3/products/500", http.StatusBadRequest,
		map[string]any{
			"code":    "woocommerce_rest_error",
			"message": "request failed for " + reviewSecret,
			"data":    map[string]any{"status": 400},
		})
	provider := reviewProvider(t, harness)
	_, err := provider.UpsertProduct(context.Background(),
		testUpsertReq(testProduct(productID), true, &commerce.ProviderProductRef{ExternalProductID: "500"}))
	if err == nil {
		t.Fatal("expected the injected failure")
	}
	assertSecretAbsent(t, "ProviderError", err.Error())
	var providerErr *commerce.ProviderError
	if !asProviderError(err, &providerErr) || providerErr.Kind != commerce.ErrorValidation {
		t.Fatalf("classification preserved: %v", err)
	}
}

// TestWooReflectedKeyRedacted treats the consumer key as sensitive too.
func TestWooReflectedKeyRedacted(t *testing.T) {
	harness := newWooHarness(t, reviewKey, reviewSecret)
	productID, preloaded := reviewMappedProduct()
	harness.preload(500, preloaded)
	harness.fail(http.MethodPut, "/wp-json/wc/v3/products/500", http.StatusBadRequest,
		map[string]any{
			"code":    reviewKey,
			"message": "key " + reviewKey + " rejected",
			"data":    map[string]any{"status": 400},
		})
	provider := reviewProvider(t, harness)
	_, err := provider.UpsertProduct(context.Background(),
		testUpsertReq(testProduct(productID), true, &commerce.ProviderProductRef{ExternalProductID: "500"}))
	if err == nil {
		t.Fatal("expected the injected failure")
	}
	assertSecretAbsent(t, "ProviderError", err.Error())
}

// TestWooReflectedBasicTokenRedacted proves a reflected Authorization
// value (and both credentials composing it) never escapes.
func TestWooReflectedBasicTokenRedacted(t *testing.T) {
	harness := newWooHarness(t, reviewKey, reviewSecret)
	productID, preloaded := reviewMappedProduct()
	harness.preload(500, preloaded)
	basic := base64.StdEncoding.EncodeToString([]byte(reviewKey + ":" + reviewSecret))
	harness.fail(http.MethodPut, "/wp-json/wc/v3/products/500", http.StatusInternalServerError,
		map[string]any{
			"code":    "test_boom",
			"message": "upstream saw Basic " + basic,
			"data":    map[string]any{"status": 500},
		})
	provider := reviewProvider(t, harness)
	_, err := provider.UpsertProduct(context.Background(),
		testUpsertReq(testProduct(productID), true, &commerce.ProviderProductRef{ExternalProductID: "500"}))
	if err == nil {
		t.Fatal("expected the injected failure")
	}
	assertSecretAbsent(t, "ProviderError", err.Error())
	var providerErr *commerce.ProviderError
	if !asProviderError(err, &providerErr) || !providerErr.Retryable() {
		t.Fatalf("classification preserved: %v", err)
	}
}

// TestWooEncodedSecretRedacted covers the URL-escaped credential form:
// the secret contains characters so its raw and escaped forms differ,
// and both must redact. Supported forms are documented here:
// raw, URL-escaped, percent-decoded, and Basic (raw and header form).
func TestWooEncodedSecretRedacted(t *testing.T) {
	harness := newWooHarness(t, reviewKey, reviewSecret)
	productID, preloaded := reviewMappedProduct()
	harness.preload(500, preloaded)
	escaped := url.QueryEscape(reviewSecret)
	if escaped == reviewSecret {
		t.Fatal("review secret must exercise escaping")
	}
	harness.fail(http.MethodPut, "/wp-json/wc/v3/products/500", http.StatusBadRequest,
		map[string]any{
			"code":    "test_bad",
			"message": "bad credential " + escaped,
			"data":    map[string]any{"status": 400},
		})
	provider := reviewProvider(t, harness)
	_, err := provider.UpsertProduct(context.Background(),
		testUpsertReq(testProduct(productID), true, &commerce.ProviderProductRef{ExternalProductID: "500"}))
	if err == nil {
		t.Fatal("expected the injected failure")
	}
	assertSecretAbsent(t, "ProviderError", err.Error())
}

// TestWooRateLimitedSecretRedacted proves sanitization preserves
// RetryAfter metadata while redacting the message.
func TestWooRateLimitedSecretRedacted(t *testing.T) {
	harness := newWooHarness(t, reviewKey, reviewSecret)
	productID, preloaded := reviewMappedProduct()
	harness.preload(500, preloaded)
	harness.failHeaders(http.MethodPut, "/wp-json/wc/v3/products/500", http.StatusTooManyRequests,
		map[string]any{
			"code":    "test_limit",
			"message": "slow down " + reviewSecret,
			"data":    map[string]any{"status": 429},
		},
		map[string]string{"Retry-After": "45"})
	provider := reviewProvider(t, harness)
	_, err := provider.UpsertProduct(context.Background(),
		testUpsertReq(testProduct(productID), true, &commerce.ProviderProductRef{ExternalProductID: "500"}))
	var providerErr *commerce.ProviderError
	if !asProviderError(err, &providerErr) || providerErr.Kind != commerce.ErrorRateLimited {
		t.Fatalf("classification preserved: %v", err)
	}
	assertSecretAbsent(t, "ProviderError", err.Error())
}
