package catalog

import (
	"encoding/json"
	"strings"
	"testing"
)

const mediaProduct = "0b3c3f0e-4c1a-4f55-9d55-4f7f8f1a2b3c"

func mediaEntry(id string, variant *string, position int, primary bool) ProductMediaEntry {
	return ProductMediaEntry{ImageID: id, VariantID: variant, Position: position, IsPrimary: primary,
		SHA256: strings.Repeat("ab", 32), ContentType: "image/jpeg", ByteSize: 1200, Width: 800, Height: 600}
}

func TestProductMediaSnapshotValidatesAndCanonicalizes(t *testing.T) {
	variant := "7C0D7C2E-1111-4222-8333-944455556666"
	raw, _ := json.Marshal(ProductMediaSnapshot{ProductID: mediaProduct, MediaRevision: 3, Images: []ProductMediaEntry{
		mediaEntry("aaaaaaaa-0000-4000-8000-000000000002", &variant, 0, true),
		mediaEntry("aaaaaaaa-0000-4000-8000-000000000001", nil, 1, false),
		mediaEntry("aaaaaaaa-0000-4000-8000-000000000003", nil, 0, true),
	}})
	decoded, err := DecodeProductMediaSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := ValidateProductMediaSnapshot(decoded)
	if err != nil {
		t.Fatal(err)
	}
	// Product gallery first by position, then the Variant gallery; UUIDs lower-case.
	if valid.Images[0].ImageID != "aaaaaaaa-0000-4000-8000-000000000003" || valid.Images[1].Position != 1 ||
		*valid.Images[2].VariantID != strings.ToLower(variant) {
		t.Fatalf("canonical order: %+v", valid.Images)
	}
}

func TestProductMediaSnapshotRejectsBrokenGalleries(t *testing.T) {
	variant := "7c0d7c2e-1111-4222-8333-944455556666"
	base := func() ProductMediaSnapshot {
		return ProductMediaSnapshot{ProductID: mediaProduct, MediaRevision: 1, Images: []ProductMediaEntry{
			mediaEntry("aaaaaaaa-0000-4000-8000-000000000001", nil, 0, true),
		}}
	}
	cases := map[string]func(*ProductMediaSnapshot){
		"revision":       func(s *ProductMediaSnapshot) { s.MediaRevision = 0 },
		"nil images":     func(s *ProductMediaSnapshot) { s.Images = nil },
		"product id":     func(s *ProductMediaSnapshot) { s.ProductID = "nope" },
		"duplicate id":   func(s *ProductMediaSnapshot) { s.Images = append(s.Images, mediaEntry(s.Images[0].ImageID, &variant, 0, true)) },
		"same position":  func(s *ProductMediaSnapshot) { s.Images = append(s.Images, mediaEntry("aaaaaaaa-0000-4000-8000-000000000009", nil, 0, false)) },
		"two primaries":  func(s *ProductMediaSnapshot) { s.Images = append(s.Images, mediaEntry("aaaaaaaa-0000-4000-8000-000000000009", nil, 1, true)) },
		"upper hash":     func(s *ProductMediaSnapshot) { s.Images[0].SHA256 = strings.Repeat("AB", 32) },
		"gif":            func(s *ProductMediaSnapshot) { s.Images[0].ContentType = "image/gif" },
		"too big":        func(s *ProductMediaSnapshot) { s.Images[0].ByteSize = MaxMediaBytes + 1 },
		"too many pixel": func(s *ProductMediaSnapshot) { s.Images[0].Width, s.Images[0].Height = 5000, 5000 },
		"bad variant":    func(s *ProductMediaSnapshot) { bad := "x"; s.Images[0].VariantID = &bad },
	}
	for name, mutate := range cases {
		snapshot := base()
		mutate(&snapshot)
		if _, err := ValidateProductMediaSnapshot(snapshot); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
	// One primary per gallery: a Variant gallery may have its own.
	ok := base()
	ok.Images = append(ok.Images, mediaEntry("aaaaaaaa-0000-4000-8000-000000000009", &variant, 0, true))
	if _, err := ValidateProductMediaSnapshot(ok); err != nil {
		t.Fatalf("separate galleries: %v", err)
	}
}
