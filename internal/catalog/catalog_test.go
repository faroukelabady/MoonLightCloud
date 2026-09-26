package catalog

import (
	"encoding/json"
	"testing"
)

func TestValidateCategorySnapshotRoundTrip(t *testing.T) {
	raw := json.RawMessage(`{"category_id":"11111111-1111-4111-8111-111111111111","status":"active",` +
		`"names":[{"locale":"ar","name":"إسلامي"},{"locale":"en","name":"Islamic"}],` +
		`"parent_ids":["22222222-2222-4222-8222-222222222222"],"catalog_revision":3}`)
	decoded, err := DecodeCategorySnapshot(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	valid, err := ValidateCategorySnapshot(decoded)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if valid.CatalogRevision != 3 || len(valid.ParentIDs) != 1 {
		t.Fatalf("snapshot: %+v", valid)
	}
}

func TestValidateCategorySnapshotRejects(t *testing.T) {
	base := CategorySnapshot{
		CategoryID: "11111111-1111-4111-8111-111111111111", Status: CategoryActive,
		Names:           []CatalogName{{Locale: "ar", Name: "x"}},
		ParentIDs:       []string{},
		CatalogRevision: 1,
	}
	cases := []struct {
		name   string
		mutate func(*CategorySnapshot)
	}{
		{"bad id", func(p *CategorySnapshot) { p.CategoryID = "nope" }},
		{"bad status", func(p *CategorySnapshot) { p.Status = "deleted" }},
		{"no names", func(p *CategorySnapshot) { p.Names = nil }},
		{"bad locale", func(p *CategorySnapshot) { p.Names[0].Locale = "fr" }},
		{"blank name", func(p *CategorySnapshot) { p.Names[0].Name = "  " }},
		{"self parent", func(p *CategorySnapshot) { p.ParentIDs = []string{p.CategoryID} }},
		{"bad parent", func(p *CategorySnapshot) { p.ParentIDs = []string{"nope"} }},
		{"zero revision", func(p *CategorySnapshot) { p.CatalogRevision = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.mutate(&p)
			if _, err := ValidateCategorySnapshot(p); err == nil {
				t.Fatalf("%s must fail", tc.name)
			}
		})
	}
	dup := "22222222-2222-4222-8222-222222222222"
	p := base
	p.ParentIDs = []string{dup, dup}
	if _, err := ValidateCategorySnapshot(p); err == nil {
		t.Fatal("duplicate parents must fail")
	}
}

func TestValidateTagSnapshot(t *testing.T) {
	raw := json.RawMessage(`{"tag_id":"11111111-1111-4111-8111-111111111111","slug":"gold",` +
		`"is_active":true,"names":[{"locale":"en","name":"Gold"}],"catalog_revision":1}`)
	decoded, err := DecodeTagSnapshot(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, err := ValidateTagSnapshot(decoded); err != nil {
		t.Fatalf("validate: %v", err)
	}
	for _, slug := range []string{"", "UPPER", "has space", "under_score"} {
		p := TagSnapshot{TagID: "11111111-1111-4111-8111-111111111111", Slug: slug, CatalogRevision: 1}
		if _, err := ValidateTagSnapshot(p); err == nil {
			t.Fatalf("slug %q must fail", slug)
		}
	}
}

func TestValidateProductSnapshotRoundTrip(t *testing.T) {
	cost := int64(40000)
	width, height := 70, 100
	p := ProductSnapshot{
		ProductID: "11111111-1111-4111-8111-111111111111", SKU: "PAP-001", Name: "توت عنخ آمون",
		Translations: []CatalogProductTranslation{{Locale: "ar", Name: "توت عنخ آمون"}},
		Prices: []CatalogPrice{
			{Currency: "EGP", PriceCents: 65000, CostCents: &cost},
			{Currency: "USD", PriceCents: 1300},
		},
		TopCategoryID:  "22222222-2222-4222-8222-222222222222",
		SubcategoryIDs: []string{"33333333-3333-4333-8333-333333333333"},
		TagIDs:         []string{"44444444-4444-4333-8444-444444444444"},
		WidthCM:        &width, HeightCM: &height, IsActive: true, CatalogRevision: 2,
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeProductSnapshot(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	valid, err := ValidateProductSnapshot(decoded)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if valid.CatalogRevision != 2 || len(valid.Prices) != 2 {
		t.Fatalf("snapshot: %+v", valid)
	}
	// Float money must fail at parse time, never silently convert.
	if _, err := DecodeProductSnapshot(json.RawMessage(`{"product_id":"11111111-1111-4111-8111-111111111111"}`)); err != nil {
		t.Fatalf("minimal shape decodes (validation is separate): %v", err)
	}
	floatPrice := `{"product_id":"11111111-1111-4111-8111-111111111111","sku":"X","name":"Y",` +
		`"prices":[{"currency":"EGP","price_cents":65.5}],"top_category_id":"22222222-2222-4222-8222-222222222222","catalog_revision":1}`
	if _, err := DecodeProductSnapshot(json.RawMessage(floatPrice)); err == nil {
		t.Fatal("float money must fail decode")
	}
}

func TestValidateProductSnapshotRejects(t *testing.T) {
	width, height := 70, 100
	base := ProductSnapshot{
		ProductID: "11111111-1111-4111-8111-111111111111", SKU: "PAP-001", Name: "x",
		Prices:          []CatalogPrice{{Currency: "EGP", PriceCents: 1}},
		TopCategoryID:   "22222222-2222-4222-8222-222222222222",
		SubcategoryIDs:  []string{},
		TagIDs:          []string{},
		WidthCM:         &width,
		HeightCM:        &height,
		CatalogRevision: 1,
	}
	neg := int64(-1)
	zero := 0
	cases := []struct {
		name   string
		mutate func(*ProductSnapshot)
	}{
		{"bad id", func(p *ProductSnapshot) { p.ProductID = "x" }},
		{"blank sku", func(p *ProductSnapshot) { p.SKU = "  " }},
		{"negative price", func(p *ProductSnapshot) { p.Prices[0].PriceCents = -1 }},
		{"negative cost", func(p *ProductSnapshot) { p.Prices[0].CostCents = &neg }},
		{"bad currency", func(p *ProductSnapshot) { p.Prices[0].Currency = "EUR" }},
		{"zero width", func(p *ProductSnapshot) { p.WidthCM = &zero }},
		{"bad top", func(p *ProductSnapshot) { p.TopCategoryID = "x" }},
		{"zero revision", func(p *ProductSnapshot) { p.CatalogRevision = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.mutate(&p)
			if _, err := ValidateProductSnapshot(p); err == nil {
				t.Fatalf("%s must fail", tc.name)
			}
		})
	}
	dup := "33333333-3333-4333-8333-333333333333"
	p := base
	p.SubcategoryIDs = []string{dup, dup}
	if _, err := ValidateProductSnapshot(p); err == nil {
		t.Fatal("duplicate subcategories must fail")
	}
}
