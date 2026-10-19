package dashboard

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/report"
)

type dashboardRead struct {
	operation, store, mode string
	start, end             time.Time
	limit                  int
}
type dashboardScopeCapture struct {
	Repository
	calls   []dashboardRead
	failure error
}

func (r *dashboardScopeCapture) DashboardNormalizedSummary(context.Context, time.Time, time.Time) (NormalizedSummaryRow, error) {
	r.calls = append(r.calls, dashboardRead{operation: "global"})
	return NormalizedSummaryRow{}, r.failure
}
func (r *dashboardScopeCapture) DashboardNormalizedSummaryForStore(_ context.Context, store string, _, _ time.Time) (NormalizedSummaryRow, error) {
	r.calls = append(r.calls, dashboardRead{operation: "store", store: store})
	return NormalizedSummaryRow{}, r.failure
}
func (r *dashboardScopeCapture) SalesSummaryForStore(_ context.Context, store string, start, end time.Time, currency string) ([]report.SummaryRow, error) {
	r.calls = append(r.calls, dashboardRead{operation: "native", store: store, start: start, end: end, mode: currency})
	return nil, nil
}
func (r *dashboardScopeCapture) DashboardNormalizedDailyForStore(_ context.Context, store string, start, end time.Time, timezone string) ([]NormalizedDailyRow, error) {
	r.calls = append(r.calls, dashboardRead{operation: "normalized", store: store, start: start, end: end, mode: timezone})
	return nil, nil
}
func (r *dashboardScopeCapture) DashboardRecentActivityForStore(_ context.Context, store string, limit int) ([]ActivityItem, error) {
	r.calls = append(r.calls, dashboardRead{operation: "activity", store: store, limit: limit})
	return nil, nil
}
func TestDashboardReaderPreservesScopeAndErrors(t *testing.T) {
	failure := errors.New("read unavailable")
	for _, tc := range []struct {
		name  string
		scope *report.StoreScope
		store string
	}{
		{name: "nil global"},
		{name: "blank global", scope: &report.StoreScope{}},
		{name: "Store A", scope: &report.StoreScope{StoreID: "01910000-0000-7000-8000-000000000001"}, store: "01910000-0000-7000-8000-000000000001"},
		{name: "unknown Store", scope: &report.StoreScope{StoreID: "01910000-0000-7000-8000-000000000003"}, store: "01910000-0000-7000-8000-000000000003"},
		{name: "manual nonempty is never global", scope: &report.StoreScope{StoreID: " "}, store: " "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &dashboardScopeCapture{failure: failure}
			_, err := ReaderFor(repo, tc.scope).DashboardNormalizedSummary(context.Background(), time.Time{}, time.Time{})
			if err != failure {
				t.Fatalf("error changed: %v", err)
			}
			operation := "global"
			if tc.store != "" {
				operation = "store"
			}
			want := dashboardRead{operation: operation, store: tc.store}
			if len(repo.calls) != 1 || repo.calls[0] != want {
				t.Fatalf("scope changed: %+v, want %+v", repo.calls, want)
			}
		})
	}
}
func TestDashboardReaderSharesCapturedFinancialScopeAndParameters(t *testing.T) {
	repo := &dashboardScopeCapture{}
	scope := &report.StoreScope{StoreID: "Store A"}
	reader := ReaderFor(repo, scope)
	scope.StoreID = "Store B"
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	ctx := context.Background()
	if _, err := reader.SalesSummary(ctx, start, end, "USD"); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.DashboardNormalizedDaily(ctx, start, end, "Africa/Cairo"); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.DashboardRecentActivity(ctx, 17); err != nil {
		t.Fatal(err)
	}
	want := []dashboardRead{
		{operation: "native", store: "Store A", start: start, end: end, mode: "USD"},
		{operation: "normalized", store: "Store A", start: start, end: end, mode: "Africa/Cairo"},
		{operation: "activity", store: "Store A", limit: 17},
	}
	if len(repo.calls) != len(want) {
		t.Fatalf("wrong call count: %+v", repo.calls)
	}
	for i := range want {
		if repo.calls[i] != want[i] {
			t.Fatalf("read %d: %+v, want %+v", i, repo.calls[i], want[i])
		}
	}
}
