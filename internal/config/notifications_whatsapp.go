package config

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// WhatsApp notification runtime configuration. Credentials are
// Cloud-only runtime secrets: never persisted, never logged, never sent
// to Retail. Disabled by default: an unconfigured adapter registers no
// provider and requires nothing.
type WhatsAppNotificationConfig struct {
	// Enabled turns the WhatsApp provider on. Default false.
	Enabled bool
	// ProviderKey is the generic logical instance key (for example
	// "whatsapp-main"). Never required to name the vendor.
	ProviderKey string
	// GraphVersion is the explicit Meta Graph API version (vXX.X).
	GraphVersion string
	// BaseURL is the Graph origin, https only, no userinfo/query/fragment.
	BaseURL string
	// PhoneNumberID is the WhatsApp Business phone number ID used in the
	// API path. It is not a recipient.
	PhoneNumberID string
	// AccessToken authenticates outbound Graph calls (Bearer header
	// only). Handled as a secret.
	AccessToken string
	// AppSecret verifies inbound webhook HMAC. Handled as a secret,
	// never persisted.
	AppSecret string
	// WebhookVerifyToken answers Meta callback verification GETs.
	// Handled as sensitive, never persisted.
	WebhookVerifyToken string
	// HTTPTimeout bounds every Graph request. Default 15s.
	HTTPTimeout time.Duration
}

// WhatsApp notification configuration values.
const (
	DefaultWhatsAppGraphBaseURL = "https://graph.facebook.com"
	DefaultWhatsAppHTTPTimeout  = 15 * time.Second
	MinWhatsAppHTTPTimeout      = 1 * time.Second
	MaxWhatsAppHTTPTimeout      = 120 * time.Second
)

var (
	whatsAppGraphVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+$`)
	whatsAppPhoneIDPattern      = regexp.MustCompile(`^[0-9]{1,32}$`)
)

// loadWhatsAppNotificationConfig reads NOTIFICATIONS_WHATSAPP_*
// environment variables. Disabled (default) requires nothing and
// ignores the rest.
func loadWhatsAppNotificationConfig() (WhatsAppNotificationConfig, error) {
	enabled, err := parseBoolFlag("NOTIFICATIONS_WHATSAPP_ENABLED")
	if err != nil {
		return WhatsAppNotificationConfig{}, err
	}
	cfg := WhatsAppNotificationConfig{Enabled: enabled}
	if !enabled {
		return cfg, nil
	}
	cfg.ProviderKey = strings.TrimSpace(os.Getenv("NOTIFICATIONS_WHATSAPP_PROVIDER_KEY"))
	cfg.GraphVersion = strings.TrimSpace(os.Getenv("NOTIFICATIONS_WHATSAPP_GRAPH_VERSION"))
	cfg.BaseURL = strings.TrimSpace(os.Getenv("NOTIFICATIONS_WHATSAPP_BASE_URL"))
	cfg.PhoneNumberID = strings.TrimSpace(os.Getenv("NOTIFICATIONS_WHATSAPP_PHONE_NUMBER_ID"))
	cfg.AccessToken = strings.TrimSpace(os.Getenv("NOTIFICATIONS_WHATSAPP_ACCESS_TOKEN"))
	cfg.AppSecret = strings.TrimSpace(os.Getenv("NOTIFICATIONS_WHATSAPP_APP_SECRET"))
	cfg.WebhookVerifyToken = strings.TrimSpace(os.Getenv("NOTIFICATIONS_WHATSAPP_WEBHOOK_VERIFY_TOKEN"))
	cfg.HTTPTimeout = DefaultWhatsAppHTTPTimeout
	if v := strings.TrimSpace(os.Getenv("NOTIFICATIONS_WHATSAPP_HTTP_TIMEOUT")); v != "" {
		timeout, err := time.ParseDuration(v)
		if err != nil {
			return WhatsAppNotificationConfig{}, fmt.Errorf("invalid NOTIFICATIONS_WHATSAPP_HTTP_TIMEOUT: %v", err)
		}
		cfg.HTTPTimeout = timeout
	}
	if err := cfg.validate(); err != nil {
		return WhatsAppNotificationConfig{}, err
	}
	return cfg, nil
}

// validate checks an enabled configuration eagerly so operator mistakes
// fail startup, never the first notification send. Error text names
// keys and rules without printing values.
func (c WhatsAppNotificationConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	if !validNotificationProviderKey(c.ProviderKey) {
		return fmt.Errorf("invalid NOTIFICATIONS_WHATSAPP_PROVIDER_KEY: use 1..64 lowercase letters, numbers, hyphen, underscore")
	}
	if !whatsAppGraphVersionPattern.MatchString(c.GraphVersion) {
		return fmt.Errorf("invalid NOTIFICATIONS_WHATSAPP_GRAPH_VERSION: want vXX.X")
	}
	if c.BaseURL == "" {
		// Empty means the production default; normalization applies it.
	} else if err := validateNotificationBaseURL(c.BaseURL); err != nil {
		return err
	}
	if !whatsAppPhoneIDPattern.MatchString(c.PhoneNumberID) {
		return fmt.Errorf("invalid NOTIFICATIONS_WHATSAPP_PHONE_NUMBER_ID: want 1..32 digits")
	}
	if c.AccessToken == "" {
		return fmt.Errorf("NOTIFICATIONS_WHATSAPP_ACCESS_TOKEN is required when WhatsApp notifications are enabled")
	}
	if c.AppSecret == "" {
		return fmt.Errorf("NOTIFICATIONS_WHATSAPP_APP_SECRET is required when WhatsApp notifications are enabled")
	}
	if c.WebhookVerifyToken == "" {
		return fmt.Errorf("NOTIFICATIONS_WHATSAPP_WEBHOOK_VERIFY_TOKEN is required when WhatsApp notifications are enabled")
	}
	if c.HTTPTimeout < MinWhatsAppHTTPTimeout || c.HTTPTimeout > MaxWhatsAppHTTPTimeout {
		return fmt.Errorf("NOTIFICATIONS_WHATSAPP_HTTP_TIMEOUT must be within [%s, %s]", MinWhatsAppHTTPTimeout, MaxWhatsAppHTTPTimeout)
	}
	return nil
}

func validNotificationProviderKey(key string) bool {
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

// validateNotificationBaseURL enforces https origins without userinfo,
// query, or fragment. Error text never echoes the supplied value: it
// may itself carry credential material.
func validateNotificationBaseURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("NOTIFICATIONS_WHATSAPP_BASE_URL is not a valid URL")
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("NOTIFICATIONS_WHATSAPP_BASE_URL must use https")
	}
	if parsed.User != nil {
		return fmt.Errorf("NOTIFICATIONS_WHATSAPP_BASE_URL must not contain userinfo")
	}
	if parsed.RawQuery != "" {
		return fmt.Errorf("NOTIFICATIONS_WHATSAPP_BASE_URL must not contain query parameters")
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("NOTIFICATIONS_WHATSAPP_BASE_URL must not contain a fragment")
	}
	return nil
}

// NormalizedBaseURL returns the origin without a trailing slash.
func (c WhatsAppNotificationConfig) NormalizedBaseURL() string {
	if c.BaseURL == "" {
		return DefaultWhatsAppGraphBaseURL
	}
	return strings.TrimSuffix(c.BaseURL, "/")
}
