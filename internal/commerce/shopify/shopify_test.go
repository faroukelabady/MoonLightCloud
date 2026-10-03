package shopify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
)

func newTestProvider(t *testing.T, h *shopifyHarness) *ShopifyProvider {
	t.Helper()
	provider, err := NewShopifyProvider(testConfig(), harnessClient(t, h), fixtureCoordinator{})
	if err != nil {
		t.Fatalf("NewShopifyProvider: %v", err)
	}
	return provider
}

// Phase 11 §147/§18: construction and Cloud startup perform ZERO
// Shopify network calls; an unreachable Shopify endpoint must not
// prevent startup.
func TestStartupPerformsNoShopifyRequests(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	if len(h.recorded()) != 0 {
		t.Fatalf("startup issued %d shopify requests", len(h.recorded()))
	}
	if provider.Key() != commerce.ProviderKey("shopify-main") {
		t.Fatalf("key = %q", provider.Key())
	}
}

// Phase 11 §18: enabled + unreachable host must still construct.
func TestStartupSucceedsWhenShopifyUnreachable(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := dead.URL
	dead.Close()
	cfg := testConfig()
	cfg.ShopDomain = "unreachable.myshopify.com"
	provider, err := NewShopifyProvider(cfg, nil)
	if err != nil {
		t.Fatalf("construction must not contact shopify: %v", err)
	}
	_ = deadURL
	if provider == nil {
		t.Fatal("nil provider")
	}
}

// Phase 11 §146: constructor rejects invalid configuration eagerly.
func TestProviderConfigValidation(t *testing.T) {
	mutate := func(f func(*config.ShopifyConfig)) config.ShopifyConfig {
		cfg := testConfig()
		f(&cfg)
		return cfg
	}
	cases := map[string]func(*config.ShopifyConfig){
		"disabled":             func(c *config.ShopifyConfig) { c.Enabled = false },
		"bad provider key":     func(c *config.ShopifyConfig) { c.ProviderKey = "Shopify Main" },
		"scheme in domain":     func(c *config.ShopifyConfig) { c.ShopDomain = "https://test.myshopify.com" },
		"path in domain":       func(c *config.ShopifyConfig) { c.ShopDomain = "test.myshopify.com/admin" },
		"foreign host":         func(c *config.ShopifyConfig) { c.ShopDomain = "evil.example.com" },
		"whitespace domain":    func(c *config.ShopifyConfig) { c.ShopDomain = "test.myshopify.com " },
		"latest version":       func(c *config.ShopifyConfig) { c.APIVersion = "latest" },
		"unstable version":     func(c *config.ShopifyConfig) { c.APIVersion = "unstable" },
		"malformed version":    func(c *config.ShopifyConfig) { c.APIVersion = "2026-10-01" },
		"empty version":        func(c *config.ShopifyConfig) { c.APIVersion = "" },
		"missing token":        func(c *config.ShopifyConfig) { c.AccessToken = "" },
		"unsupported currency": func(c *config.ShopifyConfig) { c.Currency = "JPY" },
		"missing location":     func(c *config.ShopifyConfig) { c.LocationID = "" },
		"invalid location":     func(c *config.ShopifyConfig) { c.LocationID = "gid://shopify/Product/1" },
		"missing publication":  func(c *config.ShopifyConfig) { c.PublicationID = "" },
		"invalid publication":  func(c *config.ShopifyConfig) { c.PublicationID = "7700000002" },
	}
	for name, mutateCase := range cases {
		cfg := mutate(mutateCase)
		if _, err := NewShopifyProvider(cfg, nil); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

// Phase 11 §148/§19: authentication shape and redirect refusal.
func TestClientSendsTokenHeaderNeverURL(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	if err := provider.ensureShopCurrency(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, request := range h.recorded() {
		if request.Token != h.token {
			t.Fatalf("missing/incorrect access token header")
		}
		if strings.Contains(request.URL, h.token) || strings.Contains(request.URL, "access_token") {
			t.Fatalf("token leaked into URL: %s", request.URL)
		}
		if strings.Contains(request.Query, h.token) {
			t.Fatal("token leaked into query document")
		}
	}
}

// Phase 11 §19: 301/302/307/308 are refused and the redirect target
// receives zero requests (the credential is never forwarded).
func TestRedirectsAreRefused(t *testing.T) {
	for _, status := range []int{301, 302, 307, 308} {
		var targetHits atomic.Int64
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			targetHits.Add(1)
		}))
		h := newHarness(t)
		provider := newTestProvider(t, h)
		// First ensure currency succeeds and is cached; inject the
		// redirect on the next operation.
		h.mu.Lock()
		h.redirectTo = target.URL
		h.mu.Unlock()
		_, err := provider.UpsertProduct(context.Background(), commerce.ProductUpsertRequest{
			ProviderKey: "shopify-main", ProductID: "prod-1", OperationKey: "op-" + string(rune('0'+status)),
			Product: commerce.CommerceProduct{
				SKU:    "SKU-REDIRECT",
				Names:  []commerce.LocalizedName{{Locale: "en", Name: "Redirect"}},
				Prices: []commerce.Money{{Currency: "EGP", AmountMinor: 100}},
			},
		})
		if err == nil {
			t.Fatalf("status %d: redirect was followed", status)
		}
		if targetHits.Load() != 0 {
			t.Fatalf("status %d: redirect target received %d requests", status, targetHits.Load())
		}
		target.Close()
	}
}

