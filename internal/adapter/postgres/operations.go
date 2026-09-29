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

func toOpsIncident(r sqlcgen.OperationalIncident) operations.Incident {
	in := operations.Incident{
		ID: uuidString(r.ID), Rule: r.RuleKey, SubjectType: r.SubjectType,
		SubjectID: r.SubjectID, Severity: r.Severity, State: r.State,
		Episode: int(r.Episode), OpenedAt: r.OpenedAt.Time.UTC(),
		LastObservedAt: r.LastObservedAt.Time.UTC(),
	}
	if r.SourceEventKey.Valid {
		s := r.SourceEventKey.String
		in.SourceEventKey = &s
	}
	if r.AcknowledgedAt.Valid {
		t := r.AcknowledgedAt.Time.UTC()
		in.AcknowledgedAt = &t
	}
	if r.ResolvedAt.Valid {
		t := r.ResolvedAt.Time.UTC()
		in.ResolvedAt = &t
	}
	if r.ResolutionCode.Valid {
		s := r.ResolutionCode.String
		in.ResolutionCode = &s
	}
	in.OpenIntentMaterialized = r.OpenIntentMaterialized
	in.ResolvedIntentMaterialized = r.ResolvedIntentMaterialized
	return in
}

func toOpsRecipient(r sqlcgen.OperationalAlertRecipient) operations.Recipient {
	return operations.Recipient{
		ID: uuidString(r.ID), Label: r.Label, ProviderKey: r.ProviderKey,
		Recipient: r.Recipient, Locale: r.Locale, Enabled: r.Enabled,
	}
}

func toOpsDelivery(r sqlcgen.OperationalAlertDelivery) operations.Delivery {
	d := operations.Delivery{
		ID: uuidString(r.ID), IncidentID: uuidString(r.IncidentID),
		RecipientID: uuidString(r.RecipientID), Event: r.EventType,
		ProviderKey: r.ProviderKeySnapshot, RecipientSnapshot: r.RecipientSnapshot,
		Locale: r.LocaleSnapshot, TemplateKey: r.TemplateKey, Body: r.BodySnapshot,
		Fingerprint: r.BodyFingerprint, NotificationKey: r.NotificationIdempotencyKey,
		Status: r.Status,
	}
	if r.NotificationID.Valid {
		s := uuidString(r.NotificationID)
		d.NotificationID = &s
	}
	if r.LastErrorCode.Valid {
		s := r.LastErrorCode.String
		d.LastErrorCode = &s
	}
	return d
}

// CreateRecipient persists one alert recipient (full address at rest).
func (d Devices) CreateOpsRecipient(ctx context.Context, id, label, provider, recipient, locale string, at time.Time) (operations.Recipient, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return operations.Recipient{}, apperr.New(apperr.InvalidInput, "recipient id must be a UUID")
	}
	row, err := sqlcgen.New(d.pool).CreateAlertRecipient(ctx, sqlcgen.CreateAlertRecipientParams{
		ID: uid, Label: label, ProviderKey: provider, Recipient: recipient,
		Locale: locale, CreatedAt: pgTime(at.UTC()),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return operations.Recipient{}, apperr.New(apperr.Conflict, "recipient already exists")
		}
		return operations.Recipient{}, apperr.Wrap(apperr.Internal, "create recipient", redact(err))
	}
	return toOpsRecipient(row), nil
}

// ListRecipients returns all recipients (caller masks addresses).
func (d Devices) ListOpsRecipients(ctx context.Context) ([]operations.Recipient, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ListAlertRecipients(ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "list recipients", redact(err))
	}
	out := make([]operations.Recipient, 0, len(rows))
	for _, r := range rows {
		out = append(out, toOpsRecipient(r))
	}
	return out, nil
}

// ListEnabledRecipients returns enabled recipients for alert fan-out.
func (d Devices) ListEnabledOpsRecipients(ctx context.Context) ([]operations.Recipient, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ListEnabledAlertRecipients(ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "list recipients", redact(err))
	}
	out := make([]operations.Recipient, 0, len(rows))
	for _, r := range rows {
		out = append(out, toOpsRecipient(r))
	}
	return out, nil
}

