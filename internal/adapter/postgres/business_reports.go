package postgres

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/businessreports"
)

// Scheduled business-report recipients, schedules, runs, and
// deliveries live on Devices (shared pool + timeouts). They are
// Cloud-only durable business state: no catalog/policy/inventory/
// order/sale rebuild path touches these tables.

func businessReportID() pgtype.UUID {
	id, err := parseUUID(ids.System{}.New())
	if err != nil {
		panic("report id must be a UUID")
	}
	return id
}

func businessReportDate(value string) pgtype.Date {
	var parsed pgtype.Date
	if err := parsed.Scan(value); err != nil {
		return pgtype.Date{}
	}
	return parsed
}

func businessReportDateString(date pgtype.Date) string {
	if !date.Valid {
		return ""
	}
	return date.Time.Format("2006-01-02")
}

// CreateRecipient stores one enabled report destination.
func (d Devices) CreateRecipient(ctx context.Context, id, label, providerKey, recipient, locale string, enabled bool) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	parsed, err := parseUUID(id)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "invalid recipient id")
	}
	if _, err := sqlcgen.New(d.pool).CreateRecipient(ctx, sqlcgen.CreateRecipientParams{
		ID: parsed, Label: label, ProviderKey: providerKey,
		Recipient: recipient, Locale: locale, Enabled: enabled,
	}); err != nil {
		if isUniqueViolation(err) {
			return apperr.New(apperr.Conflict, "report recipient already exists")
		}
		return apperr.Wrap(apperr.Internal, "report recipient", redact(err))
	}
	return nil
}

// GetRecipient resolves one recipient or reports found=false.
func (d Devices) GetRecipient(ctx context.Context, id string) (businessreports.Recipient, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	parsed, err := parseUUID(id)
	if err != nil {
		return businessreports.Recipient{}, false, apperr.New(apperr.InvalidInput, "invalid recipient id")
	}
	row, err := sqlcgen.New(d.pool).GetRecipient(ctx, parsed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return businessreports.Recipient{}, false, nil
		}
		return businessreports.Recipient{}, false, apperr.Wrap(apperr.Internal, "report recipient", redact(err))
	}
	return businessreports.Recipient{
		ID: uuidString(row.ID), Label: row.Label, ProviderKey: row.ProviderKey,
		Recipient: row.Recipient, Locale: row.Locale, Enabled: row.Enabled,
	}, true, nil
}

// ListRecipients returns configured destinations in creation order.
func (d Devices) ListRecipients(ctx context.Context) ([]businessreports.Recipient, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ListRecipients(ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "report recipient", redact(err))
	}
	recipients := make([]businessreports.Recipient, 0, len(rows))
	for _, row := range rows {
		recipients = append(recipients, businessreports.Recipient{
			ID: uuidString(row.ID), Label: row.Label, ProviderKey: row.ProviderKey,
			Recipient: row.Recipient, Locale: row.Locale, Enabled: row.Enabled,
		})
	}
	return recipients, nil
}

// SetRecipientEnabled toggles one destination. Materialized runs keep
// their snapshots; only future runs observe the change.
func (d Devices) SetRecipientEnabled(ctx context.Context, id string, enabled bool) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	parsed, err := parseUUID(id)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "invalid recipient id")
	}
	if err := sqlcgen.New(d.pool).SetRecipientEnabled(ctx, sqlcgen.SetRecipientEnabledParams{
		ID: parsed, Enabled: enabled,
	}); err != nil {
		return apperr.Wrap(apperr.Internal, "report recipient", redact(err))
	}
	return nil
}

func mapScheduleFields(id pgtype.UUID, name, kind, timezone, localTime string, anchor pgtype.Date,
	enabled bool, revision int64, nextDate pgtype.Date, nextAt pgtype.Timestamptz) businessreports.Schedule {
	var anchorDate string
	if anchor.Valid {
		anchorDate = anchor.Time.Format("2006-01-02")
	}
	return businessreports.Schedule{
		ID: uuidString(id), Name: name,
		Kind: businessreports.ReportKind(kind), Timezone: timezone,
		LocalTime: localTime, AnchorLocalDate: anchorDate, Enabled: enabled,
		Revision: revision, NextRunLocalDate: businessReportDateString(nextDate),
		NextRunAt: nextAt.Time,
	}
}

// CreateSchedule stores one cadence. The schedule row type name below
// follows sqlc generation for business_report_schedules.
func (d Devices) CreateSchedule(ctx context.Context, schedule businessreports.Schedule) error {
	return d.CreateScheduleWithRecipients(ctx, schedule, nil)
}

