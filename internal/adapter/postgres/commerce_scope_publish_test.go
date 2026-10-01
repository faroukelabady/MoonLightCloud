package postgres

// Phase 9C provider publication attacks on real PostgreSQL against a
// minimal fake WooCommerce HTTPS server. The real adapter's SKU
// preflight/recovery plus mapping Store ownership must fail closed when
// Store B reaches for Store A's remote resources.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/woocommerce"
	"github.com/faroukelabady/MoonLightCloud/internal/config"
)

// fakeWoo is a minimal WooCommerce stand-in: SKU search, product
// create/get/update with ownership metadata, and request recording.
type fakeWoo struct {
	t        *testing.T
	mu       sync.Mutex
	server   *httptest.Server
	products map[int64]map[string]any
	nextID   int64
	puts     []int64
	posts    int
	// dropFirstPost simulates ambiguous creation success: the remote
	// product is stored but the response is replaced with a 500, so the
	// adapter must recover via SKU ownership lookup, not duplicate.
	dropFirstPost bool
	dropped       bool
}

func newFakeWoo(t *testing.T) *fakeWoo {
	t.Helper()
	fake := &fakeWoo{t: t, products: map[int64]map[string]any{}, nextID: 700}
	fake.server = httptest.NewTLSServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.server.Close)
	return fake
}

func (fake *fakeWoo) client() *http.Client {
	fake.t.Helper()
	transport, ok := fake.server.Client().Transport.(*http.Transport)
	if !ok {
		fake.t.Fatal("tls test transport")
	}
	clone := transport.Clone()
	clone.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test-only local TLS
	return &http.Client{Transport: clone, Timeout: 10 * time.Second}
}

func (fake *fakeWoo) meta(product map[string]any) map[string]string {
	out := map[string]string{}
	if meta, ok := product["meta_data"].([]any); ok {
		for _, item := range meta {
			if entry, ok := item.(map[string]any); ok {
				key, _ := entry["key"].(string)
				value, _ := entry["value"].(string)
				out[key] = value
			}
		}
	}
	return out
}

func (fake *fakeWoo) serve(w http.ResponseWriter, r *http.Request) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	write := func(status int, body any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}
	path := strings.TrimPrefix(r.URL.Path, "/wp-json/wc/v3")
	switch {
	case r.Method == http.MethodGet && path == "/products":
		sku := r.URL.Query().Get("sku")
		var out []any
		for id, product := range fake.products {
			if ps, _ := product["sku"].(string); ps == sku {
				copy := map[string]any{}
				for key, value := range product {
					copy[key] = value
				}
				copy["id"] = float64(id)
				out = append(out, copy)
			}
		}
		if out == nil {
			out = []any{}
		}
		write(http.StatusOK, out)
	case r.Method == http.MethodPost && path == "/products":
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			write(http.StatusBadRequest, map[string]any{"code": "bad_request", "message": "bad"})
			return
		}
		if sku, _ := body["sku"].(string); sku != "" {
			for _, product := range fake.products {
				if ps, _ := product["sku"].(string); ps == sku {
					write(http.StatusBadRequest, map[string]any{"code": "product_invalid_sku", "message": "SKU exists", "data": map[string]any{"status": 400}})
					return
				}
			}
		}
		fake.posts++
		id := fake.nextID
		fake.nextID++
		body["id"] = float64(id)
		fake.products[id] = body
		if fake.dropFirstPost && !fake.dropped {
			fake.dropped = true
			write(http.StatusInternalServerError, map[string]any{"code": "error", "message": "lost"})
			return
		}
		write(http.StatusCreated, body)
	case (r.Method == http.MethodGet || r.Method == http.MethodPut) && strings.HasPrefix(path, "/products/"):
		id, err := strconv.ParseInt(strings.TrimPrefix(path, "/products/"), 10, 64)
		if err != nil {
			write(http.StatusBadRequest, map[string]any{"code": "bad_request", "message": "bad"})
			return
		}
		stored, ok := fake.products[id]
		if !ok {
			write(http.StatusNotFound, map[string]any{"code": "woocommerce_rest_product_invalid_id", "message": "missing"})
			return
		}
		if r.Method == http.MethodPut {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				write(http.StatusBadRequest, map[string]any{"code": "bad_request", "message": "bad"})
				return
			}
			for key, value := range body {
				stored[key] = value
			}
			stored["id"] = float64(id)
			fake.puts = append(fake.puts, id)
		}
		copy := map[string]any{}
		for key, value := range stored {
			copy[key] = value
		}
		copy["id"] = float64(id)
		write(http.StatusOK, copy)
	default:
		write(http.StatusNotFound, map[string]any{"code": "not_found", "message": "no"})
	}
}

