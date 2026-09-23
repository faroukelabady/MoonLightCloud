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
	"net"
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

// Dashboard operator credential rules. Human login uses a configured
// username plus an Argon2id PHC hash — never a plaintext password, never
// in source control. Development falls back to documented constants when
// unset; staging/production require explicit values and reject the dev
// placeholder hash.
const (
	// DefaultDashboardUsername is the development-only username.
	DefaultDashboardUsername = "operator"
	// DevDashboardPassword is the documented development-only password.
	// The matching hash below is rejected outside development.
	DevDashboardPassword = "moonlight-dev-operator"
	// DevDashboardPasswordHash is Argon2id(DevDashboardPassword).
	DevDashboardPasswordHash = "$argon2id$v=19$m=65536,t=3,p=2$bEVIeBswLFNCwT4AKrDjOg$3v4UQUa5CkzWw3Iy/i35r2bG3xXH7Rvp5tLT0MBJKy0"
	// DefaultSessionTTL bounds operator sessions.
	DefaultSessionTTL = 12 * time.Hour
	// SessionTTL bounds for DASHBOARD_SESSION_TTL validation.
	MinSessionTTL = 15 * time.Minute
	MaxSessionTTL = 7 * 24 * time.Hour
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
	// ReportingToken is the temporary report-read Bearer secret.
	// Fail-closed policy (see resolveReportingToken):
	//   token configured (16+ chars) ............ authenticated reporting
	//   no token + ENVIRONMENT=development
	//     + ALLOW_UNAUTHENTICATED_REPORTING=true .. open local-dev reporting
	//   anything else ........................... startup failure
	// A missing ENVIRONMENT never implies development permission: without
	// an explicit development environment the open mode is unreachable.
	ReportingToken string
	// AllowUnauthenticatedReporting enables open reports. Valid only with
	// ENVIRONMENT=development and no reporting token; true anywhere else
	// (or with a token configured) is a startup failure.
	AllowUnauthenticatedReporting bool
	// DashboardUsername is the operator login name.
	DashboardUsername string
	// DashboardPasswordHash is the Argon2id PHC verifier for login.
	DashboardPasswordHash string
	// DashboardSessionTTL bounds dashboard sessions.
	DashboardSessionTTL time.Duration
	// TrustedProxyCIDRs are the only peers whose X-Forwarded-For chain is
	// believed for login rate limiting. Empty (default) trusts none:
	// forwarded headers from untrusted peers are ignored entirely.
	TrustedProxyCIDRs []net.IPNet
	// DashboardAssetsDir serves the built Svelte SPA at /dashboard.
	// Defaults to dashboard/dist (repo checkout); the OCI image overrides
	// to the baked-in assets path. Absent assets yield dashboard 404s;
	// the API is unaffected.
	DashboardAssetsDir string
	// envExplicit records whether ENVIRONMENT was set. Missing-environment
	// defaults still apply for other settings, but never grant open access.
	envExplicit   bool
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
		Environment:           envOr("ENVIRONMENT", EnvDevelopment),
		HTTPAddr:              envOr("HTTP_ADDR", DefaultHTTPAddr),
		DatabaseURL:           os.Getenv("DATABASE_URL"),
		LogLevel:              strings.ToLower(envOr("LOG_LEVEL", DefaultLogLevel)),
		PepperRaw:             strings.TrimSpace(os.Getenv("DEVICE_SECRET_PEPPER")),
		PepperVersion:         CurrentPepperVersion,
		StoreTimezone:         strings.TrimSpace(os.Getenv("STORE_TIMEZONE")),
		ReportingToken:        strings.TrimSpace(os.Getenv("REPORTING_API_TOKEN")),
		ShutdownAfter:         DefaultShutdownTimeout,
		DashboardUsername:     strings.TrimSpace(os.Getenv("DASHBOARD_USERNAME")),
		DashboardPasswordHash: strings.TrimSpace(os.Getenv("DASHBOARD_PASSWORD_HASH")),
		DashboardAssetsDir:    envOr("DASHBOARD_ASSETS_DIR", "dashboard/dist"),
		envExplicit:           strings.TrimSpace(os.Getenv("ENVIRONMENT")) != "",
	}
	allowOpen, err := parseBoolFlag("ALLOW_UNAUTHENTICATED_REPORTING")
	if err != nil {
		return Config{}, err
	}
	c.AllowUnauthenticatedReporting = allowOpen
	// A missing ENVIRONMENT never grants unauthenticated reporting, even
	// with the open flag: fail closed before any other default applies.
	if !c.envExplicit && c.ReportingToken == "" {
		return Config{}, fmt.Errorf("REPORTING_API_TOKEN is required (or explicit ENVIRONMENT=development open mode)")
	}
	c.DBMaxConns = DefaultDBMaxConns
	c.DBMinConns = DefaultDBMinConns
	c.DBMaxConnLife = DefaultDBMaxConnLife
	c.DBMaxConnIdle = DefaultDBMaxConnIdle
	c.DBConnectTimeout = DefaultDBConnectTimeout
	c.DBQueryTimeout = DefaultDBQueryTimeout
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
	if v := strings.TrimSpace(os.Getenv("DASHBOARD_SESSION_TTL")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("invalid DASHBOARD_SESSION_TTL %q: %w", v, err)
		}
		c.DashboardSessionTTL = d
	} else {
		c.DashboardSessionTTL = DefaultSessionTTL
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
	if err := c.resolveDashboardAuth(); err != nil {
		return err
	}
	if err := c.resolveTrustedProxies(); err != nil {
		return err
	}
	return nil
}