// CreateScheduleWithRecipients validates recipient rows under lock and
// persists the schedule plus every recipient link in ONE transaction:
// any failure rolls back schedule, links, and the unique name claim.
// Recipient rows are locked so a concurrent disable commits to one
// deterministic outcome.
func (d Devices) CreateScheduleWithRecipients(ctx context.Context, schedule businessreports.Schedule, recipientIDs []string) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	parsed, err := parseUUID(schedule.ID)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "invalid schedule id")
	}
	var anchor pgtype.Date
	if schedule.AnchorLocalDate != "" {
		anchor = businessReportDate(schedule.AnchorLocalDate)
		if !anchor.Valid {
			return apperr.New(apperr.InvalidInput, "invalid anchor date")
		}
	}
	nextDate := businessReportDate(schedule.NextRunLocalDate)
	if !nextDate.Valid {
		return apperr.New(apperr.InvalidInput, "invalid next run date")
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return apperr.Wrap(apperr.Internal, "report schedule", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	// Validate every recipient under row lock first: existence and
	// enabled state are transaction-consistent, so a concurrent
	// disable yields one deterministic outcome, never a link to an
	// invalid recipient.
	locked := make([]pgtype.UUID, 0, len(recipientIDs))
	for _, recipientID := range recipientIDs {
		recipient, err := parseUUID(recipientID)
		if err != nil {
			return apperr.New(apperr.InvalidInput, "invalid recipient id")
		}
		row, err := q.GetRecipientForUpdate(ctx, recipient)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apperr.New(apperr.InvalidInput, "schedule recipient must exist and be enabled")
			}
			return apperr.Wrap(apperr.Internal, "report schedule", redact(err))
		}
		if !row.Enabled {
			return apperr.New(apperr.InvalidInput, "schedule recipient must exist and be enabled")
		}
		locked = append(locked, recipient)
	}
	if err := q.CreateSchedule(ctx, sqlcgen.CreateScheduleParams{
		ID: parsed, Name: schedule.Name, ReportKind: string(schedule.Kind),
		Timezone: schedule.Timezone, LocalTime: schedule.LocalTime,
		AnchorLocalDate: anchor, NextRunLocalDate: nextDate,
		NextRunAt: pgTime(schedule.NextRunAt),
	}); err != nil {
		if isUniqueViolation(err) {
			return apperr.New(apperr.Conflict, "report schedule already exists")
		}
		return apperr.Wrap(apperr.Internal, "report schedule", redact(err))
	}
	for _, recipient := range locked {
		if err := q.LinkScheduleRecipient(ctx, sqlcgen.LinkScheduleRecipientParams{
			ScheduleID: parsed, RecipientID: recipient,
		}); err != nil {
			return apperr.Wrap(apperr.Internal, "report schedule", redact(err))
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return apperr.Wrap(apperr.Internal, "report schedule", redact(err))
	}
	return nil
}

// GetSchedule resolves one schedule or reports found=false.
func (d Devices) GetSchedule(ctx context.Context, id string) (businessreports.Schedule, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	parsed, err := parseUUID(id)
	if err != nil {
		return businessreports.Schedule{}, false, apperr.New(apperr.InvalidInput, "invalid schedule id")
	}
	row, err := sqlcgen.New(d.pool).GetSchedule(ctx, parsed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return businessreports.Schedule{}, false, nil
		}
		return businessreports.Schedule{}, false, apperr.Wrap(apperr.Internal, "report schedule", redact(err))
	}
	return mapScheduleFields(row.ID, row.Name, row.ReportKind, row.Timezone, row.LocalTime,
		row.AnchorLocalDate, row.Enabled, row.Revision,
		row.NextRunLocalDate, row.NextRunAt), true, nil
}

// ListSchedules returns configured cadences in creation order.
func (d Devices) ListSchedules(ctx context.Context) ([]businessreports.Schedule, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ListSchedules(ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "report schedule", redact(err))
	}
	schedules := make([]businessreports.Schedule, 0, len(rows))
	for _, row := range rows {
		schedules = append(schedules, mapScheduleFields(row.ID, row.Name, row.ReportKind,
			row.Timezone, row.LocalTime, row.AnchorLocalDate, row.Enabled, row.Revision,
			row.NextRunLocalDate, row.NextRunAt))
	}
	return schedules, nil
}

// UpdateScheduleEnablement flips one schedule with a revision bump and
// an explicitly computed next slot. Disabling keeps the cursor (never
// due while disabled); enabling positions the first future slot.
func (d Devices) UpdateScheduleEnablement(ctx context.Context, id string, enabled bool, revision int64, nextDate string, nextAt time.Time) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	parsed, err := parseUUID(id)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "invalid schedule id")
	}
	date := businessReportDate(nextDate)
	if !date.Valid {
		return apperr.New(apperr.InvalidInput, "invalid next run date")
	}
	if err := sqlcgen.New(d.pool).UpdateScheduleEnablement(ctx, sqlcgen.UpdateScheduleEnablementParams{
		ID: parsed, Enabled: enabled,
		NextRunLocalDate: date, NextRunAt: pgTime(nextAt),
	}); err != nil {
		return apperr.Wrap(apperr.Internal, "report schedule", redact(err))
	}
	return nil
}

