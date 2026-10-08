package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/fleetupdate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FleetUpdates is the PostgreSQL implementation of fleetupdate.Store
// (Phase 18, ADR-0052). Every mutation is one transaction including its
// audit row; delivery and reports lock the target row; all scheduling
// timestamps are the caller's application clock.
type FleetUpdates struct {
	pool    *pgxpool.Pool
	timeout time.Duration
}

// NewFleetUpdates wires the store.
func NewFleetUpdates(pool *pgxpool.Pool, timeout time.Duration) FleetUpdates {
	return FleetUpdates{pool: pool, timeout: timeout}
}

var _ fleetupdate.Store = FleetUpdates{}

func (f FleetUpdates) ctx(ctx context.Context) (context.Context, context.CancelFunc) {
	if f.timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, f.timeout)
}

func (f FleetUpdates) inTx(ctx context.Context, fn func(q *sqlcgen.Queries) error) error {
	ctx, cancel := f.ctx(ctx)
	defer cancel()
	tx, err := f.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return apperr.Wrap(apperr.Unavailable, "update store unavailable", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(sqlcgen.New(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return apperr.Wrap(apperr.Unavailable, "update store unavailable", redact(err))
	}
	return nil
}

func storeErr(err error) error {
	if err == nil {
		return nil
	}
	var app *apperr.Error
	if errors.As(err, &app) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.New(apperr.NotFound, "not found")
	}
	if errors.Is(err, fleetupdate.ErrNotFound) {
		return err
	}
	return apperr.Wrap(apperr.Internal, "update store failure", redact(err))
}

func optUUID(s string) pgtype.UUID {
	if s == "" {
		return pgtype.UUID{}
	}
	u, err := parseUUID(s)
	if err != nil {
		return pgtype.UUID{}
	}
	return u
}

func mustUUID(s string) pgtype.UUID {
	u, _ := parseUUID(s)
	return u
}

func optTime(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time.UTC()
	return &v
}

func optText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgText(s)
}

func audit(ctx context.Context, q *sqlcgen.Queries, now time.Time, kind, actor, action string, releaseID, rolloutID, targetID, deviceID, storeID pgtype.UUID, details map[string]any) error {
	if details == nil {
		details = map[string]any{}
	}
	raw, _ := json.Marshal(details)
	return q.InsertUpdateAudit(ctx, sqlcgen.InsertUpdateAuditParams{Now: pgTime(now), ActorKind: kind, Actor: actor, Action: action,
		ReleaseID: releaseID, RolloutID: rolloutID, TargetID: targetID, DeviceID: deviceID, StoreID: storeID, Details: raw})
}

func releaseView(r sqlcgen.Release, artifacts []sqlcgen.ReleaseArtifact) fleetupdate.ReleaseView {
	v := fleetupdate.ReleaseView{ID: uuidString(r.ID), ManifestDigest: r.ManifestDigest, ReleaseSequence: r.ReleaseSequence,
		Version: r.Version, BuildCommit: r.BuildCommit, MinInstalledSequence: r.MinInstalledSequence, KeyID: r.KeyID,
		Status: r.Status, ImportedBy: r.ImportedBy, ImportedAt: r.ImportedAt.Time.UTC(),
		StatusChangedBy: r.StatusChangedBy, StatusChangedAt: r.StatusChangedAt.Time.UTC(), Artifacts: []fleetupdate.ArtifactView{}}
	for _, a := range artifacts {
		v.Artifacts = append(v.Artifacts, fleetupdate.ArtifactView{OS: a.Os, Arch: a.Arch, Package: a.Package,
			FileName: a.FileName, Size: a.Size, SHA256: a.Sha256, URL: a.Url})
	}
	return v
}

func loadRelease(ctx context.Context, q *sqlcgen.Queries, id pgtype.UUID) (fleetupdate.ReleaseView, error) {
	r, err := q.GetRelease(ctx, id)
	if err != nil {
		return fleetupdate.ReleaseView{}, err
	}
	artifacts, err := q.ListReleaseArtifacts(ctx, id)
	if err != nil {
		return fleetupdate.ReleaseView{}, err
	}
	return releaseView(r, artifacts), nil
}

