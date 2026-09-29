package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/operations"
	"github.com/jackc/pgx/v5"
)

func opsLimit(limit int) int32 {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	return int32(limit)
}

// ScanOffline scans active devices unseen since the threshold (server time).
func (d Devices) ScanOffline(ctx context.Context, olderThan time.Time, limit int) ([]operations.OfflineCandidate, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ScanOfflineDevices(ctx, sqlcgen.ScanOfflineDevicesParams{
		LastSeenAt: pgTime(olderThan.UTC()), Limit: opsLimit(limit),
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "scan offline", redact(err))
	}
	out := make([]operations.OfflineCandidate, 0, len(rows))
	for _, r := range rows {
		out = append(out, operations.OfflineCandidate{
			DeviceID: uuidString(r.ID), Name: r.Name, LastSeen: r.LastSeenAt.Time.UTC(),
		})
	}
	return out, nil
}

// ScanReconnected scans active devices seen since the threshold.
func (d Devices) ScanReconnected(ctx context.Context, newerThan time.Time, limit int) ([]operations.OfflineCandidate, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ScanReconnectedDevices(ctx, sqlcgen.ScanReconnectedDevicesParams{
		LastSeenAt: pgTime(newerThan.UTC()), Limit: opsLimit(limit),
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "scan reconnected", redact(err))
	}
	out := make([]operations.OfflineCandidate, 0, len(rows))
	for _, r := range rows {
		out = append(out, operations.OfflineCandidate{
			DeviceID: uuidString(r.ID), Name: r.Name, LastSeen: r.LastSeenAt.Time.UTC(),
		})
	}
	return out, nil
}

// ScanRevoked lists non-active device IDs (bounded).
func (d Devices) ScanRevoked(ctx context.Context, limit int) ([]string, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ScanRevokedDevices(ctx, opsLimit(limit))
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "scan revoked", redact(err))
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, uuidString(r.ID))
	}
	return out, nil
}

// ScanFailedCommands lists failed sync commands (bounded, oldest first).
func (d Devices) ScanFailedCommands(ctx context.Context, limit int) ([]operations.FailedCommand, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ScanFailedCommands(ctx, opsLimit(limit))
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "scan failed commands", redact(err))
	}
	out := make([]operations.FailedCommand, 0, len(rows))
	for _, r := range rows {
		fc := operations.FailedCommand{CommandID: uuidString(r.ID), DeviceID: uuidString(r.DeviceID)}
		if r.ResultCode.Valid {
			s := r.ResultCode.String
			fc.ResultCode = &s
		}
		if r.FinishedAt.Valid {
			t := r.FinishedAt.Time.UTC()
			fc.FinishedAt = &t
		}
		out = append(out, fc)
	}
	return out, nil
}

// ScanStaleCommands lists non-terminal commands older than the threshold.
func (d Devices) ScanStaleCommands(ctx context.Context, olderThan time.Time, limit int) ([]operations.StaleCommand, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ScanStaleCommands(ctx, sqlcgen.ScanStaleCommandsParams{
		RequestedAt: pgTime(olderThan.UTC()), Limit: opsLimit(limit),
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "scan stale commands", redact(err))
	}
	out := make([]operations.StaleCommand, 0, len(rows))
	for _, r := range rows {
		out = append(out, operations.StaleCommand{
			CommandID: uuidString(r.ID), DeviceID: uuidString(r.DeviceID),
			Status: r.Status, RequestedAt: r.RequestedAt.Time.UTC(),
		})
	}
	return out, nil
}

// ScanBlockedRuns lists terminally blocked report runs.
func (d Devices) ScanBlockedRuns(ctx context.Context, limit int) ([]operations.BlockedRun, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ScanBlockedRuns(ctx, opsLimit(limit))
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "scan blocked runs", redact(err))
	}
	out := make([]operations.BlockedRun, 0, len(rows))
	for _, r := range rows {
		br := operations.BlockedRun{
			RunID: uuidString(r.ID), Schedule: uuidString(r.ScheduleID),
			Kind: string(r.RunKind), CreatedAt: r.CreatedAt.Time.UTC(),
		}
		if r.SlotLocalDate.Valid {
			s := r.SlotLocalDate.Time.Format("2006-01-02")
			br.Slot = &s
		}
		out = append(out, br)
	}
	return out, nil
}

// ScanStaleRuns lists pending/retry report runs older than the threshold.
func (d Devices) ScanStaleRuns(ctx context.Context, olderThan time.Time, limit int) ([]operations.StaleRun, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ScanStaleRuns(ctx, sqlcgen.ScanStaleRunsParams{
		CreatedAt: pgTime(olderThan.UTC()), Limit: opsLimit(limit),
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "scan stale runs", redact(err))
	}
	out := make([]operations.StaleRun, 0, len(rows))
	for _, r := range rows {
		out = append(out, operations.StaleRun{
			RunID: uuidString(r.ID), Status: r.Status, CreatedAt: r.CreatedAt.Time.UTC(),
		})
	}
	return out, nil
}

