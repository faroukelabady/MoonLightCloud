package businessreports

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
)

// Runner claims due report runs, composes missing immutable delivery
// snapshots, enqueues them through Phase 7A, and finishes under the
// run claim token. Claim transactions end before report queries;
// report composition and provider enqueueing never hold DB locks.
// ScheduleReader resolves schedules for run processing. Schedule
// kinds are immutable (7B offers no cadence edits), so the lookup is
// stable across retries and owner changes.
type ScheduleReader interface {
	GetSchedule(ctx context.Context, id string) (Schedule, bool, error)
}

type Runner struct {
	runs          RunStore
	schedules     ScheduleReader
	reports       ReportSource
	notifications *notifications.Service
	clock         Clock
	loc           *time.Location
	log           *slog.Logger
	wake          chan struct{}
	interval      time.Duration
	batch         int
	owner         string
	lease         time.Duration
}

// NewRunner wires the worker over the run store, schedule reader,
// canonical reports, and the frozen enqueue service.
func NewRunner(runs RunStore, schedules ScheduleReader, reports ReportSource, notifications *notifications.Service,
	clock Clock, loc *time.Location, interval time.Duration, batch int,
	owner string, lease time.Duration, log *slog.Logger) *Runner {
	return &Runner{
		runs: runs, schedules: schedules, reports: reports, notifications: notifications,
		clock: clock, loc: loc, log: log, wake: make(chan struct{}, 1),
		interval: interval, batch: batch, owner: owner, lease: lease,
	}
}

// Notify wakes the worker after run creation. Non-blocking.
func (w *Runner) Notify() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run drains on startup, on wake, and on interval until cancellation.
func (w *Runner) Run(ctx context.Context) {
	w.drain(ctx)
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
			w.drain(ctx)
		case <-ticker.C:
			w.drain(ctx)
		}
	}
}

// DrainForTest runs one bounded worker pass. Test hook only:
// production uses Run with wakeups and intervals.
func (w *Runner) DrainForTest(ctx context.Context) {
	w.drain(ctx)
}

func (w *Runner) drain(ctx context.Context) {
	for i := 0; i < w.batch; i++ {
		if ctx.Err() != nil {
			return
		}
		claimed, ok, err := w.runs.ClaimRun(ctx, w.owner, w.lease, w.clock.Now())
		if err != nil {
			w.logError("report run claim failed", "err", err.Error())
			return
		}
		if !ok {
			return
		}
		w.processOne(ctx, claimed)
	}
}

// processOne converges one claimed run. Snapshot persistence is
// conditional (first writer wins, content deterministic); delivery
// transitions require pending; run finishes are fenced. A stale owner
// therefore cannot duplicate sends or regress state: Phase 7A
// idempotency absorbs repeated enqueue calls with the same key.
func (w *Runner) processOne(ctx context.Context, claimed Run) {
	finish := func(result RunFinish, err error, what string) {
		if err != nil {
			w.logError("report run finish failed",
				"run", claimed.ID, "err", err.Error())
			return
		}
		if result == RunFinishStale {
			w.logInfo("report run finish stale",
				"run", claimed.ID,
				"lease_owner", claimed.LeaseOwner, "lease_generation", claimed.LeaseGeneration)
			return
		}
		w.logInfo("report run "+what, "run", claimed.ID,
			"schedule", claimed.ScheduleID, "attempt", claimed.AttemptCount)
	}
	kind, err := w.reportKind(ctx, claimed)
	if err != nil {
		result, ferr := w.runs.FinishRunBlocked(ctx,
			claimed.ID, claimed.LeaseOwner, claimed.LeaseGeneration, CodeCompositionFailed)
		finish(result, ferr, "blocked")
		return
	}
	deliveries, err := w.runs.ListRunDeliveries(ctx, claimed.ID)
	if err != nil {
		w.logError("report deliveries load failed", "run", claimed.ID, "err", err.Error())
		return
	}
	if len(deliveries) == 0 {
		result, ferr := w.runs.FinishRunBlocked(ctx,
			claimed.ID, claimed.LeaseOwner, claimed.LeaseGeneration, CodeNoRecipients)
		finish(result, ferr, "blocked")
		return
	}
	from, to, err := periodCivilDates(claimed, w.loc)
	if err != nil {
		result, ferr := w.runs.FinishRunBlocked(ctx,
			claimed.ID, claimed.LeaseOwner, claimed.LeaseGeneration, CodeCompositionFailed)
		finish(result, ferr, "blocked")
		return
	}
	pendingRemain := false
	blockedCode := ""
	for _, delivery := range deliveries {
		if ctx.Err() != nil {
			return
		}
		switch delivery.Status {
		case DeliveryEnqueued:
			continue
		case DeliveryBlocked:
			if blockedCode == "" {
				blockedCode = delivery.LastErrorCode
			}
			continue
		}
		outcome := w.processDelivery(ctx, claimed, kind, from, to, delivery)
		switch outcome {
		case deliveryDone:
			continue
		case deliveryBlocked:
			if blockedCode == "" {
				blockedCode = w.deliveryCode(ctx, claimed.ID, delivery.ID)
			}
		case deliveryRetry:
			pendingRemain = true
		case deliveryAbort:
			return
		}
	}
	now := w.clock.Now()
	switch {
	case pendingRemain:
		next := now.Add(runBackoff(int(claimed.AttemptCount)))
		result, ferr := w.runs.FinishRunRetry(ctx,
			claimed.ID, claimed.LeaseOwner, claimed.LeaseGeneration, next, CodeInternalRetry)
		finish(result, ferr, "retry")
	case blockedCode != "":
		result, ferr := w.runs.FinishRunBlocked(ctx,
			claimed.ID, claimed.LeaseOwner, claimed.LeaseGeneration, blockedCode)
		finish(result, ferr, "blocked")
	default:
		result, ferr := w.runs.FinishRunCompleted(ctx,
			claimed.ID, claimed.LeaseOwner, claimed.LeaseGeneration)
		finish(result, ferr, "completed")
	}
}

