package postgres

// Phase 9-R1 F01 regression: the normalized Product dashboard SQL must
// execute on real PostgreSQL and preserve row identity and Store badges.
// Same-SKU Products in different Stores stay distinct rows; a badge is
// only emitted for a coherent single proven Store; scoped reads exclude
// foreign and NULL ownership; exact money is preserved beyond 2^53.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
)

// saleWithProduct rewrites the fixture sale's id and first line identity.
func saleWithProduct(t *testing.T, saleID, productID, sku, name string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(fixture(t, "sale_egp.json")), &m); err != nil {
		t.Fatal(err)
	}
	m["sale_id"] = saleID
	line := m["lines"].([]any)[0].(map[string]any)
	line["product_id"], line["sku"], line["product_name"] = productID, sku, name
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func normalizedWindow() (time.Time, time.Time) {
	start, _ := time.Parse(time.RFC3339, "2026-01-01T00:00:00Z")
	end, _ := time.Parse(time.RFC3339, "2027-01-01T00:00:00Z")
	return start, end
}

func storeOfProduct(rows []dashboard.NormalizedProductRowRaw, id string) (*string, bool) {
	for _, r := range rows {
		if r.ProductID != nil && *r.ProductID == id {
			return r.StoreID, true
		}
	}
	return nil, false
}

