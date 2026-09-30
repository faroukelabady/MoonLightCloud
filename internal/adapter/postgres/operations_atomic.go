package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/operations"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Atomic incident transitions live here: each incident transition and its
// durable event intent (deliveries, optional recovery) commit in a single
// transaction. Callers must keep Phase 7A enqueue outside the transaction.

func opsDeliveryParams(del operations.Delivery, at time.Time) (sqlcgen.CreateAlertDeliveryIdempotentParams, error) {
	uid, err := opsUUID(del.ID)
	if err != nil {
		return sqlcgen.CreateAlertDeliveryIdempotentParams{}, apperr.New(apperr.InvalidInput, "delivery id must be a UUID")
	}
	iid, err := opsUUID(del.IncidentID)
	if err != nil {
		return sqlcgen.CreateAlertDeliveryIdempotentParams{}, apperr.New(apperr.InvalidInput, "incident id must be a UUID")
	}
	rid, err := opsUUID(del.RecipientID)
	if err != nil {
		return sqlcgen.CreateAlertDeliveryIdempotentParams{}, apperr.New(apperr.InvalidInput, "recipient id must be a UUID")
	}
	return sqlcgen.CreateAlertDeliveryIdempotentParams{
		ID: uid, IncidentID: iid, RecipientID: rid, EventType: del.Event,
		ProviderKeySnapshot: del.ProviderKey, RecipientSnapshot: del.RecipientSnapshot,
		LocaleSnapshot: del.Locale, TemplateKey: del.TemplateKey, BodySnapshot: del.Body,
		BodyFingerprint: del.Fingerprint, NotificationIdempotencyKey: del.NotificationKey,
		CreatedAt: pgTime(at.UTC()),
	}, nil
}

// insertMissingDeliveries inserts delivery snapshots that do not already
// exist for (incident, event, recipient). Existing snapshots are never
// touched: no new alert keys, no rewrites. Entries carrying a preset
// LastErrorCode are recorded blocked in the same transaction so the
// dashboard shows the configuration problem explicitly.
func insertMissingDeliveries(ctx context.Context, q *sqlcgen.Queries, deliveries []operations.Delivery, at time.Time) error {
	for _, del := range deliveries {
		params, err := opsDeliveryParams(del, at)
		if err != nil {
			return err
		}
		if _, err := q.CreateAlertDeliveryIdempotent(ctx, params); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Already exists (concurrent writer or pre-atomic row):
				// finish a preset block only if it is still pending.
				if del.LastErrorCode != nil {
					if err := finishBlockedIfPending(ctx, q, del, at); err != nil {
						return err
					}
				}
				continue
			}
			return apperr.Wrap(apperr.Internal, "create delivery", redact(err))
		}
		if del.LastErrorCode != nil {
			uid, err := opsUUID(del.ID)
			if err != nil {
				return apperr.New(apperr.InvalidInput, "delivery id must be a UUID")
			}
			if _, err := q.FinishAlertDeliveryBlocked(ctx, sqlcgen.FinishAlertDeliveryBlockedParams{
				ID: uid, LastErrorCode: pgText(*del.LastErrorCode), UpdatedAt: pgTime(at.UTC()),
			}); err != nil {
				return apperr.Wrap(apperr.Internal, "block delivery", redact(err))
			}
		}
	}
	return nil
}

func finishBlockedIfPending(ctx context.Context, q *sqlcgen.Queries, del operations.Delivery, at time.Time) error {
	iid, err := opsUUID(del.IncidentID)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "incident id must be a UUID")
	}
	rid, err := opsUUID(del.RecipientID)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "recipient id must be a UUID")
	}
	existing, err := q.GetDeliveryByIdentity(ctx, sqlcgen.GetDeliveryByIdentityParams{
		IncidentID: iid, EventType: del.Event, RecipientID: rid,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return apperr.Wrap(apperr.Internal, "load delivery", redact(err))
	}
	if existing.Status != "pending" {
		return nil
	}
	if _, err := q.FinishAlertDeliveryBlocked(ctx, sqlcgen.FinishAlertDeliveryBlockedParams{
		ID: existing.ID, LastErrorCode: pgText(*del.LastErrorCode), UpdatedAt: pgTime(at.UTC()),
	}); err != nil {
		return apperr.Wrap(apperr.Internal, "block delivery", redact(err))
	}
	return nil
}