// EnableScheduleIfDisabled performs the disabled→enabled transition
// atomically: exactly one concurrent caller wins; losers re-read the
// winner's state. Returns applied=false when already enabled.
func (d Devices) EnableScheduleIfDisabled(ctx context.Context, id string, nextDate string, nextAt time.Time) (businessreports.Schedule, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	parsed, err := parseUUID(id)
	if err != nil {
		return businessreports.Schedule{}, false, apperr.New(apperr.InvalidInput, "invalid schedule id")
	}
	date := businessReportDate(nextDate)
	if !date.Valid {
		return businessreports.Schedule{}, false, apperr.New(apperr.InvalidInput, "invalid next run date")
	}
	row, err := sqlcgen.New(d.pool).EnableScheduleIfDisabled(ctx, sqlcgen.EnableScheduleIfDisabledParams{
		ID: parsed, NextRunLocalDate: date, NextRunAt: pgTime(nextAt),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return businessreports.Schedule{}, false, nil
		}
		return businessreports.Schedule{}, false, apperr.Wrap(apperr.Internal, "report schedule", redact(err))
	}
	return mapScheduleFields(row.ID, row.Name, row.ReportKind, row.Timezone, row.LocalTime,
		row.AnchorLocalDate, row.Enabled, row.Revision,
		row.NextRunLocalDate, row.NextRunAt), true, nil
}

// LinkScheduleRecipient attaches one recipient to a schedule.
// Idempotent: duplicates are no-ops, never errors.
func (d Devices) LinkScheduleRecipient(ctx context.Context, scheduleID, recipientID string) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	schedule, err := parseUUID(scheduleID)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "invalid schedule id")
	}
	recipient, err := parseUUID(recipientID)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "invalid recipient id")
	}
	if err := sqlcgen.New(d.pool).LinkScheduleRecipient(ctx, sqlcgen.LinkScheduleRecipientParams{
		ScheduleID: schedule, RecipientID: recipient,
	}); err != nil {
		return apperr.Wrap(apperr.Internal, "report schedule", redact(err))
	}
	return nil
}

// ListScheduleRecipients returns linked recipients in creation order.
func (d Devices) ListScheduleRecipients(ctx context.Context, scheduleID string) ([]businessreports.Recipient, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	parsed, err := parseUUID(scheduleID)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "invalid schedule id")
	}
	rows, err := sqlcgen.New(d.pool).ListScheduleRecipients(ctx, parsed)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "report schedule", redact(err))
	}
	recipients := make([]businessreports.Recipient, 0, len(rows))
	for _, row := range rows {
		recipients = append(recipients, businessreports.Recipient{
			ID: uuidString(row.ID), Label: row.Label, ProviderKey: row.ProviderKey,
			Recipient: row.Recipient, Locale: row.Locale, Enabled: row.Enabled,
		})
	}
	return recipients, nil
}

func mapRunFields(id pgtype.UUID, scheduleID pgtype.UUID, kind string, slotDate pgtype.Date,
	manualKey pgtype.Text, scheduledFor pgtype.Timestamptz, periodStart, periodEnd pgtype.Timestamptz,
	revision int64, status string, attempts int32, owner pgtype.Text, generation int64,
	code pgtype.Text) businessreports.Run {
	var slot, manual string
	if slotDate.Valid {
		slot = slotDate.Time.Format("2006-01-02")
	}
	if manualKey.Valid {
		manual = manualKey.String
	}
	return businessreports.Run{
		ID: uuidString(id), ScheduleID: uuidString(scheduleID),
		Kind: businessreports.RunKind(kind), SlotLocalDate: slot,
		ManualIdempotencyKey: manual, ScheduledFor: scheduledFor.Time,
		PeriodStart: periodStart.Time, PeriodEnd: periodEnd.Time,
		ScheduleRevision: revision, Status: businessreports.RunStatus(status),
		AttemptCount: attempts, LeaseOwner: owner.String, LeaseGeneration: generation,
		LastErrorCode: code.String,
	}
}

func mapDeliveryFields(id, runID pgtype.UUID, recipientID pgtype.UUID, providerKey, recipient,
	locale, templateKey string, body pgtype.Text, fingerprint []byte,
	idempotencyKey string, notificationID pgtype.UUID, status, code string) businessreports.Delivery {
	var notification string
	if notificationID.Valid {
		notification = uuidString(notificationID)
	}
	delivery := businessreports.Delivery{
		ID: uuidString(id), RunID: uuidString(runID), RecipientID: uuidString(recipientID),
		ProviderKey: providerKey, Recipient: recipient, Locale: locale,
		TemplateKey:                templateKey,
		NotificationIdempotencyKey: idempotencyKey, NotificationID: notification,
		Status: businessreports.DeliveryStatus(status), LastErrorCode: code,
	}
	if body.Valid {
		delivery.Body = body.String
		delivery.HasBody = true
		delivery.Fingerprint = append([]byte(nil), fingerprint...)
	}
	return delivery
}

