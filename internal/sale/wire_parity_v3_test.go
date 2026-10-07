package sale

import "testing"

// TestWireParityV3MoonLightRetailFixture is the Cloud side of the Phase
// 17-R0 sale.finalized.v3 wire-parity pin: the committed fixture was
// captured from MoonLightRetail's REAL BuildSaleFinalizedV3 +
// MarshalSaleFinalizedV3 (internal/domain/sync/wire_parity_test.go) and
// must decode AND validate through the real ingestion validators here.
// Retail asserts byte-stability against the same file.
func TestWireParityV3MoonLightRetailFixture(t *testing.T) {
	raw := loadFixture(t, "wire_sale_v3.json")
	decoded, err := DecodeV3(raw)
	if err != nil {
		t.Fatalf("decode sale v3 fixture: %v", err)
	}
	valid, err := ValidateV3(decoded)
	if err != nil {
		t.Fatalf("validate sale v3 fixture: %v", err)
	}
	if valid.SaleID != "99999999-0000-4000-8000-000000000001" || valid.Currency != "EGP" {
		t.Fatalf("sale v3 parity broken: id=%q currency=%q", valid.SaleID, valid.Currency)
	}
	if valid.Fx != nil {
		t.Fatal("EGP sale must carry no fx snapshot")
	}
	lines := decoded.Lines
	if len(lines) != 1 {
		t.Fatalf("sale v3 line parity broken: %d lines", len(lines))
	}
	line := lines[0]
	if line.VariantID == nil || *line.VariantID != "77777777-0000-4000-8000-000000000002" {
		t.Fatalf("sale v3 variant identity parity broken: %v", line.VariantID)
	}
	if line.VariantSKU != "NEF-BLU-TRA" || line.SKU != "NEF-BLU-TRA" {
		t.Fatalf("sale v3 sku parity broken: line sku=%q variant sku=%q", line.SKU, line.VariantSKU)
	}
	if len(line.VariantAttributes) != 2 {
		t.Fatalf("sale v3 variant attribute parity broken: %+v", line.VariantAttributes)
	}
	if line.VariantPriceEGPCents == nil || *line.VariantPriceEGPCents != 100000 || line.VariantPriceUSDCents != nil {
		t.Fatalf("sale v3 variant pricing parity broken: egp=%v usd=%v", line.VariantPriceEGPCents, line.VariantPriceUSDCents)
	}
}
