package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/devicecontrol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// TouchSeen refreshes last_seen monotonically (server time authority).
// Poll contact additionally refreshes last_poll_at monotonically;
// non-poll contact preserves it (NULL until the first poll).
func (d Devices) TouchSeen(ctx context.Context, deviceID string, at time.Time, isPoll bool) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(deviceID)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "device id must be a UUID")
	}
	at = at.UTC()
	poll := pgtype.Timestamptz{}
	if isPoll {
		poll = pgTime(at)
	}
	if err := sqlcgen.New(d.pool).UpsertPresenceSeen(ctx, sqlcgen.UpsertPresenceSeenParams{
		DeviceID: uid, LastSeenAt: pgTime(at), LastPollAt: poll, CreatedAt: pgTime(at),
	}); err != nil {
		return apperr.Wrap(apperr.Internal, "touch presence", redact(err))
	}
	return nil
}

// TouchAccepted records accepted contact, creating the presence row when
// this is the first contact (last_poll_at stays NULL until a real poll).
func (d Devices) TouchAccepted(ctx context.Context, deviceID string, at time.Time) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(deviceID)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "device id must be a UUID")
	}
	if err := sqlcgen.New(d.pool).UpsertPresenceAccepted(ctx, sqlcgen.UpsertPresenceAcceptedParams{
		DeviceID: uid, LastSeenAt: pgTime(at.UTC()),
	}); err != nil {
		return apperr.Wrap(apperr.Internal, "touch presence", redact(err))
	}
	return nil
}

// TouchFinished records terminal contact, creating the presence row when
// this is the first contact (last_poll_at stays NULL until a real poll).
func (d Devices) TouchFinished(ctx context.Context, deviceID string, at time.Time) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(deviceID)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "device id must be a UUID")
	}
	if err := sqlcgen.New(d.pool).UpsertPresenceFinished(ctx, sqlcgen.UpsertPresenceFinishedParams{
		DeviceID: uid, LastSeenAt: pgTime(at.UTC()),
	}); err != nil {
		return apperr.Wrap(apperr.Internal, "touch presence", redact(err))
	}
	return nil
}

// GetPresence loads one presence row.
func (d Devices) GetPresence(ctx context.Context, deviceID string) (devicecontrol.Presence, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(deviceID)
	if err != nil {
		return devicecontrol.Presence{}, false, apperr.New(apperr.InvalidInput, "device id must be a UUID")
	}
	row, err := sqlcgen.New(d.pool).GetPresence(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return devicecontrol.Presence{}, false, nil
		}
		return devicecontrol.Presence{}, false, apperr.Wrap(apperr.Internal, "load presence", redact(err))
	}
	return toPresence(row), true, nil
}

// ListPresence returns all presence rows.
func (d Devices) ListPresence(ctx context.Context) ([]devicecontrol.Presence, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ListPresence(ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "list presence", redact(err))
	}
	out := make([]devicecontrol.Presence, 0, len(rows))
	for _, r := range rows {
		out = append(out, toPresence(sqlcgen.DeviceControlPresence(r)))
	}
	return out, nil
}

// CreateCommand inserts a pending command; unique violations map to Conflict.
func (d Devices) CreateCommand(ctx context.Context, id, deviceID, idempotencyKey string, at time.Time) (devicecontrol.Command, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return devicecontrol.Command{}, apperr.New(apperr.InvalidInput, "command id must be a UUID")
	}
	did, err := parseUUID(deviceID)
	if err != nil {
		return devicecontrol.Command{}, apperr.New(apperr.InvalidInput, "device id must be a UUID")
	}
	row, err := sqlcgen.New(d.pool).CreateControlCommand(ctx, sqlcgen.CreateControlCommandParams{
		ID: uid, DeviceID: did, IdempotencyKey: idempotencyKey, RequestedAt: pgTime(at.UTC()),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return devicecontrol.Command{}, apperr.New(apperr.Conflict, "command already exists")
		}
		return devicecontrol.Command{}, apperr.Wrap(apperr.Internal, "create command", redact(err))
	}
	return toCommand(row), nil
}

func (d Devices) commandByRow(ctx context.Context, fn func(*sqlcgen.Queries) (sqlcgen.DeviceControlCommand, error)) (devicecontrol.Command, bool, error) {
	row, err := fn(sqlcgen.New(d.pool))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return devicecontrol.Command{}, false, nil
		}
		return devicecontrol.Command{}, false, apperr.Wrap(apperr.Internal, "load command", redact(err))
	}
	return toCommand(row), true, nil
}

