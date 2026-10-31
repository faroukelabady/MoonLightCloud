package postgres

// Fetched USD->EGP rates against real PostgreSQL (ADR-0058): one row per
// change even when several Cloud instances fetch at once.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/faroukelabady/MoonLightCloud/internal/settings"
)

func TestFetchedRatesStoreOnlyChangesOnce(t *testing.T) {
	env := openDashEnv(t)
	ctx := context.Background()
	repo := NewBusinessSettings(env.pool, 5*time.Second)
	fetched := func(micro int64) settings.NewRate {
		// EffectiveAt is ignored: the database clock starts the rate.
		return settings.NewRate{ID: uuid.NewString(), RateMicrorate: micro, EffectiveAt: time.Unix(0, 0), Source: "open.er-api.com"}
	}

	// Eight instances fetch the same rate at the same moment.
	var wg sync.WaitGroup
	stored := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := repo.AddFetchedRate(ctx, fetched(48_500_000))
			if err != nil {
				t.Error(err)
			}
			stored <- ok
		}()
	}
	wg.Wait()
	close(stored)
	n := 0
	for ok := range stored {
		if ok {
			n++
		}
	}
	rates, err := repo.ListRates(ctx, 10)
	if err != nil || n != 1 || len(rates) != 1 || rates[0].Source != "open.er-api.com" || rates[0].Note != "" {
		t.Fatalf("one row for one change: stored=%d rows=%+v %v", n, rates, err)
	}
	if d := time.Since(rates[0].EffectiveAt); d < 0 || d > time.Minute {
		t.Fatalf("starts now: %v", rates[0].EffectiveAt)
	}

	// A person's rate keeps source manual; the next fetch compares with it.
	svc := settings.NewService(repo, uuid.NewString, time.Now)
	if _, err := svc.AddRate(ctx, "49", time.Time{}, "bank", ""); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.AddFetchedRate(ctx, fetched(49_000_000)); err != nil || ok {
		t.Fatalf("same as the manual rate in effect: %v %v", ok, err)
	}
	if ok, err := repo.AddFetchedRate(ctx, fetched(48_500_000)); err != nil || !ok {
		t.Fatalf("back to the provider rate: %v %v", ok, err)
	}
	rates, _ = repo.ListRates(ctx, 10)
	if len(rates) != 3 || rates[0].Source != "open.er-api.com" || rates[1].Source != settings.SourceManual {
		t.Fatalf("history: %+v", rates)
	}
	// A rate typed for the future does not count as in effect now.
	if _, err := svc.AddRate(ctx, "60", time.Now().Add(48*time.Hour), "", ""); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.AddFetchedRate(ctx, fetched(48_500_000)); err != nil || ok {
		t.Fatalf("future rate is not in effect yet: %v %v", ok, err)
	}
}