func markOpenFlag(ctx context.Context, q *sqlcgen.Queries, id pgtype.UUID, at time.Time) error {
	if _, err := q.MarkOpenIntentMaterialized(ctx, sqlcgen.MarkOpenIntentMaterializedParams{
		ID: id, UpdatedAt: pgTime(at.UTC()),
	}); err != nil {
		return apperr.Wrap(apperr.Internal, "mark open intent", redact(err))
	}
	return nil
}

func markResolvedFlag(ctx context.Context, q *sqlcgen.Queries, id pgtype.UUID, at time.Time) error {
	if _, err := q.MarkResolvedIntentMaterialized(ctx, sqlcgen.MarkResolvedIntentMaterializedParams{
		ID: id, UpdatedAt: pgTime(at.UTC()),
	}); err != nil {
		return apperr.Wrap(apperr.Internal, "mark resolved intent", redact(err))
	}
	return nil
}

// OpenStatefulAtomic opens (or adopts) the stateful incident and
// materializes its opened-event deliveries in one transaction.
func (d Devices) OpenStatefulAtomic(ctx context.Context, id, rule, subjectType, subjectID, severity, sourceKey string, episode int, deliveries []operations.Delivery, at time.Time) (operations.Incident, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(id)
	if err != nil {
		return operations.Incident{}, false, apperr.New(apperr.InvalidInput, "incident id must be a UUID")
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "open incident", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	row, err := q.OpenIncident(ctx, sqlcgen.OpenIncidentParams{
		ID: uid, RuleKey: rule, SubjectType: subjectType, SubjectID: subjectID,
		Severity: severity, Episode: int32(episode), SourceEventKey: pgText(sourceKey),
		OpenedAt: pgTime(at.UTC()),
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "open incident", redact(err))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// Lost the open race (or a resolved-then-reopened window): adopt
		// the current row and repair a missing intent inline.
		current, ok, gerr := d.adoptActiveTx(ctx, q, rule, subjectType, subjectID)
		if gerr != nil {
			return operations.Incident{}, false, gerr
		}
		if !ok {
			return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "open incident", errors.New("no active row after conflict"))
		}
		if !current.OpenIntentMaterialized {
			if err := insertMissingDeliveries(ctx, q, deliveries, at); err != nil {
				return operations.Incident{}, false, err
			}
			if err := markOpenFlag(ctx, q, mustParseOpsUUID(current.ID), at); err != nil {
				return operations.Incident{}, false, err
			}
			current.OpenIntentMaterialized = true
		}
		if err := tx.Commit(ctx); err != nil {
			return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "open incident", redact(err))
		}
		return current, false, nil
	}
	if err := insertMissingDeliveries(ctx, q, deliveries, at); err != nil {
		return operations.Incident{}, false, err
	}
	if err := markOpenFlag(ctx, q, uid, at); err != nil {
		return operations.Incident{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "open incident", redact(err))
	}
	incident := toOpsIncident(row)
	incident.OpenIntentMaterialized = true
	return incident, true, nil
}

