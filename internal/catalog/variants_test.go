package catalog

import (
	"encoding/json"
	"strings"
	"testing"
)

// Phase 17 contract tests: strict wire validation, semantic normalization
// (mirror fields are never semantic), and the variant availability formula.

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func validVariantWire() map[string]any {
	return map[string]any{
		"variant_id":         "aaaaaaaa-0000-4000-8000-000000000001",
		"product_id":         "bbbbbbbb-0000-4000-8000-000000000002",
		"sku":                "ML-V-1",
		"is_active":          true,
		"price_egp_cents":    123456,
		"price_usd_cents":    nil,
		"stock_quantity":     4,
		"inventory_revision": 7,
		"variant_revision":   3,
		"position":           0,
		"combination_key":    "color=blue\u001fpainting_style=traditional",
		"attributes": []any{
			map[string]any{
				"definition_code": "color", "value_code": "blue",
				"name_ar": "أزرق", "name_en": "Blue",
				"definition_name_ar": "اللون", "definition_name_en": "Color",
				"position": 0,
			},
		},
		"catalog_revision": 9,
	}
}

func decodeValidVariant(t *testing.T) ProductVariantSnapshot {
	t.Helper()
	raw, err := DecodeProductVariantSnapshot(mustJSON(t, validVariantWire()))
	if err != nil {
		t.Fatal(err)
	}
	valid, err := ValidateProductVariantSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	return valid
}

func TestProductVariantSnapshotRoundTrip(t *testing.T) {
	valid := decodeValidVariant(t)
	if valid.VariantID != "aaaaaaaa-0000-4000-8000-000000000001" || valid.SKU != "ML-V-1" {
		t.Fatalf("identity: %+v", valid)
	}
	if valid.PriceEGPCents == nil || *valid.PriceEGPCents != 123456 || valid.PriceUSDCents != nil {
		t.Fatalf("money: %+v", valid)
	}
	if valid.Deleted {
		t.Fatal("deleted must default to false")
	}
	if len(valid.Attributes) != 1 || valid.Attributes[0].DefinitionCode != "color" {
		t.Fatalf("attributes: %+v", valid.Attributes)
	}
}

func TestProductVariantSnapshotValidation(t *testing.T) {
	cases := map[string]func(w map[string]any){
		"bad variant_id":      func(w map[string]any) { w["variant_id"] = "nope" },
		"bad product_id":      func(w map[string]any) { w["product_id"] = "nope" },
		"blank sku":           func(w map[string]any) { w["sku"] = "   " },
		"long sku":            func(w map[string]any) { w["sku"] = strings.Repeat("s", 65) },
		"sku control ws":      func(w map[string]any) { w["sku"] = "a\tb" },
		"negative egp price":  func(w map[string]any) { w["price_egp_cents"] = -1 },
		"negative usd price":  func(w map[string]any) { w["price_usd_cents"] = -2 },
		"float money":         func(w map[string]any) { w["price_egp_cents"] = 1.5 },
		"negative stock":      func(w map[string]any) { w["stock_quantity"] = -1 },
		"zero variant rev":    func(w map[string]any) { w["variant_revision"] = 0 },
		"zero catalog rev":    func(w map[string]any) { w["catalog_revision"] = 0 },
		"negative position":   func(w map[string]any) { w["position"] = -1 },
		"combination control": func(w map[string]any) { w["combination_key"] = "a\nb" },
		"duplicate definition": func(w map[string]any) {
			w["attributes"] = []any{
				map[string]any{"definition_code": "color", "value_code": "blue", "name_ar": "أزرق", "definition_name_ar": "اللون", "position": 0},
				map[string]any{"definition_code": "color", "value_code": "red", "name_ar": "أحمر", "definition_name_ar": "اللون", "position": 1},
			}
		},
		"empty definition code": func(w map[string]any) {
			w["attributes"] = []any{
				map[string]any{"definition_code": "", "value_code": "blue", "name_ar": "أزرق", "definition_name_ar": "اللون", "position": 0},
			}
		},
		"blank name_ar": func(w map[string]any) {
			w["attributes"] = []any{
				map[string]any{"definition_code": "color", "value_code": "blue", "name_ar": " ", "definition_name_ar": "اللون", "position": 0},
			}
		},
		"too many attributes": func(w map[string]any) {
			list := make([]any, 0, 33)
			for i := 0; i <= MaxVariantAttributes; i++ {
				list = append(list, map[string]any{"definition_code": "c" + strings.Repeat("x", i), "value_code": "v", "name_ar": "n", "definition_name_ar": "d", "position": i})
			}
			w["attributes"] = list
		},
	}
	for name, mutate := range cases {
		w := validVariantWire()
		mutate(w)
		// Rejection may happen at decode (float/overflow money) or at
		// strict validation: both are closed ingestion gates.
		raw, err := DecodeProductVariantSnapshot(mustJSON(t, w))
		if err != nil {
			continue
		}
		if _, err := ValidateProductVariantSnapshot(raw); err == nil {
			t.Fatalf("%s: want validation failure", name)
		}
	}
}

