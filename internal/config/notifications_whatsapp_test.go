package config

import (
	"strings"
	"testing"
)

func setWhatsAppEnv(t *testing.T, values map[string]string) {
	t.Helper()
	keys := []string{
		"NOTIFICATIONS_WHATSAPP_ENABLED", "NOTIFICATIONS_WHATSAPP_PROVIDER_KEY",
		"NOTIFICATIONS_WHATSAPP_GRAPH_VERSION", "NOTIFICATIONS_WHATSAPP_BASE_URL",
		"NOTIFICATIONS_WHATSAPP_PHONE_NUMBER_ID", "NOTIFICATIONS_WHATSAPP_ACCESS_TOKEN",
		"NOTIFICATIONS_WHATSAPP_APP_SECRET", "NOTIFICATIONS_WHATSAPP_WEBHOOK_VERIFY_TOKEN",
		"NOTIFICATIONS_WHATSAPP_HTTP_TIMEOUT",
	}
	for _, key := range keys {
		t.Setenv(key, "")
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
}

func validWhatsAppEnv() map[string]string {
	return map[string]string{
		"NOTIFICATIONS_WHATSAPP_ENABLED":              "true",
		"NOTIFICATIONS_WHATSAPP_PROVIDER_KEY":         "whatsapp-main",
		"NOTIFICATIONS_WHATSAPP_GRAPH_VERSION":        "v25.0",
		"NOTIFICATIONS_WHATSAPP_BASE_URL":             "https://graph.facebook.com",
		"NOTIFICATIONS_WHATSAPP_PHONE_NUMBER_ID":      "106540352242922",
		"NOTIFICATIONS_WHATSAPP_ACCESS_TOKEN":         "EAATestAccessToken",
		"NOTIFICATIONS_WHATSAPP_APP_SECRET":           "test-app-secret",
		"NOTIFICATIONS_WHATSAPP_WEBHOOK_VERIFY_TOKEN": "test-verify-token",
	}
}

func TestWhatsAppNotificationConfig(t *testing.T) {
	t.Run("disabled requires nothing", func(t *testing.T) {
		setWhatsAppEnv(t, map[string]string{})
		cfg, err := loadWhatsAppNotificationConfig()
		if err != nil {
			t.Fatalf("disabled: %v", err)
		}
		if cfg.Enabled {
			t.Fatal("disabled by default")
		}
	})
	t.Run("valid enabled", func(t *testing.T) {
		setWhatsAppEnv(t, validWhatsAppEnv())
		cfg, err := loadWhatsAppNotificationConfig()
		if err != nil {
			t.Fatalf("valid: %v", err)
		}
		if !cfg.Enabled || cfg.ProviderKey != "whatsapp-main" || cfg.GraphVersion != "v25.0" {
			t.Fatalf("config: %+v", cfg.Enabled)
		}
		if cfg.NormalizedBaseURL() != "https://graph.facebook.com" {
			t.Fatalf("base: %q", cfg.NormalizedBaseURL())
		}
	})
	t.Run("graph version syntax", func(t *testing.T) {
		for _, version := range []string{"", "25.0", "v25", "latest", "v25.0/extra"} {
			env := validWhatsAppEnv()
			env["NOTIFICATIONS_WHATSAPP_GRAPH_VERSION"] = version
			setWhatsAppEnv(t, env)
			if _, err := loadWhatsAppNotificationConfig(); err == nil {
				t.Fatalf("version %q must fail", version)
			}
		}
	})
	t.Run("missing credentials rejected", func(t *testing.T) {
		for _, key := range []string{
			"NOTIFICATIONS_WHATSAPP_PHONE_NUMBER_ID",
			"NOTIFICATIONS_WHATSAPP_ACCESS_TOKEN",
			"NOTIFICATIONS_WHATSAPP_APP_SECRET",
			"NOTIFICATIONS_WHATSAPP_WEBHOOK_VERIFY_TOKEN",
		} {
			env := validWhatsAppEnv()
			env[key] = ""
			setWhatsAppEnv(t, env)
			if _, err := loadWhatsAppNotificationConfig(); err == nil {
				t.Fatalf("missing %s must fail", key)
			}
		}
	})
	t.Run("insecure base rejected without value leak", func(t *testing.T) {
		for _, raw := range []string{
			"http://graph.facebook.com",
			"https://user:EAATestAccessToken@graph.facebook.com",
			"https://graph.facebook.com?token=EAATestAccessToken",
		} {
			env := validWhatsAppEnv()
			env["NOTIFICATIONS_WHATSAPP_BASE_URL"] = raw
			setWhatsAppEnv(t, env)
			_, err := loadWhatsAppNotificationConfig()
			if err == nil {
				t.Fatalf("base %q must fail", raw)
			}
			if strings.Contains(err.Error(), "EAATestAccessToken") {
				t.Fatalf("value leaked: %q", err.Error())
			}
		}
	})
	t.Run("invalid phone id", func(t *testing.T) {
		env := validWhatsAppEnv()
		env["NOTIFICATIONS_WHATSAPP_PHONE_NUMBER_ID"] = "recipient-phone"
		setWhatsAppEnv(t, env)
		if _, err := loadWhatsAppNotificationConfig(); err == nil {
			t.Fatal("phone id must be numeric")
		}
	})
}
