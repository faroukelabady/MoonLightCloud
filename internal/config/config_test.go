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
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Pepper) != PepperBytes || c.PepperVersion != CurrentPepperVersion {
		t.Fatalf("pepper not resolved: %d bytes", len(c.Pepper))
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