// DisableRecipient flips one recipient off.
func (d Devices) DisableOpsRecipient(ctx context.Context, id string, at time.Time) (bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return false, apperr.New(apperr.NotFound, "recipient not found")
	}
	n, err := sqlcgen.New(d.pool).DisableAlertRecipient(ctx, sqlcgen.DisableAlertRecipientParams{
		ID: uid, UpdatedAt: pgTime(at.UTC()),
	})
	if err != nil {
		return false, apperr.Wrap(apperr.Internal, "disable recipient", redact(err))
	}
	return n == 1, nil
}

func opsUUID(id string) (pgtype.UUID, error) {
	return parseUUID(id)
}

// OpenStateful inserts or adopts the active stateful incident. The partial
// unique index arbitrates multi-instance races (created=false on adopt).
func (d Devices) OpenStateful(ctx context.Context, id, rule, subjectType, subjectID, severity, sourceKey string, episode int, at time.Time) (operations.Incident, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(id)
	if err != nil {
		return operations.Incident{}, false, apperr.New(apperr.InvalidInput, "incident id must be a UUID")
	}
	row, err := sqlcgen.New(d.pool).OpenIncident(ctx, sqlcgen.OpenIncidentParams{
		ID: uid, RuleKey: rule, SubjectType: subjectType, SubjectID: subjectID,
		Severity: severity, Episode: int32(episode), SourceEventKey: pgText(sourceKey),
		OpenedAt: pgTime(at.UTC()),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			active, ok, gerr := d.ActiveIncident(ctx, rule, subjectType, subjectID)
			if gerr != nil {
				return operations.Incident{}, false, gerr
			}
			if !ok {
				return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "open incident", errors.New("no active row after conflict"))
			}
			return active, false, nil
		}
		return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "open incident", redact(err))
	}
	return toOpsIncident(row), true, nil
}

// OpenEvent inserts or adopts the all-time terminal-event incident.
func (d Devices) OpenEvent(ctx context.Context, id, rule, subjectType, subjectID, severity, sourceKey string, at time.Time) (operations.Incident, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(id)
	if err != nil {
		return operations.Incident{}, false, apperr.New(apperr.InvalidInput, "incident id must be a UUID")
	}
	row, err := sqlcgen.New(d.pool).OpenEventIncident(ctx, sqlcgen.OpenEventIncidentParams{
		ID: uid, RuleKey: rule, SubjectType: subjectType, SubjectID: subjectID,
		Severity: severity, SourceEventKey: pgText(sourceKey), OpenedAt: pgTime(at.UTC()),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			existing, ok, gerr := d.IncidentByEventKey(ctx, sourceKey)
			if gerr != nil {
				return operations.Incident{}, false, gerr
			}
			if !ok {
				return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "open incident", errors.New("no event row after conflict"))
			}
			return existing, false, nil
		}
		return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "open incident", redact(err))
	}
	return toOpsIncident(row), true, nil
}

// ActiveIncident loads the open/acknowledged incident for rule+subject.
func (d Devices) ActiveIncident(ctx context.Context, rule, subjectType, subjectID string) (operations.Incident, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	row, err := sqlcgen.New(d.pool).GetActiveIncident(ctx, sqlcgen.GetActiveIncidentParams{
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

// IncidentByEventKey loads any incident for a stable event key.
func (d Devices) IncidentByEventKey(ctx context.Context, key string) (operations.Incident, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	row, err := sqlcgen.New(d.pool).GetIncidentByEventKey(ctx, pgText(key))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return operations.Incident{}, false, nil
		}
		return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "load incident", redact(err))
	}
	return toOpsIncident(row), true, nil
}

// IncidentByID loads one incident by ID.
func (d Devices) IncidentByID(ctx context.Context, id string) (operations.Incident, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(id)
	if err != nil {
		return operations.Incident{}, false, apperr.New(apperr.NotFound, "incident not found")
	}
	row, err := sqlcgen.New(d.pool).GetIncident(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return operations.Incident{}, false, nil
		}
		return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "load incident", redact(err))
	}
	return toOpsIncident(row), true, nil
}