// OpenEventAtomic opens (or adopts) the terminal-event incident and
// materializes its opened-event deliveries in one transaction.
func (d Devices) OpenEventAtomic(ctx context.Context, id, rule, subjectType, subjectID, severity, sourceKey string, deliveries []operations.Delivery, at time.Time) (operations.Incident, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(id)
	if err != nil {
		return operations.Incident{}, false, apperr.New(apperr.InvalidInput, "incident id must be a UUID")
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "open incident", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	row, err := q.OpenEventIncident(ctx, sqlcgen.OpenEventIncidentParams{
		ID: uid, RuleKey: rule, SubjectType: subjectType, SubjectID: subjectID,
		Severity: severity, SourceEventKey: pgText(sourceKey), OpenedAt: pgTime(at.UTC()),
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "open incident", redact(err))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		current, ok, gerr := d.adoptEventTx(ctx, q, sourceKey)
		if gerr != nil {
			return operations.Incident{}, false, gerr
		}
		if !ok {
			return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "open incident", errors.New("no event row after conflict"))
		}
		if !current.OpenIntentMaterialized {
			if err := insertMissingDeliveries(ctx, q, deliveries, at); err != nil {
				return operations.Incident{}, false, err
			}
			if err := markOpenFlag(ctx, q, mustParseOpsUUID(current.ID), at); err != nil {
				return operations.Incident{}, false, err
			}
			current.OpenIntentMaterialized = true
		}
		if err := tx.Commit(ctx); err != nil {
			return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "open incident", redact(err))
		}
		return current, false, nil
	}
	if err := insertMissingDeliveries(ctx, q, deliveries, at); err != nil {
		return operations.Incident{}, false, err
	}
	if err := markOpenFlag(ctx, q, uid, at); err != nil {
		return operations.Incident{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "open incident", redact(err))
	}
	incident := toOpsIncident(row)
	incident.OpenIntentMaterialized = true
	return incident, true, nil
}

func (d Devices) adoptActiveTx(ctx context.Context, q *sqlcgen.Queries, rule, subjectType, subjectID string) (operations.Incident, bool, error) {
	row, err := q.GetActiveIncident(ctx, sqlcgen.GetActiveIncidentParams{
		RuleKey: rule, SubjectType: subjectType, SubjectID: subjectID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return operations.Incident{}, false, nil
		}
		return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "load incident", redact(err))
	}
	return toOpsIncident(row), true, nil
}

func (d Devices) adoptEventTx(ctx context.Context, q *sqlcgen.Queries, sourceKey string) (operations.Incident, bool, error) {
	row, err := q.GetIncidentByEventKey(ctx, pgText(sourceKey))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return operations.Incident{}, false, nil
		}
		return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "load incident", redact(err))
	}
	return toOpsIncident(row), true, nil
}

// ResolveAtomic conditionally resolves the incident and materializes its
// resolved-event deliveries plus an optional reconnect recovery in one
// transaction. Only the committing scanner gets won=true; losers create
// nothing and must not proceed to metrics or further work.
func (d Devices) ResolveAtomic(ctx context.Context, id, code string, deliveries []operations.Delivery, recovery *operations.RecoveryIntent, at time.Time) (operations.Incident, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(id)
	if err != nil {
		return operations.Incident{}, false, apperr.New(apperr.NotFound, "incident not found")
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "resolve incident", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	row, err := q.ResolveIncident(ctx, sqlcgen.ResolveIncidentParams{
		ID: uid, ResolvedAt: pgTime(at.UTC()), ResolutionCode: pgText(code),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			current, ok, gerr := d.incidentByIDTx(ctx, q, uid)
			if gerr != nil {
				return operations.Incident{}, false, gerr
			}
			if !ok {
				return operations.Incident{}, false, nil
			}
			_ = tx.Rollback(ctx)
			return current, false, nil
		}
		return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "resolve incident", redact(err))
	}
	if err := insertMissingDeliveries(ctx, q, deliveries, at); err != nil {
		return operations.Incident{}, false, err
	}
	if err := markResolvedFlag(ctx, q, uid, at); err != nil {
		return operations.Incident{}, false, err
	}
	if recovery != nil {
		status, err := q.CheckDeviceActive(ctx, mustParseOpsUUID(recovery.DeviceID))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				status = ""
			} else {
				return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "resolve incident", redact(err))
			}
		}
		if status == "active" {
			rid, rerr := opsUUID(ids.System{}.New())
			if rerr != nil {
				return operations.Incident{}, false, apperr.New(apperr.Internal, "recovery id")
			}
			if _, err := q.CreateRecoveryAction(ctx, sqlcgen.CreateRecoveryActionParams{
				ID: rid, IncidentID: uid, ActionType: operations.ActionReconnectSync,
				IdempotencyKey: recovery.Key, CreatedAt: pgTime(at.UTC()),
			}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "resolve incident", redact(err))
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "resolve incident", redact(err))
	}
	incident := toOpsIncident(row)
	incident.ResolvedIntentMaterialized = true
	return incident, true, nil
}