// CreateManualRun inserts one manual run with its recipient snapshot
// deliveries atomically. Same key + same semantics returns the
// existing run; changed semantics conflict without overwriting.
func (d Devices) CreateManualRun(ctx context.Context, run businessreports.Run, deliveries []businessreports.Delivery) (string, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return "", false, apperr.Wrap(apperr.Internal, "report run", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	runID, err := parseUUID(run.ID)
	if err != nil {
		return "", false, apperr.New(apperr.InvalidInput, "invalid run id")
	}
	scheduleID, err := parseUUID(run.ScheduleID)
	if err != nil {
		return "", false, apperr.New(apperr.InvalidInput, "invalid schedule id")
	}
	returned, err := q.InsertManualRun(ctx, sqlcgen.InsertManualRunParams{
		ID: runID, ScheduleID: scheduleID, ManualIdempotencyKey: pgText(run.ManualIdempotencyKey),
		ScheduledFor: pgTime(run.ScheduledFor),
		PeriodStart:  pgTime(run.PeriodStart), PeriodEnd: pgTime(run.PeriodEnd),
		ScheduleRevision: run.ScheduleRevision,
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", false, apperr.Wrap(apperr.Internal, "report run", redact(err))
	}
	if err == nil {
		for _, delivery := range deliveries {
			if err := insertDelivery(ctx, q, uuidString(returned), delivery); err != nil {
				return "", false, err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return "", false, apperr.Wrap(apperr.Internal, "report run", redact(err))
		}
		return uuidString(returned), true, nil
	}
	// Idempotency replay: compare stored semantics before returning.
	existing, err := q.GetManualRun(ctx, sqlcgen.GetManualRunParams{
		ScheduleID: scheduleID, ManualIdempotencyKey: pgText(run.ManualIdempotencyKey),
	})
	if err != nil {
		return "", false, apperr.Wrap(apperr.Internal, "report run", redact(err))
	}
	if !existing.PeriodStart.Time.Equal(run.PeriodStart) || !existing.PeriodEnd.Time.Equal(run.PeriodEnd) {
		_ = tx.Rollback(ctx)
		return "", false, apperr.New(apperr.Conflict, "manual run key already used with different period")
	}
	stored, err := q.ListRunDeliveries(ctx, existing.ID)
	if err != nil {
		return "", false, apperr.Wrap(apperr.Internal, "report run", redact(err))
	}
	if len(stored) != len(deliveries) {
		_ = tx.Rollback(ctx)
		return "", false, apperr.New(apperr.Conflict, "manual run key already used with different recipients")
	}
	want := map[string]string{}
	for _, delivery := range deliveries {
		// Full delivery semantics: recipient identity, address,
		// provider, locale, and template. Labels and timestamps are
		// not semantics and never conflict.
		want[delivery.RecipientID] = strings.Join([]string{
			delivery.Recipient, delivery.ProviderKey, delivery.Locale, delivery.TemplateKey,
		}, "\x00")
	}
	for _, row := range stored {
		got, ok := want[uuidString(row.RecipientID)]
		if !ok || got != strings.Join([]string{
			row.RecipientSnapshot, row.ProviderKey, row.LocaleSnapshot, row.TemplateKey,
		}, "\x00") {
			_ = tx.Rollback(ctx)
			return "", false, apperr.New(apperr.Conflict, "manual run key already used with different recipients")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, apperr.Wrap(apperr.Internal, "report run", redact(err))
	}
	return uuidString(existing.ID), false, nil
}

// insertDelivery persists one delivery row inside the caller's
// transaction. Idempotent per (run, recipient).
func insertDelivery(ctx context.Context, q *sqlcgen.Queries, runID string, delivery businessreports.Delivery) error {
	deliveryID, err := parseUUID(delivery.ID)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "invalid delivery id")
	}
	parsedRun, err := parseUUID(runID)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "invalid run id")
	}
	recipientID, err := parseUUID(delivery.RecipientID)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "invalid recipient id")
	}
	if err := q.InsertDelivery(ctx, sqlcgen.InsertDeliveryParams{
		ID: deliveryID, RunID: parsedRun, RecipientID: recipientID,
		ProviderKey: delivery.ProviderKey, RecipientSnapshot: delivery.Recipient,
		LocaleSnapshot: delivery.Locale, TemplateKey: delivery.TemplateKey,
		NotificationIdempotencyKey: delivery.NotificationIdempotencyKey,
	}); err != nil {
		return apperr.Wrap(apperr.Internal, "report delivery", redact(err))
	}
	return nil
}

// ClaimDueSchedules lists enabled schedules due at or before now,
// oldest first, skipping rows locked by in-flight planner
// transactions on other instances.
func (d Devices) ClaimDueSchedules(ctx context.Context, limit int32) ([]businessreports.Schedule, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := sqlcgen.New(d.pool).ClaimDueSchedules(ctx, limit)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "report schedule", redact(err))
	}
	schedules := make([]businessreports.Schedule, 0, len(rows))
	for _, row := range rows {
		schedules = append(schedules, mapScheduleFields(row.ID, row.Name, row.ReportKind,
			row.Timezone, row.LocalTime, row.AnchorLocalDate, row.Enabled, row.Revision,
			row.NextRunLocalDate, row.NextRunAt))
	}
	return schedules, nil
}