// TouchObserved refreshes last_observed on active incidents.
func (d Devices) TouchObserved(ctx context.Context, id string, at time.Time) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(id)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "incident id must be a UUID")
	}
	if _, err := sqlcgen.New(d.pool).TouchIncidentObserved(ctx, sqlcgen.TouchIncidentObservedParams{
		ID: uid, LastObservedAt: pgTime(at.UTC()),
	}); err != nil {
		return apperr.Wrap(apperr.Internal, "touch incident", redact(err))
	}
	return nil
}

// Acknowledge transitions open → acknowledged (idempotent past that).
func (d Devices) Acknowledge(ctx context.Context, id string, at time.Time) (operations.Incident, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(id)
	if err != nil {
		return operations.Incident{}, false, apperr.New(apperr.NotFound, "incident not found")
	}
	row, err := sqlcgen.New(d.pool).AcknowledgeIncident(ctx, sqlcgen.AcknowledgeIncidentParams{
		ID: uid, UpdatedAt: pgTime(at.UTC()),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			current, ok, gerr := d.IncidentByID(ctx, id)
			if gerr != nil {
				return operations.Incident{}, false, gerr
			}
			if !ok {
				return operations.Incident{}, false, nil
			}
			return current, false, nil
		}
		return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "acknowledge incident", redact(err))
	}
	return toOpsIncident(row), true, nil
}

// Resolve transitions open/acknowledged → resolved (first writer wins).
func (d Devices) Resolve(ctx context.Context, id, code string, at time.Time) (operations.Incident, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(id)
	if err != nil {
		return operations.Incident{}, false, apperr.New(apperr.NotFound, "incident not found")
	}
	row, err := sqlcgen.New(d.pool).ResolveIncident(ctx, sqlcgen.ResolveIncidentParams{
		ID: uid, ResolvedAt: pgTime(at.UTC()), ResolutionCode: pgText(code),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			current, ok, gerr := d.IncidentByID(ctx, id)
			if gerr != nil {
				return operations.Incident{}, false, gerr
			}
			if !ok {
				return operations.Incident{}, false, nil
			}
			return current, false, nil
		}
		return operations.Incident{}, false, apperr.Wrap(apperr.Internal, "resolve incident", redact(err))
	}
	return toOpsIncident(row), true, nil
}

// MaxEpisode returns the highest episode for rule+subject (recurrence).
func (d Devices) MaxEpisode(ctx context.Context, rule, subjectType, subjectID string) (int, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	n, err := sqlcgen.New(d.pool).MaxEpisodeForSubject(ctx, sqlcgen.MaxEpisodeForSubjectParams{
		RuleKey: rule, SubjectType: subjectType, SubjectID: subjectID,
	})
	if err != nil {
		return 0, apperr.Wrap(apperr.Internal, "max episode", redact(err))
	}
	return int(n), nil
}

// ListPage returns one bounded incident page (keyset on opened_at,id).
func (d Devices) ListPage(ctx context.Context, state, severity, rule string, cursorAt *time.Time, cursorID string, limit int) ([]operations.Incident, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	var at pgtype.Timestamptz
	var cid pgtype.UUID
	if cursorAt != nil {
		at = pgTime(cursorAt.UTC())
		uid, err := opsUUID(cursorID)
		if err != nil {
			return nil, apperr.New(apperr.InvalidInput, "invalid page cursor")
		}
		cid = uid
	}
	// Empty-string filter convention matches the query's ($n = '') guards.
	rows, err := sqlcgen.New(d.pool).ListIncidentsPage(ctx, sqlcgen.ListIncidentsPageParams{
		Column1: state, Column2: severity, Column3: rule,
		Column4: at, Column5: cid, Limit: int32(limit),
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "list incidents", redact(err))
	}
	out := make([]operations.Incident, 0, len(rows))
	for _, r := range rows {
		out = append(out, toOpsIncident(r))
	}
	return out, nil
}

// DeviceSummary aggregates active device incidents in one query.
func (d Devices) DeviceSummary(ctx context.Context) ([]operations.DeviceSummary, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).CountActiveIncidentsByDevice(ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "device summary", redact(err))
	}
	out := make([]operations.DeviceSummary, 0, len(rows))
	for _, r := range rows {
		sev := operations.SeverityWarning
		if r.MaxSeverity == 2 {
			sev = operations.SeverityUrgent
		}
		out = append(out, operations.DeviceSummary{
			DeviceID: r.DeviceID, OpenCount: int(r.OpenCount), MaxSeverity: sev,
		})
	}
	return out, nil
}

