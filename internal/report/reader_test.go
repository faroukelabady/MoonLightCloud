package report

import (
	"context"
	"errors"
	"testing"
	"time"
)

type readerCall struct {
	operation, store, currency string
	start, end                 time.Time
}
type scopeCapture struct {
	Repository
	calls   []readerCall
	failure error
}

func (r *scopeCapture) SalesSummary(_ context.Context, start, end time.Time, currency string) ([]SummaryRow, error) {
	r.calls = append(r.calls, readerCall{operation: "global", currency: currency, start: start, end: end})
	return []SummaryRow{{Currency: currency}}, nil
}
func (r *scopeCapture) SalesSummaryForStore(_ context.Context, store string, start, end time.Time, currency string) ([]SummaryRow, error) {
	r.calls = append(r.calls, readerCall{operation: "store", store: store, currency: currency, start: start, end: end})
	return []SummaryRow{{Currency: currency}}, r.failure
}
func TestReaderForPreservesScopeAndQuerySemantics(t *testing.T) {
	invalid := errors.New("invalid Store")
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	for _, tc := range []struct {
		name             string
		scope            *StoreScope
		operation, store string
		failure          error
	}{
		{name: "nil is global", operation: "global"},
		{name: "blank is global", scope: &StoreScope{}, operation: "global"},
		{name: "Store A", scope: &StoreScope{StoreID: "01910000-0000-7000-8000-000000000001"}, operation: "store", store: "01910000-0000-7000-8000-000000000001"},
		{name: "Store B", scope: &StoreScope{StoreID: "01910000-0000-7000-8000-000000000002"}, operation: "store", store: "01910000-0000-7000-8000-000000000002"},
		{name: "unknown valid Store", scope: &StoreScope{StoreID: "01910000-0000-7000-8000-000000000003"}, operation: "store", store: "01910000-0000-7000-8000-000000000003"},
		{name: "manual whitespace is not global", scope: &StoreScope{StoreID: " "}, operation: "store", store: " ", failure: invalid},
		{name: "manual invalid is not global", scope: &StoreScope{StoreID: "invalid"}, operation: "store", store: "invalid", failure: invalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &scopeCapture{failure: tc.failure}
			rows, err := ReaderFor(repo, tc.scope).SalesSummary(context.Background(), start, end, "USD")
			if err != tc.failure {
				t.Fatalf("error changed: %v", err)
			}
			if len(rows) != 1 || rows[0].Currency != "USD" {
				t.Fatalf("row changed: %+v", rows)
			}
			want := readerCall{operation: tc.operation, store: tc.store, currency: "USD", start: start, end: end}
			if len(repo.calls) != 1 || repo.calls[0] != want {
				t.Fatalf("wrong scope/query: %+v, want %+v", repo.calls, want)
			}
		})
	}
}
func TestReadersCaptureIndependentStoreSelections(t *testing.T) {
	repo := &scopeCapture{}
	scope := &StoreScope{StoreID: "Store A"}
	a := ReaderFor(repo, scope)
	scope.StoreID = "Store B"
	b := ReaderFor(repo, scope)
	scope.StoreID = ""
	for _, reader := range []Reader{a, b, a} {
		if _, err := reader.SalesSummary(context.Background(), time.Time{}, time.Time{}, ""); err != nil {
			t.Fatal(err)
		}
	}
	for i, want := range []string{"Store A", "Store B", "Store A"} {
		if repo.calls[i].store != want {
			t.Fatalf("reader switched scope: %+v", repo.calls)
		}
	}
}
