package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
	"github.com/faroukelabady/MoonLightCloud/internal/salesreport"
	"github.com/google/uuid"
)

func TestReportSaleContextUsesFrozenVariantPriceAndCostInRecordedCurrency(t *testing.T) {
	env := openDashEnv(t)
	ctx := context.Background()
	saleID := uuid.NewString()
	var event map[string]any
	if err := json.Unmarshal([]byte(v3Fixture(t, saleID, "SOLD-BLUE")), &event); err != nil {
		t.Fatal(err)
	}
	event["lines"].([]any)[0].(map[string]any)["variant_price_usd_cents"] = int64(2000)
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	projectDashSale(t, env, uuid.NewString(), "2026-09-20T10:00:00Z", string(raw))
	req := dashReq(t, env, "custom", "2026-09-20", "2026-09-20")
	got, err := printableService(t, env.pool, true).Sales(ctx, req, salesreport.DisplayEGP, salesreport.Filter{}, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sales) != 1 || len(got.Sales[0].Lines) != 1 {
		t.Fatalf("missing recorded lines: %+v", got.Sales)
	}
	line := got.Sales[0].Lines[0]
	if line.Currency != "USD" || line.CatalogPrice == nil || *line.CatalogPrice != 2000 || line.UnitPrice != 1300 || line.UnitCost == nil || *line.UnitCost != 400 || len(line.Attributes) != 1 || line.Attributes[0].NameAR != "أزرق" {
		t.Fatalf("recorded price/cost/labels changed: %+v", line)
	}
	// No current variant-price lookup is needed: the captured values are
	// available even though this device has not synced a current catalog.
	var prices int
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM catalog_product_variant_prices`).Scan(&prices); err != nil || prices != 0 {
		t.Fatalf("unexpected catalog fixture: %d %v", prices, err)
	}
	detail, err := dashboard.NewInsights(NewReporting(env.pool, 5*time.Second), "local", nil, time.Now).SaleDetail(ctx, report.Request{Store: &report.StoreScope{StoreID: env.storeID}}, saleID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Lines[0].CatalogPriceMinor == nil || *detail.Lines[0].CatalogPriceMinor != "2000" || *detail.Lines[0].UnitCostMinor != "400" || len(detail.Lines[0].Attributes) != 1 {
		t.Fatalf("detail missing frozen context: %+v", detail.Lines[0])
	}
}
