package postgres

import (
	"context"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"sync"
	"testing"
)

func TestPhase17R1VariantMappingTransitionCAS(t *testing.T) {
	env := openSaleEnv(t)
	store := catalogStore(env)
	ctx := context.Background()
	_, product := seedCatalogProduct(t, env, 730, "ML-TRANSITION")
	variant := catalogIDs(t, 750, "v")["v"]
	// Prove Store ownership; legacy unowned mappings may never transition.
	scope := catalogIDs(t, 751, "s")["s"]
	if _, err := env.pool.Exec(ctx, `INSERT INTO stores(id,display_name,timezone) VALUES($1,'transition','Africa/Cairo') ON CONFLICT DO NOTHING`, scope); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx, `UPDATE catalog_products SET store_id=$1 WHERE product_id=$2`, scope, product); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProductVariantMapping(ctx, "website", product, variant, "500", "500"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, child := range []string{"77", "78"} {
		wg.Add(1)
		go func(child string) {
			defer wg.Done()
			_, err := store.UpdateProductVariantMappingExternal(ctx, "website", product, variant, "500", "500", child)
			results <- err
		}(child)
	}
	wg.Wait()
	close(results)
	wins := 0
	conflicts := 0
	for err := range results {
		if err == nil {
			wins++
		} else if commerce.IsMappingConflict(err) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins %d conflicts %d", wins, conflicts)
	}
	row, err := store.GetProductVariantMapping(ctx, "website", variant)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.UpdateProductVariantMappingExternal(ctx, "website", product, variant, "500", "500", row.ExternalVariantID); err != nil {
		t.Fatalf("same transition replay: %v", err)
	}
	if _, err = store.UpdateProductVariantMappingExternal(ctx, "website", product, variant, "500", row.ExternalVariantID, "99"); !commerce.IsMappingConflict(err) {
		t.Fatalf("arbitrary child remap accepted: %v", err)
	}
}