// preloadExternal inserts a remote Woo product carrying explicit MoonLight
// ownership metadata (simulating A's published resource).
func (fake *fakeWoo) preloadExternal(id int64, sku, productID, providerKey string) {
	fake.t.Helper()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.products[id] = map[string]any{
		"sku": sku,
		"meta_data": []any{
			map[string]any{"key": "_moonlight_product_id", "value": productID},
			map[string]any{"key": "_moonlight_provider_key", "value": providerKey},
		},
	}
	if id >= fake.nextID {
		fake.nextID = id + 1
	}
}

func (fake *fakeWoo) putTargets() []int64 {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]int64{}, fake.puts...)
}

func wooProvider(t *testing.T, fake *fakeWoo, providerKey string) *woocommerce.WooCommerceProvider {
	t.Helper()
	provider, err := woocommerce.NewWooCommerceProvider(config.WooCommerceConfig{
		Enabled: true, ProviderKey: providerKey, BaseURL: fake.server.URL,
		ConsumerKey: "ck_test", ConsumerSecret: "cs_test",
		Currency: config.WooCurrencyEGP, DimensionUnit: config.WooDimensionCM,
		HTTPTimeout: 10 * time.Second,
	}, fake.client())
	if err != nil {
		t.Fatalf("woo provider: %v", err)
	}
	return provider
}

type stubDesiredSource struct {
	states map[string]commerce.DesiredProduct
}

func (s *stubDesiredSource) GetDesiredCommerceProduct(_ context.Context, productID string) (commerce.DesiredProduct, error) {
	state, ok := s.states[productID]
	if !ok {
		return commerce.DesiredProduct{}, fmt.Errorf("product not ready")
	}
	return state, nil
}

func desiredFor(productID, sku, storeID string, published bool, stock int) commerce.DesiredProduct {
	width, height := 70, 100
	return commerce.DesiredProduct{
		Product: commerce.CommerceProduct{
			ProductID: productID, SKU: sku, IsActive: true,
			Names:   []commerce.LocalizedName{{Locale: "en", Name: "Horse"}},
			Prices:  []commerce.Money{{Currency: "EGP", AmountMinor: 65000}},
			WidthCM: &width, HeightCM: &height,
		},
		StoreID:      &storeID,
		Published:    published,
		Availability: catalogAvailability(stock),
	}
}

func catalogAvailability(stock int) catalog.ProductAvailability {
	quantity := stock
	ready := stock > 0
	available := 0
	if ready {
		available = stock
	}
	return catalog.ProductAvailability{
		ProductActive: true, SellOnline: true,
		StockQuantity: &quantity, OnlineAvailable: available, Ready: ready,
	}
}

func publishService(t *testing.T, provider *woocommerce.WooCommerceProvider, source *stubDesiredSource, mappings commerce.ProductMappingRepository) *commerce.CommerceService {
	t.Helper()
	registry := commerce.NewRegistry()
	if err := registry.Register("website", provider); err != nil {
		t.Fatal(err)
	}
	return commerce.NewCommerceService(registry, mappings, source, slog.Default())
}

