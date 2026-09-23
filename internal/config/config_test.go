package config

import (
	"encoding/base64"
	"os"
	"testing"
)

func setenv(t *testing.T, k, v string) {
	t.Helper()
	t.Setenv(k, v)
}

// setReportingToken pins a valid token so tests focus on other settings.
func setReportingToken(t *testing.T) {
	t.Helper()
	t.Setenv("REPORTING_API_TOKEN", "0123456789abcdef0123456789abcdef")
	os.Unsetenv("ALLOW_UNAUTHENTICATED_REPORTING")
}

// setDashboardAuth pins valid operator credentials so tests focus elsewhere.
// Uses a non-placeholder hash so staging/production success paths pass.
func setDashboardAuth(t *testing.T) {
	t.Helper()
	t.Setenv("DASHBOARD_USERNAME", "op")
	t.Setenv("DASHBOARD_PASSWORD_HASH", "$argon2id$v=19$m=65536,t=3,p=2$FUDrA/wAq/7mONAYJerxEg$5GSTO+qp6PL1LIy8JoXcPBN6eKU5YLD53VMkJCdYw/U")
}

func TestLoadDevelopmentDefaults(t *testing.T) {
	setReportingToken(t)
	setenv(t, "ENVIRONMENT", "development")
	setenv(t, "DATABASE_URL", "postgres://moonlight:moonlight@localhost:5432/moonlight_dev?sslmode=disable")
	os.Unsetenv("HTTP_ADDR")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":8080" || c.LogLevel != "info" {
		t.Fatalf("bad defaults: %+v", c)
	}
}

func TestMissingDatabaseURL(t *testing.T) {
	setenv(t, "ENVIRONMENT", "development")
	os.Unsetenv("DATABASE_URL")
	if _, err := Load(); err == nil {
		t.Fatal("want error for missing DATABASE_URL")
	}
}

func TestBadEnvironment(t *testing.T) {
	setenv(t, "ENVIRONMENT", "mars")
	setenv(t, "DATABASE_URL", "postgres://u@h:5432/d?sslmode=disable")
	if _, err := Load(); err == nil {
		t.Fatal("want error for bad ENVIRONMENT")
	}
}

func TestProductionRefusesDevCredentials(t *testing.T) {
	setenv(t, "ENVIRONMENT", "production")
	setenv(t, "DATABASE_URL", "postgres://moonlight:moonlight@db:5432/app?sslmode=require")
	setenv(t, "DEVICE_SECRET_PEPPER", pepperB64('v'))
	if _, err := Load(); err == nil {
		t.Fatal("want error for dev DATABASE_URL in production")
	}
}

func TestProductionRequiresPepper(t *testing.T) {
	setenv(t, "ENVIRONMENT", "production")
	setenv(t, "DATABASE_URL", "postgres://cloud:secret@10.0.0.5:5432/cloud?sslmode=require")
	os.Unsetenv("DEVICE_SECRET_PEPPER")
	if _, err := Load(); err == nil {
		t.Fatal("want error for missing pepper in production")
	}
}

func TestProductionValid(t *testing.T) {
	setenv(t, "ENVIRONMENT", "production")
	setenv(t, "DATABASE_URL", "postgres://cloud:secret@10.0.0.5:5432/cloud?sslmode=require")
	setenv(t, "DEVICE_SECRET_PEPPER", pepperB64('v'))
	setenv(t, "STORE_TIMEZONE", "Africa/Cairo")
	setenv(t, "REPORTING_API_TOKEN", "0123456789abcdef0123456789abcdef")
	os.Unsetenv("ALLOW_UNAUTHENTICATED_REPORTING")
	setDashboardAuth(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Pepper) != PepperBytes || c.PepperVersion != CurrentPepperVersion {
		t.Fatalf("pepper not resolved: %d bytes", len(c.Pepper))
	}
	if c.StoreLocation == nil || c.StoreLocation.String() != "Africa/Cairo" {
		t.Fatalf("timezone not resolved: %v", c.StoreLocation)
	}
}

