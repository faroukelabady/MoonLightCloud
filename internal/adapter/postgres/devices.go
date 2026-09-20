package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Devices implements auth.DeviceRepository plus the transactional Store
// methods (Provision, Rotate, RevokeDeviceAll) with pgx transactions.
type Devices struct {
	pool    *pgxpool.Pool
	timeout time.Duration
}

// NewDevices wires the store. timeout bounds each operation.
func NewDevices(pool *pgxpool.Pool, timeout time.Duration) Devices {
	return Devices{pool: pool, timeout: timeout}
}

func (d Devices) ctx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d.timeout)
}

// CreateDevice persists a device row (no secret material).
func (d Devices) CreateDevice(ctx context.Context, dev auth.Device) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(dev.ID)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "device id must be a UUID")
	}
	err = sqlcgen.New(d.pool).CreateDevice(ctx, sqlcgen.CreateDeviceParams{
		ID: uid, Name: dev.Name, Status: dev.Status,
		CreatedAt: pgTime(dev.CreatedAt), UpdatedAt: pgTime(dev.UpdatedAt),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return apperr.New(apperr.Conflict, "device already exists")
		}
		return apperr.Wrap(apperr.Internal, "create device", redact(err))
	}
	return nil
}

// DeviceByID loads a device or NotFound.
func (d Devices) DeviceByID(ctx context.Context, id string) (auth.Device, error) {
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
	return toDevice(row), nil
}

// ListDevices returns all devices ordered by creation.
func (d Devices) ListDevices(ctx context.Context) ([]auth.Device, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ListDevices(ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "list devices", redact(err))
	}
	out := make([]auth.Device, 0, len(rows))
	for _, r := range rows {
		out = append(out, toDevice(sqlcgen.Device{
			ID: r.ID, Name: r.Name, Status: r.Status,
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
			LastSeenAt: r.LastSeenAt, RevokedAt: r.RevokedAt,
		}))
	}
	return out, nil
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

// RevokeDevice marks a device revoked (credentials handled by RevokeDeviceAll).
func (d Devices) RevokeDevice(ctx context.Context, id string, at time.Time) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "device id must be a UUID")
	}
	if err := sqlcgen.New(d.pool).RevokeDeviceRow(ctx, sqlcgen.RevokeDeviceRowParams{ID: uid, RevokedAt: pgTime(at)}); err != nil {
		return apperr.Wrap(apperr.Internal, "revoke device", redact(err))
	}
	return nil
}

// Provision inserts device + first credential in one transaction.
func (d Devices) Provision(ctx context.Context, dev auth.Device, cred auth.Credential) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	return d.inTx(ctx, func(q *sqlcgen.Queries) error {
		uid, err := parseUUID(dev.ID)
		if err != nil {
			return apperr.New(apperr.InvalidInput, "device id must be a UUID")
		}
		if err := q.CreateDevice(ctx, sqlcgen.CreateDeviceParams{
			ID: uid, Name: dev.Name, Status: dev.Status,
			CreatedAt: pgTime(dev.CreatedAt), UpdatedAt: pgTime(dev.UpdatedAt),
		}); err != nil {
			if isUniqueViolation(err) {
				return apperr.New(apperr.Conflict, "device already exists")
			}
			return apperr.Wrap(apperr.Internal, "create device", redact(err))
		}
		return insertCredential(ctx, q, cred)
	})
}

// Rotate inserts the successor credential and revokes every other active
// credential of the device in one transaction. The device row is locked
// first so concurrent rotations serialize: exactly one credential stays
// active and every rotation call succeeds.
func (d Devices) Rotate(ctx context.Context, cred auth.Credential) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	return d.inTx(ctx, func(q *sqlcgen.Queries) error {
		duid, err := parseUUID(cred.DeviceID)
		if err != nil {
			return apperr.New(apperr.InvalidInput, "device id must be a UUID")
		}
		if _, err := q.LockDevice(ctx, duid); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apperr.New(apperr.NotFound, "device not found")
			}
			return apperr.Wrap(apperr.Internal, "lock device", redact(err))
		}
		if err := insertCredential(ctx, q, cred); err != nil {
			return err
		}
		cuid, err := parseUUID(cred.ID)
		if err != nil {
			return apperr.New(apperr.InvalidInput, "credential id must be a UUID")
		}
		if err := q.RevokeOtherCredentials(ctx, sqlcgen.RevokeOtherCredentialsParams{
			DeviceID: duid, ID: cuid, RevokedAt: pgTime(cred.ActivatedAt),
		}); err != nil {
			return apperr.Wrap(apperr.Internal, "revoke old credentials", redact(err))
		}
		return nil
	})
}

// RevokeDeviceAll revokes the device and all its credentials in one
// transaction: immediate effect, no partial state.
func (d Devices) RevokeDeviceAll(ctx context.Context, id string, at time.Time) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	return d.inTx(ctx, func(q *sqlcgen.Queries) error {
		uid, err := parseUUID(id)
		if err != nil {
			return apperr.New(apperr.InvalidInput, "device id must be a UUID")
		}
		if err := q.RevokeDeviceRow(ctx, sqlcgen.RevokeDeviceRowParams{ID: uid, RevokedAt: pgTime(at)}); err != nil {
			return apperr.Wrap(apperr.Internal, "revoke device", redact(err))
		}
		if err := q.RevokeDeviceCredentials(ctx, sqlcgen.RevokeDeviceCredentialsParams{
			DeviceID: uid, RevokedAt: pgTime(at),
		}); err != nil {
			return apperr.Wrap(apperr.Internal, "revoke credentials", redact(err))
		}
		return nil
	})
}

// inTx runs fn inside a pgx transaction, rolling back on error.
func (d Devices) inTx(ctx context.Context, fn func(q *sqlcgen.Queries) error) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return apperr.Wrap(apperr.Internal, "begin transaction", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(sqlcgen.New(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return apperr.Wrap(apperr.Internal, "commit transaction", redact(err))
	}
	return nil
}

func toDevice(row sqlcgen.Device) auth.Device {
	dev := auth.Device{
		ID: uuidString(row.ID), Name: row.Name, Status: row.Status,
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
	if row.LastSeenAt.Valid {
		t := row.LastSeenAt.Time
		dev.LastSeenAt = &t
	}
	if row.RevokedAt.Valid {
		t := row.RevokedAt.Time
		dev.RevokedAt = &t
	}
	return dev
}