// TestPublish_CrossStoreTakeoverBlocked is the mandatory attack test: a
// remote product owned by Store A's product metadata cannot be claimed
// by Store B's same-SKU publication; A's mapping is untouched and the
// fake Woo receives no write for the foreign resource.
func TestPublish_CrossStoreTakeoverBlocked(t *testing.T) {
	f := openScopeFixture(t)
	prodA, prodB := scopeProducts(t, f, 700, "pub", "PUB-001")
	createMapping(t, f, "website", prodA, "750")
	fake := newFakeWoo(t)
	fake.preloadExternal(750, "PUB-001", prodA, "website")
	provider := wooProvider(t, fake, "website")
	source := &stubDesiredSource{states: map[string]commerce.DesiredProduct{
		prodB: desiredFor(prodB, "PUB-001", scopeStoreB, true, 20),
	}}
	service := publishService(t, provider, source, NewDevices(f.pool, 5*time.Second))
	_, err := service.SyncProduct(context.Background(), "website", prodB)
	if err == nil {
		t.Fatal("B takeover of A external resource must fail closed")
	}
	if !strings.Contains(err.Error(), "another product") && !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("stable conflict: %v", err)
	}
	mapping, merr := NewDevices(f.pool, 5*time.Second).GetProductMapping(context.Background(), "website", prodA)
	if merr != nil || mapping.ExternalProductID != "750" {
		t.Fatalf("A mapping intact: %+v (%v)", mapping, merr)
	}
	if mapping.StoreID == nil || *mapping.StoreID != scopeStoreA {
		t.Fatalf("A mapping store intact: %v", mapping.StoreID)
	}
	for _, id := range fake.putTargets() {
		if id == 750 {
			t.Fatal("no remote write to the foreign resource")
		}
	}
	var count int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM commerce_product_mappings WHERE provider_key='website' AND external_product_id='750'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("one owner of external 750: %d (%v)", count, err)
	}
}

// TestPublish_SameInstanceFailsClosed proves one provider instance cannot
// host same-SKU Products from two Stores: the second publication fails
// closed instead of merging or taking over.
func TestPublish_SameInstanceFailsClosed(t *testing.T) {
	f := openScopeFixture(t)
	prodA, prodB := scopeProducts(t, f, 720, "pco", "PUB-002")
	fake := newFakeWoo(t)
	provider := wooProvider(t, fake, "website")
	source := &stubDesiredSource{states: map[string]commerce.DesiredProduct{
		prodA: desiredFor(prodA, "PUB-002", scopeStoreA, true, 5),
		prodB: desiredFor(prodB, "PUB-002", scopeStoreB, true, 20),
	}}
	service := publishService(t, provider, source, NewDevices(f.pool, 5*time.Second))
	ctx := context.Background()
	resA, err := service.SyncProduct(ctx, "website", prodA)
	if err != nil {
		t.Fatalf("publish A: %v", err)
	}
	if resA.ExternalProductID == "" {
		t.Fatal("A external assigned")
	}
	_, err = service.SyncProduct(ctx, "website", prodB)
	if err == nil {
		t.Fatal("second same-SKU publication on one instance must fail closed")
	}
	mappingA, merr := NewDevices(f.pool, 5*time.Second).GetProductMapping(ctx, "website", prodA)
	if merr != nil || mappingA.ExternalProductID != resA.ExternalProductID {
		t.Fatalf("A mapping intact: %+v (%v)", mappingA, merr)
	}
}