// MaterializeNextSlot atomically materializes one schedule's stored
// next slot: lock row, verify due+enabled, compute slot/period, insert
// run (conflict-safe), snapshot enabled recipients into deliveries,
// advance next_run. Run creation and advancement commit together: a
// crash before commit leaves neither a run nor an advanced cursor,
// and a conflict means another instance already covered the slot.
func (d Devices) MaterializeNextSlot(ctx context.Context, scheduleID string, now time.Time, loc *time.Location) (businessreports.MaterializeOutcome, error) {
	none := businessreports.MaterializeOutcome{}
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	parsed, err := parseUUID(scheduleID)
	if err != nil {
		return none, apperr.New(apperr.InvalidInput, "invalid schedule id")
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return none, apperr.Wrap(apperr.Internal, "report schedule", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	row, err := q.GetScheduleForUpdate(ctx, parsed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return none, apperr.New(apperr.NotFound, "schedule not found")
		}
		return none, apperr.Wrap(apperr.Internal, "report schedule", redact(err))
	}
	if !row.Enabled || row.NextRunAt.Time.After(now) {
		_ = tx.Rollback(ctx)
		return none, nil
	}
	if loc == nil {
		return none, apperr.New(apperr.Internal, "report schedule: timezone not configured")
	}
	slotDate := businessReportDateString(row.NextRunLocalDate)
	plan, err := businessreports.PlanSlot(
		businessreports.ReportKind(row.ReportKind), businessReportDateString(row.AnchorLocalDate), slotDate, loc)
	if err != nil {
		return none, err
	}
	runID := businessReportID()
	_, err = q.InsertScheduledRun(ctx, sqlcgen.InsertScheduledRunParams{
		ID: runID, ScheduleID: parsed, SlotLocalDate: businessReportDate(slotDate),
		ScheduledFor: pgTime(row.NextRunAt.Time),
		PeriodStart:  pgTime(plan.PeriodStart), PeriodEnd: pgTime(plan.PeriodEnd),
		ScheduleRevision: row.Revision,
	})
	created := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return none, apperr.Wrap(apperr.Internal, "report run", redact(err))
	}
	if !created {
		// Another instance covered this slot: converge on its run,
		// then advance past the durably-covered slot below.
		existing, err := q.GetScheduledRun(ctx, sqlcgen.GetScheduledRunParams{
			ScheduleID: parsed, SlotLocalDate: businessReportDate(slotDate),
		})
		if err != nil {
			return none, apperr.Wrap(apperr.Internal, "report run", redact(err))
		}
		runID = existing.ID
	} else {
		recipients, err := q.ListScheduleRecipients(ctx, parsed)
		if err != nil {
			return none, apperr.Wrap(apperr.Internal, "report schedule", redact(err))
		}
		kind := businessreports.ReportKind(row.ReportKind)
		for _, recipient := range recipients {
			if !recipient.Enabled {
				continue
			}
			deliveryID := businessReportID()
			if err := q.InsertDelivery(ctx, sqlcgen.InsertDeliveryParams{
				ID: deliveryID, RunID: runID, RecipientID: recipient.ID,
				ProviderKey: recipient.ProviderKey, RecipientSnapshot: recipient.Recipient,
				LocaleSnapshot: recipient.Locale,
				TemplateKey:    businessreports.TemplateKeyForKind(kind),
				NotificationIdempotencyKey: businessreports.NotificationIdempotencyKey(
					uuidString(runID), uuidString(deliveryID)),
			}); err != nil {
				return none, apperr.Wrap(apperr.Internal, "report delivery", redact(err))
			}
		}
	}
	nextDate, err := businessreports.AdvanceSlotDate(
		businessreports.ReportKind(row.ReportKind), businessReportDateString(row.AnchorLocalDate), slotDate)
	if err != nil {
		return none, err
	}
	nextAt, err := businessreports.SlotInstant(nextDate, row.LocalTime, loc)
	if err != nil {
		return none, err
	}
	if err := q.AdvanceScheduleNextRun(ctx, sqlcgen.AdvanceScheduleNextRunParams{
		ID: parsed, NextRunLocalDate: businessReportDate(nextDate), NextRunAt: pgTime(nextAt),
	}); err != nil {
		return none, apperr.Wrap(apperr.Internal, "report schedule", redact(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return none, apperr.Wrap(apperr.Internal, "report schedule", redact(err))
	}
	return businessreports.MaterializeOutcome{
		Ran: true, RunID: uuidString(runID), Created: created,
		SlotDate: slotDate, Revision: row.Revision,
	}, nil
}

// GetRun resolves one run or reports found=false.
func (d Devices) GetRun(ctx context.Context, id string) (businessreports.Run, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	parsed, err := parseUUID(id)
	if err != nil {
		return businessreports.Run{}, false, apperr.New(apperr.InvalidInput, "invalid run id")
	}
	row, err := sqlcgen.New(d.pool).GetRun(ctx, parsed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return businessreports.Run{}, false, nil
		}
		return businessreports.Run{}, false, apperr.Wrap(apperr.Internal, "report run", redact(err))
	}
	return mapRunFields(row.ID, row.ScheduleID, row.RunKind, row.SlotLocalDate,
		row.ManualIdempotencyKey, row.ScheduledFor, row.PeriodStart, row.PeriodEnd,
		row.ScheduleRevision, row.Status, row.AttemptCount, row.LeaseOwner,
		row.LeaseGeneration, row.LastErrorCode), true, nil
}

// ListRuns returns recent runs, newest first.
func (d Devices) ListRuns(ctx context.Context, limit int32) ([]businessreports.Run, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := sqlcgen.New(d.pool).ListRuns(ctx, limit)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "report run", redact(err))
	}
	runs := make([]businessreports.Run, 0, len(rows))
	for _, row := range rows {
		runs = append(runs, mapRunFields(row.ID, row.ScheduleID, row.RunKind, row.SlotLocalDate,
			row.ManualIdempotencyKey, row.ScheduledFor, row.PeriodStart, row.PeriodEnd,
			row.ScheduleRevision, row.Status, row.AttemptCount, row.LeaseOwner,
			row.LeaseGeneration, row.LastErrorCode))
	}
	return runs, nil
}

