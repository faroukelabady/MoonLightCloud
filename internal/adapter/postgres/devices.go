package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/MoonLightSoftware/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/MoonLightSoftware/MoonLightCloud/internal/apperr"
	"github.com/MoonLightSoftware/MoonLightCloud/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Devices implements auth.Repository with sqlc-generated queries.
type Devices struct {
	pool    *pgxpool.Pool
	timeout time.Duration
}

// NewDevices wires the repository. timeout bounds each operation.
func NewDevices(pool *pgxpool.Pool, timeout time.Duration) Devices {
	return Devices{pool: pool, timeout: timeout}
}

func (d Devices) ctx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d.timeout)
}

// Create persists a device.
func (d Devices) Create(ctx context.Context, dev auth.Device) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(dev.ID)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "device id must be a UUID")
	}
	q := sqlcgen.New(d.pool)
	err = q.CreateDevice(ctx, sqlcgen.CreateDeviceParams{
		ID:         uid,
		Name:       dev.Name,
		Status:     dev.Status,
		SecretHash: dev.SecretHash,
		SecretSalt: dev.SecretSalt,
		CreatedAt:  pgTime(dev.CreatedAt),
		UpdatedAt:  pgTime(dev.UpdatedAt),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return apperr.New(apperr.Conflict, "device already exists")
		}
		return apperr.Wrap(apperr.Internal, "create device", redact(err))
	}
	return nil
}

// ByID loads a device or NotFound.
func (d Devices) ByID(ctx context.Context, id string) (auth.Device, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return auth.Device{}, apperr.New(apperr.NotFound, "device not found")
	}
	row, err := sqlcgen.New(d.pool).DeviceByID(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return auth.Device{}, apperr.New(apperr.NotFound, "device not found")
		}
		return auth.Device{}, apperr.Wrap(apperr.Internal, "load device", redact(err))
	}
	dev := auth.Device{
		ID:         uuidString(row.ID),
		Name:       row.Name,
		Status:     row.Status,
		SecretHash: row.SecretHash,
		SecretSalt: row.SecretSalt,
		CreatedAt:  row.CreatedAt.Time,
		UpdatedAt:  row.UpdatedAt.Time,
	}
	if row.LastSeenAt.Valid {
		t := row.LastSeenAt.Time
		dev.LastSeenAt = &t
	}
	return dev, nil
}

// TouchLastSeen refreshes last_seen_at.
func (d Devices) TouchLastSeen(ctx context.Context, id string, at time.Time) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "device id must be a UUID")
	}
	if err := sqlcgen.New(d.pool).TouchDeviceLastSeen(ctx, sqlcgen.TouchDeviceLastSeenParams{ID: uid, LastSeenAt: pgTime(at)}); err != nil {
		return apperr.Wrap(apperr.Internal, "touch device", redact(err))
	}
	return nil
}

// Revoke marks a device revoked.
func (d Devices) Revoke(ctx context.Context, id string, at time.Time) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "device id must be a UUID")
	}
	if err := sqlcgen.New(d.pool).RevokeDevice(ctx, sqlcgen.RevokeDeviceParams{ID: uid, RevokedAt: pgTime(at)}); err != nil {
		return apperr.Wrap(apperr.Internal, "revoke device", redact(err))
	}
	return nil
}
