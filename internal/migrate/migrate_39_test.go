package migrate_test

import (
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
)

// v38 → 39 (Phase 17-R2) extends the admin command vocabulary CHECK with
// the six type commands (ADR-0050 §45/§46). No business data moves; the
// pre-existing commands stay valid under the recreated constraint.
func TestV38To39ExtendsCommandVocabulary(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 38); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != 38 {
		t.Fatalf("want 38, got %d", v)
	}
	exec := func(query string) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query); err != nil {
			t.Fatalf("seed v38: %v\n%s", err, query)
		}
	}
	exec(`INSERT INTO devices (id, name, status, created_at, updated_at)
		VALUES ('11111111-1111-4111-8111-111111111111', 'shop-pc', 'active', now(), now())`)
	exec(`INSERT INTO stores (id, display_name, timezone)
		VALUES ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', 'Store A', 'Africa/Cairo')`)
	exec(`INSERT INTO catalog_admin_commands (id, store_id, command_type, command_version, entity_id, payload, payload_hash, expected_revision, actor)
		VALUES ('22222222-2222-4222-8222-222222222222', 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', 'catalog.product.details.update.v1', 1, '33333333-3333-4333-8333-333333333333', '{}', '0000000000000000000000000000000000000000000000000000000000000000', 1, 'op')`)

	if err := migrate.UpTo(ctx, conn, 39); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != 39 {
		t.Fatalf("want 39, got %d", v)
	}
	// Old command survives the CHECK recreation.
	var n int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM catalog_admin_commands`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("commands preserved: %d", n)
	}
	// New vocabulary accepted.
	exec(`INSERT INTO catalog_admin_commands (id, store_id, command_type, command_version, entity_id, payload, payload_hash, expected_revision, actor)
		VALUES ('44444444-4444-4222-8222-444444444444', 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', 'catalog.product-type.details.update.v1', 1, '55555555-5555-4555-8555-555555555555', '{}', '0000000000000000000000000000000000000000000000000000000000000000', 1, 'op')`)
}

// v39 → 40 (Phase 17-R3) adds the create-identity columns with a coherence
// rule: entity commands keep the business-UUID invariant, creates carry
// the explicit absent marker plus a requested key. Existing rows default
// to target_kind='entity' with no other change.
func TestV39To40CreateIdentityCoherence(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 39); err != nil {
		t.Fatal(err)
	}
	exec := func(query string) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query); err != nil {
			t.Fatalf("seed v39: %v\n%s", err, query)
		}
	}
	exec(`INSERT INTO devices (id, name, status, created_at, updated_at)
		VALUES ('11111111-1111-4111-8111-111111111111', 'shop-pc', 'active', now(), now())`)
	exec(`INSERT INTO stores (id, display_name, timezone)
		VALUES ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', 'Store A', 'Africa/Cairo')`)
	exec(`INSERT INTO catalog_admin_commands (id, store_id, command_type, command_version, entity_id, payload, payload_hash, expected_revision, actor)
		VALUES ('22222222-2222-4222-8222-222222222222', 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', 'catalog.product.details.update.v1', 1, '33333333-3333-4333-8333-333333333333', '{}', '0000000000000000000000000000000000000000000000000000000000000000', 1, 'op')`)

	if err := migrate.UpTo(ctx, conn, 40); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != 40 {
		t.Fatalf("want 40, got %d", v)
	}
	var kind, entity string
	var requested *string
	if err := conn.QueryRowContext(ctx, `SELECT target_kind, entity_id, requested_key FROM catalog_admin_commands`).Scan(&kind, &entity, &requested); err != nil {
		t.Fatal(err)
	}
	if kind != "entity" || entity != "33333333-3333-4333-8333-333333333333" || requested != nil {
		t.Fatalf("existing rows default to entity-kind: %q %q %v", kind, entity, requested)
	}
	// A create with a placeholder business UUID is refused by the schema.
	if _, err := conn.ExecContext(ctx, `INSERT INTO catalog_admin_commands (id, store_id, command_type, command_version, entity_id, target_kind, requested_key, payload, payload_hash, expected_revision, actor)
		VALUES ('44444444-4444-4222-8222-444444444444', 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', 'catalog.product-type.create.v1', 1, '55555555-5555-4555-8555-555555555555', 'create', 'book', '{}', '0000000000000000000000000000000000000000000000000000000000000000', 0, 'op')`); err == nil {
		t.Fatal("create with a fake business UUID must violate the coherence rule")
	}
	// The honest shape stores.
	exec(`INSERT INTO catalog_admin_commands (id, store_id, command_type, command_version, entity_id, target_kind, requested_key, payload, payload_hash, expected_revision, actor)
		VALUES ('44444444-4444-4222-8222-444444444444', 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', 'catalog.product-type.create.v1', 1, '', 'create', 'book', '{}', '0000000000000000000000000000000000000000000000000000000000000000', 0, 'op')`)
}
