package postgres

// Phase 15-R1 F11/F12/F13: immutable purchase-time selection snapshots,
// currency-correct capture, and truthful public view assembly.

import (
	"context"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// F11: re-inserting the same provider line (ordinary reconciliation)
// evolves provider money but NEVER substitutes current configuration
// values for the captured purchase-time selection.
func TestOrderSelectionSnapshotIsImmutable(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testutil.Isolated(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	q := sqlcgen.New(pool)

	if _, err := pool.Exec(ctx, `INSERT INTO commerce_online_orders
		(provider_key, external_order_id, order_number, provider_status, canonical_status, currency,
		 discount_minor, shipping_minor, cart_tax_minor, total_tax_minor, total_minor, prices_include_tax,
		 created_at, modified_at, revision, fingerprint, provider_deleted, mapping_complete, unmapped_lines)
		VALUES ('website','1001','1001','processing','PROCESSING','EGP',0,0,0,0,130000,false,$1,$1,1,'\\x00',false,true,0)`,
		time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	configID := pgtype.UUID{}
	_ = configID.Scan("11111111-0000-4000-8000-0000000000c1")
	delta := int64(30000)
	styleAR := "Original"
	params := sqlcgen.InsertCommerceOrderLineParams{
		ProviderKey: "website", ExternalOrderID: "1001", ExternalLineID: 1,
		ExternalProductID: "1000", VariationID: 2000, Sku: "ML-1", Name: "Papyrus",
		Quantity: 1, SubtotalMinor: 130000, SubtotalTaxMinor: 0, TotalMinor: 130000, TotalTaxMinor: 0,
		Mapped: true, ConfigurationID: configID,
		FrameStyleNameAr:             pgtype.Text{String: styleAR, Valid: true},
		ConfigurationPriceDeltaMinor: pgtype.Int8{Int64: delta, Valid: true},
		ProviderConfigurationID:      pgtype.Text{String: "2000", Valid: true},
	}
	if _, err := pool.Exec(ctx, `SELECT 1`); err != nil {
		t.Fatal(err)
	}
	// First authoritative capture (uses the adapter's persistence path).
	if err := q.InsertCommerceOrderLine(ctx, params); err != nil {
		t.Fatal(err)
	}

	// Ordinary reconciliation with CURRENT (renamed/repriced) catalog
	// values must not rewrite history: updated provider money is fine,
	// selection snapshot is frozen.
	params2 := params
	params2.TotalMinor = 130500 // provider money may evolve
	params2.FrameStyleNameAr = pgtype.Text{String: "Renamed", Valid: true}
	params2.ConfigurationPriceDeltaMinor = pgtype.Int8{Int64: 90000, Valid: true}
	if err := q.InsertCommerceOrderLine(ctx, params2); err != nil {
		t.Fatal(err)
	}
	var style string
	var deltaMinor int64
	if err := pool.QueryRow(ctx, `SELECT frame_style_name_ar, configuration_price_delta_minor
		FROM commerce_online_order_lines WHERE provider_key='website' AND external_order_id='1001'`).
		Scan(&style, &deltaMinor); err != nil {
		t.Fatal(err)
	}
	if style != "Original" || deltaMinor != 30000 {
		t.Fatalf("purchase-time selection rewritten: %s/%d", style, deltaMinor)
	}
	var total int64
	if err := pool.QueryRow(ctx, `SELECT total_minor FROM commerce_online_order_lines
		WHERE provider_key='website' AND external_order_id='1001'`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 130500 {
		t.Fatalf("provider money must evolve under reconciliation: %d", total)
	}
}

// F12: delta capture follows the ORDER's currency; NULL USD stays NULL
// (never zero, never EGP).
func TestOrderDeltaCapturesOrderCurrency(t *testing.T) {
	usd := int64(2500)
	row := sqlcgen.CatalogProductConfigurationByIDRow{
		PriceDeltaEgpMinor: 90000, PriceDeltaUsdMinor: pgtype.Int8{Int64: usd, Valid: true},
		StyleCode: "classic", StyleNameAr: "كلاسيكي", ColorCode: "black", ColorNameAr: "أسود",
	}
	line := &orders.OrderLine{}
	snapshotConfiguration(line, row, "USD")
	if line.ConfigurationPriceDeltaMinor == nil || *line.ConfigurationPriceDeltaMinor != 2500 {
		t.Fatalf("USD order must capture the USD delta: %+v", line.ConfigurationPriceDeltaMinor)
	}
	line = &orders.OrderLine{}
	snapshotConfiguration(line, row, "EGP")
	if line.ConfigurationPriceDeltaMinor == nil || *line.ConfigurationPriceDeltaMinor != 90000 {
		t.Fatalf("EGP order must capture the EGP delta: %+v", line.ConfigurationPriceDeltaMinor)
	}
	nilRow := row
	nilRow.PriceDeltaUsdMinor = pgtype.Int8{}
	line = &orders.OrderLine{}
	snapshotConfiguration(line, nilRow, "USD")
	if line.ConfigurationPriceDeltaMinor != nil {
		t.Fatalf("NULL USD delta must stay NULL, never EGP fallback: %+v", line.ConfigurationPriceDeltaMinor)
	}
}

// F13: the public view exposes the stored immutable selection and the
// truthful unresolved flag — no catalog join, no hidden default false.
func TestOrderLineViewExposesStoredSelection(t *testing.T) {
	unresolved := true
	row := sqlcgen.CommerceOnlineOrderLine{
		ExternalLineID: 7, ExternalProductID: "1000", VariationID: 2000,
		Sku: "ML-1", Name: "Papyrus", Quantity: 1, TotalMinor: 130000, Mapped: true,
		ProviderConfigurationID: pgtype.Text{String: "9999", Valid: true},
		ConfigurationUnresolved: unresolved,
	}
	view := orderLineViewFromRow(row)
	if !view.ConfigurationUnresolved || view.ProviderConfigurationID != "9999" {
		t.Fatalf("unresolved selection hidden: %+v", view)
	}
	if view.ConfigurationID != nil {
		t.Fatal("unresolved selection must not fabricate a configuration identity")
	}
	_ = unresolved
}
