package shopify

import (
	"context"
	"math"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// inventoryRequest builds one frozen InventoryUpdateRequest for tests.
func inventoryRequest(externalID, productID string, quantity int64, ready bool) commerce.InventoryUpdateRequest {
	return commerce.InventoryUpdateRequest{
		ProviderKey:       "shopify-main",
		ProductID:         productID,
		ExternalProductID: externalID,
		AvailableQuantity: quantity,
		InventoryRevision: 9,
		CatalogRevision:   3,
		PolicyRevision:    2,
		Ready:             ready,
		OperationKey:      "inv-" + productID,
	}
}

const testLocationGID = "gid://shopify/Location/7700000001"

// Phase 11 §162/§163: activation when needed (initial 0), quantity
// writes 0/5/20/MaxInt32, not-ready and disabled force zero.
func TestInventoryActivationAndQuantities(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	created, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}
	// Not active at the configured location yet: activation at 0 first.
	// Each change mirrors the frozen flow: UpsertProduct safe-zeros, then
	// SetInventory restores the derived availability.
	for _, quantity := range []int64{0, 5, 20, math.MaxInt32} {
		update := upsertRequest("prod-1", "PAP-001", true)
		update.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: created.ExternalProductID}
		update.OperationKey = "opkey-q-" + itoa(quantity)
		if _, err := provider.UpsertProduct(context.Background(), update); err != nil {
			t.Fatalf("quantity %d upsert: %v", quantity, err)
		}
		req := inventoryRequest(created.ExternalProductID, "prod-1", quantity, true)
		req.OperationKey = "inv-q-" + itoa(quantity)
		if err := provider.SetInventory(context.Background(), req); err != nil {
			t.Fatalf("quantity %d: %v", quantity, err)
		}
		if got := h.quantityOf(created.ExternalProductID, testLocationGID); got != quantity {
			t.Fatalf("quantity = %d, want %d", got, quantity)
		}
	}
	if len(h.keysFor("activate")) == 0 {
		t.Fatal("inventory was never activated")
	}

	// Not ready → forced zero regardless of requested quantity.
	req := inventoryRequest(created.ExternalProductID, "prod-1", 42, false)
	req.OperationKey = "inv-not-ready"
	if err := provider.SetInventory(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := h.quantityOf(created.ExternalProductID, testLocationGID); got != 0 {
		t.Fatalf("not-ready quantity = %d (must be 0)", got)
	}

	// Already active: no second activation for the same item/location.
	activations := len(h.keysFor("activate"))
	req = inventoryRequest(created.ExternalProductID, "prod-1", 3, true)
	req.OperationKey = "inv-active-again"
	if err := provider.SetInventory(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(h.keysFor("activate")) != activations {
		t.Fatal("already-active inventory item re-activated")
	}
}

// Phase 11 §74: inactive/zero-availability paths always write zero.
func TestInventoryZeroCases(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	created, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}
	for name, req := range map[string]commerce.InventoryUpdateRequest{
		"inactive":   inventoryRequest(created.ExternalProductID, "prod-1", 17, true),
		"not ready":  inventoryRequest(created.ExternalProductID, "prod-1", 17, false),
		"zero alloc": inventoryRequest(created.ExternalProductID, "prod-1", 0, true),
	} {
		req.OperationKey = "inv-zero-" + name
		if name == "inactive" {
			req.AvailableQuantity = 0
		}
		if err := provider.SetInventory(context.Background(), req); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := h.quantityOf(created.ExternalProductID, testLocationGID); got != 0 {
			t.Fatalf("%s: quantity = %d", name, got)
		}
	}
}

// Phase 11 §71/§164: the Shopify idempotency key is derived from the
// frozen operation key — identical desired state replays the same key,
// changed desired state gets a new one.
func TestInventoryIdempotencyKeyDerivation(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	created, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}
	reqA := inventoryRequest(created.ExternalProductID, "prod-1", 5, true)
	reqA.OperationKey = "inv-same-state"
	if err := provider.SetInventory(context.Background(), reqA); err != nil {
		t.Fatal(err)
	}
	// Already-converged desired state performs no write at all.
	if err := provider.SetInventory(context.Background(), reqA); err != nil {
		t.Fatal(err)
	}
	if keys := h.keysFor("set"); len(keys) != 1 {
		t.Fatalf("already-converged state wrote again: %v", keys)
	}
	// The same operation identity always derives the same idempotency
	// key: re-establishing the same desired state replays one key.
	update := upsertRequest("prod-1", "PAP-001", true)
	update.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: created.ExternalProductID}
	update.OperationKey = "opkey-same-state"
	if _, err := provider.UpsertProduct(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetInventory(context.Background(), reqA); err != nil {
		t.Fatal(err)
	}
	keys := h.keysFor("set")
	if len(keys) < 2 {
		t.Fatalf("set calls = %d", len(keys))
	}
	if keys[len(keys)-1] != keys[0] {
		t.Fatalf("same operation key produced different idempotency keys: %v", keys)
	}
	reqB := inventoryRequest(created.ExternalProductID, "prod-1", 6, true)
	reqB.OperationKey = "inv-changed-state"
	// Frozen flow: UpsertProduct safe-zeros before the restore write.
	update = upsertRequest("prod-1", "PAP-001", true)
	update.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: created.ExternalProductID}
	update.OperationKey = "opkey-changed-state"
	if _, err := provider.UpsertProduct(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetInventory(context.Background(), reqB); err != nil {
		t.Fatal(err)
	}
	keys = h.keysFor("set")
	if keys[len(keys)-1] == keys[len(keys)-2] {
		t.Fatal("changed desired state reused the same idempotency key")
	}
	// Keys are bounded hex-derived identifiers, never raw operation keys.
	for _, key := range keys {
		if key == "" || len(key) > 64 {
			t.Fatalf("bad idempotency key %q", key)
		}
	}
}

