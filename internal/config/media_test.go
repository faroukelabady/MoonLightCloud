package config

import (
	"fmt"
	"strings"
	"testing"
)

func setMediaEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"MEDIA_STORAGE", "MEDIA_LOCAL_DIR", "R2_ENDPOINT", "R2_BUCKET", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY"} {
		t.Setenv(name, "")
	}
}
func validMediaEnv(t *testing.T) {
	t.Helper()
	setMediaEnv(t)
	t.Setenv("MEDIA_STORAGE", "r2")
	t.Setenv("R2_ENDPOINT", "https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com")
	t.Setenv("R2_BUCKET", "moonlight-media-dev")
	t.Setenv("R2_ACCESS_KEY_ID", "synthetic-access-key")
	t.Setenv("R2_SECRET_ACCESS_KEY", strings.Repeat("c", 64))
}
func TestMediaDefaultsAndRequiredR2Configuration(t *testing.T) {
	setMediaEnv(t)
	cfg, err := loadMediaConfig()
	// Unset storage resolves per environment (TestMediaStoragePolicyPerEnvironment).
	if err != nil || cfg.Storage != "" || cfg.LocalDir != ".media" {
		t.Fatalf("default: %q %v", cfg.LocalDir, err)
	}
	t.Setenv("MEDIA_STORAGE", "postgres")
	if _, err := loadMediaConfig(); err == nil {
		t.Fatal("PostgreSQL image storage is gone (ADR-0057) but was accepted")
	}
	validMediaEnv(t)
	cfg, err = loadMediaConfig()
	if err != nil || cfg.Storage != "r2" || cfg.Bucket != "moonlight-media-dev" {
		t.Fatalf("R2 config: %v", err)
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if out := fmt.Sprintf(format, cfg); strings.Contains(out, cfg.AccessKeyID) || strings.Contains(out, cfg.SecretAccessKey) {
			t.Fatal("configuration formatter exposed credentials")
		}
	}
	for _, name := range []string{"R2_ENDPOINT", "R2_BUCKET", "R2_ACCESS_KEY_ID", "R2_SECRET_ACCESS_KEY"} {
		t.Run(name, func(t *testing.T) {
			validMediaEnv(t)
			t.Setenv(name, "")
			if _, err := loadMediaConfig(); err == nil {
				t.Fatal("missing setting accepted")
			}
		})
	}
}
func TestMediaRejectsUnsafeDestinationsAndRedactsValidationErrors(t *testing.T) {
	cases := map[string]string{
		"MEDIA_STORAGE":        "typo-secret-value",
		"http":                 "http://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com",
		"userinfo":             "https://private-secret@0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com",
		"path":                 "https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com/bucket",
		"port":                 "https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com:443",
		"query":                "https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com?secret=private-secret",
		"foreign":              "https://objects.example.com",
		"suffix":               "https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com.attacker.invalid",
		"R2_BUCKET":            "../private-secret",
		"R2_SECRET_ACCESS_KEY": "private-secret\nvalue",
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			validMediaEnv(t)
			key := "R2_ENDPOINT"
			if strings.HasPrefix(name, "R2_") || name == "MEDIA_STORAGE" {
				key = name
			}
			t.Setenv(key, value)
			_, err := loadMediaConfig()
			if err == nil {
				t.Fatal("unsafe configuration accepted")
			}
			if strings.Contains(err.Error(), "private-secret") || strings.Contains(err.Error(), "typo-secret-value") {
				t.Fatal("error exposed configured value")
			}
		})
	}
	for _, jurisdiction := range []string{"eu", "us", "fedramp"} {
		validMediaEnv(t)
		t.Setenv("R2_ENDPOINT", "https://0123456789abcdef0123456789abcdef."+jurisdiction+".r2.cloudflarestorage.com/")
		if _, err := loadMediaConfig(); err != nil {
			t.Fatal(err)
		}
	}
}
func TestLoadIncludesMediaSettings(t *testing.T) {
	validMediaEnv(t)
	setReportingToken(t)
	t.Setenv("ENVIRONMENT", "development")
	t.Setenv("DATABASE_URL", "postgres://test:test@localhost:5432/moonlight_dev?sslmode=disable")
	cfg, err := Load()
	if err != nil || cfg.Media.Storage != "r2" {
		t.Fatalf("Load media: %v", err)
	}
	t.Setenv("R2_SECRET_ACCESS_KEY", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load accepted incomplete R2 settings")
	}
}
