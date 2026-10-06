package migrate_test

import (
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
)

// v32 → 33 (Phase 17 product variants) is additive: every existing row
// keeps its values, legacy NULL ownership stays NULL, and the extended
// catalog_admin_commands type vocabulary is the only constraint change.
func TestV32To33PreservesExistingRows(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 32); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != 32 {
		t.Fatalf("want 32, got %d", v)
	}
	exec := func(query string) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query); err != nil {
			t.Fatalf("seed v32: %v\n%s", err, query)
		}
	}
	exec(`INSERT INTO devices (id, name, status, created_at, updated_at)
		VALUES ('11111111-1111-4111-8111-111111111111', 'shop-pc', 'active', now(), now())`)
	exec(`INSERT INTO stores (id, display_name, timezone)
		VALUES ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', 'Store A', 'Africa/Cairo')`)
	exec(`INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, payload, payload_hash) VALUES
		('aaaaaaaa-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'catalog.product.snapshot.v1', now(), '{"product_id":"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"}', '\x00')`)
	exec(`INSERT INTO catalog_categories (category_id, status, name_ar, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
		VALUES ('cccccccc-cccc-4ccc-8ccc-cccccccccccc', 'active', 'cat', 1, 'aaaaaaaa-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', '\x00', now())`)
	exec(`INSERT INTO catalog_tags (tag_id, slug, is_active, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
		VALUES ('dddddddd-dddd-4ddd-8ddd-dddddddddddd', 'horse', true, 1, 'aaaaaaaa-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', '\x00', now())`)
	exec(`INSERT INTO catalog_products (product_id, sku, name, top_category_id, is_active, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
		VALUES ('eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee', 'ML-001', 'Horse', 'cccccccc-cccc-4ccc-8ccc-cccccccccccc', true, 4, 'aaaaaaaa-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', '\x00', now())`)
	exec(`INSERT INTO catalog_product_inventory (product_id, stock_quantity, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
		VALUES ('eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee', 5, 3, 'aaaaaaaa-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', '\x00', now())`)
	exec(`INSERT INTO catalog_product_sales_policies (product_id, sell_offline, sell_online, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
		VALUES ('eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee', true, true, 2, 'aaaaaaaa-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', '\x00', now())`)
	exec(`INSERT INTO sales_projection (sale_id, source_event_id, source_device_id, sale_number, channel, occurred_at, paid_at, shop_name_ar, shop_name_en, shop_address_ar, shop_address_en, shop_phone, shop_receipt_footer_ar, shop_receipt_footer_en, currency, subtotal_minor, discount_minor, tax_minor, total_minor, received_at)
		VALUES ('aaaaaaaa-0000-4000-8000-0000000000a1', 'aaaaaaaa-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'S-1', 'STORE', now(), now(), 'a', 'b', 'c', 'd', 'e', 'f', 'g', 'EGP', 10000, 0, 0, 10000, now())`)
	exec(`INSERT INTO sale_lines_projection (sale_id, sale_item_id, position, sku, product_name, quantity, unit_price_minor, unit_currency, line_total_minor, line_currency)
		VALUES ('aaaaaaaa-0000-4000-8000-0000000000a1', 'aaaaaaaa-0000-4000-8000-0000000000b1', 0, 'ML-001', 'Horse', 1, 10000, 'EGP', 10000, 'EGP')`)
	exec(`INSERT INTO catalog_admin_commands (id, store_id, command_type, command_version, entity_id, payload, payload_hash, expected_revision, actor)
		VALUES ('aaaaaaaa-0000-4000-8000-0000000000c1', 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', 'catalog.product.details.update.v1', 1, 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee', '{}', '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef', 4, 'op')`)

	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != migrate.TargetVersion {
		t.Fatalf("want %d, got %d", migrate.TargetVersion, v)
	}
	// Every pre-existing business value preserved byte-identically.
	queries := map[string]string{
		"product sku":     `SELECT sku FROM catalog_products WHERE product_id='eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee'`,
		"product rev":     `SELECT source_revision FROM catalog_products WHERE product_id='eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee'`,
		"product store":   `SELECT COALESCE(store_id::text,'') FROM catalog_products WHERE product_id='eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee'`,
		"inventory":       `SELECT stock_quantity FROM catalog_product_inventory WHERE product_id='eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee'`,
		"policy rev":      `SELECT source_revision FROM catalog_product_sales_policies WHERE product_id='eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee'`,
		"sale total":      `SELECT total_minor FROM sales_projection WHERE sale_id='aaaaaaaa-0000-4000-8000-0000000000a1'`,
		"sale line total": `SELECT line_total_minor FROM sale_lines_projection WHERE sale_id='aaaaaaaa-0000-4000-8000-0000000000a1'`,
		"tag slug":        `SELECT slug FROM catalog_tags WHERE tag_id='dddddddd-dddd-4ddd-8ddd-dddddddddddd'`,
		"admin command":   `SELECT command_type FROM catalog_admin_commands WHERE id='aaaaaaaa-0000-4000-8000-0000000000c1'`,
	}
	want := map[string]string{
		"product sku": "ML-001", "product rev": "4", "product store": "",
		"inventory": "5", "policy rev": "2",
		"sale total": "10000", "sale line total": "10000",
		"tag slug": "horse", "admin command": "catalog.product.details.update.v1",
	}
	for name, query := range queries {
		var got string
		if err := conn.QueryRowContext(ctx, query).Scan(&got); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != want[name] {
			t.Fatalf("%s: got %q want %q", name, got, want[name])
		}
	}
	// New variant tables exist and are empty.
	for _, table := range []string{
		"catalog_product_variants",
		"catalog_product_variant_attribute_values",
		"catalog_product_variant_inventory",
		"commerce_product_variant_mappings",
	} {
		var exists bool
		if err := conn.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name=$1)`, table).Scan(&exists); err != nil || !exists {
			t.Fatalf("table %s missing (%v)", table, err)
		}
		var n int
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("table %s not empty: %d (%v)", table, n, err)
		}
	}
}

// The v33 command vocabulary accepts the three Phase 17 variant command
// types and still accepts every Phase 16 type; the CHECK is the only
// constraint rebuilt by the migration.
func TestV33AdminCommandVariantTypes(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	exec := func(query string) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query); err != nil {
			t.Fatalf("exec: %v\n%s", err, query)
		}
	}
	exec(`INSERT INTO devices (id, name, status, created_at, updated_at)
		VALUES ('11111111-1111-4111-8111-111111111111', 'shop-pc', 'active', now(), now())`)
	exec(`INSERT INTO stores (id, display_name, timezone)
		VALUES ('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', 'Store A', 'Africa/Cairo')`)
	ids := []string{
		"10000000-0000-4000-8000-000000000001",
		"10000000-0000-4000-8000-000000000002",
		"10000000-0000-4000-8000-000000000003",
		"10000000-0000-4000-8000-000000000004",
	}
	for i, typ := range []string{
		"catalog.product.variants.update.v1",
		"catalog.product.variant.update.v1",
		"catalog.variant.attributes.update.v1",
		"catalog.product.details.update.v1",
	} {
		exec(`INSERT INTO catalog_admin_commands (id, store_id, command_type, command_version, entity_id, payload, payload_hash, expected_revision, actor)
			VALUES ('` + ids[i] + `', 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', '` + typ + `', 1, 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee', '{}', '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef', 0, 'op')`)
	}
	// Unknown types stay rejected.
	if _, err := conn.ExecContext(ctx, `INSERT INTO catalog_admin_commands (id, store_id, command_type, command_version, entity_id, payload, payload_hash, expected_revision, actor)
		VALUES ('99999999-9999-4999-8999-999999999999', 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', 'catalog.variant.bogus.v1', 1, 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee', '{}', '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef', 0, 'op')`); err == nil {
		t.Fatal("unknown command type must stay rejected")
	}
}
