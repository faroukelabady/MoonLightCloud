package woocommerce

import (
	"context"
	"math"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

func TestWooEffectiveSingleVariantPrice(t *testing.T) {
	for _, currency := range []string{"EGP", "USD"} {
		for _, tc := range []struct {
			name     string
			override *int64
			noBase   bool
			want     string
		}{
			{name: "inherit", want: map[string]string{"EGP": "650.00", "USD": "13.00"}[currency]},
			{name: "zero", override: effectivePricePointer(0), want: "0.00"},
			{name: "override", override: effectivePricePointer(70000), want: "700.00"},
			{name: "override_without_base", override: effectivePricePointer(70000), noBase: true, want: "700.00"},
			{name: "exact_above_2pow53", override: effectivePricePointer(9007199254740993), want: "90071992547409.93"},
		} {
			t.Run(currency+"/"+tc.name, func(t *testing.T) {
				product := effectivePriceProduct(currency, tc.override)
				if tc.noBase {
					product.Prices = nil
				}
				got, err := selectPrice(product, currency)
				if err != nil || got != tc.want {
					t.Fatalf("effective price: %q, %v; want %q", got, err, tc.want)
				}
			})
		}
	}
}

func TestWooEffectiveFramePriceCheckedArithmetic(t *testing.T) {
	for _, currency := range []string{"EGP", "USD"} {
		for _, tc := range []struct {
			name  string
			base  int64
			delta int64
			want  string
		}{
			{name: "zero_override", base: 0, delta: 1500, want: "15.00"},
			{name: "override_plus_frame", base: 70000, delta: 1500, want: "715.00"},
			{name: "exact_large_plus_frame", base: 9007199254740993, delta: 1500, want: "90071992547424.93"},
		} {
			t.Run(currency+"/"+tc.name, func(t *testing.T) {
				product := effectivePriceProduct(currency, &tc.base)
				got, err := configuredPrice(product, currency, tc.delta)
				if err != nil || got != tc.want {
					t.Fatalf("configured price: %q, %v; want %q", got, err, tc.want)
				}
			})
		}
		product := effectivePriceProduct(currency, effectivePricePointer(math.MaxInt64))
		if _, err := configuredPrice(product, currency, 1); err == nil {
			t.Fatalf("%s frame addition must reject int64 overflow", currency)
		}
		product = effectivePriceProduct(currency, nil)
		product.Prices = nil
		if _, err := selectPrice(product, currency); err == nil {
			t.Fatalf("%s missing inherited price must reject rather than use another currency", currency)
		}
	}
}

func TestWooMultipleVariantOverridesWithoutProductBasePrice(t *testing.T) {
	for _, currency := range []string{"EGP", "USD"} {
		h := newWooHarness(t, testConsumerKey, testConsumerSec)
		cfg := testConfig(h)
		cfg.Currency = currency
		provider, err := NewWooCommerceProvider(cfg, h.server.Client())
		if err != nil {
			t.Fatal(err)
		}
		bluePrice, goldPrice := int64(70000), int64(0)
		blue, gold := testVariant("blue", "PAP-BLUE", 4), testVariant("gold", "PAP-GOLD", 2)
		if currency == "USD" {
			blue.PriceUSDMinor, gold.PriceUSDMinor = &bluePrice, &goldPrice
		} else {
			blue.PriceEGPMinor, gold.PriceEGPMinor = &bluePrice, &goldPrice
		}
		product := testVariantProduct("multiple-override-no-base", blue, gold)
		product.Prices = nil
		parent, err := provider.UpsertProduct(context.Background(), testUpsertReq(product, true, nil))
		if err != nil {
			t.Fatal(err)
		}
		req := testVariantsUpsertReq(product, nil)
		req.ExternalProductID = parent.ExternalProductID
		mapped, err := provider.UpsertProductVariants(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		parentID, _ := parseWooID(parent.ExternalProductID)
		for variantID, want := range map[string]string{blue.VariantID: "700.00", gold.VariantID: "0.00"} {
			childID, _ := parseWooID(mapped.Variants[variantID])
			if actual := variationState(t, h, parentID, childID)["regular_price"]; actual != want {
				t.Fatalf("%s variant %s: price=%v want=%s", currency, variantID, actual, want)
			}
		}
	}
}

func TestWooSingleVariantAndFramePublishEffectivePrice(t *testing.T) {
	for _, currency := range []string{"EGP", "USD"} {
		for _, amount := range []int64{0, 70000, 9007199254740993} {
			for _, framed := range []bool{false, true} {
				h := newWooHarness(t, testConsumerKey, testConsumerSec)
				cfg := testConfig(h)
				cfg.Currency = currency
				provider, err := NewWooCommerceProvider(cfg, h.server.Client())
				if err != nil {
					t.Fatal(err)
				}
				product := effectivePriceProduct(currency, &amount)
				if framed {
					usdDelta := int64(1500)
					product.Configurations = []commerce.CommerceConfiguration{{
						ConfigurationID: "11111111-0000-4000-8000-0000000000c1", Kind: "frame",
						StyleCode: "classic", StyleNameAR: "كلاسيكي", ColorCode: "black", ColorNameAR: "أسود",
						PriceDeltaEGPMinor: 1500, PriceDeltaUSDMinor: &usdDelta, Enabled: true,
					}}
				}
				parent, err := provider.UpsertProduct(context.Background(), testUpsertReq(product, true, nil))
				if err != nil {
					t.Fatal(err)
				}
				req := testVariantsUpsertReq(product, nil)
				req.ExternalProductID = parent.ExternalProductID
				if _, err := provider.UpsertProductVariants(context.Background(), req); err != nil {
					t.Fatal(err)
				}
				id, _ := parseWooID(parent.ExternalProductID)
				want, _ := minorToDecimal(amount)
				if !framed {
					if got := h.productState(id)["regular_price"]; got != want {
						t.Fatalf("%s simple override %d: got %v want %s", currency, amount, got, want)
					}
					continue
				}
				wantFrame, _ := minorToDecimal(amount + 1500)
				noFrame, frame := false, false
				h.mu.Lock()
				for _, variation := range h.variations[id] {
					meta := metaMap(t, variation)
					if meta[metaConfigurationID] == commerce.NoFrameConfigurationID {
						noFrame = variation["regular_price"] == want
					} else if meta[metaConfigurationID] == product.Configurations[0].ConfigurationID {
						frame = variation["regular_price"] == wantFrame
					}
				}
				h.mu.Unlock()
				if !noFrame || !frame {
					t.Fatalf("%s framed override %d: no-frame=%v, frame=%v; expected %s / %s", currency, amount, noFrame, frame, want, wantFrame)
				}
			}
		}
	}
}

func effectivePriceProduct(currency string, override *int64) commerce.CommerceProduct {
	variant := testVariant("blue", "PAP-BLUE", 4)
	// Deliberately different opposite-currency value catches implicit FX
	// and accidental cross-currency fallback in the resolver.
	other := int64(99999)
	if currency == "USD" {
		variant.PriceUSDMinor = override
		variant.PriceEGPMinor = &other
	} else {
		variant.PriceEGPMinor = override
		variant.PriceUSDMinor = &other
	}
	return testVariantProduct("effective-price-product", variant)
}

func effectivePricePointer(value int64) *int64 { return &value }
