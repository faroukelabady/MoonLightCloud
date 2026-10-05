package woocommerce

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// stubMappingRepo is a minimal in-memory mapping repository for
// orchestration-level recovery tests.
type stubMappingRepo struct {
	rows     map[string]commerce.ProductMapping
	failNext int
}

func (s *stubMappingRepo) key(key commerce.ProviderKey, productID string) string {
	return string(key) + "/" + productID
}

func (s *stubMappingRepo) GetProductMapping(_ context.Context, key commerce.ProviderKey, productID string) (commerce.ProductMapping, error) {
	mapping, ok := s.rows[s.key(key, productID)]
	if !ok {
		return commerce.ProductMapping{}, apperr.New(apperr.NotFound, "no mapping")
	}
	return mapping, nil
}

func (s *stubMappingRepo) GetProductConfigurationMapping(context.Context, commerce.ProviderKey, string, string) (commerce.ProductConfigurationMapping, error) {
	return commerce.ProductConfigurationMapping{}, errMappingNotFoundStub()
}

func (s *stubMappingRepo) UpdateProductMappingExternal(_ context.Context, providerKey commerce.ProviderKey, productID, expectedExternalID, newExternalID string) (commerce.ProductMapping, error) {
	return commerce.ProductMapping{ProviderKey: providerKey, ProductID: productID, ExternalProductID: newExternalID}, nil
}
func (s *stubMappingRepo) FindConfigurationByExternal(context.Context, commerce.ProviderKey, string, string) (commerce.ProductConfigurationMapping, error) {
	return commerce.ProductConfigurationMapping{}, errMappingNotFoundStub()
}
func (s *stubMappingRepo) UpsertProductConfigurationMapping(_ context.Context, providerKey commerce.ProviderKey, productID, configurationID, externalProductID, externalConfigurationID string) (commerce.ProductConfigurationMapping, error) {
	return commerce.ProductConfigurationMapping{ProviderKey: providerKey, ProductID: productID, ConfigurationID: configurationID, ExternalProductID: externalProductID, ExternalConfigurationID: externalConfigurationID}, nil
}

func (s *stubMappingRepo) FindByExternalProductID(_ context.Context, key commerce.ProviderKey, externalID string) (commerce.ProductMapping, error) {
	for _, mapping := range s.rows {
		if mapping.ProviderKey == key && mapping.ExternalProductID == externalID {
			return mapping, nil
		}
	}
	return commerce.ProductMapping{}, apperr.New(apperr.NotFound, "no mapping")
}

func (s *stubMappingRepo) CreateProductMapping(_ context.Context, key commerce.ProviderKey, productID, externalID string) (commerce.ProductMapping, error) {
	if s.failNext > 0 {
		s.failNext--
		return commerce.ProductMapping{}, errors.New("injected mapping persistence failure")
	}
	k := s.key(key, productID)
	if existing, ok := s.rows[k]; ok {
		if existing.ExternalProductID != externalID {
			return commerce.ProductMapping{}, apperr.New(apperr.Conflict, "mapping conflict")
		}
		return existing, nil
	}
	if s.rows == nil {
		s.rows = map[string]commerce.ProductMapping{}
	}
	mapping := commerce.ProductMapping{ProviderKey: key, ProductID: productID, ExternalProductID: externalID}
	s.rows[k] = mapping
	return mapping, nil
}

// stubSource serves one canned desired state.
type stubSource struct {
	state commerce.DesiredProduct
	err   error
}

func (s *stubSource) GetDesiredCommerceProduct(_ context.Context, _ string) (commerce.DesiredProduct, error) {
	if s.err != nil {
		return commerce.DesiredProduct{}, s.err
	}
	return s.state, nil
}

func enabledDesiredState(product commerce.CommerceProduct, quantity int64, ready bool) commerce.DesiredProduct {
	stock := int(quantity)
	return commerce.DesiredProduct{
		Product: product, Published: true,
		Availability: catalog.ProductAvailability{
			ProductID: product.ProductID, ProductActive: true, SellOnline: true,
			OnlineAvailable: int(quantity), Ready: ready,
			StockQuantity: &stock, InventoryRevision: 27,
		},
		CatalogRevision: 12, PolicyRevision: 4, InventoryRevision: 27,
	}
}