func TestStoreTimezoneMatrix(t *testing.T) {
	const url = "postgres://cloud:secret@10.0.0.5:5432/cloud?sslmode=require"
	const devURL = "postgres://moonlight:moonlight@localhost:5432/moonlight_dev?sslmode=disable"
	cases := []struct {
		name    string
		env     string
		tz      string // "" means unset
		wantTZ  string
		wantErr bool
	}{
		{"dev default", "development", "", "Africa/Cairo", false},
		{"dev explicit UTC", "development", "UTC", "UTC", false},
		{"dev invalid", "development", "Mars/Olympus", "", true},
		{"dev fixed offset rejected as zone", "development", "+02:00", "", true},
		{"prod missing", "production", "", "", true},
		{"prod Cairo", "production", "Africa/Cairo", "Africa/Cairo", false},
		{"prod invalid", "production", "Nope/Zone", "", true},
		{"staging missing", "staging", "", "", true},
		{"staging explicit", "staging", "Africa/Cairo", "Africa/Cairo", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setenv(t, "ENVIRONMENT", tc.env)
			if tc.env == "development" {
				setenv(t, "DATABASE_URL", devURL)
			} else {
				setenv(t, "DATABASE_URL", url)
			}
			setenv(t, "DEVICE_SECRET_PEPPER", pepperB64('v'))
			setenv(t, "REPORTING_API_TOKEN", "0123456789abcdef0123456789abcdef")
			os.Unsetenv("ALLOW_UNAUTHENTICATED_REPORTING")
			setDashboardAuth(t)
			if tc.tz == "" {
				os.Unsetenv("STORE_TIMEZONE")
			} else {
				setenv(t, "STORE_TIMEZONE", tc.tz)
			}
			c, err := Load()
			if (err != nil) != tc.wantErr {
				t.Fatalf("wantErr=%v got %v", tc.wantErr, err)
			}
			if err == nil && c.StoreLocation.String() != tc.wantTZ {
				t.Fatalf("want %s got %s", tc.wantTZ, c.StoreLocation)
			}
		})
	}
}

func TestReportingTokenMatrix(t *testing.T) {
	const url = "postgres://cloud:secret@10.0.0.5:5432/cloud?sslmode=require"
	const devURL = "postgres://moonlight:moonlight@localhost:5432/moonlight_dev?sslmode=disable"
	const goodToken = "0123456789abcdef0123456789abcdef"
	cases := []struct {
		name string
		// env "" means ENVIRONMENT unset entirely.
		env     string
		token   string // "" means unset
		open    string // "" means unset
		wantErr bool
	}{
		{"production + token PASS", "production", goodToken, "", false},
		{"production + no token FAIL", "production", "", "", true},
		{"production + open flag FAIL", "production", "", "true", true},
		{"production + token + open flag FAIL", "production", goodToken, "true", true},
		{"staging + token PASS", "staging", goodToken, "", false},
		{"staging + no token FAIL", "staging", "", "", true},
		{"staging + open flag FAIL", "staging", "", "true", true},
		{"development + token PASS", "development", goodToken, "", false},
		{"development + no token + open flag PASS", "development", "", "true", false},
		{"development + no token + no flag FAIL", "development", "", "", true},
		{"development + token + open flag FAIL", "development", goodToken, "true", true},
		{"development + bad flag value FAIL", "development", "", "yes", true},
		{"missing env + token FAIL (dashboard needs explicit env)", "", goodToken, "", true},
		{"missing env + no token FAIL", "", "", "", true},
		{"missing env + open flag FAIL", "", "", "true", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env == "" {
				os.Unsetenv("ENVIRONMENT")
				setenv(t, "DATABASE_URL", devURL)
			} else {
				setenv(t, "ENVIRONMENT", tc.env)
				if tc.env == "development" {
					setenv(t, "DATABASE_URL", devURL)
				} else {
					setenv(t, "DATABASE_URL", url)
				}
			}
			setenv(t, "DEVICE_SECRET_PEPPER", pepperB64('v'))
			setenv(t, "STORE_TIMEZONE", "Africa/Cairo")
			setDashboardAuth(t)
			if tc.token == "" {
				os.Unsetenv("REPORTING_API_TOKEN")
			} else {
				setenv(t, "REPORTING_API_TOKEN", tc.token)
			}
			if tc.open == "" {
				os.Unsetenv("ALLOW_UNAUTHENTICATED_REPORTING")
			} else {
				setenv(t, "ALLOW_UNAUTHENTICATED_REPORTING", tc.open)
			}
			_, err := Load()
			if (err != nil) != tc.wantErr {
				t.Fatalf("wantErr=%v got %v", tc.wantErr, err)
			}
		})
	}
}

