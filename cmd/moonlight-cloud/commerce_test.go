package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
)

type cliStubSource struct {
	state commerce.DesiredProduct
	err   error
}

func (s *cliStubSource) GetDesiredCommerceProduct(_ context.Context, _ string) (commerce.DesiredProduct, error) {
	if s.err != nil {
		return commerce.DesiredProduct{}, s.err
	}
	return s.state, nil
}

type cliStubMappings struct {
	rows map[string]commerce.ProductMapping
}

var errCLINoMapping = apperr.New(apperr.NotFound, "no mapping")

func (s *cliStubMappings) GetProductMapping(_ context.Context, key commerce.ProviderKey, productID string) (commerce.ProductMapping, error) {
	mapping, ok := s.rows[string(key)+"/"+productID]
	if !ok {
		return commerce.ProductMapping{}, errCLINoMapping
	}
	return mapping, nil
}

func (s *cliStubMappings) GetProductConfigurationMapping(context.Context, commerce.ProviderKey, string, string) (commerce.ProductConfigurationMapping, error) {
	return commerce.ProductConfigurationMapping{}, errMappingNotFoundStub()
}

func (s *cliStubMappings) UpdateProductMappingExternal(_ context.Context, providerKey commerce.ProviderKey, productID, expectedExternalID, newExternalID string) (commerce.ProductMapping, error) {
	return commerce.ProductMapping{ProviderKey: providerKey, ProductID: productID, ExternalProductID: newExternalID}, nil
}
func (s *cliStubMappings) FindConfigurationByExternal(context.Context, commerce.ProviderKey, string, string) (commerce.ProductConfigurationMapping, error) {
	return commerce.ProductConfigurationMapping{}, errMappingNotFoundStub()
}
func (s *cliStubMappings) UpsertProductConfigurationMapping(_ context.Context, providerKey commerce.ProviderKey, productID, configurationID, externalProductID, externalConfigurationID string) (commerce.ProductConfigurationMapping, error) {
	return commerce.ProductConfigurationMapping{ProviderKey: providerKey, ProductID: productID, ConfigurationID: configurationID, ExternalProductID: externalProductID, ExternalConfigurationID: externalConfigurationID}, nil
}

func (s *cliStubMappings) FindByExternalProductID(_ context.Context, _ commerce.ProviderKey, _ string) (commerce.ProductMapping, error) {
	return commerce.ProductMapping{}, errCLINoMapping
}

func (s *cliStubMappings) CreateProductMapping(_ context.Context, key commerce.ProviderKey, productID, externalID string) (commerce.ProductMapping, error) {
	mapping := commerce.ProductMapping{ProviderKey: key, ProductID: productID, ExternalProductID: externalID}
	if s.rows == nil {
		s.rows = map[string]commerce.ProductMapping{}
	}
	s.rows[string(key)+"/"+productID] = mapping
	return mapping, nil
}

func cliDesiredState(published bool, online int, ready bool) commerce.DesiredProduct {
	stock := online
	return commerce.DesiredProduct{
		Product: commerce.CommerceProduct{
			ProductID: "11111111-1111-4111-8111-111111111111", SKU: "PAP-CLI",
			IsActive: true, SellOnline: published,
			Names:           []commerce.LocalizedName{{Locale: "ar", Name: "منتج"}},
			Prices:          []commerce.Money{{Currency: "EGP", AmountMinor: 65000}},
			CatalogRevision: 12, PolicyRevision: 4,
		},
		Published: published,
		Availability: catalog.ProductAvailability{
			ProductID:     "11111111-1111-4111-8111-111111111111",
			ProductActive: true, SellOnline: published,
			OnlineAvailable: online, Ready: ready,
			StockQuantity: &stock, InventoryRevision: 27,
		},
		CatalogRevision: 12, PolicyRevision: 4, InventoryRevision: 27,
	}
}

func cliTestService(t *testing.T, state commerce.DesiredProduct, stateErr error) (*commerce.CommerceService, *commerce.FakeProvider) {
	t.Helper()
	provider := commerce.NewFakeProvider("website")
	registry := commerce.NewRegistry()
	if err := registry.Register("website", provider); err != nil {
		t.Fatal(err)
	}
	service := commerce.NewCommerceService(registry, &cliStubMappings{},
		&cliStubSource{state: state, err: stateErr}, nil)
	return service, provider
}