// deliveryOutcome is one delivery's processing result.
type deliveryOutcome int

const (
	// deliveryDone means enqueued or already terminal this pass.
	deliveryDone deliveryOutcome = iota + 1
	// deliveryBlocked means permanently failed this pass.
	deliveryBlocked
	// deliveryRetry means left pending for a later attempt.
	deliveryRetry
	// deliveryAbort means store failure: stop without finishing.
	deliveryAbort
)

// processDelivery converges one pending delivery: ensure the immutable
// body snapshot, then enqueue exactly once per snapshot.
func (w *Runner) processDelivery(ctx context.Context, claimed Run, kind ReportKind, from, to string, delivery Delivery) deliveryOutcome {
	body := delivery.Body
	if !delivery.HasBody {
		composed, digest, err := ComposeReport(ctx, w.reports, kind, from, to, delivery.Locale)
		if err != nil {
			if code, blocked := compositionBlockedCode(err); blocked {
				return w.blockDelivery(ctx, claimed, delivery, code)
			}
			// Composition needed the canonical service (temporary
			// local failure): retry without a terminal verdict.
			w.logError("report composition failed",
				"run", claimed.ID, "delivery", delivery.ID, "err", err.Error())
			return deliveryRetry
		}
		persisted, err := w.runs.PersistDeliverySnapshot(ctx,
			claimed.ID, delivery.ID, claimed.LeaseOwner, claimed.LeaseGeneration, composed, digest[:])
		if err != nil {
			w.logError("report snapshot persist failed",
				"run", claimed.ID, "delivery", delivery.ID, "err", err.Error())
			return deliveryAbort
		}
		if !persisted {
			// Another owner snapshotted first: adopt is unnecessary —
			// the immutable body is identical; re-read below through
			// the next attempt. Treat as retry without error.
			return deliveryRetry
		}
		body = composed
	}
	result, err := w.notifications.EnqueueTemplate(ctx, notifications.EnqueueTemplateRequest{
		ProviderKey: delivery.ProviderKey, IdempotencyKey: delivery.NotificationIdempotencyKey,
		Recipient: delivery.Recipient, TemplateKey: delivery.TemplateKey, Locale: delivery.Locale,
		Parameters: map[string]string{"report_body": body},
	})
	if err != nil {
		if code, blocked := notificationBlockedCode(err); blocked {
			return w.blockDelivery(ctx, claimed, delivery, code)
		}
		w.logError("report notification enqueue failed",
			"run", claimed.ID, "delivery", delivery.ID, "err", err.Error())
		return deliveryRetry
	}
	enqueued, err := w.runs.FinishDeliveryEnqueued(ctx, delivery.ID, result.ID)
	if err != nil {
		w.logError("report delivery finish failed",
			"run", claimed.ID, "delivery", delivery.ID, "err", err.Error())
		return deliveryAbort
	}
	if !enqueued {
		// Already enqueued by a racing owner with the same idempotent
		// call: converged regardless.
		return deliveryDone
	}
	return deliveryDone
}

