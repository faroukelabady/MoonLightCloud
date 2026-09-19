// Package auth owns device identity, credentials, and authentication.
// It defines repository contracts; the postgres adapter implements them.
// This package never imports pgx or net/http.
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

// Device is a MoonLightRetail installation identity.
type Device struct {
	ID         string
	Name       string
	Status     string
	SecretHash []byte
	SecretSalt []byte
	CreatedAt  time.Time
	UpdatedAt  time.Time
	LastSeenAt *time.Time
}

// Active reports whether the device may authenticate.
func (d Device) Active() bool { return d.Status == StatusActive }

// Repository is the persistence contract implemented by the postgres adapter.
type Repository interface {
	Create(ctx context.Context, d Device) error
	ByID(ctx context.Context, id string) (Device, error)
	TouchLastSeen(ctx context.Context, id string, at time.Time) error
	Revoke(ctx context.Context, id string, at time.Time) error
}

// Provisioned is the result of creating a device: the raw secret exists
// only here and is shown once to the operator.
type Provisioned struct {
	Device    Device
	RawSecret string
}