func TestCommerceCmdUsage(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"sync"},
		{"sync-product"},
		{"sync-product", "--provider", "website"},
		{"sync-product", "--product", "11111111-1111-4111-8111-111111111111"},
		{"sync-product", "--provider", "", "--product", "11111111-1111-4111-8111-111111111111"},
	} {
		var stdout, stderr bytes.Buffer
		if err := commerceCmd(args, &stdout, &stderr); err == nil {
			t.Fatalf("args %v must fail", args)
		}
	}
}

func TestRunCommerceSyncSuccess(t *testing.T) {
	service, provider := cliTestService(t, cliDesiredState(true, 10, true), nil)
	var out bytes.Buffer
	result := func() error {
		return runCommerceSync(context.Background(), service, &out,
			"website", "11111111-1111-4111-8111-111111111111")
	}
	if err := result(); err != nil {
		t.Fatalf("sync: %v", err)
	}
	output := out.String()
	for _, want := range []string{
		"provider=website",
		"product=11111111-1111-4111-8111-111111111111",
		"outcome=product_synced",
		"mapping_created=true",
		"inventory_updated=true",
		"product_key=",
		"inventory_key=",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q: %q", want, output)
		}
	}
	if len(provider.Upserts()) != 1 || len(provider.Inventories()) != 1 {
		t.Fatal("one upsert and one inventory call")
	}
}

func TestRunCommerceSyncTemporaryFailure(t *testing.T) {
	service, provider := cliTestService(t, cliDesiredState(true, 10, true), nil)
	provider.FailUpsertOnce("11111111-1111-4111-8111-111111111111", commerce.TemporaryError("timeout"))
	var out bytes.Buffer
	err := runCommerceSync(context.Background(), service, &out,
		"website", "11111111-1111-4111-8111-111111111111")
	if err == nil {
		t.Fatal("temporary failure must exit non-zero")
	}
	output := out.String()
	for _, want := range []string{"error_kind=temporary", "retryable=true"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q: %q", want, output)
		}
	}
}

func TestRunCommerceSyncConflict(t *testing.T) {
	mappings := &cliStubMappings{rows: map[string]commerce.ProductMapping{
		"website/11111111-1111-4111-8111-111111111111": {
			ProviderKey: "website", ProductID: "11111111-1111-4111-8111-111111111111",
			ExternalProductID: "ext-old",
		},
	}}
	provider := commerce.NewFakeProvider("website")
	provider.OverrideExternal("11111111-1111-4111-8111-111111111111", "ext-new")
	registry := commerce.NewRegistry()
	if err := registry.Register("website", provider); err != nil {
		t.Fatal(err)
	}
	service := commerce.NewCommerceService(registry, mappings,
		&cliStubSource{state: cliDesiredState(true, 10, true)}, nil)
	var out bytes.Buffer
	err := runCommerceSync(context.Background(), service, &out,
		"website", "11111111-1111-4111-8111-111111111111")
	if err == nil {
		t.Fatal("conflict must exit non-zero")
	}
	if !strings.Contains(out.String(), "error=") {
		t.Fatalf("error printed: %q", out.String())
	}
}

func TestRunCommerceSyncInvalidProduct(t *testing.T) {
	service, _ := cliTestService(t, commerce.DesiredProduct{},
		apperr.New(apperr.NotFound, "product not ready"))
	var out bytes.Buffer
	err := runCommerceSync(context.Background(), service, &out,
		"website", "not-a-uuid")
	if err == nil {
		t.Fatal("not-ready must exit non-zero")
	}
}

func TestRunCommerceSyncUnknownProvider(t *testing.T) {
	service, _ := cliTestService(t, cliDesiredState(true, 10, true), nil)
	var out bytes.Buffer
	err := runCommerceSync(context.Background(), service, &out,
		"missing", "11111111-1111-4111-8111-111111111111")
	if !commerce.UnknownProvider(err) {
		t.Fatalf("unknown provider: %v", err)
	}
}

