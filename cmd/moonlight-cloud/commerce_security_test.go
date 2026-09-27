package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/woocommerce"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
)

// cliWooStub is a minimal TLS Woo stub for CLI-level security tests:
// auth-gated product create/get/update with scripted inventory behavior.
type cliWooStub struct {
	t        *testing.T
	server   *httptest.Server
	mu       sync.Mutex
	requests int
	// inventoryMode selects the PUT /products/500 behavior for
	// inventory-shape updates: "ok", "empty-id", or "secret-error".
	inventoryMode string
	secret        string
}

func newCLIWooStub(t *testing.T, key, secret, inventoryMode string) *cliWooStub {
	t.Helper()
	stub := &cliWooStub{t: t, inventoryMode: inventoryMode, secret: secret}
	stub.server = httptest.NewTLSServer(http.HandlerFunc(stub.serve))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *cliWooStub) serve(w http.ResponseWriter, r *http.Request) {
	username, password, ok := r.BasicAuth()
	if !ok || username == "" || password == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	s.mu.Lock()
	s.requests++
	s.mu.Unlock()
	write := func(status int, value any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(value)
	}
	meta := []any{
		map[string]any{"key": "_moonlight_product_id", "value": "cli-1"},
		map[string]any{"key": "_moonlight_provider_key", "value": "website"},
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/wp-json/wc/v3/products":
		write(http.StatusOK, []any{})
	case r.Method == http.MethodPost && r.URL.Path == "/wp-json/wc/v3/products":
		product := map[string]any{}
		for key, value := range parsed {
			product[key] = value
		}
		product["id"] = float64(500)
		write(http.StatusCreated, product)
	case r.Method == http.MethodGet && r.URL.Path == "/wp-json/wc/v3/products/500":
		write(http.StatusOK, map[string]any{"id": float64(500), "sku": "PAP-CLI", "meta_data": meta})
	case r.Method == http.MethodPut && r.URL.Path == "/wp-json/wc/v3/products/500":
		if _, hasName := parsed["name"]; hasName {
			write(http.StatusOK, map[string]any{"id": float64(500), "sku": "PAP-CLI", "meta_data": meta})
			return
		}
		switch s.inventoryMode {
		case "empty-id":
			write(http.StatusOK, map[string]any{"stock_quantity": 7})
		case "boundary-error":
			write(http.StatusBadRequest, map[string]any{
				"code":    "woocommerce_rest_error",
				"message": strings.Repeat("x", 195) + s.secret,
				"data":    map[string]any{"status": 400},
			})
		case "secret-error":
			write(http.StatusBadRequest, map[string]any{
				"code":    "woocommerce_rest_error",
				"message": "request failed for " + s.secret,
				"data":    map[string]any{"status": 400},
			})
		default:
			write(http.StatusOK, map[string]any{"id": float64(500), "meta_data": meta})
		}
	default:
		write(http.StatusNotFound, map[string]any{"code": "rest_no_route", "message": "no route"})
	}
}

