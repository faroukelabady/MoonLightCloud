package settings

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

type fakeSource struct {
	name  string
	micro int64
	err   error
	calls int
}

func (f *fakeSource) Name() string { return f.name }
func (f *fakeSource) USDToEGP(context.Context) (int64, error) {
	f.calls++
	return f.micro, f.err
}

func TestFetcherStoresOnlyChangedRates(t *testing.T) {
	now := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	repo := &memRepo{}
	svc := NewService(repo, func() string { return "id" }, func() time.Time { return now })
	src := &fakeSource{name: "open.er-api.com", micro: 48_712_346}
	f := NewFetcher(svc, src, slog.Default())
	ctx := context.Background()

	got, err := f.Refresh(ctx)
	if err != nil || !got.Changed || got.Rate != "48.712346" || got.Source != "open.er-api.com" || !got.FetchedAt.Equal(now) {
		t.Fatalf("first fetch: %+v %v", got, err)
	}
	if r := repo.rates[0]; r.Source != "open.er-api.com" || !r.EffectiveAt.Equal(now) || r.Note != "" {
		t.Fatalf("stored: %+v", r)
	}
	if got, err := f.Refresh(ctx); err != nil || got.Changed || len(repo.rates) != 1 {
		t.Fatalf("same rate again: %+v %v rows=%d", got, err, len(repo.rates))
	}
	src.micro = 49_000_000
	if got, err := f.Refresh(ctx); err != nil || !got.Changed || len(repo.rates) != 2 {
		t.Fatalf("new rate: %+v %v", got, err)
	}
}

func TestFetcherKeepsStoredRateOnProviderFaults(t *testing.T) {
	repo := &memRepo{}
	svc := NewService(repo, func() string { return "id" }, time.Now)
	ctx := context.Background()
	for name, src := range map[string]*fakeSource{
		"network":   {name: "p.example", err: errors.New("dial")},
		"too low":   {name: "p.example", micro: 999_999},
		"too high":  {name: "p.example", micro: 10_000*1_000_000 + 1},
		"zero rate": {name: "p.example"},
	} {
		_, err := NewFetcher(svc, src, slog.Default()).Refresh(ctx)
		var ae *apperr.Error
		if !errors.As(err, &ae) || ae.Kind != apperr.Unavailable || ae.Message != "RATE_PROVIDER_FAILED" {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if len(repo.rates) != 0 {
		t.Fatalf("a fault stored a rate: %+v", repo.rates)
	}
}

func TestFetcherClickWithinAMinuteReusesTheAnswer(t *testing.T) {
	now := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	repo := &memRepo{}
	svc := NewService(repo, func() string { return "id" }, func() time.Time { return now })
	src := &fakeSource{name: "open.er-api.com", micro: 48_000_000}
	f := NewFetcher(svc, src, slog.Default())
	ctx := context.Background()
	if got, err := f.RefreshNow(ctx); err != nil || !got.Changed {
		t.Fatalf("first click: %+v %v", got, err)
	}
	now = now.Add(30 * time.Second)
	if got, err := f.RefreshNow(ctx); err != nil || got.Changed || got.Rate != "48" || src.calls != 1 {
		t.Fatalf("second click: %+v %v calls=%d", got, err, src.calls)
	}
	now = now.Add(time.Minute)
	if _, err := f.RefreshNow(ctx); err != nil || src.calls != 2 {
		t.Fatalf("later click: %v calls=%d", err, src.calls)
	}
}

func TestFetcherOffAndOddNames(t *testing.T) {
	svc := NewService(&memRepo{}, func() string { return "id" }, time.Now)
	var off *Fetcher = NewFetcher(svc, nil, slog.Default())
	if off != nil || off.Enabled() || off.Source() != "" {
		t.Fatal("nil source must turn fetching off")
	}
	var ae *apperr.Error
	if _, err := off.RefreshNow(context.Background()); !errors.As(err, &ae) || ae.Message != "RATE_FETCH_DISABLED" {
		t.Fatalf("off: %v", err)
	}
	off.Run(context.Background()) // returns at once
	if got := NewFetcher(svc, &fakeSource{name: "Bad Host:80"}, slog.Default()).Source(); got != "provider" {
		t.Fatalf("odd name kept: %q", got)
	}
}
