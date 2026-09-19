// Package testutil gives integration tests isolated PostgreSQL databases.
// Every test gets a fresh throwaway database; nothing ever touches the
// development or production database. Guards refuse unsafe names.
package testutil

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
)

// Isolated creates a fresh migrated throwaway database and returns its URL.
// Cleanup drops it after the test. Requires TEST_DATABASE_URL (an admin
// connection, e.g. the postgres maintenance db). Skips when unset so
// unit-only runs stay green without a container runtime.
func Isolated(t *testing.T) string {
	t.Helper()
	if os.Getenv("ENVIRONMENT") == "production" {
		t.Fatal("tests refuse ENVIRONMENT=production")
	}
	adminURL := os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("TEST_DATABASE_URL unset: skipping integration test")
	}
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	name := "moonlight_test_" + hex.EncodeToString(suffix[:])
	guardName(t, name)
	admin, err := sql.Open("pgx", adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec(fmt.Sprintf("CREATE DATABASE %q", name)); err != nil {
		t.Fatalf("create test db: %v", err)
	}
	t.Cleanup(func() {
		kill, _ := sql.Open("pgx", adminURL)
		defer kill.Close()
		_, _ = kill.Exec(fmt.Sprintf("DROP DATABASE %q WITH (FORCE)", name))
	})
	testURL := replaceDBName(t, adminURL, name)
	conn, err := sql.Open("pgx", testURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := migrate.Up(context.Background(), conn); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	v, err := migrate.Current(context.Background(), conn)
	if err != nil || v != migrate.TargetVersion {
		t.Fatalf("test db version %d (want %d): %v", v, migrate.TargetVersion, err)
	}
	return testURL
}

// Raw creates a fresh EMPTY database (no migrations) and returns its URL.
// For tests that must prove behavior against an unmigrated schema.
func Raw(t *testing.T) string {
	t.Helper()
	if os.Getenv("ENVIRONMENT") == "production" {
		t.Fatal("tests refuse ENVIRONMENT=production")
	}
	adminURL := os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("TEST_DATABASE_URL unset: skipping integration test")
	}
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	name := "moonlight_test_" + hex.EncodeToString(suffix[:])
	guardName(t, name)
	admin, err := sql.Open("pgx", adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec(fmt.Sprintf("CREATE DATABASE %q", name)); err != nil {
		t.Fatalf("create test db: %v", err)
	}
	t.Cleanup(func() {
		kill, _ := sql.Open("pgx", adminURL)
		defer kill.Close()
		_, _ = kill.Exec(fmt.Sprintf("DROP DATABASE %q WITH (FORCE)", name))
	})
	return replaceDBName(t, adminURL, name)
}

func guardName(t *testing.T, name string) {
	t.Helper()
	if !strings.HasPrefix(name, "moonlight_test_") {
		t.Fatalf("refusing unsafe test database name %q", name)
	}
	for _, banned := range []string{"moonlight_dev", "moonlight_prod", "postgres", "template"} {
		if name == banned {
			t.Fatalf("refusing protected database name %q", name)
		}
	}
}

func replaceDBName(t *testing.T, adminURL, name string) string {
	t.Helper()
	// net/url parsing keeps passwords with special chars intact.
	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatalf("TEST_DATABASE_URL must be a postgres URL: %v", err)
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		t.Fatalf("TEST_DATABASE_URL must be a postgres URL")
	}
	u.Path = "/" + name
	return u.String()
}
