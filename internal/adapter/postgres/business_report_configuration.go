package postgres

import (
	"context"
	"errors"
	"sort"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/businessreports"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (d BusinessReports) UpdateRecipient(ctx context.Context, id, label, address, locale string) (businessreports.Recipient, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	parsed, err := parseUUID(id)
	if err != nil {
		return businessreports.Recipient{}, apperr.New(apperr.InvalidInput, "invalid recipient id")
	}
	row, err := sqlcgen.New(d.pool).UpdateReportRecipient(ctx, sqlcgen.UpdateReportRecipientParams{ID: parsed, Label: label, Recipient: address, Locale: locale})
	if errors.Is(err, pgx.ErrNoRows) {
		return businessreports.Recipient{}, apperr.New(apperr.NotFound, "recipient not found")
	}
	if err != nil {
		return businessreports.Recipient{}, apperr.Wrap(apperr.Internal, "report recipient", redact(err))
	}
	return businessreports.Recipient{ID: uuidString(row.ID), Label: row.Label, ProviderKey: row.ProviderKey, Recipient: row.Recipient, Locale: row.Locale, Enabled: row.Enabled}, nil
}

func (d BusinessReports) DeleteRecipient(ctx context.Context, id string) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	parsed, err := parseUUID(id)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "invalid recipient id")
	}
	rows, err := sqlcgen.New(d.pool).DeleteReportRecipient(ctx, parsed)
	if err != nil {
		return apperr.Wrap(apperr.Internal, "report recipient", redact(err))
	}
	if rows == 0 {
		return apperr.New(apperr.NotFound, "recipient not found")
	}
	return nil
}

func lockReportRecipients(ctx context.Context, q *sqlcgen.Queries, ids []string) ([]pgtype.UUID, error) {
	ordered := append([]string(nil), ids...)
	sort.Strings(ordered)
	locked := make([]pgtype.UUID, 0, len(ordered))
	for i, id := range ordered {
		if i > 0 && id == ordered[i-1] {
			continue
		}
		parsed, err := parseUUID(id)
		if err != nil {
			return nil, apperr.New(apperr.InvalidInput, "invalid recipient id")
		}
		row, err := q.GetRecipientForUpdate(ctx, parsed)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !row.Enabled {
			return nil, apperr.New(apperr.InvalidInput, "schedule recipient must exist and be enabled")
		}
		if err != nil {
			return nil, apperr.Wrap(apperr.Internal, "report recipient", redact(err))
		}
		locked = append(locked, parsed)
	}
	return locked, nil
}