func TestNewCommerceRegistry(t *testing.T) {
	registry, err := newCommerceRegistry(config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if registry.Count() != 0 {
		t.Fatal("disabled registers nothing")
	}
	enabled := config.Config{WooCommerce: config.WooCommerceConfig{
		Enabled: true, ProviderKey: "website", BaseURL: "https://store.example.com",
		ConsumerKey: "SECRET-KEY-XYZ", ConsumerSecret: "SECRET-SEC-XYZ",
		Currency: config.WooCurrencyEGP, DimensionUnit: config.WooDimensionCM,
		HTTPTimeout: 15 * time.Second,
	}}
	registry, err = newCommerceRegistry(enabled)
	if err != nil {
		t.Fatalf("enabled: %v", err)
	}
	if registry.Count() != 1 {
		t.Fatalf("one provider: %d", registry.Count())
	}
	provider, err := registry.Get("website")
	if err != nil {
		t.Fatal(err)
	}
	if provider.Key() != "website" {
		t.Fatalf("key: %q", provider.Key())
	}
	// Invalid enabled configuration fails before any registration.
	broken := enabled
	broken.WooCommerce.BaseURL = "http://insecure.example.com"
	if _, err := newCommerceRegistry(broken); err == nil {
		t.Fatal("insecure URL must fail")
	}
}

func TestCommerceCmdSyncOrderUsage(t *testing.T) {
	for _, args := range [][]string{
		{"sync-order"},
		{"sync-order", "--provider", "website"},
		{"sync-order", "--order", "100"},
		{"bogus"},
	} {
		var stdout, stderr bytes.Buffer
		if err := commerceCmd(args, &stdout, &stderr); err == nil {
			t.Fatalf("args %v must fail", args)
		}
	}
}

// cliStubOrderProvider serves canned order snapshots.
type cliStubOrderProvider struct {
	key      commerce.ProviderKey
	snapshot commerceOrderSnapshot
	err      error
}

type commerceOrderSnapshot = orders.OrderSnapshot

func (s *cliStubOrderProvider) Key() commerce.ProviderKey { return s.key }

func (s *cliStubOrderProvider) UpsertProduct(_ context.Context, _ commerce.ProductUpsertRequest) (commerce.ProductUpsertResult, error) {
	return commerce.ProductUpsertResult{}, errors.New("not implemented")
}

func (s *cliStubOrderProvider) SetInventory(_ context.Context, _ commerce.InventoryUpdateRequest) error {
	return errors.New("not implemented")
}

func (s *cliStubOrderProvider) GetOrder(_ context.Context, _ string) (orders.OrderSnapshot, error) {
	if s.err != nil {
		return orders.OrderSnapshot{}, s.err
	}
	return s.snapshot, nil
}

// cliStubOrderStore is an in-memory projection store.
type cliStubOrderStore struct {
	orders        map[string]orders.OrderSnapshot
	revs          map[string]int64
	gens          map[string]orders.ReconcileGeneration
	supersedeNext bool
}

func (s *cliStubOrderStore) BeginOrderReconcile(_ context.Context, providerKey, externalOrderID string) (orders.ReconcileGeneration, error) {
	if s.gens == nil {
		s.gens = map[string]orders.ReconcileGeneration{}
	}
	key := providerKey + "/" + externalOrderID
	s.gens[key]++
	return s.gens[key], nil
}

func (s *cliStubOrderStore) ReconcileProjectedOrder(_ context.Context, snapshot orders.OrderSnapshot, fingerprint [32]byte, _ orders.ReconcileGeneration) (orders.ReconcileOutcome, error) {
	if s.supersedeNext {
		s.supersedeNext = false
		return orders.ReconcileOutcome{Superseded: true}, nil
	}
	key := snapshot.ProviderKey + "/" + snapshot.ExternalOrderID
	if s.orders == nil {
		s.orders = map[string]orders.OrderSnapshot{}
		s.revs = map[string]int64{}
	}
	if existing, ok := s.orders[key]; ok && orders.Fingerprint(existing) == fingerprint {
		return orders.ReconcileOutcome{Revision: s.revs[key]}, nil
	}
	s.orders[key] = snapshot
	s.revs[key]++
	return orders.ReconcileOutcome{Revision: s.revs[key], Changed: true}, nil
}

func (s *cliStubOrderStore) LoadProjectedOrder(_ context.Context, providerKey, externalOrderID string) (orders.OrderSnapshot, int64, bool, error) {
	key := providerKey + "/" + externalOrderID
	snapshot, ok := s.orders[key]
	if !ok {
		return orders.OrderSnapshot{}, 0, false, nil
	}
	return snapshot, s.revs[key], true, nil
}

func cliOrderService(t *testing.T, snapshot orders.OrderSnapshot, err error) *orders.OrderService {
	t.Helper()
	provider := &cliStubOrderProvider{key: "website", snapshot: snapshot, err: err}
	registry := commerce.NewRegistry()
	if rerr := registry.Register("website", provider); rerr != nil {
		t.Fatal(rerr)
	}
	return orders.NewOrderService(registry, &cliStubOrderStore{}, nil)
}

func cliOrderSnapshot() orders.OrderSnapshot {
	return orders.OrderSnapshot{
		ProviderKey: "website", ExternalOrderID: "700", OrderNumber: "700",
		ProviderStatus: "processing", Canonical: orders.StatusProcessing,
		Currency: "EGP", TotalMinor: 68000,
		CreatedAt:  time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC),
		ModifiedAt: time.Date(2026, 9, 27, 11, 0, 0, 0, time.UTC),
		Lines: []orders.OrderLine{
			{ExternalLineID: 1, ExternalProductID: "500", SKU: "PAP-1", Name: "X", Quantity: 2, TotalMinor: 68000},
		},
		MappingComplete: true,
	}
}

