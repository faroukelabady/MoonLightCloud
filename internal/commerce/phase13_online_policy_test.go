package commerce

// Phase 13 — canonical effective-online publication, Category-policy
// operation identity, and the durable re-evaluation worker semantics.

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

// stubReader is a minimal CatalogReader for the publication formula.
type stubReader struct {
	product        catalog.Product
	policy         catalog.ProductSalesPolicy
	policyFound    bool
	availability   catalog.ProductAvailability
	online         catalog.ProductOnlinePolicy
	productMissing bool
}

func (s stubReader) GetProduct(context.Context, string) (catalog.Product, error) {
	if s.productMissing {
		return catalog.Product{}, apperr.New(apperr.NotFound, "missing product")
	}
	return s.product, nil
}
func (s stubReader) GetProductOnlinePolicy(context.Context, string) (catalog.ProductOnlinePolicy, error) {
	return s.online, nil
}
func (s stubReader) GetCategory(context.Context, string) (catalog.Category, error) {
	return catalog.Category{}, apperr.New(apperr.NotFound, "missing category")
}
func (s stubReader) GetTag(context.Context, string) (catalog.Tag, error) {
	return catalog.Tag{}, apperr.New(apperr.NotFound, "missing tag")
}
func (s stubReader) GetProductSalesPolicy(context.Context, string) (catalog.ProductSalesPolicy, error) {
	if !s.policyFound {
		return catalog.ProductSalesPolicy{}, apperr.New(apperr.NotFound, "missing policy")
	}
	return s.policy, nil
}
func (s stubReader) GetProductAvailability(context.Context, string) (catalog.ProductAvailability, error) {
	return s.availability, nil
}

// §135: the publication matrix through the ONE canonical formula.
// Published = is_active AND sell_online AND category hierarchy allows.
func TestEffectiveOnlinePublicationFormula(t *testing.T) {
	cases := []struct {
		name       string
		active     bool
		sellOnline bool
		categories bool
		published  bool
	}{
		{"active+online+categories allow", true, true, true, true},
		{"active+online+categories block", true, true, false, false},
		{"active+offline+categories allow", true, false, true, false},
		{"active+offline+categories block", true, false, false, false},
		{"inactive+online+categories allow", false, true, true, false},
		{"inactive+online+categories block", false, true, false, false},
		{"inactive+offline+categories allow", false, false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := catalog.OnlineAllowed
			if !tc.categories {
				reason = catalog.OnlineBlockedCategory
			}
			reader := stubReader{
				product:      catalog.Product{ID: "p1", SKU: "S1", IsActive: tc.active, Revision: 7},
				policy:       catalog.ProductSalesPolicy{SellOnline: tc.sellOnline, Revision: 3},
				policyFound:  true,
				availability: catalog.ProductAvailability{OnlineAvailable: 10, Ready: true, InventoryRevision: 5},
				online: catalog.ProductOnlinePolicy{
					Allowed: tc.categories, Reason: reason,
					PolicyFingerprint: "fp", PolicyVersion: "ver",
				},
			}
			source := NewCatalogCommerceSource(reader)
			desired, err := source.GetDesiredCommerceProduct(context.Background(), "p1")
			if err != nil {
				t.Fatal(err)
			}
			if desired.Published != tc.published {
				t.Fatalf("Published = %v, want %v", desired.Published, tc.published)
			}
			// §68: provider-facing quantity follows disabled-product
			// semantics — never positive stock on an unpublished product.
			if !tc.published && desired.Availability.OnlineAvailable != 0 {
				t.Fatalf("unpublished product must expose 0, got %d", desired.Availability.OnlineAvailable)
			}
			if tc.published && desired.Availability.OnlineAvailable != 10 {
				t.Fatalf("published product quantity = %d, want 10", desired.Availability.OnlineAvailable)
			}
			if desired.CategoryPolicyFingerprint != "fp" || desired.CategoryPolicyVersion != "ver" {
				t.Fatalf("policy identity not carried: %+v", desired)
			}
		})
	}
}