// ListRunDeliveries returns one run's deliveries in creation order.
func (d Devices) ListRunDeliveries(ctx context.Context, runID string) ([]businessreports.Delivery, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	parsed, err := parseUUID(runID)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "invalid run id")
	}
	rows, err := sqlcgen.New(d.pool).ListRunDeliveries(ctx, parsed)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "report delivery", redact(err))
	}
	deliveries := make([]businessreports.Delivery, 0, len(rows))
	for _, row := range rows {
		deliveries = append(deliveries, mapDeliveryFields(row.ID, row.RunID, row.RecipientID,
			row.ProviderKey, row.RecipientSnapshot, row.LocaleSnapshot, row.TemplateKey,
			row.ReportBodySnapshot, row.ReportFingerprint, row.NotificationIdempotencyKey,
			row.NotificationID, row.Status, row.LastErrorCode.String))
	}
	return deliveries, nil
}

// ClaimRun leases one due run. Row-level SKIP LOCKED keeps
// multi-instance workers apart; the bounded lease (not a permanent
// state) makes crashed claims eligible again.
func (d Devices) ClaimRun(ctx context.Context, owner string, lease time.Duration, now time.Time) (businessreports.Run, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	leaseUntil := pgtype.Timestamptz{Time: now.Add(lease).UTC(), Valid: true}
	row, err := sqlcgen.New(d.pool).ClaimRun(ctx, sqlcgen.ClaimRunParams{
		LeaseOwner: pgText(owner), LeaseUntil: leaseUntil,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return businessreports.Run{}, false, nil
		}
		return businessreports.Run{}, false, apperr.Wrap(apperr.Internal, "report run", redact(err))
	}
	return mapRunFields(row.ID, row.ScheduleID, row.RunKind, row.SlotLocalDate,
		row.ManualIdempotencyKey, row.ScheduledFor, row.PeriodStart, row.PeriodEnd,
		row.ScheduleRevision, row.Status, row.AttemptCount, row.LeaseOwner,
		row.LeaseGeneration, pgtype.Text{}), true, nil
}

// FinishRunCompleted records full enqueue convergence.
func (d Devices) FinishRunCompleted(ctx context.Context, id, owner string, generation int64) (businessreports.RunFinish, error) {
	return d.fencedRunFinish(ctx, id, owner, generation, "complete",
		func(q *sqlcgen.Queries) (int64, error) {
			return q.FinishRunCompleted(ctx, sqlcgen.FinishRunCompletedParams{
				ID: mustReportID(id), LeaseOwner: pgText(owner), LeaseGeneration: generation,
			})
		})
}

// FinishRunRetry records a temporary failure with backoff metadata.
func (d Devices) FinishRunRetry(ctx context.Context, id, owner string, generation int64, next time.Time, code string) (businessreports.RunFinish, error) {
	return d.fencedRunFinish(ctx, id, owner, generation, "retry",
		func(q *sqlcgen.Queries) (int64, error) {
			return q.FinishRunRetry(ctx, sqlcgen.FinishRunRetryParams{
				ID: mustReportID(id), LeaseOwner: pgText(owner), LeaseGeneration: generation,
				NextAttemptAt: pgTime(next), LastErrorCode: pgText(code),
			})
		})
}

// FinishRunBlocked records a terminal run outcome.
func (d Devices) FinishRunBlocked(ctx context.Context, id, owner string, generation int64, code string) (businessreports.RunFinish, error) {
	return d.fencedRunFinish(ctx, id, owner, generation, "block",
		func(q *sqlcgen.Queries) (int64, error) {
			return q.FinishRunBlocked(ctx, sqlcgen.FinishRunBlockedParams{
				ID: mustReportID(id), LeaseOwner: pgText(owner), LeaseGeneration: generation,
				LastErrorCode: pgText(code),
			})
		})
}

