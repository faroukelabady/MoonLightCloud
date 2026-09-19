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

// Known development credentials. Production startup is rejected when the
// DATABASE_URL still carries one of these markers.
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
	SecretPepper  string
	ShutdownAfter time.Duration

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
		SecretPepper:     os.Getenv("DEVICE_SECRET_PEPPER"),
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

// Validate rejects missing or unsafe configuration.
func (c Config) Validate() error {
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
	if c.Environment == EnvProduction {
		lowered := strings.ToLower(c.DatabaseURL)
		for _, m := range devDBMarkers {
			if strings.Contains(lowered, m) {
				return fmt.Errorf("production ENVIRONMENT refuses development DATABASE_URL marker %q", m)
			}
		}
		if c.SecretPepper == "" {
			return fmt.Errorf("production ENVIRONMENT requires DEVICE_SECRET_PEPPER")
		}
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
