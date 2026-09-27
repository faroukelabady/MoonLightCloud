package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// WooCommerce adapter runtime configuration (Phase 6B). Credentials are
// Cloud-only runtime secrets: never persisted, never logged, never sent
// to Retail. Disabled by default: an unconfigured adapter registers no
// provider and requires nothing.
type WooCommerceConfig struct {
	// Enabled turns the WooCommerce provider on. Default false.
	Enabled bool
	// ProviderKey is the generic logical instance key (for example
	// "website"). Never required to be "woocommerce".
	ProviderKey string
	// BaseURL is the store origin, https only, no userinfo/query/fragment.
	BaseURL string
	// ConsumerKey and ConsumerSecret are the Woo REST API credentials
	// (HTTP Basic Auth username/password). Handled as secrets.
	ConsumerKey    string
	ConsumerSecret string
	// Currency selects which MoonLight price becomes regular_price.
	// Supported: EGP, USD.
	Currency string
	// DimensionUnit is the Woo store dimension unit. Only cm in 6B.
	DimensionUnit string
	// HTTPTimeout bounds every Woo request. Default 15s.
	HTTPTimeout time.Duration
}

// Supported Woo adapter values.
const (
	WooCurrencyEGP = "EGP"
	WooCurrencyUSD = "USD"

	WooDimensionCM = "cm"

	DefaultWooHTTPTimeout = 15 * time.Second
	MinWooHTTPTimeout     = 1 * time.Second
	MaxWooHTTPTimeout     = 120 * time.Second
)

// loadWooCommerceConfig reads COMMERCE_WOO_* environment variables.
// Disabled (default) requires nothing and ignores the rest.
func loadWooCommerceConfig() (WooCommerceConfig, error) {
	enabled, err := parseBoolFlag("COMMERCE_WOO_ENABLED")
	if err != nil {
		return WooCommerceConfig{}, err
	}
	cfg := WooCommerceConfig{Enabled: enabled}
	if !enabled {
		return cfg, nil
	}
	cfg.ProviderKey = strings.TrimSpace(os.Getenv("COMMERCE_WOO_PROVIDER_KEY"))
	cfg.BaseURL = strings.TrimSpace(os.Getenv("COMMERCE_WOO_BASE_URL"))
	cfg.ConsumerKey = strings.TrimSpace(os.Getenv("COMMERCE_WOO_CONSUMER_KEY"))
	cfg.ConsumerSecret = strings.TrimSpace(os.Getenv("COMMERCE_WOO_CONSUMER_SECRET"))
	cfg.Currency = strings.ToUpper(strings.TrimSpace(os.Getenv("COMMERCE_WOO_CURRENCY")))
	cfg.DimensionUnit = strings.ToLower(strings.TrimSpace(os.Getenv("COMMERCE_WOO_DIMENSION_UNIT")))
	cfg.HTTPTimeout = DefaultWooHTTPTimeout
	if v := strings.TrimSpace(os.Getenv("COMMERCE_WOO_HTTP_TIMEOUT")); v != "" {
		timeout, err := time.ParseDuration(v)
		if err != nil {
			return WooCommerceConfig{}, fmt.Errorf("invalid COMMERCE_WOO_HTTP_TIMEOUT %q: %w", v, err)
		}
		cfg.HTTPTimeout = timeout
	}
	if err := cfg.validate(); err != nil {
		return WooCommerceConfig{}, err
	}
	return cfg, nil
}

// validate checks a complete enabled configuration eagerly so operator
// mistakes fail startup/config loading, never the first product sync.
func (c WooCommerceConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	if !validWooProviderKey(c.ProviderKey) {
		return fmt.Errorf("invalid COMMERCE_WOO_PROVIDER_KEY %q: use 1..64 lowercase letters, numbers, hyphen, underscore", c.ProviderKey)
	}
	if err := validateWooBaseURL(c.BaseURL); err != nil {
		return err
	}
	if c.ConsumerKey == "" {
		return fmt.Errorf("COMMERCE_WOO_CONSUMER_KEY is required when WooCommerce is enabled")
	}
	if c.ConsumerSecret == "" {
		return fmt.Errorf("COMMERCE_WOO_CONSUMER_SECRET is required when WooCommerce is enabled")
	}
	if c.Currency != WooCurrencyEGP && c.Currency != WooCurrencyUSD {
		return fmt.Errorf("invalid COMMERCE_WOO_CURRENCY %q: want EGP|USD", c.Currency)
	}
	if c.DimensionUnit != WooDimensionCM {
		return fmt.Errorf("invalid COMMERCE_WOO_DIMENSION_UNIT %q: only cm is supported", c.DimensionUnit)
	}
	if c.HTTPTimeout < MinWooHTTPTimeout || c.HTTPTimeout > MaxWooHTTPTimeout {
		return fmt.Errorf("COMMERCE_WOO_HTTP_TIMEOUT must be within [%s, %s]", MinWooHTTPTimeout, MaxWooHTTPTimeout)
	}
	return nil
}

func validWooProviderKey(key string) bool {
	if len(key) == 0 || len(key) > 64 {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		first := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		rest := first || c == '-' || c == '_'
		if i == 0 && !first {
			return false
		}
		if i > 0 && !rest {
			return false
		}
	}
	return true
}

// validateWooBaseURL enforces https origins without userinfo, query, or
// fragment. Basic Auth credentials must never travel over HTTP.
func validateWooBaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("invalid COMMERCE_WOO_BASE_URL %q: want https://host[/subpath]", raw)
	}
	if parsed.User != nil {
		return fmt.Errorf("invalid COMMERCE_WOO_BASE_URL: userinfo is forbidden")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("invalid COMMERCE_WOO_BASE_URL: query and fragment are forbidden")
	}
	return nil
}

// NormalizedBaseURL returns the origin without a trailing slash.
func (c WooCommerceConfig) NormalizedBaseURL() string {
	return strings.TrimSuffix(c.BaseURL, "/")
}