// CreateDelivery persists one delivery snapshot (pending).
func (d Devices) CreateDelivery(ctx context.Context, del operations.Delivery, at time.Time) (operations.Delivery, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(del.ID)
	if err != nil {
		return operations.Delivery{}, apperr.New(apperr.InvalidInput, "delivery id must be a UUID")
	}
	iid, err := opsUUID(del.IncidentID)
	if err != nil {
		return operations.Delivery{}, apperr.New(apperr.InvalidInput, "incident id must be a UUID")
	}
	rid, err := opsUUID(del.RecipientID)
	if err != nil {
		return operations.Delivery{}, apperr.New(apperr.InvalidInput, "recipient id must be a UUID")
	}
	row, err := sqlcgen.New(d.pool).CreateAlertDelivery(ctx, sqlcgen.CreateAlertDeliveryParams{
		ID: uid, IncidentID: iid, RecipientID: rid, EventType: del.Event,
		ProviderKeySnapshot: del.ProviderKey, RecipientSnapshot: del.RecipientSnapshot,
		LocaleSnapshot: del.Locale, TemplateKey: del.TemplateKey, BodySnapshot: del.Body,
		BodyFingerprint: del.Fingerprint, NotificationIdempotencyKey: del.NotificationKey,
		CreatedAt: pgTime(at.UTC()),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return operations.Delivery{}, apperr.New(apperr.Conflict, "delivery already exists")
		}
		return operations.Delivery{}, apperr.Wrap(apperr.Internal, "create delivery", redact(err))
	}
	return toOpsDelivery(row), nil
}

// ClaimDelivery takes the oldest pending delivery (multi-instance safe).
func (d Devices) ClaimDelivery(ctx context.Context) (operations.Delivery, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return operations.Delivery{}, false, apperr.Wrap(apperr.Internal, "claim delivery", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := sqlcgen.New(tx).ClaimAlertDelivery(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return operations.Delivery{}, false, nil
		}
		return operations.Delivery{}, false, apperr.Wrap(apperr.Internal, "claim delivery", redact(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return operations.Delivery{}, false, apperr.Wrap(apperr.Internal, "claim delivery", redact(err))
	}
	return toOpsDelivery(row), true, nil
}

// FinishDeliverySent records durable enqueue with the adopted ID.
func (d Devices) FinishOpsDeliverySent(ctx context.Context, id, notificationID string, at time.Time) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(id)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "delivery id must be a UUID")
	}
	nid, err := opsUUID(notificationID)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "notification id must be a UUID")
	}
	n, err := sqlcgen.New(d.pool).FinishAlertDeliverySent(ctx, sqlcgen.FinishAlertDeliverySentParams{
		ID: uid, NotificationID: nid, UpdatedAt: pgTime(at.UTC()),
	})
	if err != nil {
		return apperr.Wrap(apperr.Internal, "finish delivery", redact(err))
	}
	if n != 1 {
		return apperr.Wrap(apperr.Internal, "finish delivery", errors.New("delivery already finished"))
	}
	return nil
}

// FinishDeliveryBlocked records a terminally blocked delivery.
func (d Devices) FinishOpsDeliveryBlocked(ctx context.Context, id, code string, at time.Time) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(id)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "delivery id must be a UUID")
	}
	n, err := sqlcgen.New(d.pool).FinishAlertDeliveryBlocked(ctx, sqlcgen.FinishAlertDeliveryBlockedParams{
		ID: uid, LastErrorCode: pgText(code), UpdatedAt: pgTime(at.UTC()),
	})
	if err != nil {
		return apperr.Wrap(apperr.Internal, "finish delivery", redact(err))
	}
	if n != 1 {
		return apperr.Wrap(apperr.Internal, "finish delivery", errors.New("delivery already finished"))
	}
	return nil
}

