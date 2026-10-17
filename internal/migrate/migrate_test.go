package migrate_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func openRaw(t *testing.T) (*sql.DB, context.Context) {
	t.Helper()
	url := testutil.Raw(t)
	conn, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, context.Background()
}

func version(t *testing.T, conn *sql.DB, ctx context.Context) int64 {
	t.Helper()
	v, err := migrate.Current(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func tableExists(t *testing.T, conn *sql.DB, table string) bool {
	t.Helper()
	var exists bool
	if err := conn.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name=$1)`, table).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

// Fresh database migrates to the single baseline with all core tables.
func TestFreshToLatest(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != migrate.TargetVersion {
		t.Fatalf("want %d, got %d", migrate.TargetVersion, v)
	}
	for _, table := range []string{"sync_events", "sales_projection", "sync_event_processing", "sale_event_ownership", "stores", "admin_users", "releases"} {
		if !tableExists(t, conn, table) {
			t.Fatalf("table %s missing", table)
		}
	}
}

// Down removes every baseline object and Up rebuilds it (dev/test repair).
func TestBaselineDownThenUp(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if err := migrate.DownTo(ctx, conn, 0); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != 0 {
		t.Fatalf("version after down = %d, want 0", v)
	}
	if tableExists(t, conn, "sync_events") {
		t.Fatal("down left sync_events behind")
	}
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != migrate.TargetVersion {
		t.Fatalf("version after re-up = %d, want %d", v, migrate.TargetVersion)
	}
}

// ADR-0055: a database built from an older migration chain or an older
// baseline revision is refused, never migrated on top of a stale schema.
func TestBaselineRefusesExistingSchema(t *testing.T) {
	conn, ctx := openRaw(t)
	if _, err := conn.Exec(`CREATE TABLE sync_events (event_id uuid PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	err := migrate.Up(ctx, conn)
	if err == nil || !strings.Contains(err.Error(), "refuses an existing schema") {
		t.Fatalf("want baseline refusal, got %v", err)
	}
	if v := version(t, conn, ctx); v != 0 {
		t.Fatalf("refused baseline recorded version %d", v)
	}
}