// fencedRunFinish runs one fenced terminal transition against a
// non-terminal run. Stale claims affect zero rows: safe no-op.
func (d Devices) fencedRunFinish(ctx context.Context, id, owner string, generation int64, op string,
	exec func(q *sqlcgen.Queries) (int64, error)) (businessreports.RunFinish, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	affected, err := exec(sqlcgen.New(d.pool))
	if err != nil {
		return businessreports.RunFinishStale, apperr.Wrap(apperr.Internal, "report run "+op, redact(err))
	}
	if affected == 0 {
		return businessreports.RunFinishStale, nil
	}
	return businessreports.RunFinishApplied, nil
}

// PersistDeliverySnapshot stores the immutable localized body exactly
// once and only under the current run lease. Deterministic content
// makes first-writer-wins safe under owner races.
// lockRunForDeliveryMutation begins a short transaction, locks the
// parent run row first, and validates ownership against the
// post-lock read. Every worker-owned delivery mutation goes through
// here so run takeover can never race a delivery write: a concurrent
// claim either happens-before (we observe the new generation and
// refuse) or blocks behind our parent lock until we commit. Caller
// must Rollback on any non-nil error path via the deferred call and
// Commit explicitly. Time-based lease validity is enforced by the
// guarded UPDATE itself using clock_timestamp(), which evaluates
// after lock acquisition rather than at transaction start.
func (d Devices) lockRunForDeliveryMutation(ctx context.Context, runID, owner string, generation int64) (context.Context, context.CancelFunc, pgx.Tx, error) {
	ctx, cancel := d.ctx(ctx)
	run, err := parseUUID(runID)
	if err != nil {
		cancel()
		return nil, nil, nil, apperr.New(apperr.InvalidInput, "invalid run id")
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		cancel()
		return nil, nil, nil, apperr.Wrap(apperr.Internal, "report delivery", redact(err))
	}
	locked, err := sqlcgen.New(tx).GetRunForUpdate(ctx, run)
	if err != nil {
		_ = tx.Rollback(ctx)
		cancel()
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil, apperr.New(apperr.NotFound, "report run not found")
		}
		return nil, nil, nil, apperr.Wrap(apperr.Internal, "report delivery", redact(err))
	}
	if locked.LeaseOwner.String != owner || locked.LeaseGeneration != generation ||
		(locked.Status != "pending" && locked.Status != "retry") {
		_ = tx.Rollback(ctx)
		cancel()
		return nil, nil, nil, errStaleRunLease
	}
	return ctx, cancel, tx, nil
}

// errStaleRunLease is the sentinel for a lost ownership race. Callers
// map it to a stale/false result, never an error.
var errStaleRunLease = errors.New("stale run lease")

func (d Devices) PersistDeliverySnapshot(ctx context.Context, runID, deliveryID, owner string, generation int64, body string, fingerprint []byte) (bool, error) {
	ctx, cancel, tx, err := d.lockRunForDeliveryMutation(ctx, runID, owner, generation)
	if err != nil {
		if errors.Is(err, errStaleRunLease) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	defer cancel()
	parsed, err := parseUUID(deliveryID)
	if err != nil {
		return false, apperr.New(apperr.InvalidInput, "invalid delivery id")
	}
	q := sqlcgen.New(tx)
	// Acquire the child lock before the guarded write: the write then
	// never waits, so its predicates (including the clock_timestamp()
	// lease check) evaluate after all locks are held.
	if _, err := q.GetDeliveryForUpdate(ctx, parsed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return false, nil
		}
		return false, apperr.Wrap(apperr.Internal, "report delivery", redact(err))
	}
	affected, err := q.PersistDeliverySnapshot(ctx, sqlcgen.PersistDeliverySnapshotParams{
		ID: parsed, ReportBodySnapshot: pgText(body), ReportFingerprint: fingerprint,
		ID_2: mustReportID(runID), LeaseOwner: pgText(owner), LeaseGeneration: generation,
	})
	if err != nil {
		return false, apperr.Wrap(apperr.Internal, "report delivery", redact(err))
	}
	if affected != 1 {
		_ = tx.Rollback(ctx)
		return false, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return false, apperr.Wrap(apperr.Internal, "report delivery", redact(err))
	}
	return true, nil
}

