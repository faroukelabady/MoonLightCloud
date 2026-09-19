package auth

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/MoonLightSoftware/MoonLightCloud/internal/apperr"
	"github.com/MoonLightSoftware/MoonLightCloud/internal/platform/clock"
	"github.com/MoonLightSoftware/MoonLightCloud/internal/platform/ids"
)

// fakeRepo is an in-memory Repository for service tests.
type fakeRepo struct {
	mu sync.Mutex
	m  map[string]Device
}

func newFakeRepo() *fakeRepo { return &fakeRepo{m: map[string]Device{}} }

func (f *fakeRepo) Create(_ context.Context, d Device) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.m[d.ID]; ok {
		return apperr.New(apperr.Conflict, "device already exists")
	}
	f.m[d.ID] = d
	return nil
}

func (f *fakeRepo) ByID(_ context.Context, id string) (Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.m[id]
	if !ok {
		return Device{}, apperr.New(apperr.NotFound, "device not found")
	}
	return d, nil
}

func (f *fakeRepo) TouchLastSeen(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.m[id]
	d.LastSeenAt = &at
	f.m[id] = d
	return nil
}

func (f *fakeRepo) Revoke(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.m[id]
	d.Status = StatusRevoked
	f.m[id] = d
	return nil
}

func testService() (Service, *fakeRepo) {
	repo := newFakeRepo()
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc := NewService(repo, NewHasher("test-pepper"),
		clock.Fixed{T: fixed},
		&ids.Fixed{Values: []string{"11111111-1111-7111-8111-111111111111"}})
	return svc, repo
}

func TestCreateAndAuthenticate(t *testing.T) {
	svc, _ := testService()
	ctx := context.Background()
	p, err := svc.Create(ctx, "shop-dev")
	if err != nil {
		t.Fatal(err)
	}
	if p.RawSecret == "" || p.Device.Status != StatusActive {
		t.Fatalf("bad provision: %+v", p.Device)
	}
	got, err := svc.Authenticate(ctx, p.Device.ID, p.RawSecret)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastSeenAt == nil {
		t.Fatal("last_seen_at must refresh on auth")
	}
}

func TestAuthenticateFailures(t *testing.T) {
	svc, _ := testService()
	ctx := context.Background()
	p, err := svc.Create(ctx, "shop-dev")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][2]string{
		"invalid secret": {p.Device.ID, "deadbeef"},
		"unknown device": {"22222222-2222-7222-8222-222222222222", p.RawSecret},
		"empty":          {"", ""},
	}
	for name, cred := range cases {
		if _, err := svc.Authenticate(ctx, cred[0], cred[1]); err == nil {
			t.Fatalf("%s: want error", name)
		} else if ae, ok := err.(*apperr.Error); !ok || ae.Kind != apperr.Unauthorized {
			t.Fatalf("%s: want UNAUTHORIZED, got %v", name, err)
		}
	}
}

func TestRevokedDeviceRejected(t *testing.T) {
	svc, _ := testService()
	ctx := context.Background()
	p, err := svc.Create(ctx, "shop-dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Revoke(ctx, p.Device.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Authenticate(ctx, p.Device.ID, p.RawSecret); err == nil {
		t.Fatal("revoked device must not authenticate")
	}
}

func TestCreateValidation(t *testing.T) {
	svc, _ := testService()
	if _, err := svc.Create(context.Background(), ""); err == nil {
		t.Fatal("empty name must fail")
	}
}