// recoveryService wires real orchestration over the real Woo adapter.
func recoveryService(harness *wooHarness, product commerce.CommerceProduct, quantity int64, ready bool, mappings *stubMappingRepo) (*commerce.CommerceService, *WooCommerceProvider) {
	provider, err := NewWooCommerceProvider(testConfig(harness), harness.server.Client())
	if err != nil {
		panic(err)
	}
	registry := commerce.NewRegistry()
	if err := registry.Register(provider.Key(), provider); err != nil {
		panic(err)
	}
	if mappings == nil {
		mappings = &stubMappingRepo{}
	}
	source := &stubSource{state: enabledDesiredState(product, quantity, ready)}
	return commerce.NewCommerceService(registry, mappings, source, nil), provider
}

func countPosts(harness *wooHarness) int {
	n := 0
	for _, request := range harness.recorded() {
		if request.Method == http.MethodPost && request.Path == "/wp-json/wc/v3/products" {
			n++
		}
	}
	return n
}

func countPuts(harness *wooHarness, id string) int {
	n := 0
	for _, request := range harness.recorded() {
		if request.Method == http.MethodPut && request.Path == "/wp-json/wc/v3/products/"+id {
			n++
		}
	}
	return n
}

func countInventoryPuts(harness *wooHarness) int {
	n := 0
	for _, request := range harness.recorded() {
		if request.Method != http.MethodPut || request.Path != "/wp-json/wc/v3/products/500" {
			continue
		}
		// Inventory updates carry no product metadata fields.
		if _, hasName := request.Body["name"]; !hasName {
			n++
		}
	}
	return n
}

func requireProviderConflict(t *testing.T, err error, what string) {
	t.Helper()
	var providerErr *commerce.ProviderError
	if !asProviderError(err, &providerErr) || providerErr.Kind != commerce.ErrorConflict {
		t.Fatalf("%s: %v", what, err)
	}
	if providerErr.Retryable() {
		t.Fatalf("%s must be terminal: %v", what, err)
	}
}

// TestWooMappingLossRecovery is the critical integration case: POST
// succeeds, mapping persistence fails (so no inventory call), and the
// retry recovers the same Woo product through SKU ownership lookup.
func TestWooMappingLossRecovery(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	ctx := context.Background()
	product := testProduct("loss-1")
	mappings := &stubMappingRepo{failNext: 1}
	service, _ := recoveryService(harness, product, 7, true, mappings)

	if _, err := service.SyncProduct(ctx, "website", "loss-1"); err == nil {
		t.Fatal("mapping failure must surface")
	}
	if countPosts(harness) != 1 {
		t.Fatalf("one post, got %d", countPosts(harness))
	}
	// No inventory call happened before the mapping persisted.
	for _, request := range harness.recorded() {
		if request.Method == http.MethodPut {
			t.Fatalf("no inventory before mapping: %+v", request)
		}
	}
	if _, err := mappings.GetProductMapping(ctx, "website", "loss-1"); err == nil {
		t.Fatal("mapping must be absent")
	}

	// Retry: SKU lookup finds the owned Woo product, no second POST.
	result, err := service.SyncProduct(ctx, "website", "loss-1")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if countPosts(harness) != 1 {
		t.Fatalf("no duplicate post, got %d", countPosts(harness))
	}
	if result.ExternalProductID != "500" {
		t.Fatalf("recovered id: %q", result.ExternalProductID)
	}
	stored, err := mappings.GetProductMapping(ctx, "website", "loss-1")
	if err != nil || stored.ExternalProductID != "500" {
		t.Fatalf("mapping final: %+v %v", stored, err)
	}
	// Inventory addressed exactly once, after the mapping persisted.
	puts := countInventoryPuts(harness)
	if puts != 1 {
		t.Fatalf("one inventory update, got %d", puts)
	}
}

// TestWooAmbiguousPostRecovery simulates a created-then-dropped
// connection: first call returns Temporary, retry recovers via SKU
// lookup with no duplicate creation.
func TestWooAmbiguousPostRecovery(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	harness.setDropCreate(true)
	ctx := context.Background()
	mappings := &stubMappingRepo{}
	service, _ := recoveryService(harness, testProduct("ambig-1"), 7, true, mappings)

	_, err := service.SyncProduct(ctx, "website", "ambig-1")
	var providerErr *commerce.ProviderError
	if !asProviderError(err, &providerErr) || !providerErr.Retryable() {
		t.Fatalf("ambiguous post must be temporary: %v", err)
	}
	harness.setDropCreate(false)
	result, err := service.SyncProduct(ctx, "website", "ambig-1")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if result.ExternalProductID != "500" {
		t.Fatalf("recovered: %q", result.ExternalProductID)
	}
	if countPosts(harness) != 1 {
		t.Fatalf("no duplicate post, got %d", countPosts(harness))
	}
}