func (d Devices) incidentByIDTx(ctx context.Context, q *sqlcgen.Queries, uid pgtype.UUID) (operations.Incident, bool, error) {
	row, err := q.GetIncident(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return operations.Incident{}, false, nil
		}
		return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "load incident", redact(err))
	}
	return toOpsIncident(row), true, nil
}

// MaterializeOpen fills missing opened-event deliveries for an incident
// whose open transition committed without its intent (pre-atomic rows),
// then sets the flag. Existing snapshots are never touched.
func (d Devices) MaterializeOpen(ctx context.Context, id string, deliveries []operations.Delivery, at time.Time) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(id)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "incident id must be a UUID")
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return apperr.Wrap(apperr.Internal, "materialize open intent", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	if err := insertMissingDeliveries(ctx, q, deliveries, at); err != nil {
		return err
	}
	if err := markOpenFlag(ctx, q, uid, at); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return apperr.Wrap(apperr.Internal, "materialize open intent", redact(err))
	}
	return nil
}

// MaterializeResolved fills missing resolved-event deliveries (and an
// optional reconnect recovery for still-active devices) for an incident
// whose resolution committed without its intent, then sets the flag.
func (d Devices) MaterializeResolved(ctx context.Context, id string, deliveries []operations.Delivery, recovery *operations.RecoveryIntent, at time.Time) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(id)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "incident id must be a UUID")
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return apperr.Wrap(apperr.Internal, "materialize resolved intent", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	if err := insertMissingDeliveries(ctx, q, deliveries, at); err != nil {
		return err
	}
	if recovery != nil {
		status, err := q.CheckDeviceActive(ctx, mustParseOpsUUID(recovery.DeviceID))
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return apperr.Wrap(apperr.Internal, "materialize resolved intent", redact(err))
		}
		if err == nil && status == "active" {
			rid, rerr := opsUUID(ids.System{}.New())
			if rerr != nil {
				return apperr.New(apperr.Internal, "recovery id")
			}
			if _, err := q.CreateRecoveryAction(ctx, sqlcgen.CreateRecoveryActionParams{
				ID: rid, IncidentID: uid, ActionType: operations.ActionReconnectSync,
				IdempotencyKey: recovery.Key, CreatedAt: pgTime(at.UTC()),
			}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return apperr.Wrap(apperr.Internal, "materialize resolved intent", redact(err))
			}
		}
	}
	if err := markResolvedFlag(ctx, q, uid, at); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return apperr.Wrap(apperr.Internal, "materialize resolved intent", redact(err))
	}
	return nil
}

// DeliveryByIdentity loads one delivery by its natural identity.
func (d Devices) DeliveryByIdentity(ctx context.Context, incidentID, event, recipientID string) (operations.Delivery, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	iid, err := opsUUID(incidentID)
	if err != nil {
		return operations.Delivery{}, false, apperr.New(apperr.InvalidInput, "incident id must be a UUID")
	}
	rid, err := opsUUID(recipientID)
	if err != nil {
		return operations.Delivery{}, false, apperr.New(apperr.InvalidInput, "recipient id must be a UUID")
	}
	row, err := sqlcgen.New(d.pool).GetDeliveryByIdentity(ctx, sqlcgen.GetDeliveryByIdentityParams{
		IncidentID: iid, EventType: event, RecipientID: rid,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return operations.Delivery{}, false, nil
		}
		return operations.Delivery{}, false, apperr.Wrap(apperr.Internal, "load delivery", redact(err))
	}
	return toOpsDelivery(row), true, nil
}

