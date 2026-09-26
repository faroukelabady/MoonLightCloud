package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// commerceIDs mints deterministic UUIDs disjoint from catalog fixture
// bases used elsewhere in this package.
func isCommerceNotFound(err error) bool {
	var appErr *apperr.Error
	return errors.As(err, &appErr) && appErr.Kind == apperr.NotFound
}
func commerceIDs(t *testing.T, base int, names ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for i, name := range names {
		out[name] = fmt.Sprintf("d%07x-0000-4000-8000-%012x", base+i, 0xd00ce0+base+i)
		if len(out[name]) != 36 {
			t.Fatalf("bad fixture uuid for %s", name)
		}
	}
	return out
}

func commerceProductID(base int) string {
	return fmt.Sprintf("e%07x-0000-4000-8000-%012x", base, 0xe00ce0+base)
}

// TestCommerceMappingRepository proves create, idempotent same-pair,
// both conflict directions, multi-provider isolation, and lookups.
func TestCommerceMappingRepository(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	productA := commerceProductID(0xA0)
	productB := commerceProductID(0xB0)

	created, err := store.CreateProductMapping(ctx, "primary", productA, "ext-1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ProviderKey != "primary" || created.ProductID != productA || created.ExternalProductID != "ext-1" {
		t.Fatalf("mapping: %+v", created)
	}
	if created.CreatedAt.IsZero() {
		t.Fatal("timestamps recorded")
	}

	// Same pair is idempotent, no duplicate.
	again, err := store.CreateProductMapping(ctx, "primary", productA, "ext-1")
	if err != nil {
		t.Fatalf("idempotent: %v", err)
	}
	if again.ExternalProductID != "ext-1" {
		t.Fatalf("same pair: %+v", again)
	}

	// Same provider+product, different external: conflict.
	if _, err := store.CreateProductMapping(ctx, "primary", productA, "ext-2"); !commerce.IsMappingConflict(err) {
		t.Fatalf("product conflict: %v", err)
	}

	// Same provider+external, different product: conflict.
	if _, err := store.CreateProductMapping(ctx, "primary", productB, "ext-1"); !commerce.IsMappingConflict(err) {
		t.Fatalf("external conflict: %v", err)
	}

	// Same product under a second provider: valid and independent.
	other, err := store.CreateProductMapping(ctx, "website", productA, "ext-1")
	if err != nil {
		t.Fatalf("multi-provider: %v", err)
	}
	if other.ProviderKey != "website" {
		t.Fatalf("provider isolation: %+v", other)
	}

	// Lookups by product and by external ID.
	found, err := store.GetProductMapping(ctx, "primary", productA)
	if err != nil || found.ExternalProductID != "ext-1" {
		t.Fatalf("lookup by product: %+v %v", found, err)
	}
	byExternal, err := store.FindByExternalProductID(ctx, "primary", "ext-1")
	if err != nil || byExternal.ProductID != productA {
		t.Fatalf("lookup by external: %+v %v", byExternal, err)
	}
	if _, err := store.GetProductMapping(ctx, "primary", productB); !isCommerceNotFound(err) {
		t.Fatalf("missing lookup: %v", err)
	}

	// Invalid inputs fail closed.
	for _, tc := range []struct {
		name              string
		key               commerce.ProviderKey
		product, external string
	}{
		{"bad key", "BAD KEY", productA, "ext-9"},
		{"bad product", "primary", "nope", "ext-9"},
		{"empty external", "primary", productA, ""},
	} {
		if _, err := store.CreateProductMapping(ctx, tc.key, tc.product, tc.external); err == nil {
			t.Fatalf("%s must fail", tc.name)
		}
	}
}

// TestCommerceMappingConcurrentSame proves concurrent same-pair creation
// resolves to one durable mapping idempotently.
func TestCommerceMappingConcurrentSame(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	productID := commerceProductID(0xC0)

	start := make(chan struct{})
	results := make(chan error, 4)
	for i := 0; i < 4; i++ {
		go func() {
			<-start
			_, err := store.CreateProductMapping(ctx, "primary", productID, "ext-same")
			results <- err
		}()
	}
	close(start)
	for i := 0; i < 4; i++ {
		if err := <-results; err != nil {
			t.Fatalf("concurrent same: %v", err)
		}
	}
	stored, err := store.GetProductMapping(ctx, "primary", productID)
	if err != nil || stored.ExternalProductID != "ext-same" {
		t.Fatalf("one mapping: %+v %v", stored, err)
	}
}