// ImportRelease stores a verified release immutably. Re-importing the same
// digest is idempotent; another digest for an existing sequence is a
// conflicting release identity and is refused (prompt §132).
func (f FleetUpdates) ImportRelease(ctx context.Context, rec fleetupdate.ReleaseImport, actor string, now time.Time) (fleetupdate.ReleaseView, bool, error) {
	var view fleetupdate.ReleaseView
	created := false
	m := rec.Verified.Manifest
	err := f.inTx(ctx, func(q *sqlcgen.Queries) error {
		if existing, err := q.GetReleaseByDigest(ctx, rec.Verified.Digest); err == nil {
			var lerr error
			view, lerr = loadRelease(ctx, q, existing.ID)
			return lerr
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err := q.GetReleaseBySequence(ctx, int64(m.ReleaseSequence)); err == nil {
			return apperr.New(apperr.Conflict, "RELEASE_SEQUENCE_CONFLICT")
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		id := mustUUID(rec.ID)
		if err := q.InsertRelease(ctx, sqlcgen.InsertReleaseParams{ID: id, ManifestDigest: rec.Verified.Digest,
			ReleaseSequence: int64(m.ReleaseSequence), Version: m.Version, BuildCommit: m.BuildCommit,
			MinInstalledSequence: int64(m.MinInstalledSequence), KeyID: rec.Verified.KeyID, Envelope: rec.Envelope,
			Actor: actor, Now: pgTime(now)}); err != nil {
			if isUniqueViolation(err) {
				return apperr.New(apperr.Conflict, "RELEASE_SEQUENCE_CONFLICT")
			}
			return err
		}
		for _, a := range rec.Artifacts {
			if err := q.InsertReleaseArtifact(ctx, sqlcgen.InsertReleaseArtifactParams{ReleaseID: id, Os: a.OS, Arch: a.Arch,
				Package: a.Package, FileName: a.FileName, Size: a.Size, Sha256: a.SHA256, Url: a.URL}); err != nil {
				return err
			}
		}
		if err := audit(ctx, q, now, "operator", actor, "release.imported", id, pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{},
			map[string]any{"manifest_digest": rec.Verified.Digest, "release_sequence": m.ReleaseSequence, "version": m.Version,
				"build_commit": m.BuildCommit, "key_id": rec.Verified.KeyID}); err != nil {
			return err
		}
		created = true
		var lerr error
		view, lerr = loadRelease(ctx, q, id)
		return lerr
	})
	return view, created, storeErr(err)
}

// GetRelease returns one release with artifacts.
func (f FleetUpdates) GetRelease(ctx context.Context, id string) (fleetupdate.ReleaseView, error) {
	ctx, cancel := f.ctx(ctx)
	defer cancel()
	view, err := loadRelease(ctx, sqlcgen.New(f.pool), mustUUID(id))
	return view, storeErr(err)
}

// ListReleases returns a bounded page without envelopes.
func (f FleetUpdates) ListReleases(ctx context.Context, beforeSequence int64, limit int) ([]fleetupdate.ReleaseView, error) {
	ctx, cancel := f.ctx(ctx)
	defer cancel()
	q := sqlcgen.New(f.pool)
	rows, err := q.ListReleases(ctx, sqlcgen.ListReleasesParams{BeforeSequence: beforeSequence, RowLimit: int32(limit)})
	if err != nil {
		return nil, storeErr(err)
	}
	out := make([]fleetupdate.ReleaseView, 0, len(rows))
	for _, r := range rows {
		artifacts, err := q.ListReleaseArtifacts(ctx, r.ID)
		if err != nil {
			return nil, storeErr(err)
		}
		out = append(out, releaseView(sqlcgen.Release{ID: r.ID, ManifestDigest: r.ManifestDigest, ReleaseSequence: r.ReleaseSequence,
			Version: r.Version, BuildCommit: r.BuildCommit, MinInstalledSequence: r.MinInstalledSequence, KeyID: r.KeyID,
			Status: r.Status, ImportedBy: r.ImportedBy, ImportedAt: r.ImportedAt, StatusChangedBy: r.StatusChangedBy,
			StatusChangedAt: r.StatusChangedAt}, artifacts))
	}
	return out, nil
}

// SetReleaseStatus changes lifecycle status (idempotent).
func (f FleetUpdates) SetReleaseStatus(ctx context.Context, id, status, actor string, now time.Time) (fleetupdate.ReleaseView, error) {
	var view fleetupdate.ReleaseView
	err := f.inTx(ctx, func(q *sqlcgen.Queries) error {
		uid := mustUUID(id)
		if _, err := q.GetRelease(ctx, uid); err != nil {
			return err
		}
		n, err := q.SetReleaseStatus(ctx, sqlcgen.SetReleaseStatusParams{ID: uid, Status: status, Actor: actor, Now: pgTime(now)})
		if err != nil {
			return err
		}
		if n > 0 {
			action := "release.activated"
			if status == fleetupdate.ReleaseRevoked {
				action = "release.revoked"
			}
			if err := audit(ctx, q, now, "operator", actor, action, uid, pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, pgtype.UUID{}, nil); err != nil {
				return err
			}
		}
		var lerr error
		view, lerr = loadRelease(ctx, q, uid)
		return lerr
	})
	return view, storeErr(err)
}

// CreateRollout snapshots the eligible target set immutably.
func (f FleetUpdates) CreateRollout(ctx context.Context, id string, req fleetupdate.RolloutRequest, actor string, newID func() string, now time.Time) (fleetupdate.RolloutView, error) {
	var view fleetupdate.RolloutView
	err := f.inTx(ctx, func(q *sqlcgen.Queries) error {
		rel, err := q.GetRelease(ctx, mustUUID(req.ReleaseID))
		if err != nil {
			return err
		}
		if rel.Status != fleetupdate.ReleaseActive {
			return apperr.New(apperr.Conflict, "RELEASE_REVOKED")
		}
		devices, err := q.EligibleRolloutDevices(ctx, sqlcgen.EligibleRolloutDevicesParams{StoreID: optUUID(req.StoreID),
			DeviceID: optUUID(req.DeviceID), RowLimit: fleetupdate.MaxTargets + 1})
		if err != nil {
			return err
		}
		if len(devices) > fleetupdate.MaxTargets {
			return apperr.New(apperr.InvalidInput, "rollout exceeds the target limit")
		}
		if req.Scope == fleetupdate.ScopeDevice && len(devices) != 1 {
			return apperr.New(apperr.InvalidInput, "device is not active and bound to the selected store")
		}
		rid := mustUUID(id)
		if err := q.InsertRollout(ctx, sqlcgen.InsertRolloutParams{ID: rid, ReleaseID: rel.ID, Scope: req.Scope,
			StoreID: optUUID(req.StoreID), DeviceID: optUUID(req.DeviceID), Mode: req.Mode, Percentage: int32(req.Percentage),
			NotBefore: pgTimePtr(req.NotBefore), TargetCount: int32(len(devices)), Actor: actor, Now: pgTime(now)}); err != nil {
			return err
		}
		for _, d := range devices {
			bucket := fleetupdate.Bucket(id, uuidString(d.DeviceID))
			state := fleetupdate.TargetNotSelected
			if bucket < req.Percentage {
				state = fleetupdate.TargetPending
			}
			if err := q.InsertRolloutTarget(ctx, sqlcgen.InsertRolloutTargetParams{ID: mustUUID(newID()), RolloutID: rid,
				DeviceID: d.DeviceID, StoreID: d.StoreID, Bucket: int32(bucket), State: state, Now: pgTime(now)}); err != nil {
				return err
			}
		}
		if err := audit(ctx, q, now, "operator", actor, "rollout.created", rel.ID, rid, pgtype.UUID{}, optUUID(req.DeviceID), optUUID(req.StoreID),
			map[string]any{"scope": req.Scope, "mode": req.Mode, "percentage": req.Percentage, "target_count": len(devices)}); err != nil {
			return err
		}
		var lerr error
		view, lerr = rolloutView(ctx, q, rid)
		return lerr
	})
	return view, storeErr(err)
}

func rolloutView(ctx context.Context, q *sqlcgen.Queries, id pgtype.UUID) (fleetupdate.RolloutView, error) {
	r, err := q.GetRollout(ctx, id)
	if err != nil {
		return fleetupdate.RolloutView{}, err
	}
	rel, err := q.GetRelease(ctx, r.ReleaseID)
	if err != nil {
		return fleetupdate.RolloutView{}, err
	}
	counts, err := q.CountRolloutTargetStates(ctx, id)
	if err != nil {
		return fleetupdate.RolloutView{}, err
	}
	v := toRolloutView(r, rel.Version, rel.ReleaseSequence)
	v.Counts = map[string]int64{}
	for _, c := range counts {
		v.Counts[c.State] = c.N
	}
	return v, nil
}

func toRolloutView(r sqlcgen.UpdateRollout, version string, seq int64) fleetupdate.RolloutView {
	v := fleetupdate.RolloutView{ID: uuidString(r.ID), ReleaseID: uuidString(r.ReleaseID), ReleaseVersion: version, ReleaseSeq: seq,
		Scope: r.Scope, Mode: r.Mode, Percentage: int(r.Percentage), Status: r.Status, NotBefore: optTime(r.NotBefore),
		TargetCount: int(r.TargetCount), CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt.Time.UTC(), UpdatedAt: r.UpdatedAt.Time.UTC()}
	if r.StoreID.Valid {
		v.StoreID = uuidString(r.StoreID)
	}
	if r.DeviceID.Valid {
		v.DeviceID = uuidString(r.DeviceID)
	}
	return v
}

// TransitionRollout changes lifecycle under the rollout row lock, so it
// serializes with delivery (which share-locks the rollout).
func (f FleetUpdates) TransitionRollout(ctx context.Context, id string, from []string, to, action, actor string, now time.Time) (fleetupdate.RolloutView, error) {
	var view fleetupdate.RolloutView
	err := f.inTx(ctx, func(q *sqlcgen.Queries) error {
		rid := mustUUID(id)
		r, err := q.LockRollout(ctx, rid)
		if err != nil {
			return err
		}
		n, err := q.SetRolloutStatus(ctx, sqlcgen.SetRolloutStatusParams{ID: rid, FromStatuses: from, ToStatus: to, Now: pgTime(now)})
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.New(apperr.Conflict, "ROLLOUT_STATE_CONFLICT")
		}
		details := map[string]any{"from": r.Status, "to": to}
		if to == fleetupdate.RolloutCancelled {
			cancelled, err := q.CancelOpenTargets(ctx, sqlcgen.CancelOpenTargetsParams{RolloutID: rid, Now: pgTime(now)})
			if err != nil {
				return err
			}
			details["cancelled_targets"] = cancelled
		}
		if err := audit(ctx, q, now, "operator", actor, action, r.ReleaseID, rid, pgtype.UUID{}, pgtype.UUID{}, r.StoreID, details); err != nil {
			return err
		}
		var lerr error
		view, lerr = rolloutView(ctx, q, rid)
		return lerr
	})
	return view, storeErr(err)
}

// SetRolloutPercentage widens a staged rollout over the SAME snapshot.
func (f FleetUpdates) SetRolloutPercentage(ctx context.Context, id string, percentage int, actor string, now time.Time) (fleetupdate.RolloutView, error) {
	var view fleetupdate.RolloutView
	err := f.inTx(ctx, func(q *sqlcgen.Queries) error {
		rid := mustUUID(id)
		r, err := q.LockRollout(ctx, rid)
		if err != nil {
			return err
		}
		n, err := q.SetRolloutPercentage(ctx, sqlcgen.SetRolloutPercentageParams{ID: rid, Percentage: int32(percentage), Now: pgTime(now)})
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.New(apperr.Conflict, "ROLLOUT_PERCENTAGE_CONFLICT")
		}
		promoted, err := q.PromoteSelectedTargets(ctx, sqlcgen.PromoteSelectedTargetsParams{RolloutID: rid, Percentage: int32(percentage), Now: pgTime(now)})
		if err != nil {
			return err
		}
		if err := audit(ctx, q, now, "operator", actor, "rollout.percentage", r.ReleaseID, rid, pgtype.UUID{}, pgtype.UUID{}, r.StoreID,
			map[string]any{"from": r.Percentage, "to": percentage, "promoted": promoted}); err != nil {
			return err
		}
		var lerr error
		view, lerr = rolloutView(ctx, q, rid)
		return lerr
	})
	return view, storeErr(err)
}

