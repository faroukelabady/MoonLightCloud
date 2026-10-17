package postgres

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
)

type productStockFixture struct {
	env       *saleEnv
	productID string
	nextEvent int
}

func seedProductStockFixture(t *testing.T, env *saleEnv) *productStockFixture {
	t.Helper()
	_, productID := seedCatalogProduct(t, env, 9000, "STOCK-PRODUCT")
	f := &productStockFixture{env: env, productID: productID, nextEvent: 9100}
	if res := f.project(t, catalog.EventProductSalesPolicySnapshotV1,
		policyPayload(productID, 1, true, true, nil)); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("sales policy: %+v", res)
	}
	return f
}

func (f *productStockFixture) project(t *testing.T, eventType, payload string) catalog.ProjectResult {
	t.Helper()
	f.nextEvent++
	eventID := variantEventID(f.nextEvent)
	ingestCatalog(t, f.env, eventID, eventType, "2026-09-20T12:00:00Z", payload)
	return projectCatalogOnce(t, f.env, eventID)
}

func (f *productStockFixture) variant(t *testing.T, index int, active, deleted bool) string {
	t.Helper()
	variantID := catalogIDs(t, 9200+index, "variant")["variant"]
	res := f.project(t, catalog.EventProductVariantSnapshotV1,
		variantPayload(variantID, f.productID, variantID, active, deleted, variantID, nil, 1))
	if res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("variant: %+v", res)
	}
	return variantID
}

func (f *productStockFixture) inventory(t *testing.T, variantID string, stock int, revision int64, ready bool) {
	t.Helper()
	payload := variantInventoryPayload(variantID, f.productID, variantID, stock, revision, true, nil)
	if !ready {
		var value map[string]any
		if err := json.Unmarshal([]byte(payload), &value); err != nil {
			t.Fatal(err)
		}
		value["ready"] = false
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		payload = string(raw)
	}
	if res := f.project(t, catalog.EventInventoryProductVariantSnapshotV1, payload); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("inventory: %+v", res)
	}
}