// TestWooForeignSKUConflict preloads the same SKU under different
// ownership: no POST, typed conflict, mapping unchanged.
func TestWooForeignSKUConflict(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	harness.preload(600, map[string]any{
		"sku": "PAP-001",
		"meta_data": []any{
			map[string]any{"key": "_moonlight_product_id", "value": "someone-else"},
			map[string]any{"key": "_moonlight_provider_key", "value": "website"},
		},
	})
	ctx := context.Background()
	mappings := &stubMappingRepo{}
	service, _ := recoveryService(harness, testProduct("foreign-1"), 7, true, mappings)
	_, err := service.SyncProduct(ctx, "website", "foreign-1")
	requireProviderConflict(t, err, "foreign sku conflict")
	if countPosts(harness) != 0 {
		t.Fatal("no post on foreign sku")
	}
}

// TestWooDuplicateSKUAfterPost covers both recovery branches after a
// duplicate-SKU POST error.
func TestWooDuplicateSKUAfterPost(t *testing.T) {
	duplicate := map[string]any{"code": "product_invalid_sku", "message": "Invalid or duplicated SKU.", "data": map[string]any{"status": 400}}
	skuOwner := func(productID string) map[string]any {
		return map[string]any{
			"sku": "PAP-001",
			"meta_data": []any{
				map[string]any{"key": "_moonlight_product_id", "value": productID},
				map[string]any{"key": "_moonlight_provider_key", "value": "website"},
			},
		}
	}
	t.Run("owned recovers", func(t *testing.T) {
		harness := newWooHarness(t, testConsumerKey, testConsumerSec)
		harness.preload(600, skuOwner("dup-1"))
		harness.fail(http.MethodPost, "/wp-json/wc/v3/products", http.StatusBadRequest, duplicate)
		mappings := &stubMappingRepo{}
		service, _ := recoveryService(harness, testProduct("dup-1"), 7, true, mappings)
		result, err := service.SyncProduct(context.Background(), "website", "dup-1")
		if err != nil {
			t.Fatalf("recover: %v", err)
		}
		if result.ExternalProductID != "600" {
			t.Fatalf("recovered: %q", result.ExternalProductID)
		}
		if _, err := mappings.GetProductMapping(context.Background(), "website", "dup-1"); err != nil {
			t.Fatalf("mapping: %v", err)
		}
	})
	t.Run("foreign conflicts", func(t *testing.T) {
		harness := newWooHarness(t, testConsumerKey, testConsumerSec)
		harness.preload(600, skuOwner("someone-else"))
		harness.fail(http.MethodPost, "/wp-json/wc/v3/products", http.StatusBadRequest, duplicate)
		mappings := &stubMappingRepo{}
		service, _ := recoveryService(harness, testProduct("dup-1"), 7, true, mappings)
		if _, err := service.SyncProduct(context.Background(), "website", "dup-1"); err == nil {
			t.Fatal("foreign must fail")
		} else {
			requireProviderConflict(t, err, "foreign duplicate")
		}
	})
}

// TestWooMappedOwnershipConflict proves GET-verified foreign ownership
// blocks PUT and inventory with the mapping untouched.
func TestWooMappedOwnershipConflict(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	harness.preload(500, map[string]any{
		"sku": "PAP-001",
		"meta_data": []any{
			map[string]any{"key": "_moonlight_product_id", "value": "another-product"},
			map[string]any{"key": "_moonlight_provider_key", "value": "website"},
		},
	})
	ctx := context.Background()
	mappings := &stubMappingRepo{rows: map[string]commerce.ProductMapping{
		"website/mapped-1": {ProviderKey: "website", ProductID: "mapped-1", ExternalProductID: "500"},
	}}
	service, _ := recoveryService(harness, testProduct("mapped-1"), 7, true, mappings)
	_, err := service.SyncProduct(ctx, "website", "mapped-1")
	requireProviderConflict(t, err, "ownership conflict")
	if countPuts(harness, "500") != 0 {
		t.Fatal("no put on foreign product")
	}
	stored, _ := mappings.GetProductMapping(ctx, "website", "mapped-1")
	if stored.ExternalProductID != "500" {
		t.Fatalf("mapping unchanged: %+v", stored)
	}
}

