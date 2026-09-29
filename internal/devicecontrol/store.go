package devicecontrol

import (
	"context"
	"time"
)

// Store is the persistence contract; postgres implements it.
type Store interface {
	TouchSeen(ctx context.Context, deviceID string, at time.Time, isPoll bool) error
	TouchAccepted(ctx context.Context, deviceID string, at time.Time) error
	TouchFinished(ctx context.Context, deviceID string, at time.Time) error
	GetPresence(ctx context.Context, deviceID string) (Presence, bool, error)
	ListPresence(ctx context.Context) ([]Presence, error)
	CreateCommand(ctx context.Context, id, deviceID, idempotencyKey string, at time.Time) (Command, error)
	GetCommand(ctx context.Context, id string) (Command, bool, error)
	GetCommandForDevice(ctx context.Context, id, deviceID string) (Command, bool, error)
	GetByIdempotency(ctx context.Context, deviceID, key string) (Command, bool, error)
	GetActive(ctx context.Context, deviceID string) (Command, bool, error)
	PollClaim(ctx context.Context, deviceID string, now time.Time, lease time.Duration) (Command, bool, error)
	Accept(ctx context.Context, id, deviceID string, at time.Time) (Command, bool, error)
	MarkRunning(ctx context.Context, id, deviceID string, at time.Time) (Command, bool, error)
	Finish(ctx context.Context, id, deviceID, status, resultCode string, at time.Time) (Command, bool, error)
	Recent(ctx context.Context, deviceID string, limit int) ([]Command, error)
}

// DeviceStatus abstracts the existing lifecycle check (active vs revoked).
// Implemented by the auth service adapter in app wiring.
type DeviceStatus interface {
	IsActive(ctx context.Context, deviceID string) (bool, error)
}