func (f *productStockFixture) assertAvailability(t *testing.T, stock int, ready bool) {
	t.Helper()
	got, err := catalog.NewService(catalogStore(f.env)).GetProductAvailability(context.Background(), f.productID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ready != ready || got.MissingInventory == ready || got.MissingPolicy {
		t.Fatalf("availability readiness: %+v, want ready=%t", got, ready)
	}
	if ready {
		if got.StockQuantity == nil || *got.StockQuantity != stock || got.OnlineAvailable != stock {
			t.Fatalf("availability: %+v, want stock=%d", got, stock)
		}
	} else if got.OnlineAvailable != 0 || got.StockQuantity != nil {
		t.Fatalf("incomplete inventory must expose no aggregate stock: %+v", got)
	}
}

func TestCatalogProductStockIndependentVariantRevisions(t *testing.T) {
	f := seedProductStockFixture(t, openSaleEnv(t))
	first := f.variant(t, 1, true, false)
	second := f.variant(t, 2, true, false)
	f.inventory(t, first, 4, 100, true)
	f.inventory(t, second, 7, 1, true)
	f.assertAvailability(t, 11, true)

	// The product total must change even though MAX(inventory_revision)
	// stays 100: each variant owns its independent revision sequence.
	f.inventory(t, second, 9, 2, true)
	f.assertAvailability(t, 13, true)
	var highest int64
	if err := f.env.pool.QueryRow(context.Background(),
		`SELECT max(source_revision) FROM catalog_product_variant_inventory WHERE product_id=$1`,
		f.productID).Scan(&highest); err != nil || highest != 100 {
		t.Fatalf("highest variant revision=%d, err=%v; want 100", highest, err)
	}
}

func TestCatalogProductStockUnreadyInventoryRetainsVariantFence(t *testing.T) {
	f := seedProductStockFixture(t, openSaleEnv(t))
	variant := f.variant(t, 1, true, false)
	f.inventory(t, variant, 4, 1, true)
	f.inventory(t, variant, 4, 2, false)
	got, err := catalog.NewService(catalogStore(f.env)).GetVariantAvailability(context.Background(), variant)
	if err != nil {
		t.Fatal(err)
	}
	// Safe-zero must carry revision 2 so a provider with revision 1 accepts
	// it. Hiding the row would send revision 0 and fail its stale fence.
	if got.Ready || !got.MissingInventory || got.OnlineAvailable != 0 || got.InventoryRevision != 2 || got.InventoryEventID == "" {
		t.Fatalf("unready inventory lost its authoritative fence: %+v", got)
	}
	f.assertAvailability(t, 0, false)
}

func TestCatalogProductStockRequiresCompleteReadyInventory(t *testing.T) {
	f := seedProductStockFixture(t, openSaleEnv(t))
	f.assertAvailability(t, 0, false) // No active variants is unknown stock.
	first := f.variant(t, 1, true, false)
	f.assertAvailability(t, 0, false)
	f.inventory(t, first, 0, 1, true)
	f.assertAvailability(t, 0, true) // A complete zero is known stock.

	second := f.variant(t, 2, true, false)
	f.assertAvailability(t, 0, false) // A partial total is never available.
	f.inventory(t, second, 8, 1, false)
	f.assertAvailability(t, 0, false)
	f.inventory(t, second, 8, 2, true)
	f.assertAvailability(t, 8, true)
}

func TestCatalogProductStockExcludesInactiveAndDeletedVariants(t *testing.T) {
	f := seedProductStockFixture(t, openSaleEnv(t))
	live := f.variant(t, 1, true, false)
	inactive := f.variant(t, 2, false, false)
	deleted := f.variant(t, 3, true, true)
	// Excluded variants need no inventory for the live total to be ready.
	f.inventory(t, live, 3, 1, true)
	f.assertAvailability(t, 3, true)
	f.inventory(t, inactive, 100, 1, true)
	f.inventory(t, deleted, 200, 1, true)
	f.assertAvailability(t, 3, true)

	if res := f.project(t, catalog.EventProductVariantSnapshotV1,
		variantPayload(live, f.productID, live, false, false, live, nil, 2)); res.Outcome != catalog.OutcomeProcessed {
		t.Fatalf("deactivate last live variant: %+v", res)
	}
	f.assertAvailability(t, 0, false)
}

func TestCatalogProductStockFencesCorruptedRelationships(t *testing.T) {
	for _, corruption := range []string{"inventory product", "inventory store", "inventory sku", "variant store"} {
		t.Run(corruption, func(t *testing.T) {
			scoped := openScopeFixture(t)
			env := &saleEnv{pool: scoped.pool, syncSvc: scoped.syncSvc, devID: scoped.devA, credID: scoped.credA}
			f := seedProductStockFixture(t, env)
			variantID := f.variant(t, 1, true, false)
			f.inventory(t, variantID, 12, 1, true)
			f.assertAvailability(t, 12, true)

			// Projections normally prevent these relationships. Simulate
			// durable drift to prove the aggregate read also fails closed.
			var err error
			switch corruption {
			case "inventory product":
				_, otherProduct := seedCatalogProduct(t, env, 9400, "OTHER-PRODUCT")
				_, err = env.pool.Exec(context.Background(),
					`UPDATE catalog_product_variant_inventory SET product_id=$2 WHERE variant_id=$1`, variantID, otherProduct)
			case "inventory store":
				_, err = env.pool.Exec(context.Background(),
					`UPDATE catalog_product_variant_inventory SET store_id=$2 WHERE variant_id=$1`, variantID, scopeStoreB)
			case "inventory sku":
				_, err = env.pool.Exec(context.Background(),
					`UPDATE catalog_product_variant_inventory SET sku='FOREIGN-SKU' WHERE variant_id=$1`, variantID)
			case "variant store":
				_, err = env.pool.Exec(context.Background(),
					`UPDATE catalog_product_variants SET store_id=$2 WHERE variant_id=$1`, variantID, scopeStoreB)
			}
			if err != nil {
				t.Fatal(err)
			}
			f.assertAvailability(t, 0, false)
		})
	}
}

func TestCatalogProductStockRejectsContradictoryInventoryPayload(t *testing.T) {
	for _, contradiction := range []string{"product", "sku"} {
		t.Run(contradiction, func(t *testing.T) {
			f := seedProductStockFixture(t, openSaleEnv(t))
			variantID := f.variant(t, 1, true, false)
			f.inventory(t, variantID, 4, 1, true)
			productID, sku := f.productID, variantID
			if contradiction == "product" {
				_, productID = seedCatalogProduct(t, f.env, 9400, "OTHER-PRODUCT")
			} else {
				sku = "CONTRADICTORY-SKU"
			}
			res := f.project(t, catalog.EventInventoryProductVariantSnapshotV1,
				variantInventoryPayload(variantID, productID, sku, 99, 2, true, nil))
			if res.Outcome != catalog.OutcomeBlocked || res.ErrorCode != ErrValidation {
				t.Fatalf("contradictory %s: %+v, want blocked/%s", contradiction, res, ErrValidation)
			}
			f.assertAvailability(t, 4, true)
			var revision int64
			if err := f.env.pool.QueryRow(context.Background(),
				`SELECT source_revision FROM catalog_product_variant_inventory WHERE variant_id=$1`,
				variantID).Scan(&revision); err != nil || revision != 1 {
				t.Fatalf("rejected inventory changed revision=%d, err=%v; want 1", revision, err)
			}
		})
	}
}
