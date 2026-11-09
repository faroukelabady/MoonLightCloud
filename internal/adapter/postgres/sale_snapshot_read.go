package postgres

import (
	"encoding/json"

	"github.com/faroukelabady/MoonLightCloud/internal/sale"
	"github.com/jackc/pgx/v5/pgtype"
)

// Read only frozen projection labels; never enrich from today's catalog.
func saleAttributeSnapshot(raw []byte) []sale.SaleLineVariantAttribute {
	var attrs []sale.SaleLineVariantAttribute
	_ = json.Unmarshal(raw, &attrs)
	return attrs
}

func saleCatalogPrice(currency string, egp, usd pgtype.Int8) *int64 {
	if currency == "USD" {
		return int8Ptr(usd)
	}
	return int8Ptr(egp)
}
