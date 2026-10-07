package postgres

// Phase 15-R2 F11/F13: purchase-time selection immutability and truthful
// aggregate completeness through the PUBLIC reconciliation service
// (OrderService.ReconcileOrder) with real PostgreSQL — the exact path the
// review proved defective.

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// stubOrderProvider serves one mutable order snapshot (the provider's
// CURRENT state) through the frozen CommerceProvider+OrderProvider seam.
type stubOrderProvider struct {
	snapshot orders.OrderSnapshot
}

func (s *stubOrderProvider) Key() commerce.ProviderKey { return "website" }
func (s *stubOrderProvider) UpsertProduct(context.Context, commerce.ProductUpsertRequest) (commerce.ProductUpsertResult, error) {
	return commerce.ProductUpsertResult{}, nil
}
func (s *stubOrderProvider) SetInventory(context.Context, commerce.InventoryUpdateRequest) error {
	return nil
}
func (s *stubOrderProvider) GetOrder(_ context.Context, externalOrderID string) (orders.OrderSnapshot, error) {
	snapshot := s.snapshot
	snapshot.ExternalOrderID = externalOrderID
	return snapshot, nil
}

func lineEntry(id int64, variation int64, name string, total int64, delta *int64, styleAR, styleEN string) orders.OrderLine {
	line := orders.OrderLine{
		ExternalLineID: id, ExternalProductID: "1000", VariationID: variation,
		SKU: "ML-1", Name: name, Quantity: 1,
		SubtotalMinor: total, SubtotalTaxMinor: 0, TotalMinor: total, TotalTaxMinor: 0,
	}
	if variation != 0 {
		line.ProviderConfigurationID = "var-" + string(rune('0'+variation%10))
	}
	return line
}

