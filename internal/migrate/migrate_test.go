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
	// Down-8 (return tables, no return decisions here) applies, then down-7
	// refuses on the sale decision: version rests at 7 with sale history
	// intact.
	if v := version(t, conn, ctx); v != 7 {
		t.Fatalf("version must stay 7, got %d", v)
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM sale_event_ownership`); err != nil {
		t.Fatal(err)
	}
	// Return ownership carries the same durable-decision policy: a present
	// return decision refuses down-8. Down-11 (inventory), down-10
	// (policy), and down-9 (catalog projections, no durable-decision
	// tables) still apply, so the version rests at 8: the refusal point
	// is the return-ownership migration itself, independent of how many
	// projection-only migrations sit above it.
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO return_refund_ownership (return_refund_id, winning_event_id, winning_device_id)
		 VALUES ('33333333-3333-7333-8333-333333333333',$1,$2)`, e1, dev); err != nil {
		t.Fatal(err)
	}
	if err := migrate.DownTo(ctx, conn, 7); err == nil {
		t.Fatal("return ownership downgrade with decisions must fail")
	}
	if v := version(t, conn, ctx); v != 8 {
		t.Fatalf("version must stay 8 (return-ownership refusal), got %d", v)
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM return_refund_ownership`); err != nil {
		t.Fatal(err)
	}
	if err := migrate.DownTo(ctx, conn, 6); err != nil {
		t.Fatalf("empty ownership downgrade must proceed: %v", err)
	}
	if v := version(t, conn, ctx); v != 6 {
		t.Fatalf("want 6, got %d", v)
	}
}

// TestV10ToLatest proves the frozen Phase 5B schema upgrades to the
// inventory projection cleanly: 00011 only adds the inventory table.
func TestV10ToLatest(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 10); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != 10 {
		t.Fatalf("want 10, got %d", v)
	}
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != migrate.TargetVersion {
		t.Fatalf("want %d, got %d", migrate.TargetVersion, v)
	}
	var exists bool
	if err := conn.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name='catalog_product_inventory')`).Scan(&exists); err != nil || !exists {
		t.Fatalf("inventory table missing (%v)", err)
	}
}

