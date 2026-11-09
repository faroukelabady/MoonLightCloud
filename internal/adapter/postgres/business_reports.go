package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/businessreports"
)

// Scheduled business-report recipients, schedules, runs, and
// deliveries live on BusinessReports (shared pool + timeouts). They are
// Cloud-only durable business state: no catalog/policy/inventory/
// order/sale rebuild path touches these tables.

type BusinessReports struct {
	pool    *pgxpool.Pool
	timeout time.Duration
}

var _ businessreports.RecipientStore = BusinessReports{}
var _ businessreports.ScheduleStore = BusinessReports{}
var _ businessreports.SchedulePlannerStore = BusinessReports{}
var _ businessreports.RunStore = BusinessReports{}

// NewBusinessReports shares the application's pool without owning its lifecycle.
func NewBusinessReports(pool *pgxpool.Pool, timeout time.Duration) BusinessReports {
	return BusinessReports{pool: pool, timeout: timeout}
}

func (d BusinessReports) ctx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d.timeout)
}

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
func (d BusinessReports) CreateRecipient(ctx context.Context, id, label, providerKey, recipient, locale string, enabled bool) error {
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
func (d BusinessReports) GetRecipient(ctx context.Context, id string) (businessreports.Recipient, bool, error) {
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
func (d BusinessReports) ListRecipients(ctx context.Context) ([]businessreports.Recipient, error) {
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
func (d BusinessReports) SetRecipientEnabled(ctx context.Context, id string, enabled bool) error {
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
func (d BusinessReports) CreateSchedule(ctx context.Context, schedule businessreports.Schedule) error {
	return d.CreateScheduleWithRecipients(ctx, schedule, nil)
}

// CreateScheduleWithRecipients validates recipient rows under lock and
// persists the schedule plus every recipient link in ONE transaction:
// any failure rolls back schedule, links, and the unique name claim.
// Recipient rows are locked so a concurrent disable commits to one
// deterministic outcome.
func (d BusinessReports) CreateScheduleWithRecipients(ctx context.Context, schedule businessreports.Schedule, recipientIDs []string) error {
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
	locked, err := lockReportRecipients(ctx, q, recipientIDs)
	if err != nil {
		return err
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
func (d BusinessReports) GetSchedule(ctx context.Context, id string) (businessreports.Schedule, bool, error) {
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
	schedule := mapScheduleFields(row.ID, row.Name, row.ReportKind, row.Timezone, row.LocalTime,
		row.AnchorLocalDate, row.Enabled, row.Revision,
		row.NextRunLocalDate, row.NextRunAt)
	schedule.Deleted = row.Deleted
	return schedule, true, nil
}

// ListSchedules returns configured cadences in creation order.
func (d BusinessReports) ListSchedules(ctx context.Context) ([]businessreports.Schedule, error) {
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
func (d BusinessReports) UpdateScheduleEnablement(ctx context.Context, id string, enabled bool, revision int64, nextDate string, nextAt time.Time) error {
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
func (d BusinessReports) EnableScheduleIfDisabled(ctx context.Context, id string, nextDate string, nextAt time.Time) (businessreports.Schedule, bool, error) {
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
func (d BusinessReports) LinkScheduleRecipient(ctx context.Context, scheduleID, recipientID string) error {
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
func (d BusinessReports) ListScheduleRecipients(ctx context.Context, scheduleID string) ([]businessreports.Recipient, error) {
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