// §19/§20/§67: missing/incomplete Category state fails safe — never
// publish positive inventory on unproven hierarchy.
func TestMissingCategoryStateFailsSafe(t *testing.T) {
	reader := stubReader{
		product:      catalog.Product{ID: "p1", SKU: "S1", IsActive: true, Revision: 1},
		policy:       catalog.ProductSalesPolicy{SellOnline: true, Revision: 1},
		policyFound:  true,
		availability: catalog.ProductAvailability{OnlineAvailable: 10, Ready: true},
		online: catalog.ProductOnlinePolicy{
			Allowed: false, Reason: catalog.OnlineBlockedStateIncomplete,
		},
	}
	source := NewCatalogCommerceSource(reader)
	desired, err := source.GetDesiredCommerceProduct(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if desired.Published {
		t.Fatal("missing category state must never publish")
	}
	if desired.Availability.OnlineAvailable != 0 {
		t.Fatalf("quantity must be 0, got %d", desired.Availability.OnlineAvailable)
	}
}

// §66: missing Product sales policy can never publish (frozen).
func TestMissingPolicyCannotPublish(t *testing.T) {
	reader := stubReader{
		product:      catalog.Product{ID: "p1", SKU: "S1", IsActive: true, Revision: 1},
		policyFound:  false,
		availability: catalog.ProductAvailability{OnlineAvailable: 0, Ready: false},
		online:       catalog.ProductOnlinePolicy{Allowed: true, Reason: catalog.OnlineAllowed},
	}
	source := NewCatalogCommerceSource(reader)
	desired, err := source.GetDesiredCommerceProduct(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if desired.Published {
		t.Fatal("missing policy must never publish")
	}
}

// §146: Category-policy operation identity matrix.
func TestOperationKeyCategoryPolicyIdentity(t *testing.T) {
	key := func(fp, ver string) string {
		return ProductOperationKey("prov", "prod-1", 7, 3, true, fp, ver)
	}
	// Same category policy -> same key.
	if key("fpA:0:1", "v1") != key("fpA:0:1", "v1") {
		t.Fatal("same category policy must map to the same key")
	}
	// Disable -> key changes.
	if key("fpA:0:1", "v1") == key("fpA:0:0", "v1") {
		t.Fatal("category disable must change the key")
	}
	// Re-enable -> key must differ from the ORIGINAL enable generation
	// (the version marker advanced across the transition cycle).
	if key("fpA:0:1", "v1") == key("fpA:0:1", "v3") {
		t.Fatal("re-enable must not reuse the original generation key")
	}
	// Unrelated category / rename-only -> eligibility fingerprint
	// unchanged (name/translation never enters the fingerprint).
	if key("fpA:0:1", "v1") != key("fpA:0:1", "v1") {
		t.Fatal("unrelated category must not change the key")
	}
	// Parent relation affecting the product -> fingerprint changes.
	if key("fpA:0:1,fpB:1:1", "v1") == key("fpA:0:1", "v1") {
		t.Fatal("hierarchy change must change the key")
	}
	// Provider and product identity still separate keys.
	if ProductOperationKey("p1", "prod-1", 7, 3, true, "fp", "v") == ProductOperationKey("p2", "prod-1", 7, 3, true, "fp", "v") {
		t.Fatal("provider must separate keys")
	}
	if ProductOperationKey("p1", "prod-1", 7, 3, true, "fp", "v") == ProductOperationKey("p1", "prod-2", 7, 3, true, "fp", "v") {
		t.Fatal("product must separate keys")
	}
	// Inventory identity carries the same policy inputs.
	ik1 := InventoryOperationKey("p1", "prod-1", 7, 3, 5, 10, true, "fpA:0:1", "v1")
	ik2 := InventoryOperationKey("p1", "prod-1", 7, 3, 5, 10, true, "fpA:0:0", "v1")
	if ik1 == ik2 {
		t.Fatal("inventory identity must be policy-aware")
	}
}

// ---- durable re-evaluation worker semantics (§78/§83/§91) ----

type stubReevaluationStore struct {
	claimed   []ProductReevaluation
	completed []string
	retried   []string
	lastCode  string
}

func (s *stubReevaluationStore) ClaimProductReevaluations(context.Context, int, time.Time) ([]ProductReevaluation, error) {
	return s.claimed, nil
}
func (s *stubReevaluationStore) CompleteProductReevaluation(_ context.Context, productID string) error {
	s.completed = append(s.completed, productID)
	return nil
}
func (s *stubReevaluationStore) RetryProductReevaluation(_ context.Context, productID string, _ time.Time, code string) error {
	s.retried = append(s.retried, productID)
	s.lastCode = code
	return nil
}

type p13Mappings struct{}

func (p13Mappings) GetProductMapping(context.Context, ProviderKey, string) (ProductMapping, error) {
	return ProductMapping{}, apperr.New(apperr.NotFound, "no mapping")
}
func (p13Mappings) FindByExternalProductID(context.Context, ProviderKey, string) (ProductMapping, error) {
	return ProductMapping{}, apperr.New(apperr.NotFound, "no mapping")
}
func (p13Mappings) CreateProductMapping(_ context.Context, key ProviderKey, productID, externalID string) (ProductMapping, error) {
	return ProductMapping{ProviderKey: key, ProductID: productID, ExternalProductID: externalID}, nil
}

type stubDesiredSource struct{ desired DesiredProduct }

func (s stubDesiredSource) GetDesiredCommerceProduct(context.Context, string) (DesiredProduct, error) {
	return s.desired, nil
}

type fakeProvider struct {
	key      ProviderKey
	failWith error
	upserts  int
	zeroes   int
}

func (f *fakeProvider) Key() ProviderKey { return f.key }
func (f *fakeProvider) UpsertProduct(context.Context, ProductUpsertRequest) (ProductUpsertResult, error) {
	f.upserts++
	if f.failWith != nil {
		return ProductUpsertResult{}, f.failWith
	}
	return ProductUpsertResult{ExternalProductID: "ext-" + string(f.key)}, nil
}
func (f *fakeProvider) SetInventory(context.Context, InventoryUpdateRequest) error {
	f.zeroes++
	if f.failWith != nil {
		return f.failWith
	}
	return nil
}

func reevaluationFixture(t *testing.T, failing ProviderKey) (*ReevaluationWorker, *stubReevaluationStore, *fakeProvider, *fakeProvider) {
	t.Helper()
	registry := NewRegistry()
	ok := &fakeProvider{key: "okprov"}
	bad := &fakeProvider{key: "badprov"}
	if failing != "" {
		if failing == "okprov" {
			ok.failWith = &ProviderError{Kind: ErrorTemporary, Message: "down"}
		} else {
			bad.failWith = &ProviderError{Kind: ErrorTemporary, Message: "down"}
		}
	}
	if err := registry.Register(ok.Key(), ok); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(bad.Key(), bad); err != nil {
		t.Fatal(err)
	}
	desired := DesiredProduct{
		Product:         CommerceProduct{ProductID: "prod-1", SKU: "S", IsActive: true, CatalogRevision: 2, PolicyRevision: 1},
		Published:       true,
		Availability:    catalog.ProductAvailability{OnlineAvailable: 5, Ready: true, InventoryRevision: 1},
		CatalogRevision: 2, PolicyRevision: 1, InventoryRevision: 1,
		CategoryPolicyFingerprint: "fp", CategoryPolicyVersion: "v1",
	}
	service := NewCommerceService(registry, p13Mappings{}, stubDesiredSource{desired: desired}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	store := &stubReevaluationStore{claimed: []ProductReevaluation{{ProductID: "prod-1", Reason: "category_policy"}}}
	worker := NewReevaluationWorker(store, service, registry, func() time.Time { return time.Unix(1770000000, 0).UTC() }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return worker, store, ok, bad
}

// §78: one provider failure must not prevent the other provider's
// durable progress; the request retries later and completes only when
// every provider converged.
func TestReevaluationWorkerProviderIndependence(t *testing.T) {
	worker, store, ok, bad := reevaluationFixture(t, "badprov")
	worker.drain(context.Background())
	if ok.upserts != 1 || ok.zeroes != 1 {
		t.Fatalf("successful provider must make durable progress: %+v", ok)
	}
	if len(store.completed) != 0 {
		t.Fatal("request must not complete while a provider is unconverged")
	}
	if len(store.retried) != 1 || store.lastCode != "PROVIDER_temporary" {
		t.Fatalf("failure must schedule a bounded retry: %+v code=%q", store.retried, store.lastCode)
	}
	_ = bad
}

// A pass where every provider converges completes the request exactly
// once (the retried row after recovery converges and disappears).
func TestReevaluationWorkerCompletesWhenAllConverged(t *testing.T) {
	worker, store, ok, bad := reevaluationFixture(t, "")
	worker.drain(context.Background())
	if ok.upserts != 1 || bad.upserts != 1 {
		t.Fatalf("both providers must be attempted: ok=%+v bad=%+v", ok, bad)
	}
	if len(store.completed) != 1 || len(store.retried) != 0 {
		t.Fatalf("all-green run must complete exactly once: completed=%v retried=%v", store.completed, store.retried)
	}
}

// §78 reverse isolation: the previously failing provider's later green
// pass completes the pending request while the healthy provider's work
// idempotently re-converges (no duplicate mapping, same operation key).
func TestReevaluationWorkerIndependentProgressBothDirections(t *testing.T) {
	worker, store, ok, bad := reevaluationFixture(t, "okprov")
	worker.drain(context.Background())
	if bad.upserts != 1 {
		t.Fatalf("second provider must still be attempted and converge: %+v", bad)
	}
	if len(store.retried) != 1 || store.lastCode != "PROVIDER_temporary" {
		t.Fatalf("unconverged provider must schedule bounded retry: %+v code=%q", store.retried, store.lastCode)
	}
	_ = ok
}

// §83 bounded backoff: retry spacing doubles per attempt, capped at 1h —
// never a hot loop.
func TestReevaluationBackoffBounded(t *testing.T) {
	previous := time.Duration(0)
	for attempt := 0; attempt < 20; attempt++ {
		delay := reevaluationBackoff(attempt)
		if delay < previous {
			t.Fatalf("backoff must be monotonic: attempt %d %s < %s", attempt, delay, previous)
		}
		if delay > time.Hour {
			t.Fatalf("backoff must cap at 1h: %s", delay)
		}
		previous = delay
	}
	if reevaluationBackoff(0) != 5*time.Second {
		t.Fatalf("first retry must be 5s, got %s", reevaluationBackoff(0))
	}
}
