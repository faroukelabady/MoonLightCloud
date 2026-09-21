package migrate_test

import (
	"context"
	"database/sql"
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

// Fresh database migrates to latest with all tables.
func TestFreshToLatest(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != migrate.TargetVersion {
		t.Fatalf("want %d, got %d", migrate.TargetVersion, v)
	}
	for _, table := range []string{"sync_events", "sales_projection", "sync_event_processing", "sale_event_ownership"} {
		var exists bool
		if err := conn.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name=$1)`, table).Scan(&exists); err != nil || !exists {
			t.Fatalf("table %s missing (%v)", table, err)
		}
	}
}

// v5 → latest carries old data forward and backfills ownership empty.
func TestV5ToLatest(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 5); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != 5 {
		t.Fatalf("want 5, got %d", v)
	}
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != migrate.TargetVersion {
		t.Fatalf("want %d, got %d", migrate.TargetVersion, v)
	}
}

// v6 → latest is the Phase 2D checkpoint upgrade.
func TestV6ToLatest(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 6); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != migrate.TargetVersion {
		t.Fatalf("want %d, got %d", migrate.TargetVersion, v)
	}
}

// Existing projections (winner + conflict loser rows) migrate ownership
// from the projected source_event_id.
func TestExistingProjectionsMigrateOwnership(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 6); err != nil {
		t.Fatal(err)
	}
	dev := "11111111-1111-7111-8111-111111111111"
	e1 := "22222222-2222-7222-8222-222222222222"
	e2 := "33333333-3333-7333-8333-333333333333"
	saleID := "aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa"
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO devices (id, name, status) VALUES ($1,'shop','active')`, dev)
	for _, e := range []string{e1, e2} {
		exec(`INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, received_at, payload, payload_hash)
			VALUES ($1,$2,'sale.finalized.v1',now(),now(),'{}','\x00')`, e, dev)
	}
	exec(`INSERT INTO sales_projection (sale_id, source_event_id, source_device_id, sale_number, channel,
		occurred_at, paid_at, shop_name_ar, shop_name_en, shop_address_ar, shop_address_en, shop_phone,
		shop_receipt_footer_ar, shop_receipt_footer_en, currency, subtotal_minor, discount_minor, tax_minor, total_minor, received_at)
		VALUES ($1,$2,$3,'MLR-1','STORE',now(),now(),'a','b','c','d','e','f','g','EGP',1,0,0,1,now())`,
		saleID, e1, dev)
	exec(`INSERT INTO sync_event_processing (event_id, processor, status, last_error_code)
		VALUES ($1,'sale_projection.v1','processed',NULL),($2,'sale_projection.v1','blocked','SALE_ID_CONFLICT')`, e1, e2)
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := conn.QueryRowContext(ctx,
		`SELECT winning_event_id::text FROM sale_event_ownership WHERE sale_id=$1`, saleID).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != e1 {
		t.Fatalf("ownership must follow projected winner %s, got %s", e1, owner)
	}
}

// v6 Down with only v1 rows proceeds.
func TestDownV6V1Only(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 6); err != nil {
		t.Fatal(err)
	}
	if err := migrate.DownTo(ctx, conn, 5); err != nil {
		t.Fatalf("v1-only downgrade must proceed: %v", err)
	}
	if v := version(t, conn, ctx); v != 5 {
		t.Fatalf("want 5, got %d", v)
	}
	// Fresh up remains valid afterwards.
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != migrate.TargetVersion {
		t.Fatalf("want %d, got %d", migrate.TargetVersion, v)
	}
}

// v6 Down with a v2 row fails safely; schema and data intact.
func TestDownV6WithV2Fails(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 6); err != nil {
		t.Fatal(err)
	}
	dev := "11111111-1111-7111-8111-111111111111"
	if _, err := conn.ExecContext(ctx, `INSERT INTO devices (id, name, status) VALUES ($1,'shop','active')`, dev); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, received_at, payload, payload_hash, payload_hash_version)
		 VALUES ('22222222-2222-7222-8222-222222222222',$1,'system.test.v1',now(),now(),'{}','\x00',2)`, dev); err != nil {
		t.Fatal(err)
	}
	if err := migrate.DownTo(ctx, conn, 5); err == nil {
		t.Fatal("downgrade with v2 rows must fail")
	}
	if v := version(t, conn, ctx); v != 6 {
		t.Fatalf("failed downgrade must leave version at 6, got %d", v)
	}
	var n int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sync_events`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("data must be intact: %d (%v)", n, err)
	}
	var hasCol bool
	if err := conn.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='sync_events' AND column_name='payload_hash_version')`).Scan(&hasCol); err != nil || !hasCol {
		t.Fatalf("schema must be intact (%v)", err)
	}
}

// Ownership Down with decisions fails; empty ownership rolls back.
func TestDownOwnershipPolicy(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	dev := "11111111-1111-7111-8111-111111111111"
	e1 := "22222222-2222-7222-8222-222222222222"
	saleID := "aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa"
	if _, err := conn.ExecContext(ctx, `INSERT INTO devices (id, name, status) VALUES ($1,'shop','active')`, dev); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, received_at, payload, payload_hash)
		 VALUES ($1,$2,'sale.finalized.v1',now(),now(),'{}','\x00')`, e1, dev); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO sale_event_ownership (sale_id, winning_event_id, winning_device_id) VALUES ($1,$2,$3)`,
		saleID, e1, dev); err != nil {
		t.Fatal(err)
	}
	if err := migrate.DownTo(ctx, conn, 6); err == nil {
		t.Fatal("ownership downgrade with decisions must fail")
	}
	if v := version(t, conn, ctx); v != migrate.TargetVersion {
		t.Fatalf("version must stay %d, got %d", migrate.TargetVersion, v)
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM sale_event_ownership`); err != nil {
		t.Fatal(err)
	}
	if err := migrate.DownTo(ctx, conn, 6); err != nil {
		t.Fatalf("empty ownership downgrade must proceed: %v", err)
	}
	if v := version(t, conn, ctx); v != 6 {
		t.Fatalf("want 6, got %d", v)
	}
}
