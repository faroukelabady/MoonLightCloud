package config

import (
	"strings"
	"testing"
	"time"
)

func setTelegramEnv(t *testing.T, values map[string]string) {
	t.Helper()
	keys := []string{
		"NOTIFICATIONS_TELEGRAM_ENABLED", "NOTIFICATIONS_TELEGRAM_PROVIDER_KEY",
		"NOTIFICATIONS_TELEGRAM_BOT_TOKEN", "NOTIFICATIONS_TELEGRAM_BASE_URL",
		"NOTIFICATIONS_TELEGRAM_HTTP_TIMEOUT",
	}
	for _, key := range keys {
		t.Setenv(key, "")
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
}

func validTelegramEnv() map[string]string {
	return map[string]string{
		"NOTIFICATIONS_TELEGRAM_ENABLED":      "true",
		"NOTIFICATIONS_TELEGRAM_PROVIDER_KEY": "telegram-main",
		"NOTIFICATIONS_TELEGRAM_BOT_TOKEN":    "123456:AAA-fake-test-only",
	}
}

func TestTelegramNotificationConfig(t *testing.T) {
	t.Run("disabled requires nothing", func(t *testing.T) {
		setTelegramEnv(t, map[string]string{})
		cfg, err := loadTelegramNotificationConfig()
		if err != nil {
			t.Fatalf("disabled: %v", err)
		}
		if cfg.Enabled {
			t.Fatal("disabled by default")
		}
	})
	t.Run("valid enabled", func(t *testing.T) {
		setTelegramEnv(t, validTelegramEnv())
		cfg, err := loadTelegramNotificationConfig()
		if err != nil {
			t.Fatalf("valid: %v", err)
		}
		if !cfg.Enabled || cfg.ProviderKey != "telegram-main" {
			t.Fatalf("config: %+v", cfg.Enabled)
		}
		if cfg.NormalizedBaseURL() != DefaultTelegramBaseURL {
			t.Fatalf("base: %q", cfg.NormalizedBaseURL())
		}
		if cfg.HTTPTimeout != DefaultTelegramHTTPTimeout {
			t.Fatalf("timeout: %v", cfg.HTTPTimeout)
		}
	})
	t.Run("missing key or token rejected", func(t *testing.T) {
		for _, key := range []string{
			"NOTIFICATIONS_TELEGRAM_PROVIDER_KEY",
			"NOTIFICATIONS_TELEGRAM_BOT_TOKEN",
		} {
			env := validTelegramEnv()
			env[key] = ""
			setTelegramEnv(t, env)
			if _, err := loadTelegramNotificationConfig(); err == nil {
				t.Fatalf("missing %s must fail", key)
			}
		}
	})
	t.Run("non-URL-safe token rejected without value leak", func(t *testing.T) {
		// F-02: only Bot API charset digits:word reaches URL
		// construction; everything else fails closed at startup.
		for _, token := range []string{
			"123:SECRET%zzTOKEN", "justastring", "abc:DEF123",
			"123:tok en", "123:tok?en", "123:tok#en", "123:tok/en",
		} {
			env := validTelegramEnv()
			env["NOTIFICATIONS_TELEGRAM_BOT_TOKEN"] = token
			setTelegramEnv(t, env)
			_, err := loadTelegramNotificationConfig()
			if err == nil {
				t.Fatalf("token %q must fail", token)
			}
			if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "tok ") {
				t.Fatalf("value leaked: %q", err.Error())
			}
		}
	})
	t.Run("insecure base rejected without value leak", func(t *testing.T) {
		for _, raw := range []string{
			"http://api.telegram.org",
			"https://user:123456@api.telegram.org",
			"https://api.telegram.org?token=123456",
			"https://api.telegram.org#frag",
		} {
			env := validTelegramEnv()
			env["NOTIFICATIONS_TELEGRAM_BASE_URL"] = raw
			setTelegramEnv(t, env)
			_, err := loadTelegramNotificationConfig()
			if err == nil {
				t.Fatalf("base %q must fail", raw)
			}
			if strings.Contains(err.Error(), "123456") {
				t.Fatalf("value leaked: %q", err.Error())
			}
		}
	})
	t.Run("timeout bounds", func(t *testing.T) {
		for _, timeout := range []string{"500ms", "5m", "banana"} {
			env := validTelegramEnv()
			env["NOTIFICATIONS_TELEGRAM_HTTP_TIMEOUT"] = timeout
			setTelegramEnv(t, env)
			if _, err := loadTelegramNotificationConfig(); err == nil {
				t.Fatalf("timeout %q must fail", timeout)
			}
		}
		env := validTelegramEnv()
		env["NOTIFICATIONS_TELEGRAM_HTTP_TIMEOUT"] = "30s"
		setTelegramEnv(t, env)
		cfg, err := loadTelegramNotificationConfig()
		if err != nil || cfg.HTTPTimeout != 30*time.Second {
			t.Fatalf("30s timeout: %v %+v", err, cfg)
		}
	})
	t.Run("bad flag rejected", func(t *testing.T) {
		env := validTelegramEnv()
		env["NOTIFICATIONS_TELEGRAM_ENABLED"] = "maybe"
		setTelegramEnv(t, env)
		if _, err := loadTelegramNotificationConfig(); err == nil {
			t.Fatal("bad flag must fail")
		}
	})
	t.Run("telegram env never touches whatsapp", func(t *testing.T) {
		setTelegramEnv(t, validTelegramEnv())
		setWhatsAppEnv(t, map[string]string{})
		whatsApp, err := loadWhatsAppNotificationConfig()
		if err != nil || whatsApp.Enabled {
			t.Fatalf("whatsapp must stay disabled: %v %+v", err, whatsApp.Enabled)
		}
	})
}
