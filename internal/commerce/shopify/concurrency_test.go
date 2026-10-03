package shopify

import (
	"context"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// Phase 11 F2 remediation regression tests. These encode the exact
// adversarial repros from the freeze reviews: a stale SyncProduct whose
// remote writes land after a newer completed operation must never leave
// older business state as the durable final outcome with success.

// remoteState is a locked snapshot of the fields the reviews proved
// regretable: content, price, fence revisions, and managed inventory.
type remoteState struct {
	title, desc, price string
	catalogRev         string
	policyRev          string
	operationKey       string
	quantity           int64
}

func (h *shopifyHarness) remoteState(productDecimal string) remoteState {
	h.mu.Lock()
	defer h.mu.Unlock()
	state := remoteState{quantity: -1}
	product := h.products[productDecimal]
	if product == nil {
		return state
	}
	state.title = product.title
	state.desc = product.desc
	state.catalogRev = product.metafields[metafieldNamespace+"."+metafieldCatalogRevision]
	state.policyRev = product.metafields[metafieldNamespace+"."+metafieldPolicyRevision]
	state.operationKey = product.metafields[metafieldNamespace+"."+metafieldProductOperation]
	if len(product.variants) > 0 {
		state.price = product.variants[0].price
		if quantity, ok := product.variants[0].levels[testLocationGID]; ok {
			state.quantity = quantity
		}
	}
	return state
}

// staleRequest builds the stale (older desired state) request.
func staleRequest(externalID string) commerce.ProductUpsertRequest {
	req := upsertRequest("prod-1", "PAP-001", true)
	req.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: externalID}
	req.OperationKey = "opkey-OLD"
	req.Product.Names = []commerce.LocalizedName{{Locale: "ar", Name: "OLD-TITLE"}}
	req.Product.Descriptions = map[string]string{"ar": "old-desc"}
	req.Product.Prices = []commerce.Money{{Currency: "EGP", AmountMinor: 65000}}
	return req
}

// newerRequest builds the newer (fresher desired state) request.
func newerRequest(externalID string) commerce.ProductUpsertRequest {
	req := upsertRequest("prod-1", "PAP-001", true)
	req.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: externalID}
	req.OperationKey = "opkey-NEW"
	req.CatalogRevision = 9
	req.PolicyRevision = 9
	req.Product.CatalogRevision = 9
	req.Product.Names = []commerce.LocalizedName{{Locale: "ar", Name: "NEW-TITLE"}}
	req.Product.Descriptions = map[string]string{"ar": "new-desc"}
	req.Product.Prices = []commerce.Money{{Currency: "EGP", AmountMinor: 70000}}
	return req
}

// wantRetryableSupersession asserts the error is the retryable
// supersession failure (a stale operation must fail retryably, never
// succeed silently).
func wantRetryableSupersession(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("stale operation succeeded silently")
	}
	providerErr, ok := err.(*commerce.ProviderError)
	if !ok || providerErr.Kind != commerce.ErrorTemporary || !providerErr.Retryable() {
		t.Fatalf("stale operation must fail retryably, got %v", err)
	}
	if !strings.Contains(providerErr.Error(), "superseded") {
		t.Fatalf("want supersession failure, got %v", err)
	}
}

