package app

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/config"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
)

func testConfig(url string) config.Config {
	c := config.Config{
		Environment:      config.EnvDevelopment,
		HTTPAddr:         ":0",
		DatabaseURL:      url,
		LogLevel:         "error",
		PepperRaw:        config.DevPepper,
		PepperVersion:    config.CurrentPepperVersion,
		ShutdownAfter:    config.DefaultShutdownTimeout,
		DBMaxConns:       4,
		DBMinConns:       1,
		DBMaxConnLife:    config.DefaultDBMaxConnLife,
		DBMaxConnIdle:    config.DefaultDBMaxConnIdle,
		DBConnectTimeout: config.DefaultDBConnectTimeout,
		DBQueryTimeout:   config.DefaultDBQueryTimeout,
	}
	if err := c.Validate(); err != nil {
		panic(err)
	}
	return c
}

func TestNewHealthyAndReady(t *testing.T) {
	url := testutil.Isolated(t)
	a, err := New(context.Background(), testConfig(url))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	req := httptest.NewRequest("GET", "/health/ready", nil)
	rec := httptest.NewRecorder()
	a.Health.ServeReady(rec, req)
	if rec.Code != 200 {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestNewRejectsUnmigratedSchema(t *testing.T) {
	url := testutil.Raw(t)
	if _, err := New(context.Background(), testConfig(url)); err == nil {
		t.Fatal("startup must fail on unmigrated schema")
	}
}

func TestReadyFailsWithoutDB(t *testing.T) {
	url := testutil.Isolated(t)
	a, err := New(context.Background(), testConfig(url))
	if err != nil {
		t.Fatal(err)
	}
	a.Pool.Close() // simulate unavailable DB
	req := httptest.NewRequest("GET", "/health/ready", nil)
	rec := httptest.NewRecorder()
	a.Health.ServeReady(rec, req)
	if rec.Code != 503 {
		t.Fatalf("want 503, got %d: %s", rec.Code, rec.Body.String())
	}
}
