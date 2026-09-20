package postgres

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"strings"
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

var testPepperBytes = bytesOf('t')

func bytesOf(c byte) []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = c
	}
	return b
}

func mustTestHasher(t *testing.T) auth.Hasher {
	t.Helper()
	h, err := auth.NewHasher(testPepperBytes)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// openTestRepo wires the real store against an isolated database.
func openTestRepo(t *testing.T) (*pgxpool.Pool, auth.Service) {
	t.Helper()
	url := testutil.Isolated(t)
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	h, err := auth.NewHasher(testPepperBytes)
	if err != nil {
		t.Fatal(err)
	}
	repo := NewDevices(pool, 5*time.Second)
	svc := auth.NewService(repo, h, "", 1, clock.System{}, ids.System{})
	return pool, svc
}

func TestRepositoryRoundtrip(t *testing.T) {
	pool, svc := openTestRepo(t)
	ctx := context.Background()
	p, err := svc.Create(ctx, "shop-dev")
	if err != nil {
		t.Fatal(err)
	}
	dev, cred, err := svc.Authenticate(ctx, p.Device.ID, p.Credential.ID, p.RawSecret)
	if err != nil {
		t.Fatal(err)
	}
	if dev.Name != "shop-dev" || dev.LastSeenAt == nil || cred.LastUsedAt == nil {
		t.Fatalf("bad roundtrip: %+v", dev)
	}
	if _, _, err := svc.Authenticate(ctx, p.Device.ID, p.Credential.ID, "wrong"); err == nil {
		t.Fatal("wrong secret must fail")
	}
	// Stored verifier is a keyed hash, never the raw secret; pepper absent.
	var verifier, salt []byte
	var verVer, pepVer int
	err = pool.QueryRow(ctx,
		`SELECT verifier, salt, verifier_version, pepper_version
		 FROM device_credentials WHERE id = $1`, p.Credential.ID).Scan(&verifier, &salt, &verVer, &pepVer)
	if err != nil {
		t.Fatal(err)
	}
	if string(verifier) == p.RawSecret || string(salt) == p.RawSecret {
		t.Fatal("raw secret must never persist")
	}
	if verVer != auth.VerifierV1 || pepVer != 1 {
		t.Fatalf("want v1/pepper1, got %d/%d", verVer, pepVer)
	}
	var cols string
	_ = pool.QueryRow(ctx, `SELECT string_agg(column_name, ',') FROM information_schema.columns
		WHERE table_name='device_credentials'`).Scan(&cols)
	for _, banned := range []string{"secret", "token"} {
		if strings.Contains(strings.ToLower(cols), banned) {
			t.Fatalf("credential table must not store %q material", banned)
		}
	}
	// pepper_version is metadata (an integer), not key material: prove no
	// stored value carries the raw secret.
	var nleak int
	err = pool.QueryRow(ctx, `SELECT count(*) FROM device_credentials
		WHERE encode(verifier,'hex') = $1 OR encode(salt,'hex') = $1`, p.RawSecret).Scan(&nleak)
	if err != nil || nleak != 0 {
		t.Fatal("raw secret must never persist in any column")
	}
	// Revocation takes effect immediately.
	if err := svc.RevokeDevice(ctx, p.Device.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Authenticate(ctx, p.Device.ID, p.Credential.ID, p.RawSecret); err == nil {
		t.Fatal("revoked device must fail")
	}
}

func TestRotationLifecycle(t *testing.T) {
	_, svc := openTestRepo(t)
	ctx := context.Background()
	p, err := svc.Create(ctx, "shop-dev")
	if err != nil {
		t.Fatal(err)
	}
	r, err := svc.Rotate(ctx, p.Device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Authenticate(ctx, p.Device.ID, r.Credential.ID, r.RawSecret); err != nil {
		t.Fatalf("rotated credential must work: %v", err)
	}
	if _, _, err := svc.Authenticate(ctx, p.Device.ID, p.Credential.ID, p.RawSecret); err == nil {
		t.Fatal("pre-rotation credential must fail")
	}
}

func TestMigration1Ato1BPreservesCredentials(t *testing.T) {
	// Build a Phase-1A schema (version 1 only), insert a device with the
	// exact legacy construction, migrate to current, authenticate.
	url := testutil.Raw(t)
	conn, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := migrate.UpTo(context.Background(), conn, 1); err != nil {
		t.Fatal(err)
	}
	// Legacy row, no pepper: verifier = SHA-256(salt || secret).
	raw := "aa"
	salt := []byte("0123456789abcdef")
	sum := sha256.Sum256(append(append([]byte{}, salt...), raw...))
	deviceID := "11111111-1111-7111-8111-111111111111"
	if _, err := conn.Exec(`INSERT INTO devices
		(id, name, status, secret_hash, secret_salt, created_at, updated_at)
		VALUES ($1, 'legacy-shop', 'active', $2, $3, now(), now())`,
		deviceID, sum[:], salt); err != nil {
		t.Fatal(err)
	}
	// Legacy row with a pepper string: HMAC(pepper, salt || secret).
	pepperedSalt := []byte("fedcba9876543210")
	mac := hmac.New(sha256.New, []byte("oldpepper"))
	mac.Write(pepperedSalt)
	mac.Write([]byte(raw))
	deviceID2 := "22222222-2222-7222-8222-222222222222"
	if _, err := conn.Exec(`INSERT INTO devices
		(id, name, status, secret_hash, secret_salt, created_at, updated_at)
		VALUES ($1, 'legacy-peppered', 'active', $2, $3, now(), now())`,
		deviceID2, mac.Sum(nil), pepperedSalt); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	v, err := migrate.Current(context.Background(), conn)
	if err != nil || v != migrate.TargetVersion {
		t.Fatalf("version %d (want %d): %v", v, migrate.TargetVersion, err)
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	h, _ := auth.NewHasher(testPepperBytes)
	repo := NewDevices(pool, 5*time.Second)
	svc := auth.NewService(repo, h, "", 1, clock.System{}, ids.System{})
	creds, err := repo.ActiveCredentials(context.Background(), deviceID)
	if err != nil || len(creds) != 1 {
		t.Fatalf("migrated device must own one credential: %v %d", err, len(creds))
	}
	if creds[0].VerifierVersion != auth.VerifierV0 {
		t.Fatalf("migrated credential must be v0, got %d", creds[0].VerifierVersion)
	}
	if _, _, err := svc.Authenticate(context.Background(), deviceID, creds[0].ID, raw); err != nil {
		t.Fatalf("migrated legacy credential must authenticate: %v", err)
	}
	// Peppered legacy row verifies under the same pepper string.
	svcPeppered := auth.NewService(repo, mustTestHasher(t), "oldpepper", 1, clock.System{}, ids.System{})
	creds2, err := repo.ActiveCredentials(context.Background(), deviceID2)
	if err != nil || len(creds2) != 1 {
		t.Fatalf("migrated peppered device must own one credential: %v", err)
	}
	if _, _, err := svcPeppered.Authenticate(context.Background(), deviceID2, creds2[0].ID, raw); err != nil {
		t.Fatalf("migrated peppered credential must authenticate: %v", err)
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
	for _, table := range []string{"devices", "device_credentials", "sync_events"} {
		var exists bool
		if err := conn.QueryRow(
			"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name=$1)", table).Scan(&exists); err != nil || !exists {
			t.Fatalf("%s table missing: %v", table, err)
		}
	}
}