// GetRollout returns one rollout with counts.
func (f FleetUpdates) GetRollout(ctx context.Context, id string) (fleetupdate.RolloutView, error) {
	ctx, cancel := f.ctx(ctx)
	defer cancel()
	v, err := rolloutView(ctx, sqlcgen.New(f.pool), mustUUID(id))
	return v, storeErr(err)
}

// ListRollouts returns a keyset page.
func (f FleetUpdates) ListRollouts(ctx context.Context, storeID string, cursorAt *time.Time, cursorID string, limit int) ([]fleetupdate.RolloutView, error) {
	ctx, cancel := f.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(f.pool).ListRollouts(ctx, sqlcgen.ListRolloutsParams{StoreID: optUUID(storeID),
		CursorAt: pgTimePtr(cursorAt), CursorID: optUUID(cursorID), RowLimit: int32(limit)})
	if err != nil {
		return nil, storeErr(err)
	}
	out := make([]fleetupdate.RolloutView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toRolloutView(sqlcgen.UpdateRollout{ID: r.ID, ReleaseID: r.ReleaseID, Scope: r.Scope, StoreID: r.StoreID,
			DeviceID: r.DeviceID, Mode: r.Mode, Percentage: r.Percentage, Status: r.Status, NotBefore: r.NotBefore,
			TargetCount: r.TargetCount, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}, r.Version, r.ReleaseSequence))
	}
	return out, nil
}

