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
	mu    sync.Mutex
	devs  map[string]auth.Device
	creds map[string]auth.Credential
}

func newMemRepo() *memRepo {
	return &memRepo{devs: map[string]auth.Device{}, creds: map[string]auth.Credential{}}
}

func (f *memRepo) CreateDevice(_ context.Context, d auth.Device) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.devs[d.ID] = d
	return nil
}

func (f *memRepo) DeviceByID(_ context.Context, id string) (auth.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.devs[id]
	if !ok {
		return auth.Device{}, apperr.New(apperr.NotFound, "device not found")
	}
	return d, nil
}

func (f *memRepo) ListDevices(_ context.Context) ([]auth.Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []auth.Device
	for _, d := range f.devs {
		out = append(out, d)
	}
	return out, nil
}

func (f *memRepo) TouchLastSeen(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.devs[id]
	d.LastSeenAt = &at
	f.devs[id] = d
	return nil
}

func (f *memRepo) RevokeDevice(_ context.Context, id string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.devs[id]
	d.Status = auth.StatusRevoked
	f.devs[id] = d
	return nil
}

func (f *memRepo) CreateCredential(_ context.Context, c auth.Credential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creds[c.ID] = c
	return nil
}

func (f *memRepo) CredentialByID(_ context.Context, id string) (auth.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.creds[id]
	if !ok {
		return auth.Credential{}, apperr.New(apperr.NotFound, "device not found")
	}
	return c, nil
}

func (f *memRepo) ActiveCredentials(_ context.Context, deviceID string) ([]auth.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []auth.Credential
	for _, c := range f.creds {
		if c.DeviceID == deviceID && c.Active() {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *memRepo) CredentialsForDevice(_ context.Context, deviceID string) ([]auth.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []auth.Credential
	for _, c := range f.creds {
		if c.DeviceID == deviceID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *memRepo) TouchCredentialLastUsed(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.creds[id]
	c.LastUsedAt = &at
	f.creds[id] = c
	return nil
}

func (f *memRepo) RevokeCredential(_ context.Context, id string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.creds[id]
	c.Status = auth.CredentialRevoked
	f.creds[id] = c
	return nil
}

func (f *memRepo) Provision(_ context.Context, d auth.Device, c auth.Credential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.devs[d.ID] = d
	f.creds[c.ID] = c
	return nil
}

func (f *memRepo) Rotate(_ context.Context, c auth.Credential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creds[c.ID] = c
	for id, o := range f.creds {
		if o.DeviceID == c.DeviceID && id != c.ID && o.Active() {
			o.Status = auth.CredentialRevoked
			f.creds[id] = o
		}
	}
	return nil
}

func (f *memRepo) RevokeDeviceAll(_ context.Context, id string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.devs[id]
	d.Status = auth.StatusRevoked
	f.devs[id] = d
	for cid, c := range f.creds {
		if c.DeviceID == id {
			c.Status = auth.CredentialRevoked
			f.creds[cid] = c
		}
	}
	return nil
}

func deviceSvcForTest(r *memRepo) auth.Service {
	h, err := auth.NewHasher(make([]byte, 32))
	if err != nil {
		panic(err)
	}
	return auth.NewService(r, h, "", 1,
		clock.Fixed{T: time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)},
		&ids.Fixed{Values: []string{
			"11111111-1111-7111-8111-111111111111",
			"22222222-2222-7222-8222-222222222222",
		}})
}