// TestCommerceMappingConcurrentConflict proves same provider/product with
// different external IDs resolves to exactly one winner with conflicts
// elsewhere: no silent overwrite.
func TestCommerceMappingConcurrentConflict(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	productID := commerceProductID(0xD0)

	start := make(chan struct{})
	type outcome struct {
		external string
		err      error
	}
	results := make(chan outcome, 2)
	for _, external := range []string{"ext-x", "ext-y"} {
		go func(external string) {
			<-start
			_, err := store.CreateProductMapping(ctx, "primary", productID, external)
			results <- outcome{external, err}
		}(external)
	}
	close(start)
	var winner string
	conflicts := 0
	for i := 0; i < 2; i++ {
		result := <-results
		if result.err == nil {
			winner = result.external
		} else if commerce.IsMappingConflict(result.err) {
			conflicts++
		} else {
			t.Fatalf("unexpected: %v", result.err)
		}
	}
	if winner == "" || conflicts != 1 {
		t.Fatalf("one winner one conflict: %q %d", winner, conflicts)
	}
	stored, err := store.GetProductMapping(ctx, "primary", productID)
	if err != nil || stored.ExternalProductID != winner {
		t.Fatalf("winner durable: %+v %v", stored, err)
	}
}

// commerceFixture projects one sellable product with policy and
// inventory: stock 10, online, uncapped unless overridden.
func commerceFixture(t *testing.T, env *saleEnv, base int, prefix, sku string, stock int) string {
	t.Helper()
	root, sub, tag := catalogProductFixture(t, env, base, prefix)
	productID := commerceProductID(base)
	pe := commerceIDs(t, base, "product")["product"]
	ingestCatalog(t, env, pe, catalog.EventProductSnapshotV1, "2026-09-20T12:00:00Z",
		productPayload(productID, sku, "متجر", root, []string{sub}, []string{tag}, 1))
	projectCatalogOnce(t, env, pe)
	oe := commerceIDs(t, base+1000, "policy")["policy"]
	ingestCatalog(t, env, oe, catalog.EventProductSalesPolicySnapshotV1, "2026-09-20T12:01:00Z",
		policyPayload(productID, 1, true, true, nil))
	projectCatalogOnce(t, env, oe)
	ie := commerceIDs(t, base+2000, "inventory")["inventory"]
	ingestCatalog(t, env, ie, catalog.EventInventoryProductSnapshotV1, "2026-09-20T12:02:00Z",
		inventoryPayload(productID, 1, stock))
	projectCatalogOnce(t, env, ie)
	return productID
}

// TestCommerceSyncEndToEnd proves orchestration against real Phase 5C
// state: unmapped create with derived availability, idempotent repeat,
// and exact money fidelity through the boundary.
func TestCommerceSyncEndToEnd(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	productID := commerceFixture(t, env, 0xE0, "ce2e", "PAP-CE2E", 10)

	provider := commerce.NewFakeProvider("primary")
	registry := commerce.NewRegistry()
	if err := registry.Register("primary", provider); err != nil {
		t.Fatal(err)
	}
	source := commerce.NewCatalogCommerceSource(catalog.NewService(store))
	service := commerce.NewCommerceService(registry, store, source, nilLogger())

	result, err := service.SyncProduct(ctx, "primary", productID)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Outcome != commerce.SyncProduct || !result.MappingCreated || !result.InventoryUpdated {
		t.Fatalf("create: %+v", result)
	}
	upserts := provider.Upserts()
	if len(upserts) != 1 {
		t.Fatalf("one upsert: %d", len(upserts))
	}
	req := upserts[0]
	if req.Product.SKU != "PAP-CE2E" || !req.Published || req.CatalogRevision != 1 || req.PolicyRevision != 1 {
		t.Fatalf("request: %+v", req.Product)
	}
	if len(req.Product.Prices) != 2 || req.Product.Prices[0].AmountMinor != 65000 {
		t.Fatalf("prices: %+v", req.Product.Prices)
	}
	if len(req.Product.Tags) != 1 || req.Product.Tags[0].Slug != "gold" {
		t.Fatalf("tags: %+v", req.Product.Tags)
	}
	if len(req.Product.Subcategories) != 1 || req.Product.TopCategory.ID == "" {
		t.Fatalf("categories: %+v", req.Product)
	}
	if req.Product.WidthCM == nil || *req.Product.WidthCM != 70 {
		t.Fatalf("dimensions: %+v", req.Product)
	}
	inventories := provider.Inventories()
	if len(inventories) != 1 || inventories[0].AvailableQuantity != 10 || !inventories[0].Ready {
		t.Fatalf("availability: %+v", inventories)
	}
	if inventories[0].InventoryRevision != 1 || inventories[0].ExternalProductID != result.ExternalProductID {
		t.Fatalf("inventory linkage: %+v", inventories[0])
	}

	// Idempotent repeat: same keys, one remote product, mapping reused.
	second, err := service.SyncProduct(ctx, "primary", productID)
	if err != nil {
		t.Fatal(err)
	}
	if second.ExternalProductID != result.ExternalProductID ||
		second.ProductOperationKey != result.ProductOperationKey {
		t.Fatalf("stable: %+v vs %+v", result, second)
	}
	if provider.Creations() != 1 {
		t.Fatalf("one remote product: %d", provider.Creations())
	}
	stored, err := store.GetProductMapping(ctx, "primary", productID)
	if err != nil || stored.ExternalProductID != result.ExternalProductID {
		t.Fatalf("mapping: %+v %v", stored, err)
	}
}

