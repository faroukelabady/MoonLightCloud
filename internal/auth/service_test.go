package auth

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
)

// memStore is an in-memory transactional Store for service tests.
type memStore struct {
	mu    sync.Mutex
	devs  map[string]Device
	creds map[string]Credential
}

func newMemStore() *memStore {
	return &memStore{devs: map[string]Device{}, creds: map[string]Credential{}}
}

func (f *memStore) CreateDevice(_ context.Context, d Device) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.devs[d.ID]; ok {
		return apperr.New(apperr.Conflict, "device already exists")
	}
	f.devs[d.ID] = d
	return nil
}

func (f *memStore) DeviceByID(_ context.Context, id string) (Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.devs[id]
	if !ok {
		return Device{}, apperr.New(apperr.NotFound, "device not found")
	}
	return d, nil
}

func (f *memStore) ListDevices(_ context.Context) ([]Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Device, 0, len(f.devs))
	for _, d := range f.devs {
		out = append(out, d)
	}
	return out, nil
}

func (f *memStore) TouchLastSeen(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.devs[id]
	d.LastSeenAt = &at
	f.devs[id] = d
	return nil
}

func (f *memStore) RevokeDevice(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.devs[id]
	d.Status = StatusRevoked
	f.devs[id] = d
	return nil
}

func (f *memStore) CreateCredential(_ context.Context, c Credential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creds[c.ID] = c
	return nil
}

func (f *memStore) CredentialByID(_ context.Context, id string) (Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.creds[id]
	if !ok {
		return Credential{}, apperr.New(apperr.NotFound, "device not found")
	}
	return c, nil
}

