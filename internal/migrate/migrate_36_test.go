package migrate_test

import (
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
)

// v34 → 36 (Phase 17-R0) preserves every business value except the
// deliberately retired catalog_products.sku (ADR-0049: Product has no
// SKU authority anywhere) and appends the nullable sale-line variant
// snapshot columns (00036) as pure additions.
func TestV34To36PreservesExistingRows(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 34); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != 34 {
		t.Fatalf("want 34, got %d", v)
	}
	exec := func(query string) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query); err != nil {
			t.Fatalf("seed v34: %v\n%s", err, query)
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
	exec(`INSERT INTO catalog_products (product_id, sku, name, top_category_id, is_active, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
		VALUES ('eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee', 'ML-001', 'Horse', 'cccccccc-cccc-4ccc-8ccc-cccccccccccc', true, 4, 'aaaaaaaa-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', '\x00', now())`)
	exec(`INSERT INTO catalog_product_variants (variant_id, product_id, sku, is_active, deleted, position, combination_key, variant_revision, catalog_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
		VALUES ('bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb', 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee', 'ML-001-A', true, false, 0, 'size=a5', 1, 4, 'aaaaaaaa-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', '\x00', now())`)
	exec(`INSERT INTO catalog_product_variant_attribute_values (variant_id, definition_code, value_code, name_ar, name_en, definition_name_ar, definition_name_en, position)
		VALUES ('bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb', 'size', 'a5', 'أ5', 'A5', 'المقاس', 'Size', 0)`)
	exec(`INSERT INTO catalog_product_variant_inventory (variant_id, product_id, sku, stock_quantity, ready, sell_online, policy_revision, catalog_revision, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
		VALUES ('bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb', 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee', 'ML-001-A', 7, true, true, 1, 4, 3, 'aaaaaaaa-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', '\x00', now())`)
	exec(`INSERT INTO sales_projection (sale_id, source_event_id, source_device_id, sale_number, channel, occurred_at, paid_at, shop_name_ar, shop_name_en, shop_address_ar, shop_address_en, shop_phone, shop_receipt_footer_ar, shop_receipt_footer_en, currency, subtotal_minor, discount_minor, tax_minor, total_minor, received_at)
		VALUES ('aaaaaaaa-0000-4000-8000-0000000000a1', 'aaaaaaaa-0000-4000-8000-000000000001', '11111111-1111-4111-8111-111111111111', 'S-1', 'STORE', now(), now(), 'a', 'b', 'c', 'd', 'e', 'f', 'g', 'EGP', 10000, 0, 0, 10000, now())`)
	exec(`INSERT INTO sale_lines_projection (sale_id, sale_item_id, position, product_id, variant_id, sku, product_name, quantity, unit_price_minor, unit_currency, line_total_minor, line_currency)
		VALUES ('aaaaaaaa-0000-4000-8000-0000000000a1', 'aaaaaaaa-0000-4000-8000-0000000000b1', 0, 'eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee', 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb', 'ML-001', 'Horse', 1, 10000, 'EGP', 10000, 'EGP')`)
	exec(`INSERT INTO commerce_online_orders (provider_key, external_order_id, order_number, provider_status, canonical_status, currency, discount_minor, shipping_minor, cart_tax_minor, total_tax_minor, total_minor, created_at, modified_at, revision, fingerprint, mapping_complete, unmapped_lines)
		VALUES ('website','1','1','processing','PROCESSING','EGP',0,0,0,0,100,now(),now(),1,'\x01',TRUE,0)`)
	exec(`INSERT INTO commerce_online_order_lines (provider_key, external_order_id, external_line_id, external_product_id, variation_id, sku, name, quantity, subtotal_minor, subtotal_tax_minor, total_minor, total_tax_minor, mapped, variant_id, variant_sku, variant_attribute_snapshot)
		VALUES ('website','1',1,'500',0,'ML-001','Horse',1,100,0,100,0,TRUE,'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb','ML-001-A','[{"definition_code":"size","value_code":"a5","name_ar":"أ5","name_en":"A5","definition_name_ar":"المقاس","definition_name_en":"Size","position":0}]')`)

	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != migrate.TargetVersion {
		t.Fatalf("want %d, got %d", migrate.TargetVersion, v)
	}
	// Business values preserved byte-identically (product sku retired).
	queries := map[string]string{
		"product rev":       `SELECT source_revision FROM catalog_products WHERE product_id='eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee'`,
		"variant sku":       `SELECT sku FROM catalog_product_variants WHERE variant_id='bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb'`,
		"variant label":     `SELECT name_ar FROM catalog_product_variant_attribute_values WHERE variant_id='bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb'`,
		"variant stock":     `SELECT stock_quantity FROM catalog_product_variant_inventory WHERE variant_id='bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb'`,
		"sale line sku":     `SELECT sku FROM sale_lines_projection WHERE sale_id='aaaaaaaa-0000-4000-8000-0000000000a1'`,
		"sale line total":   `SELECT line_total_minor FROM sale_lines_projection WHERE sale_id='aaaaaaaa-0000-4000-8000-0000000000a1'`,
		"order variant sku": `SELECT variant_sku FROM commerce_online_order_lines WHERE external_order_id='1'`,
		"sale total":        `SELECT total_minor FROM sales_projection WHERE sale_id='aaaaaaaa-0000-4000-8000-0000000000a1'`,
	}
	want := map[string]string{
		"product rev": "4", "variant sku": "ML-001-A", "variant label": "أ5",
		"variant stock": "7", "sale line sku": "ML-001", "sale line total": "10000",
		"order variant sku": "ML-001-A", "sale total": "10000",
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
	// 00035: the product SKU column is gone.
	var skuCols int
	if err := conn.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.columns WHERE table_name='catalog_products' AND column_name='sku'`).Scan(&skuCols); err != nil || skuCols != 0 {
		t.Fatalf("product sku must be retired, columns=%d (%v)", skuCols, err)
	}
	// 00036: the new columns exist, are NULL for pre-existing lines
	// (truthful: v1..v2 lines carry no variant snapshot), and enforce
	// their bounds.
	var attrsNull, skuNull bool
	if err := conn.QueryRowContext(ctx,
		`SELECT variant_attributes IS NULL, variant_sku IS NULL
		 FROM sale_lines_projection WHERE sale_id='aaaaaaaa-0000-4000-8000-0000000000a1'`).Scan(&attrsNull, &skuNull); err != nil {
		t.Fatal(err)
	}
	if !attrsNull || !skuNull {
		t.Fatal("pre-existing lines must keep truthful NULL variant snapshots")
	}
	for _, bad := range []string{
		`UPDATE sale_lines_projection SET variant_price_egp_cents = -1 WHERE sale_id='aaaaaaaa-0000-4000-8000-0000000000a1'`,
		`UPDATE sale_lines_projection SET variant_attributes = '{"not":"array"}'::jsonb WHERE sale_id='aaaaaaaa-0000-4000-8000-0000000000a1'`,
		`UPDATE sale_lines_projection SET variant_attributes = (SELECT jsonb_agg(i) FROM generate_series(1,33) i) WHERE sale_id='aaaaaaaa-0000-4000-8000-0000000000a1'`,
	} {
		if _, err := conn.ExecContext(ctx, bad); err == nil {
			t.Fatalf("bound must reject: %s", bad)
		}
	}
	if _, err := conn.ExecContext(ctx, `UPDATE sale_lines_projection
		SET variant_sku='ML-001-A', variant_attributes='[{"definition_code":"size","value_code":"a5","name_ar":"أ5","definition_name_ar":"المقاس"}]'::jsonb,
		    variant_price_egp_cents=10000, variant_price_usd_cents=NULL
		WHERE sale_id='aaaaaaaa-0000-4000-8000-0000000000a1'`); err != nil {
		t.Fatalf("valid snapshot must store: %v", err)
	}
}