// DeliveriesForIncident lists deliveries with live notification state.
func (d Devices) DeliveriesForIncident(ctx context.Context, incidentID string) ([]operations.Delivery, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(incidentID)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "incident id must be a UUID")
	}
	rows, err := sqlcgen.New(d.pool).ListDeliveriesForIncident(ctx, uid)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "list deliveries", redact(err))
	}
	out := make([]operations.Delivery, 0, len(rows))
	for _, r := range rows {
		del := toOpsDelivery(sqlcgen.OperationalAlertDelivery{
			ID: r.ID, IncidentID: r.IncidentID, RecipientID: r.RecipientID,
			EventType: r.EventType, ProviderKeySnapshot: r.ProviderKeySnapshot,
			RecipientSnapshot: r.RecipientSnapshot, LocaleSnapshot: r.LocaleSnapshot,
			TemplateKey: r.TemplateKey, BodySnapshot: r.BodySnapshot,
			BodyFingerprint: r.BodyFingerprint, NotificationID: r.NotificationID,
			NotificationIdempotencyKey: r.NotificationIdempotencyKey,
			Status:                     r.Status, LastErrorCode: r.LastErrorCode,
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		})
		if r.NotificationDispatch.Valid {
			s := r.NotificationDispatch.String
			del.NotificationDispatch = &s
		}
		if r.NotificationDelivery.Valid {
			s := r.NotificationDelivery.String
			del.NotificationDelivery = &s
		}
		out = append(out, del)
	}
	return out, nil
}

// CreateRecovery inserts or adopts the one recovery action per incident.
func (d Devices) CreateRecovery(ctx context.Context, incidentID, idempotencyKey string, at time.Time) (operations.Recovery, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	iid, err := opsUUID(incidentID)
	if err != nil {
		return operations.Recovery{}, false, apperr.New(apperr.InvalidInput, "incident id must be a UUID")
	}
	rid, rerr := opsUUID(ids.System{}.New())
	if rerr != nil {
		return operations.Recovery{}, false, apperr.New(apperr.Internal, "recovery id")
	}
	row, err := sqlcgen.New(d.pool).CreateRecoveryAction(ctx, sqlcgen.CreateRecoveryActionParams{
		ID: rid, IncidentID: iid, ActionType: operations.ActionReconnectSync,
		IdempotencyKey: idempotencyKey, CreatedAt: pgTime(at.UTC()),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			existing, ok, gerr := d.RecoveryForIncident(ctx, incidentID)
			if gerr != nil {
				return operations.Recovery{}, false, gerr
			}
			if !ok {
				return operations.Recovery{}, false, apperr.Wrap(apperr.Internal, "create recovery", errors.New("no action after conflict"))
			}
			return existing, false, nil
		}
		return operations.Recovery{}, false, apperr.Wrap(apperr.Internal, "create recovery", redact(err))
	}
	return toOpsRecovery(row), true, nil
}

// RecoveryForIncident loads the incident's reconnect action, if any.
func (d Devices) RecoveryForIncident(ctx context.Context, incidentID string) (operations.Recovery, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	iid, err := opsUUID(incidentID)
	if err != nil {
		return operations.Recovery{}, false, apperr.New(apperr.InvalidInput, "incident id must be a UUID")
	}
	row, err := sqlcgen.New(d.pool).GetRecoveryAction(ctx, sqlcgen.GetRecoveryActionParams{
		IncidentID: iid, ActionType: operations.ActionReconnectSync,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return operations.Recovery{}, false, nil
		}
		return operations.Recovery{}, false, apperr.Wrap(apperr.Internal, "load recovery", redact(err))
	}
	return toOpsRecovery(row), true, nil
}

// ClaimRecovery takes one due pending action (multi-instance safe).
func (d Devices) ClaimRecovery(ctx context.Context, now time.Time) (operations.Recovery, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return operations.Recovery{}, false, apperr.Wrap(apperr.Internal, "claim recovery", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := sqlcgen.New(tx).ClaimRecoveryAction(ctx, pgTime(now.UTC()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return operations.Recovery{}, false, nil
		}
		return operations.Recovery{}, false, apperr.Wrap(apperr.Internal, "claim recovery", redact(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return operations.Recovery{}, false, apperr.Wrap(apperr.Internal, "claim recovery", redact(err))
	}
	return toOpsRecovery(row), true, nil
}

// FinishRecoveryCompleted records a queued-or-satisfied recovery.
func (d Devices) FinishRecoveryCompleted(ctx context.Context, id, target, result string, at time.Time) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(id)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "recovery id must be a UUID")
	}
	n, err := sqlcgen.New(d.pool).FinishRecoveryCompleted(ctx, sqlcgen.FinishRecoveryCompletedParams{
		ID: uid, TargetEntityID: pgText(target), ResultCode: pgText(result), UpdatedAt: pgTime(at.UTC()),
	})
	if err != nil {
		return apperr.Wrap(apperr.Internal, "finish recovery", redact(err))
	}
	if n != 1 {
		return apperr.Wrap(apperr.Internal, "finish recovery", errors.New("action already finished"))
	}
	return nil
}