func TestProductVariantSnapshotTombstoneAccepted(t *testing.T) {
	w := validVariantWire()
	w["deleted"] = true
	raw, err := DecodeProductVariantSnapshot(mustJSON(t, w))
	if err != nil {
		t.Fatal(err)
	}
	valid, err := ValidateProductVariantSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !valid.Deleted {
		t.Fatal("tombstone must round-trip")
	}
}

func TestProductVariantInventoryValidation(t *testing.T) {
	wire := map[string]any{
		"variant_id": "aaaaaaaa-0000-4000-8000-000000000001",
		"product_id": "bbbbbbbb-0000-4000-8000-000000000002",
		"sku":        "ML-V-1", "stock_quantity": 4,
		"inventory_revision": 7, "ready": true, "sell_online": true,
		"online_allocation_limit": nil, "policy_revision": 2, "catalog_revision": 9,
	}
	raw, err := DecodeProductVariantInventorySnapshot(mustJSON(t, wire))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateProductVariantInventorySnapshot(raw); err != nil {
		t.Fatalf("valid: %v", err)
	}
	for name, mutate := range map[string]func(w map[string]any){
		"bad variant_id":   func(w map[string]any) { w["variant_id"] = "x" },
		"zero inv rev":     func(w map[string]any) { w["inventory_revision"] = 0 },
		"negative stock":   func(w map[string]any) { w["stock_quantity"] = -1 },
		"negative policy":  func(w map[string]any) { w["policy_revision"] = -1 },
		"zero catalog rev": func(w map[string]any) { w["catalog_revision"] = 0 },
		"huge allocation":  func(w map[string]any) { w["online_allocation_limit"] = 1 << 40 },
		"blank sku":        func(w map[string]any) { w["sku"] = "" },
	} {
		w := map[string]any{}
		for k, v := range wire {
			w[k] = v
		}
		mutate(w)
		raw, err := DecodeProductVariantInventorySnapshot(mustJSON(t, w))
		if err != nil {
			continue
		}
		if _, err := ValidateProductVariantInventorySnapshot(raw); err == nil {
			t.Fatalf("%s: want validation failure", name)
		}
	}
}

