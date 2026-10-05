package shopify

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// stubMappings is a durable-mapping fake with injectable persistence
// failure (Phase 11 §155 mapping-loss injection).
type stubMappings struct {
	mu        sync.Mutex
	rows      map[string]string // providerKey|productID -> externalID
	failNextN int
}

func newStubMappings() *stubMappings {
	return &stubMappings{rows: map[string]string{}}
}

func mappingKey(key commerce.ProviderKey, productID string) string {
	return string(key) + "|" + productID
}

func (s *stubMappings) GetProductMapping(_ context.Context, key commerce.ProviderKey, productID string) (commerce.ProductMapping, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	external, ok := s.rows[mappingKey(key, productID)]
	if !ok {
		return commerce.ProductMapping{}, apperr.New(apperr.NotFound, "mapping not found")
	}
	return commerce.ProductMapping{ProviderKey: key, ProductID: productID, ExternalProductID: external}, nil
}

func (s *stubMappings) GetProductConfigurationMapping(context.Context, commerce.ProviderKey, string, string) (commerce.ProductConfigurationMapping, error) {
	return commerce.ProductConfigurationMapping{}, errMappingNotFoundStub()
}

func (s *stubMappings) UpdateProductMappingExternal(_ context.Context, providerKey commerce.ProviderKey, productID, expectedExternalID, newExternalID string) (commerce.ProductMapping, error) {
	return commerce.ProductMapping{ProviderKey: providerKey, ProductID: productID, ExternalProductID: newExternalID}, nil
}
func (s *stubMappings) FindConfigurationByExternal(context.Context, commerce.ProviderKey, string, string) (commerce.ProductConfigurationMapping, error) {
	return commerce.ProductConfigurationMapping{}, errMappingNotFoundStub()
}
func (s *stubMappings) UpsertProductConfigurationMapping(_ context.Context, providerKey commerce.ProviderKey, productID, configurationID, externalProductID, externalConfigurationID string) (commerce.ProductConfigurationMapping, error) {
	return commerce.ProductConfigurationMapping{ProviderKey: providerKey, ProductID: productID, ConfigurationID: configurationID, ExternalProductID: externalProductID, ExternalConfigurationID: externalConfigurationID}, nil
}

func (s *stubMappings) FindByExternalProductID(_ context.Context, key commerce.ProviderKey, externalID string) (commerce.ProductMapping, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prefix := string(key) + "|"
	for compound, external := range s.rows {
		if external == externalID && strings.HasPrefix(compound, prefix) {
			return commerce.ProductMapping{
				ProviderKey: key, ProductID: compound[len(prefix):], ExternalProductID: externalID,
			}, nil
		}
	}
	return commerce.ProductMapping{}, apperr.New(apperr.NotFound, "mapping not found")
}

func (s *stubMappings) CreateProductMapping(_ context.Context, key commerce.ProviderKey, productID, externalID string) (commerce.ProductMapping, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failNextN > 0 {
		s.failNextN--
		return commerce.ProductMapping{}, fmt.Errorf("injected mapping persistence failure")
	}
	s.rows[mappingKey(key, productID)] = externalID
	return commerce.ProductMapping{ProviderKey: key, ProductID: productID, ExternalProductID: externalID}, nil
}

// stubSource serves fixed desired states keyed by product id.
type stubSource struct {
	desired map[string]commerce.DesiredProduct
}

func (s stubSource) GetDesiredCommerceProduct(_ context.Context, id string) (commerce.DesiredProduct, error) {
	desired, ok := s.desired[id]
	if !ok {
		return commerce.DesiredProduct{}, fmt.Errorf("desired product %q not found", id)
	}
	return desired, nil
}

func fixedDesired(productID, sku string) commerce.DesiredProduct {
	return commerce.DesiredProduct{
		Product: commerce.CommerceProduct{
			ProductID: productID,
			SKU:       sku,
			Names:     []commerce.LocalizedName{{Locale: "ar", Name: "بردية"}, {Locale: "en", Name: "Papyrus"}},
			Prices:    []commerce.Money{{Currency: "EGP", AmountMinor: 65000}},
			IsActive:  true, SellOnline: true,
		},
		Published:         true,
		Availability:      catalog.ProductAvailability{ProductID: productID, Ready: true, OnlineAvailable: 12, InventoryRevision: 5},
		CatalogRevision:   3,
		PolicyRevision:    2,
		InventoryRevision: 5,
	}
}

// Phase 11 §56/§155: remote product creation succeeds, Cloud mapping
// persistence fails — the remote stays safe at inventory 0 with
// ownership present; the retry discovers the same owned product,
// persists the mapping, and restores availability without a second
// remote create.
func TestMappingLossRecoveryCreatesExactlyOnce(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	mappings := newStubMappings()
	mappings.failNextN = 1
	service := commerce.NewCommerceService(
		newRegistryWith(t, provider), mappings,
		stubSource{map[string]commerce.DesiredProduct{"prod-1": fixedDesired("prod-1", "PAP-001")}}, nil)

	_, err := service.SyncProduct(context.Background(), "shopify-main", "prod-1")
	if err == nil {
		t.Fatal("mapping persistence failure must surface")
	}
	if h.creations() != 1 {
		t.Fatalf("remote creations = %d", h.creations())
	}
	externalAfterCreate := firstProductID(h)
	if got := h.quantityOf(externalAfterCreate, testLocationGID); got != 0 {
		t.Fatalf("remote quantity = %d after mapping loss (must stay safe-zero)", got)
	}

	result, err := service.SyncProduct(context.Background(), "shopify-main", "prod-1")
	if err != nil {
		t.Fatal(err)
	}
	if h.creations() != 1 {
		t.Fatalf("remote creations = %d after recovery (duplicate)", h.creations())
	}
	if result.ExternalProductID != externalAfterCreate {
		t.Fatal("recovery produced a different remote product")
	}
	if got := h.quantityOf(result.ExternalProductID, testLocationGID); got != 12 {
		t.Fatalf("availability = %d, want 12", got)
	}
	if _, err := mappings.GetProductMapping(context.Background(), "shopify-main", "prod-1"); err != nil {
		t.Fatal("mapping not persisted on recovery")
	}
}

