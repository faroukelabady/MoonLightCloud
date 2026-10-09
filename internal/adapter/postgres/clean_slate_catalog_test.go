package postgres

import (
	"context"
	"testing"
)

// Phase 18 R1 (Retail ADR-042): a brand-new shop legitimately has zero
// ProductTypes, Categories and Products. Cloud projections must tolerate
// that state and Catalog Health must not report it as a defect.
func TestCatalogHealthTreatsEmptyCleanShopAsHealthy(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	for _, table := range []string{"catalog_product_types", "catalog_categories", "catalog_tags", "catalog_products", "catalog_product_variants"} {
		var n int
		if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
		if n != 0 {
			t.Fatalf("fresh Cloud %s = %d, want 0 (no seeded merchant catalog)", table, n)
		}
	}
	store := catalogStore(env)
	counts, err := store.CatalogHealthSummaryRows(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range counts {
		if c.Products != 0 {
			t.Fatalf("empty shop reported health finding %s=%d", c.ReasonCode, c.Products)
		}
	}
	detail, err := store.CatalogHealthDetailRows(ctx, "", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail) != 0 {
		t.Fatalf("empty shop health detail = %v, want none", detail)
	}
}
