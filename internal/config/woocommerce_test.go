package config

import (
	"strings"
	"testing"
	"time"
)

func setWooEnv(t *testing.T, values map[string]string) {
	t.Helper()
	keys := []string{
		"COMMERCE_WOO_ENABLED", "COMMERCE_WOO_PROVIDER_KEY", "COMMERCE_WOO_BASE_URL",
		"COMMERCE_WOO_CONSUMER_KEY", "COMMERCE_WOO_CONSUMER_SECRET",
		"COMMERCE_WOO_CURRENCY", "COMMERCE_WOO_DIMENSION_UNIT", "COMMERCE_WOO_HTTP_TIMEOUT",
	}
	for _, key := range keys {
		t.Setenv(key, "")
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
}

func validWooEnv() map[string]string {
	return map[string]string{
		"COMMERCE_WOO_ENABLED":         "true",
		"COMMERCE_WOO_PROVIDER_KEY":    "website",
		"COMMERCE_WOO_BASE_URL":        "https://store.example.com/shop",
		"COMMERCE_WOO_CONSUMER_KEY":    "ck_test",
		"COMMERCE_WOO_CONSUMER_SECRET": "cs_test",
		"COMMERCE_WOO_CURRENCY":        "EGP",
		"COMMERCE_WOO_DIMENSION_UNIT":  "cm",
	}
}

func withWooEnv(override map[string]string) map[string]string {
	env := validWooEnv()
	for key, value := range override {
		env[key] = value
	}
	return env
}

func TestWooCommerceConfigMatrix(t *testing.T) {
	t.Run("disabled with no secrets is valid", func(t *testing.T) {
		setWooEnv(t, map[string]string{})
		cfg, err := loadWooCommerceConfig()
		if err != nil {
			t.Fatalf("disabled: %v", err)
		}
		if cfg.Enabled || cfg.HTTPTimeout != 0 {
			t.Fatalf("disabled defaults: %+v", cfg)
		}
	})

	t.Run("enabled complete is valid", func(t *testing.T) {
		setWooEnv(t, validWooEnv())
		cfg, err := loadWooCommerceConfig()
		if err != nil {
			t.Fatalf("enabled: %v", err)
		}
		if !cfg.Enabled || cfg.ProviderKey != "website" || cfg.Currency != "EGP" ||
			cfg.DimensionUnit != "cm" || cfg.HTTPTimeout != DefaultWooHTTPTimeout {
			t.Fatalf("config: %+v", cfg)
		}
		if cfg.NormalizedBaseURL() != "https://store.example.com/shop" {
			t.Fatalf("normalized: %q", cfg.NormalizedBaseURL())
		}
		setWooEnv(t, map[string]string{
			"COMMERCE_WOO_ENABLED": "true", "COMMERCE_WOO_PROVIDER_KEY": "website",
			"COMMERCE_WOO_BASE_URL":     "https://store.example.com/",
			"COMMERCE_WOO_CONSUMER_KEY": "ck", "COMMERCE_WOO_CONSUMER_SECRET": "cs",
			"COMMERCE_WOO_CURRENCY": "usd", "COMMERCE_WOO_DIMENSION_UNIT": "CM",
			"COMMERCE_WOO_HTTP_TIMEOUT": "30s",
		})
		cfg, err = loadWooCommerceConfig()
		if err != nil {
			t.Fatalf("normalized values: %v", err)
		}
		if cfg.Currency != "USD" || cfg.DimensionUnit != "cm" || cfg.HTTPTimeout != 30*time.Second {
			t.Fatalf("normalization: %+v", cfg)
		}
		if cfg.NormalizedBaseURL() != "https://store.example.com" {
			t.Fatalf("trailing slash: %q", cfg.NormalizedBaseURL())
		}
	})

	invalid := map[string]map[string]string{
		"enabled missing key":        withWooEnv(map[string]string{"COMMERCE_WOO_PROVIDER_KEY": ""}),
		"enabled missing secret":     withWooEnv(map[string]string{"COMMERCE_WOO_CONSUMER_SECRET": ""}),
		"http URL":                   withWooEnv(map[string]string{"COMMERCE_WOO_BASE_URL": "http://store.example.com"}),
		"URL with userinfo":          withWooEnv(map[string]string{"COMMERCE_WOO_BASE_URL": "https://user@store.example.com"}),
		"URL with query":             withWooEnv(map[string]string{"COMMERCE_WOO_BASE_URL": "https://store.example.com/?x=1"}),
		"URL with fragment":          withWooEnv(map[string]string{"COMMERCE_WOO_BASE_URL": "https://store.example.com/#x"}),
		"URL empty host":             withWooEnv(map[string]string{"COMMERCE_WOO_BASE_URL": "https://"}),
		"invalid ProviderKey":        withWooEnv(map[string]string{"COMMERCE_WOO_PROVIDER_KEY": "Woo Commerce!"}),
		"unsupported currency":       withWooEnv(map[string]string{"COMMERCE_WOO_CURRENCY": "EUR"}),
		"unsupported dimension unit": withWooEnv(map[string]string{"COMMERCE_WOO_DIMENSION_UNIT": "in"}),
		"zero timeout":               withWooEnv(map[string]string{"COMMERCE_WOO_HTTP_TIMEOUT": "0s"}),
		"negative timeout":           withWooEnv(map[string]string{"COMMERCE_WOO_HTTP_TIMEOUT": "-5s"}),
		"huge timeout":               withWooEnv(map[string]string{"COMMERCE_WOO_HTTP_TIMEOUT": "1h"}),
		"bad timeout":                withWooEnv(map[string]string{"COMMERCE_WOO_HTTP_TIMEOUT": "soon"}),
	}
	for name, env := range invalid {
		t.Run(name, func(t *testing.T) {
			setWooEnv(t, env)
			if _, err := loadWooCommerceConfig(); err == nil {
				t.Fatalf("%s must fail", name)
			}
		})
	}
}

func TestWooBaseURLRejectsCredentialMaterial(t *testing.T) {
	cases := []string{
		"http://ck-review:sk-review@example.com",
		"https://ck-review:sk-review@example.com",
		"http://example.com/?consumer_secret=sk-review",
		"https://example.com/?consumer_key=ck-review",
		"https://example.com/#sk-review",
		"http://%zz",
		"https://user:pass@example.com/shop",
	}
	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			setWooEnv(t, withWooEnv(map[string]string{"COMMERCE_WOO_BASE_URL": raw}))
			_, err := loadWooCommerceConfig()
			if err == nil {
				t.Fatalf("%q must fail", raw)
			}
			for _, forbidden := range []string{raw, "ck-review", "sk-review"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("error echoes %q: %q", forbidden, err.Error())
				}
			}
		})
	}
}

