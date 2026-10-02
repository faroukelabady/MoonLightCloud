package config

import (
	"strings"
	"testing"
)

// Phase 11 §146: Shopify configuration test matrix.

func setShopifyEnv(t *testing.T, env map[string]string) {
	t.Helper()
	keys := []string{
		"COMMERCE_SHOPIFY_ENABLED", "COMMERCE_SHOPIFY_PROVIDER_KEY",
		"COMMERCE_SHOPIFY_SHOP_DOMAIN", "COMMERCE_SHOPIFY_API_VERSION",
		"COMMERCE_SHOPIFY_ACCESS_TOKEN", "COMMERCE_SHOPIFY_CLIENT_SECRET",
		"COMMERCE_SHOPIFY_CURRENCY", "COMMERCE_SHOPIFY_LOCATION_ID",
		"COMMERCE_SHOPIFY_PUBLICATION_ID", "COMMERCE_SHOPIFY_HTTP_TIMEOUT",
		"COMMERCE_SHOPIFY_ORDERS_ENABLED",
	}
	for _, key := range keys {
		t.Setenv(key, "")
	}
	for key, value := range env {
		t.Setenv(key, value)
	}
}

func validShopifyEnv() map[string]string {
	return map[string]string{
		"COMMERCE_SHOPIFY_ENABLED":        "true",
		"COMMERCE_SHOPIFY_PROVIDER_KEY":   "shopify-main",
		"COMMERCE_SHOPIFY_SHOP_DOMAIN":    "example.myshopify.com",
		"COMMERCE_SHOPIFY_API_VERSION":    "2026-10",
		"COMMERCE_SHOPIFY_ACCESS_TOKEN":   "shpat_0123456789abcdef",
		"COMMERCE_SHOPIFY_CURRENCY":       "EGP",
		"COMMERCE_SHOPIFY_LOCATION_ID":    "gid://shopify/Location/1234567890",
		"COMMERCE_SHOPIFY_PUBLICATION_ID": "gid://shopify/Publication/2234567890",
		"COMMERCE_SHOPIFY_HTTP_TIMEOUT":   "15s",
	}
}

