package dashboard

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/media"
	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

type fakeInsights struct {
	storeID string
	sync    []SyncCategoryRow
	head    SaleDetailRow
	lines   []SaleDetailLineRow
	media   MediaStatusRow
}

func (f *fakeInsights) DashboardSyncSummary(_ context.Context, storeID string, _, _, _, _ time.Time) ([]SyncCategoryRow, error) {
	f.storeID = storeID
	return f.sync, nil
}

func (f *fakeInsights) DashboardSaleDetail(_ context.Context, storeID, _ string) (SaleDetailRow, []SaleDetailLineRow, error) {
	f.storeID = storeID
	return f.head, f.lines, nil
}

func (f *fakeInsights) DashboardMediaStatus(_ context.Context, storeID string, _, _ time.Time) (MediaStatusRow, error) {
	f.storeID = storeID
	return f.media, nil
}

func i64(v int64) *int64 { return &v }

func TestSyncSummaryOrdersKindsAndDropsOthers(t *testing.T) {
	older := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)
	repo := &fakeInsights{sync: []SyncCategoryRow{
		{Category: "other", Synced: 99, LastReceivedAt: &newer},
		{Category: "catalog", Synced: 4, LastReceivedAt: &newer},
		{Category: "sales", Synced: 7, LastReceivedAt: &older},
	}}
	svc := NewInsights(repo, "r2", nil, time.Now)
	got, err := svc.SyncSummary(context.Background(), report.Request{Store: &report.StoreScope{StoreID: "store-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if repo.storeID != "store-a" {
		t.Fatalf("scope not passed: %q", repo.storeID)
	}
	var kinds []string
	for _, c := range got.Categories {
		kinds = append(kinds, c.Category)
	}
	if len(kinds) != 4 || kinds[0] != "sales" || kinds[1] != "returns" || kinds[2] != "catalog" || kinds[3] != "stock" {
		t.Fatalf("kinds: %v", kinds)
	}
	if got.TotalSynced != 11 || got.Categories[1].Synced != 0 || got.Categories[1].LastReceivedAt != "" {
		t.Fatalf("counts: %+v", got)
	}
	if got.LastReceivedAt != newer.Format(time.RFC3339) {
		t.Fatalf("newest arrival from the reported kinds: %q", got.LastReceivedAt)
	}
}

func TestSaleDetailCostsAndProfitInputs(t *testing.T) {
	repo := &fakeInsights{
		head: SaleDetailRow{SaleID: "s", SaleNumber: "S-1", Currency: "EGP", Subtotal: 35000, Total: 35000},
		lines: []SaleDetailLineRow{
			{SKU: "A", Quantity: 2, UnitPriceMinor: 10000, LineTotalMinor: 20000, UnitCostMinor: i64(4000)},
			{SKU: "B", Quantity: 1, UnitPriceMinor: 15000, LineTotalMinor: 15000, UnitCostMinor: i64(6000)},
		},
	}
	svc := NewInsights(repo, "r2", nil, time.Now)
	id := "0192f0a0-0000-7000-8000-000000000001"
	got, err := svc.SaleDetail(context.Background(), report.Request{}, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.CostMinor != "14000" || !got.CostComplete {
		t.Fatalf("cost: %s complete=%v", got.CostMinor, got.CostComplete)
	}
	if *got.Lines[0].LineCostMinor != "8000" || *got.Lines[0].UnitCostMinor != "4000" {
		t.Fatalf("line cost: %+v", got.Lines[0])
	}
	repo.lines[1].UnitCostMinor = nil
	got, err = svc.SaleDetail(context.Background(), report.Request{}, id)
	if err != nil || got.CostComplete || got.CostMinor != "8000" || got.Lines[1].LineCostMinor != nil {
		t.Fatalf("unknown cost stays unknown: %+v %v", got, err)
	}
	if _, err := svc.SaleDetail(context.Background(), report.Request{}, "not-a-uuid"); !isKind(err, apperr.InvalidInput) {
		t.Fatalf("bad id: %v", err)
	}
	repo.lines = []SaleDetailLineRow{{Quantity: 3, UnitCostMinor: i64(1 << 62)}}
	if _, err := svc.SaleDetail(context.Background(), report.Request{}, id); err == nil {
		t.Fatal("overflowing cost must fail, not wrap")
	}
}

func TestMediaStatusChecksNewestFile(t *testing.T) {
	last := time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC)
	repo := &fakeInsights{media: MediaStatusRow{StoredFiles: 3, LatestSHA256: "abc", LastUploadAt: &last, GalleryImages: 5, WaitingImages: 2}}
	for _, tc := range []struct {
		name  string
		probe MediaProbe
		want  string
	}{
		{"found", func(context.Context, string) (bool, error) { return true, nil }, "ok"},
		{"missing", func(context.Context, string) (bool, error) { return false, nil }, "missing"},
		{"down", func(context.Context, string) (bool, error) { return false, errors.New("timeout") }, "error"},
	} {
		svc := NewInsights(repo, "r2", tc.probe, time.Now)
		got, err := svc.MediaStatus(context.Background(), report.Request{})
		if err != nil || got.Check != tc.want || got.Storage != "r2" || got.WaitingImages != 2 || got.LastUploadAt != last.Format(time.RFC3339) {
			t.Fatalf("%s: %+v %v", tc.name, got, err)
		}
	}
	repo.media.LatestSHA256 = ""
	probed := false
	svc := NewInsights(repo, "local", func(context.Context, string) (bool, error) { probed = true; return true, nil }, time.Now)
	got, err := svc.MediaStatus(context.Background(), report.Request{})
	if err != nil || got.Check != "empty" || probed {
		t.Fatalf("no file to check: %+v %v probed=%v", got, err, probed)
	}
}

func isKind(err error, kind apperr.Kind) bool {
	var ae *apperr.Error
	return errors.As(err, &ae) && ae.Kind == kind
}

func TestBucketUsageIsExplicitAndNeverLeaksAcrossStores(t *testing.T) {
	calls := 0
	probe := func(context.Context) (media.Usage, error) {
		calls++
		return media.Usage{Files: 5, Bytes: 9000, Complete: true}, nil
	}
	svc := NewInsights(&fakeInsights{}, "r2", nil, time.Now).WithStorageUsage(probe)
	got, err := svc.MediaStatus(context.Background(), report.Request{})
	if err != nil || got.BucketUsage == nil || got.BucketUsage.Files != 5 || got.BucketUsageState != "complete" || got.Check != "ok" {
		t.Fatalf("bucket contents hidden by empty DB: %+v %v", got, err)
	}
	scoped, err := svc.MediaStatus(context.Background(), report.Request{Store: &report.StoreScope{StoreID: "A"}})
	if err != nil || scoped.BucketUsage != nil || scoped.BucketUsageState != "scope_limited" || calls != 1 {
		t.Fatalf("cross-store bucket totals exposed: %+v %v calls=%d", scoped, err, calls)
	}
	svc = svc.WithStorageUsage(func(context.Context) (media.Usage, error) {
		return media.Usage{}, errors.New("private-provider-message")
	})
	got, err = svc.MediaStatus(context.Background(), report.Request{})
	if err != nil || got.BucketUsage != nil || got.BucketUsageState != "unavailable" || got.Check != "error" {
		t.Fatalf("failed request shown as empty bucket: %+v %v", got, err)
	}
}