// GetCommand loads by ID across devices (internal use).
func (d Devices) GetCommand(ctx context.Context, id string) (devicecontrol.Command, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return devicecontrol.Command{}, false, apperr.New(apperr.NotFound, "command not found")
	}
	return d.commandByRow(ctx, func(q *sqlcgen.Queries) (sqlcgen.DeviceControlCommand, error) {
		return q.GetControlCommand(ctx, uid)
	})
}

// GetCommandForDevice enforces device binding (404 on cross-device).
func (d Devices) GetCommandForDevice(ctx context.Context, id, deviceID string) (devicecontrol.Command, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return devicecontrol.Command{}, false, apperr.New(apperr.NotFound, "command not found")
	}
	did, err := parseUUID(deviceID)
	if err != nil {
		return devicecontrol.Command{}, false, apperr.New(apperr.NotFound, "command not found")
	}
	return d.commandByRow(ctx, func(q *sqlcgen.Queries) (sqlcgen.DeviceControlCommand, error) {
		return q.GetControlCommandForDevice(ctx, sqlcgen.GetControlCommandForDeviceParams{ID: uid, DeviceID: did})
	})
}

// GetByIdempotency loads by (device, key).
func (d Devices) GetByIdempotency(ctx context.Context, deviceID, key string) (devicecontrol.Command, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	did, err := parseUUID(deviceID)
	if err != nil {
		return devicecontrol.Command{}, false, apperr.New(apperr.InvalidInput, "device id must be a UUID")
	}
	return d.commandByRow(ctx, func(q *sqlcgen.Queries) (sqlcgen.DeviceControlCommand, error) {
		return q.GetCommandByIdempotency(ctx, sqlcgen.GetCommandByIdempotencyParams{DeviceID: did, IdempotencyKey: key})
	})
}

// GetActive returns the single non-terminal command, if any.
func (d Devices) GetActive(ctx context.Context, deviceID string) (devicecontrol.Command, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	did, err := parseUUID(deviceID)
	if err != nil {
		return devicecontrol.Command{}, false, apperr.New(apperr.InvalidInput, "device id must be a UUID")
	}
	return d.commandByRow(ctx, func(q *sqlcgen.Queries) (sqlcgen.DeviceControlCommand, error) {
		return q.GetActiveControlCommand(ctx, did)
	})
}

// PollClaim performs SKIP LOCKED claim + lease in one short transaction.
func (d Devices) PollClaim(ctx context.Context, deviceID string, now time.Time, lease time.Duration) (devicecontrol.Command, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	did, err := parseUUID(deviceID)
	if err != nil {
		return devicecontrol.Command{}, false, apperr.New(apperr.InvalidInput, "device id must be a UUID")
	}
	now = now.UTC()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return devicecontrol.Command{}, false, apperr.Wrap(apperr.Internal, "claim command", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	candidate, err := q.ClaimControlCommand(ctx, sqlcgen.ClaimControlCommandParams{DeviceID: did, LeaseUntil: pgTime(now)})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return devicecontrol.Command{}, false, nil
		}
		return devicecontrol.Command{}, false, apperr.Wrap(apperr.Internal, "claim command", redact(err))
	}
	leased, err := q.LeaseControlCommand(ctx, sqlcgen.LeaseControlCommandParams{
		ID: candidate.ID, DeviceID: did, LeasedAt: pgTime(now), LeaseUntil: pgTime(now.Add(lease)),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return devicecontrol.Command{}, false, nil
		}
		return devicecontrol.Command{}, false, apperr.Wrap(apperr.Internal, "lease command", redact(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return devicecontrol.Command{}, false, apperr.Wrap(apperr.Internal, "claim command", redact(err))
	}
	return toCommand(leased), true, nil
}

// Accept transitions pending/leased → accepted (idempotent).
func (d Devices) Accept(ctx context.Context, id, deviceID string, at time.Time) (devicecontrol.Command, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return devicecontrol.Command{}, false, apperr.New(apperr.NotFound, "command not found")
	}
	did, err := parseUUID(deviceID)
	if err != nil {
		return devicecontrol.Command{}, false, apperr.New(apperr.NotFound, "command not found")
	}
	row, err := sqlcgen.New(d.pool).AcceptControlCommand(ctx, sqlcgen.AcceptControlCommandParams{
		ID: uid, DeviceID: did, UpdatedAt: pgTime(at.UTC()),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return devicecontrol.Command{}, false, nil
		}
		return devicecontrol.Command{}, false, apperr.Wrap(apperr.Internal, "accept command", redact(err))
	}
	return toCommand(row), true, nil
}

