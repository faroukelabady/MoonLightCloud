package config

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// Telegram notification runtime configuration. The bot token is a
// Cloud-only runtime secret: never persisted, never logged, never sent
// to Retail. Disabled by default: an unconfigured adapter registers no
// provider and requires nothing.
//
// Unlike WhatsApp Bearer authentication, the Telegram Bot API carries
// the token in the request path (https://<base>/bot<token>/sendMessage),
// so every full outbound URL is secret-bearing: it is never logged,
// persisted, or returned in diagnostics.
type TelegramNotificationConfig struct {
	// Enabled turns the Telegram provider on. Default false.
	Enabled bool
	// ProviderKey is the generic logical instance key (for example
	// "telegram-main"). Never required to name the vendor.
	ProviderKey string
	// BotToken authenticates outbound Bot API calls (URL path only).
	// Handled as a secret.
	BotToken string
	// BaseURL is the Bot API origin, https only, no userinfo/query/
	// fragment. Defaults to the official Telegram origin.
	BaseURL string
	// HTTPTimeout bounds every Bot API request. Default 15s.
	HTTPTimeout time.Duration
}

// Telegram notification configuration values.
const (
	DefaultTelegramBaseURL     = "https://api.telegram.org"
	DefaultTelegramHTTPTimeout = 15 * time.Second
	MinTelegramHTTPTimeout     = 1 * time.Second
	MaxTelegramHTTPTimeout     = 120 * time.Second
)

// telegramBotTokenPattern mirrors the adapter gate (see
// telegram.validateBotToken): Bot API charset only.
var telegramBotTokenPattern = regexp.MustCompile(`^[0-9]+:[A-Za-z0-9_-]+$`)

// loadTelegramNotificationConfig reads NOTIFICATIONS_TELEGRAM_*
// environment variables. Disabled (default) requires nothing and
// ignores the rest. All validation errors are value-free by
// construction: raw supplied values (which may contain the bot token
// or recipient data) are never echoed.
func loadTelegramNotificationConfig() (TelegramNotificationConfig, error) {
	enabled, err := parseTelegramBoolFlag("NOTIFICATIONS_TELEGRAM_ENABLED")
	if err != nil {
		return TelegramNotificationConfig{}, err
	}
	cfg := TelegramNotificationConfig{Enabled: enabled}
	if !enabled {
		return cfg, nil
	}
	cfg.ProviderKey = strings.TrimSpace(os.Getenv("NOTIFICATIONS_TELEGRAM_PROVIDER_KEY"))
	cfg.BotToken = strings.TrimSpace(os.Getenv("NOTIFICATIONS_TELEGRAM_BOT_TOKEN"))
	cfg.BaseURL = strings.TrimSpace(os.Getenv("NOTIFICATIONS_TELEGRAM_BASE_URL"))
	cfg.HTTPTimeout = DefaultTelegramHTTPTimeout
	if v := strings.TrimSpace(os.Getenv("NOTIFICATIONS_TELEGRAM_HTTP_TIMEOUT")); v != "" {
		timeout, err := time.ParseDuration(v)
		if err != nil {
			return TelegramNotificationConfig{}, fmt.Errorf("invalid NOTIFICATIONS_TELEGRAM_HTTP_TIMEOUT: want a valid duration")
		}
		cfg.HTTPTimeout = timeout
	}
	if err := cfg.validate(); err != nil {
		return TelegramNotificationConfig{}, err
	}
	return cfg, nil
}

// parseTelegramBoolFlag parses one Telegram boolean without echoing
// the raw value.
func parseTelegramBoolFlag(key string) (bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	switch {
	case v == "":
		return false, nil
	case strings.EqualFold(v, "true") || v == "1":
		return true, nil
	case strings.EqualFold(v, "false") || v == "0":
		return false, nil
	default:
		return false, fmt.Errorf("invalid %s: want true|false|1|0", key)
	}
}

// validate checks an enabled configuration eagerly so operator mistakes
// fail startup, never the first notification send. Error text names
// keys and rules without printing values. Token format is deliberately
// loose (non-empty, no controls): only current official guarantees
// plus safe bounded validation, so a future token format change needs
// no code change.
func (c TelegramNotificationConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	if !validNotificationProviderKey(c.ProviderKey) {
		return fmt.Errorf("invalid NOTIFICATIONS_TELEGRAM_PROVIDER_KEY: use 1..64 lowercase letters, numbers, hyphen, underscore")
	}
	// Same Bot API charset as the adapter gate (telegram.validateBotToken;
	// kept in sync manually — config cannot import the adapter package):
	// digits, one colon, word characters and hyphens. Rejected before
	// any URL construction so parse errors can never echo the token.
	if !telegramBotTokenPattern.MatchString(c.BotToken) || len(c.BotToken) > 256 {
		return fmt.Errorf("invalid NOTIFICATIONS_TELEGRAM_BOT_TOKEN: want a bounded Bot API token")
	}
	if c.BaseURL == "" {
		// Empty means the production default; normalization applies it.
	} else if err := validateTelegramBaseURL(c.BaseURL); err != nil {
		return err
	}
	if c.HTTPTimeout < MinTelegramHTTPTimeout || c.HTTPTimeout > MaxTelegramHTTPTimeout {
		return fmt.Errorf("NOTIFICATIONS_TELEGRAM_HTTP_TIMEOUT must be within [%s, %s]", MinTelegramHTTPTimeout, MaxTelegramHTTPTimeout)
	}
	return nil
}

// validateTelegramBaseURL enforces https origins without userinfo,
// query, or fragment. Error text never echoes the supplied value.
func validateTelegramBaseURL(raw string) error {
	// Reuse the shared notification URL policy: https, no userinfo,
	// query, or fragment. Error text names the Telegram key.
	if err := validateNotificationBaseURL(raw); err != nil {
		return fmt.Errorf("invalid NOTIFICATIONS_TELEGRAM_BASE_URL: want an https origin without userinfo, query, or fragment")
	}
	return nil
}

// NormalizedBaseURL returns the origin without a trailing slash.
func (c TelegramNotificationConfig) NormalizedBaseURL() string {
	if c.BaseURL == "" {
		return DefaultTelegramBaseURL
	}
	return strings.TrimSuffix(c.BaseURL, "/")
}