// Phase 11 F2 review repro #1 (material class): a stale full sequence
// running AFTER a newer operation completed. Before remediation this
// silently restored OLD-TITLE/old-desc/650.00/rev 3/qty 5 with success.
// Now every stale write aborts retryably and the newer state survives.
func TestStaleSequenceAfterNewerCompletesIsRejected(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	created, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}

	// Newer operation completes fully: content + fence + availability.
	if _, err := provider.UpsertProduct(context.Background(), newerRequest(created.ExternalProductID)); err != nil {
		t.Fatal(err)
	}
	newerInventory := inventoryRequest(created.ExternalProductID, "prod-1", 20, true)
	newerInventory.OperationKey = "inv-NEW"
	newerInventory.CatalogRevision, newerInventory.PolicyRevision = 9, 9
	if err := provider.SetInventory(context.Background(), newerInventory); err != nil {
		t.Fatal(err)
	}
	before := h.remoteState(created.ExternalProductID)
	if before.title != "NEW-TITLE" || before.price != "700.00" || before.quantity != 20 {
		t.Fatalf("newer state not established: %+v", before)
	}

	// Stale operation (rev 3/2, OLD content) full sequence: product AND
	// inventory writes must both fail retryably.
	wantRetryableSupersession(t, func() error {
		_, err := provider.UpsertProduct(context.Background(), staleRequest(created.ExternalProductID))
		return err
	}())
	staleInventory := inventoryRequest(created.ExternalProductID, "prod-1", 5, true)
	staleInventory.OperationKey = "inv-OLD"
	wantRetryableSupersession(t, provider.SetInventory(context.Background(), staleInventory))

	after := h.remoteState(created.ExternalProductID)
	if after != before {
		t.Fatalf("stale operation regressed newer state:\nbefore %+v\nafter  %+v", before, after)
	}
	if h.creations() != 1 {
		t.Fatalf("creates = %d", h.creations())
	}
}

// Phase 11 F2 review repro #2 (interleaving window): the newer operation
// completes while the stale one is mid-flight, between its child writes.
// At most one write may slip; the stale operation must then fail
// retryably at the next stage, and the newer fence/price/inventory must
// survive. The slipped write is repaired by the caller's retry (the
// failure is the durable reconvergence trigger).
func TestInterleavedNewerCompletionFailsStaleOperationRetryably(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	created, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpsertProduct(context.Background(), newerRequest(created.ExternalProductID)); err != nil {
		t.Fatal(err)
	}
	newerInventory := inventoryRequest(created.ExternalProductID, "prod-1", 20, true)
	newerInventory.OperationKey = "inv-NEW"
	newerInventory.CatalogRevision, newerInventory.PolicyRevision = 9, 9
	if err := provider.SetInventory(context.Background(), newerInventory); err != nil {
		t.Fatal(err)
	}

	// Roll the remote back to the pre-newer state and re-run the stale
	// operation with a hook that lets the NEWER operation complete again
	// in the middle of the stale content write.
	h.mu.Lock()
	product := h.products[created.ExternalProductID]
	product.title = "PRE"
	product.desc = "pre"
	product.variants[0].price = "1.00"
	product.metafields[metafieldNamespace+"."+metafieldCatalogRevision] = "3"
	product.metafields[metafieldNamespace+"."+metafieldPolicyRevision] = "2"
	product.metafields[metafieldNamespace+"."+metafieldProductOperation] = "opkey-PRE"
	product.variants[0].levels[testLocationGID] = 0
	fired := false
	h.beforeOp = func(operation string, hh *shopifyHarness) {
		if fired || operation != "MoonlightProductUpdate" {
			return
		}
		fired = true
		// The newer operation completes between the stale operation's
		// safe-zero and its content write.
		current := hh.products[created.ExternalProductID]
		current.title = "NEW-TITLE"
		current.desc = "new-desc"
		current.variants[0].price = "700.00"
		current.metafields[metafieldNamespace+"."+metafieldCatalogRevision] = "9"
		current.metafields[metafieldNamespace+"."+metafieldPolicyRevision] = "9"
		current.metafields[metafieldNamespace+"."+metafieldProductOperation] = "opkey-NEW"
		current.variants[0].levels[testLocationGID] = 20
	}
	h.mu.Unlock()

	wantRetryableSupersession(t, func() error {
		_, err := provider.UpsertProduct(context.Background(), staleRequest(created.ExternalProductID))
		return err
	}())

	after := h.remoteState(created.ExternalProductID)
	if after.catalogRev != "9" || after.policyRev != "9" || after.operationKey == "opkey-OLD" {
		t.Fatalf("newer fence regressed: %+v", after)
	}
	if after.price != "700.00" || after.quantity != 20 {
		t.Fatalf("newer price/inventory regressed: %+v", after)
	}
	if after.title == "NEW-TITLE" && after.desc == "new-desc" {
		// Nothing slipped: also acceptable (strictly better).
		return
	}
	// At most the content write slipped (documented residual); the
	// caller's retry reconverges because the failure is retryable.
	if after.operationKey == "opkey-OLD" {
		t.Fatal("stale operation claimed the fence with success")
	}
}

