package http

import (
	"context"
	"sync"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
)

// memRepo backs middleware tests without a database.
type memRepo struct {
	mu sync.Mutex
	m  map[string]auth.Device
}

func (f *memRepo) Create(_ context.Context, d auth.Device) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[d.ID] = d
	return nil
}

func (f *memRepo) ByID(_ context.Context, id string) (auth.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.m[id]
	if !ok {
		return auth.Device{}, apperr.New(apperr.NotFound, "device not found")
	}
	return d, nil
}

func (f *memRepo) TouchLastSeen(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.m[id]
	d.LastSeenAt = &at
	f.m[id] = d
	return nil
}

func (f *memRepo) Revoke(_ context.Context, id string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.m[id]
	d.Status = auth.StatusRevoked
	f.m[id] = d
	return nil
}

func deviceSvcForTest(r *memRepo) auth.Service {
	return auth.NewService(r, auth.NewHasher("test"),
		clock.Fixed{T: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		&ids.Fixed{Values: []string{"11111111-1111-7111-8111-111111111111"}})
}