// RetryRecoveryLater backs off a transiently failed action.
func (d Devices) RetryRecoveryLater(ctx context.Context, id, code string, next, at time.Time) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(id)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "recovery id must be a UUID")
	}
	n, err := sqlcgen.New(d.pool).RetryRecoveryLater(ctx, sqlcgen.RetryRecoveryLaterParams{
		ID: uid, NextAttemptAt: pgTime(next.UTC()), LastErrorCode: pgText(code), UpdatedAt: pgTime(at.UTC()),
	})
	if err != nil {
		return apperr.Wrap(apperr.Internal, "retry recovery", redact(err))
	}
	if n != 1 {
		return apperr.Wrap(apperr.Internal, "retry recovery", errors.New("action already finished"))
	}
	return nil
}

// FinishRecoveryBlocked terminally blocks a recovery action.
func (d Devices) FinishRecoveryBlocked(ctx context.Context, id, code string, at time.Time) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(id)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "recovery id must be a UUID")
	}
	n, err := sqlcgen.New(d.pool).FinishRecoveryBlocked(ctx, sqlcgen.FinishRecoveryBlockedParams{
		ID: uid, LastErrorCode: pgText(code), UpdatedAt: pgTime(at.UTC()),
	})
	if err != nil {
		return apperr.Wrap(apperr.Internal, "finish recovery", redact(err))
	}
	if n != 1 {
		return apperr.Wrap(apperr.Internal, "finish recovery", errors.New("action already finished"))
	}
	return nil
}

// RecoveriesForIncident lists an incident's recovery actions.
func (d Devices) RecoveriesForIncident(ctx context.Context, incidentID string) ([]operations.Recovery, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := opsUUID(incidentID)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "incident id must be a UUID")
	}
	rows, err := sqlcgen.New(d.pool).ListRecoveryForIncident(ctx, uid)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "list recovery", redact(err))
	}
	out := make([]operations.Recovery, 0, len(rows))
	for _, r := range rows {
		out = append(out, toOpsRecovery(r))
	}
	return out, nil
}

func toOpsRecovery(r sqlcgen.OperationalRecoveryAction) operations.Recovery {
	rec := operations.Recovery{
		ID: uuidString(r.ID), IncidentID: uuidString(r.IncidentID),
		ActionType: r.ActionType, State: r.State, IdempotencyKey: r.IdempotencyKey,
		AttemptCount: int(r.AttemptCount),
	}
	if r.TargetEntityID.Valid {
		s := r.TargetEntityID.String
		rec.TargetEntityID = &s
	}
	if r.ResultCode.Valid {
		s := r.ResultCode.String
		rec.ResultCode = &s
	}
	if r.NextAttemptAt.Valid {
		t := r.NextAttemptAt.Time.UTC()
		rec.NextAttemptAt = &t
	}
	if r.LastErrorCode.Valid {
		s := r.LastErrorCode.String
		rec.LastErrorCode = &s
	}
	return rec
}

// ActiveByRule lists open/acknowledged incidents for one rule (bounded,
// oldest first) for resolution checks.
func (d Devices) ActiveByRule(ctx context.Context, rule string, limit int) ([]operations.Incident, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ListActiveByRule(ctx, sqlcgen.ListActiveByRuleParams{
		RuleKey: rule, Limit: opsLimit(limit),
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "list active incidents", redact(err))
	}
	out := make([]operations.Incident, 0, len(rows))
	for _, r := range rows {
		out = append(out, toOpsIncident(r))
	}
	return out, nil
}