// ListTargets returns targets of one rollout (optionally one Store).
func (f FleetUpdates) ListTargets(ctx context.Context, rolloutID, storeID, afterDevice string, limit int) ([]fleetupdate.TargetView, error) {
	ctx, cancel := f.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(f.pool).ListRolloutTargets(ctx, sqlcgen.ListRolloutTargetsParams{RolloutID: mustUUID(rolloutID),
		StoreID: optUUID(storeID), AfterDevice: optUUID(afterDevice), RowLimit: int32(limit)})
	if err != nil {
		return nil, storeErr(err)
	}
	out := make([]fleetupdate.TargetView, 0, len(rows))
	for _, r := range rows {
		out = append(out, fleetupdate.TargetView{ID: uuidString(r.ID), RolloutID: uuidString(r.RolloutID), DeviceID: uuidString(r.DeviceID),
			DeviceName: r.DeviceName, StoreID: uuidString(r.StoreID), State: r.State, AttemptCount: int(r.AttemptCount),
			NextAttempt: optTime(r.NextAttemptAt), LastError: textOrEmpty(r.LastError), DeliveredAt: optTime(r.DeliveredAt),
			FinishedAt: optTime(r.FinishedAt), UpdatedAt: r.UpdatedAt.Time.UTC()})
	}
	return out, nil
}

