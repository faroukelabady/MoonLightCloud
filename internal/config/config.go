// Package config is the single centralized typed configuration system.
//
// Precedence (highest wins): environment variables > built-in defaults.
// There is no config file: in production the environment is authoritative,
// and local development uses .env.local loaded by scripts (never committed).
//
// Startup fails on invalid or missing required configuration. Production
// never silently falls back to development credentials or insecure defaults.
package config

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment values.
const (
	EnvDevelopment = "development"
	EnvStaging     = "staging"
	EnvProduction  = "production"
)

// Defaults for a small deployment. MoonLightCloud starts with extremely low
// load; the pool is intentionally small. See docs/architecture/overview.md.
const (
	DefaultHTTPAddr         = ":8080"
	DefaultLogLevel         = "info"
	DefaultShutdownTimeout  = 10 * time.Second
	DefaultDBMaxConns       = 10
	DefaultDBMinConns       = 1
	DefaultDBMaxConnLife    = 30 * time.Minute
	DefaultDBMaxConnIdle    = 5 * time.Minute
	DefaultDBConnectTimeout = 5 * time.Second
	DefaultDBQueryTimeout   = 5 * time.Second
)

// Device-secret pepper rules (ADR-0015). The pepper is a 256-bit server-side
// HMAC key, base64 StdEncoding, exactly 32 bytes decoded. It lives only in
// configuration — never in PostgreSQL, never in logs.
// Development uses a documented fixed value when DEVICE_SECRET_PEPPER is
// unset; staging/production require an explicit value that decodes to 32
// bytes and is not the dev placeholder.
const (
	PepperBytes = 32
	// DevPepper is the documented development-only pepper (base64 of the
	// ASCII string "moonlight-cloud-dev-pepper-00001"). Rejected outside
	// development. It exists so local development works with zero secrets
	// while production can never silently inherit it.
	DevPepper = "bW9vbmxpZ2h0LWNsb3VkLWRldi1wZXBwZXItMDAwMDE="
	// CurrentPepperVersion tags new credentials; enables future rotation.
	CurrentPepperVersion = 1
)

// Store timezone rules. Business days are IANA calendar days in the store
// timezone; production (Africa/Cairo) requires an explicit value.
// Development defaults to Africa/Cairo. Nothing ever falls back to UTC,
// system local time, or fixed offsets: LoadLocation failure fails startup.
const (
	// DefaultStoreTimezone is the development/test default.
	DefaultStoreTimezone = "Africa/Cairo"
	// MinReportingTokenLen requires a high-entropy reporting token.
	MinReportingTokenLen = 16
)

var devDBMarkers = []string{
	"moonlight:moonlight@",
	"postgres:postgres@",
	"password@",
	"localhost",
	"127.0.0.1",
}

// Config is the full typed application configuration.
type Config struct {
	Environment   string
	HTTPAddr      string
	DatabaseURL   string
	LogLevel      string
	PepperRaw     string
	Pepper        []byte
	PepperVersion int
	// StoreTimezone is the IANA identifier for business-day semantics.
	StoreTimezone string
	// StoreLocation is the resolved timezone; never nil after Validate.
	StoreLocation *time.Location
	// ReportingToken is the temporary report-read Bearer secret. Empty is
	// allowed only in development (open reports with a startup warning);
	// staging/production require an explicit high-entropy value.
	ReportingToken string
	ShutdownAfter  time.Duration

	DBMaxConns       int32
	DBMinConns       int32
	DBMaxConnLife    time.Duration
	DBMaxConnIdle    time.Duration
	DBConnectTimeout time.Duration
	DBQueryTimeout   time.Duration
}

// Load reads configuration from the environment.
func Load() (Config, error) {
	c := Config{
		Environment:      envOr("ENVIRONMENT", EnvDevelopment),
		HTTPAddr:         envOr("HTTP_ADDR", DefaultHTTPAddr),
		DatabaseURL:      os.Getenv("DATABASE_URL"),
		LogLevel:         strings.ToLower(envOr("LOG_LEVEL", DefaultLogLevel)),
		PepperRaw:        strings.TrimSpace(os.Getenv("DEVICE_SECRET_PEPPER")),
		PepperVersion:    CurrentPepperVersion,
		StoreTimezone:    strings.TrimSpace(os.Getenv("STORE_TIMEZONE")),
		ReportingToken:   strings.TrimSpace(os.Getenv("REPORTING_API_TOKEN")),
		ShutdownAfter:    DefaultShutdownTimeout,
		DBMaxConns:       DefaultDBMaxConns,
		DBMinConns:       DefaultDBMinConns,
		DBMaxConnLife:    DefaultDBMaxConnLife,
		DBMaxConnIdle:    DefaultDBMaxConnIdle,
		DBConnectTimeout: DefaultDBConnectTimeout,
		DBQueryTimeout:   DefaultDBQueryTimeout,
	}
	// Railway and similar hosts inject PORT; honor it when HTTP_ADDR is default.
	if c.HTTPAddr == DefaultHTTPAddr {
		if port := strings.TrimSpace(os.Getenv("PORT")); port != "" {
			c.HTTPAddr = ":" + strings.TrimPrefix(port, ":")
		}
	}
	if v := strings.TrimSpace(os.Getenv("SHUTDOWN_TIMEOUT")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("invalid SHUTDOWN_TIMEOUT %q: %w", v, err)
		}
		c.ShutdownAfter = d
	}
	if v := strings.TrimSpace(os.Getenv("DB_MAX_CONNS")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("invalid DB_MAX_CONNS %q: %w", v, err)
		}
		c.DBMaxConns = int32(n)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate rejects missing or unsafe configuration. It also resolves the