// Mirror fields (stock on the catalog stream) and wire attribute order
// are non-semantic: fingerprints must stay equal so equal-revision
// redelivery converges instead of conflicting. The stream's own revision
// IS semantic.
func TestProductVariantFingerprintSemantics(t *testing.T) {
	base := decodeValidVariant(t)
	baseFP := FingerprintProductVariant(base)

	moved := validVariantWire()
	moved["stock_quantity"] = 99
	moved["inventory_revision"] = 42
	other, err := ValidateProductVariantSnapshot(mustDecodeVariant(t, moved))
	if err != nil {
		t.Fatal(err)
	}
	if FingerprintProductVariant(other) != baseFP {
		t.Fatal("inventory mirrors must not change semantic identity")
	}

	reordered := validVariantWire()
	reordered["attributes"] = []any{
		map[string]any{"definition_code": "style", "value_code": "traditional", "name_ar": "تقليدي", "definition_name_ar": "النمط", "position": 1},
		map[string]any{"definition_code": "color", "value_code": "blue", "name_ar": "أزرق", "name_en": "Blue", "definition_name_ar": "اللون", "definition_name_en": "Color", "position": 0},
	}
	sorted, err := ValidateProductVariantSnapshot(mustDecodeVariant(t, reordered))
	if err != nil {
		t.Fatal(err)
	}
	if FingerprintProductVariant(sorted) == baseFP {
		t.Fatal("changed attribute set must change semantic identity")
	}
	// Same set in wire-order noise: position 0 then 1, listed reversed.
	same := validVariantWire()
	same["attributes"] = reordered["attributes"].([]any)[:1]
	base2 := validVariantWire()
	base2["attributes"] = []any{
		map[string]any{"definition_code": "style", "value_code": "traditional", "name_ar": "تقليدي", "definition_name_ar": "النمط", "position": 1},
	}
	a, err := ValidateProductVariantSnapshot(mustDecodeVariant(t, same))
	if err != nil {
		t.Fatal(err)
	}
	b, err := ValidateProductVariantSnapshot(mustDecodeVariant(t, base2))
	if err != nil {
		t.Fatal(err)
	}
	if FingerprintProductVariant(a) != FingerprintProductVariant(b) {
		t.Fatal("wire order must not change semantic identity")
	}

	rev := validVariantWire()
	rev["variant_revision"] = 4
	bumped, err := ValidateProductVariantSnapshot(mustDecodeVariant(t, rev))
	if err != nil {
		t.Fatal(err)
	}
	if FingerprintProductVariant(bumped) == baseFP {
		t.Fatal("variant_revision must change semantic identity")
	}
}

