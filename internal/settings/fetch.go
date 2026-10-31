package settings

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// RateSource is an outside USD->EGP provider (ADR-0058). Adapters keep the
// provider's wire format and network client out of this package.
type RateSource interface {
	// Name is the provider host, recorded as the fetched rate's source.
	Name() string
	// USDToEGP returns the provider's current rate in millionths.
	USDToEGP(ctx context.Context) (int64, error)
}

const (
	// FetchInterval is how often Cloud fetches the rate, as Retail does.
	FetchInterval = 12 * time.Hour
	// A fetched rate outside 1..10,000 EGP per USD is a provider fault.
	minFetchedMicrorate = 1_000_000
	maxFetchedMicrorate = 10_000 * 1_000_000
	// A second "Fetch latest rate" click within this gap reuses the last
	// answer instead of calling the provider again.
	manualFetchGap = time.Minute
)

var sourceName = regexp.MustCompile(`^[a-z0-9.-]{1,64}$`)

// FetchResult is one fetch: the provider rate and whether it was stored.
// Changed is false when the rate in effect already had that value.
type FetchResult struct {
	Rate          string    `json:"rate"`
	RateMicrorate int64     `json:"rate_microrate"`
	Source        string    `json:"source"`
	FetchedAt     time.Time `json:"fetched_at"`
	Changed       bool      `json:"changed"`
}

// Fetcher keeps the USD->EGP rate current from a RateSource. A nil
// *Fetcher means fetching is off; selling and reports use the stored rates.
type Fetcher struct {
	svc    Service
	source RateSource
	name   string
	log    *slog.Logger

	mu   sync.Mutex
	last FetchResult
}

// NewFetcher wires a fetcher; a nil source turns fetching off (nil result).
func NewFetcher(svc Service, source RateSource, log *slog.Logger) *Fetcher {
	if source == nil {
		return nil
	}
	name := source.Name()
	if !sourceName.MatchString(name) {
		name = "provider"
	}
	return &Fetcher{svc: svc, source: source, name: name, log: log}
}

// Enabled reports whether a provider is configured.
func (f *Fetcher) Enabled() bool { return f != nil }

// Source names the provider, or "" when fetching is off.
func (f *Fetcher) Source() string {
	if f == nil {
		return ""
	}
	return f.name
}

// Refresh fetches the provider rate and stores it, starting now, when it
// differs from the rate in effect. Provider faults keep the stored rate.
func (f *Fetcher) Refresh(ctx context.Context) (FetchResult, error) {
	if f == nil {
		return FetchResult{}, apperr.New(apperr.Conflict, "RATE_FETCH_DISABLED")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refresh(ctx)
}

// RefreshNow is Refresh for a person's click: a repeat within a minute
// returns the last answer without calling the provider.
func (f *Fetcher) RefreshNow(ctx context.Context) (FetchResult, error) {
	if f == nil {
		return FetchResult{}, apperr.New(apperr.Conflict, "RATE_FETCH_DISABLED")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.last.FetchedAt.IsZero() && f.svc.now().Sub(f.last.FetchedAt) < manualFetchGap {
		repeat := f.last
		repeat.Changed = false
		return repeat, nil
	}
	return f.refresh(ctx)
}

func (f *Fetcher) refresh(ctx context.Context) (FetchResult, error) {
	micro, err := f.source.USDToEGP(ctx)
	if err != nil {
		return FetchResult{}, apperr.Wrap(apperr.Unavailable, "RATE_PROVIDER_FAILED", err)
	}
	if micro < minFetchedMicrorate || micro > maxFetchedMicrorate {
		return FetchResult{}, apperr.New(apperr.Unavailable, "RATE_PROVIDER_FAILED")
	}
	now := f.svc.now().UTC()
	stored, err := f.svc.repo.AddFetchedRate(ctx, NewRate{
		ID: f.svc.newID(), RateMicrorate: micro, EffectiveAt: now, Source: f.name,
	})
	if err != nil {
		return FetchResult{}, err
	}
	f.last = FetchResult{Rate: FormatMicrorate(micro), RateMicrorate: micro, Source: f.name, FetchedAt: now, Changed: stored}
	return f.last, nil
}

// Run refreshes at startup and every FetchInterval until ctx ends.
// Failures are logged by class only and never stop selling or reports.
func (f *Fetcher) Run(ctx context.Context) {
	if f == nil {
		return
	}
	ticker := time.NewTicker(FetchInterval)
	defer ticker.Stop()
	for {
		res, err := f.Refresh(ctx)
		switch {
		case err != nil && ctx.Err() == nil:
			f.log.Warn("exchange rate fetch failed", "source", f.name, "error_class", errClass(err))
		case err == nil && res.Changed:
			f.log.Info("exchange rate fetched", "source", f.name, "rate", res.Rate)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func errClass(err error) string {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae.Message
	}
	return "internal"
}
