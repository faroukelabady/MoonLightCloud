package catalog

import (
	"testing"

	"github.com/google/uuid"
)

// Phase 17-R3 F16: derived Store-scoped storage keys (UUIDv5) are never a
// ProductType identity in Retail events; Retail v4 IDs and the fixed v4
// installation seed remain valid.
func TestF16DerivedStorageIdentityRefused(t *testing.T) {
	seed := "10000000-0000-4000-8000-000000000001"
	derived := uuid.NewSHA1(uuid.MustParse("d3f4a1b2-5c6d-4e7f-8a90-123456789abc"), []byte("aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa:product-type:"+seed)).String()
	if !IsDerivedStorageIdentity(derived) || IsDerivedStorageIdentity(seed) || IsDerivedStorageIdentity(uuid.NewString()) || IsDerivedStorageIdentity("not-a-uuid") {
		t.Fatal("derived identity classification")
	}

	typeSnapshot := func(id string) ProductTypeSnapshot {
		return ProductTypeSnapshot{ProductTypeID: id, Code: "papyrus", NameAR: "برديات", NameEN: "Papyrus", TypeRevision: 1, Dimensions: []string{}, Capabilities: []string{}}
	}
	for _, id := range []string{seed, uuid.NewString()} {
		if _, err := ValidateProductTypeSnapshot(typeSnapshot(id)); err != nil {
			t.Fatalf("valid type identity %s rejected: %v", id, err)
		}
	}
	if _, err := ValidateProductTypeSnapshot(typeSnapshot(derived)); err == nil {
		t.Fatal("derived storage key accepted as ProductType identity")
	}

	product := func(typeID string) map[string]any {
		return map[string]any{
			"product_id": "bbbbbbbb-0000-4000-8000-000000000002", "product_type_id": typeID,
			"name": "ساعة", "translations": []any{},
			"prices":          []any{map[string]any{"currency": "EGP", "price_cents": 100}},
			"top_category_id": "cccccccc-0000-4000-8000-000000000003",
			"subcategory_ids": []any{}, "tag_ids": []any{}, "is_active": true, "catalog_revision": 5,
		}
	}
	for typeID, ok := range map[string]bool{seed: true, "": true, derived: false} {
		raw, err := DecodeProductSnapshotV2(mustJSON(t, product(typeID)))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ValidateProductSnapshotV2(raw); (err == nil) != ok {
			t.Fatalf("product_type_id %q: accepted=%v want %v (%v)", typeID, err == nil, ok, err)
		}
	}
}