// TestCommerceAvailabilityFidelity drives stock 10 with a cap of 3
// through real Phase 5C reads: the provider request must carry exactly 3
// with no duplicated formula.
func TestCommerceAvailabilityFidelity(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	root, sub, tag := catalogProductFixture(t, env, 0xF0, "cav")
	productID := commerceProductID(0xF0)
	pe := commerceIDs(t, 0xF0, "product")["product"]
	ingestCatalog(t, env, pe, catalog.EventProductSnapshotV1, "2026-09-20T12:00:00Z",
		productPayload(productID, "PAP-CAV", "x", root, []string{sub}, []string{tag}, 1))
	projectCatalogOnce(t, env, pe)
	oe := commerceIDs(t, 0x1F0, "policy")["policy"]
	three := 3
	ingestCatalog(t, env, oe, catalog.EventProductSalesPolicySnapshotV1, "2026-09-20T12:01:00Z",
		policyPayload(productID, 1, true, true, &three))
	projectCatalogOnce(t, env, oe)
	ie := commerceIDs(t, 0x2F0, "inventory")["inventory"]
	ingestCatalog(t, env, ie, catalog.EventInventoryProductSnapshotV1, "2026-09-20T12:02:00Z",
		inventoryPayload(productID, 1, 10))
	projectCatalogOnce(t, env, ie)

	provider := commerce.NewFakeProvider("primary")
	registry := commerce.NewRegistry()
	if err := registry.Register("primary", provider); err != nil {
		t.Fatal(err)
	}
	source := commerce.NewCatalogCommerceSource(catalog.NewService(store))
	service := commerce.NewCommerceService(registry, store, source, nilLogger())
	if _, err := service.SyncProduct(ctx, "primary", productID); err != nil {
		t.Fatal(err)
	}
	inventories := provider.Inventories()
	if len(inventories) != 1 || inventories[0].AvailableQuantity != 3 {
		t.Fatalf("capped availability: %+v", inventories)
	}
}

// TestCommerceMoneyExact proves >2^53 minor units survive catalog read,
// assembler, and provider DTO exactly.
func TestCommerceMoneyExact(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	root, _, _ := catalogProductFixture(t, env, 0x1A0, "mx")
	productID := commerceProductID(0x1A0)
	payload := productPayload(productID, "PAP-BIG", "x", root, nil, nil, 1)
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatal(err)
	}
	m["prices"] = []any{map[string]any{"currency": "EGP", "price_cents": 9007199254740993}}
	raw, _ := json.Marshal(m)
	event := commerceIDs(t, 0x1A0, "product")["product"]
	ingestCatalog(t, env, event, catalog.EventProductSnapshotV1, "2026-09-20T12:00:00Z", string(raw))
	projectCatalogOnce(t, env, event)
	oe := commerceIDs(t, 0x2A0, "policy")["policy"]
	ingestCatalog(t, env, oe, catalog.EventProductSalesPolicySnapshotV1, "2026-09-20T12:01:00Z",
		policyPayload(productID, 1, true, true, nil))
	projectCatalogOnce(t, env, oe)
	ie := commerceIDs(t, 0x3A0, "inventory")["inventory"]
	ingestCatalog(t, env, ie, catalog.EventInventoryProductSnapshotV1, "2026-09-20T12:02:00Z",
		inventoryPayload(productID, 1, 5))
	projectCatalogOnce(t, env, ie)

	provider := commerce.NewFakeProvider("primary")
	registry := commerce.NewRegistry()
	if err := registry.Register("primary", provider); err != nil {
		t.Fatal(err)
	}
	source := commerce.NewCatalogCommerceSource(catalog.NewService(store))
	service := commerce.NewCommerceService(registry, store, source, nilLogger())
	if _, err := service.SyncProduct(ctx, "primary", productID); err != nil {
		t.Fatal(err)
	}
	upserts := provider.Upserts()
	if len(upserts) != 1 || len(upserts[0].Product.Prices) != 1 ||
		upserts[0].Product.Prices[0].AmountMinor != 9007199254740993 {
		t.Fatalf("exact money: %+v", upserts)
	}
}

