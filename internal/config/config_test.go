package config

import (
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
	setenv(t, "DEVICE_SECRET_PEPPER", "pepper")
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
	setenv(t, "DEVICE_SECRET_PEPPER", "pepper")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
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
