package auth

import (
	"context"
	"strings"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
)

// Service implements device provisioning, authentication, and revocation.
type Service struct {
	repo   Repository
	hasher Hasher
	clock  clock.Clock
	ids    ids.Generator
}

// NewService wires the device service.
func NewService(r Repository, h Hasher, c clock.Clock, g ids.Generator) Service {
	return Service{repo: r, hasher: h, clock: c, ids: g}
}

// Create provisions a device. The raw secret is returned once; only the hash
// persists. Name is required, max 200 chars.
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
	d := Device{
		ID:         s.ids.New(),
		Name:       name,
		Status:     StatusActive,
		SecretHash: hash,
		SecretSalt: salt,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := s.repo.Create(ctx, d); err != nil {
		return Provisioned{}, err
	}
	return Provisioned{Device: d, RawSecret: raw}, nil
}

// Authenticate verifies a device_id + raw secret pair. Failures are all
// UNAUTHORIZED (unknown device, bad secret, revoked) to avoid enumeration.
// last_seen_at is refreshed on success; its failure does not fail auth.
func (s Service) Authenticate(ctx context.Context, id, rawSecret string) (Device, error) {
	fail := apperr.New(apperr.Unauthorized, "invalid device credential")
	if strings.TrimSpace(id) == "" || strings.TrimSpace(rawSecret) == "" {
		return Device{}, fail
	}
	d, err := s.repo.ByID(ctx, id)
	if err != nil {
		return Device{}, fail
	}
	if !d.Active() {
		return Device{}, fail
	}
	if !s.hasher.Verify(rawSecret, d.SecretHash, d.SecretSalt) {
		return Device{}, fail
	}
	now := s.clock.Now()
	d.LastSeenAt = &now
	_ = s.repo.TouchLastSeen(ctx, id, now)
	return d, nil
}

// Revoke disables a device; existing tokens stop working immediately.
func (s Service) Revoke(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return apperr.New(apperr.InvalidInput, "device id is required")
	}
	if _, err := s.repo.ByID(ctx, id); err != nil {
		return err
	}
	return s.repo.Revoke(ctx, id, s.clock.Now())
}

// Get returns a device or NotFound (admin/CLI use, not the auth path).
func (s Service) Get(ctx context.Context, id string) (Device, error) {
	if strings.TrimSpace(id) == "" {
		return Device{}, apperr.New(apperr.InvalidInput, "device id is required")
	}
	return s.repo.ByID(ctx, id)
}