func TestDashboardNormalizedScopeIdentity(t *testing.T) {
	f := openScopeFixture(t)
	saleA := "aaaaaaaa-0000-4000-8000-0000000000a1"
	saleB := "bbbbbbbb-0000-4000-8000-0000000000b1"
	saleL := "cccccccc-0000-4000-8000-0000000000c1"
	prodA := "aaaaaaaa-1111-4111-8111-0000000000a1"
	prodB := "bbbbbbbb-1111-4111-8111-0000000000b1"
	prodL := "cccccccc-1111-4111-8111-0000000000c1"

	f.ingest(t, f.devA, f.credA, "e0000700-0000-4000-8000-000000000001", "sale.finalized.v1", saleWithProduct(t, saleA, prodA, "NR1", "NR A"))
	f.ingest(t, f.devB, f.credB, "e0000700-0000-4000-8000-000000000002", "sale.finalized.v1", saleWithProduct(t, saleB, prodB, "NR1", "NR B"))
	f.ingest(t, f.devC, f.credC, "e0000700-0000-4000-8000-000000000003", "sale.finalized.v1", saleWithProduct(t, saleL, prodL, "NRLEG", "NR Leg"))
	for _, event := range []string{
		"e0000700-0000-4000-8000-000000000001", "e0000700-0000-4000-8000-000000000002", "e0000700-0000-4000-8000-000000000003",
	} {
		if res := f.projectSale(t, event); res.Outcome != 1 {
			t.Fatalf("sale %s: %+v", event, res)
		}
	}

	devices := NewDevices(f.pool, 5*time.Second)
	ctx := context.Background()
	start, end := normalizedWindow()

	global, err := devices.DashboardProductsNormalized(ctx, start, end)
	if err != nil {
		t.Fatalf("global products (F01 42883 regression): %v", err)
	}
	if len(global) != 3 {
		t.Fatalf("global rows: %+v", global)
	}
	for _, tc := range []struct {
		id    string
		store *string
	}{
		{prodA, scopePtr(scopeStoreA)}, {prodB, scopePtr(scopeStoreB)}, {prodL, nil},
	} {
		got, ok := storeOfProduct(global, tc.id)
		if !ok {
			t.Fatalf("global missing %s: %+v", tc.id, global)
		}
		if tc.store == nil {
			if got != nil {
				t.Fatalf("legacy product badge must be null: %v", *got)
			}
		} else if got == nil || *got != *tc.store {
			t.Fatalf("%s badge: %v want %s", tc.id, got, *tc.store)
		}
	}

	scopedA, err := devices.DashboardProductsNormalizedForStore(ctx, scopeStoreA, start, end)
	if err != nil {
		t.Fatalf("scoped A products: %v", err)
	}
	if len(scopedA) != 1 || scopedA[0].ProductID == nil || *scopedA[0].ProductID != prodA || scopedA[0].StoreID == nil || *scopedA[0].StoreID != scopeStoreA {
		t.Fatalf("scoped A: %+v", scopedA)
	}
	scopedB, err := devices.DashboardProductsNormalizedForStore(ctx, scopeStoreB, start, end)
	if err != nil {
		t.Fatalf("scoped B products: %v", err)
	}
	if len(scopedB) != 1 || scopedB[0].ProductID == nil || *scopedB[0].ProductID != prodB || scopedB[0].StoreID == nil || *scopedB[0].StoreID != scopeStoreB {
		t.Fatalf("scoped B: %+v", scopedB)
	}

	// Same product ID with a legacy sale and a Store A sale: the global
	// group is mixed ownership and must not claim a Store badge.
	prodM := "dddddddd-1111-4111-8111-0000000000d1"
	f.ingest(t, f.devC, f.credC, "e0000700-0000-4000-8000-000000000004", "sale.finalized.v1", saleWithProduct(t, "cccccccc-0000-4000-8000-0000000000d2", prodM, "NRMIX", "NR Mix"))
	f.ingest(t, f.devA, f.credA, "e0000700-0000-4000-8000-000000000005", "sale.finalized.v1", saleWithProduct(t, "aaaaaaaa-0000-4000-8000-0000000000d3", prodM, "NRMIX", "NR Mix"))
	for _, event := range []string{"e0000700-0000-4000-8000-000000000004", "e0000700-0000-4000-8000-000000000005"} {
		if res := f.projectSale(t, event); res.Outcome != 1 {
			t.Fatalf("mixed sale %s: %+v", event, res)
		}
	}
	global, err = devices.DashboardProductsNormalized(ctx, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := storeOfProduct(global, prodM); !ok || got != nil {
		t.Fatalf("mixed-ownership badge must be null: %v (present=%v)", got, ok)
	}
	scopedMix, err := devices.DashboardProductsNormalizedForStore(ctx, scopeStoreA, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := storeOfProduct(scopedMix, prodM); !ok || got == nil || *got != scopeStoreA {
		t.Fatalf("scoped A mixed-ownership badge: %v (present=%v)", got, ok)
	}
}

func TestDashboardNormalizedRefundsScopeIdentity(t *testing.T) {
	f := openScopeFixture(t)
	saleA := "aaaaaaaa-0000-4000-8000-0000000000e1"
	prodA := "aaaaaaaa-2222-4222-8222-0000000000a2"
	payload := saleWithProduct(t, saleA, prodA, "NRREF", "NR Ref")
	f.ingest(t, f.devA, f.credA, "e0000710-0000-4000-8000-000000000001", "sale.finalized.v1", payload)
	if res := f.projectSale(t, "e0000710-0000-4000-8000-000000000001"); res.Outcome != 1 {
		t.Fatalf("sale: %+v", res)
	}
	f.ingest(t, f.devA, f.credA, "e0000710-0000-4000-8000-000000000002",
		"sale.return_refund.finalized.v1", dashReturnPayload(t, payload, "aaaaaaaa-3333-4333-8333-0000000000a2", "RET-R1", "2026-09-20T13:00:00Z", 1))
	if res := f.projectReturn(t, "e0000710-0000-4000-8000-000000000002"); res.Outcome != 1 {
		t.Fatalf("return: %+v", res)
	}

	devices := NewDevices(f.pool, 5*time.Second)
	ctx := context.Background()
	start, end := normalizedWindow()
	global, err := devices.DashboardProductsNormalizedRefunds(ctx, start, end)
	if err != nil {
		t.Fatalf("global refunds (F01 42883 regression): %v", err)
	}
	if got, ok := storeOfProductRaw(global, prodA); !ok || got == nil || *got != scopeStoreA {
		t.Fatalf("global refund badge: %v (present=%v)", got, ok)
	}
	scopedB, err := devices.DashboardProductsNormalizedRefundsForStore(ctx, scopeStoreB, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(scopedB) != 0 {
		t.Fatalf("scoped B refunds must exclude A: %+v", scopedB)
	}
	scopedA, err := devices.DashboardProductsNormalizedRefundsForStore(ctx, scopeStoreA, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := storeOfProductRaw(scopedA, prodA); !ok || got == nil || *got != scopeStoreA {
		t.Fatalf("scoped A refund badge: %v (present=%v)", got, ok)
	}
}

func storeOfProductRaw(rows []dashboard.NormalizedRefundProductRowRaw, id string) (*string, bool) {
	for _, r := range rows {
		if r.ProductID != nil && *r.ProductID == id {
			return r.StoreID, true
		}
	}
	return nil, false
}

func TestDashboardNormalizedExactMoneyAbove53(t *testing.T) {
	f := openScopeFixture(t)
	const big = int64(9007199254740993)
	f.ingest(t, f.devA, f.credA, "e0000720-0000-4000-8000-000000000001", "sale.finalized.v1", bigSalePayload(t, "aaaaaaaa-0000-4000-8000-0000000000f1"))
	if res := f.projectSale(t, "e0000720-0000-4000-8000-000000000001"); res.Outcome != 1 {
		t.Fatalf("big sale: %+v", res)
	}
	devices := NewDevices(f.pool, 5*time.Second)
	start, end := normalizedWindow()
	rows, err := devices.DashboardProductsNormalizedForStore(context.Background(), scopeStoreA, start, end)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Normalized != big {
		t.Fatalf("exact normalized value: %+v want %d", rows, big)
	}
}

func scopePtr(s string) *string { return &s }
