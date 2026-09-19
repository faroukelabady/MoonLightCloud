package postgres

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// openTestRepo wires the real repository against an isolated database.
func openTestRepo(t *testing.T) (*pgxpool.Pool, auth.Service) {
	t.Helper()
	url := testutil.Isolated(t)
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := NewDevices(pool, 5*time.Second)
	svc := auth.NewService(repo, auth.NewHasher("test-pepper"),
		clock.System{}, ids.System{})
	return pool, svc
}

func TestRepositoryRoundtrip(t *testing.T) {
	pool, svc := openTestRepo(t)
	ctx := context.Background()
	p, err := svc.Create(ctx, "shop-dev")
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.Authenticate(ctx, p.Device.ID, p.RawSecret)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "shop-dev" || got.LastSeenAt == nil {
		t.Fatalf("bad roundtrip: %+v", got)
	}
	if _, err := svc.Authenticate(ctx, p.Device.ID, "wrong"); err == nil {
		t.Fatal("wrong secret must fail")
	}
	// Stored verifier is a hash, never the raw secret.
	var hash, salt []byte
	err = pool.QueryRow(ctx,
		"SELECT secret_hash, secret_salt FROM devices WHERE id = $1", p.Device.ID).Scan(&hash, &salt)
	if err != nil {
		t.Fatal(err)
	}
	if string(hash) == p.RawSecret || string(salt) == p.RawSecret {
		t.Fatal("raw secret must never persist")
	}
	// Revocation takes effect immediately.
	if err := svc.Revoke(ctx, p.Device.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, p.Device.ID, p.RawSecret); err == nil {
		t.Fatal("revoked device must fail")
	}
}

func TestMigrationFromEmpty(t *testing.T) {
	url := testutil.Isolated(t) // migrate.Up from empty already ran inside
	conn, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	v, err := migrate.Current(context.Background(), conn)
	if err != nil || v != migrate.TargetVersion {
		t.Fatalf("version %d (want %d): %v", v, migrate.TargetVersion, err)
	}
	var exists bool
	if err := conn.QueryRow(
		"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name='devices')").Scan(&exists); err != nil || !exists {
		t.Fatalf("devices table missing: %v", err)
	}
}
