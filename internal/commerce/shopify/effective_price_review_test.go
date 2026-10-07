package shopify

import (
	"context"
	"math"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// Exercise the public HTTP adapter; the parent and physical Variant paths must
// both publish the same effective price, including explicit zero and exact money.
func TestReviewShopifySingleVariantEffectivePrice(t *testing.T) {
	for _, currency := range []string{"EGP", "USD"} {
		for _, tc := range []struct {
			name     string
			override *int64
			noBase   bool
			want     string
		}{
			{name: "inherit", want: map[string]string{"EGP": "650.00", "USD": "13.00"}[currency]},
			{name: "zero", override: reviewPricePointer(0), want: "0.00"},
			{name: "positive", override: reviewPricePointer(70000), want: "700.00"},
			{name: "without_base", override: reviewPricePointer(70000), noBase: true, want: "700.00"},
			{name: "above_2pow53", override: reviewPricePointer(9007199254740993), want: "90071992547409.93"},
		} {
			t.Run(currency+"/"+tc.name, func(t *testing.T) {
				h := newHarness(t)
				provider := reviewCurrencyProvider(t, h, currency)
				product := reviewPriceProduct(currency, tc.override)
				if tc.noBase {
					product.Prices = nil
				}
				result, err := provider.UpsertProduct(context.Background(), reviewPriceRequest(product))
				if err != nil {
					t.Fatal(err)
				}
				mapped, err := provider.UpsertProductVariants(context.Background(), p17VariantsUpsertReq(product, result.ExternalProductID, nil))
				if err != nil {
					t.Fatal(err)
				}
				remote := h.productOf(result.ExternalProductID)
				if remote == nil || len(remote.variants) != 1 || remote.variants[0].price != tc.want || mapped.Variants[p17BlueVariantID] == "" {
					t.Fatalf("effective price=%+v mapping=%v want=%s", remote, mapped.Variants, tc.want)
				}
			})
		}
	}
}

func TestReviewShopifyMultiVariantOverridesWithoutBase(t *testing.T) {
	for _, currency := range []string{"EGP", "USD"} {
		t.Run(currency, func(t *testing.T) {
			h := newHarness(t)
			provider := reviewCurrencyProvider(t, h, currency)
			product := reviewPriceProduct(currency, reviewPricePointer(70000))
			gold := p17Variant("gold", "PAP-GOLD", 2, reviewPricePointer(0))
			gold.PriceUSDMinor = reviewPricePointer(0)
			product.Variants = append(product.Variants, gold)
			product.Prices = nil
			result, err := provider.UpsertProduct(context.Background(), reviewPriceRequest(product))
			if err != nil {
				t.Fatal(err)
			}
			mapped, err := provider.UpsertProductVariants(context.Background(), p17VariantsUpsertReq(product, result.ExternalProductID, nil))
			if err != nil {
				t.Fatal(err)
			}
			remote := h.productOf(result.ExternalProductID)
			if remote == nil || len(remote.variants) != 2 || len(mapped.Variants) != 2 {
				t.Fatalf("expected two physical Variants: %+v, %v", remote, mapped.Variants)
			}
			for _, variant := range remote.variants {
				want := map[string]string{"PAP-BLUE": "700.00", "PAP-GOLD": "0.00"}[variant.sku]
				if want == "" || variant.price != want {
					t.Fatalf("%s price=%s want=%s", variant.sku, variant.price, want)
				}
			}
		})
	}
}

// Real GraphQL price writes against a stateful fixture, isolated from bundle
// creation/durable-receipt proofs that are covered by PostgreSQL suites.
func TestReviewShopifyFrameEffectivePriceHTTP(t *testing.T) {
	for _, currency := range []string{"EGP", "USD"} {
		for _, amount := range []int64{0, 70000, 9007199254740993, math.MaxInt64} {
			f := newBundleFixture(t)
			provider := reviewCurrencyProvider(t, f.h, currency)
			product := reviewPriceProduct(currency, &amount)
			const parent = "gid://shopify/Product/9000"
			const plain = "gid://shopify/ProductVariant/9001"
			const framed = "gid://shopify/ProductVariant/9002"
			const configuration = "11111111-0000-4000-8000-0000000000c1"
			product.Configurations = []commerce.CommerceConfiguration{frameConfig(configuration, "classic", "black", "Classic", "Black", 1500, reviewPricePointer(1500), true)}
			f.resources[parent] = map[string]any{"variants": map[string]any{"nodes": []any{map[string]any{"id": plain}, map[string]any{"id": framed}}}}
			identities := map[string]string{commerce.NoFrameConfigurationID: plain, configuration: framed}
			err := (fixtureCoordinator{}).WithProductSync(context.Background(), provider.Key(), product.ProductID, func(ctx context.Context) error {
				return provider.setBundleVariantPrices(ctx, parent, product, identities, product.Configurations)
			})
			if amount == math.MaxInt64 {
				if err == nil || f.priceWrites != 0 {
					t.Fatalf("overflow must reject before price writes: err=%v writes=%d", err, f.priceWrites)
				}
				continue
			}
			if err != nil {
				t.Fatal(err)
			}
			wantPlain, _ := FormatMinorUnits(amount)
			wantFrame, _ := FormatMinorUnits(amount + 1500)
			if f.prices[plain] != wantPlain || f.prices[framed] != wantFrame || f.priceWrites != 2 {
				t.Fatalf("%s effective bundle prices=%v want=%s/%s", currency, f.prices, wantPlain, wantFrame)
			}
		}
	}
}

func reviewCurrencyProvider(t *testing.T, h *shopifyHarness, currency string) *ShopifyProvider {
	t.Helper()
	h.shopCurrency = currency
	cfg := testConfig()
	cfg.Currency = currency
	provider, err := NewShopifyProvider(cfg, harnessClient(t, h), fixtureCoordinator{})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func reviewPricePointer(value int64) *int64 { return &value }

func reviewPriceProduct(currency string, override *int64) commerce.CommerceProduct {
	variant := p17Variant("blue", "PAP-BLUE", 4, override)
	variant.PriceUSDMinor = reviewPricePointer(12345)
	if currency == "USD" {
		variant.PriceEGPMinor, variant.PriceUSDMinor = reviewPricePointer(12345), override
	}
	product := p17Product("019c0000-0000-7000-8000-000000000017", variant)
	product.SKU = variant.SKU
	product.Prices = append(product.Prices, commerce.Money{Currency: "USD", AmountMinor: 1300})
	return product
}

func reviewPriceRequest(product commerce.CommerceProduct) commerce.ProductUpsertRequest {
	return commerce.ProductUpsertRequest{
		ProviderKey: "shopify-main", ProductID: product.ProductID,
		Product: product, Published: true, CatalogRevision: 12, PolicyRevision: 4, OperationKey: "review-effective-price",
	}
}