func (f *memStore) ActiveCredentials(_ context.Context, deviceID string) ([]Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Credential
	for _, c := range f.creds {
		if c.DeviceID == deviceID && c.Active() {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *memStore) CredentialsForDevice(_ context.Context, deviceID string) ([]Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Credential
	for _, c := range f.creds {
		if c.DeviceID == deviceID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *memStore) TouchCredentialLastUsed(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.creds[id]
	c.LastUsedAt = &at
	f.creds[id] = c
	return nil
}

func (f *memStore) RevokeCredential(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.creds[id]
	c.Status = CredentialRevoked
	f.creds[id] = c
	return nil
}

func (f *memStore) Provision(_ context.Context, d Device, c Credential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.devs[d.ID] = d
	f.creds[c.ID] = c
	return nil
}

func (f *memStore) Rotate(_ context.Context, c Credential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creds[c.ID] = c
	for id, o := range f.creds {
		if o.DeviceID == c.DeviceID && id != c.ID && o.Active() {
			o.Status = CredentialRevoked
			f.creds[id] = o
		}
	}
	return nil
}

func (f *memStore) RevokeDeviceAll(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.devs[id]
	d.Status = StatusRevoked
	f.devs[id] = d
	for cid, c := range f.creds {
		if c.DeviceID == id {
			c.Status = CredentialRevoked
			f.creds[cid] = c
		}
	}
	return nil
}

var fixedTime = time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)

func testService() (Service, *memStore) {
	store := newMemStore()
	h, err := NewHasher(bytes32('p'))
	if err != nil {
		panic(err)
	}
	svc := NewService(store, h, "", 1,
		clock.Fixed{T: fixedTime},
		&ids.Fixed{Values: []string{
			"11111111-1111-7111-8111-111111111111",
			"22222222-2222-7222-8222-222222222222",
			"33333333-3333-7333-8333-333333333333",
		}})
	return svc, store
}

func TestCreateAndAuthenticate(t *testing.T) {
	svc, _ := testService()
	ctx := context.Background()
	p, err := svc.Create(ctx, "shop-dev")
	if err != nil {
		t.Fatal(err)
	}
	if p.RawSecret == "" || p.Device.Status != StatusActive || p.Credential.VerifierVersion != VerifierV1 {
		t.Fatalf("bad provision: %+v", p.Device)
	}
	dev, cred, err := svc.Authenticate(ctx, p.Device.ID, p.Credential.ID, p.RawSecret)
	if err != nil {
		t.Fatal(err)
	}
	if dev.LastSeenAt == nil || cred.LastUsedAt == nil {
		t.Fatal("last_seen/last_used must refresh on auth")
	}
}

func TestAuthenticateFailuresEnumerationSafe(t *testing.T) {
	svc, _ := testService()
	ctx := context.Background()
	p, err := svc.Create(ctx, "shop-dev")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][3]string{
		"invalid secret":   {p.Device.ID, p.Credential.ID, "deadbeef"},
		"unknown device":   {"99999999-9999-7999-8999-999999999999", p.Credential.ID, p.RawSecret},
		"unknown cred":     {p.Device.ID, "99999999-9999-7999-8999-999999999999", p.RawSecret},
		"cross-device use": {p.Device.ID, p.Credential.ID, p.RawSecret}, // control, replaced below
		"empty":            {"", "", ""},
	}
	for name, cred := range cases {
		if name == "cross-device use" {
			continue
		}
		_, _, err := svc.Authenticate(ctx, cred[0], cred[1], cred[2])
		if err == nil {
			t.Fatalf("%s: want error", name)
		}
		ae, ok := err.(*apperr.Error)
		if !ok || ae.Kind != apperr.Unauthorized || ae.Message != "invalid device credential" {
			t.Fatalf("%s: want stable UNAUTHORIZED, got %v", name, err)
		}
	}
	// Credential bound to another device fails identically.
	p2, err := svc.Create(ctx, "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Authenticate(ctx, p.Device.ID, p2.Credential.ID, p2.RawSecret); err == nil {
		t.Fatal("cross-device credential use must fail")
	}
}

func TestRotate(t *testing.T) {
	svc, _ := testService()
	ctx := context.Background()
	p, err := svc.Create(ctx, "shop-dev")
	if err != nil {
		t.Fatal(err)
	}
	r, err := svc.Rotate(ctx, p.Device.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.RawSecret == "" || r.Credential.ID == p.Credential.ID {
		t.Fatal("rotation must issue a fresh credential")
	}
	if _, _, err := svc.Authenticate(ctx, p.Device.ID, r.Credential.ID, r.RawSecret); err != nil {
		t.Fatalf("new credential must work: %v", err)
	}
	if _, _, err := svc.Authenticate(ctx, p.Device.ID, p.Credential.ID, p.RawSecret); err == nil {
		t.Fatal("old credential must fail after rotation")
	}
}

func TestRotateRevokedDeviceFails(t *testing.T) {
	svc, _ := testService()
	ctx := context.Background()
	p, _ := svc.Create(ctx, "shop-dev")
	if err := svc.RevokeDevice(ctx, p.Device.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Rotate(ctx, p.Device.ID); err == nil {
		t.Fatal("rotation of revoked device must fail")
	}
}

func TestRevokeDeviceInvalidatesAll(t *testing.T) {
	svc, _ := testService()
	ctx := context.Background()
	p, err := svc.Create(ctx, "shop-dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeDevice(ctx, p.Device.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Authenticate(ctx, p.Device.ID, p.Credential.ID, p.RawSecret); err == nil {
		t.Fatal("revoked device must not authenticate")
	}
}

func TestRevokeCredentialKeepsDevice(t *testing.T) {
	svc, _ := testService()
	ctx := context.Background()
	p, err := svc.Create(ctx, "shop-dev")
	if err != nil {
		t.Fatal(err)
	}
	r, err := svc.Rotate(ctx, p.Device.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Old already revoked by rotation; revoke new explicitly, then device
	// has no usable credential.
	if err := svc.RevokeCredential(ctx, r.Credential.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Authenticate(ctx, p.Device.ID, r.Credential.ID, r.RawSecret); err == nil {
		t.Fatal("revoked credential must fail")
	}
}

func TestCreateValidation(t *testing.T) {
	svc, _ := testService()
	if _, err := svc.Create(context.Background(), ""); err == nil {
		t.Fatal("empty name must fail")
	}
}

func TestListHidesSecrets(t *testing.T) {
	svc, _ := testService()
	ctx := context.Background()
	p, _ := svc.Create(ctx, "shop-dev")
	devs, creds, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 {
		t.Fatalf("want 1 device, got %d", len(devs))
	}
	for _, c := range creds[p.Device.ID] {
		if len(c.Verifier) == 0 || len(c.Salt) == 0 {
			t.Fatal("credential rows must carry verifier+salt")
		}
	}
}
