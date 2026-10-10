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
// reconciliation after a Cloud restore and starts a fresh verification
// generation, so a ledger from an earlier recovery can never be reused.
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

// CountRecoveryRequired reports how many active devices of the
// requesting device's Store still need reconciliation. Another Store's
// devices never count. A Store is never "recovered" while this is nonzero.
func (d Devices) CountRecoveryRequired(ctx context.Context, deviceID string) (int, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	duid, err := parseUUID(deviceID)
	if err != nil {
		return 0, err
	}
	n, err := sqlcgen.New(d.pool).CountStoreSyncRecoveryRequired(ctx, duid)
	if err != nil {
		return 0, catalogAdminErr(err)
	}
	return int(n), nil
}

// SyncEventHashes returns the stored payload hash of every reported event
// Cloud holds, keyed by canonical (lowercase) event ID, in one bounded
// query. Comparison is by identity only — counts never prove equality.
func (d Devices) SyncEventHashes(ctx context.Context, eventIDs []string) (map[string]string, error) {
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
	rows, err := sqlcgen.New(d.pool).SyncEventHashes(ctx, ids)
	if err != nil {
		return nil, catalogAdminErr(err)
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[strings.ToLower(uuidString(row.EventID))] = strings.ToLower(hex.EncodeToString(row.PayloadHash))
	}
	return out, nil
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
		Missing:       row.VerifyMissing,
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

// RecordVerifyPage advances the coverage cursor and the missing ledger.
// The UPDATE applies only when the page starts above the recorded cursor
// in byte order (COLLATE "C"), so a skipped-back, replayed or
// out-of-order page affects zero rows and is reported as refused.
func (d Devices) RecordVerifyPage(ctx context.Context, deviceID, firstEventID, lastEventID string, events, missing int64, endOfHistory bool) (bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	duid, err := parseUUID(deviceID)
	if err != nil {
		return false, err
	}
	affected, err := sqlcgen.New(d.pool).SyncRecoveryRecordVerifyPage(ctx, sqlcgen.SyncRecoveryRecordVerifyPageParams{
		DeviceID: duid, FirstEventID: firstEventID, LastEventID: lastEventID,
		Events: events, Missing: missing, EndOfHistory: endOfHistory,
	})
	if err != nil {
		return false, catalogAdminErr(err)
	}
	return affected == 1, nil
}

// CountPendingProjections reports non-terminal processor rows (pending,
// retry, blocked) for events of the requesting device's Store. §5:
// ingest is not projection; a non-zero count means business projections
// have not converged.
func (d Devices) CountPendingProjections(ctx context.Context, deviceID string) (int64, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	duid, err := parseUUID(deviceID)
	if err != nil {
		return 0, err
	}
	return sqlcgen.New(d.pool).CountStorePendingProjections(ctx, duid)
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
