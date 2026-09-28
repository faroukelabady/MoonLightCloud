package config

import (
	"strings"
	"testing"
)

func setBusinessReportsEnv(t *testing.T, values map[string]string) {
	t.Helper()
	keys := []string{
		"BUSINESS_REPORTS_ENABLED", "BUSINESS_REPORTS_POLL_INTERVAL",
		"BUSINESS_REPORTS_BATCH_SIZE", "BUSINESS_REPORTS_LEASE_DURATION",
	}
	for _, key := range keys {
		t.Setenv(key, "")
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
}

func TestBusinessReportsConfig(t *testing.T) {
	t.Run("disabled by default", func(t *testing.T) {
		setBusinessReportsEnv(t, map[string]string{})
		cfg, err := loadBusinessReports()
		if err != nil {
			t.Fatalf("disabled: %v", err)
		}
		if cfg.Enabled || cfg.PollInterval == 0 || cfg.BatchSize == 0 || cfg.LeaseDuration == 0 {
			t.Fatalf("defaults: %+v", cfg)
		}
	})
	t.Run("valid enabled", func(t *testing.T) {
		setBusinessReportsEnv(t, map[string]string{"BUSINESS_REPORTS_ENABLED": "true"})
		cfg, err := loadBusinessReports()
		if err != nil {
			t.Fatalf("valid: %v", err)
		}
		if !cfg.Enabled {
			t.Fatal("enabled")
		}
	})
	t.Run("invalid values value-free", func(t *testing.T) {
		for key, value := range map[string]string{
			"BUSINESS_REPORTS_ENABLED":        "maybe SECRET_VALUE",
			"BUSINESS_REPORTS_POLL_INTERVAL":  "SECRET_VALUE",
			"BUSINESS_REPORTS_BATCH_SIZE":     "SECRET_VALUE",
			"BUSINESS_REPORTS_LEASE_DURATION": "SECRET_VALUE",
		} {
			setBusinessReportsEnv(t, map[string]string{"BUSINESS_REPORTS_ENABLED": "true", key: value})
			if key == "BUSINESS_REPORTS_ENABLED" {
				setBusinessReportsEnv(t, map[string]string{key: value})
			}
			_, err := loadBusinessReports()
			if err == nil {
				t.Fatalf("%s must fail", key)
			}
			if got := err.Error(); got == "" {
				t.Fatal("empty error")
			} else {
				for _, forbidden := range []string{"SECRET_VALUE"} {
					if strings.Contains(got, forbidden) {
						t.Fatalf("%s leaked: %q", key, got)
					}
				}
				if key == "BUSINESS_REPORTS_ENABLED" && strings.Contains(got, "maybe") {
					t.Fatalf("bool value leaked: %q", got)
				}
			}
		}
	})
	t.Run("bounds enforced", func(t *testing.T) {
		setBusinessReportsEnv(t, map[string]string{
			"BUSINESS_REPORTS_ENABLED": "true", "BUSINESS_REPORTS_BATCH_SIZE": "1000",
		})
		if _, err := loadBusinessReports(); err == nil {
			t.Fatal("oversized batch must fail")
		}
	})
}