func TestPepperMatrix(t *testing.T) {
	validProdURL := "postgres://cloud:secret@10.0.0.5:5432/cloud?sslmode=require"
	devURL := "postgres://moonlight:moonlight@localhost:5432/moonlight_dev?sslmode=disable"
	cases := []struct {
		name    string
		env     string
		pepper  string // "" means unset
		wantErr bool
	}{
		{"dev default when unset", "development", "", false},
		{"dev explicit valid", "development", pepperB64('d'), false},
		{"dev malformed rejected", "development", "!!!", true},
		{"dev wrong length rejected", "development", pepperB64short(), true},
		{"prod missing rejected", "production", "", true},
		{"prod malformed rejected", "production", "pepper", true},
		{"prod wrong length rejected", "production", pepperB64short(), true},
		{"prod placeholder rejected", "production", DevPepper, true},
		{"prod valid accepted", "production", pepperB64('v'), false},
		{"staging requires explicit", "staging", "", true},
		{"staging placeholder rejected", "staging", DevPepper, true},
		{"staging valid accepted", "staging", pepperB64('v'), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setenv(t, "ENVIRONMENT", tc.env)
			if tc.env == "development" {
				setenv(t, "DATABASE_URL", devURL)
			} else {
				setenv(t, "DATABASE_URL", validProdURL)
			}
			if tc.pepper == "" {
				os.Unsetenv("DEVICE_SECRET_PEPPER")
			} else {
				setenv(t, "DEVICE_SECRET_PEPPER", tc.pepper)
			}
			// Pepper matrix predates reporting config; hold those constant.
			setenv(t, "STORE_TIMEZONE", "Africa/Cairo")
			setenv(t, "REPORTING_API_TOKEN", "0123456789abcdef0123456789abcdef")
			setDashboardAuth(t)
			_, err := Load()
			if (err != nil) != tc.wantErr {
				t.Fatalf("wantErr=%v got %v", tc.wantErr, err)
			}
		})
	}
}

// pepperB64 returns base64 of 32 repeated bytes.
func pepperB64(c byte) string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = c
	}
	return base64.StdEncoding.EncodeToString(b)
}

func pepperB64short() string {
	return base64.StdEncoding.EncodeToString([]byte("too-short"))
}

func TestPortOverride(t *testing.T) {
	setenv(t, "ENVIRONMENT", "development")
	setenv(t, "DATABASE_URL", "postgres://moonlight:moonlight@localhost:5432/moonlight_dev?sslmode=disable")
	os.Unsetenv("HTTP_ADDR")
	setReportingToken(t)
	setenv(t, "PORT", "1234")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":1234" {
		t.Fatalf("want :1234, got %s", c.HTTPAddr)
	}
}

