package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// ShopifyConfig is the optional second commerce adapter configuration
// (Phase 11). Shopify is a concrete CommerceProvider instance like
// WooCommerce: provider identity is the configured logical key
// (for example "shopify-main"), never the vendor name. All values come
// from environment configuration; credentials are runtime-only and are
// never persisted, logged, or echoed in errors.
//
// Construction performs no network I/O: configuration is validated
// locally and Cloud startup must not contact Shopify.
type ShopifyConfig struct {
	Enabled       bool
	ProviderKey   string
	ShopDomain    string
	APIVersion    string
	AccessToken   string // runtime secret
	ClientSecret  string // runtime secret (webhook HMAC), distinct from the token
	Currency      string
	LocationID    string // gid://shopify/Location/...
	PublicationID string // gid://shopify/Publication/...
	HTTPTimeout   time.Duration
	OrdersEnabled bool
}

// Supported Shopify currencies for provider-facing prices. The
// configured currency selects exactly one MoonLight price; there is no
// fallback to another currency.
const (
	ShopifyCurrencyEGP = "EGP"
	ShopifyCurrencyUSD = "USD"
)

// Shopify GraphQL transport bounds.
const (
	DefaultShopifyHTTPTimeout = 15 * time.Second
	MinShopifyHTTPTimeout     = 1 * time.Second
	MaxShopifyHTTPTimeout     = 120 * time.Second
)

// shopDomainPattern accepts a canonical Shopify Admin shop domain:
// a single host label chain under myshopify.com. No scheme, userinfo,
// port, path, query, fragment, whitespace, or control characters.
var shopDomainPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,61}[a-z0-9](\.[a-z0-9][a-z0-9-]{0,61}[a-z0-9])*\.myshopify\.com$`)

// shopifyGIDPattern accepts canonical Shopify GraphQL IDs of the form
// gid://shopify/<Type>/<decimal>. Type and digits are checked strictly
// by parse helpers; this pattern rejects structural garbage early.
var shopifyGIDPattern = regexp.MustCompile(`^gid://shopify/[A-Za-z]+/[0-9]+$`)

func loadShopifyConfig() (ShopifyConfig, error) {
	enabled, err := parseBoolFlag("COMMERCE_SHOPIFY_ENABLED")
	if err != nil {
		return ShopifyConfig{}, err
	}
	cfg := ShopifyConfig{Enabled: enabled}
	if !enabled {
		if orders, err := parseBoolFlag("COMMERCE_SHOPIFY_ORDERS_ENABLED"); err != nil {
			return ShopifyConfig{}, err
		} else if orders {
			return ShopifyConfig{}, fmt.Errorf("COMMERCE_SHOPIFY_ORDERS_ENABLED requires COMMERCE_SHOPIFY_ENABLED")
		}
		return cfg, nil
	}
	cfg.ProviderKey = strings.TrimSpace(os.Getenv("COMMERCE_SHOPIFY_PROVIDER_KEY"))
	cfg.ShopDomain = strings.ToLower(strings.TrimSpace(os.Getenv("COMMERCE_SHOPIFY_SHOP_DOMAIN")))
	cfg.APIVersion = strings.TrimSpace(os.Getenv("COMMERCE_SHOPIFY_API_VERSION"))
	cfg.AccessToken = strings.TrimSpace(os.Getenv("COMMERCE_SHOPIFY_ACCESS_TOKEN"))
	cfg.ClientSecret = strings.TrimSpace(os.Getenv("COMMERCE_SHOPIFY_CLIENT_SECRET"))
	cfg.Currency = strings.ToUpper(strings.TrimSpace(os.Getenv("COMMERCE_SHOPIFY_CURRENCY")))
	cfg.LocationID = strings.TrimSpace(os.Getenv("COMMERCE_SHOPIFY_LOCATION_ID"))
	cfg.PublicationID = strings.TrimSpace(os.Getenv("COMMERCE_SHOPIFY_PUBLICATION_ID"))
	cfg.OrdersEnabled, err = parseBoolFlag("COMMERCE_SHOPIFY_ORDERS_ENABLED")
	if err != nil {
		return ShopifyConfig{}, err
	}
	cfg.HTTPTimeout = DefaultShopifyHTTPTimeout
	if v := strings.TrimSpace(os.Getenv("COMMERCE_SHOPIFY_HTTP_TIMEOUT")); v != "" {
		timeout, err := time.ParseDuration(v)
		if err != nil {
			return ShopifyConfig{}, fmt.Errorf("invalid COMMERCE_SHOPIFY_HTTP_TIMEOUT %q: %w", v, err)
		}
		cfg.HTTPTimeout = timeout
	}
	if err := cfg.validate(); err != nil {
		return ShopifyConfig{}, err
	}
	return cfg, nil
}

