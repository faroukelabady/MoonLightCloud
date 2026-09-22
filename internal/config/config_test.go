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

func TestLoadDevelopmentDefaults(t *testing.T) {
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
	cases := []struct {
		name    string
		env     string
		token   string // "" means unset
		wantErr bool
	}{
		{"dev open allowed", "development", "", false},
		{"dev token allowed", "development", "0123456789abcdef", false},
		{"prod missing rejected", "production", "", true},
		{"prod short rejected", "production", "short", true},
		{"prod valid accepted", "production", "0123456789abcdef0123456789abcdef", false},
		{"staging missing rejected", "staging", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setenv(t, "ENVIRONMENT", tc.env)
			if tc.env == "development" {
				setenv(t, "DATABASE_URL", "postgres://moonlight:moonlight@localhost:5432/moonlight_dev?sslmode=disable")
			} else {
				setenv(t, "DATABASE_URL", url)
			}
			setenv(t, "DEVICE_SECRET_PEPPER", pepperB64('v'))
			setenv(t, "STORE_TIMEZONE", "Africa/Cairo")
			if tc.token == "" {
				os.Unsetenv("REPORTING_API_TOKEN")
			} else {
				setenv(t, "REPORTING_API_TOKEN", tc.token)
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
	setenv(t, "PORT", "1234")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":1234" {
		t.Fatalf("want :1234, got %s", c.HTTPAddr)
	}
}