func cliServiceWithStub(t *testing.T, stub *cliWooStub, key, secret string) *commerce.CommerceService {
	t.Helper()
	provider, err := woocommerce.NewWooCommerceProvider(config.WooCommerceConfig{
		Enabled: true, ProviderKey: "website", BaseURL: stub.server.URL,
		ConsumerKey: key, ConsumerSecret: secret,
		Currency: config.WooCurrencyEGP, DimensionUnit: config.WooDimensionCM,
		HTTPTimeout: testTimeout(t),
	}, stub.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	registry := commerce.NewRegistry()
	if err := registry.Register("website", provider); err != nil {
		t.Fatal(err)
	}
	return commerce.NewCommerceService(registry, &cliStubMappings{},
		&cliStubSource{state: cliDesiredState(true, 7, true)}, nil)
}

func testTimeout(t *testing.T) time.Duration {
	t.Helper()
	return 5 * time.Second
}

func TestCommerceCLIReflectedSecretSafe(t *testing.T) {
	const key, secret = "ck-review-secret-key", "sk-review-super-secret"
	stub := newCLIWooStub(t, key, secret, "secret-error")
	service := cliServiceWithStub(t, stub, key, secret)
	var stdout bytes.Buffer
	err := runCommerceSync(context.Background(), service, &stdout, "website", "cli-1")
	if err == nil {
		t.Fatal("expected the injected failure")
	}
	// The CLI prints errors to stdout; main() echoes the same string to
	// stderr. Both paths carry the sanitized error only.
	assertNoSecret(t, "stdout", stdout.String(), key, secret)
	assertNoSecret(t, "error", err.Error(), key, secret)
	var providerErr *commerce.ProviderError
	if !isCommerceProviderError(err, &providerErr) || providerErr.Kind != commerce.ErrorValidation {
		t.Fatalf("classification preserved: %v", err)
	}
}

func TestCommerceCLIAmbiguousInventory(t *testing.T) {
	stub := newCLIWooStub(t, "ck", "cs", "empty-id")
	service := cliServiceWithStub(t, stub, "ck", "cs")
	var stdout bytes.Buffer
	err := runCommerceSync(context.Background(), service, &stdout, "website", "cli-1")
	if err == nil {
		t.Fatal("ambiguous inventory must fail")
	}
	var providerErr *commerce.ProviderError
	if !isCommerceProviderError(err, &providerErr) || !providerErr.Retryable() {
		t.Fatalf("temporary: %v", err)
	}
	if strings.Contains(stdout.String(), "inventory_updated=true") {
		t.Fatalf("must not report success: %q", stdout.String())
	}
	assertNoSecret(t, "stdout", stdout.String(), "ck", "cs")
}

// TestCommerceCmdMaliciousConfigSafe proves the exact CLI surface
// rejects a credential-bearing invalid configuration without echoing
// secrets to stdout, stderr, or the error (no DB is touched: config
// loading fails first).
// TestCommerceCLIBoundaryLeak runs the real sync-product path against a
// hostile error whose credential crosses the 200-char diagnostic
// boundary. Both captured streams must lack the full secret and the
// boundary-created fragment, with classification intact.
func TestCommerceCLIBoundaryLeak(t *testing.T) {
	const key, secret = "ck-review-secret-key", "sk-review-super-secret"
	stub := newCLIWooStub(t, key, secret, "boundary-error")
	service := cliServiceWithStub(t, stub, key, secret)
	var stdout bytes.Buffer
	err := runCommerceSync(context.Background(), service, &stdout, "website", "cli-1")
	if err == nil {
		t.Fatal("expected the injected failure")
	}
	assertNoSecret(t, "stdout", stdout.String(), key, secret)
	if strings.Contains(stdout.String(), "sk-re") {
		t.Fatalf("boundary fragment leaks in: %q", stdout.String())
	}
	var providerErr *commerce.ProviderError
	if !isCommerceProviderError(err, &providerErr) || providerErr.Kind != commerce.ErrorValidation {
		t.Fatalf("classification preserved: %v", err)
	}
}

func TestCommerceCmdMaliciousConfigSafe(t *testing.T) {
	t.Setenv("ENVIRONMENT", "development")
	t.Setenv("ALLOW_UNAUTHENTICATED_REPORTING", "true")
	t.Setenv("DATABASE_URL", "postgres://u@localhost:5432/x?sslmode=disable")
	t.Setenv("COMMERCE_WOO_ENABLED", "true")
	t.Setenv("COMMERCE_WOO_PROVIDER_KEY", "website")
	t.Setenv("COMMERCE_WOO_BASE_URL", "http://ck-review:sk-review@example.com")
	t.Setenv("COMMERCE_WOO_CONSUMER_KEY", "ck-review")
	t.Setenv("COMMERCE_WOO_CONSUMER_SECRET", "sk-review")
	t.Setenv("COMMERCE_WOO_CURRENCY", "EGP")
	t.Setenv("COMMERCE_WOO_DIMENSION_UNIT", "cm")
	var stdout, stderr bytes.Buffer
	err := commerceCmd([]string{"sync-product", "--provider", "website",
		"--product", "11111111-1111-4111-8111-111111111111"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("malicious config must fail")
	}
	assertNoSecret(t, "stdout", stdout.String(), "ck-review", "sk-review")
	assertNoSecret(t, "stderr", stderr.String(), "ck-review", "sk-review")
	assertNoSecret(t, "error", err.Error(), "ck-review", "sk-review")
}

func isCommerceProviderError(err error, target **commerce.ProviderError) bool {
	return errors.As(err, target)
}

func assertNoSecret(t *testing.T, what, text, key, secret string) {
	t.Helper()
	for _, forbidden := range []string{key, secret} {
		if forbidden != "" && strings.Contains(text, forbidden) {
			t.Fatalf("%s leaks %q in: %q", what, forbidden, text)
		}
	}
}
