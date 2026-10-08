// Package testutil gives integration tests isolated PostgreSQL databases.
// Every test gets a fresh throwaway database; nothing ever touches the
// development or production database. Guards refuse unsafe names.
package testutil

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/faroukelabady/MoonLightCloud/db"
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
	// Clone a once-migrated template instead of replaying every migration
	// per test. MOONLIGHT_TEST_NO_TEMPLATE=1 restores per-test migration.
	template := ""
	if os.Getenv("MOONLIGHT_TEST_NO_TEMPLATE") != "1" {
		template, err = migratedTemplate(adminURL)
		if err != nil {
			t.Fatalf("prepare migrated template: %v", err)
		}
	}
	if err := createDatabase(admin, name, template); err != nil {
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
	if template == "" {
		if err := migrate.Up(context.Background(), conn); err != nil {
			t.Fatalf("migrate test db: %v", err)
		}
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

var (
	templateMu    sync.Mutex
	templateReady = map[string]string{}
)

// migratedTemplate returns a fully migrated template database for the
// embedded migration set, creating it at most once per server. The name
// carries a hash of every migration file and the target version, so a
// changed history never reuses a stale template. Concurrent test binaries
// serialize creation on a server-wide advisory lock, and the template is
// renamed into place only after migration and version verification, so a
// visible template is always complete.
func migratedTemplate(adminURL string) (string, error) {
	templateMu.Lock()
	defer templateMu.Unlock()
	if name, ok := templateReady[adminURL]; ok {
		return name, nil
	}
	sum := sha256.New()
	err := fs.WalkDir(db.Migrations(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		body, err := fs.ReadFile(db.Migrations(), path)
		if err != nil {
			return err
		}
		fmt.Fprintf(sum, "%s\x00%d\x00", path, len(body))
		sum.Write(body)
		return nil
	})
	if err != nil {
		return "", err
	}
	fmt.Fprintf(sum, "target=%d", migrate.TargetVersion)
	name := "moonlight_test_tpl_" + hex.EncodeToString(sum.Sum(nil))[:16]

	ctx := context.Background()
	admin, err := sql.Open("pgx", adminURL)
	if err != nil {
		return "", err
	}
	defer admin.Close()
	lock, err := admin.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	const lockKey = 0x4d4c5450 // "MLTP": test template creation
	if _, err := lock.ExecContext(ctx, "SELECT pg_advisory_lock($1)", lockKey); err != nil {
		return "", err
	}
	defer lock.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", lockKey) //nolint:errcheck // released on close too
	var exists bool
	if err := lock.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		return "", err
	}
	if !exists {
		var suffix [4]byte
		if _, err := rand.Read(suffix[:]); err != nil {
			return "", err
		}
		building := "moonlight_test_tplbuild_" + hex.EncodeToString(suffix[:])
		if _, err := lock.ExecContext(ctx, fmt.Sprintf("CREATE DATABASE %q", building)); err != nil {
			return "", err
		}
		if err := migrateTemplate(replaceURLName(adminURL, building)); err != nil {
			_, _ = lock.ExecContext(ctx, fmt.Sprintf("DROP DATABASE %q WITH (FORCE)", building))
			return "", err
		}
		if _, err := lock.ExecContext(ctx, fmt.Sprintf("ALTER DATABASE %q RENAME TO %q", building, name)); err != nil {
			_, _ = lock.ExecContext(ctx, fmt.Sprintf("DROP DATABASE %q WITH (FORCE)", building))
			return "", err
		}
	}
	templateReady[adminURL] = name
	return name, nil
}

func migrateTemplate(templateURL string) error {
	conn, err := sql.Open("pgx", templateURL)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx := context.Background()
	if err := migrate.Up(ctx, conn); err != nil {
		return err
	}
	v, err := migrate.Current(ctx, conn)
	if err != nil {
		return err
	}
	if v != migrate.TargetVersion {
		return fmt.Errorf("template version %d (want %d)", v, migrate.TargetVersion)
	}
	return nil
}

// createDatabase creates name, cloned from template when one is given.
// A template is briefly unavailable while another session clones it
// (SQLSTATE 55006); that is retried with a short bounded backoff.
func createDatabase(admin *sql.DB, name, template string) error {
	stmt := fmt.Sprintf("CREATE DATABASE %q", name)
	if template != "" {
		stmt = fmt.Sprintf("CREATE DATABASE %q TEMPLATE %q", name, template)
	}
	var err error
	for attempt := 0; attempt < 50; attempt++ {
		if _, err = admin.Exec(stmt); err == nil || template == "" || !strings.Contains(err.Error(), "55006") {
			return err
		}
		time.Sleep(time.Duration(20+attempt*10) * time.Millisecond)
	}
	return err
}

func replaceURLName(adminURL, name string) string {
	u, err := url.Parse(adminURL)
	if err != nil {
		return adminURL
	}
	u.Path = "/" + name
	return u.String()
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