// resolveTrustedProxies parses TRUSTED_PROXY_CIDRS (comma-separated CIDRs,
// empty by default). Invalid entries fail startup; no undocumented Railway
// or cloud IP ranges are ever trusted implicitly.
func (c *Config) resolveTrustedProxies() error {
	raw := strings.TrimSpace(os.Getenv("TRUSTED_PROXY_CIDRS"))
	if raw == "" {
		return nil
	}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		_, cidr, err := net.ParseCIDR(part)
		if err != nil {
			// Accept a bare IP as a /32 (or /128) CIDR.
			bits := 32
			if ip := net.ParseIP(part); ip == nil {
				return fmt.Errorf("invalid TRUSTED_PROXY_CIDRS entry %q: %w", part, err)
			} else if ip.To4() == nil {
				bits = 128
			}
			_, cidr, err = net.ParseCIDR(fmt.Sprintf("%s/%d", part, bits))
			if err != nil {
				return fmt.Errorf("invalid TRUSTED_PROXY_CIDRS entry %q: %w", part, err)
			}
		}
		c.TrustedProxyCIDRs = append(c.TrustedProxyCIDRs, *cidr)
	}
	return nil
}

// resolveDashboardAuth validates the operator credential. Dashboard
// authentication requires an explicitly configured ENVIRONMENT: dev
// defaults activate only with explicit ENVIRONMENT=development, and even
// explicit production-quality credentials fail closed when the environment
// is omitted (an operator must declare which environment serves login).
func (c *Config) resolveDashboardAuth() error {
	if !c.envExplicit {
		return fmt.Errorf("ENVIRONMENT must be explicitly set for dashboard authentication")
	}
	devExplicit := c.Environment == EnvDevelopment
	if c.DashboardUsername == "" {
		if devExplicit {
			c.DashboardUsername = DefaultDashboardUsername
		} else {
			return fmt.Errorf("DASHBOARD_USERNAME is required outside development")
		}
	}
	if c.DashboardPasswordHash == "" {
		if devExplicit {
			c.DashboardPasswordHash = DevDashboardPasswordHash
		} else {
			return fmt.Errorf("DASHBOARD_PASSWORD_HASH is required outside development")
		}
	}
	if !devExplicit && c.DashboardPasswordHash == DevDashboardPasswordHash {
		return fmt.Errorf("DASHBOARD_PASSWORD_HASH must not be the development placeholder outside development")
	}
	if c.DashboardSessionTTL < MinSessionTTL || c.DashboardSessionTTL > MaxSessionTTL {
		return fmt.Errorf("DASHBOARD_SESSION_TTL must be within [15m, 168h]")
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

// resolveReportingToken enforces fail-closed reporting authentication:
//   - a valid token (16+ chars) enables authenticated reporting in any
//     environment, including an unset ENVIRONMENT (auth is enforced, so
//     nothing is exposed);
//   - without a token, open reporting requires ALL of: explicitly set
//     ENVIRONMENT=development AND ALLOW_UNAUTHENTICATED_REPORTING=true;
//   - everything else (staging/production without token, open flag outside
//     development, open flag with a token set, missing environment without
//     token) is a startup failure.
//
// A missing ENVIRONMENT therefore never grants unauthenticated reporting.
func (c *Config) resolveReportingToken() error {
	if c.ReportingToken != "" {
		if len(c.ReportingToken) < MinReportingTokenLen {
			return fmt.Errorf("REPORTING_API_TOKEN must be at least %d characters", MinReportingTokenLen)
		}
		if c.AllowUnauthenticatedReporting {
			return fmt.Errorf("ALLOW_UNAUTHENTICATED_REPORTING must not be set when REPORTING_API_TOKEN is configured")
		}
		return nil
	}
	if c.AllowUnauthenticatedReporting {
		if c.Environment != EnvDevelopment {
			return fmt.Errorf("ALLOW_UNAUTHENTICATED_REPORTING is development-only with explicit ENVIRONMENT=development")
		}
		return nil
	}
	return fmt.Errorf("REPORTING_API_TOKEN is required (or explicit development open mode)")
}

// parseBoolFlag parses an opt-in flag strictly: unset/false/0 deny,
// true/1 allow, anything else fails startup (typos must not silently
// change security posture).
func parseBoolFlag(key string) (bool, error) {
	v := strings.TrimSpace(os.Getenv(key))
	switch {
	case v == "":
		return false, nil
	case strings.EqualFold(v, "true") || v == "1":
		return true, nil
	case strings.EqualFold(v, "false") || v == "0":
		return false, nil
	default:
		return false, fmt.Errorf("invalid %s %q: want true|false|1|0", key, v)
	}
}

// IsDev reports local development mode (destructive scripts gate on this).
func (c Config) IsDev() bool { return c.Environment == EnvDevelopment }

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