// TargetHistory returns a target's append-only history.
func (f FleetUpdates) TargetHistory(ctx context.Context, targetID string, limit int) ([]fleetupdate.TargetEvent, error) {
	ctx, cancel := f.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(f.pool).ListTargetEvents(ctx, sqlcgen.ListTargetEventsParams{TargetID: mustUUID(targetID), RowLimit: int32(limit)})
	if err != nil {
		return nil, storeErr(err)
	}
	out := make([]fleetupdate.TargetEvent, 0, len(rows))
	for _, r := range rows {
		out = append(out, fleetupdate.TargetEvent{ReportedState: r.ReportedState, TargetState: r.TargetState,
			ErrorCode: textOrEmpty(r.ErrorCode), Retryable: r.Retryable, RecordedAt: r.RecordedAt.Time.UTC()})
	}
	return out, nil
}

// Fleet returns a bounded device page.
func (f FleetUpdates) Fleet(ctx context.Context, storeID, afterDevice string, limit int) ([]fleetupdate.FleetRow, error) {
	ctx, cancel := f.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(f.pool).ListFleet(ctx, sqlcgen.ListFleetParams{StoreID: optUUID(storeID), AfterDevice: optUUID(afterDevice), RowLimit: int32(limit)})
	if err != nil {
		return nil, storeErr(err)
	}
	out := make([]fleetupdate.FleetRow, 0, len(rows))
	for _, r := range rows {
		row := fleetupdate.FleetRow{DeviceID: uuidString(r.DeviceID), DeviceName: r.Name, DeviceStatus: r.DeviceStatus,
			StoreID: uuidString(r.StoreID), LastSeenAt: optTime(r.LastSeenAt), Version: textOrEmpty(r.Version),
			BuildCommit: textOrEmpty(r.BuildCommit), ReleaseSequence: r.ReleaseSequence.Int64, OS: textOrEmpty(r.Os),
			Arch: textOrEmpty(r.Arch), UpdaterProtocol: int(r.UpdaterProtocol.Int32), UpdaterCapable: r.UpdaterCapable.Bool,
			UpdaterReported: r.ReportedAt.Valid, UnsupportedReason: textOrEmpty(r.UnsupportedReason),
			UpdateState: textOrEmpty(r.UpdateState), UpdateError: textOrEmpty(r.UpdateError), ReportedAt: optTime(r.ReportedAt),
			TargetState: r.TargetState, TargetError: textOrEmpty(r.TargetError), TargetVersion: r.TargetVersion, TargetSequence: r.TargetSequence}
		if r.TargetID.Valid {
			row.TargetID = uuidString(r.TargetID)
		}
		if r.RolloutID.Valid {
			row.RolloutID = uuidString(r.RolloutID)
		}
		out = append(out, row)
	}
	return out, nil
}

