package postgres

// Sync page reads against real PostgreSQL: period counts per kind, one sale
// with its costs, the image store counts, and the paged activity feed.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/dashboard"
	"github.com/faroukelabady/MoonLightCloud/internal/media"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

func insightsSalePayload(t *testing.T, saleID string, cost any) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(fixture(t, "sale_egp.json")), &m); err != nil {
		t.Fatal(err)
	}
	m["sale_id"] = saleID
	line := m["lines"].([]any)[0].(map[string]any)
	if cost != nil {
		line["cost"] = cost
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestDashboardInsightsSyncSummaryAndSaleDetail(t *testing.T) {
	env := openDashEnv(t)
	ctx := context.Background()
	saleID := "0192f0a0-0000-7000-8000-00000000a001"
	projectDashSale(t, env, uuid.NewString(), "2026-09-20T10:00:00Z",
		insightsSalePayload(t, saleID, map[string]any{"amount_minor": 30000, "currency": "EGP"}))

	insights := dashboard.NewInsights(NewReporting(env.pool, 5*time.Second), "local", nil, time.Now)
	now := time.Now().UTC()
	period := report.Request{Period: report.Period{StartUTC: now.Add(-time.Hour), EndUTC: now.Add(time.Hour)}}
	summary, err := insights.SyncSummary(ctx, period)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Categories[0].Category != "sales" || summary.Categories[0].Synced != 1 || summary.Categories[0].LastReceivedAt == "" {
		t.Fatalf("sales kind: %+v", summary.Categories)
	}
	if summary.LastReceivedAt == "" || summary.TotalSynced < 1 {
		t.Fatalf("summary: %+v", summary)
	}
	// The count is per period; the newest arrival is not.
	earlier := report.Request{Period: report.Period{StartUTC: now.Add(-48 * time.Hour), EndUTC: now.Add(-24 * time.Hour)}}
	old, err := insights.SyncSummary(ctx, earlier)
	if err != nil || old.Categories[0].Synced != 0 || old.Categories[0].LastReceivedAt == "" {
		t.Fatalf("earlier period: %+v %v", old, err)
	}
	other := report.Request{Period: period.Period, Store: &report.StoreScope{StoreID: uuid.NewString()}}
	if scoped, err := insights.SyncSummary(ctx, other); err != nil || scoped.TotalSynced != 0 || scoped.LastReceivedAt != "" {
		t.Fatalf("another Store sees nothing: %+v %v", scoped, err)
	}

	detail, err := insights.SaleDetail(ctx, report.Request{Store: &report.StoreScope{StoreID: env.storeID}}, saleID)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Lines) != 1 || detail.TotalMinor != "200000" || detail.Currency != "EGP" {
		t.Fatalf("detail: %+v", detail)
	}
	line := detail.Lines[0]
	if line.Quantity != 2 || line.UnitPriceMinor != "100000" || line.LineTotalMinor != "200000" ||
		line.UnitCostMinor == nil || *line.UnitCostMinor != "30000" || *line.LineCostMinor != "60000" {
		t.Fatalf("line: %+v", line)
	}
	if detail.CostMinor != "60000" || !detail.CostComplete {
		t.Fatalf("cost: %s %v", detail.CostMinor, detail.CostComplete)
	}
	_, err = insights.SaleDetail(ctx, other, saleID)
	var ae *apperr.Error
	if !errors.As(err, &ae) || ae.Kind != apperr.NotFound {
		t.Fatalf("another Store's sale is not found: %v", err)
	}
}

func TestDashboardInsightsMediaStatus(t *testing.T) {
	f := openScopeFixture(t)
	ctx := context.Background()
	repo := NewReporting(f.pool, 5*time.Second)
	blobs := NewMediaBlobs(f.pool, 5*time.Second)
	insights := dashboard.NewInsights(repo, "r2", func(context.Context, string) (bool, error) { return true, nil }, time.Now)
	now := time.Now().UTC()
	period := report.Request{Period: report.Period{StartUTC: now.Add(-time.Hour), EndUTC: now.Add(time.Hour)}}

	empty, err := insights.MediaStatus(ctx, period)
	if err != nil || empty.Check != "empty" || empty.StoredFiles != 0 {
		t.Fatalf("empty: %+v %v", empty, err)
	}
	for _, b := range []string{"a", "b"} {
		blob := media.Blob{SHA256: repeatHex(b), ContentType: "image/png", ByteSize: 10, Width: 1, Height: 1}
		if _, err := blobs.StoreBlob(ctx, blob, f.devA); err != nil {
			t.Fatal(err)
		}
	}
	got, err := insights.MediaStatus(ctx, period)
	if err != nil {
		t.Fatal(err)
	}
	if got.Check != "ok" || got.StoredFiles != 2 || got.StoredBytes != 20 || got.UploadedInRange != 2 || got.LastUploadAt == "" {
		t.Fatalf("all Stores: %+v", got)
	}
	// A Store sees only files its galleries name; none name these yet.
	scoped, err := insights.MediaStatus(ctx, report.Request{Period: period.Period, Store: &report.StoreScope{StoreID: scopeStoreA}})
	if err != nil || scoped.StoredFiles != 0 || scoped.Check != "empty" || scoped.GalleryImages != 0 {
		t.Fatalf("Store A: %+v %v", scoped, err)
	}
}

func TestDashboardActivityPages(t *testing.T) {
	env := openDashEnv(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		projectDashSale(t, env, uuid.NewString(), "2026-09-20T10:00:00Z", insightsSalePayload(t, uuid.NewString(), nil))
	}
	// Three sales give six feed items (accepted and projected).
	first, more, err := env.dash.RecentActivityPage(ctx, report.Request{}, 0, 4)
	if err != nil || len(first) != 4 || !more {
		t.Fatalf("first page: %d %v %v", len(first), more, err)
	}
	second, more, err := env.dash.RecentActivityPage(ctx, report.Request{}, 4, 4)
	if err != nil || len(second) != 2 || more {
		t.Fatalf("second page: %d %v %v", len(second), more, err)
	}
	if second[0].EventID == first[0].EventID && second[0].Kind == first[0].Kind {
		t.Fatal("pages must not repeat items")
	}
	if beyond, more, err := env.dash.RecentActivityPage(ctx, report.Request{}, 40, 4); err != nil || len(beyond) != 0 || more {
		t.Fatalf("past the end: %d %v %v", len(beyond), more, err)
	}
	if _, _, err := env.dash.RecentActivityPage(ctx, report.Request{}, 201, 4); err == nil {
		t.Fatal("offset past the bound must fail")
	}
}

func repeatHex(c string) string {
	out := ""
	for len(out) < 64 {
		out += c
	}
	return out
}