// TestCommerceTranslationFidelity proves Arabic and English names reach
// the provider-neutral request uncorrupted, unflattened.
func TestCommerceTranslationFidelity(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	root, _, _ := catalogProductFixture(t, env, 0x1B0, "trx")
	productID := commerceProductID(0x1B0)
	payload := productPayload(productID, "PAP-TRX", "بردية", root, nil, nil, 1)
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatal(err)
	}
	m["translations"] = []any{
		map[string]any{"locale": "ar", "name": "بردية توت", "description": "وصف"},
		map[string]any{"locale": "en", "name": "Tutankhamun Papyrus"},
	}
	raw, _ := json.Marshal(m)
	event := commerceIDs(t, 0x1B0, "product")["product"]
	ingestCatalog(t, env, event, catalog.EventProductSnapshotV1, "2026-09-20T12:00:00Z", string(raw))
	projectCatalogOnce(t, env, event)
	oe := commerceIDs(t, 0x2B0, "policy")["policy"]
	ingestCatalog(t, env, oe, catalog.EventProductSalesPolicySnapshotV1, "2026-09-20T12:01:00Z",
		policyPayload(productID, 1, true, true, nil))
	projectCatalogOnce(t, env, oe)
	ie := commerceIDs(t, 0x3B0, "inventory")["inventory"]
	ingestCatalog(t, env, ie, catalog.EventInventoryProductSnapshotV1, "2026-09-20T12:02:00Z",
		inventoryPayload(productID, 1, 5))
	projectCatalogOnce(t, env, ie)

	provider := commerce.NewFakeProvider("primary")
	registry := commerce.NewRegistry()
	if err := registry.Register("primary", provider); err != nil {
		t.Fatal(err)
	}
	source := commerce.NewCatalogCommerceSource(catalog.NewService(store))
	service := commerce.NewCommerceService(registry, store, source, nilLogger())
	if _, err := service.SyncProduct(ctx, "primary", productID); err != nil {
		t.Fatal(err)
	}
	upserts := provider.Upserts()
	if len(upserts) != 1 || len(upserts[0].Product.Names) != 2 {
		t.Fatalf("names: %+v", upserts)
	}
	names := map[string]string{}
	for _, name := range upserts[0].Product.Names {
		names[name.Locale] = name.Name
	}
	if names["ar"] != "بردية توت" || names["en"] != "Tutankhamun Papyrus" {
		t.Fatalf("translations: %+v", names)
	}
	if upserts[0].Product.Descriptions["ar"] != "وصف" {
		t.Fatalf("descriptions: %+v", upserts[0].Product.Descriptions)
	}
}