// Audit returns a bounded audit page.
func (f FleetUpdates) Audit(ctx context.Context, storeID string, beforeID int64, limit int) ([]fleetupdate.AuditEvent, error) {
	ctx, cancel := f.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(f.pool).ListUpdateAudit(ctx, sqlcgen.ListUpdateAuditParams{StoreID: optUUID(storeID), BeforeID: beforeID, RowLimit: int32(limit)})
	if err != nil {
		return nil, storeErr(err)
	}
	out := make([]fleetupdate.AuditEvent, 0, len(rows))
	for _, r := range rows {
		ev := fleetupdate.AuditEvent{ID: r.ID, OccurredAt: r.OccurredAt.Time.UTC(), ActorKind: r.ActorKind, Actor: r.Actor, Action: r.Action, Details: map[string]any{}}
		_ = json.Unmarshal(r.Details, &ev.Details)
		for _, p := range []struct {
			dst *string
			src pgtype.UUID
		}{{&ev.ReleaseID, r.ReleaseID}, {&ev.RolloutID, r.RolloutID}, {&ev.TargetID, r.TargetID}, {&ev.DeviceID, r.DeviceID}, {&ev.StoreID, r.StoreID}} {
			if p.src.Valid {
				*p.dst = uuidString(p.src)
			}
		}
		out = append(out, ev)
	}
	return out, nil
}

