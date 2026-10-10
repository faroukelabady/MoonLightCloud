package postgres

import (
	"context"
	"encoding/hex"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/syncrecovery"
)

// Sync recovery persistence on Devices (shared pool + timeouts). All
// operations are bounded and read/write only the recovery-state table
// and sync_events identity — never business projections.

// UpsertRecoveryState durably marks one device as needing
// reconciliation after a Cloud restore.
func (d Devices) UpsertRecoveryState(ctx context.Context, deviceID, reason string) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	duid, err := parseUUID(deviceID)
	if err != nil {
		return err
	}
	return catalogAdminErr(sqlcgen.New(d.pool).UpsertSyncRecoveryState(ctx, sqlcgen.UpsertSyncRecoveryStateParams{
		DeviceID: duid, Reason: reason,
	}))
}

// ClearRecoveryState marks one device reconciled, recording how many
// retained events were verified.
func (d Devices) ClearRecoveryState(ctx context.Context, deviceID string, verifiedEvents int64) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	duid, err := parseUUID(deviceID)
	if err != nil {
		return err
	}
	return catalogAdminErr(sqlcgen.New(d.pool).ClearSyncRecoveryState(ctx, sqlcgen.ClearSyncRecoveryStateParams{
		DeviceID: duid, VerifiedEvents: verifiedEvents,
	}))
}

// GetRecoveryState reads one device's durable recovery requirement.
func (d Devices) GetRecoveryState(ctx context.Context, deviceID string) (syncrecovery.State, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	duid, err := parseUUID(deviceID)
	if err != nil {
		return syncrecovery.State{}, false, err
	}
	row, err := sqlcgen.New(d.pool).GetSyncRecoveryState(ctx, duid)
	if err != nil {
		if isNotFoundErr(err) {
			return syncrecovery.State{}, false, nil
		}
		return syncrecovery.State{}, false, catalogAdminErr(err)
	}
	state := syncrecovery.State{
		DeviceID:       uuidString(row.DeviceID),
		Required:       row.Required,
		Reason:         row.Reason,
		VerifiedEvents: row.VerifiedEvents,
		UpdatedAt:      row.UpdatedAt.Time,
	}
	if row.StartedAt.Valid {
		t := row.StartedAt.Time
		state.StartedAt = &t
	}
	if row.ClearedAt.Valid {
		t := row.ClearedAt.Time
		state.ClearedAt = &t
	}
	return state, true, nil
}

// CountRecoveryRequired reports how many devices still need
// reconciliation. A Store is never "recovered" while this is nonzero.
func (d Devices) CountRecoveryRequired(ctx context.Context) (int, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	n, err := sqlcgen.New(d.pool).CountSyncRecoveryRequired(ctx)
	if err != nil {
		return 0, catalogAdminErr(err)
	}
	return int(n), nil
}

// MissingSyncEvents returns which reported event IDs Cloud does not
// hold. The comparison is by identity only — counts never prove
// equality and are deliberately not consulted here.
func (d Devices) MissingSyncEvents(ctx context.Context, eventIDs []string) ([]string, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	ids := make([]pgtype.UUID, 0, len(eventIDs))
	for _, id := range eventIDs {
		uid, err := parseUUID(id)
		if err != nil {
			return nil, err
		}
		ids = append(ids, uid)
	}
	rows, err := sqlcgen.New(d.pool).MissingSyncEvents(ctx, ids)
	if err != nil {
		return nil, catalogAdminErr(err)
	}
	out := make([]string, 0, len(rows))
	for _, id := range rows {
		out = append(out, uuidString(id))
	}
	return out, nil
}

// SyncEventPayloadHash returns the stored identity hash of one event.
func (d Devices) SyncEventPayloadHash(ctx context.Context, eventID string) (string, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(eventID)
	if err != nil {
		return "", false, err
	}
	hash, err := sqlcgen.New(d.pool).SyncEventPayloadHash(ctx, uid)
	if err != nil {
		if isNotFoundErr(err) {
			return "", false, nil
		}
		return "", false, catalogAdminErr(err)
	}
	return strings.ToLower(hex.EncodeToString(hash)), true, nil
}

// VerifyProgress reads the server-side verification coverage ledger.
func (d Devices) VerifyProgress(ctx context.Context, deviceID string) (syncrecovery.VerifyProgress, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	duid, err := parseUUID(deviceID)
	if err != nil {
		return syncrecovery.VerifyProgress{}, false, err
	}
	row, err := sqlcgen.New(d.pool).SyncRecoveryVerifyProgress(ctx, duid)
	if err != nil {
		if isNotFoundErr(err) {
			return syncrecovery.VerifyProgress{}, false, nil
		}
		return syncrecovery.VerifyProgress{}, false, catalogAdminErr(err)
	}
	return syncrecovery.VerifyProgress{
		Generation:    row.VerifyGeneration,
		LastEventID:   row.VerifyLastEventID,
		Pages:         int(row.VerifyPages),
		Events:        row.VerifyEvents,
		Complete:      row.VerifyComplete,
		DeclaredTotal: row.VerifyDeclaredTotal,
	}, true, nil
}

// BeginVerifyPass starts a fresh verification generation so an
// interrupted reconciliation resumes cleanly.
func (d Devices) BeginVerifyPass(ctx context.Context, deviceID string, declaredTotal int64) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	duid, err := parseUUID(deviceID)
	if err != nil {
		return err
	}
	return catalogAdminErr(sqlcgen.New(d.pool).SyncRecoveryBeginVerify(ctx, sqlcgen.SyncRecoveryBeginVerifyParams{
		DeviceID: duid, VerifyDeclaredTotal: declaredTotal,
	}))
}

// RecordVerifyPage advances the coverage cursor. The UPDATE applies
// only when the page's keyset position moves forward, so a skipped or
// replayed page affects zero rows and is reported as refused.
func (d Devices) RecordVerifyPage(ctx context.Context, deviceID, lastEventID string, events int64, endOfHistory bool) (bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	duid, err := parseUUID(deviceID)
	if err != nil {
		return false, err
	}
	affected, err := sqlcgen.New(d.pool).SyncRecoveryRecordVerifyPage(ctx, sqlcgen.SyncRecoveryRecordVerifyPageParams{
		DeviceID: duid, VerifyLastEventID: lastEventID,
		VerifyEvents: events, Column4: endOfHistory,
	})
	if err != nil {
		return false, catalogAdminErr(err)
	}
	return affected == 1, nil
}

// CountPendingProjections reports event processors still retrying or
// blocked. §5: ingest is not projection; a non-zero count means
// business projections have not converged.
func (d Devices) CountPendingProjections(ctx context.Context) (int64, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	return sqlcgen.New(d.pool).CountPendingProjections(ctx)
}

// CountDeviceSyncEvents reports how many events Cloud already accepted
// from this device. It is an independent lower bound on the device's
// retained history: those events came out of the device's durable outbox,
// so the device cannot legitimately claim to retain fewer (Phase 20 R5).
func (d Devices) CountDeviceSyncEvents(ctx context.Context, deviceID string) (int64, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(deviceID)
	if err != nil {
		return 0, err
	}
	return sqlcgen.New(d.pool).CountDeviceSyncEvents(ctx, uid)
}
