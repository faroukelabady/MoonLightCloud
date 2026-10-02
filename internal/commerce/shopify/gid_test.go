package shopify

import "testing"

// Phase 11 §29: GID validation prevents resource-type confusion and the
// durable identity normalization stays deterministic.

func TestParseGIDAcceptsCanonicalForms(t *testing.T) {
	id, err := ParseGID("gid://shopify/Product/8294713837621", ResourceProduct)
	if err != nil || id != "8294713837621" {
		t.Fatalf("got %q, %v", id, err)
	}
	// suffix IS the legacy resource id
	if got := FormatGID(ResourceProduct, id); got != "gid://shopify/Product/8294713837621" {
		t.Fatalf("round trip failed: %s", got)
	}
	if _, err := ParseGID("gid://shopify/Order/5231234567890", ResourceOrder); err != nil {
		t.Fatal(err)
	}
}

func TestParseGIDRejectsWrongResourceType(t *testing.T) {
	// A Variant GID is never a Product GID.
	if _, err := ParseGID("gid://shopify/ProductVariant/8294713837621", ResourceProduct); err == nil {
		t.Fatal("variant gid accepted as product gid")
	}
	if _, err := ParseGID("gid://shopify/Product/8294713837621", ResourceProductVariant); err == nil {
		t.Fatal("product gid accepted as variant gid")
	}
}

func TestParseGIDRejectsMalformed(t *testing.T) {
	for _, raw := range []string{
		"", "8294713837621", "gid://shopify/Product/", "gid://shopify/Product/abc",
		"gid://shopify/Product/007", "gid://shopify/Product/0", "https://evil.example/Product/1",
		"gid://evil/Product/1", "gid://shopify/Product/1/2", "gid://shopify/Product/1 ",
	} {
		if _, err := ParseGID(raw, ResourceProduct); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestCanonicalDecimalIDBounds(t *testing.T) {
	if _, err := CanonicalDecimalID("9223372036854775807"); err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalDecimalID("92233720368547758070"); err == nil {
		t.Fatal("accepted 20-digit id")
	}
}

func TestFormatGIDUsesInternalTypesOnly(t *testing.T) {
	if got := FormatGID(ResourceInventoryItem, "30322695"); got != "gid://shopify/InventoryItem/30322695" {
		t.Fatal(got)
	}
}