// RecordStatus upserts the authenticated device's trusted report and
// settles PENDING targets it can never take: incapable updater,
// platform without artifact, or already at/past the target release.
func (f FleetUpdates) RecordStatus(ctx context.Context, deviceID string, s fleetupdate.StatusReport, now time.Time) error {
	return storeErr(f.inTx(ctx, func(q *sqlcgen.Queries) error {
		did := mustUUID(deviceID)
		if err := q.UpsertDeviceUpdateStatus(ctx, sqlcgen.UpsertDeviceUpdateStatusParams{DeviceID: did, Version: s.Version,
			BuildCommit: s.BuildCommit, ReleaseSequence: s.ReleaseSequence, Os: s.OS, Arch: s.Arch,
			UpdaterProtocol: int32(s.UpdaterProtocol), UpdaterCapable: s.UpdaterCapable, UnsupportedReason: optText(s.UnsupportedReason),
			UpdateState: s.UpdateState, UpdateError: optText(s.UpdateError), Now: pgTime(now)}); err != nil {
			return err
		}
		if !s.UpdaterCapable || s.UpdaterProtocol < fleetupdate.UpdaterProtocol {
			reason := s.UnsupportedReason
			if reason == "" {
				reason = "UPDATE_MANUAL_ACTION_REQUIRED"
			}
			_, err := q.MarkIncapableDeviceTargets(ctx, sqlcgen.MarkIncapableDeviceTargetsParams{DeviceID: did, Reason: reason, Now: pgTime(now)})
			return err
		}
		if _, err := q.MarkUnsupportedPlatformTargets(ctx, sqlcgen.MarkUnsupportedPlatformTargetsParams{DeviceID: did, Os: s.OS,
			Arch: s.Arch, Package: fleetupdate.PackageTarGz, Now: pgTime(now)}); err != nil {
			return err
		}
		if s.ReleaseSequence > 0 {
			if _, err := q.MarkNewerDeviceTargets(ctx, sqlcgen.MarkNewerDeviceTargetsParams{DeviceID: did,
				ReleaseSequence: s.ReleaseSequence, BuildCommit: s.BuildCommit, Now: pgTime(now)}); err != nil {
				return err
			}
		}
		return nil
	}))
}

// ClaimCommand delivers at most one eligible target to the device. A
// device that never reported a capable updater (pre-Phase-18 Retail)
// receives nothing: Cloud cannot bootstrap an updater (prompt §97).
func (f FleetUpdates) ClaimCommand(ctx context.Context, deviceID string, now time.Time) (*fleetupdate.Command, error) {
	var cmd *fleetupdate.Command
	err := f.inTx(ctx, func(q *sqlcgen.Queries) error {
		did := mustUUID(deviceID)
		status, err := q.GetDeviceUpdateStatus(ctx, did)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !status.UpdaterCapable || status.UpdaterProtocol < fleetupdate.UpdaterProtocol {
			return nil
		}
		if _, err := q.MarkUnsupportedPlatformTargets(ctx, sqlcgen.MarkUnsupportedPlatformTargetsParams{DeviceID: did, Os: status.Os,
			Arch: status.Arch, Package: fleetupdate.PackageTarGz, Now: pgTime(now)}); err != nil {
			return err
		}
		row, err := q.ClaimDeviceUpdateTarget(ctx, sqlcgen.ClaimDeviceUpdateTargetParams{DeviceID: did, Os: status.Os,
			Arch: status.Arch, Package: fleetupdate.PackageTarGz, Now: pgTime(now)})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := q.MarkTargetDelivered(ctx, sqlcgen.MarkTargetDeliveredParams{ID: row.TargetID, Now: pgTime(now)}); err != nil {
			return err
		}
		if row.State == fleetupdate.TargetPending {
			if err := audit(ctx, q, now, "device", deviceID, "target.delivered", row.ReleaseID, row.RolloutID, row.TargetID, did, pgtype.UUID{}, nil); err != nil {
				return err
			}
		}
		r, err := q.GetRollout(ctx, row.RolloutID)
		if err != nil {
			return err
		}
		cmd = &fleetupdate.Command{TargetID: uuidString(row.TargetID), Type: fleetupdate.CommandType, Version: 1,
			ReleaseID: uuidString(row.ReleaseID), ManifestDigest: row.ManifestDigest, Mode: row.Mode,
			NotBefore: optTime(r.NotBefore), Envelope: row.Envelope, ArtifactURL: row.ArtifactUrl, ReleaseStatus: row.ReleaseStatus}
		return nil
	})
	if err != nil {
		return nil, storeErr(err)
	}
	return cmd, nil
}