// TestPublish_IndependentInstancesCoexists proves two provider instances
// backed by independent remotes publish the same SKU independently.
func TestPublish_IndependentInstancesCoexist(t *testing.T) {
	f := openScopeFixture(t)
	prodA, prodB := scopeProducts(t, f, 722, "pci", "PUB-002B")
	fakeA, fakeB := newFakeWoo(t), newFakeWoo(t)
	providerA := wooProvider(t, fakeA, "website")
	providerB := wooProvider(t, fakeB, "website2")
	registry := commerce.NewRegistry()
	if err := registry.Register("website", providerA); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("website2", providerB); err != nil {
		t.Fatal(err)
	}
	source := &stubDesiredSource{states: map[string]commerce.DesiredProduct{
		prodA: desiredFor(prodA, "PUB-002B", scopeStoreA, true, 5),
		prodB: desiredFor(prodB, "PUB-002B", scopeStoreB, true, 20),
	}}
	devices := NewDevices(f.pool, 5*time.Second)
	service := commerce.NewCommerceService(registry, devices, source, slog.Default())
	ctx := context.Background()
	resA, err := service.SyncProduct(ctx, "website", prodA)
	if err != nil {
		t.Fatalf("publish A: %v", err)
	}
	resB, err := service.SyncProduct(ctx, "website2", prodB)
	if err != nil {
		t.Fatalf("publish B: %v", err)
	}
	if resA.ExternalProductID == "" || resB.ExternalProductID == "" {
		t.Fatalf("both externals: %q %q", resA.ExternalProductID, resB.ExternalProductID)
	}
	mappingA, _ := devices.GetProductMapping(ctx, "website", prodA)
	mappingB, _ := devices.GetProductMapping(ctx, "website2", prodB)
	if mappingA.StoreID == nil || *mappingA.StoreID != scopeStoreA || mappingB.StoreID == nil || *mappingB.StoreID != scopeStoreB {
		t.Fatalf("mapping stores: %v %v", mappingA.StoreID, mappingB.StoreID)
	}
}

// TestPublish_SyncProductAdoptsMapping proves the orchestration-level
// adoption: a legacy NULL mapping follows its product to Store A on the
// next sync without republishing identity.
func TestPublish_SyncProductAdoptsMapping(t *testing.T) {
	f := openScopeFixture(t)
	ids := catalogIDs(t, 760, "root-s", "tag-s", "prod-s")
	rootS, tagS, prodS := ids["root-s"], ids["tag-s"], ids["prod-s"]
	names := map[string]string{"ar": "s"}
	f.ingest(t, f.devC, f.credC, "e0000760-0000-4000-8000-000000000001",
		catalog.EventCategorySnapshotV1, categoryPayload(rootS, "active", names, nil, 1))
	f.ingest(t, f.devC, f.credC, "e0000760-0000-4000-8000-000000000002",
		catalog.EventTagSnapshotV1, tagPayload(tagS, "adopt-pub-tag", true, names, 1))
	f.ingest(t, f.devC, f.credC, "e0000760-0000-4000-8000-000000000003",
		catalog.EventProductSnapshotV1, productPayload(prodS, "ADOPT-PUB", "S", rootS, nil, []string{tagS}, 1))
	for _, tc := range []struct{ event, typ string }{
		{"e0000760-0000-4000-8000-000000000001", catalog.EventCategorySnapshotV1},
		{"e0000760-0000-4000-8000-000000000002", catalog.EventTagSnapshotV1},
		{"e0000760-0000-4000-8000-000000000003", catalog.EventProductSnapshotV1},
	} {
		requireOutcome(t, f.projectCatalog(t, tc.event, tc.typ), catalog.OutcomeProcessed, "")
	}
	fake := newFakeWoo(t)
	provider := wooProvider(t, fake, "website")
	legacy := desiredFor(prodS, "ADOPT-PUB", "", true, 5)
	legacy.StoreID = nil
	source := &stubDesiredSource{states: map[string]commerce.DesiredProduct{prodS: legacy}}
	devices := NewDevices(f.pool, 5*time.Second)
	service := publishService(t, provider, source, devices)
	ctx := context.Background()
	res, err := service.SyncProduct(ctx, "website", prodS)
	if err != nil {
		t.Fatalf("legacy publish: %v", err)
	}
	if got := mappingStore(t, f, "website", prodS); got != nil {
		t.Fatalf("mapping starts NULL: %v", got)
	}
	// Product adopts Store A; next sync adopts the mapping in place.
	f.ingest(t, f.devA, f.credA, "e0000760-0000-4000-8000-000000000004",
		catalog.EventProductSnapshotV1, productPayload(prodS, "ADOPT-PUB", "S", rootS, nil, []string{tagS}, 2))
	requireOutcome(t, f.projectCatalog(t, "e0000760-0000-4000-8000-000000000004", catalog.EventProductSnapshotV1), catalog.OutcomeProcessed, "")
	scoped := desiredFor(prodS, "ADOPT-PUB", scopeStoreA, true, 5)
	source.states[prodS] = scoped
	res2, err := service.SyncProduct(ctx, "website", prodS)
	if err != nil {
		t.Fatalf("adopting sync: %v", err)
	}
	if res2.ExternalProductID != res.ExternalProductID {
		t.Fatalf("same remote identity: %q vs %q", res.ExternalProductID, res2.ExternalProductID)
	}
	if got := mappingStore(t, f, "website", prodS); got == nil || *got != scopeStoreA {
		t.Fatalf("mapping adopted A: %v", got)
	}
}