// FinishDeliveryEnqueued marks one delivery handed to Phase 7A. The
// transition requires pending status plus the current run lease
// (owner, generation, unexpired) on a non-terminal run: stale owners
// affect zero rows and must treat that as no convergence.
func (d Devices) FinishDeliveryEnqueued(ctx context.Context, runID, deliveryID, owner string, generation int64, notificationID string) (bool, error) {
	ctx, cancel, tx, err := d.lockRunForDeliveryMutation(ctx, runID, owner, generation)
	if err != nil {
		if errors.Is(err, errStaleRunLease) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	defer cancel()
	parsed, err := parseUUID(deliveryID)
	if err != nil {
		return false, apperr.New(apperr.InvalidInput, "invalid delivery id")
	}
	notification, err := parseUUID(notificationID)
	if err != nil {
		return false, apperr.New(apperr.InvalidInput, "invalid notification id")
	}
	q := sqlcgen.New(tx)
	if _, err := q.GetDeliveryForUpdate(ctx, parsed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return false, nil
		}
		return false, apperr.Wrap(apperr.Internal, "report delivery", redact(err))
	}
	affected, err := q.FinishDeliveryEnqueued(ctx, sqlcgen.FinishDeliveryEnqueuedParams{
		ID: parsed, NotificationID: notification, ID_2: mustReportID(runID),
		LeaseOwner: pgText(owner), LeaseGeneration: generation,
	})
	if err != nil {
		return false, apperr.Wrap(apperr.Internal, "report delivery", redact(err))
	}
	if affected != 1 {
		_ = tx.Rollback(ctx)
		return false, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return false, apperr.Wrap(apperr.Internal, "report delivery", redact(err))
	}
	return true, nil
}

// FinishDeliveryBlocked marks one delivery permanently un-sendable.
// Same run-lease fencing as enqueued finishes: stale owners affect
// zero rows.
func (d Devices) FinishDeliveryBlocked(ctx context.Context, runID, deliveryID, owner string, generation int64, code string) (bool, error) {
	ctx, cancel, tx, err := d.lockRunForDeliveryMutation(ctx, runID, owner, generation)
	if err != nil {
		if errors.Is(err, errStaleRunLease) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	defer cancel()
	parsed, err := parseUUID(deliveryID)
	if err != nil {
		return false, apperr.New(apperr.InvalidInput, "invalid delivery id")
	}
	q := sqlcgen.New(tx)
	if _, err := q.GetDeliveryForUpdate(ctx, parsed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return false, nil
		}
		return false, apperr.Wrap(apperr.Internal, "report delivery", redact(err))
	}
	affected, err := q.FinishDeliveryBlocked(ctx, sqlcgen.FinishDeliveryBlockedParams{
		ID: parsed, LastErrorCode: pgText(code), ID_2: mustReportID(runID),
		LeaseOwner: pgText(owner), LeaseGeneration: generation,
	})
	if err != nil {
		return false, apperr.Wrap(apperr.Internal, "report delivery", redact(err))
	}
	if affected != 1 {
		_ = tx.Rollback(ctx)
		return false, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return false, apperr.Wrap(apperr.Internal, "report delivery", redact(err))
	}
	return true, nil
}

// PersistRunDeliverySnapshots persists a complete missing-snapshot set
// in ONE transaction under the current run lease: the run row is
// locked first and ownership verified, then every delivery snapshot
// persists conditionally. If any expected delivery already carries a
// body (partial pre-existing state), the whole barrier rolls back and
// reports incomplete: the runner blocks the run instead of mixing
// canonical bases.
func (d Devices) PersistRunDeliverySnapshots(ctx context.Context, runID, owner string, generation int64, snapshots map[string]businessreports.SnapshotBody) (bool, error) {
	ctx, cancel, tx, err := d.lockRunForDeliveryMutation(ctx, runID, owner, generation)
	if err != nil {
		if errors.Is(err, errStaleRunLease) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	defer cancel()
	q := sqlcgen.New(tx)
	run, err := parseUUID(runID)
	if err != nil {
		return false, apperr.New(apperr.InvalidInput, "invalid run id")
	}
	ids := make([]string, 0, len(snapshots))
	for deliveryID := range snapshots {
		ids = append(ids, deliveryID)
	}
	sort.Strings(ids)
	for _, deliveryID := range ids {
		snapshot := snapshots[deliveryID]
		parsed, err := parseUUID(deliveryID)
		if err != nil {
			return false, apperr.New(apperr.InvalidInput, "invalid delivery id")
		}
		if _, err := q.GetDeliveryForUpdate(ctx, parsed); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				_ = tx.Rollback(ctx)
				return false, nil
			}
			return false, apperr.Wrap(apperr.Internal, "report delivery", redact(err))
		}
		affected, err := q.PersistDeliverySnapshot(ctx, sqlcgen.PersistDeliverySnapshotParams{
			ID: parsed, ReportBodySnapshot: pgText(snapshot.Body), ReportFingerprint: snapshot.Fingerprint,
			ID_2: run, LeaseOwner: pgText(owner), LeaseGeneration: generation,
		})
		if err != nil {
			return false, apperr.Wrap(apperr.Internal, "report delivery", redact(err))
		}
		if affected != 1 {
			// Pre-existing body (partial legacy state or racing
			// owner): all-or-nothing rollback, runner blocks safely.
			_ = tx.Rollback(ctx)
			return false, nil
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, apperr.Wrap(apperr.Internal, "report delivery", redact(err))
	}
	return true, nil
}

// mustReportID converts a domain UUID string for fenced writes.
func mustReportID(id string) pgtype.UUID {
	converted, err := parseUUID(id)
	if err != nil {
		panic("report id must be a UUID")
	}
	return converted
}