// Phase 11 §22: HTTP 200 does not imply success — top-level GraphQL
// errors are classified separately from userErrors and data.
func TestGraphQLErrorsOnHTTP200AreFailures(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	h.setFailure("MoonlightProduct", 200,
		`{"errors":[{"message":"internal","extensions":{"code":"INTERNAL_SERVER_ERROR"}}]}`)
	_, err := provider.loadProduct(context.Background(), "gid://shopify/Product/1")
	if err == nil {
		t.Fatal("200 with errors[] treated as success")
	}
	providerErr, ok := err.(*commerce.ProviderError)
	if !ok || providerErr.Kind != commerce.ErrorTemporary {
		t.Fatalf("want temporary provider error, got %v", err)
	}
}

func TestThrottledIsRateLimitedWithBoundedHint(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	h.setFailure("MoonlightProduct", 200, `{"errors":[{"message":"Throttled","extensions":{"code":"THROTTLED"}}],`+
		`"extensions":{"cost":{"requestedQueryCost":100,"currentlyAvailable":10,"throttleStatus":{"restoreRate":50}}}}`)
	_, err := provider.loadProduct(context.Background(), "gid://shopify/Product/1")
	providerErr, ok := err.(*commerce.ProviderError)
	if !ok || providerErr.Kind != commerce.ErrorRateLimited {
		t.Fatalf("want rate limited, got %v", err)
	}
	if !providerErr.Retryable() {
		t.Fatal("throttled must be retryable")
	}
	if after, present := providerErr.GetRetryAfter(); !present || after <= 0 {
		t.Fatalf("expected bounded retry hint, got %v", after)
	}
}

func TestResponseBodiesAreBounded(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	h.setFailure("MoonlightProduct", 200, `{"data":{"product":{"id":"`+strings.Repeat("x", maxResponseBytes+10)+`"}}}`)
	if _, err := provider.loadProduct(context.Background(), "gid://shopify/Product/1"); err == nil {
		t.Fatal("oversized response accepted")
	}
}

func TestMalformedJSONIsTemporary(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	h.setFailure("MoonlightProduct", 200, `{"data":`)
	_, err := provider.loadProduct(context.Background(), "gid://shopify/Product/1")
	providerErr, ok := err.(*commerce.ProviderError)
	if !ok || providerErr.Kind != commerce.ErrorTemporary {
		t.Fatalf("want temporary, got %v", err)
	}
}

func TestHTTPStatusClassification(t *testing.T) {
	cases := []struct {
		status int
		kind   commerce.ErrorKind
	}{
		{401, commerce.ErrorAuthentication},
		{403, commerce.ErrorAuthentication},
		{429, commerce.ErrorRateLimited},
		{500, commerce.ErrorTemporary},
		{503, commerce.ErrorTemporary},
		{400, commerce.ErrorValidation},
	}
	h := newHarness(t)
	provider := newTestProvider(t, h)
	for _, tc := range cases {
		h.setFailure("MoonlightProduct", tc.status, `{"errors":[{"message":"x"}]}`)
		_, err := provider.loadProduct(context.Background(), "gid://shopify/Product/1")
		providerErr, ok := err.(*commerce.ProviderError)
		if !ok || providerErr.Kind != tc.kind {
			t.Fatalf("status %d: got %v want kind %s", tc.status, err, tc.kind)
		}
	}
}

// Serial adapter fixtures test wire behavior; distributed coordination is tested
// separately with independent instances and real PostgreSQL.
type fixtureCoordinator struct{}

func (fixtureCoordinator) WithProductSync(ctx context.Context, key commerce.ProviderKey, product string, work func(context.Context) error) error {
	return work(commerce.WithProductMutationBarrier(commerce.CoordinatedProductContext(ctx, key, product), fixtureMutationBarrier{}))
}

// Wire-only fixtures do not claim durable uncertainty proof.
type fixtureMutationBarrier struct{}

func (fixtureMutationBarrier) Begin(context.Context, string) (string, error) {
	return "fixture-token", nil
}
func (fixtureMutationBarrier) Complete(context.Context, string) error { return nil }