// TestPublish_AmbiguousPostRecovery proves ambiguous creation success
// (remote created, response lost) recovers via SKU ownership lookup
// without duplicating the remote product or losing Store ownership.
func TestPublish_AmbiguousPostRecovery(t *testing.T) {
	f := openScopeFixture(t)
	prodA, _ := scopeProducts(t, f, 780, "amb", "AMB-001")
	fake := newFakeWoo(t)
	fake.dropFirstPost = true
	provider := wooProvider(t, fake, "website")
	source := &stubDesiredSource{states: map[string]commerce.DesiredProduct{
		prodA: desiredFor(prodA, "AMB-001", scopeStoreA, true, 5),
	}}
	service := publishService(t, provider, source, NewDevices(f.pool, 5*time.Second))
	ctx := context.Background()
	if _, err := service.SyncProduct(ctx, "website", prodA); err == nil {
		t.Fatal("first attempt must observe the dropped response")
	}
	res, err := service.SyncProduct(ctx, "website", prodA)
	if err != nil {
		t.Fatalf("recovery: %v", err)
	}
	if res.ExternalProductID == "" {
		t.Fatal("recovered external identity")
	}
	var remote int
	fake.mu.Lock()
	for id := range fake.products {
		_ = id
		remote++
	}
	fake.mu.Unlock()
	if remote != 1 {
		t.Fatalf("one remote product: %d", remote)
	}
	mapping, err := NewDevices(f.pool, 5*time.Second).GetProductMapping(ctx, "website", prodA)
	if err != nil || mapping.ExternalProductID != res.ExternalProductID {
		t.Fatalf("mapping recovered: %+v (%v)", mapping, err)
	}
	if mapping.StoreID == nil || *mapping.StoreID != scopeStoreA {
		t.Fatalf("mapping store: %v", mapping.StoreID)
	}
}
func TestPublish_SafeZeroStoreScoped(t *testing.T) {
	f := openScopeFixture(t)
	prodA, prodB := scopeProducts(t, f, 740, "psz", "PUB-003")
	_ = prodB
	fake := newFakeWoo(t)
	provider := wooProvider(t, fake, "website")
	// A unpublished (policy off in desired state): safe-zero path with a
	// pre-existing mapping.
	createMapping(t, f, "website", prodA, "760")
	fake.preloadExternal(760, "PUB-003", prodA, "website")
	source := &stubDesiredSource{states: map[string]commerce.DesiredProduct{
		prodA: desiredFor(prodA, "PUB-003", scopeStoreA, false, 0),
	}}
	service := publishService(t, provider, source, NewDevices(f.pool, 5*time.Second))
	res, err := service.SyncProduct(context.Background(), "website", prodA)
	if err != nil {
		t.Fatalf("safe-zero sync: %v", err)
	}
	if !res.InventoryUpdated {
		t.Fatalf("inventory addressed: %+v", res)
	}
	if len(fake.putTargets()) == 0 {
		t.Fatal("expected remote inventory write")
	}
}
