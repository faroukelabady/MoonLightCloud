package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// DefaultExchangeRateURL is the free open.er-api.com USD endpoint that
// MoonLightRetail also uses (ADR-0058).
const DefaultExchangeRateURL = "https://open.er-api.com/v6/latest/USD"

// exchangeRateURL reads EXCHANGE_RATE_URL: absent means the default, and
// an empty value turns fetching off (rates are then entered by hand).
func exchangeRateURL() string {
	value, present := os.LookupEnv("EXCHANGE_RATE_URL")
	if !present {
		return DefaultExchangeRateURL
	}
	return strings.TrimSpace(value)
}

// validateExchangeRateURL allows plain http only in development, where
// tests and local mocks run.
func validateExchangeRateURL(raw, environment string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("EXCHANGE_RATE_URL must be an absolute https URL (or empty to turn fetching off)")
	}
	if u.Scheme != "https" && environment != EnvDevelopment {
		return fmt.Errorf("EXCHANGE_RATE_URL must use https outside development")
	}
	return nil
}