func mustParseOpsUUID(id string) pgtype.UUID {
	uid, err := opsUUID(id)
	if err != nil {
		panic(err)
	}
	return uid
}

// ResolveStatefulIfClear commits manual resolution only when the stateful
// predicate currently reads clear, with no check/write race. One
// transaction takes locks in a fixed global order — incident row, then
// predicate subject row — so concurrent resolvers serialize with exactly
// one winner and concurrent predicate writers block until commit; a
// condition turning active mid-flight is always observed. applied=false
// means the row did not transition; callers re-read to distinguish
// already-resolved (no-op success) from still-active (409). Unknown
// rules and non-UUID subjects fail closed.
func (d Devices) ResolveStatefulIfClear(ctx context.Context, id, rule, subjectID, code string, offlineEdge, at time.Time) (operations.Incident, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(id)
	if err != nil {
		return operations.Incident{}, false, apperr.New(apperr.NotFound, "incident not found")
	}
	suid, err := opsUUID(subjectID)
	if err != nil {
		return operations.Incident{}, false, apperr.New(apperr.Conflict, "CONDITION_STILL_ACTIVE")
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "guarded resolve", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	// Lock the incident row first: concurrent resolvers pile up here, and
	// later waiters observe whatever the winner committed.
	if _, err := q.LockIncidentRow(ctx, uid); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return operations.Incident{}, false, nil
		}
		return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "guarded resolve", redact(err))
	}
	// Lock the predicate source second (fixed global order: incident,
	// then predicate; no other path holds these in reverse, so this
	// cannot deadlock): concurrent writers of the same subject block
	// here until commit, so the guarded UPDATE below observes a stable
	// predicate. Missing subjects lock nothing, which matches StillActive
	// (absent reads as clear).
	var lockErr error
	switch rule {
	case operations.RuleDeviceOffline:
		_, lockErr = q.LockPresenceRow(ctx, suid)
	case operations.RuleDeviceSyncStale:
		_, lockErr = q.LockCommandRow(ctx, suid)
	case operations.RuleReportStale:
		_, lockErr = q.LockRunRow(ctx, suid)
	case operations.RuleNotificationStale:
		_, lockErr = q.LockNotificationRow(ctx, suid)
	default:
		return operations.Incident{}, false, apperr.New(apperr.InvalidInput, "unknown operations rule")
	}
	if lockErr != nil && !errors.Is(lockErr, pgx.ErrNoRows) {
		return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "guarded resolve", redact(lockErr))
	}
	var row sqlcgen.OperationalIncident
	var qerr error
	switch rule {
	case operations.RuleDeviceOffline:
		row, qerr = q.ResolveIfOfflineClear(ctx, sqlcgen.ResolveIfOfflineClearParams{
			ID: uid, LastSeenAt: pgTime(offlineEdge.UTC()), ResolvedAt: pgTime(at.UTC()), ResolutionCode: pgText(code),
		})
	case operations.RuleDeviceSyncStale:
		row, qerr = q.ResolveIfSyncClear(ctx, sqlcgen.ResolveIfSyncClearParams{
			ID: uid, ResolvedAt: pgTime(at.UTC()), ResolutionCode: pgText(code),
		})
	case operations.RuleReportStale:
		row, qerr = q.ResolveIfReportClear(ctx, sqlcgen.ResolveIfReportClearParams{
			ID: uid, ResolvedAt: pgTime(at.UTC()), ResolutionCode: pgText(code),
		})
	case operations.RuleNotificationStale:
		row, qerr = q.ResolveIfNotificationClear(ctx, sqlcgen.ResolveIfNotificationClearParams{
			ID: uid, ResolvedAt: pgTime(at.UTC()), ResolutionCode: pgText(code),
		})
	}
	if qerr != nil {
		if errors.Is(qerr, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return operations.Incident{}, false, nil
		}
		return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "guarded resolve", redact(qerr))
	}
	if err := tx.Commit(ctx); err != nil {
		return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "guarded resolve", redact(err))
	}
	return toOpsIncident(row), true, nil
}
