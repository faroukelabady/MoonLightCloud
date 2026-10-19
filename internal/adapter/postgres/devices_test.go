package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
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