func TestShopifyConfigMatrix(t *testing.T) {
	t.Run("disabled valid", func(t *testing.T) {
		setShopifyEnv(t, map[string]string{})
		if _, err := loadShopifyConfig(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("enabled complete config", func(t *testing.T) {
		setShopifyEnv(t, validShopifyEnv())
		cfg, err := loadShopifyConfig()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.ProviderKey != "shopify-main" || cfg.Currency != "EGP" {
			t.Fatalf("cfg = %+v", cfg)
		}
		endpoint := cfg.GraphQLEndpoint()
		if endpoint != "https://example.myshopify.com/admin/api/2026-10/graphql.json" {
			t.Fatalf("endpoint = %s", endpoint)
		}
	})

	t.Run("orders enabled with client secret", func(t *testing.T) {
		env := validShopifyEnv()
		env["COMMERCE_SHOPIFY_ORDERS_ENABLED"] = "true"
		env["COMMERCE_SHOPIFY_CLIENT_SECRET"] = "shhh_0123456789abcdef"
		setShopifyEnv(t, env)
		cfg, err := loadShopifyConfig()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.OrdersEnabled {
			t.Fatal("orders not enabled")
		}
	})

	invalid := map[string]map[string]string{}
	clone := func(mutate func(map[string]string)) map[string]string {
		env := validShopifyEnv()
		mutate(env)
		return env
	}
	invalid["invalid shop domain"] = clone(func(e map[string]string) {
		e["COMMERCE_SHOPIFY_SHOP_DOMAIN"] = "https://example.myshopify.com"
	})
	invalid["foreign host"] = clone(func(e map[string]string) {
		e["COMMERCE_SHOPIFY_SHOP_DOMAIN"] = "evil.example.com"
	})
	invalid["http endpoint host"] = clone(func(e map[string]string) {
		e["COMMERCE_SHOPIFY_SHOP_DOMAIN"] = "example.myshopify.com:8080"
	})
	invalid["missing token"] = clone(func(e map[string]string) {
		e["COMMERCE_SHOPIFY_ACCESS_TOKEN"] = ""
	})
	invalid["missing api version"] = clone(func(e map[string]string) {
		e["COMMERCE_SHOPIFY_API_VERSION"] = ""
	})
	invalid["latest api version"] = clone(func(e map[string]string) {
		e["COMMERCE_SHOPIFY_API_VERSION"] = "latest"
	})
	invalid["unstable api version"] = clone(func(e map[string]string) {
		e["COMMERCE_SHOPIFY_API_VERSION"] = "unstable"
	})
	invalid["malformed api version"] = clone(func(e map[string]string) {
		e["COMMERCE_SHOPIFY_API_VERSION"] = "2026-10-01"
	})
	invalid["missing location"] = clone(func(e map[string]string) {
		e["COMMERCE_SHOPIFY_LOCATION_ID"] = ""
	})
	invalid["invalid location gid"] = clone(func(e map[string]string) {
		e["COMMERCE_SHOPIFY_LOCATION_ID"] = "gid://shopify/Publication/1234567890"
	})
	invalid["missing publication"] = clone(func(e map[string]string) {
		e["COMMERCE_SHOPIFY_PUBLICATION_ID"] = ""
	})
	invalid["invalid publication gid"] = clone(func(e map[string]string) {
		e["COMMERCE_SHOPIFY_PUBLICATION_ID"] = "1234567890"
	})
	invalid["unsupported currency"] = clone(func(e map[string]string) {
		e["COMMERCE_SHOPIFY_CURRENCY"] = "JPY"
	})
	invalid["timeout too small"] = clone(func(e map[string]string) {
		e["COMMERCE_SHOPIFY_HTTP_TIMEOUT"] = "10ms"
	})
	invalid["timeout too large"] = clone(func(e map[string]string) {
		e["COMMERCE_SHOPIFY_HTTP_TIMEOUT"] = "10m"
	})
	invalid["orders enabled missing client secret"] = clone(func(e map[string]string) {
		e["COMMERCE_SHOPIFY_ORDERS_ENABLED"] = "true"
		e["COMMERCE_SHOPIFY_CLIENT_SECRET"] = ""
	})
	invalid["orders enabled without provider"] = map[string]string{
		"COMMERCE_SHOPIFY_ORDERS_ENABLED": "true",
	}
	invalid["bad provider key"] = clone(func(e map[string]string) {
		e["COMMERCE_SHOPIFY_PROVIDER_KEY"] = "Shopify Main!"
	})

	for name, env := range invalid {
		t.Run(name, func(t *testing.T) {
			setShopifyEnv(t, env)
			if _, err := loadShopifyConfig(); err == nil {
				t.Fatal("accepted invalid configuration")
			}
		})
	}
}

// Phase 11 §128/§146: Woo + Shopify coexist; duplicate provider keys
// are refused.
func TestWooShopifyProviderKeyCollision(t *testing.T) {
	setShopifyEnv(t, validShopifyEnv())
	t.Setenv("COMMERCE_WOO_ENABLED", "true")
	t.Setenv("COMMERCE_WOO_PROVIDER_KEY", "shopify-main")
	t.Setenv("COMMERCE_WOO_BASE_URL", "https://shop.example.com")
	t.Setenv("COMMERCE_WOO_CONSUMER_KEY", "ck")
	t.Setenv("COMMERCE_WOO_CONSUMER_SECRET", "cs")
	t.Setenv("COMMERCE_WOO_CURRENCY", "EGP")
	t.Setenv("COMMERCE_WOO_DIMENSION_UNIT", "cm")
	woo, err := loadWooCommerceConfig()
	if err != nil {
		t.Fatal(err)
	}
	shopifyCfg, err := loadShopifyConfig()
	if err != nil {
		t.Fatal(err)
	}
	err = validateCommerceProviderKeys(woo, shopifyCfg)
	if err == nil || !strings.Contains(err.Error(), "provider key") {
		t.Fatalf("duplicate provider key accepted: %v", err)
	}

	t.Setenv("COMMERCE_WOO_PROVIDER_KEY", "woo-main")
	woo, err = loadWooCommerceConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCommerceProviderKeys(woo, shopifyCfg); err != nil {
		t.Fatalf("woo + shopify must coexist: %v", err)
	}
}

func TestShopifyDisabledIgnoresOrdersFlag(t *testing.T) {
	setShopifyEnv(t, map[string]string{"COMMERCE_SHOPIFY_ORDERS_ENABLED": "true"})
	if _, err := loadShopifyConfig(); err == nil {
		t.Fatal("orders enabled without provider accepted")
	}
}