func (d BusinessReports) UpdateScheduleWithRecipients(ctx context.Context, s businessreports.Schedule, ids []string, revision int64) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	parsed, err := parseUUID(s.ID)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "invalid schedule id")
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return apperr.Wrap(apperr.Internal, "report schedule", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	current, err := q.GetScheduleForUpdate(ctx, parsed)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && current.Deleted {
		return apperr.New(apperr.NotFound, "schedule not found")
	}
	if err != nil {
		return apperr.Wrap(apperr.Internal, "report schedule", redact(err))
	}
	if current.Revision != revision {
		return apperr.New(apperr.Conflict, "SCHEDULE_CHANGED")
	}
	locked, err := lockReportRecipients(ctx, q, ids)
	if err != nil {
		return err
	}
	// A planner may have advanced the cursor after the service read it.
	// Retain that locked cursor for edits that do not change the cadence.
	nextDate, nextAt := businessReportDate(s.NextRunLocalDate), pgTime(s.NextRunAt)
	if current.ReportKind == string(s.Kind) && current.LocalTime == s.LocalTime && businessReportDateString(current.AnchorLocalDate) == s.AnchorLocalDate {
		nextDate, nextAt = current.NextRunLocalDate, current.NextRunAt
	}
	var anchor pgtype.Date
	if s.AnchorLocalDate != "" {
		anchor = businessReportDate(s.AnchorLocalDate)
	}
	_, err = q.UpdateReportSchedule(ctx, sqlcgen.UpdateReportScheduleParams{ID: parsed, Name: s.Name, ReportKind: string(s.Kind), LocalTime: s.LocalTime, AnchorLocalDate: anchor, NextRunLocalDate: nextDate, NextRunAt: nextAt, Revision: revision})
	if isUniqueViolation(err) {
		return apperr.New(apperr.Conflict, "report schedule already exists")
	}
	if err != nil {
		return apperr.Wrap(apperr.Internal, "report schedule", redact(err))
	}
	if err = q.ClearReportScheduleRecipients(ctx, parsed); err != nil {
		return apperr.Wrap(apperr.Internal, "report schedule", redact(err))
	}
	for _, recipient := range locked {
		if err = q.LinkScheduleRecipient(ctx, sqlcgen.LinkScheduleRecipientParams{ScheduleID: parsed, RecipientID: recipient}); err != nil {
			return apperr.Wrap(apperr.Internal, "report schedule", redact(err))
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return apperr.Wrap(apperr.Internal, "report schedule", redact(err))
	}
	return nil
}

func (d BusinessReports) DeleteSchedule(ctx context.Context, id string) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	parsed, err := parseUUID(id)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "invalid schedule id")
	}
	rows, err := sqlcgen.New(d.pool).DeleteReportSchedule(ctx, parsed)
	if err != nil {
		return apperr.Wrap(apperr.Internal, "report schedule", redact(err))
	}
	if rows == 0 {
		return apperr.New(apperr.NotFound, "schedule not found")
	}
	return nil
}

func (d BusinessReports) ListRunPage(ctx context.Context, before string, limit int32) ([]businessreports.Run, int64, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	var cursor pgtype.UUID
	if before != "" {
		var err error
		cursor, err = parseUUID(before)
		if err != nil {
			return nil, 0, apperr.New(apperr.InvalidInput, "invalid report cursor")
		}
	}
	rows, err := sqlcgen.New(d.pool).ListReportRunPage(ctx, sqlcgen.ListReportRunPageParams{BeforeID: cursor, PageLimit: limit})
	if err != nil {
		return nil, 0, apperr.Wrap(apperr.Internal, "report history", redact(err))
	}
	runs := make([]businessreports.Run, 0, len(rows))
	for _, r := range rows {
		runs = append(runs, mapRunFields(r.ID, r.ScheduleID, r.RunKind, r.ReportKind, r.ScheduleName, r.SlotLocalDate, r.ManualIdempotencyKey, r.ScheduledFor, r.PeriodStart, r.PeriodEnd, r.ScheduleRevision, r.Status, r.AttemptCount, r.LeaseOwner, r.LeaseGeneration, r.LastErrorCode))
	}
	if len(rows) > 0 {
		ids := make([]pgtype.UUID, 0, len(rows))
		byRun := make(map[string]int, len(rows))
		for i, row := range rows {
			ids = append(ids, row.ID)
			byRun[runs[i].ID] = i
		}
		states, err := sqlcgen.New(d.pool).ListReportDeliveryStates(ctx, ids)
		if err != nil {
			return nil, 0, apperr.Wrap(apperr.Internal, "report delivery history", redact(err))
		}
		for _, row := range states {
			i := byRun[uuidString(row.RunID)]
			runs[i].DeliveryStates = append(runs[i].DeliveryStates, businessreports.DeliveryState{
				ID: uuidString(row.ID), RunID: uuidString(row.RunID), ProviderKey: row.ProviderKey,
				RecipientLabel: row.RecipientLabel, RecipientMasked: row.RecipientMasked,
				ReportStatus: row.ReportStatus, DispatchStatus: row.DispatchStatus, DeliveryStatus: row.DeliveryStatus,
				LastErrorCode: row.LastErrorCode, AttemptCount: row.AttemptCount, SendStarted: row.SendStarted, UpdatedAt: row.UpdatedAt.Time,
			})
		}
	}
	var remaining int64
	if len(rows) > 0 {
		remaining = rows[0].RemainingCount
	}
	return runs, remaining, nil
}
