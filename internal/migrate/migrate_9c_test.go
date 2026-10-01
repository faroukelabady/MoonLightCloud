package migrate_test

import (
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/migrate"
)

// v23 → 24 preserves commerce integration state and keeps legacy
// ownership NULL: no mapping/order backfill by migration. Adoption
// happens only through live deterministic rules.
func TestV23To24PreservesCommerce(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 23); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != 23 {
		t.Fatalf("want 23, got %d", v)
	}
	exec := func(query string) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, query); err != nil {
			t.Fatalf("seed v23: %v\n%s", err, query)
		}
	}
	exec(`INSERT INTO devices (id, name, status, created_at, updated_at)
		VALUES ('11111111-1111-4111-8111-111111111111', 'shop-pc', 'active', now(), now())`)
	exec(`INSERT INTO commerce_product_mappings (provider_key, product_id, external_product_id)
		VALUES ('website', 'aaaaaaaa-0000-4000-8000-000000000001', '500')`)
	exec(`INSERT INTO commerce_online_orders (provider_key, external_order_id, canonical_status, currency, discount_minor, shipping_minor, cart_tax_minor, total_tax_minor, total_minor, created_at, modified_at, revision, fingerprint, mapping_complete, unmapped_lines)
		VALUES ('website', '1001', 'PROCESSING', 'EGP', 0, 0, 0, 0, 10000, now(), now(), 1, '\x01', true, 0)`)
	exec(`INSERT INTO commerce_online_order_lines (provider_key, external_order_id, external_line_id, external_product_id, quantity, subtotal_minor, subtotal_tax_minor, total_minor, total_tax_minor, mapped)
		VALUES ('website', '1001', 1, '500', 2, 10000, 0, 10000, 0, true)`)
	exec(`INSERT INTO commerce_online_order_addresses (provider_key, external_order_id, kind, first_name, city)
		VALUES ('website', '1001', 'billing', 'Amal', 'Cairo')`)
	exec(`INSERT INTO commerce_online_order_status_history (provider_key, external_order_id, order_revision, provider_status, canonical_status)
		VALUES ('website', '1001', 1, 'processing', 'PROCESSING')`)
	exec(`INSERT INTO commerce_online_order_webhook_events (provider_key, delivery_id, topic, external_order_id, payload_hash, status)
		VALUES ('website', 'dlv-1', 'order.created', '1001', '\x02', 'processed')`)
	exec(`INSERT INTO commerce_online_order_reconcile_fences (provider_key, external_order_id, generation)
		VALUES ('website', '1001', 3)`)

	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != migrate.TargetVersion {
		t.Fatalf("want %d, got %d", migrate.TargetVersion, v)
	}
	// Every business value identical except newly introduced NULL stores.
	checks := map[string]string{
		`SELECT external_product_id FROM commerce_product_mappings WHERE provider_key='website'`:                                "500",
		`SELECT total_minor FROM commerce_online_orders WHERE external_order_id='1001'`:                                         "10000",
		`SELECT quantity FROM commerce_online_order_lines WHERE external_order_id='1001'`:                                       "2",
		`SELECT first_name FROM commerce_online_order_addresses WHERE external_order_id='1001' AND kind='billing'`:              "Amal",
		`SELECT canonical_status FROM commerce_online_order_status_history WHERE external_order_id='1001' AND order_revision=1`: "PROCESSING",
		`SELECT status FROM commerce_online_order_webhook_events WHERE delivery_id='dlv-1'`:                                     "processed",
		`SELECT generation FROM commerce_online_order_reconcile_fences WHERE external_order_id='1001'`:                          "3",
	}
	for query, want := range checks {
		var got string
		if err := conn.QueryRowContext(ctx, query).Scan(&got); err != nil || got != want {
			t.Fatalf("preserved %q: got %q want %q (%v)", query, got, want, err)
		}
	}
	for _, table := range []string{"commerce_product_mappings", "commerce_online_orders"} {
		var count int
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE store_id IS NOT NULL`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s legacy ownership NULL: non-null=%d (%v)", table, count, err)
		}
	}
	for _, index := range []string{
		"idx_commerce_product_mappings_store", "idx_commerce_product_mappings_store_provider",
		"idx_commerce_online_orders_store_created", "idx_commerce_online_orders_store_provider_order",
	} {
		var exists bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname=$1)`, index).Scan(&exists); err != nil || !exists {
			t.Fatalf("index %s present (%v)", index, err)
		}
	}
}