// TestCommerceMappingSurvivesCatalogRebuild is the Phase 6A freeze gate:
// create a mapping, reset/rebuild current catalog projection state, and
// prove the mapping still points at the same external ID with the
// product readable and SyncProduct reusing it.
func TestCommerceMappingSurvivesCatalogRebuild(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	productID := commerceFixture(t, env, 0x1C0, "crb", "PAP-CRB", 10)

	provider := commerce.NewFakeProvider("primary")
	registry := commerce.NewRegistry()
	if err := registry.Register("primary", provider); err != nil {
		t.Fatal(err)
	}
	source := commerce.NewCatalogCommerceSource(catalog.NewService(store))
	service := commerce.NewCommerceService(registry, store, source, nilLogger())
	first, err := service.SyncProduct(ctx, "primary", productID)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Reset all derived catalog/policy/inventory projection state using
	// the documented rebuild path; inbox stays authoritative.
	for _, table := range []string{
		"catalog_product_inventory",
		"catalog_product_sales_policies",
		"catalog_product_tags", "catalog_product_subcategories", "catalog_product_translations",
		"catalog_product_prices", "catalog_products", "catalog_category_edges",
		"catalog_tags", "catalog_categories",
	} {
		if _, err := env.pool.Exec(ctx, `DELETE FROM `+table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := env.pool.Exec(ctx,
		`UPDATE sync_event_processing SET status='pending', next_attempt_at=NULL,
		 attempt_count=0, processed_at=NULL, last_error_code=NULL, last_error_message=NULL
		 WHERE processor LIKE 'catalog_%' OR processor = 'inventory_product_projection.v1'`); err != nil {
		t.Fatal(err)
	}

	// Mapping table untouched by the rebuild.
	stored, err := store.GetProductMapping(ctx, "primary", productID)
	if err != nil || stored.ExternalProductID != first.ExternalProductID {
		t.Fatalf("mapping survives reset: %+v %v", stored, err)
	}

	// During the gap the product is unreadable but the mapping remains
	// and SyncProduct reports not-ready without deleting anything.
	if _, err := service.SyncProduct(ctx, "primary", productID); err == nil {
		t.Fatal("product not ready during rebuild gap")
	}
	if _, err := store.GetProductMapping(ctx, "primary", productID); err != nil {
		t.Fatalf("mapping retained during gap: %v", err)
	}

	// Replay in dependency order (the documented rebuild sequence):
	// categories, tag, product, policy, inventory. Ordered replay is
	// deterministic under test; concurrent drain converges via the same
	// backoff machinery production uses.
	rows, err := env.pool.Query(ctx,
		`SELECT event_id::text, event_type FROM sync_events
		 WHERE event_type LIKE 'catalog.%' OR event_type LIKE 'inventory.%'`)
	if err != nil {
		t.Fatal(err)
	}
	type queued struct{ id, typ string }
	var order []queued
	for rows.Next() {
		var entry queued
		if err := rows.Scan(&entry.id, &entry.typ); err != nil {
			t.Fatal(err)
		}
		order = append(order, entry)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rank := map[string]int{
		catalog.EventCategorySnapshotV1: 0, catalog.EventTagSnapshotV1: 1,
		catalog.EventProductSnapshotV1: 2, catalog.EventProductSalesPolicySnapshotV1: 3,
		catalog.EventInventoryProductSnapshotV1: 4,
	}
	sort.SliceStable(order, func(i, j int) bool { return rank[order[i].typ] < rank[order[j].typ] })
	for _, entry := range order {
		rec, ok, err := store.LoadCatalogEvent(ctx, entry.id)
		if err != nil || !ok {
			t.Fatal("load")
		}
		var res catalog.ProjectResult
		switch entry.typ {
		case catalog.EventCategorySnapshotV1:
			res, err = store.ProjectCategory(ctx, rec, time.Now())
		case catalog.EventTagSnapshotV1:
			res, err = store.ProjectTag(ctx, rec, time.Now())
		case catalog.EventProductSalesPolicySnapshotV1:
			res, err = store.ProjectProductSalesPolicy(ctx, rec, time.Now())
		case catalog.EventInventoryProductSnapshotV1:
			res, err = store.ProjectProductInventory(ctx, rec, time.Now())
		default:
			res, err = store.ProjectProduct(ctx, rec, time.Now())
		}
		if err != nil {
			t.Fatalf("replay %s: %v", entry.id, err)
		}
		if res.Outcome != catalog.OutcomeProcessed {
			t.Fatalf("replay %s: %+v", entry.id, res)
		}
	}
	second, err := service.SyncProduct(ctx, "primary", productID)
	if err != nil {
		t.Fatalf("sync after rebuild: %v", err)
	}
	if second.ExternalProductID != first.ExternalProductID {
		t.Fatalf("same external id: %+v vs %+v", first, second)
	}
	if second.MappingCreated {
		t.Fatal("no duplicate mapping after rebuild")
	}
	if provider.Creations() != 1 {
		t.Fatalf("one remote product across rebuild: %d", provider.Creations())
	}
}

// TestCommerceMappingConcurrentGate runs mapping creation raced across
// pool connections to prove exactly-once durability under load.
func TestCommerceMappingConcurrentGate(t *testing.T) {
	env := openSaleEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := catalogStore(env)
	productID := commerceProductID(0x1D0)

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.CreateProductMapping(ctx, "primary", productID, "ext-gate")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("gate: %v", err)
		}
	}
	var count int
	if err := env.pool.QueryRow(ctx,
		`SELECT count(*) FROM commerce_product_mappings WHERE provider_key='primary' AND product_id=$1`,
		productID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("one row: %d (%v)", count, err)
	}
}