func toOpsBadNotification(r sqlcgen.ScanBlockedNotificationsRow) operations.BadNotification {
	return operations.BadNotification{
		NotificationID: uuidString(r.ID), TemplateKey: r.TemplateKey, CreatedAt: r.CreatedAt.Time.UTC(),
	}
}

// ScanBlockedNotifications lists non-7D blocked notifications.
func (d Devices) ScanBlockedNotifications(ctx context.Context, limit int) ([]operations.BadNotification, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ScanBlockedNotifications(ctx, opsLimit(limit))
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "scan blocked notifications", redact(err))
	}
	out := make([]operations.BadNotification, 0, len(rows))
	for _, r := range rows {
		out = append(out, toOpsBadNotification(r))
	}
	return out, nil
}

// ScanAmbiguousNotifications lists non-7D ambiguous notifications.
func (d Devices) ScanAmbiguousNotifications(ctx context.Context, limit int) ([]operations.BadNotification, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ScanAmbiguousNotifications(ctx, opsLimit(limit))
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "scan ambiguous notifications", redact(err))
	}
	out := make([]operations.BadNotification, 0, len(rows))
	for _, r := range rows {
		out = append(out, operations.BadNotification{
			NotificationID: uuidString(r.ID), TemplateKey: r.TemplateKey, CreatedAt: r.CreatedAt.Time.UTC(),
		})
	}
	return out, nil
}

// ScanRetryStaleNotifications lists non-7D retry notifications past the age.
func (d Devices) ScanRetryStaleNotifications(ctx context.Context, olderThan time.Time, limit int) ([]operations.BadNotification, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ScanRetryStaleNotifications(ctx, sqlcgen.ScanRetryStaleNotificationsParams{
		CreatedAt: pgTime(olderThan.UTC()), Limit: opsLimit(limit),
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "scan retry notifications", redact(err))
	}
	out := make([]operations.BadNotification, 0, len(rows))
	for _, r := range rows {
		out = append(out, operations.BadNotification{
			NotificationID: uuidString(r.ID), TemplateKey: r.TemplateKey, CreatedAt: r.CreatedAt.Time.UTC(),
		})
	}
	return out, nil
}

// CommandTerminal reads one command's status for stale resolution.
func (d Devices) CommandTerminal(ctx context.Context, commandID string) (string, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(commandID)
	if err != nil {
		return "", false, apperr.New(apperr.InvalidInput, "command id must be a UUID")
	}
	status, err := sqlcgen.New(d.pool).GetCommandTerminal(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, apperr.Wrap(apperr.Internal, "command status", redact(err))
	}
	return status, true, nil
}

// RunStatus reads one report run's status for stale resolution.
func (d Devices) RunStatus(ctx context.Context, runID string) (string, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(runID)
	if err != nil {
		return "", false, apperr.New(apperr.InvalidInput, "run id must be a UUID")
	}
	status, err := sqlcgen.New(d.pool).GetRunTerminal(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, apperr.Wrap(apperr.Internal, "run status", redact(err))
	}
	return string(status), true, nil
}

// NotificationDispatch reads one notification's dispatch status.
func (d Devices) NotificationDispatch(ctx context.Context, notificationID string) (string, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(notificationID)
	if err != nil {
		return "", false, apperr.New(apperr.InvalidInput, "notification id must be a UUID")
	}
	status, err := sqlcgen.New(d.pool).GetNotificationDispatch(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, apperr.Wrap(apperr.Internal, "notification status", redact(err))
	}
	return status, true, nil
}

// DeviceStatus reads one device's lifecycle status.
func (d Devices) DeviceStatus(ctx context.Context, deviceID string) (string, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(deviceID)
	if err != nil {
		return "", false, apperr.New(apperr.InvalidInput, "device id must be a UUID")
	}
	status, err := sqlcgen.New(d.pool).CheckDeviceActive(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, apperr.Wrap(apperr.Internal, "device status", redact(err))
	}
	return status, true, nil
}

// Presence reads one device's last_seen_at.
func (d Devices) Presence(ctx context.Context, deviceID string) (*time.Time, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(deviceID)
	if err != nil {
		return nil, false, apperr.New(apperr.InvalidInput, "device id must be a UUID")
	}
	ts, err := sqlcgen.New(d.pool).OpsPresence(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, apperr.Wrap(apperr.Internal, "presence", redact(err))
	}
	if !ts.Valid {
		return nil, true, nil
	}
	t := ts.Time.UTC()
	return &t, true, nil
}