func seedConfiguration(t *testing.T, pool *pgxpool.Pool, productID, configID, styleAR, styleEN string, delta int64) {
	t.Helper()
	ctx := context.Background()
	// Minimal durable provenance for the projected rows
	// (device -> sync event -> category -> product -> configuration).
	seeds := []string{
		`INSERT INTO devices (id, name, created_at)
		 VALUES ('11111111-1111-4111-8111-111111111111','probe', now())
		 ON CONFLICT (id) DO NOTHING`,
		`INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, received_at, payload, payload_hash)
		 VALUES ('22222222-2222-4222-8222-222222222222','11111111-1111-4111-8111-111111111111',
		 'catalog.product.snapshot.v1', now(), now(), '{}'::jsonb, '\x00')
		 ON CONFLICT (event_id) DO NOTHING`,
		`INSERT INTO catalog_categories (category_id, status, name_ar, source_revision,
		 source_event_id, source_device_id, source_payload_hash, source_received_at)
		 VALUES ('33333333-3333-4333-8333-333333333333','active','Root', 1,
		 '22222222-2222-4222-8222-222222222222','11111111-1111-4111-8111-111111111111',
		 '\x00', now())
		 ON CONFLICT (category_id) DO NOTHING`,
	}
	for _, stmt := range seeds {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO catalog_products (
		 product_id, name, top_category_id, is_active, source_revision,
		 source_event_id, source_device_id, source_payload_hash, source_received_at)
		 VALUES ($1, 'Seed', '33333333-3333-4333-8333-333333333333', true, 1,
		 '22222222-2222-4222-8222-222222222222','11111111-1111-4111-8111-111111111111',
		 '\x00', now())
		 ON CONFLICT (product_id) DO NOTHING`, productID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO commerce_product_configuration_mappings (
		 provider_key, product_id, configuration_id, external_product_id,
		 external_configuration_id, created_at, updated_at)
		 VALUES ('website', $2, $1, '1000', 'var-0', now(), now())
		 ON CONFLICT (provider_key, product_id, configuration_id) DO NOTHING`,
		configID, productID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO catalog_product_configurations (
		 configuration_id, product_id, kind, style_code, style_name_ar, style_name_en,
		 color_code, color_name_ar, price_delta_egp_minor, enabled, position,
		 configuration_revision, source_revision, source_event_id, source_device_id,
		 source_payload_hash, source_received_at)
		 VALUES ($1,$2,'frame','classic',$3,$4,'black','black',$5,true,0,1,1,
		 '22222222-2222-4222-8222-222222222222','11111111-1111-4111-8111-111111111111',
		 '\x00', now())`,
		configID, productID, styleAR, styleEN, delta); err != nil {
		t.Fatal(err)
	}
}

// F11: a public reconciliation after a catalog rename/reprice must keep
// every frozen purchase-time selection field byte-identical while normal
// provider money/status evolve.
func TestPublicReconcilePreservesSelectionHistory(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testutil.Isolated(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	devices := NewDevices(pool, 5*time.Second)
	registry := commerce.NewRegistry()
	provider := &stubOrderProvider{}
	if err := registry.Register("website", provider); err != nil {
		t.Fatal(err)
	}
	service := orders.NewOrderService(registry, devices, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	productID := "11111111-0000-4000-8000-0000000000aa"
	configID := "11111111-0000-4000-8000-0000000000c1"
	seedConfiguration(t, pool, productID, configID, "Original", "Original", 30000)
	delta := int64(30000)
	provider.snapshot = orders.OrderSnapshot{
		ProviderKey: "website", ExternalOrderID: "1001", OrderNumber: "1001",
		ProviderStatus: "pending", Canonical: orders.StatusPending,
		Currency: "EGP", TotalMinor: 130000,
		CreatedAt: time.Now().UTC(), ModifiedAt: time.Now().UTC(),
		Lines: []orders.OrderLine{lineEntry(1, 2000, "Papyrus", 130000, &delta, "Original", "Original")},
	}
	if _, err := service.ReconcileOrder(ctx, "website", "1001"); err != nil {
		t.Fatal(err)
	}

	// Purchase-time selection captured; the CATALOG then changes.
	if _, err := pool.Exec(ctx, `UPDATE catalog_product_configurations
		SET style_name_ar='Renamed', style_name_en='Renamed', price_delta_egp_minor=90000
		WHERE configuration_id=$1`, configID); err != nil {
		t.Fatal(err)
	}
	provider.snapshot.ProviderStatus = "processing"
	provider.snapshot.Canonical = orders.StatusProcessing
	provider.snapshot.TotalMinor = 130500
	if _, err := service.ReconcileOrder(ctx, "website", "1001"); err != nil {
		t.Fatal(err)
	}

	var styleAR, styleEN string
	var deltaMinor int64
	if err := pool.QueryRow(ctx, `SELECT frame_style_name_ar, frame_style_name_en,
		configuration_price_delta_minor FROM commerce_online_order_lines
		WHERE provider_key='website' AND external_order_id='1001' AND external_line_id=1`).
		Scan(&styleAR, &styleEN, &deltaMinor); err != nil {
		t.Fatal(err)
	}
	if styleAR != "Original" || styleEN != "Original" || deltaMinor != 30000 {
		t.Fatalf("purchase-time selection rewritten by public reconciliation: %s/%s/%d", styleAR, styleEN, deltaMinor)
	}
	var total int64
	var status string
	if err := pool.QueryRow(ctx, `SELECT total_minor, provider_status FROM commerce_online_orders
		WHERE provider_key='website' AND external_order_id='1001'`).Scan(&total, &status); err != nil {
		t.Fatal(err)
	}
	if total != 130500 || status != "processing" {
		t.Fatalf("provider money/status must still evolve: %d/%s", total, status)
	}

	// Removed/new/reordered lines synchronize by stable identity while
	// surviving lines keep their own snapshots (F11 §3).
	deltaB := int64(40000)
	provider.snapshot.Lines = []orders.OrderLine{
		lineEntry(2, 2001, "Papyrus B", 140000, &deltaB, "Second", "Second"),
		lineEntry(1, 2000, "Papyrus", 130000, &delta, "Original", "Original"),
	}
	provider.snapshot.ProviderStatus = "completed"
	provider.snapshot.Canonical = orders.StatusCompleted
	if _, err := service.ReconcileOrder(ctx, "website", "1001"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM commerce_online_order_lines
		WHERE provider_key='website' AND external_order_id='1001'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("line lifecycle wrong after reorder/new: %d", count)
	}
	if err := pool.QueryRow(ctx, `SELECT frame_style_name_ar FROM commerce_online_order_lines
		WHERE provider_key='website' AND external_order_id='1001' AND external_line_id=1`).Scan(&styleAR); err != nil {
		t.Fatal(err)
	}
	if styleAR != "Original" {
		t.Fatalf("surviving line snapshot rewritten on reorder: %s", styleAR)
	}
}

// F13: an unresolved provider selection never claims a fully mapped
// order; No Frame and unknown selections stay distinguishable.
func TestPublicReconcileTruthfulCompleteness(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testutil.Isolated(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	devices := NewDevices(pool, 5*time.Second)
	registry := commerce.NewRegistry()
	provider := &stubOrderProvider{}
	if err := registry.Register("website", provider); err != nil {
		t.Fatal(err)
	}
	service := orders.NewOrderService(registry, devices, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	provider.snapshot = orders.OrderSnapshot{
		ProviderKey: "website", ExternalOrderID: "2001", OrderNumber: "2001",
		ProviderStatus: "pending", Canonical: orders.StatusPending,
		Currency: "EGP", TotalMinor: 100000,
		CreatedAt: time.Now().UTC(), ModifiedAt: time.Now().UTC(),
		Lines: []orders.OrderLine{
			lineEntry(1, 599, "Unknown variation", 100000, nil, "", ""),
			lineEntry(2, 0, "No Frame line", 100000, nil, "", ""),
		},
	}
	if _, err := service.ReconcileOrder(ctx, "website", "2001"); err != nil {
		t.Fatal(err)
	}
	var complete bool
	var unresolved int
	if err := pool.QueryRow(ctx, `SELECT mapping_complete,
		(SELECT count(*) FROM commerce_online_order_lines
		 WHERE provider_key='website' AND external_order_id='2001'
		   AND configuration_unresolved) FROM commerce_online_orders
		WHERE provider_key='website' AND external_order_id='2001'`).Scan(&complete, &unresolved); err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatal("order with unknown provider selection must not claim mapping_complete")
	}
	if unresolved != 1 {
		t.Fatalf("exactly the unknown selection is unresolved, got %d", unresolved)
	}
	// No Frame vs unknown stay distinct business identities.
	var noFrameConfigNull, unknownConfigNull bool
	if err := pool.QueryRow(ctx, `SELECT configuration_id IS NULL FROM commerce_online_order_lines
		WHERE provider_key='website' AND external_order_id='2001' AND external_line_id=2`).Scan(&noFrameConfigNull); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT configuration_id IS NULL FROM commerce_online_order_lines
		WHERE provider_key='website' AND external_order_id='2001' AND external_line_id=1`).Scan(&unknownConfigNull); err != nil {
		t.Fatal(err)
	}
	if !noFrameConfigNull || !unknownConfigNull {
		t.Fatal("business configuration identity must stay NULL for NO-FRAME and unknown selections")
	}
}
