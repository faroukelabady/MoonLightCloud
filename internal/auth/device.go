// Package auth owns device identity, credentials, and authentication.
// It defines repository contracts; the postgres adapter implements them.
// This package never imports pgx or net/http.
//
// Model: a device has a lifecycle (active/revoked) and owns one or more
// credential rows. Exactly one credential is normally active; rotation
// inserts a successor and revokes predecessors in one transaction.
package auth

import (
	"context"
	"time"
)

// Device statuses.
const (
	StatusActive  = "active"
	StatusRevoked = "revoked"
)

// Credential statuses.
const (
	CredentialActive  = "active"
	CredentialRevoked = "revoked"
)

// Verifier versions.
const (
	// VerifierV0 is the legacy Phase-1A construction, carried only by rows
	// migrated with 00002. Verified by VerifyLegacyV0. Never created anew.
	VerifierV0 = 0
	// VerifierV1 is HMAC-SHA256(pepper, salt || hex_secret). All new rows.
	VerifierV1 = 1
)

// Device is a MoonLightRetail installation identity. No secret material.
type Device struct {
	ID         string
	Name       string
	Status     string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	LastSeenAt *time.Time
	RevokedAt  *time.Time
}

// Active reports whether the device may authenticate.
func (d Device) Active() bool { return d.Status == StatusActive }

// Credential is one device secret verifier row. Salt is not secret;
// verifier needs the server pepper to test candidates.
type Credential struct {
	ID              string
	DeviceID        string
	Salt            []byte
	Verifier        []byte
	VerifierVersion int
	PepperVersion   int
	Status          string
	CreatedAt       time.Time
	ActivatedAt     time.Time
	RevokedAt       *time.Time
	LastUsedAt      *time.Time
}

// Active reports whether the credential may authenticate.
func (c Credential) Active() bool { return c.Status == CredentialActive }

// DeviceRepository persists devices.
type DeviceRepository interface {
	CreateDevice(ctx context.Context, d Device) error
	DeviceByID(ctx context.Context, id string) (Device, error)
	ListDevices(ctx context.Context) ([]Device, error)
	TouchLastSeen(ctx context.Context, id string, at time.Time) error
	RevokeDevice(ctx context.Context, id string, at time.Time) error
}

// CredentialRepository persists credentials.
type CredentialRepository interface {
	CreateCredential(ctx context.Context, c Credential) error
	CredentialByID(ctx context.Context, id string) (Credential, error)
	ActiveCredentials(ctx context.Context, deviceID string) ([]Credential, error)
	CredentialsForDevice(ctx context.Context, deviceID string) ([]Credential, error)
	TouchCredentialLastUsed(ctx context.Context, id string, at time.Time) error
	RevokeCredential(ctx context.Context, id string, at time.Time) error
}

// Store is the transactional boundary. Provision and Rotate are atomic:
// a device is never left with zero usable credentials by a partial op.
type Store interface {
	DeviceRepository
	CredentialRepository
	// Provision inserts a device with its first credential atomically.
	Provision(ctx context.Context, d Device, c Credential) error
	// Rotate inserts the new active credential and revokes every other
	// active credential of the device atomically.
	Rotate(ctx context.Context, c Credential) error
	// RevokeDeviceAll revokes the device and all its credentials atomically.
	RevokeDeviceAll(ctx context.Context, id string, at time.Time) error
}

// Provisioned is the result of provisioning or rotation: the raw secret
// exists only here and is shown once to the operator.
type Provisioned struct {
	Device     Device
	Credential Credential
	RawSecret  string
}
