package businessreports

import (
	"context"
	"log/slog"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
	"github.com/google/uuid"
)

// Clock abstracts time for deterministic tests.
type Clock interface {
	Now() time.Time
}

// SystemClock is production time.
type SystemClock struct{}

// Now implements Clock.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// Service orchestrates recipients, schedules, runs, and deliveries
// over durable stores, the canonical report service, and the frozen
// Phase 7A enqueue service. It performs provider-neutral work only:
// no WhatsApp imports anywhere in this package.
type Service struct {
	recipients    RecipientStore
	schedules     ScheduleStore
	planner       SchedulePlannerStore
	runs          RunStore
	reports       ReportSource
	notifications *notifications.Service
	clock         Clock
	loc           *time.Location
	log           *slog.Logger
	lease         time.Duration
	batch         int32
}

// NewService wires business reports. loc must be the Cairo zone;
// lease bounds run claims; batch bounds planner passes.
func NewService(recipients RecipientStore, schedules ScheduleStore, planner SchedulePlannerStore,
	runs RunStore, reports ReportSource, notifications *notifications.Service,
	clock Clock, loc *time.Location, lease time.Duration, batch int32, log *slog.Logger) *Service {
	return &Service{
		recipients: recipients, schedules: schedules, planner: planner,
		runs: runs, reports: reports, notifications: notifications,
		clock: clock, loc: loc, lease: lease, batch: batch, log: log,
	}
}

// AddRecipient stores one enabled report destination. The recipient is
// validated but never echoed back beyond its new identity.
func (s *Service) AddRecipient(ctx context.Context, label, providerKey, recipient, locale string) (Recipient, error) {
	if len(label) == 0 || len(label) > 64 {
		return Recipient{}, apperr.New(apperr.InvalidInput, "invalid label: want 1..64 characters")
	}
	key, err := notifications.ValidateProviderKey(providerKey)
	if err != nil {
		return Recipient{}, apperr.New(apperr.InvalidInput, err.Error())
	}
	if err := notifications.ValidateRecipient(recipient); err != nil {
		return Recipient{}, apperr.New(apperr.InvalidInput, err.Error())
	}
	if err := ValidateLocale(locale); err != nil {
		return Recipient{}, err
	}
	record := Recipient{
		ID: uuid.NewString(), Label: label, ProviderKey: string(key),
		Recipient: recipient, Locale: locale, Enabled: true,
	}
	if err := s.recipients.CreateRecipient(ctx, record.ID, label, string(key), recipient, locale, true); err != nil {
		return Recipient{}, err
	}
	s.logInfo("report recipient added", "provider", string(key), "locale", locale)
	return record, nil
}

// ListRecipients returns configured destinations (full addresses stay
// in the store; CLI masks them for display).
func (s *Service) ListRecipients(ctx context.Context) ([]Recipient, error) {
	return s.recipients.ListRecipients(ctx)
}

// DisableRecipient stops future runs from using this destination.
// Materialized runs keep their snapshots.
func (s *Service) DisableRecipient(ctx context.Context, id string) error {
	return s.recipients.SetRecipientEnabled(ctx, id, false)
}

// CreateSchedule stores one cadence with at least one enabled
// recipient and positions next_run at the first future slot. Nothing
// historical is ever materialized: creation never backfills.
func (s *Service) CreateSchedule(ctx context.Context, name, kind, localTime, anchor string, recipientIDs []string) (Schedule, error) {
	if len(name) == 0 || len(name) > 64 {
		return Schedule{}, apperr.New(apperr.InvalidInput, "invalid schedule name: want 1..64 characters")
	}
	reportKind, err := ValidateReportKind(kind)
	if err != nil {
		return Schedule{}, err
	}
	if err := ValidateLocalTime(localTime); err != nil {
		return Schedule{}, err
	}
	if reportKind == ReportTenDay {
		if err := ValidateCivilDate(anchor); err != nil {
			return Schedule{}, apperr.New(apperr.InvalidInput, "ten-day schedule requires anchor_local_date")
		}
	}
	if len(recipientIDs) == 0 {
		return Schedule{}, apperr.New(apperr.InvalidInput, CodeNoRecipients)
	}
	now := s.clock.Now()
	slotDate, slotAt, err := s.firstFutureSlot(reportKind, anchor, localTime, now)
	if err != nil {
		return Schedule{}, err
	}
	schedule := Schedule{
		ID: uuid.NewString(), Name: name, Kind: reportKind,
		Timezone: TimezoneCairo, LocalTime: localTime, AnchorLocalDate: anchor,
		Enabled: true, Revision: 1, NextRunLocalDate: slotDate, NextRunAt: slotAt,
	}
	if err := s.schedules.CreateSchedule(ctx, schedule); err != nil {
		return Schedule{}, err
	}
	linked := 0
	for _, recipientID := range recipientIDs {
		recipient, found, err := s.recipients.GetRecipient(ctx, recipientID)
		if err != nil {
			return Schedule{}, err
		}
		if !found || !recipient.Enabled {
			return Schedule{}, apperr.New(apperr.InvalidInput, "schedule recipient must exist and be enabled")
		}
		if err := s.schedules.LinkScheduleRecipient(ctx, schedule.ID, recipientID); err != nil {
			return Schedule{}, err
		}
		linked++
	}
	if linked == 0 {
		return Schedule{}, apperr.New(apperr.InvalidInput, CodeNoRecipients)
	}
	s.logInfo("report schedule created", "schedule", schedule.ID, "kind", string(reportKind), "slot", slotDate)
	return schedule, nil
}

