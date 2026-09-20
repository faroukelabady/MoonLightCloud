package auth

import (
	"context"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
)

// Service implements provisioning, authentication, rotation, revocation.
type Service struct {
	store     Store
	hasher    Hasher
	pepperRaw string
	pepperVer int
	clock     clock.Clock
	ids       ids.Generator
}

// NewService wires the device service. pepperRaw is the configured pepper
// in raw string form, needed only to verify legacy v0 migrated rows.
func NewService(s Store, h Hasher, pepperRaw string, pepperVer int, c clock.Clock, g ids.Generator) Service {
	return Service{store: s, hasher: h, pepperRaw: pepperRaw, pepperVer: pepperVer, clock: c, ids: g}
}

// Create provisions a device with its first credential. The raw secret is
// returned once; only salt + verifier persist. Name is 1..200 chars.
func (s Service) Create(ctx context.Context, name string) (Provisioned, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 {
		return Provisioned{}, apperr.New(apperr.InvalidInput, "device name must be 1..200 chars")
	}
	raw, err := GenerateSecret()
	if err != nil {
		return Provisioned{}, apperr.Wrap(apperr.Internal, "cannot provision device", err)
	}
	hash, salt, err := s.hasher.Hash(raw)
	if err != nil {
		return Provisioned{}, apperr.Wrap(apperr.Internal, "cannot provision device", err)
	}
	now := s.clock.Now()
	d := Device{ID: s.ids.New(), Name: name, Status: StatusActive, CreatedAt: now, UpdatedAt: now}
	c := Credential{
		ID: s.ids.New(), DeviceID: d.ID,
		Salt: salt, Verifier: hash,
		VerifierVersion: VerifierV1, PepperVersion: s.pepperVer,
		Status: CredentialActive, CreatedAt: now, ActivatedAt: now,
	}
	if err := s.store.Provision(ctx, d, c); err != nil {
		return Provisioned{}, err
	}
	return Provisioned{Device: d, Credential: c, RawSecret: raw}, nil
}

// Authenticate verifies a device_id + credential_id + raw secret triple.
// Every failure — unknown device, unknown credential, revoked, wrong
// secret, rotated — is the same UNAUTHORIZED: no enumeration. Timestamps
// refresh on success; their failure does not fail auth.
func (s Service) Authenticate(ctx context.Context, deviceID, credID, rawSecret string) (Device, Credential, error) {
	fail := apperr.New(apperr.Unauthorized, "invalid device credential")
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(credID) == "" || strings.TrimSpace(rawSecret) == "" {
		return Device{}, Credential{}, fail
	}
	cred, err := s.store.CredentialByID(ctx, credID)
	if err != nil {
		return Device{}, Credential{}, fail
	}
	if cred.DeviceID != deviceID || !cred.Active() {
		return Device{}, Credential{}, fail
	}
	dev, err := s.store.DeviceByID(ctx, deviceID)
	if err != nil || !dev.Active() {
		return Device{}, Credential{}, fail
	}
	if !s.verifySecret(rawSecret, cred) {
		return Device{}, Credential{}, fail
	}
	now := s.clock.Now()
	dev.LastSeenAt = &now
	cred.LastUsedAt = &now
	_ = s.store.TouchLastSeen(ctx, deviceID, now)
	_ = s.store.TouchCredentialLastUsed(ctx, credID, now)
	return dev, cred, nil
}

func (s Service) verifySecret(raw string, cred Credential) bool {
	if cred.VerifierVersion == VerifierV0 {
		return VerifyLegacyV0(raw, cred.Verifier, cred.Salt, s.pepperRaw)
	}
	return s.hasher.Verify(raw, cred.Verifier, cred.Salt)
}

// Rotate creates a successor credential and revokes all other active ones
// in one transaction. Old secret stops working; if the op fails, the old
// credential remains usable (never zero usable credentials from a partial
// op). The replacement secret is returned once.
func (s Service) Rotate(ctx context.Context, deviceID string) (Provisioned, error) {
	if strings.TrimSpace(deviceID) == "" {
		return Provisioned{}, apperr.New(apperr.InvalidInput, "device id is required")
	}
	dev, err := s.store.DeviceByID(ctx, deviceID)
	if err != nil {
		return Provisioned{}, err
	}
	if !dev.Active() {
		return Provisioned{}, apperr.New(apperr.Conflict, "cannot rotate credential of a revoked device")
	}
	raw, err := GenerateSecret()
	if err != nil {
		return Provisioned{}, apperr.Wrap(apperr.Internal, "cannot rotate credential", err)
	}
	hash, salt, err := s.hasher.Hash(raw)
	if err != nil {
		return Provisioned{}, apperr.Wrap(apperr.Internal, "cannot rotate credential", err)
	}
	now := s.clock.Now()
	c := Credential{
		ID: s.ids.New(), DeviceID: deviceID,
		Salt: salt, Verifier: hash,
		VerifierVersion: VerifierV1, PepperVersion: s.pepperVer,
		Status: CredentialActive, CreatedAt: now, ActivatedAt: now,
	}
	if err := s.store.Rotate(ctx, c); err != nil {
		return Provisioned{}, err
	}
	return Provisioned{Device: dev, Credential: c, RawSecret: raw}, nil
}

// RevokeDevice revokes the device and every credential atomically.
func (s Service) RevokeDevice(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return apperr.New(apperr.InvalidInput, "device id is required")
	}
	if _, err := s.store.DeviceByID(ctx, id); err != nil {
		return err
	}
	return s.store.RevokeDeviceAll(ctx, id, s.clock.Now())
}

// RevokeCredential revokes one credential; the device keeps working if it
// still owns another active credential.
func (s Service) RevokeCredential(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return apperr.New(apperr.InvalidInput, "credential id is required")
	}
	if _, err := s.store.CredentialByID(ctx, id); err != nil {
		return err
	}
	return s.store.RevokeCredential(ctx, id, s.clock.Now())
}

// Get returns a device or NotFound (admin/CLI use, not the auth path).
func (s Service) Get(ctx context.Context, id string) (Device, error) {
	if strings.TrimSpace(id) == "" {
		return Device{}, apperr.New(apperr.InvalidInput, "device id is required")
	}
	return s.store.DeviceByID(ctx, id)
}

// List returns devices with their credentials for operator visibility.
func (s Service) List(ctx context.Context) ([]Device, map[string][]Credential, error) {
	devs, err := s.store.ListDevices(ctx)
	if err != nil {
		return nil, nil, err
	}
	creds := make(map[string][]Credential, len(devs))
	for _, d := range devs {
		cs, err := s.store.CredentialsForDevice(ctx, d.ID)
		if err != nil {
			return nil, nil, err
		}
		creds[d.ID] = cs
	}
	return devs, creds, nil
}
