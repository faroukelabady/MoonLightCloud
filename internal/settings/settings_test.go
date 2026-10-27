package settings

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

func TestParseMicrorateIsExact(t *testing.T) {
	for raw, want := range map[string]int64{"50": 50_000_000, "50.25": 50_250_000, " 0.000001 ": 1, "48.123456": 48_123_456} {
		got, err := ParseMicrorate(raw)
		if err != nil || got != want {
			t.Fatalf("%q = %d, %v; want %d", raw, got, err, want)
		}
		if back, _ := ParseMicrorate(FormatMicrorate(got)); back != got {
			t.Fatalf("round trip %q -> %s", raw, FormatMicrorate(got))
		}
	}
	for _, raw := range []string{"", "0", "-5", "+5", "1.1234567", "abc", ".5", "1e3", "1000001"} {
		if _, err := ParseMicrorate(raw); err == nil {
			t.Fatalf("%q accepted", raw)
		}
	}
	if FormatMicrorate(50_250_000) != "50.25" || FormatMicrorate(52_000_000) != "52" {
		t.Fatal("format")
	}
}

type memRepo struct {
	business Business
	rates    []NewRate
}

func (m *memRepo) GetBusiness(context.Context) (Business, error) { return m.business, nil }
func (m *memRepo) UpdateBusiness(_ context.Context, next Business, rev int64, _ string) (Business, bool, error) {
	if rev != m.business.Revision {
		return Business{}, false, nil
	}
	next.Revision = rev + 1
	m.business = next
	return next, true, nil
}
func (m *memRepo) ListRates(context.Context, int32) ([]ExchangeRate, error) { return nil, nil }
func (m *memRepo) AddRate(_ context.Context, r NewRate) error {
	m.rates = append(m.rates, r)
	return nil
}
func (m *memRepo) DeleteRate(context.Context, string) (bool, error) { return false, nil }

func TestServiceRejectsStaleRevisionsAndDefaultsRateTime(t *testing.T) {
	now := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	repo := &memRepo{business: Business{Revision: 3}}
	svc := NewService(repo, func() string { return "id-1" }, func() time.Time { return now })
	ctx := context.Background()

	saved, err := svc.UpdateBusiness(ctx, Business{ReturnsEnabled: true}, 3, "u")
	if err != nil || !saved.ReturnsEnabled || saved.Revision != 4 {
		t.Fatalf("update %+v %v", saved, err)
	}
	var ae *apperr.Error
	if _, err := svc.UpdateBusiness(ctx, Business{}, 3, "u"); !errors.As(err, &ae) || ae.Kind != apperr.Conflict {
		t.Fatalf("stale revision: %v", err)
	}

	rate, err := svc.AddRate(ctx, "50.5", time.Time{}, "  bank  ", "u")
	if err != nil || rate.RateMicrorate != 50_500_000 || !rate.EffectiveAt.Equal(now) || rate.Note != "bank" {
		t.Fatalf("rate %+v %v", rate, err)
	}
	if _, err := svc.AddRate(ctx, "50", now.Add(400*24*time.Hour), "", "u"); err == nil {
		t.Fatal("far-future rate accepted")
	}
	if err := svc.DeleteRate(ctx, "missing"); !errors.As(err, &ae) || ae.Kind != apperr.NotFound {
		t.Fatalf("delete missing: %v", err)
	}
}
