package postgres

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/report"
	"github.com/faroukelabady/MoonLightCloud/internal/returnrefund"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
)

// Run Retail's TestImportSaleReportReturnIntegration with the same env path
// first. This proves the actual candidate pair's versioned event contract.
func TestCatalogMilestoneActualRetailOutboxPair(t *testing.T) {
	path := os.Getenv("MOONLIGHT_8R1_PROOF_EVENTS")
	if path == "" {
		t.Skip("MOONLIGHT_8R1_PROOF_EVENTS unset: requires actual Retail outbox proof")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var batch struct {
		Events []struct {
			ID   string `json:"event_id"`
			Type string `json:"event_type"`
		} `json:"events"`
	}
	if err := json.Unmarshal(raw, &batch); err != nil {
		t.Fatal(err)
	}
	if len(batch.Events) != 4 {
		t.Fatalf("expected two Sales and two Returns: %d", len(batch.Events))
	}
	env := openSaleEnv(t)
	ctx := context.Background()
	result, err := env.syncSvc.Ingest(ctx, env.devID, env.credID, raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range result.Events {
		if event.Status != "accepted" {
			t.Fatalf("Retail event rejected: %+v", event)
		}
	}
	svc := repService(env)
	now := time.Now()
	req, err := svc.ParseRequest("custom", now.AddDate(0, 0, -1).Format("2006-01-02"), now.AddDate(0, 0, 1).Format("2006-01-02"), "")
	if err != nil {
		t.Fatal(err)
	}
	before, err := svc.Summary(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if before.Freshness.ProjectionBacklogCount != 2 || before.Freshness.CloudProjectionComplete {
		t.Fatalf("v2 pending freshness: %+v", before.Freshness)
	}
	store := NewDevices(env.pool, 5*time.Second)
	// Sales first, then returns, regardless of timestamp ties in Retail.
	for _, event := range batch.Events {
		if event.Type != sale.EventSaleFinalizedV2 {
			continue
		}
		record, ok, err := store.LoadSaleEvent(ctx, event.ID)
		if err != nil || !ok {
			t.Fatalf("load sale: %v %v", ok, err)
		}
		out, err := store.ProjectSaleV2(ctx, record, time.Now())
		if err != nil || out.Outcome != sale.OutcomeProcessed {
			t.Fatalf("project sale: %+v %v", out, err)
		}
	}
	for _, event := range batch.Events {
		if event.Type != "sale.return_refund.finalized.v1" {
			continue
		}
		record, ok, err := store.LoadReturnEvent(ctx, event.ID)
		if err != nil || !ok {
			t.Fatalf("load return: %v %v", ok, err)
		}
		out, err := store.ProjectReturn(ctx, record, time.Now())
		if err != nil || out.Outcome != returnrefund.OutcomeProcessed {
			t.Fatalf("project return: %+v %v", out, err)
		}
	}
	after, err := svc.Summary(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.CurrencyTotals) != 1 {
		t.Fatalf("currency buckets: %+v", after)
	}
	money := after.CurrencyTotals[0]
	if money.SalesTotalMinor != 270000 || money.RefundTotalMinor != 205000 || money.NetSalesMinor != 65000 || money.ReturnedUnits != 3 {
		t.Fatalf("canonical parity: %+v", money)
	}
	if after.Freshness.ProjectionBacklogCount != 0 || !after.Freshness.CloudProjectionComplete {
		t.Fatalf("converged freshness: %+v", after.Freshness)
	}
	for _, dimension := range []string{report.DimensionTag, report.DimensionRootCategory} {
		out, err := svc.Breakdown(ctx, req, dimension)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, row := range out.Rows {
			if row.NameEN == nil {
				continue
			}
			name := *row.NameEN
			if name == "Silver" {
				continue
			}
			seen[name] = true
			if len(row.LineSales) != 1 {
				t.Fatalf("bucket: %+v", row)
			}
			bucket := row.LineSales[0]
			if name == "Gold" || name == "Pharaonic" {
				if bucket.LineRefundMinor != 65000 || bucket.LineSalesMinor-bucket.LineRefundMinor != 65000 || row.UnitsReturned != 1 {
					t.Fatalf("old historical row: %+v", row)
				}
			} else if name == "Golden" || name == "New Pharaonic" {
				if bucket.LineRefundMinor != 140000 || bucket.LineSalesMinor-bucket.LineRefundMinor != 0 || row.UnitsReturned != 2 {
					t.Fatalf("new historical row: %+v", row)
				}
			} else {
				t.Fatalf("unexpected historical label: %s", name)
			}
		}
		if len(seen) != 2 {
			t.Fatalf("missing historical variants: %v", seen)
		}
	}
}
