package postgres

// Phase 15-R1 F11/F12/F13: immutable purchase-time selection snapshots,
// currency-correct capture, and truthful public view assembly.

import (
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/commerce/orders"
	"github.com/jackc/pgx/v5/pgtype"
)

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