// ApplyReport records one device report against the device's OWN target
// (device id is part of the lookup: foreign targets are not found).
func (f FleetUpdates) ApplyReport(ctx context.Context, deviceID, targetID string, r fleetupdate.TargetReport, now time.Time) (fleetupdate.Ack, error) {
	var ack fleetupdate.Ack
	err := f.inTx(ctx, func(q *sqlcgen.Queries) error {
		did := mustUUID(deviceID)
		t, err := q.LockDeviceTarget(ctx, sqlcgen.LockDeviceTargetParams{ID: mustUUID(targetID), DeviceID: did})
		if errors.Is(err, pgx.ErrNoRows) {
			return fleetupdate.ErrNotFound
		}
		if err != nil {
			return err
		}
		if (r.ManifestDigest != "" && r.ManifestDigest != t.ManifestDigest) || (r.ReleaseSequence != 0 && r.ReleaseSequence != t.ReleaseSequence) {
			return apperr.New(apperr.InvalidInput, "report does not match the target release")
		}
		mapped, _ := fleetupdate.MapReport(r.State, r.ErrorCode)
		d := fleetupdate.ApplyReport(t.State, int(t.AttemptCount), mapped, r.Retryable, now)
		if d.Changed {
			var finished pgtype.Timestamptz
			if d.Finished {
				finished = pgTime(now)
			}
			if err := q.UpdateTargetState(ctx, sqlcgen.UpdateTargetStateParams{ID: t.ID, State: d.State, LastError: optText(r.ErrorCode),
				Retryable: r.Retryable, AttemptCount: int32(d.AttemptCount), NextAttemptAt: pgTimePtr(d.NextAttemptAt),
				FinishedAt: finished, Now: pgTime(now)}); err != nil {
				return err
			}
			if err := q.InsertTargetEvent(ctx, sqlcgen.InsertTargetEventParams{TargetID: t.ID, DeviceID: did, ReportedState: r.State,
				TargetState: d.State, ErrorCode: optText(r.ErrorCode), Retryable: r.Retryable, Now: pgTime(now)}); err != nil {
				return err
			}
			if action := reportAuditAction(d); action != "" {
				if err := audit(ctx, q, now, "device", deviceID, action, t.ReleaseID, t.RolloutID, t.ID, did, t.StoreID,
					map[string]any{"reported_state": r.State, "error_code": r.ErrorCode}); err != nil {
					return err
				}
			}
			if d.Finished {
				if err := completeIfDone(ctx, q, t.RolloutID, now); err != nil {
					return err
				}
			}
		}
		ack = fleetupdate.Ack{TargetState: d.State, ReleaseStatus: t.ReleaseStatus}
		return nil
	})
	if errors.Is(err, fleetupdate.ErrNotFound) {
		return fleetupdate.Ack{}, err
	}
	return ack, storeErr(err)
}

func reportAuditAction(d fleetupdate.Decision) string {
	switch {
	case d.CancelSuperseded:
		return "target.cancel_superseded"
	case d.State == fleetupdate.TargetSucceeded || d.State == fleetupdate.TargetCompliant:
		return "target.succeeded"
	case d.State == fleetupdate.TargetRolledBack:
		return "target.rolled_back"
	case d.State == fleetupdate.TargetFailed || d.State == fleetupdate.TargetManual:
		return "target.failed"
	case d.State == fleetupdate.TargetPending && d.NextAttemptAt != nil:
		return "target.retry_scheduled"
	}
	return ""
}

// completeIfDone marks a fully-widened ACTIVE rollout COMPLETED once no
// target is still open. Failed/offline devices never block others: each
// target progresses independently (prompt §140).
func completeIfDone(ctx context.Context, q *sqlcgen.Queries, rolloutID pgtype.UUID, now time.Time) error {
	r, err := q.GetRollout(ctx, rolloutID)
	if err != nil {
		return err
	}
	if r.Status != fleetupdate.RolloutActive || r.Percentage != 100 {
		return nil
	}
	open, err := q.OpenRolloutTargetCount(ctx, rolloutID)
	if err != nil || open > 0 {
		return err
	}
	_, err = q.SetRolloutStatus(ctx, sqlcgen.SetRolloutStatusParams{ID: rolloutID, FromStatuses: []string{fleetupdate.RolloutActive},
		ToStatus: fleetupdate.RolloutCompleted, Now: pgTime(now)})
	return err
}
