package commerce

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

func TestValidateProviderKey(t *testing.T) {
	for _, valid := range []string{"primary", "website", "secondary-store", "shop_2", "a", "x9-_"} {
		if _, err := ValidateProviderKey(valid); err != nil {
			t.Fatalf("valid key %q: %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "Primary", "has space", "woocommerce!", "a/b", "x.y",
		"0123456789012345678901234567890123456789012345678901234567890123456789"} {
		if _, err := ValidateProviderKey(invalid); err == nil {
			t.Fatalf("invalid key %q accepted", invalid)
		}
	}
}

type stubProvider struct {
	key ProviderKey
}

func (s *stubProvider) Key() ProviderKey { return s.key }
func (s *stubProvider) UpsertProduct(_ context.Context, _ ProductUpsertRequest) (ProductUpsertResult, error) {
	return ProductUpsertResult{ExternalProductID: "ext"}, nil
}
func (s *stubProvider) SetInventory(_ context.Context, _ InventoryUpdateRequest) error { return nil }

func TestRegistry(t *testing.T) {
	registry := NewRegistry()
	if registry.Count() != 0 {
		t.Fatal("new registry must be empty")
	}
	if len(registry.List()) != 0 {
		t.Fatal("empty registry lists nothing")
	}
	first := &stubProvider{key: "primary"}
	second := &stubProvider{key: "website"}
	if err := registry.Register("primary", first); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := registry.Register("website", second); err != nil {
		t.Fatalf("register: %v", err)
	}
	if registry.Count() != 2 {
		t.Fatal("two providers registered")
	}
	got, err := registry.Get("primary")
	if err != nil || got != CommerceProvider(first) {
		t.Fatalf("lookup: %v", got)
	}
	list := registry.List()
	if len(list) != 2 || list[0] != "primary" || list[1] != "website" {
		t.Fatalf("sorted list: %v", list)
	}
	// Duplicate key rejected.
	if err := registry.Register("primary", &stubProvider{key: "primary"}); err == nil {
		t.Fatal("duplicate key must fail")
	} else {
		var appErr *apperr.Error
		if !errors.As(err, &appErr) || appErr.Kind != apperr.Conflict {
			t.Fatalf("duplicate must be conflict: %v", err)
		}
	}
	// Unknown key is a typed not-found.
	if _, err := registry.Get("missing"); !UnknownProvider(err) {
		t.Fatalf("unknown must be typed not-found: %v", err)
	}
	// Nil and mismatched providers rejected.
	if err := registry.Register("nil", nil); err == nil {
		t.Fatal("nil provider must fail")
	}
	if err := registry.Register("other", first); err == nil {
		t.Fatal("key mismatch must fail")
	}
	if err := registry.Register("BAD KEY", first); err == nil {
		t.Fatal("invalid key must fail")
	}
	// Two providers stay independent.
	other, err := registry.Get("website")
	if err != nil || other != CommerceProvider(second) {
		t.Fatalf("second provider: %v", other)
	}
}

func TestRegistryConcurrent(t *testing.T) {
	registry := NewRegistry()
	done := make(chan error, 8)
	for i := 0; i < 4; i++ {
		key := ProviderKey("shop-" + string(rune('a'+i)))
		go func() {
			done <- registry.Register(key, &stubProvider{key: key})
		}()
		go func() {
			_, _ = registry.Get(key)
			_ = registry.List()
			_ = registry.Count()
			done <- nil
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent registry: %v", err)
		}
	}
	if registry.Count() != 4 {
		t.Fatalf("count: %d", registry.Count())
	}
}

func TestOperationKeys(t *testing.T) {
	published := ProductOperationKey("primary", "product-1", 12, 4, true)
	again := ProductOperationKey("primary", "product-1", 12, 4, true)
	if published == "" || published != again {
		t.Fatal("same state must yield a stable non-empty key")
	}
	changes := map[string]string{
		"catalog revision": ProductOperationKey("primary", "product-1", 13, 4, true),
		"policy revision":  ProductOperationKey("primary", "product-1", 12, 5, true),
		"publication":      ProductOperationKey("primary", "product-1", 12, 4, false),
		"provider key":     ProductOperationKey("website", "product-1", 12, 4, true),
		"product id":       ProductOperationKey("primary", "product-2", 12, 4, true),
	}
	for what, key := range changes {
		if key == published {
			t.Fatalf("%s must change the product key", what)
		}
	}
	inventory := InventoryOperationKey("primary", "product-1", 12, 4, 27, 10, true)
	if inventory == "" || inventory == published {
		t.Fatal("inventory key must be distinct and non-empty")
	}
	if again := InventoryOperationKey("primary", "product-1", 12, 4, 27, 10, true); again != inventory {
		t.Fatal("same inventory state must be stable")
	}
	invChanges := map[string]string{
		"catalog revision":   InventoryOperationKey("primary", "product-1", 13, 4, 27, 10, true),
		"policy revision":    InventoryOperationKey("primary", "product-1", 12, 5, 27, 10, true),
		"inventory revision": InventoryOperationKey("primary", "product-1", 12, 4, 28, 10, true),
		"quantity":           InventoryOperationKey("primary", "product-1", 12, 4, 27, 9, true),
		"provider key":       InventoryOperationKey("website", "product-1", 12, 4, 27, 10, true),
		"product id":         InventoryOperationKey("primary", "product-2", 12, 4, 27, 10, true),
	}
	for what, key := range invChanges {
		if key == inventory {
			t.Fatalf("%s must change the inventory key", what)
		}
	}
}

func TestProviderErrorTaxonomy(t *testing.T) {
	if !TemporaryError("x").Retryable() {
		t.Fatal("temporary must be retryable")
	}
	limited := RateLimitedError("slow", 30*time.Second)
	if !limited.Retryable() {
		t.Fatal("rate-limited must be retryable")
	}
	if after, ok := limited.GetRetryAfter(); !ok || after != 30*time.Second {
		t.Fatalf("retry-after: %v %v", after, ok)
	}
	if _, ok := TemporaryError("x").GetRetryAfter(); ok {
		t.Fatal("temporary carries no retry-after")
	}
	for _, terminal := range []*ProviderError{
		AuthenticationError("bad creds"), ValidationError("bad state"), ConflictError("clash"),
	} {
		if terminal.Retryable() {
			t.Fatalf("%v must be terminal", terminal.Kind)
		}
	}
	var asProvider *ProviderError
	if !errors.As(ValidationError("x"), &asProvider) || asProvider.Kind != ErrorValidation {
		t.Fatal("errors.As must expose the kind")
	}
}

// stubSource serves canned desired states without a database.
type stubSource struct {
	states map[string]DesiredProduct
	err    map[string]error
}

func (s *stubSource) GetDesiredCommerceProduct(_ context.Context, productID string) (DesiredProduct, error) {
	if err, ok := s.err[productID]; ok {
		return DesiredProduct{}, err
	}
	state, ok := s.states[productID]
	if !ok {
		return DesiredProduct{}, apperr.New(apperr.NotFound, "product not ready")
	}
	return state, nil
}

// stubMappings is an in-memory mapping repository with injectable failure.
type stubMappings struct {
	rows      map[string]ProductMapping
	failNext  error
	failCount int
}

func mappingKey(key ProviderKey, productID string) string { return string(key) + "/" + productID }

func (s *stubMappings) GetProductMapping(_ context.Context, key ProviderKey, productID string) (ProductMapping, error) {
	mapping, ok := s.rows[mappingKey(key, productID)]
	if !ok {
		return ProductMapping{}, apperr.New(apperr.NotFound, "no mapping")
	}
	return mapping, nil
}

func (s *stubMappings) FindByExternalProductID(_ context.Context, key ProviderKey, externalID string) (ProductMapping, error) {
	for _, mapping := range s.rows {
		if mapping.ProviderKey == key && mapping.ExternalProductID == externalID {
			return mapping, nil
		}
	}
	return ProductMapping{}, apperr.New(apperr.NotFound, "no mapping")
}

func (s *stubMappings) CreateProductMapping(_ context.Context, key ProviderKey, productID, externalID string) (ProductMapping, error) {
	if s.failCount > 0 {
		s.failCount--
		return ProductMapping{}, s.failNext
	}
	k := mappingKey(key, productID)
	if existing, ok := s.rows[k]; ok {
		if existing.ExternalProductID != externalID {
			return ProductMapping{}, apperr.New(apperr.Conflict, "mapping conflict")
		}
		return existing, nil
	}
	for _, mapping := range s.rows {
		if mapping.ProviderKey == key && mapping.ExternalProductID == externalID {
			return ProductMapping{}, apperr.New(apperr.Conflict, "mapping conflict")
		}
	}
	mapping := ProductMapping{ProviderKey: key, ProductID: productID, ExternalProductID: externalID}
	if s.rows == nil {
		s.rows = map[string]ProductMapping{}
	}
	s.rows[k] = mapping
	return mapping, nil
}

func desiredState(productID string, active, sellOnline bool, online int, ready bool) DesiredProduct {
	stock := online
	published := active && sellOnline
	return DesiredProduct{
		Product: CommerceProduct{
			ProductID: productID, SKU: "PAP-1", IsActive: active,
			SellOnline: sellOnline, CatalogRevision: 12, PolicyRevision: 4,
		},
		Published: published,
		Availability: catalog.ProductAvailability{
			ProductID: productID, ProductActive: active, SellOnline: sellOnline,
			OnlineAvailable: online, Ready: ready,
			StockQuantity: &stock, InventoryRevision: 27,
		},
		CatalogRevision: 12, PolicyRevision: 4, InventoryRevision: 27,
	}
}

func testService(provider *FakeProvider, states map[string]DesiredProduct) (*CommerceService, *stubMappings) {
	registry := NewRegistry()
	if err := registry.Register(provider.Key(), provider); err != nil {
		panic(err)
	}
	mappings := &stubMappings{rows: map[string]ProductMapping{}}
	return NewCommerceService(registry, mappings, &stubSource{states: states}, nil), mappings
}

func TestSyncProductMatrix(t *testing.T) {
	ctx := context.Background()
	newProvider := func() *FakeProvider { return NewFakeProvider("primary") }

	t.Run("unmapped disabled noop", func(t *testing.T) {
		provider := newProvider()
		service, _ := testService(provider, map[string]DesiredProduct{"p1": desiredState("p1", true, false, 0, true)})
		result, err := service.SyncProduct(ctx, "primary", "p1")
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome != SyncNoOp {
			t.Fatalf("noop: %+v", result)
		}
		if len(provider.Upserts()) != 0 || len(provider.Inventories()) != 0 {
			t.Fatal("no provider calls on unmapped disabled")
		}
	})

	t.Run("unmapped enabled create", func(t *testing.T) {
		provider := newProvider()
		service, mappings := testService(provider, map[string]DesiredProduct{"p1": desiredState("p1", true, true, 10, true)})
		result, err := service.SyncProduct(ctx, "primary", "p1")
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome != SyncProduct || !result.MappingCreated || !result.InventoryUpdated {
			t.Fatalf("create: %+v", result)
		}
		if result.ExternalProductID == "" || result.ProductOperationKey == "" || result.InventoryOperationKey == "" {
			t.Fatalf("identifiers: %+v", result)
		}
		upserts := provider.Upserts()
		if len(upserts) != 1 || !upserts[0].Published || upserts[0].ExistingExternal != nil {
			t.Fatalf("upsert: %+v", upserts)
		}
		inventories := provider.Inventories()
		if len(inventories) != 1 || inventories[0].AvailableQuantity != 10 || !inventories[0].Ready {
			t.Fatalf("inventory: %+v", inventories)
		}
		if _, err := mappings.GetProductMapping(ctx, "primary", "p1"); err != nil {
			t.Fatalf("mapping persisted: %v", err)
		}
		// Repeat sync is idempotent: same keys, one remote product.
		result2, err := service.SyncProduct(ctx, "primary", "p1")
		if err != nil {
			t.Fatal(err)
		}
		if result2.ExternalProductID != result.ExternalProductID ||
			result2.ProductOperationKey != result.ProductOperationKey ||
			result2.InventoryOperationKey != result.InventoryOperationKey {
			t.Fatalf("stable retry: %+v vs %+v", result, result2)
		}
		if provider.Creations() != 1 {
			t.Fatalf("one remote product, got %d", provider.Creations())
		}
	})

	t.Run("mapped enabled update", func(t *testing.T) {
		provider := newProvider()
		service, mappings := testService(provider, map[string]DesiredProduct{"p1": desiredState("p1", true, true, 3, true)})
		mappings.rows[mappingKey("primary", "p1")] = ProductMapping{ProviderKey: "primary", ProductID: "p1", ExternalProductID: "ext-existing"}
		result, err := service.SyncProduct(ctx, "primary", "p1")
		if err != nil {
			t.Fatal(err)
		}
		if result.MappingCreated || result.ExternalProductID != "ext-existing" {
			t.Fatalf("mapping reused: %+v", result)
		}
		upserts := provider.Upserts()
		if len(upserts) != 1 || upserts[0].ExistingExternal == nil || upserts[0].ExistingExternal.ExternalProductID != "ext-existing" {
			t.Fatalf("existing external passed: %+v", upserts)
		}
		if inventories := provider.Inventories(); len(inventories) != 1 || inventories[0].AvailableQuantity != 3 {
			t.Fatalf("inventory: %+v", inventories)
		}
	})

	t.Run("mapped disabled unpublish", func(t *testing.T) {
		provider := newProvider()
		service, mappings := testService(provider, map[string]DesiredProduct{"p1": desiredState("p1", true, false, 0, true)})
		mappings.rows[mappingKey("primary", "p1")] = ProductMapping{ProviderKey: "primary", ProductID: "p1", ExternalProductID: "ext-existing"}
		result, err := service.SyncProduct(ctx, "primary", "p1")
		if err != nil {
			t.Fatal(err)
		}
		upserts := provider.Upserts()
		if len(upserts) != 1 || upserts[0].Published {
			t.Fatalf("unpublished: %+v", upserts)
		}
		inventories := provider.Inventories()
		if len(inventories) != 1 || inventories[0].AvailableQuantity != 0 {
			t.Fatalf("zero inventory: %+v", inventories)
		}
		if _, err := mappings.GetProductMapping(ctx, "primary", "p1"); err != nil {
			t.Fatalf("mapping retained: %v", err)
		}
		if result.MappingCreated {
			t.Fatalf("no new mapping: %+v", result)
		}
	})

	disableCases := []struct {
		name               string
		active, sellOnline bool
	}{
		{"inactive online", false, true},
		{"inactive offline", false, false},
	}
	for _, tc := range disableCases {
		t.Run("mapped "+tc.name+" unpublish", func(t *testing.T) {
			provider := newProvider()
			service, mappings := testService(provider, map[string]DesiredProduct{"p1": desiredState("p1", tc.active, tc.sellOnline, 0, true)})
			mappings.rows[mappingKey("primary", "p1")] = ProductMapping{ProviderKey: "primary", ProductID: "p1", ExternalProductID: "ext-existing"}
			if _, err := service.SyncProduct(ctx, "primary", "p1"); err != nil {
				t.Fatal(err)
			}
			upserts := provider.Upserts()
			if len(upserts) != 1 || upserts[0].Published {
				t.Fatalf("unpublished: %+v", upserts)
			}
			inventories := provider.Inventories()
			if len(inventories) != 1 || inventories[0].AvailableQuantity != 0 {
				t.Fatalf("zero inventory: %+v", inventories)
			}
		})
	}

	t.Run("unmapped inactive noop", func(t *testing.T) {
		provider := newProvider()
		service, _ := testService(provider, map[string]DesiredProduct{"p1": desiredState("p1", false, true, 0, true)})
		result, err := service.SyncProduct(ctx, "primary", "p1")
		if err != nil {
			t.Fatal(err)
		}
		if result.Outcome != SyncNoOp {
			t.Fatalf("noop: %+v", result)
		}
		if len(provider.Upserts()) != 0 || len(provider.Inventories()) != 0 {
			t.Fatal("no provider calls on unmapped inactive")
		}
	})

	t.Run("mapped not ready zeroes inventory", func(t *testing.T) {
		provider := newProvider()
		service, mappings := testService(provider, map[string]DesiredProduct{"p1": desiredState("p1", true, true, 0, false)})
		mappings.rows[mappingKey("primary", "p1")] = ProductMapping{ProviderKey: "primary", ProductID: "p1", ExternalProductID: "ext-existing"}
		if _, err := service.SyncProduct(ctx, "primary", "p1"); err != nil {
			t.Fatal(err)
		}
		inventories := provider.Inventories()
		if len(inventories) != 1 || inventories[0].AvailableQuantity != 0 || inventories[0].Ready {
			t.Fatalf("safe zero: %+v", inventories)
		}
	})

	t.Run("unknown provider", func(t *testing.T) {
		provider := newProvider()
		service, _ := testService(provider, map[string]DesiredProduct{"p1": desiredState("p1", true, true, 1, true)})
		if _, err := service.SyncProduct(ctx, "missing", "p1"); !UnknownProvider(err) {
			t.Fatalf("unknown provider: %v", err)
		}
	})

	t.Run("product not ready", func(t *testing.T) {
		provider := newProvider()
		service, mappings := testService(provider, map[string]DesiredProduct{})
		mappings.rows[mappingKey("primary", "p1")] = ProductMapping{ProviderKey: "primary", ProductID: "p1", ExternalProductID: "ext-existing"}
		if _, err := service.SyncProduct(ctx, "primary", "p1"); err == nil {
			t.Fatal("missing product must fail")
		}
		if _, err := mappings.GetProductMapping(ctx, "primary", "p1"); err != nil {
			t.Fatalf("mapping retained on not-ready: %v", err)
		}
		if len(provider.Upserts()) != 0 {
			t.Fatal("no provider calls when product not ready")
		}
	})

	t.Run("mapping conflict on remap", func(t *testing.T) {
		provider := newProvider()
		service, mappings := testService(provider, map[string]DesiredProduct{"p1": desiredState("p1", true, true, 1, true)})
		mappings.rows[mappingKey("primary", "p1")] = ProductMapping{ProviderKey: "primary", ProductID: "p1", ExternalProductID: "ext-old"}
		// Force the adapter to claim a different remote identity.
		provider.OverrideExternal("p1", "ext-new")
		if _, err := service.SyncProduct(ctx, "primary", "p1"); !IsMappingConflict(err) {
			t.Fatalf("remap conflict: %v", err)
		}
		if len(provider.Inventories()) != 0 {
			t.Fatal("no inventory while identity disputed")
		}
		stored, err := mappings.GetProductMapping(ctx, "primary", "p1")
		if err != nil || stored.ExternalProductID != "ext-old" {
			t.Fatalf("mapping unchanged: %+v %v", stored, err)
		}
	})

	t.Run("empty external id rejected", func(t *testing.T) {
		registry := NewRegistry()
		empty := &emptyProvider{key: "primary"}
		if err := registry.Register("primary", empty); err != nil {
			t.Fatal(err)
		}
		service := NewCommerceService(registry, &stubMappings{rows: map[string]ProductMapping{}},
			&stubSource{states: map[string]DesiredProduct{"p1": desiredState("p1", true, true, 1, true)}}, nil)
		if _, err := service.SyncProduct(ctx, "primary", "p1"); err == nil {
			t.Fatal("empty external id must fail")
		}
	})
}

type emptyProvider struct{ key ProviderKey }

func (e *emptyProvider) Key() ProviderKey { return e.key }
func (e *emptyProvider) UpsertProduct(_ context.Context, _ ProductUpsertRequest) (ProductUpsertResult, error) {
	return ProductUpsertResult{}, nil
}
func (e *emptyProvider) SetInventory(_ context.Context, _ InventoryUpdateRequest) error { return nil }

func TestSyncProductFailureRecovery(t *testing.T) {
	ctx := context.Background()

	t.Run("mapping persistence failure then retry", func(t *testing.T) {
		provider := NewFakeProvider("primary")
		registry := NewRegistry()
		if err := registry.Register("primary", provider); err != nil {
			t.Fatal(err)
		}
		mappings := &stubMappings{rows: map[string]ProductMapping{},
			failNext: errors.New("db down"), failCount: 1}
		service := NewCommerceService(registry, mappings,
			&stubSource{states: map[string]DesiredProduct{"p1": desiredState("p1", true, true, 7, true)}}, nil)
		if _, err := service.SyncProduct(ctx, "primary", "p1"); err == nil {
			t.Fatal("mapping failure must surface")
		}
		if len(provider.Inventories()) != 0 {
			t.Fatal("no inventory before mapping persists")
		}
		second, err := service.SyncProduct(ctx, "primary", "p1")
		if err != nil {
			t.Fatalf("retry: %v", err)
		}
		// The retry reuses the fake's stable identity: one logical
		// remote product across the failed and retried attempts.
		stable, ok := provider.ExternalID("p1")
		if !ok || stable != second.ExternalProductID {
			t.Fatalf("stable identity: %+v", second)
		}
		if provider.Creations() != 1 {
			t.Fatalf("one remote product, got %d", provider.Creations())
		}
		stored, err := mappings.GetProductMapping(ctx, "primary", "p1")
		if err != nil || stored.ExternalProductID != second.ExternalProductID {
			t.Fatalf("mapping final: %+v %v", stored, err)
		}
		if len(provider.Inventories()) != 1 || provider.Inventories()[0].AvailableQuantity != 7 {
			t.Fatalf("inventory after recovery: %+v", provider.Inventories())
		}
	})

	t.Run("inventory failure then retry reuses mapping", func(t *testing.T) {
		provider := NewFakeProvider("primary")
		provider.FailInventoryOnce("p1", TemporaryError("timeout"))
		registry := NewRegistry()
		if err := registry.Register("primary", provider); err != nil {
			t.Fatal(err)
		}
		service := NewCommerceService(registry, &stubMappings{rows: map[string]ProductMapping{}},
			&stubSource{states: map[string]DesiredProduct{"p1": desiredState("p1", true, true, 7, true)}}, nil)
		if _, err := service.SyncProduct(ctx, "primary", "p1"); err == nil {
			t.Fatal("inventory failure must surface")
		}
		second, err := service.SyncProduct(ctx, "primary", "p1")
		if err != nil {
			t.Fatalf("retry: %v", err)
		}
		if provider.Creations() != 1 {
			t.Fatalf("no new remote product, got %d", provider.Creations())
		}
		if !second.InventoryUpdated || second.ExternalProductID == "" {
			t.Fatalf("retry result: %+v", second)
		}
		if len(provider.Inventories()) != 2 {
			t.Fatalf("inventory retried: %d", len(provider.Inventories()))
		}
	})

	t.Run("provider temporary error surfaces classified", func(t *testing.T) {
		provider := NewFakeProvider("primary")
		provider.FailUpsertOnce("p1", TemporaryError("timeout"))
		registry := NewRegistry()
		if err := registry.Register("primary", provider); err != nil {
			t.Fatal(err)
		}
		service := NewCommerceService(registry, &stubMappings{rows: map[string]ProductMapping{}},
			&stubSource{states: map[string]DesiredProduct{"p1": desiredState("p1", true, true, 7, true)}}, nil)
		errResult, err := service.SyncProduct(ctx, "primary", "p1")
		_ = errResult
		var providerErr *ProviderError
		if !errors.As(err, &providerErr) || !providerErr.Retryable() {
			t.Fatalf("classified temporary: %v", err)
		}
	})

	t.Run("provider validation error terminal", func(t *testing.T) {
		provider := NewFakeProvider("primary")
		provider.FailUpsert("p1", ValidationError("bad sku"))
		registry := NewRegistry()
		if err := registry.Register("primary", provider); err != nil {
			t.Fatal(err)
		}
		service := NewCommerceService(registry, &stubMappings{rows: map[string]ProductMapping{}},
			&stubSource{states: map[string]DesiredProduct{"p1": desiredState("p1", true, true, 7, true)}}, nil)
		_, err := service.SyncProduct(ctx, "primary", "p1")
		var providerErr *ProviderError
		if !errors.As(err, &providerErr) || providerErr.Retryable() {
			t.Fatalf("validation terminal: %v", err)
		}
	})

	t.Run("two provider keys isolated", func(t *testing.T) {
		first := NewFakeProvider("primary")
		second := NewFakeProvider("website")
		registry := NewRegistry()
		if err := registry.Register("primary", first); err != nil {
			t.Fatal(err)
		}
		if err := registry.Register("website", second); err != nil {
			t.Fatal(err)
		}
		states := map[string]DesiredProduct{"p1": desiredState("p1", true, true, 7, true)}
		mappings := &stubMappings{rows: map[string]ProductMapping{}}
		service := NewCommerceService(registry, mappings, &stubSource{states: states}, nil)
		r1, err := service.SyncProduct(ctx, "primary", "p1")
		if err != nil {
			t.Fatal(err)
		}
		r2, err := service.SyncProduct(ctx, "website", "p1")
		if err != nil {
			t.Fatal(err)
		}
		if r1.ExternalProductID == r2.ExternalProductID {
			t.Fatal("providers isolate external identities")
		}
		if r1.ProductOperationKey == r2.ProductOperationKey {
			t.Fatal("operation keys isolate providers")
		}
	})
}

type nilKeyProvider struct{}

func (*nilKeyProvider) Key() ProviderKey { return "typed-nil" }
func (*nilKeyProvider) UpsertProduct(_ context.Context, _ ProductUpsertRequest) (ProductUpsertResult, error) {
	return ProductUpsertResult{}, nil
}
func (*nilKeyProvider) SetInventory(_ context.Context, _ InventoryUpdateRequest) error { return nil }

// TestRegistryRejectsTypedNilProvider proves a typed-nil concrete
// provider inside the interface is rejected without panic.
func TestRegistryRejectsTypedNilProvider(t *testing.T) {
	registry := NewRegistry()
	var typedNil *nilKeyProvider
	if err := registry.Register("typed-nil", typedNil); err == nil {
		t.Fatal("typed-nil provider must be rejected")
	}
	if err := registry.Register("literal-nil", nil); err == nil {
		t.Fatal("literal nil must be rejected")
	}
	if registry.Count() != 0 {
		t.Fatal("failed registrations store nothing")
	}
	if _, err := registry.Get("typed-nil"); !UnknownProvider(err) {
		t.Fatalf("absent key stays unknown: %v", err)
	}
}