// pepper (pointer receiver: decoded bytes are stored back into Config).
func (c *Config) Validate() error {
	switch c.Environment {
	case EnvDevelopment, EnvStaging, EnvProduction:
	default:
		return fmt.Errorf("invalid ENVIRONMENT %q: want development|staging|production", c.Environment)
	}
	if strings.TrimSpace(c.HTTPAddr) == "" {
		return fmt.Errorf("HTTP_ADDR is required")
	}
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	u, err := url.Parse(c.DatabaseURL)
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return fmt.Errorf("DATABASE_URL must be a postgres[ql] URL")
	}
	if u.Hostname() == "" || u.User == nil {
		return fmt.Errorf("DATABASE_URL must include user and host")
	}
	switch strings.ToLower(c.LogLevel) {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid LOG_LEVEL %q: want debug|info|warn|error", c.LogLevel)
	}
	if c.ShutdownAfter <= 0 || c.ShutdownAfter > 2*time.Minute {
		return fmt.Errorf("SHUTDOWN_TIMEOUT must be within (0, 2m]")
	}
	if c.DBMaxConns < 1 || c.DBMaxConns > 100 {
		return fmt.Errorf("DB_MAX_CONNS must be within [1, 100]")
	}
	if c.Environment == EnvProduction || c.Environment == EnvStaging {
		lowered := strings.ToLower(c.DatabaseURL)
		for _, m := range devDBMarkers {
			if strings.Contains(lowered, m) {
				return fmt.Errorf("production ENVIRONMENT refuses development DATABASE_URL marker %q", m)
			}
		}
	}
	if err := c.resolvePepper(); err != nil {
		return err
	}
	if err := c.resolveStoreTimezone(); err != nil {
		return err
	}
	if err := c.resolveReportingToken(); err != nil {
		return err
	}
	return nil
}

// resolvePepper decodes and validates the server-side HMAC pepper.
// Development falls back to the documented DevPepper when unset; every
// other environment requires an explicit base64 value decoding to exactly
// 32 bytes that is not the dev placeholder.
func (c *Config) resolvePepper() error {
	raw := c.PepperRaw
	if raw == "" {
		if c.Environment == EnvDevelopment {
			raw = DevPepper
		} else {
			return fmt.Errorf("DEVICE_SECRET_PEPPER is required outside development")
		}
	}
	if c.Environment != EnvDevelopment && raw == DevPepper {
		return fmt.Errorf("DEVICE_SECRET_PEPPER must not be the development placeholder outside development")
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return fmt.Errorf("DEVICE_SECRET_PEPPER must be base64: %w", err)
	}
	if len(decoded) != PepperBytes {
		return fmt.Errorf("DEVICE_SECRET_PEPPER must decode to %d bytes, got %d", PepperBytes, len(decoded))
	}
	c.Pepper = decoded
	return nil
}

// resolveStoreTimezone applies the timezone policy: explicit value in
// staging/production, Africa/Cairo default in development. Resolution uses
// time.LoadLocation against the IANA database; any failure fails startup
// (never UTC, never system local, never fixed offsets).
func (c *Config) resolveStoreTimezone() error {
	tz := c.StoreTimezone
	if tz == "" {
		if c.Environment == EnvDevelopment {
			tz = DefaultStoreTimezone
		} else {
			return fmt.Errorf("STORE_TIMEZONE is required outside development")
		}
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return fmt.Errorf("invalid STORE_TIMEZONE %q: %w", tz, err)
	}
	c.StoreTimezone = tz
	c.StoreLocation = loc
	return nil
}

// resolveReportingToken enforces the temporary reporting guard: an explicit
// high-entropy Bearer secret outside development; development may leave it
// empty (reports unauthenticated, with a startup warning from the caller).
func (c *Config) resolveReportingToken() error {
	if c.ReportingToken == "" {
		if c.Environment == EnvDevelopment {
			return nil
		}
		return fmt.Errorf("REPORTING_API_TOKEN is required outside development")
	}
	if len(c.ReportingToken) < MinReportingTokenLen {
		return fmt.Errorf("REPORTING_API_TOKEN must be at least %d characters", MinReportingTokenLen)
	}
	return nil
}

// IsDev reports local development mode (destructive scripts gate on this).
func (c Config) IsDev() bool { return c.Environment == EnvDevelopment }

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
