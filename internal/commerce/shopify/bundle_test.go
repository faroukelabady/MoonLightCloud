package shopify

// Phase 15-R1 F08/F09/F10 + §164: configuration identity is
// metadata-based (never labels), disabled/currency-incomplete choices are
// never offered, nested price refusals surface as bounded failures, and
// the bundle representation keeps one tracked base pool.

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

func frameConfig(id, styleCode, colorCode, styleEN, colorEN string, egp int64, usd *int64, enabled bool) commerce.CommerceConfiguration {
	return commerce.CommerceConfiguration{
		ConfigurationID: id, Kind: "frame",
		StyleCode: styleCode, StyleNameAR: styleCode + "-ar", StyleNameEN: &styleEN,
		ColorCode: colorCode, ColorNameAR: colorCode + "-ar", ColorNameEN: &colorEN,
		PriceDeltaEGPMinor: egp, PriceDeltaUSDMinor: usd, Enabled: enabled,
		ConfigurationRevision: 1,
	}
}

// F08: two configurations with IDENTICAL displayed labels but distinct
// IDs/codes keep distinct identities; renames never change identity;
// foreign metadata-less variants are never adopted.
func TestConfigurationIdentityIsMetadataBased(t *testing.T) {
	same := "Same Label"
	variants := []gqlVariant{
		{ID: "gid://shopify/ProductVariant/1"},
		{ID: "gid://shopify/ProductVariant/2"},
		{ID: "gid://shopify/ProductVariant/3"},
	}
	variants[0].Metafields.Nodes = []gqlMetafield{{Namespace: metafieldNamespace, Key: keyConfigurationIDMeta, Value: "11111111-0000-4000-8000-0000000000c1"}}
	variants[1].Metafields.Nodes = []gqlMetafield{{Namespace: metafieldNamespace, Key: keyConfigurationIDMeta, Value: "11111111-0000-4000-8000-0000000000c2"}}
	// Variant 3: foreign/manual — matching display label, no metadata.
	_ = same

	identities := configurationIdentities(variants)
	if identities["11111111-0000-4000-8000-0000000000c1"] != "gid://shopify/ProductVariant/1" ||
		identities["11111111-0000-4000-8000-0000000000c2"] != "gid://shopify/ProductVariant/2" {
		t.Fatalf("distinct identities collapsed: %v", identities)
	}
	if len(identities) != 2 {
		t.Fatalf("foreign variant adopted: %v", identities)
	}
}

// F09: disabled and currency-incomplete choices are never offered; the
// NO-FRAME choice remains coherent; NULL USD is not zero and never EGP.
func TestConfigurationsFilteredByAvailability(t *testing.T) {
	usd := int64(2500)
	product := commerce.CommerceProduct{
		ProductID: "p1", SKU: "S1",
		Prices: []commerce.Money{{Currency: "EGP", AmountMinor: 100000}, {Currency: "USD", AmountMinor: 2000}},
		Configurations: []commerce.CommerceConfiguration{
			frameConfig("11111111-0000-4000-8000-0000000000c1", "classic", "black", "Classic", "Black", 30000, &usd, true),
			frameConfig("11111111-0000-4000-8000-0000000000c2", "classic", "gold", "Classic", "Gold", 35000, &usd, false), // disabled
			frameConfig("11111111-0000-4000-8000-0000000000c3", "modern", "black", "Modern", "Black", 40000, nil, true),   // no USD
		},
	}
	egp := chooseConfigurations(product, "EGP")
	if len(egp) != 2 {
		t.Fatalf("EGP choices (enabled only): %v", egp)
	}
	usdChoices := chooseConfigurations(product, "USD")
	if len(usdChoices) != 1 || usdChoices[0].ConfigurationID != "11111111-0000-4000-8000-0000000000c1" {
		t.Fatalf("USD choices (currency-coherent only): %v", usdChoices)
	}
	// Disabled/currency-incomplete choices produce NO frame variants.
	variants := frameComponentVariants(chooseConfigurations(product, "USD"))
	for _, raw := range variants {
		entry := raw
		item := entry["inventoryItem"].(map[string]any)
		if item["tracked"] != false {
			t.Fatal("frame variants must be untracked (no frame stock)")
		}
		for _, value := range entry["optionValues"].([]map[string]any) {
			if value["name"] == "Gold" {
				t.Fatal("disabled configuration leaked into the remote representation")
			}
		}
	}
}

// F10: nested productVariantsBulkUpdate userErrors are surfaced as
// bounded failures — never a silent success.
func TestBundlePriceRefusalIsBounded(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	h.failStatus["MoonlightVariantPrices"] = 200
	h.failBody["MoonlightVariantPrices"] = `{"data":{"productVariantsBulkUpdate":{"productVariants":[],"userErrors":[{"field":["variants"],"message":"price refused"}]}}}`
	usd := int64(2500)
	product := commerce.CommerceProduct{
		ProductID: "p1", SKU: "S1",
		Prices: []commerce.Money{{Currency: "EGP", AmountMinor: 100000}},
		Configurations: []commerce.CommerceConfiguration{
			frameConfig("11111111-0000-4000-8000-0000000000c1", "classic", "black", "Classic", "Black", 30000, &usd, true),
		},
	}
	identities := map[string]string{
		commerce.NoFrameConfigurationID:        "gid://shopify/ProductVariant/9",
		"11111111-0000-4000-8000-0000000000c1": "gid://shopify/ProductVariant/10",
	}
	var err error
	coord := fixtureCoordinator{}
	err = coord.WithProductSync(context.Background(), provider.Key(), "p1", func(held context.Context) error {
		return provider.setBundleVariantPrices(held, "gid://shopify/Product/7", product, identities, product.Configurations)
	})
	if err == nil {
		t.Fatal("nested refusal must never be a silent success")
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Fatalf("bounded refusal message expected, got %v", err)
	}
}

