package config

import "testing"

func TestExchangeRateURL(t *testing.T) {
	t.Setenv("EXCHANGE_RATE_URL", "")
	if got := exchangeRateURL(); got != "" {
		t.Fatalf("empty turns fetching off: %q", got)
	}
	t.Setenv("EXCHANGE_RATE_URL", " https://rates.example/v6/latest/USD ")
	if got := exchangeRateURL(); got != "https://rates.example/v6/latest/USD" {
		t.Fatalf("trimmed: %q", got)
	}
	for _, tc := range []struct {
		raw, env string
		ok       bool
	}{
		{"", EnvProduction, true},
		{DefaultExchangeRateURL, EnvProduction, true},
		{"http://127.0.0.1:9/usd", EnvDevelopment, true},
		{"http://rates.example/usd", EnvProduction, false},
		{"http://rates.example/usd", EnvStaging, false},
		{"rates.example/usd", EnvDevelopment, false},
		{"ftp://rates.example/usd", EnvDevelopment, false},
	} {
		if err := validateExchangeRateURL(tc.raw, tc.env); (err == nil) != tc.ok {
			t.Fatalf("%q in %s: %v", tc.raw, tc.env, err)
		}
	}
}

func TestExchangeRateURLDefaultsToOpenERAPI(t *testing.T) {
	unsetenv(t, "EXCHANGE_RATE_URL")
	if got := exchangeRateURL(); got != DefaultExchangeRateURL {
		t.Fatalf("absent: %q", got)
	}
}
