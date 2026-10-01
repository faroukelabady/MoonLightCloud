package migrate_test

import (
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
)

// v22 → 23 preserves all business values and keeps legacy ownership NULL:
// no historical Store fabrication, no current-state adoption by migration.
// Adoption happens only through live scoped projection writes.
func TestV22To23PreservesBusinessValues(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 22); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != 22 {
		t.Fatalf("want 22, got %d", v)
	}
	exec := func(query string) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query); err != nil {
			t.Fatalf("seed v22: %v\n%s", err, query)
		}
	}
	exec(`INSERT INTO devices (id, name, status, created_at, updated_at)
		VALUES ('11111111-1111-4111-8111-111111111111', 'shop-pc', 'active', now(), now())`)
	exec(`INSERT INTO stores (id, display_name, timezone) VALUES
		('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', 'Store A', 'Africa/Cairo'),
		('bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb', 'Store B', 'Africa/Cairo')`)
	exec(`INSERT INTO device_store_bindings (device_id, store_id)
		VALUES ('11111111-1111-4111-8111-111111111111', 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa')`)
	// Legacy NULL event + scoped Store A event.
	exec(`INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, payload, payload_hash) VALUES
		('aaaaaaaa-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'sale.finalized.v1', now(), '{"sale_id":"aaaaaaaa-0000-4000-8000-0000000000a1"}', '\x00'),
		('aaaaaaaa-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', 'sale.finalized.v1', now(), '{"sale_id":"aaaaaaaa-0000-4000-8000-0000000000a2"}', '\x00')`)
	exec(`UPDATE sync_events SET store_id = 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa'
		WHERE event_id = 'aaaaaaaa-0000-4000-8000-000000000002'`)
	// Legacy catalog/inventory/policy rows.
	exec(`INSERT INTO catalog_categories (category_id, status, name_ar, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
		VALUES ('cccccccc-cccc-4ccc-8ccc-cccccccccccc', 'active', 'cat', 1, 'aaaaaaaa-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', '\x00', now())`)
	exec(`INSERT INTO catalog_tags (tag_id, slug, is_active, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
		VALUES ('dddddddd-dddd-4ddd-8ddd-dddddddddddd', 'horse', true, 1, 'aaaaaaaa-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', '\x00', now())`)
	exec(`INSERT INTO catalog_products (product_id, sku, name, top_category_id, is_active, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
		VALUES ('eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee', 'ML-001', 'Horse', 'cccccccc-cccc-4ccc-8ccc-cccccccccccc', true, 1, 'aaaaaaaa-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', '\x00', now())`)
	exec(`INSERT INTO catalog_product_inventory (product_id, stock_quantity, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
		VALUES ('eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee', 5, 1, 'aaaaaaaa-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', '\x00', now())`)
	exec(`INSERT INTO catalog_product_sales_policies (product_id, sell_offline, sell_online, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
		VALUES ('eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee', true, true, 1, 'aaaaaaaa-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', '\x00', now())`)
	// Legacy sale + return rows.
	exec(`INSERT INTO sales_projection (sale_id, source_event_id, source_device_id, sale_number, channel, occurred_at, paid_at, shop_name_ar, shop_name_en, shop_address_ar, shop_address_en, shop_phone, shop_receipt_footer_ar, shop_receipt_footer_en, currency, subtotal_minor, discount_minor, tax_minor, total_minor, received_at)
		VALUES ('aaaaaaaa-0000-4000-8000-0000000000a1', 'aaaaaaaa-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'S-1', 'STORE', now(), now(), 'a', 'b', 'c', 'd', 'e', 'f', 'g', 'EGP', 10000, 0, 0, 10000, now())`)
	exec(`INSERT INTO sale_lines_projection (sale_id, sale_item_id, position, sku, product_name, quantity, unit_price_minor, unit_currency, line_total_minor, line_currency)
		VALUES ('aaaaaaaa-0000-4000-8000-0000000000a1', 'aaaaaaaa-0000-4000-8000-0000000000b1', 0, 'ML-001', 'Horse', 1, 10000, 'EGP', 10000, 'EGP')`)
	exec(`INSERT INTO return_refund_projection (return_refund_id, source_event_id, source_device_id, return_number, kind, reason, sale_id, sale_number, channel, occurred_at, currency, gross_refunded_minor, discount_refunded_minor, tax_refunded_minor, refund_total_minor, shop_name_ar, shop_name_en, shop_address_ar, shop_address_en, shop_phone, shop_receipt_footer_ar, shop_receipt_footer_en, received_at)
		VALUES ('aaaaaaaa-0000-4000-8000-0000000000c1', 'aaaaaaaa-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'R-1', 'return', 'defect', 'aaaaaaaa-0000-4000-8000-0000000000a1', 'S-1', 'STORE', now(), 'EGP', 10000, 0, 0, 10000, 'a', 'b', 'c', 'd', 'e', 'f', 'g', now())`)

	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != migrate.TargetVersion {
		t.Fatalf("want %d, got %d", migrate.TargetVersion, v)
	}
	// Every business value preserved byte-identically.
	queries := map[string]string{
		"sale total":      `SELECT total_minor FROM sales_projection WHERE sale_id='aaaaaaaa-0000-4000-8000-0000000000a1'`,
		"sale line total": `SELECT line_total_minor FROM sale_lines_projection WHERE sale_id='aaaaaaaa-0000-4000-8000-0000000000a1'`,
		"refund total":    `SELECT refund_total_minor FROM return_refund_projection WHERE return_refund_id='aaaaaaaa-0000-4000-8000-0000000000c1'`,
		"product sku":     `SELECT sku FROM catalog_products WHERE product_id='eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee'`,
		"product rev":     `SELECT source_revision FROM catalog_products WHERE product_id='eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee'`,
		"inventory":       `SELECT stock_quantity FROM catalog_product_inventory WHERE product_id='eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee'`,
		"tag slug":        `SELECT slug FROM catalog_tags WHERE tag_id='dddddddd-dddd-4ddd-8ddd-dddddddddddd'`,
		"category name":   `SELECT name_ar FROM catalog_categories WHERE category_id='cccccccc-cccc-4ccc-8ccc-cccccccccccc'`,
	}
	want := map[string]string{
		"sale total": "10000", "sale line total": "10000", "refund total": "10000",
		"product sku": "ML-001", "product rev": "1", "inventory": "5",
		"tag slug": "horse", "category name": "cat",
	}
	for name, query := range queries {
		var got string
		if err := conn.QueryRowContext(ctx, query).Scan(&got); err != nil || got != want[name] {
			t.Fatalf("%s preserved: got %q want %q (%v)", name, got, want[name], err)
		}
	}
	// No historical Store fabrication: every projection root stays NULL,
	// including the row fed by the scoped event (migration never adopts).
	for _, table := range []string{
		"catalog_categories", "catalog_tags", "catalog_products",
		"catalog_product_inventory", "catalog_product_sales_policies",
		"sales_projection", "return_refund_projection",
	} {
		var count int
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE store_id IS NOT NULL`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s legacy ownership NULL: non-null=%d (%v)", table, count, err)
		}
	}
	// New constraints exist and stay satisfiable: same SKU/slug across
	// Stores allowed, duplicates within one Store rejected.
	for _, query := range []string{
		`INSERT INTO catalog_products (product_id, sku, name, top_category_id, is_active, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at, store_id)
			VALUES ('ffffffff-ffff-4fff-8fff-ffffffffffff', 'ML-001', 'Horse B', 'cccccccc-cccc-4ccc-8ccc-cccccccccccc', true, 1, 'aaaaaaaa-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', '\x00', now(), 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb')`,
		`INSERT INTO catalog_tags (tag_id, slug, is_active, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at, store_id)
			VALUES ('ffffffff-ffff-4fff-8fff-fffffffffffe', 'horse', true, 1, 'aaaaaaaa-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', '\x00', now(), 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb')`,
	} {
		if _, err := conn.ExecContext(ctx, query); err != nil {
			t.Fatalf("cross-store SKU/slug coexistence: %v", err)
		}
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO catalog_products (product_id, sku, name, top_category_id, is_active, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at, store_id)
		VALUES ('ffffffff-ffff-4fff-8fff-fffffffffffd', 'ML-001', 'Horse B2', 'cccccccc-cccc-4ccc-8ccc-cccccccccccc', true, 1, 'aaaaaaaa-0000-4000-8000-000000000002', '11111111-1111-4111-8111-111111111111', '\x00', now(), 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb')`); err == nil {
		t.Fatal("same-store duplicate SKU must stay prohibited")
	}
	// Fresh-store indexes exist.
	for _, index := range []string{
		"idx_catalog_categories_store", "idx_catalog_tags_store", "idx_catalog_products_store",
		"idx_catalog_product_sales_policies_store", "idx_catalog_product_inventory_store",
		"idx_sales_projection_store_occurred", "idx_return_refund_projection_store_occurred",
	} {
		var exists bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname=$1)`, index).Scan(&exists); err != nil || !exists {
			t.Fatalf("index %s present (%v)", index, err)
		}
	}
}
