package migrate_test

import (
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
)

// v24 → 25 adds the identity-store safety index and the default_algorithm
// marker without altering, rewriting, or deleting any existing catalog row
// or ownership annotation. Upgrade is additive and preservation-safe.
func TestV24To25PreservesCatalog(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 24); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != 24 {
		t.Fatalf("want 24, got %d", v)
	}
	exec := func(query string) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query); err != nil {
			t.Fatalf("seed v24: %v\n%s", err, query)
		}
	}
	exec(`INSERT INTO stores (id, display_name, timezone) VALUES ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', 'Store A', 'Africa/Cairo')`)
	exec(`INSERT INTO devices (id, name, status, created_at, updated_at)
		VALUES ('d0000000-0000-4000-8000-000000000001', 'seed-dev', 'active', now(), now())`)
	exec(`INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, received_at, payload, payload_hash)
		VALUES ('e0000000-0000-4000-8000-000000000001', 'd0000000-0000-4000-8000-000000000001',
		'catalog.category.snapshot.v1', now(), now(), '{}', '\x00')`)
	exec(`INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, received_at, payload, payload_hash)
		VALUES ('e0000000-0000-4000-8000-000000000002', 'd0000000-0000-4000-8000-000000000001',
		'catalog.tag.snapshot.v1', now(), now(), '{}', '\x00')`)
	// A pre-R2 raw default category annotated with Store A, plus a legacy
	// NULL default and a default tag owned by A.
	exec(`INSERT INTO catalog_categories (category_id, status, name_ar, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at, store_id)
		VALUES ('00000000-0000-0000-0000-000000000101', 'active', 'اسلامي', 3, 'e0000000-0000-4000-8000-000000000001', 'd0000000-0000-4000-8000-000000000001', '\x00', now(), 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa')`)
	exec(`INSERT INTO catalog_categories (category_id, status, name_ar, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at, store_id)
		VALUES ('00000000-0000-0000-0000-000000000201', 'active', 'طبيعة', 1, 'e0000000-0000-4000-8000-000000000001', 'd0000000-0000-4000-8000-000000000001', '\x00', now(), NULL)`)
	exec(`INSERT INTO catalog_tags (tag_id, slug, is_active, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at, store_id)
		VALUES ('10000000-0000-0000-0000-000000000002', 'gold', true, 2, 'e0000000-0000-4000-8000-000000000002', 'd0000000-0000-4000-8000-000000000001', '\x00', now(), 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa')`)

	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != migrate.TargetVersion {
		t.Fatalf("want %d, got %d", migrate.TargetVersion, v)
	}
	// Every pre-existing value, identity, and ownership annotation intact.
	checks := map[string]string{
		`SELECT store_id::text FROM catalog_categories WHERE category_id='00000000-0000-0000-0000-000000000101'`:                  "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		`SELECT source_revision::text FROM catalog_categories WHERE category_id='00000000-0000-0000-0000-000000000101'`:           "3",
		`SELECT COALESCE(store_id::text,'NULL') FROM catalog_categories WHERE category_id='00000000-0000-0000-0000-000000000201'`: "NULL",
		`SELECT store_id::text FROM catalog_tags WHERE tag_id='10000000-0000-0000-0000-000000000002'`:                             "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
		`SELECT slug FROM catalog_tags WHERE tag_id='10000000-0000-0000-0000-000000000002'`:                                       "gold",
	}
	for query, want := range checks {
		var got string
		if err := conn.QueryRowContext(ctx, query).Scan(&got); err != nil || got != want {
			t.Fatalf("preserved %q: got %q want %q (%v)", query, got, want, err)
		}
	}
	// New marker defaults to 0 for pre-existing rows.
	var alg int16
	if err := conn.QueryRowContext(ctx, `SELECT default_algorithm FROM catalog_categories WHERE category_id='00000000-0000-0000-0000-000000000101'`).Scan(&alg); err != nil || alg != 0 {
		t.Fatalf("default_algorithm default: %d (%v)", alg, err)
	}
	// Safety indexes present.
	for _, index := range []string{"idx_catalog_categories_identity_store", "idx_catalog_tags_identity_store"} {
		var exists bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname=$1)`, index).Scan(&exists); err != nil || !exists {
			t.Fatalf("index %s present (%v)", index, err)
		}
	}
}

// v25 → 24 reverses the additive schema but is not lossless for rows
// written under the canonical identity; the test proves the honest
// reversal shape only.
func TestV25To24ReversesAdditiveSchema(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if err := migrate.DownTo(ctx, conn, 24); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != 24 {
		t.Fatalf("want 24, got %d", v)
	}
	for _, index := range []string{"idx_catalog_categories_identity_store", "idx_catalog_tags_identity_store"} {
		var exists bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname=$1)`, index).Scan(&exists); err != nil || exists {
			t.Fatalf("index %s removed (%v)", index, err)
		}
	}
	var columnExists bool
	if err := conn.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name='catalog_categories' AND column_name='default_algorithm')`).Scan(&columnExists); err != nil || columnExists {
		t.Fatalf("default_algorithm removed (%v)", err)
	}
}