func TestDashboardAuthMatrix(t *testing.T) {
	const url = "postgres://cloud:secret@10.0.0.5:5432/cloud?sslmode=require"
	const devURL = "postgres://moonlight:moonlight@localhost:5432/moonlight_dev?sslmode=disable"
	const goodHash = "$argon2id$v=19$m=65536,t=3,p=2$FUDrA/wAq/7mONAYJerxEg$5GSTO+qp6PL1LIy8JoXcPBN6eKU5YLD53VMkJCdYw/U"
	cases := []struct {
		name    string
		env     string // "∅" means ENVIRONMENT unset entirely
		user    string // "" means unset
		hash    string // "" means unset
		ttl     string // "" means unset
		wantErr bool
	}{
		{"explicit development + defaults", "development", "", "", "", false},
		{"explicit development + explicit credentials", "development", "boss", goodHash, "1h", false},
		{"dev bad ttl rejected", "development", "boss", goodHash, "forever", true},
		{"dev short ttl rejected", "development", "boss", goodHash, "1m", true},
		{"explicit staging + credentials", "staging", "boss", goodHash, "", false},
		{"explicit staging + missing credentials", "staging", "", "", "", true},
		{"explicit production + credentials", "production", "boss", goodHash, "", false},
		{"explicit production + missing credentials", "production", "", "", "", true},
		{"prod missing user rejected", "production", "", goodHash, "", true},
		{"prod missing hash rejected", "production", "boss", "", "", true},
		{"prod placeholder rejected", "production", "boss", DevDashboardPasswordHash, "", true},
		{"staging placeholder rejected", "staging", "boss", DevDashboardPasswordHash, "", true},
		{"missing environment + report token", "∅", "", "", "", true},
		{"missing environment + dashboard defaults", "∅", "", "", "", true},
		{"missing environment + both configs", "∅", "boss", goodHash, "", true},
		{"blank environment string", "", "", "", "", true},
		{"unknown environment", "mars", "boss", goodHash, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env == "∅" || tc.env == "" {
				os.Unsetenv("ENVIRONMENT")
				setenv(t, "DATABASE_URL", devURL)
			} else {
				setenv(t, "ENVIRONMENT", tc.env)
				if tc.env == "development" {
					setenv(t, "DATABASE_URL", devURL)
				} else {
					setenv(t, "DATABASE_URL", url)
				}
			}
			setenv(t, "DEVICE_SECRET_PEPPER", pepperB64('v'))
			setenv(t, "STORE_TIMEZONE", "Africa/Cairo")
			setenv(t, "REPORTING_API_TOKEN", "0123456789abcdef0123456789abcdef")
			os.Unsetenv("ALLOW_UNAUTHENTICATED_REPORTING")
			if tc.user == "" {
				os.Unsetenv("DASHBOARD_USERNAME")
			} else {
				setenv(t, "DASHBOARD_USERNAME", tc.user)
			}
			if tc.hash == "" {
				os.Unsetenv("DASHBOARD_PASSWORD_HASH")
			} else {
				setenv(t, "DASHBOARD_PASSWORD_HASH", tc.hash)
			}
			if tc.ttl == "" {
				os.Unsetenv("DASHBOARD_SESSION_TTL")
			} else {
				setenv(t, "DASHBOARD_SESSION_TTL", tc.ttl)
			}
			c, err := Load()
			if (err != nil) != tc.wantErr {
				t.Fatalf("wantErr=%v got %v", tc.wantErr, err)
			}
			if err == nil && tc.env == "development" && tc.user == "" {
				if c.DashboardUsername != DefaultDashboardUsername ||
					c.DashboardPasswordHash != DevDashboardPasswordHash {
					t.Fatal("dev defaults not applied")
				}
			}
		})
	}
}

func TestTrustedProxyCIDRs(t *testing.T) {
	base := func(t *testing.T) {
		t.Helper()
		setenv(t, "ENVIRONMENT", "development")
		setenv(t, "DATABASE_URL", "postgres://moonlight:moonlight@localhost:5432/moonlight_dev?sslmode=disable")
		setenv(t, "DEVICE_SECRET_PEPPER", pepperB64('v'))
		setenv(t, "STORE_TIMEZONE", "Africa/Cairo")
		setenv(t, "REPORTING_API_TOKEN", "0123456789abcdef0123456789abcdef")
		os.Unsetenv("ALLOW_UNAUTHENTICATED_REPORTING")
		setDashboardAuth(t)
	}
	t.Run("empty trusts none", func(t *testing.T) {
		base(t)
		os.Unsetenv("TRUSTED_PROXY_CIDRS")
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if len(c.TrustedProxyCIDRs) != 0 {
			t.Fatal("default must trust none")
		}
	})
	t.Run("cidrs and bare IPs parse", func(t *testing.T) {
		base(t)
		setenv(t, "TRUSTED_PROXY_CIDRS", "10.0.0.0/8, 192.168.1.7, 2001:db8::/32")
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if len(c.TrustedProxyCIDRs) != 3 {
			t.Fatalf("got %d", len(c.TrustedProxyCIDRs))
		}
	})
	t.Run("garbage fails closed", func(t *testing.T) {
		base(t)
		setenv(t, "TRUSTED_PROXY_CIDRS", "not-a-cidr")
		if _, err := Load(); err == nil {
			t.Fatal("invalid CIDR must fail startup")
		}
	})
}