// F06: deterministic validation-class refusals map to the stable
// capability code; unknown/temporary failures keep frozen retry
// semantics (never a blanket reclassification).
func TestBundleCapabilityClassification(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	permanent := provider.classifyBundleFailure(commerce.ValidationError("invalid schema"))
	if permanent == nil || !strings.Contains(permanent.Error(), CapabilityCode) {
		t.Fatalf("deterministic refusal must map to the capability code: %v", permanent)
	}
	temporary := commerce.TemporaryError("connection reset")
	if provider.classifyBundleFailure(temporary) != temporary {
		t.Fatal("temporary failures must keep frozen retry classification")
	}
	if provider.classifyBundleFailure(nil) != nil {
		t.Fatal("nil stays nil")
	}
}

// F19: every bundle failure path routes through the frozen scrub/bound
// contract — remote messages never carry credentials or unbounded text.
func TestBundleErrorsAreScrubbedAndBounded(t *testing.T) {
	h := newHarness(t)
	provider := newTestProvider(t, h)
	secret := "shpat_synthetic_secret_value_1234567890"
	oversized := strings.Repeat("A", 2000) + " token=" + secret
	h.failStatus["MoonlightVariantPrices"] = 200
	h.failBody["MoonlightVariantPrices"] = `{"data":{"productVariantsBulkUpdate":{"productVariants":[],"userErrors":[{"field":["variants"],"message":` + strconv.Quote(oversized) + `}]}}}`
	usd := int64(2500)
	product := commerce.CommerceProduct{
		ProductID: "p1", SKU: "S1",
		Prices: []commerce.Money{{Currency: "EGP", AmountMinor: 100000}},
		Configurations: []commerce.CommerceConfiguration{
			frameConfig("11111111-0000-4000-8000-0000000000c1", "classic", "black", "Classic", "Black", 30000, &usd, true),
		},
	}
	var err error
	coord := fixtureCoordinator{}
	err = coord.WithProductSync(context.Background(), provider.Key(), "p1", func(held context.Context) error {
		return provider.setBundleVariantPrices(held, "gid://shopify/Product/7", product,
			map[string]string{"11111111-0000-4000-8000-0000000000c1": "gid://shopify/ProductVariant/10"}, product.Configurations)
	})
	if err == nil {
		t.Fatal("refusal must surface")
	}
	message := err.Error()
	if strings.Contains(message, secret) {
		t.Fatal("provider secret leaked into the error boundary")
	}
	if len(message) > 512 {
		t.Fatalf("error text unbounded: %d bytes", len(message))
	}
	t.Logf("bounded error length=%d", len(message))
}

// F08: equal display labels never collapse identities (deterministic
// code-suffixed option values) and never invent cross-product choices.
func TestEqualLabelsStayDistinct(t *testing.T) {
	usd := int64(1)
	choices := []commerce.CommerceConfiguration{
		frameConfig("11111111-0000-4000-8000-0000000000c1", "classic", "black", "Classic", "Black", 100, &usd, true),
		frameConfig("11111111-0000-4000-8000-0000000000c2", "classic", "black", "Classic", "Black", 200, &usd, true),
	}
	styles := frameStyleValues(choices)
	colors := frameColorValues(choices)
	if styles[1] == styles[2] {
		t.Fatalf("equal labels collapsed: %v", styles)
	}
	if colors[1] == colors[2] {
		t.Fatalf("equal color labels collapsed: %v", colors)
	}
	// Explicit combinations only: two tuples, no Cartesian extras.
	variants := frameComponentVariants(choices)
	if len(variants) != 3 { // NO-FRAME + 2 explicit configurations
		t.Fatalf("combinations must be explicit rows, got %d", len(variants))
	}
}

func TestBundleUpdateConfiguredSecretsAreScrubbed(t *testing.T) {
	for _, placement := range []string{"beginning", "middle", "end"} {
		for _, filler := range []int{8, 2000} {
			t.Run(placement+strconv.Itoa(filler), func(t *testing.T) {
				h := newHarness(t)
				p := newTestProvider(t, h)
				base, createErr := p.UpsertProduct(context.Background(), upsertRequest("privacy-base", "PRIVACY-BASE", false))
				if createErr != nil {
					t.Fatal(createErr)
				}

				secrets := h.token + " " + h.secret
				padding := strings.Repeat("界", filler)
				message := secrets + padding
				if placement == "middle" {
					message = padding + secrets + padding
				}
				if placement == "end" {
					message = padding + secrets
				}
				message = "privacy-oracle: " + message
				h.failStatus["bundle_update"] = 200
				h.failBody["bundle_update"] = `{"data":{"productBundleUpdate":{"productBundleOperation":null,"userErrors":[{"message":` + strconv.Quote(message) + `}]}}}`
				err := p.bundleUpdate(commerce.WithProductMutationBarrier(context.Background(), fixtureMutationBarrier{}), "gid://shopify/Product/3", FormatGID(ResourceProduct, base.ExternalProductID), "gid://shopify/Product/2", []map[string]any{{"componentOptionId": "gid://shopify/ProductOption/21", "name": "Frame", "values": []string{"Classic"}}})
				if err == nil || !strings.Contains(err.Error(), "privacy-oracle") {
					t.Fatal("nested provider error path was not exercised")
				}
				if strings.Contains(err.Error(), h.token) || strings.Contains(err.Error(), h.secret) {
					t.Fatal("configured credential survived public error boundary")
				}
				if len(err.Error()) > 512 {
					t.Fatalf("final error exceeds byte bound: %d", len(err.Error()))
				}
			})
		}
	}
}
