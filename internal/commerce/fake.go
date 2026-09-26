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