func (c ShopifyConfig) validate() error {
	if !validShopifyProviderKey(c.ProviderKey) {
		return fmt.Errorf("invalid COMMERCE_SHOPIFY_PROVIDER_KEY: use 1..64 lowercase letters, numbers, hyphen, underscore")
	}
	if !shopDomainPattern.MatchString(c.ShopDomain) {
		return fmt.Errorf("invalid COMMERCE_SHOPIFY_SHOP_DOMAIN: expected a canonical <shop>.myshopify.com domain")
	}
	if len(c.ShopDomain) > 253 {
		return fmt.Errorf("invalid COMMERCE_SHOPIFY_SHOP_DOMAIN: expected a canonical <shop>.myshopify.com domain")
	}
	if c.APIVersion != SupportedShopifyAPIVersion {
		return fmt.Errorf("invalid COMMERCE_SHOPIFY_API_VERSION: expected supported stable release")
	}
	if c.AccessToken == "" {
		return fmt.Errorf("COMMERCE_SHOPIFY_ACCESS_TOKEN is required")
	}
	if c.Currency != ShopifyCurrencyEGP && c.Currency != ShopifyCurrencyUSD {
		return fmt.Errorf("unsupported COMMERCE_SHOPIFY_CURRENCY %q", c.Currency)
	}
	if !isShopifyGID(c.LocationID, "Location") {
		return fmt.Errorf("invalid COMMERCE_SHOPIFY_LOCATION_ID: expected gid://shopify/Location/<id>")
	}
	if !isShopifyGID(c.PublicationID, "Publication") {
		return fmt.Errorf("invalid COMMERCE_SHOPIFY_PUBLICATION_ID: expected gid://shopify/Publication/<id>")
	}
	if c.HTTPTimeout < MinShopifyHTTPTimeout || c.HTTPTimeout > MaxShopifyHTTPTimeout {
		return fmt.Errorf("COMMERCE_SHOPIFY_HTTP_TIMEOUT out of range")
	}
	if c.OrdersEnabled {
		if c.ClientSecret == "" {
			return fmt.Errorf("COMMERCE_SHOPIFY_CLIENT_SECRET is required when orders are enabled")
		}
		if c.ClientSecret == c.AccessToken {
			return fmt.Errorf("COMMERCE_SHOPIFY_CLIENT_SECRET must differ from the access token")
		}
	}
	return nil
}

// isShopifyGID reports whether value is a canonical GID of the expected
// resource type with a decimal numeric suffix. This prevents resource
// type confusion (a Variant GID is never a Product GID).
func isShopifyGID(value, wantType string) bool {
	if !shopifyGIDPattern.MatchString(value) {
		return false
	}
	rest := strings.TrimPrefix(value, "gid://shopify/")
	parts := strings.SplitN(rest, "/", 2)
	return parts[0] == wantType && parts[1] != "" && !strings.HasPrefix(parts[1], "0")
}

func validShopifyProviderKey(key string) bool {
	if len(key) == 0 || len(key) > 64 {
		return false
	}
	for i, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case (r == '-' || r == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}

// validateCommerceProviderKeys refuses one logical provider key shared
// by two provider instances: the registry key namespaces all durable
// mappings and order identity.
func validateCommerceProviderKeys(woo WooCommerceConfig, shopifyCfg ShopifyConfig) error {
	if woo.Enabled && shopifyCfg.Enabled && woo.ProviderKey == shopifyCfg.ProviderKey {
		return fmt.Errorf("commerce provider key %q is configured for both woocommerce and shopify", woo.ProviderKey)
	}
	return nil
}

// GraphQLEndpoint constructs the Admin GraphQL endpoint from the
// configured shop domain and pinned API version. The endpoint is always
// https; no caller supplies a base URL.
func (c ShopifyConfig) GraphQLEndpoint() string {
	return "https://" + c.ShopDomain + "/admin/api/" + c.APIVersion + "/graphql.json"
}

// NormalizedShopDomain returns the canonical comparison form for
// webhook shop-domain validation.
func (c ShopifyConfig) NormalizedShopDomain() string {
	return strings.ToLower(strings.TrimSpace(c.ShopDomain))
}

// SupportedShopifyAPIVersion is the release whose request contracts are verified.
const SupportedShopifyAPIVersion = "2026-10"