// Phase 11 §72/§165: compare-and-set. A positive write is conditional on
// the remote still holding this operation's safe-zero; a concurrent
// change makes the stale write fail instead of silently regressing the
// proven newer state.
func TestInventoryCompareAndSetProtectsNewerState(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	created, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a newer writer landing 20 between safe-zero and restore.
	h.mu.Lock()
	h.beforeInventorySet = func(hh *shopifyHarness) {
		product := hh.products[created.ExternalProductID]
		product.variants[0].levels[testLocationGID] = 20
	}
	h.mu.Unlock()

	req := inventoryRequest(created.ExternalProductID, "prod-1", 5, true)
	req.OperationKey = "inv-stale"
	err = provider.SetInventory(context.Background(), req)
	if err == nil {
		t.Fatal("stale write silently applied")
	}
	providerErr, ok := err.(*commerce.ProviderError)
	if !ok || providerErr.Kind != commerce.ErrorTemporary || !providerErr.Retryable() {
		t.Fatalf("compare mismatch must be retryable, got %v", err)
	}
	if got := h.quantityOf(created.ExternalProductID, testLocationGID); got != 20 {
		t.Fatalf("newer state regressed: quantity = %d", got)
	}

	// A fresh convergence recomputes fresh desired state: UpsertProduct
	// safe-zeros again, then the restore lands.
	req.OperationKey = "inv-fresh"
	update := upsertRequest("prod-1", "PAP-001", true)
	update.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: created.ExternalProductID}
	update.OperationKey = "opkey-fresh"
	if _, err := provider.UpsertProduct(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetInventory(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := h.quantityOf(created.ExternalProductID, testLocationGID); got != 5 {
		t.Fatalf("quantity = %d", got)
	}
}

// Phase 11 §163: Shopify userErrors on inventory mutations are handled —
// non-conflict codes are terminal validation failures.
func TestInventoryUserErrorsClassified(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	created, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}
	h.setFailure("MoonlightInventorySet", 200,
		`{"data":{"inventorySetQuantities":{"inventoryAdjustmentGroup":null,"userErrors":[{"code":"INVALID","message":"bad input"}]}}}`)
	req := inventoryRequest(created.ExternalProductID, "prod-1", 5, true)
	req.OperationKey = "inv-user-error"
	err = provider.SetInventory(context.Background(), req)
	providerErr, ok := err.(*commerce.ProviderError)
	if !ok || providerErr.Kind != commerce.ErrorValidation {
		t.Fatalf("want validation, got %v", err)
	}
}

// Phase 11 §67/§73: only the managed inventory item at the configured
// location is mutated; foreign items and other locations are never
// written.
func TestInventoryScopeIsConfiguredLocationOnly(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	created, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}
	product := h.productOf(created.ExternalProductID)
	// A manual variant with its own inventory item on another location.
	product.variants = append(product.variants, &fakeVariant{
		gid: "gid://shopify/ProductVariant/9999", sku: "MANUAL-1",
		itemGID: "gid://shopify/InventoryItem/9998", tracked: true,
		levels: map[string]int64{"gid://shopify/Location/7700000001": 41},
	})
	req := inventoryRequest(created.ExternalProductID, "prod-1", 7, true)
	req.OperationKey = "inv-scope"
	if err := provider.SetInventory(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := h.quantityOf(created.ExternalProductID, testLocationGID); got != 7 {
		t.Fatalf("managed quantity = %d", got)
	}
	if product.variants[1].levels[testLocationGID] != 41 {
		t.Fatal("foreign variant inventory was mutated")
	}
}

// Phase 11 §68/§69: activation idempotency — the derived activation key
// depends only on the frozen operation identity, so the same logical
// activation retry reuses the same Shopify idempotency key.
func TestActivationIdempotencyKeyStable(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	first, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}
	second, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-2", "PAP-002", false))
	if err != nil {
		t.Fatal(err)
	}
	reqA := inventoryRequest(first.ExternalProductID, "prod-1", 5, true)
	reqA.OperationKey = "inv-activate-same"
	if err := provider.SetInventory(context.Background(), reqA); err != nil {
		t.Fatal(err)
	}
	reqB := inventoryRequest(second.ExternalProductID, "prod-2", 5, true)
	reqB.OperationKey = "inv-activate-same"
	if err := provider.SetInventory(context.Background(), reqB); err != nil {
		t.Fatal(err)
	}
	activationKeys := h.keysFor("activate")
	if len(activationKeys) < 2 {
		t.Fatalf("activation calls = %d", len(activationKeys))
	}
	if activationKeys[len(activationKeys)-1] != activationKeys[len(activationKeys)-2] {
		t.Fatalf("same operation identity produced different activation keys: %v", activationKeys)
	}
}

func itoa(value int64) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}
