package catalog

import (
	"encoding/json"
	"testing"
)

func TestFingerprintIgnoresObjectKeyOrder(t *testing.T) {
	a := json.RawMessage(`{"sku":"A","name_ar":"X","product_id":"11111111-1111-4111-8111-111111111111","top_category_id":"22222222-2222-4222-8222-222222222222","catalog_revision":1}`)
	b := json.RawMessage(`{"name_ar":"X","catalog_revision":1,"top_category_id":"22222222-2222-4222-8222-222222222222","product_id":"11111111-1111-4111-8111-111111111111","sku":"A"}`)
	pa, err := DecodeProductSnapshotV2(a)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := DecodeProductSnapshotV2(b)
	if err != nil {
		t.Fatal(err)
	}
	va, err := ValidateProductSnapshotV2(pa)
	if err != nil {
		t.Fatal(err)
	}
	vb, err := ValidateProductSnapshotV2(pb)
	if err != nil {
		t.Fatal(err)
	}
	if FingerprintProduct(va) != FingerprintProduct(vb) {
		t.Fatal("object key order must not affect identity")
	}
}

func TestFingerprintTagOrderIrrelevantSubOrderRelevant(t *testing.T) {
	base := ProductSnapshot{
		ProductID: "11111111-1111-4111-8111-111111111111", SKU: "A", NameAR: "X",
		TopCategoryID:   "22222222-2222-4222-8222-222222222222",
		SubcategoryIDs:  []string{"33333333-3333-4333-8333-333333333333"},
		TagIDs:          []string{"44444444-4444-4333-8444-444444444444", "55555555-5555-4555-8555-555555555555"},
		IsActive:        true,
		CatalogRevision: 1,
	}
	va, err := ValidateProductSnapshotV2(base)
	if err != nil {
		t.Fatal(err)
	}
	reversed := base
	reversed.TagIDs = []string{"55555555-5555-4555-8555-555555555555", "44444444-4444-4333-8444-444444444444"}
	vb, err := ValidateProductSnapshotV2(reversed)
	if err != nil {
		t.Fatal(err)
	}
	if FingerprintProduct(va) != FingerprintProduct(vb) {
		t.Fatal("tag order (set semantics) must not affect identity")
	}
	moved := base
	moved.SubcategoryIDs = []string{
		"66666666-6666-4666-8666-666666666666",
		"33333333-3333-4333-8333-333333333333",
	}
	vc, err := ValidateProductSnapshotV2(moved)
	if err != nil {
		t.Fatal(err)
	}
	if FingerprintProduct(va) == FingerprintProduct(vc) {
		t.Fatal("subcategory order (position semantic) must affect identity")
	}
	renamed := base
	renamed.NameAR = "Y"
	vd, err := ValidateProductSnapshotV2(renamed)
	if err != nil {
		t.Fatal(err)
	}
	if FingerprintProduct(va) == FingerprintProduct(vd) {
		t.Fatal("changed name must affect identity")
	}
	inactive := base
	inactive.IsActive = false
	ve, err := ValidateProductSnapshotV2(inactive)
	if err != nil {
		t.Fatal(err)
	}
	if FingerprintProduct(va) == FingerprintProduct(ve) {
		t.Fatal("changed lifecycle must affect identity")
	}
}

func TestFingerprintTranslationPriceOrder(t *testing.T) {
	cost := int64(1)
	base := ProductSnapshot{
		ProductID: "11111111-1111-4111-8111-111111111111", SKU: "A", NameAR: "X",
		NameEN: new("y"),
		Prices: []CatalogPrice{
			{Currency: "EGP", PriceCents: 1, CostCents: &cost},
			{Currency: "USD", PriceCents: 2},
		},
		TopCategoryID:   "22222222-2222-4222-8222-222222222222",
		CatalogRevision: 1,
	}
	va, err := ValidateProductSnapshotV2(base)
	if err != nil {
		t.Fatal(err)
	}
	reordered := base
	reordered.Prices = []CatalogPrice{
		{Currency: "USD", PriceCents: 2},
		{Currency: "EGP", PriceCents: 1, CostCents: &cost},
	}
	vb, err := ValidateProductSnapshotV2(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if FingerprintProduct(va) != FingerprintProduct(vb) {
		t.Fatal("currency-keyed order must not affect identity")
	}
}

func TestFingerprintCategoryParentOrderPreserved(t *testing.T) {
	mk := func(parents []string) CategorySnapshot {
		v, err := ValidateCategorySnapshot(CategorySnapshot{
			CategoryID: "11111111-1111-4111-8111-111111111111", Status: CategoryActive,
			NameAR:          "x",
			ParentIDs:       parents,
			CatalogRevision: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	a := mk([]string{"22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"})
	b := mk([]string{"33333333-3333-4333-8333-333333333333", "22222222-2222-4222-8222-222222222222"})
	if FingerprintCategory(a) == FingerprintCategory(b) {
		t.Fatal("parent order (position semantic) must affect identity")
	}
	if FingerprintCategory(a) != FingerprintCategory(a) {
		t.Fatal("identical state must fingerprint identically")
	}
}
