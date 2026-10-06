package commerce

import (
	"context"
	"fmt"
	"sync"
)

// FakeProvider is a TEST-ONLY CommerceProvider implementation: in-memory,
// deterministic, no network. It models idempotent creation by operation
// key so retry tests can prove no duplicate remote products. Never
// register it outside tests.
type FakeProvider struct {
	key ProviderKey

	mu                 sync.Mutex
	upserts            []ProductUpsertRequest
	inventories        []InventoryUpdateRequest
	byOperationKey     map[string]string
	byProduct          map[string]string
	creations          int
	upsertError        map[string]error
	upsertErrorOnce    map[string]error
	overrideExternal   map[string]string
	inventoryError     map[string]error
	inventoryErrorOnce map[string]error

	variantUpserts        []ProductVariantsUpsertRequest
	variantInventories    []VariantInventoryUpdateRequest
	variantExternal       map[string]string
	variantOverride       map[string]string
	variantCreations      int
	variantUpsertError    map[string]error
	variantInventoryError map[string]error
}

// NewFakeProvider returns a test provider bound to one key.
func NewFakeProvider(key ProviderKey) *FakeProvider {
	return &FakeProvider{
		key:                key,
		byOperationKey:     map[string]string{},
		byProduct:          map[string]string{},
		upsertError:        map[string]error{},
		upsertErrorOnce:    map[string]error{},
		overrideExternal:   map[string]string{},
		inventoryError:     map[string]error{},
		inventoryErrorOnce: map[string]error{},

		variantExternal:       map[string]string{},
		variantOverride:       map[string]string{},
		variantUpsertError:    map[string]error{},
		variantInventoryError: map[string]error{},
	}
}

// Key implements CommerceProvider.
func (f *FakeProvider) Key() ProviderKey { return f.key }

// FailUpsert injects a persistent upsert failure for one product.
func (f *FakeProvider) FailUpsert(productID string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upsertError[productID] = err
}

// FailUpsertOnce injects a one-shot upsert failure for one product.
func (f *FakeProvider) FailUpsertOnce(productID string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upsertErrorOnce[productID] = err
}

// FailInventory injects a persistent inventory failure for one product.
func (f *FakeProvider) FailInventory(productID string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inventoryError[productID] = err
}

// FailInventoryOnce injects a one-shot inventory failure for one product.
func (f *FakeProvider) FailInventoryOnce(productID string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inventoryErrorOnce[productID] = err
}

// OverrideExternal forces one product's next upserts to return a fixed
// external ID, modeling an adapter that claims a disputed identity.
func (f *FakeProvider) OverrideExternal(productID, externalID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.overrideExternal[productID] = externalID
}
func (f *FakeProvider) UpsertProduct(_ context.Context, req ProductUpsertRequest) (ProductUpsertResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts = append(f.upserts, req)
	if err, ok := f.upsertErrorOnce[req.ProductID]; ok {
		delete(f.upsertErrorOnce, req.ProductID)
		return ProductUpsertResult{}, err
	}
	if err, ok := f.upsertError[req.ProductID]; ok {
		return ProductUpsertResult{}, err
	}
	if external, ok := f.byOperationKey[req.OperationKey]; ok {
		return ProductUpsertResult{ExternalProductID: external}, nil
	}
	if external, ok := f.overrideExternal[req.ProductID]; ok {
		return ProductUpsertResult{ExternalProductID: external}, nil
	}
	if req.ExistingExternal != nil && req.ExistingExternal.ExternalProductID != "" {
		// A mapped product converges its known remote identity; the
		// adapter must not mint a second one.
		f.byOperationKey[req.OperationKey] = req.ExistingExternal.ExternalProductID
		f.byProduct[req.ProductID] = req.ExistingExternal.ExternalProductID
		return ProductUpsertResult{ExternalProductID: req.ExistingExternal.ExternalProductID}, nil
	}
	if external, ok := f.byProduct[req.ProductID]; ok {
		f.byOperationKey[req.OperationKey] = external
		return ProductUpsertResult{ExternalProductID: external}, nil
	}
	f.creations++
	external := fmt.Sprintf("fake-ext-%s-%d", f.key, f.creations)
	f.byOperationKey[req.OperationKey] = external
	f.byProduct[req.ProductID] = external
	return ProductUpsertResult{ExternalProductID: external}, nil
}