func TestWooOrdersConfig(t *testing.T) {
	setOrdersEnv := func(t *testing.T, values map[string]string) {
		t.Helper()
		t.Setenv("COMMERCE_WOO_ORDERS_ENABLED", "")
		t.Setenv("COMMERCE_WOO_WEBHOOK_SECRET", "")
		for key, value := range values {
			t.Setenv(key, value)
		}
	}
	t.Run("orders require woo enabled", func(t *testing.T) {
		setWooEnv(t, map[string]string{})
		setOrdersEnv(t, map[string]string{"COMMERCE_WOO_ORDERS_ENABLED": "true"})
		if _, err := loadWooCommerceConfig(); err == nil {
			t.Fatal("orders without woo must fail")
		}
	})
	t.Run("orders need webhook secret", func(t *testing.T) {
		setWooEnv(t, validWooEnv())
		setOrdersEnv(t, map[string]string{"COMMERCE_WOO_ORDERS_ENABLED": "true"})
		if _, err := loadWooCommerceConfig(); err == nil {
			t.Fatal("missing webhook secret must fail")
		}
	})
	t.Run("short webhook secret rejected", func(t *testing.T) {
		setWooEnv(t, validWooEnv())
		setOrdersEnv(t, map[string]string{
			"COMMERCE_WOO_ORDERS_ENABLED": "true",
			"COMMERCE_WOO_WEBHOOK_SECRET": "too-short",
		})
		if _, err := loadWooCommerceConfig(); err == nil {
			t.Fatal("short secret must fail")
		}
	})
	t.Run("orders enabled valid", func(t *testing.T) {
		setWooEnv(t, validWooEnv())
		setOrdersEnv(t, map[string]string{
			"COMMERCE_WOO_ORDERS_ENABLED": "true",
			"COMMERCE_WOO_WEBHOOK_SECRET": "0123456789abcdef0123456789abcdef",
		})
		cfg, err := loadWooCommerceConfig()
		if err != nil {
			t.Fatalf("valid orders config: %v", err)
		}
		if !cfg.OrdersEnabled || cfg.WebhookSecret == "" {
			t.Fatalf("orders config: %+v", cfg.Enabled)
		}
	})
	t.Run("disabled ignores orders secret", func(t *testing.T) {
		setWooEnv(t, map[string]string{})
		setOrdersEnv(t, map[string]string{"COMMERCE_WOO_WEBHOOK_SECRET": "whatever"})
		cfg, err := loadWooCommerceConfig()
		if err != nil {
			t.Fatalf("disabled: %v", err)
		}
		if cfg.OrdersEnabled || cfg.WebhookSecret != "" {
			t.Fatalf("disabled ignores orders: %+v", cfg)
		}
	})
}