// Phase 11 F2: a stale zero can never blind-zero a drifted or newer
// positive quantity — the zero write is compare-and-set against the
// observed quantity.
func TestSafeZeroIsCompareAndSet(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	created, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.SetInventory(context.Background(), inventoryRequest(created.ExternalProductID, "prod-1", 5, true)); err != nil {
		t.Fatal(err)
	}
	// A newer writer lands 20 between the stale operation's read and its
	// safe-zero write.
	h.mu.Lock()
	h.beforeInventorySet = func(hh *shopifyHarness) {
		product := hh.products[created.ExternalProductID]
		product.variants[0].levels[testLocationGID] = 20
	}
	h.mu.Unlock()

	update := staleRequest(created.ExternalProductID)
	update.OperationKey = "opkey-ZERO-RACE"
	_, err = provider.UpsertProduct(context.Background(), update)
	if err == nil {
		t.Fatal("blind zero over newer quantity must fail")
	}
	if got := h.quantityOf(created.ExternalProductID, testLocationGID); got != 20 {
		t.Fatalf("newer quantity regressed to %d by a stale zero", got)
	}
}

// Phase 11 F2: stale inventory restore is rejected by the freshness
// fence (not only by the quantity CAS).
func TestStaleInventoryRestoreRejectedByFence(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	created, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}
	// The newer desired state always converges through UpsertProduct
	// first (which stamps the fence), then restores availability.
	if _, err := provider.UpsertProduct(context.Background(), newerRequest(created.ExternalProductID)); err != nil {
		t.Fatal(err)
	}
	newerInventory := inventoryRequest(created.ExternalProductID, "prod-1", 20, true)
	newerInventory.OperationKey = "inv-NEW"
	newerInventory.CatalogRevision, newerInventory.PolicyRevision = 9, 9
	if err := provider.SetInventory(context.Background(), newerInventory); err != nil {
		t.Fatal(err)
	}
	stale := inventoryRequest(created.ExternalProductID, "prod-1", 5, true)
	stale.OperationKey = "inv-OLD"
	wantRetryableSupersession(t, provider.SetInventory(context.Background(), stale))
	if got := h.quantityOf(created.ExternalProductID, testLocationGID); got != 20 {
		t.Fatalf("quantity = %d", got)
	}
}

// Phase 11 NOTE-6: standalone SetInventory(positive) against a positive
// remote is self-healing: one bounded safe-zero re-establishes the
// operation's precondition (the caller is proven non-stale by the
// fence first).
func TestStandaloneSetInventorySelfHeals(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	created, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}
	seed := inventoryRequest(created.ExternalProductID, "prod-1", 20, true)
	seed.OperationKey = "inv-seed"
	if err := provider.SetInventory(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	// Standalone restore without a preceding safe-zero: converges.
	standalone := inventoryRequest(created.ExternalProductID, "prod-1", 5, true)
	standalone.OperationKey = "inv-standalone"
	if err := provider.SetInventory(context.Background(), standalone); err != nil {
		t.Fatal(err)
	}
	if got := h.quantityOf(created.ExternalProductID, testLocationGID); got != 5 {
		t.Fatalf("quantity = %d, want 5", got)
	}
}

// Phase 11 F2: identical concurrent desired state (same revisions and
// publication) derives the same deterministic operation key, so it can
// never register as mutual supersession.
func TestSameDesiredStateIsNotSupersession(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	created, err := provider.UpsertProduct(context.Background(), upsertRequest("prod-1", "PAP-001", true))
	if err != nil {
		t.Fatal(err)
	}
	// A retry of the same desired state under the same operation key
	// converges without any supersession signal.
	retry := upsertRequest("prod-1", "PAP-001", true)
	retry.ExistingExternal = &commerce.ProviderProductRef{ExternalProductID: created.ExternalProductID}
	if _, err := provider.UpsertProduct(context.Background(), retry); err != nil {
		t.Fatal(err)
	}
	state := h.remoteState(created.ExternalProductID)
	if state.operationKey != "opkey-prod-1" {
		t.Fatalf("fence = %+v", state)
	}
}