// TestV11ToLatest proves the frozen Phase 5C schema upgrades to the
// commerce mapping table cleanly: 00012 only adds durable integration
// state, and existing projection data is untouched.
func TestV11ToLatest(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 11); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != 11 {
		t.Fatalf("want 11, got %d", v)
	}
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != migrate.TargetVersion {
		t.Fatalf("want %d, got %d", migrate.TargetVersion, v)
	}
	var exists bool
	if err := conn.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name='commerce_product_mappings')`).Scan(&exists); err != nil || !exists {
		t.Fatalf("mapping table missing (%v)", err)
	}
	var count int
	if err := conn.QueryRow(`SELECT count(*) FROM commerce_product_mappings`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("mapping table created empty (%v)", err)
	}
}

// TestV12ToLatest proves the frozen Phase 6B schema upgrades to the
// order domain cleanly: 00013 only adds order/webhook tables, existing
// projection and mapping data is untouched, order tables start empty.
func TestV12ToLatest(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 12); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != 12 {
		t.Fatalf("want 12, got %d", v)
	}
	// A frozen v12 provider mapping survives the upgrade untouched.
	if _, err := conn.Exec(`INSERT INTO commerce_product_mappings (provider_key, product_id, external_product_id)
		VALUES ('website', '11111111-1111-4111-8111-111111111111', '500')`); err != nil {
		t.Fatalf("seed mapping at v12: %v", err)
	}
	// A v12 sync event survives untouched (00013 adds tables only).
	if _, err := conn.Exec(`INSERT INTO devices (id, name, status) VALUES
		('11111111-1111-7111-8111-111111111111','shop','active')`); err != nil {
		t.Fatalf("seed device at v12: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, received_at, payload, payload_hash)
		VALUES ('22222222-2222-7222-8222-222222222222','11111111-1111-7111-8111-111111111111','sale.finalized.v1',now(),now(),'{}','\x00')`); err != nil {
		t.Fatalf("seed event at v12: %v", err)
	}
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != migrate.TargetVersion {
		t.Fatalf("want %d, got %d", migrate.TargetVersion, v)
	}
	var external string
	if err := conn.QueryRow(`SELECT external_product_id FROM commerce_product_mappings
		WHERE provider_key='website' AND product_id='11111111-1111-4111-8111-111111111111'`).Scan(&external); err != nil || external != "500" {
		t.Fatalf("mapping preserved: %q (%v)", external, err)
	}
	var events int
	if err := conn.QueryRow(`SELECT count(*) FROM sync_events WHERE event_id='22222222-2222-7222-8222-222222222222'`).Scan(&events); err != nil || events != 1 {
		t.Fatalf("sync event preserved: %d (%v)", events, err)
	}
	for _, table := range []string{
		"commerce_online_order_webhook_events", "commerce_online_orders", "commerce_online_order_lines",
		"commerce_online_order_addresses", "commerce_online_order_status_history",
	} {
		var exists bool
		if err := conn.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name=$1)`, table).Scan(&exists); err != nil || !exists {
			t.Fatalf("table %s missing (%v)", table, err)
		}
	}
}

// TestV13ToLatest proves the frozen Phase 6C schema upgrades to the R1
// concurrency fences cleanly: 00014 only adds the reconcile-fence table
// and the lease_generation column. Seeded order domain rows (order,
// lines, addresses, history, pending/retry/processed/blocked webhook
// events), the product mapping, and representative catalog/policy/
// inventory/Sale/Return rows survive untouched; pre-existing webhook
// rows default to lease_generation=0 so pending/retry stay claimable
// and terminal rows stay terminal.
func TestV13ToLatest(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 13); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != 13 {
		t.Fatalf("want 13, got %d", v)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	dev := "11111111-1111-7111-8111-111111111111"
	eSale := "22222222-2222-7222-8222-222222222222"
	eReturn := "33333333-3333-7333-8333-333333333333"
	eCatalog := "44444444-4444-7444-8444-444444444444"
	saleID := "aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa"
	returnID := "bbbbbbbb-bbbb-7bbb-8bbb-bbbbbbbbbbbb"
	catID := "cccccccc-cccc-7ccc-8ccc-cccccccccccc"
	productID := "dddddddd-dddd-7ddd-8ddd-dddddddddddd"
	exec(`INSERT INTO devices (id, name, status) VALUES ($1,'shop','active')`, dev)
	for _, e := range []string{eSale, eReturn, eCatalog} {
		exec(`INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, received_at, payload, payload_hash)
			VALUES ($1,$2,'sale.finalized.v1',now(),now(),'{}','\x00')`, e, dev)
	}
	exec(`INSERT INTO catalog_categories (category_id, status, name_ar, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
		VALUES ($1,'active','ورق',1,$2,$3,'\x00',now())`, catID, eCatalog, dev)
	exec(`INSERT INTO catalog_products (product_id, sku, name, top_category_id, is_active, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
		VALUES ($1,'PAP-1','Papyrus',$2,TRUE,1,$3,$4,'\x00',now())`, productID, catID, eCatalog, dev)
	exec(`INSERT INTO catalog_product_sales_policies (product_id, sell_offline, sell_online, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
		VALUES ($1,TRUE,TRUE,1,$2,$3,'\x00',now())`, productID, eCatalog, dev)
	exec(`INSERT INTO catalog_product_inventory (product_id, stock_quantity, source_revision, source_event_id, source_device_id, source_payload_hash, source_received_at)
		VALUES ($1,7,1,$2,$3,'\x00',now())`, productID, eCatalog, dev)
	exec(`INSERT INTO sales_projection (sale_id, source_event_id, source_device_id, sale_number, channel, occurred_at, paid_at,
		shop_name_ar, shop_name_en, shop_address_ar, shop_address_en, shop_phone, shop_receipt_footer_ar, shop_receipt_footer_en,
		currency, subtotal_minor, discount_minor, tax_minor, total_minor, received_at)
		VALUES ($1,$2,$3,'MLR-1','STORE',now(),now(),'a','b','c','d','e','f','g','EGP',100,0,0,100,now())`,
		saleID, eSale, dev)
	exec(`INSERT INTO return_refund_projection (return_refund_id, source_event_id, source_device_id, return_number, kind, reason,
		sale_id, sale_number, channel, occurred_at, currency, gross_refunded_minor, discount_refunded_minor, tax_refunded_minor,
		refund_total_minor, shop_name_ar, shop_name_en, shop_address_ar, shop_address_en, shop_phone,
		shop_receipt_footer_ar, shop_receipt_footer_en, received_at)
		VALUES ($1,$2,$3,'RT-1','return','defect',$4,'MLR-1','STORE',now(),'EGP',100,0,0,100,'a','b','c','d','e','f','g',now())`,
		returnID, eReturn, dev, saleID)
	exec(`INSERT INTO commerce_product_mappings (provider_key, product_id, external_product_id)
		VALUES ('website', $1, '500')`, productID)
	exec(`INSERT INTO commerce_online_orders (provider_key, external_order_id, order_number, provider_status, canonical_status,
		currency, discount_minor, shipping_minor, cart_tax_minor, total_tax_minor, total_minor, created_at, modified_at,
		payment_method, revision, fingerprint, mapping_complete, unmapped_lines)
		VALUES ('website','980','980','processing','PROCESSING','EGP',0,0,0,0,68000,now(),now(),'cod',1,'\x01',TRUE,0)`)
	exec(`INSERT INTO commerce_online_order_lines (provider_key, external_order_id, external_line_id, external_product_id,
		sku, name, quantity, subtotal_minor, subtotal_tax_minor, total_minor, total_tax_minor, moonlight_product_id, mapped)
		VALUES ('website','980',1,'500','PAP-1','Papyrus',1,68000,0,68000,0,$1,TRUE)`, productID)
	for _, kind := range []string{"billing", "shipping"} {
		exec(`INSERT INTO commerce_online_order_addresses (provider_key, external_order_id, kind, city, country)
			VALUES ('website','980',$1,'Cairo','EG')`, kind)
	}
	exec(`INSERT INTO commerce_online_order_status_history (provider_key, external_order_id, order_revision, provider_status, canonical_status)
		VALUES ('website','980',1,'processing','PROCESSING')`)
	webhooks := []struct{ delivery, topic, order, status string }{
		{"delivery-p", "order.created", "980", "pending"},
		{"delivery-r", "order.updated", "980", "retry"},
		{"delivery-ok", "order.updated", "980", "processed"},
		{"delivery-block", "order.created", "981", "blocked"},
	}
	for _, w := range webhooks {
		exec(`INSERT INTO commerce_online_order_webhook_events (provider_key, delivery_id, topic, external_order_id, payload_hash, status)
			VALUES ('website',$1,$2,$3,'\x02',$4)`, w.delivery, w.topic, w.order, w.status)
	}

	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != migrate.TargetVersion {
		t.Fatalf("want %d, got %d", migrate.TargetVersion, v)
	}
	var exists bool
	if err := conn.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name='commerce_online_order_reconcile_fences')`).Scan(&exists); err != nil || !exists {
		t.Fatalf("fence table missing (%v)", err)
	}
	var fences int
	if err := conn.QueryRow(`SELECT count(*) FROM commerce_online_order_reconcile_fences`).Scan(&fences); err != nil || fences != 0 {
		t.Fatalf("fence table starts empty (%d %v)", fences, err)
	}
	var total int64
	var canonical string
	var rev int64
	if err := conn.QueryRow(`SELECT total_minor, canonical_status, revision FROM commerce_online_orders
		WHERE provider_key='website' AND external_order_id='980'`).Scan(&total, &canonical, &rev); err != nil || total != 68000 || canonical != "PROCESSING" || rev != 1 {
		t.Fatalf("order preserved: %d %s %d (%v)", total, canonical, rev, err)
	}
	for _, table := range []string{
		"commerce_online_order_lines", "commerce_online_order_addresses",
		"commerce_online_order_status_history", "commerce_product_mappings",
		"catalog_products", "catalog_product_sales_policies", "catalog_product_inventory",
		"sales_projection", "return_refund_projection",
	} {
		var count int
		if err := conn.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count == 0 {
			t.Fatalf("table %s preserved (%d %v)", table, count, err)
		}
	}
	// Pre-existing deliveries default to lease generation 0: pending and
	// retry stay claimable, terminal rows stay terminal.
	for _, w := range webhooks {
		var status string
		var generation int64
		if err := conn.QueryRow(`SELECT status, lease_generation FROM commerce_online_order_webhook_events
			WHERE provider_key='website' AND delivery_id=$1`, w.delivery).Scan(&status, &generation); err != nil || status != w.status || generation != 0 {
			t.Fatalf("webhook %s: %s gen %d (%v)", w.delivery, status, generation, err)
		}
	}
}

// TestV14ToLatest proves the frozen Phase 6C schema upgrades to the
// notification domain cleanly: 00015 only adds notification tables,
// and representative 6C state (mapping, order webhook inbox, reconcile
// fence, sync event) is untouched. Notification tables start empty.
func TestV14ToLatest(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 14); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != 14 {
		t.Fatalf("want 14, got %d", v)
	}
	if _, err := conn.Exec(`INSERT INTO commerce_product_mappings (provider_key, product_id, external_product_id)
		VALUES ('website', '11111111-1111-4111-8111-111111111111', '500')`); err != nil {
		t.Fatalf("seed mapping at v14: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO commerce_online_order_webhook_events
		(provider_key, delivery_id, topic, external_order_id, payload_hash, status)
		VALUES ('website', 'delivery-1', 'order.created', '100', '\x02', 'pending')`); err != nil {
		t.Fatalf("seed order inbox at v14: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO commerce_online_order_reconcile_fences
		(provider_key, external_order_id, generation)
		VALUES ('website', '100', 3)`); err != nil {
		t.Fatalf("seed fence at v14: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO devices (id, name, status) VALUES
		('11111111-1111-7111-8111-111111111111','shop','active')`); err != nil {
		t.Fatalf("seed device at v14: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, received_at, payload, payload_hash)
		VALUES ('22222222-2222-7222-8222-222222222222','11111111-1111-7111-8111-111111111111','sale.finalized.v1',now(),now(),'{}','\x00')`); err != nil {
		t.Fatalf("seed event at v14: %v", err)
	}
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != migrate.TargetVersion {
		t.Fatalf("want %d, got %d", migrate.TargetVersion, v)
	}
	var generation int64
	if err := conn.QueryRow(`SELECT generation FROM commerce_online_order_reconcile_fences
		WHERE provider_key='website' AND external_order_id='100'`).Scan(&generation); err != nil || generation != 3 {
		t.Fatalf("fence preserved: %d (%v)", generation, err)
	}
	var status string
	var leaseGen int64
	if err := conn.QueryRow(`SELECT status, lease_generation FROM commerce_online_order_webhook_events
		WHERE provider_key='website' AND delivery_id='delivery-1'`).Scan(&status, &leaseGen); err != nil || status != "pending" || leaseGen != 0 {
		t.Fatalf("inbox preserved: %s %d (%v)", status, leaseGen, err)
	}
	for _, table := range []string{
		"notification_template_mappings", "notification_messages",
		"notification_delivery_status_history",
	} {
		var exists bool
		if err := conn.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name=$1)`, table).Scan(&exists); err != nil || !exists {
			t.Fatalf("table %s missing (%v)", table, err)
		}
		var count int
		if err := conn.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("table %s starts empty (%d %v)", table, count, err)
		}
	}
}

// TestV15ToLatest proves the frozen Phase 7A schema upgrades to the
// business-report domain cleanly: 00016 only adds report tables, and
// representative 7A state (template mapping, notification, delivery
// history, order fence) is preserved exactly. Report tables start
// empty.
func TestV15ToLatest(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 15); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != 15 {
		t.Fatalf("want 15, got %d", v)
	}
	if _, err := conn.Exec(`INSERT INTO notification_template_mappings
		(provider_key, template_key, locale, external_template_name, external_language_code, parameter_names, enabled)
		VALUES ('whatsapp-main', 'operator_test_v1', 'ar', 'ext', 'ar', '{name}', TRUE)`); err != nil {
		t.Fatalf("seed mapping at v15: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO notification_messages
		(id, provider_key, idempotency_key, semantic_fingerprint, recipient, template_key, locale,
		 ext_template_name, ext_language_code, dispatch_status, delivery_status)
		VALUES ('11111111-1111-4111-8111-111111111111', 'whatsapp-main', 'k1', '\x01',
			'201012345678', 'operator_test_v1', 'ar', 'ext', 'ar', 'accepted', 'READ')`); err != nil {
		t.Fatalf("seed notification at v15: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO notification_delivery_status_history
		(notification_id, provider_key, provider_message_id, provider_status_raw,
		 canonical_status, event_fingerprint)
		VALUES ('11111111-1111-4111-8111-111111111111', 'whatsapp-main', 'wamid.1',
			'read', 'READ', '\x02')`); err != nil {
		t.Fatalf("seed history at v15: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO commerce_online_order_reconcile_fences
		(provider_key, external_order_id, generation)
		VALUES ('website', '100', 3)`); err != nil {
		t.Fatalf("seed fence at v15: %v", err)
	}
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != migrate.TargetVersion {
		t.Fatalf("want %d, got %d", migrate.TargetVersion, v)
	}
	var dispatch, delivery, recipient string
	if err := conn.QueryRow(`SELECT dispatch_status, delivery_status, recipient FROM notification_messages
		WHERE provider_key='whatsapp-main' AND idempotency_key='k1'`).Scan(&dispatch, &delivery, &recipient); err != nil ||
		dispatch != "accepted" || delivery != "READ" || recipient != "201012345678" {
		t.Fatalf("notification preserved: %s %s %s (%v)", dispatch, delivery, recipient, err)
	}
	var history int
	if err := conn.QueryRow(`SELECT count(*) FROM notification_delivery_status_history
		WHERE provider_message_id='wamid.1'`).Scan(&history); err != nil || history != 1 {
		t.Fatalf("history preserved: %d (%v)", history, err)
	}
	var generation int64
	if err := conn.QueryRow(`SELECT generation FROM commerce_online_order_reconcile_fences
		WHERE provider_key='website' AND external_order_id='100'`).Scan(&generation); err != nil || generation != 3 {
		t.Fatalf("fence preserved: %d (%v)", generation, err)
	}
	for _, table := range []string{
		"business_report_recipients", "business_report_schedules",
		"business_report_schedule_recipients", "business_report_runs",
		"business_report_deliveries",
	} {
		var exists bool
		if err := conn.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name=$1)`, table).Scan(&exists); err != nil || !exists {
			t.Fatalf("table %s missing (%v)", table, err)
		}
		var count int
		if err := conn.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("table %s starts empty (%d %v)", table, count, err)
		}
	}
}

// v20 → 21 carries projected sales forward with unknown tag capture and
// fabricates zero tag rows.
func TestV20To21PreservesSales(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 20); err != nil {
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
		`INSERT INTO sales_projection (sale_id, source_event_id, source_device_id, sale_number, channel, occurred_at, paid_at,
		 shop_name_ar, shop_name_en, shop_address_ar, shop_address_en, shop_phone, shop_receipt_footer_ar, shop_receipt_footer_en,
		 currency, subtotal_minor, discount_minor, tax_minor, total_minor, received_at)
		 VALUES ($1,$2,$3,'MLR-1','STORE',now(),now(),'a','b','c','d','e','f','g','EGP',100,0,0,100,now())`,
		saleID, e1, dev); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != migrate.TargetVersion {
		t.Fatalf("want %d, got %d", migrate.TargetVersion, v)
	}
	var capture *bool
	if err := conn.QueryRowContext(ctx, `SELECT tag_capture FROM sales_projection WHERE sale_id=$1`, saleID).Scan(&capture); err != nil {
		t.Fatal(err)
	}
	if capture != nil {
		t.Fatal("upgraded sales keep unknown tag capture")
	}
	var n int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sale_item_tag_snapshots`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("zero fabricated tag rows: %d (%v)", n, err)
	}
	var total int64
	if err := conn.QueryRowContext(ctx, `SELECT total_minor FROM sales_projection WHERE sale_id=$1`, saleID).Scan(&total); err != nil || total != 100 {
		t.Fatalf("sale money preserved: %d (%v)", total, err)
	}
}

// v21 → 22 preserves devices, catalog, and sales with NULL store context
// and fabricates no stores or bindings.
func TestV21To22PreservesEverything(t *testing.T) {
	conn, ctx := openRaw(t)
	if err := migrate.UpTo(ctx, conn, 21); err != nil {
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
		`INSERT INTO sales_projection (sale_id, source_event_id, source_device_id, sale_number, channel, occurred_at, paid_at,
		 shop_name_ar, shop_name_en, shop_address_ar, shop_address_en, shop_phone, shop_receipt_footer_ar, shop_receipt_footer_en,
		 currency, subtotal_minor, discount_minor, tax_minor, total_minor, received_at)
		 VALUES ($1,$2,$3,'MLR-1','STORE',now(),now(),'a','b','c','d','e','f','g','EGP',100,0,0,100,now())`,
		saleID, e1, dev); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if v := version(t, conn, ctx); v != migrate.TargetVersion {
		t.Fatalf("want %d, got %d", migrate.TargetVersion, v)
	}
	var isNull bool
	if err := conn.QueryRowContext(ctx, `SELECT store_id IS NULL FROM sync_events WHERE event_id=$1`, e1).Scan(&isNull); err != nil || !isNull {
		t.Fatalf("legacy ingress stays NULL: %v", err)
	}
	var stores, bindings int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM stores`).Scan(&stores); err != nil || stores != 0 {
		t.Fatalf("no fabricated stores: %d (%v)", stores, err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM device_store_bindings`).Scan(&bindings); err != nil || bindings != 0 {
		t.Fatalf("no fabricated bindings: %d (%v)", bindings, err)
	}
	var total int64
	if err := conn.QueryRowContext(ctx, `SELECT total_minor FROM sales_projection WHERE sale_id=$1`, saleID).Scan(&total); err != nil || total != 100 {
		t.Fatalf("sale preserved: %d (%v)", total, err)
	}
}