// MarkRunning transitions to running (idempotent).
func (d Devices) MarkRunning(ctx context.Context, id, deviceID string, at time.Time) (devicecontrol.Command, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return devicecontrol.Command{}, false, apperr.New(apperr.NotFound, "command not found")
	}
	did, err := parseUUID(deviceID)
	if err != nil {
		return devicecontrol.Command{}, false, apperr.New(apperr.NotFound, "command not found")
	}
	row, err := sqlcgen.New(d.pool).MarkControlRunning(ctx, sqlcgen.MarkControlRunningParams{
		ID: uid, DeviceID: did, UpdatedAt: pgTime(at.UTC()),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return devicecontrol.Command{}, false, nil
		}
		return devicecontrol.Command{}, false, apperr.Wrap(apperr.Internal, "mark running", redact(err))
	}
	return toCommand(row), true, nil
}

// Finish commits terminal states; only non-terminal rows match.
func (d Devices) Finish(ctx context.Context, id, deviceID, status, resultCode string, at time.Time) (devicecontrol.Command, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return devicecontrol.Command{}, false, apperr.New(apperr.NotFound, "command not found")
	}
	did, err := parseUUID(deviceID)
	if err != nil {
		return devicecontrol.Command{}, false, apperr.New(apperr.NotFound, "command not found")
	}
	row, err := sqlcgen.New(d.pool).FinishControlCommand(ctx, sqlcgen.FinishControlCommandParams{
		ID: uid, DeviceID: did, Status: status, FinishedAt: pgTime(at.UTC()), ResultCode: pgText(resultCode),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return devicecontrol.Command{}, false, nil
		}
		return devicecontrol.Command{}, false, apperr.Wrap(apperr.Internal, "finish command", redact(err))
	}
	return toCommand(row), true, nil
}

// Recent returns bounded history.
func (d Devices) Recent(ctx context.Context, deviceID string, limit int) ([]devicecontrol.Command, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	did, err := parseUUID(deviceID)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "device id must be a UUID")
	}
	rows, err := sqlcgen.New(d.pool).ListRecentControlCommands(ctx, sqlcgen.ListRecentControlCommandsParams{
		DeviceID: did, Limit: int32(limit),
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "list commands", redact(err))
	}
	out := make([]devicecontrol.Command, 0, len(rows))
	for _, r := range rows {
		out = append(out, toCommand(r))
	}
	return out, nil
}

// ListActiveAll returns every non-terminal command in one batched read for
// dashboard overviews. No per-device round trips.
func (d Devices) ListActiveAll(ctx context.Context) ([]devicecontrol.Command, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ListActiveControlCommands(ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "list active commands", redact(err))
	}
	out := make([]devicecontrol.Command, 0, len(rows))
	for _, r := range rows {
		out = append(out, toCommand(r))
	}
	return out, nil
}

// ListRecentBounded returns at most perDevice newest commands per device in
// one LATERAL-join read, ordered by (device, requested_at DESC, id DESC).
func (d Devices) ListRecentBounded(ctx context.Context, deviceIDs []string, perDevice int) ([]devicecontrol.Command, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	if len(deviceIDs) == 0 {
		return nil, nil
	}
	if perDevice <= 0 || perDevice > 20 {
		perDevice = 5
	}
	ids := make([]pgtype.UUID, 0, len(deviceIDs))
	for _, id := range deviceIDs {
		uid, err := parseUUID(id)
		if err != nil {
			return nil, apperr.New(apperr.InvalidInput, "device id must be a UUID")
		}
		ids = append(ids, uid)
	}
	rows, err := sqlcgen.New(d.pool).ListRecentBounded(ctx, sqlcgen.ListRecentBoundedParams{
		Column1: ids, Limit: int32(perDevice),
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "list recent commands", redact(err))
	}
	out := make([]devicecontrol.Command, 0, len(rows))
	for _, r := range rows {
		out = append(out, toCommand(r))
	}
	return out, nil
}