// SetInventory implements CommerceProvider, capturing the request.
func (f *FakeProvider) SetInventory(_ context.Context, req InventoryUpdateRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inventories = append(f.inventories, req)
	if err, ok := f.inventoryErrorOnce[req.ProductID]; ok {
		delete(f.inventoryErrorOnce, req.ProductID)
		return err
	}
	if err, ok := f.inventoryError[req.ProductID]; ok {
		return err
	}
	return nil
}

// Upserts returns captured upsert requests in call order.
func (f *FakeProvider) Upserts() []ProductUpsertRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ProductUpsertRequest(nil), f.upserts...)
}

// Inventories returns captured inventory requests in call order.
func (f *FakeProvider) Inventories() []InventoryUpdateRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]InventoryUpdateRequest(nil), f.inventories...)
}

// Creations counts distinct external products issued.
func (f *FakeProvider) Creations() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creations
}

// ExternalID returns the stable external ID issued for one product.
func (f *FakeProvider) ExternalID(productID string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	external, ok := f.byProduct[productID]
	return external, ok
}

// Phase 17 variant capability (test-only): FakeProvider implements
// commerce.VariantCommerceProvider so orchestration tests can prove
// per-variant mapping and inventory semantics without any network.

// UpsertProductVariants implements VariantCommerceProvider. Existing
// remote identities are honored (never duplicated); new variants mint
// deterministic identities. A recorded override on one variant models a
// disputed-identity adapter.
func (f *FakeProvider) UpsertProductVariants(_ context.Context, req ProductVariantsUpsertRequest) (ProductVariantsUpsertResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.variantUpserts = append(f.variantUpserts, req)
	if err, ok := f.variantUpsertError[req.ProductID]; ok {
		return ProductVariantsUpsertResult{}, err
	}
	result := ProductVariantsUpsertResult{Variants: map[string]string{}}
	for _, variant := range req.Product.Variants {
		if external, ok := f.variantOverride[variant.VariantID]; ok {
			result.Variants[variant.VariantID] = external
			continue
		}
		if external, ok := req.ExistingVariants[variant.VariantID]; ok && external != "" {
			result.Variants[variant.VariantID] = external
			continue
		}
		if external, ok := f.variantExternal[variant.VariantID]; ok {
			result.Variants[variant.VariantID] = external
			continue
		}
		f.variantCreations++
		external := fmt.Sprintf("fake-variant-%s-%d", f.key, f.variantCreations)
		f.variantExternal[variant.VariantID] = external
		result.Variants[variant.VariantID] = external
	}
	return result, nil
}

// SetVariantInventory implements VariantCommerceProvider, capturing the
// request.
func (f *FakeProvider) SetVariantInventory(_ context.Context, req VariantInventoryUpdateRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.variantInventories = append(f.variantInventories, req)
	if err, ok := f.variantInventoryError[req.VariantID]; ok {
		return err
	}
	return nil
}

// VariantUpserts returns captured variant upsert requests in call order.
func (f *FakeProvider) VariantUpserts() []ProductVariantsUpsertRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ProductVariantsUpsertRequest(nil), f.variantUpserts...)
}

// VariantInventories returns captured per-variant inventory requests.
func (f *FakeProvider) VariantInventories() []VariantInventoryUpdateRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]VariantInventoryUpdateRequest(nil), f.variantInventories...)
}

// VariantCreations counts distinct external variants issued.
func (f *FakeProvider) VariantCreations() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.variantCreations
}

// FailVariantUpsert injects a persistent variant upsert failure.
func (f *FakeProvider) FailVariantUpsert(productID string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.variantUpsertError[productID] = err
}

// FailVariantInventory injects a persistent per-variant inventory failure.
func (f *FakeProvider) FailVariantInventory(variantID string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.variantInventoryError[variantID] = err
}

// OverrideVariantExternal forces one variant's identity to a fixed
// external ID, modeling an adapter that claims a disputed identity.
func (f *FakeProvider) OverrideVariantExternal(variantID, externalID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.variantOverride[variantID] = externalID
}

// ResetVariantState drops recorded variant remote identities, modeling a
// provider-side mapping loss (the remote variations still exist and must
// be re-adopted, never duplicated).
func (f *FakeProvider) ResetVariantState() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.variantExternal = map[string]string{}
}

var _ VariantCommerceProvider = (*FakeProvider)(nil)