// Phase 11 §188: one MoonLight product publishes to woo-main and
// shopify-main simultaneously with independent durable mappings and no
// cross-provider mutation.
func TestMultiProviderIndependentMappings(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	woo := &fakeProvider{key: "woo-main"}
	mappings := newStubMappings()
	service := commerce.NewCommerceService(newRegistryWith(t, provider, woo), mappings,
		stubSource{map[string]commerce.DesiredProduct{"prod-1": fixedDesired("prod-1", "PAP-001")}}, nil)

	shopifyResult, err := service.SyncProduct(context.Background(), "shopify-main", "prod-1")
	if err != nil {
		t.Fatal(err)
	}
	wooResult, err := service.SyncProduct(context.Background(), "woo-main", "prod-1")
	if err != nil {
		t.Fatal(err)
	}
	if shopifyResult.ExternalProductID == wooResult.ExternalProductID {
		t.Fatal("providers share external identity")
	}
	if _, err := mappings.GetProductMapping(context.Background(), "shopify-main", "prod-1"); err != nil {
		t.Fatal("shopify mapping missing")
	}
	if _, err := mappings.GetProductMapping(context.Background(), "woo-main", "prod-1"); err != nil {
		t.Fatal("woo mapping missing")
	}
	if woo.upserts != 1 || woo.inventorySets != 1 {
		t.Fatalf("woo calls = %d/%d", woo.upserts, woo.inventorySets)
	}
	if h.creations() != 1 {
		t.Fatalf("shopify creations = %d", h.creations())
	}
}

// Phase 11 §131/§189: one provider's failure never corrupts the other
// provider's durable mapping or inventory state, in either direction.
func TestProviderFailureIsolation(t *testing.T) {
	h := newHarness(t)
	shopify := newTestProvider(t, h)
	woo := &fakeProvider{key: "woo-main"}
	mappings := newStubMappings()
	desired := map[string]commerce.DesiredProduct{
		"prod-1": fixedDesired("prod-1", "PAP-001"),
		"prod-2": fixedDesired("prod-2", "PAP-002"),
	}
	service := commerce.NewCommerceService(newRegistryWith(t, shopify, woo), mappings, stubSource{desired}, nil)

	// Shopify down: Woo sync still succeeds and keeps its mapping.
	h.setFailure("MoonlightProductCreate", 500, `{"errors":[{"message":"down"}]}`)
	if _, err := service.SyncProduct(context.Background(), "shopify-main", "prod-1"); err == nil {
		t.Fatal("shopify failure must surface")
	}
	if _, err := service.SyncProduct(context.Background(), "woo-main", "prod-1"); err != nil {
		t.Fatalf("woo must succeed while shopify is down: %v", err)
	}
	if _, err := mappings.GetProductMapping(context.Background(), "woo-main", "prod-1"); err != nil {
		t.Fatal("woo mapping corrupted by shopify failure")
	}

	// Woo down: Shopify sync still succeeds and keeps its mapping.
	h.clearFailure("MoonlightProductCreate")
	woo.failWith = fmt.Errorf("woo down")
	if _, err := service.SyncProduct(context.Background(), "woo-main", "prod-2"); err == nil {
		t.Fatal("woo failure must surface")
	}
	if _, err := service.SyncProduct(context.Background(), "shopify-main", "prod-2"); err != nil {
		t.Fatalf("shopify must succeed while woo is down: %v", err)
	}
	if _, err := mappings.GetProductMapping(context.Background(), "shopify-main", "prod-2"); err != nil {
		t.Fatal("shopify mapping corrupted by woo failure")
	}
	woo.failWith = nil
}

// fakeProvider is a minimal second CommerceProvider recording calls with
// injectable failure.
type fakeProvider struct {
	key           commerce.ProviderKey
	upserts       int
	inventorySets int
	failWith      error
	externalID    string
}

func (f *fakeProvider) Key() commerce.ProviderKey { return f.key }

func (f *fakeProvider) UpsertProduct(_ context.Context, req commerce.ProductUpsertRequest) (commerce.ProductUpsertResult, error) {
	if f.failWith != nil {
		return commerce.ProductUpsertResult{}, f.failWith
	}
	f.upserts++
	external := f.externalID
	if external == "" {
		external = "ext-" + req.ProductID
	}
	return commerce.ProductUpsertResult{ExternalProductID: external}, nil
}

func (f *fakeProvider) SetInventory(_ context.Context, _ commerce.InventoryUpdateRequest) error {
	if f.failWith != nil {
		return f.failWith
	}
	f.inventorySets++
	return nil
}

func newRegistryWith(t *testing.T, providers ...commerce.CommerceProvider) *commerce.Registry {
	t.Helper()
	registry := commerce.NewRegistry()
	for _, provider := range providers {
		if err := registry.Register(provider.Key(), provider); err != nil {
			t.Fatal(err)
		}
	}
	return registry
}

// firstProductID returns the only product the harness has created so far.
func firstProductID(h *shopifyHarness) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id := range h.products {
		return id
	}
	return ""
}

func errMappingNotFoundStub() error {
	return apperr.New(apperr.NotFound, "no mapping stub")
}