// TestWooMapped404 proves a missing mapped product conflicts without
// silent remap or replacement POST.
func TestWooMapped404(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	ctx := context.Background()
	mappings := &stubMappingRepo{rows: map[string]commerce.ProductMapping{
		"website/gone-1": {ProviderKey: "website", ProductID: "gone-1", ExternalProductID: "500"},
	}}
	service, _ := recoveryService(harness, testProduct("gone-1"), 7, true, mappings)
	_, err := service.SyncProduct(ctx, "website", "gone-1")
	requireProviderConflict(t, err, "mapped 404")
	if countPosts(harness) != 0 || countPuts(harness, "500") != 0 {
		t.Fatal("no replacement calls")
	}
}

// TestWooUpdateResponseMismatch proves a PUT identity mismatch conflicts
// with no inventory call.
func TestWooUpdateResponseMismatch(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	harness.preload(500, map[string]any{
		"sku": "PAP-001",
		"meta_data": []any{
			map[string]any{"key": "_moonlight_product_id", "value": "mm-1"},
			map[string]any{"key": "_moonlight_provider_key", "value": "website"},
		},
	})
	// Corrupt the stored identity so PUT echoes a different id.
	harness.setIntercept(func(record wooRecordedRequest) (int, any, bool) {
		if record.Method == http.MethodPut {
			return http.StatusOK, map[string]any{
				"id":   float64(501),
				"sku":  "PAP-001",
				"name": "x",
			}, true
		}
		return 0, nil, false
	})
	ctx := context.Background()
	mappings := &stubMappingRepo{rows: map[string]commerce.ProductMapping{
		"website/mm-1": {ProviderKey: "website", ProductID: "mm-1", ExternalProductID: "500"},
	}}
	putCalls := 0
	_ = putCalls
	service, _ := recoveryService(harness, testProduct("mm-1"), 7, true, mappings)
	_, err := service.SyncProduct(ctx, "website", "mm-1")
	requireProviderConflict(t, err, "response mismatch")
	// Exactly one PUT (the metadata update); inventory never addressed.
	puts := 0
	for _, request := range harness.recorded() {
		if request.Method == http.MethodPut {
			puts++
		}
	}
	if puts != 1 {
		t.Fatalf("one metadata put, no inventory: %d", puts)
	}
}

// TestWooInventoryFailureSafety proves safe-zero remote state when the
// metadata update succeeds but SetInventory fails.
func TestWooInventoryFailureSafety(t *testing.T) {
	harness := newWooHarness(t, testConsumerKey, testConsumerSec)
	harness.preload(500, map[string]any{
		"sku":            "PAP-001",
		"stock_quantity": float64(10),
		"stock_status":   "instock",
		"meta_data": []any{
			map[string]any{"key": "_moonlight_product_id", "value": "invfail-1"},
			map[string]any{"key": "_moonlight_provider_key", "value": "website"},
		},
	})
	var puts atomic.Int64
	harness.setIntercept(func(record wooRecordedRequest) (int, any, bool) {
		if record.Method != http.MethodPut {
			return 0, nil, false
		}
		if puts.Add(1) == 2 {
			// Second PUT is the inventory update: fail it.
			return http.StatusInternalServerError,
				map[string]any{"code": "test_boom", "message": "boom", "data": map[string]any{"status": 500}}, true
		}
		return 0, nil, false
	})
	ctx := context.Background()
	mappings := &stubMappingRepo{rows: map[string]commerce.ProductMapping{
		"website/invfail-1": {ProviderKey: "website", ProductID: "invfail-1", ExternalProductID: "500"},
	}}
	service, _ := recoveryService(harness, testProduct("invfail-1"), 7, true, mappings)
	_, err := service.SyncProduct(ctx, "website", "invfail-1")
	var providerErr *commerce.ProviderError
	if !asProviderError(err, &providerErr) || !providerErr.Retryable() {
		t.Fatalf("temporary: %v", err)
	}
	stored := harness.productState(500)
	if stored["stock_quantity"] != float64(0) || stored["stock_status"] != "outofstock" {
		t.Fatalf("safe-zero remote, not stale 10: %v", stored)
	}
}

func errMappingNotFoundStub() error {
	return apperr.New(apperr.NotFound, "no mapping stub")
}