func mustDecodeVariant(t *testing.T, w map[string]any) ProductVariantSnapshot {
	t.Helper()
	raw, err := DecodeProductVariantSnapshot(mustJSON(t, w))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The inventory stream's mirror fields are equally non-semantic.
func TestVariantInventoryFingerprintSemantics(t *testing.T) {
	wire := map[string]any{
		"variant_id": "aaaaaaaa-0000-4000-8000-000000000001",
		"product_id": "bbbbbbbb-0000-4000-8000-000000000002",
		"sku":        "ML-V-1", "stock_quantity": 4,
		"inventory_revision": 7, "ready": true, "sell_online": true,
		"online_allocation_limit": 3, "policy_revision": 2, "catalog_revision": 9,
	}
	raw, err := DecodeProductVariantInventorySnapshot(mustJSON(t, wire))
	if err != nil {
		t.Fatal(err)
	}
	base, err := ValidateProductVariantInventorySnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	baseFP := FingerprintProductVariantInventory(base)

	mirrors := map[string]any{}
	for k, v := range wire {
		mirrors[k] = v
	}
	mirrors["policy_revision"] = 5
	mirrors["sell_online"] = false
	mirrors["ready"] = false
	mirrors["sku"] = "OTHER"
	raw, err = DecodeProductVariantInventorySnapshot(mustJSON(t, mirrors))
	if err != nil {
		t.Fatal(err)
	}
	other, err := ValidateProductVariantInventorySnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if FingerprintProductVariantInventory(other) != baseFP {
		t.Fatal("policy/sku mirrors must not change inventory semantic identity")
	}

	mirrors["stock_quantity"] = 5
	raw, err = DecodeProductVariantInventorySnapshot(mustJSON(t, mirrors))
	if err != nil {
		t.Fatal(err)
	}
	stock, err := ValidateProductVariantInventorySnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if FingerprintProductVariantInventory(stock) == baseFP {
		t.Fatal("stock must change inventory semantic identity")
	}
}

// product.snapshot.v2 (Phase 17-R0, ADR-0049): the payload carries NO
// SKU-bearing field at all — Product has no SKU authority anywhere. The
// retired `primary_variant_sku` display mirror is tolerated-and-ignored
// for one release (Retail may still emit it until both trees ship
// together): it never validates, stores, or projects. v1 stays frozen.
func TestProductSnapshotV2Contract(t *testing.T) {
	wire := map[string]any{
		"product_id":      "bbbbbbbb-0000-4000-8000-000000000002",
		"product_type_id": "10000000-0000-4000-8000-000000000001",
		"name":            "ساعة", "translations": []any{},
		"prices":          []any{map[string]any{"currency": "EGP", "price_cents": 100}},
		"top_category_id": "cccccccc-0000-4000-8000-000000000003",
		"subcategory_ids": []any{}, "tag_ids": []any{},
		"is_active": true, "catalog_revision": 5,
	}
	raw, err := DecodeProductSnapshotV2(mustJSON(t, wire))
	if err != nil {
		t.Fatal(err)
	}
	valid, err := ValidateProductSnapshotV2(raw)
	if err != nil {
		t.Fatal(err)
	}
	if valid.SKU != "" {
		t.Fatalf("v2 must never carry a sku, got %q", valid.SKU)
	}
	// Tolerate-and-ignore: a retired primary_variant_sku mirror (blank or
	// not) is accepted for one release and NEVER mapped into the
	// normalized snapshot.
	deprecated := map[string]any{}
	for k, v := range wire {
		deprecated[k] = v
	}
	deprecated["primary_variant_sku"] = "ML-V-1"
	raw, err = DecodeProductSnapshotV2(mustJSON(t, deprecated))
	if err != nil {
		t.Fatal(err)
	}
	valid, err = ValidateProductSnapshotV2(raw)
	if err != nil {
		t.Fatalf("deprecated mirror must be tolerated and ignored: %v", err)
	}
	if valid.SKU != "" {
		t.Fatalf("deprecated mirror must never map to sku: %q", valid.SKU)
	}
	blank := map[string]any{}
	for k, v := range wire {
		blank[k] = v
	}
	blank["primary_variant_sku"] = ""
	raw, err = DecodeProductSnapshotV2(mustJSON(t, blank))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateProductSnapshotV2(raw); err != nil {
		t.Fatalf("blank mirror: %v", err)
	}
	// Non-blank v1 invariants still hold under v2.
	bad := map[string]any{}
	for k, v := range wire {
		bad[k] = v
	}
	bad["product_id"] = "nope"
	raw, err = DecodeProductSnapshotV2(mustJSON(t, bad))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateProductSnapshotV2(raw); err == nil {
		t.Fatal("v2 must enforce product_id UUID")
	}
	// v1 still requires its sku (frozen contract).
	v1 := map[string]any{}
	for k, v := range wire {
		v1[k] = v
	}
	v1["sku"] = ""
	rawV1, err := DecodeProductSnapshot(mustJSON(t, v1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateProductSnapshot(rawV1); err == nil {
		t.Fatal("v1 must keep requiring sku")
	}
}

// Variant availability formula: eligibility + activity gate, then clamp
// and cap exactly like the product formula.
func TestComputeVariantAvailability(t *testing.T) {
	three, seven := 3, 7
	cases := []struct {
		name             string
		eligible, active bool
		stock            int
		limit            *int
		want             int
	}{
		{"not eligible", false, true, 5, nil, 0},
		{"inactive variant", true, false, 5, nil, 0},
		{"uncapped", true, true, 5, nil, 5},
		{"capped", true, true, 7, &three, 3},
		{"under cap", true, true, 2, &seven, 2},
		{"zero cap", true, true, 5, new(int), 0},
		{"negative stock clamped", true, true, -4, nil, 0},
	}
	for _, tc := range cases {
		if got := ComputeVariantAvailability(tc.eligible, tc.active, tc.stock, tc.limit); got != tc.want {
			t.Fatalf("%s: got %d want %d", tc.name, got, tc.want)
		}
	}
}