// blockDelivery records a terminal delivery failure.
func (w *Runner) blockDelivery(ctx context.Context, claimed Run, delivery Delivery, code string) deliveryOutcome {
	if _, err := w.runs.FinishDeliveryBlocked(ctx, delivery.ID, code); err != nil {
		w.logError("report delivery block failed",
			"run", claimed.ID, "delivery", delivery.ID, "err", err.Error())
		return deliveryAbort
	}
	return deliveryBlocked
}

// deliveryCode re-reads one delivery's terminal code for run
// aggregation.
func (w *Runner) deliveryCode(ctx context.Context, runID, deliveryID string) string {
	deliveries, err := w.runs.ListRunDeliveries(ctx, runID)
	if err != nil {
		return CodeInternalRetry
	}
	for _, delivery := range deliveries {
		if delivery.ID == deliveryID {
			if delivery.LastErrorCode == "" {
				return CodeInternalRetry
			}
			return delivery.LastErrorCode
		}
	}
	return CodeInternalRetry
}

// compositionBlockedCode maps deterministic composition failures to
// terminal codes. Anything else (canonical service unavailable) is
// left for retry.
func compositionBlockedCode(err error) (string, bool) {
	var appErr *apperr.Error
	if !errors.As(err, &appErr) {
		return "", false
	}
	if appErr.Kind != apperr.InvalidInput {
		return "", false
	}
	if appErr.Message == CodeBodyTooLarge {
		return CodeBodyTooLarge, true
	}
	return CodeCompositionFailed, true
}

// notificationBlockedCode reports whether a Phase 7A enqueue failure
// is a terminal delivery block with its machine code.
func notificationBlockedCode(err error) (string, bool) {
	var appErr *apperr.Error
	if !errors.As(err, &appErr) {
		return "", false
	}
	switch appErr.Kind {
	case apperr.InvalidInput, apperr.NotFound:
		if appErr.Message == notifications.CodeTemplateMappingMissing {
			return CodeNotificationMappingMissing, true
		}
		return CodeNotificationValidation, true
	case apperr.Conflict:
		return CodeNotificationIdemConflict, true
	default:
		return "", false
	}
}

// reportKind resolves the immutable report kind for a claimed run
// from its schedule. Kinds never change after creation.
func (w *Runner) reportKind(ctx context.Context, claimed Run) (ReportKind, error) {
	schedule, found, err := w.schedules.GetSchedule(ctx, claimed.ScheduleID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", apperr.New(apperr.Internal, "report schedule missing for run")
	}
	return schedule.Kind, nil
}

// periodCivilDates recovers the custom-period civil bounds from a
// run's persisted half-open UTC window in the schedule zone.
func periodCivilDates(run Run, loc *time.Location) (string, string, error) {
	if loc == nil {
		return "", "", apperr.New(apperr.Internal, "report schedule: timezone not configured")
	}
	from := run.PeriodStart.In(loc).Format("2006-01-02")
	to := run.PeriodEnd.In(loc).AddDate(0, 0, -1).Format("2006-01-02")
	if err := ValidateCivilDate(from); err != nil {
		return "", "", err
	}
	if err := ValidateCivilDate(to); err != nil {
		return "", "", err
	}
	return from, to, nil
}

// runBackoff spaces run retries: 10s doubling per attempt, capped.
func runBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := 10 * time.Second
	for i := 1; i < attempt && delay < time.Hour; i++ {
		delay *= 2
	}
	if delay > time.Hour {
		return time.Hour
	}
	return delay
}

func (w *Runner) logInfo(msg string, args ...any) {
	if w.log != nil {
		w.log.Info(msg, args...)
	}
}

func (w *Runner) logError(msg string, args ...any) {
	if w.log != nil {
		w.log.Error(msg, args...)
	}
}