// firstFutureSlot resolves the first slot with an instant strictly
// after the reference time for a kind/cadence.
func (s *Service) firstFutureSlot(kind ReportKind, anchor, localTime string, after time.Time) (string, time.Time, error) {
	if kind == ReportTenDay {
		return NextTenDaySlot(anchor, localTime, after, s.loc)
	}
	return NextDailySlot(localTime, after, s.loc)
}

// ListSchedules returns configured cadences.
func (s *Service) ListSchedules(ctx context.Context) ([]Schedule, error) {
	return s.schedules.ListSchedules(ctx)
}

// EnableSchedule resumes a schedule at the first future slot from
// now. The intentionally-disabled interval is never backfilled.
func (s *Service) EnableSchedule(ctx context.Context, id string) (Schedule, error) {
	schedule, found, err := s.schedules.GetSchedule(ctx, id)
	if err != nil {
		return Schedule{}, err
	}
	if !found {
		return Schedule{}, apperr.New(apperr.NotFound, "schedule not found")
	}
	now := s.clock.Now()
	slotDate, slotAt, err := s.firstFutureSlot(schedule.Kind, schedule.AnchorLocalDate, schedule.LocalTime, now)
	if err != nil {
		return Schedule{}, err
	}
	if err := s.schedules.UpdateScheduleEnablement(ctx, id, true, schedule.Revision+1, slotDate, slotAt); err != nil {
		return Schedule{}, err
	}
	schedule.Enabled = true
	schedule.Revision++
	schedule.NextRunLocalDate = slotDate
	schedule.NextRunAt = slotAt
	return schedule, nil
}

// DisableSchedule stops future slot materialization. History,
// materialized runs, and notifications are untouched.
func (s *Service) DisableSchedule(ctx context.Context, id string) (Schedule, error) {
	schedule, found, err := s.schedules.GetSchedule(ctx, id)
	if err != nil {
		return Schedule{}, err
	}
	if !found {
		return Schedule{}, apperr.New(apperr.NotFound, "schedule not found")
	}
	if err := s.schedules.UpdateScheduleEnablement(ctx, id, false,
		schedule.Revision+1, schedule.NextRunLocalDate, schedule.NextRunAt); err != nil {
		return Schedule{}, err
	}
	schedule.Enabled = false
	schedule.Revision++
	return schedule, nil
}

// RunNow creates one manual run for the previous complete period(s)
// with a caller-supplied idempotency key. It never touches
// schedule.next_run. Identical replays return the existing run;
// contradictory replays conflict.
func (s *Service) RunNow(ctx context.Context, scheduleID, manualKey string) (string, bool, error) {
	if err := notifications.ValidateIdempotencyKey(manualKey); err != nil {
		return "", false, apperr.New(apperr.InvalidInput, err.Error())
	}
	schedule, found, err := s.schedules.GetSchedule(ctx, scheduleID)
	if err != nil {
		return "", false, err
	}
	if !found {
		return "", false, apperr.New(apperr.NotFound, "schedule not found")
	}
	now := s.clock.Now()
	from, to, err := ManualPeriod(schedule.Kind, now, s.loc)
	if err != nil {
		return "", false, err
	}
	start, end, err := PeriodBounds(from, to, s.loc)
	if err != nil {
		return "", false, err
	}
	recipients, err := s.schedules.ListScheduleRecipients(ctx, scheduleID)
	if err != nil {
		return "", false, err
	}
	run := Run{
		ID: uuid.NewString(), ScheduleID: scheduleID, Kind: RunManual,
		ManualIdempotencyKey: manualKey, ScheduledFor: now,
		PeriodStart: start.UTC(), PeriodEnd: end.UTC(),
		ScheduleRevision: schedule.Revision, Status: RunPending,
	}
	var deliveries []Delivery
	for _, recipient := range recipients {
		if !recipient.Enabled {
			continue
		}
		deliveryID := uuid.NewString()
		deliveries = append(deliveries, Delivery{
			ID: deliveryID, RecipientID: recipient.ID,
			ProviderKey: recipient.ProviderKey, Recipient: recipient.Recipient,
			Locale: recipient.Locale, TemplateKey: TemplateKeyForKind(schedule.Kind),
			NotificationIdempotencyKey: NotificationIdempotencyKey(run.ID, deliveryID),
			Status:                     DeliveryPending,
		})
	}
	id, created, err := s.runs.CreateManualRun(ctx, run, deliveries)
	if err != nil {
		return "", false, err
	}
	s.logInfo("report manual run", "schedule", scheduleID, "run", id, "created", created)
	return id, created, nil
}

// ListRuns returns recent runs, newest last for stable CLI display.
func (s *Service) ListRuns(ctx context.Context, limit int32) ([]Run, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	return s.runs.ListRuns(ctx, limit)
}

// RunDetail bundles one run with its deliveries for inspection.
type RunDetail struct {
	Run        Run
	Deliveries []Delivery
}

// GetRunStatus reads one run with deliveries (recipient addresses
// stay in the store; CLI masks them).
func (s *Service) GetRunStatus(ctx context.Context, id string) (RunDetail, error) {
	run, found, err := s.runs.GetRun(ctx, id)
	if err != nil {
		return RunDetail{}, err
	}
	if !found {
		return RunDetail{}, apperr.New(apperr.NotFound, "report run not found")
	}
	deliveries, err := s.runs.ListRunDeliveries(ctx, id)
	if err != nil {
		return RunDetail{}, err
	}
	return RunDetail{Run: run, Deliveries: deliveries}, nil
}

func (s *Service) logInfo(msg string, args ...any) {
	if s.log != nil {
		s.log.Info(msg, args...)
	}
}

func (s *Service) logError(msg string, args ...any) {
	if s.log != nil {
		s.log.Error(msg, args...)
	}
}