func TestRunCommerceSyncOrderSuccess(t *testing.T) {
	service := cliOrderService(t, cliOrderSnapshot(), nil)
	var out bytes.Buffer
	if err := runCommerceSyncOrder(context.Background(), service, &out, "website", "700"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	output := out.String()
	for _, want := range []string{
		"provider=website", "order=700", "revision=1", "changed=true",
		"status=PROCESSING", "mapping_complete=true", "unmapped_lines=0", "deleted=false",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q: %q", want, output)
		}
	}
	for _, forbidden := range []string{"a@example.com", "secret", "Bearer"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("output leaks %q: %q", forbidden, output)
		}
	}
}

func TestRunCommerceSyncOrderTemporary(t *testing.T) {
	service := cliOrderService(t, orders.OrderSnapshot{}, commerce.TemporaryError("outage"))
	var out bytes.Buffer
	err := runCommerceSyncOrder(context.Background(), service, &out, "website", "700")
	if err == nil {
		t.Fatal("temporary must exit non-zero")
	}
	output := out.String()
	for _, want := range []string{"error_kind=temporary", "retryable=true"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q: %q", want, output)
		}
	}
	if strings.Contains(output, "revision=") {
		t.Fatalf("no success fields: %q", output)
	}
}

// TestRunCommerceSyncOrderSuperseded proves a concurrently superseded
// manual sync returns success with superseded=true instead of looping.
func TestRunCommerceSyncOrderSuperseded(t *testing.T) {
	provider := &cliStubOrderProvider{key: "website", snapshot: cliOrderSnapshot()}
	registry := commerce.NewRegistry()
	if err := registry.Register("website", provider); err != nil {
		t.Fatal(err)
	}
	store := &cliStubOrderStore{supersedeNext: true}
	service := orders.NewOrderService(registry, store, nil)
	var out bytes.Buffer
	if err := runCommerceSyncOrder(context.Background(), service, &out, "website", "700"); err != nil {
		t.Fatalf("superseded is success: %v", err)
	}
	if !strings.Contains(out.String(), "superseded=true") {
		t.Fatalf("superseded reported: %q", out.String())
	}
	if strings.Contains(out.String(), "changed=true") {
		t.Fatalf("no change reported: %q", out.String())
	}
}

// TestRunCommerceSyncOrderConflict proves manual sync behind a Woo 409
// exits non-zero as Conflict — never as ORDER_NOT_FOUND — with the
// existing current order untouched.
func TestRunCommerceSyncOrderConflict(t *testing.T) {
	service := cliOrderService(t, orders.OrderSnapshot{}, commerce.ConflictError("woo error test_conflict: Clash."))
	var out bytes.Buffer
	err := runCommerceSyncOrder(context.Background(), service, &out, "website", "700")
	if err == nil {
		t.Fatal("conflict must exit non-zero")
	}
	output := out.String()
	for _, want := range []string{"error_kind=conflict", "retryable=false"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q: %q", want, output)
		}
	}
	for _, forbidden := range []string{"ORDER_NOT_FOUND", "revision="} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("output must not contain %q: %q", forbidden, output)
		}
	}
}

func errMappingNotFoundStub() error {
	return apperr.New(apperr.NotFound, "no mapping stub")
}
