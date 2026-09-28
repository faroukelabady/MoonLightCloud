package businessreports

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/report"

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

// processOne converges one claimed run in two strictly ordered
// phases. Phase 1 (snapshot barrier): every pending delivery without
// a body is composed from ONE canonical Summary result and persisted
// atomically under the run lease — no enqueue happens before the full
// snapshot set commits. Phase 2 (enqueue): pending deliveries enqueue
// from persisted snapshots only. The final run verdict always
// re-reads authoritative delivery rows. Stale owners fail every
// fenced write with zero rows and converge nothing.
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
	// Phase 1: snapshot barrier over pending deliveries missing bodies.
	if outcome := w.snapshotBarrier(ctx, claimed, kind, from, to, deliveries); outcome != barrierReady {
		switch outcome {
		case barrierBlocked:
			w.finishBlockedFromDurable(ctx, claimed)
		case barrierRetry:
			w.finishRetry(ctx, claimed)
		}
		return
	}
	// Phase 2: enqueue every pending delivery from persisted snapshots.
	pendingRemain := false
	for _, delivery := range w.pendingDeliveries(ctx, claimed.ID) {
		if ctx.Err() != nil {
			return
		}
		switch w.enqueueDelivery(ctx, claimed, delivery) {
		case deliveryDone:
			continue
		case deliveryBlocked:
			continue
		case deliveryRetry:
			pendingRemain = true
		case deliveryAbort:
			return
		}
	}
	w.finishAggregate(ctx, claimed, pendingRemain)
}

// barrierOutcome is the snapshot barrier result.
type barrierOutcome int

const (
	// barrierReady means every pending delivery has a persisted body.
	barrierReady barrierOutcome = iota + 1
	// barrierBlocked means the run was terminally blocked.
	barrierBlocked
	// barrierRetry means no verdict: a later attempt resumes.
	barrierRetry
)

// snapshotBarrier guarantees one canonical reporting basis per run:
// exactly one Summary result feeds every pending delivery locale, and
// all bodies persist atomically under the run lease before any
// enqueue. Partial pre-existing snapshot sets (another body already
// persisted while some pending delivery lacks one) block the run
// instead of mixing canonical bases across recipients.
func (w *Runner) snapshotBarrier(ctx context.Context, claimed Run, kind ReportKind, from, to string, deliveries []Delivery) barrierOutcome {
	var missing []Delivery
	var anyBody bool
	for _, delivery := range deliveries {
		if delivery.HasBody {
			anyBody = true
			continue
		}
		if delivery.Status == DeliveryPending {
			missing = append(missing, delivery)
		}
	}
	if len(missing) == 0 {
		return barrierReady
	}
	if anyBody {
		// Partial legacy state: another recipient already received a
		// body from an unrecoverable basis. Preserve it, fail closed,
		// never recompute the missing bodies from current data.
		result, err := w.runs.FinishRunBlocked(ctx,
			claimed.ID, claimed.LeaseOwner, claimed.LeaseGeneration, CodeSnapshotInconsistent)
		if err != nil || result == RunFinishStale {
			return barrierRetry
		}
		return barrierBlocked
	}
	summary, err := w.snapshotSummary(ctx, kind, from, to)
	if err != nil {
		if code, blocked := compositionBlockedCode(err); blocked {
			result, ferr := w.runs.FinishRunBlocked(ctx,
				claimed.ID, claimed.LeaseOwner, claimed.LeaseGeneration, code)
			if ferr != nil || result == RunFinishStale {
				return barrierRetry
			}
			return barrierBlocked
		}
		w.logError("report composition failed",
			"run", claimed.ID, "err", err.Error())
		return barrierRetry
	}
	snapshots := make(map[string]SnapshotBody, len(missing))
	for _, delivery := range missing {
		body, fingerprint, err := renderDeliveryBody(kind, from, to, delivery.Locale, summary)
		if err != nil {
			if code, blocked := compositionBlockedCode(err); blocked {
				result, ferr := w.runs.FinishRunBlocked(ctx,
					claimed.ID, claimed.LeaseOwner, claimed.LeaseGeneration, code)
				if ferr != nil || result == RunFinishStale {
					return barrierRetry
				}
				return barrierBlocked
			}
			w.logError("report composition failed",
				"run", claimed.ID, "delivery", delivery.ID, "err", err.Error())
			return barrierRetry
		}
		snapshots[delivery.ID] = SnapshotBody{Body: body, Fingerprint: fingerprint[:]}
	}
	persisted, err := w.runs.PersistRunDeliverySnapshots(ctx,
		claimed.ID, claimed.LeaseOwner, claimed.LeaseGeneration, snapshots)
	if err != nil {
		w.logError("report snapshot persist failed", "run", claimed.ID, "err", err.Error())
		return barrierRetry
	}
	if !persisted {
		// Lost the lease race or hit partial pre-existing state:
		// re-read decides on the next attempt. Never enqueue here.
		return barrierRetry
	}
	return barrierReady
}

// snapshotSummary queries the canonical report result ONCE per
// barrier: the single basis for every delivery locale in the run.
func (w *Runner) snapshotSummary(ctx context.Context, kind ReportKind, from, to string) (report.Summary, error) {
	request, err := w.reports.ParseRequest(report.PeriodCustom, from, to, "")
	if err != nil {
		return report.Summary{}, err
	}
	_ = kind
	summary, err := w.reports.Summary(ctx, request)
	if err != nil {
		return report.Summary{}, err
	}
	return summary, nil
}

// pendingDeliveries re-reads authoritative delivery rows for the
// enqueue phase and aggregate verdict.
func (w *Runner) pendingDeliveries(ctx context.Context, runID string) []Delivery {
	deliveries, err := w.runs.ListRunDeliveries(ctx, runID)
	if err != nil {
		w.logError("report deliveries reload failed", "run", runID, "err", err.Error())
		return nil
	}
	return deliveries
}

// enqueueDelivery enqueues one pending delivery from its persisted
// snapshot. Deliveries already terminal are skipped by the caller.
func (w *Runner) enqueueDelivery(ctx context.Context, claimed Run, delivery Delivery) deliveryOutcome {
	if delivery.Status != DeliveryPending {
		return deliveryDone
	}
	if !delivery.HasBody {
		// Barrier guarantees bodies; a missing one here means the
		// barrier lost a race after this pass listed rows: retry.
		return deliveryRetry
	}
	result, err := w.notifications.EnqueueTemplate(ctx, notifications.EnqueueTemplateRequest{
		ProviderKey: delivery.ProviderKey, IdempotencyKey: delivery.NotificationIdempotencyKey,
		Recipient: delivery.Recipient, TemplateKey: delivery.TemplateKey, Locale: delivery.Locale,
		Parameters: map[string]string{"report_body": delivery.Body},
	})
	if err != nil {
		if code, blocked := notificationBlockedCode(err); blocked {
			return w.blockDelivery(ctx, claimed, delivery, code)
		}
		w.logError("report notification enqueue failed",
			"run", claimed.ID, "delivery", delivery.ID, "err", err.Error())
		return deliveryRetry
	}
	enqueued, err := w.runs.FinishDeliveryEnqueued(ctx,
		claimed.ID, delivery.ID, claimed.LeaseOwner, claimed.LeaseGeneration, result.ID)
	if err != nil {
		w.logError("report delivery finish failed",
			"run", claimed.ID, "delivery", delivery.ID, "err", err.Error())
		return deliveryAbort
	}
	if !enqueued {
		// Fenced out (stale lease) or already transitioned by a
		// racing owner with the same idempotent call: the durable
		// rows decide on re-read, never this stale view.
		return deliveryRetry
	}
	return deliveryDone
}

// finishAggregate re-reads authoritative delivery rows and finishes
// the run to match durable state: completed only when every delivery
// is enqueued, blocked when terminal failures remain and nothing is
// pending, retry otherwise.
func (w *Runner) finishAggregate(ctx context.Context, claimed Run, pendingRemain bool) {
	now := w.clock.Now()
	deliveries, err := w.runs.ListRunDeliveries(ctx, claimed.ID)
	if err != nil {
		w.logError("report deliveries reload failed", "run", claimed.ID, "err", err.Error())
		return
	}
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
	pending, blockedCode := pendingRemain, ""
	for _, delivery := range deliveries {
		switch delivery.Status {
		case DeliveryPending:
			pending = true
		case DeliveryBlocked:
			if blockedCode == "" {
				blockedCode = delivery.LastErrorCode
			}
		}
	}
	switch {
	case pending:
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

// finishBlockedFromDurable re-reads terminal codes and blocks the run
// to match durable delivery rows.
func (w *Runner) finishBlockedFromDurable(ctx context.Context, claimed Run) {
	deliveries, err := w.runs.ListRunDeliveries(ctx, claimed.ID)
	if err != nil {
		w.logError("report deliveries reload failed", "run", claimed.ID, "err", err.Error())
		return
	}
	code := CodeSnapshotInconsistent
	for _, delivery := range deliveries {
		if delivery.Status == DeliveryBlocked && delivery.LastErrorCode != "" {
			code = delivery.LastErrorCode
			break
		}
	}
	result, ferr := w.runs.FinishRunBlocked(ctx,
		claimed.ID, claimed.LeaseOwner, claimed.LeaseGeneration, code)
	if ferr != nil {
		w.logError("report run finish failed", "run", claimed.ID, "err", ferr.Error())
		return
	}
	if result == RunFinishStale {
		w.logInfo("report run finish stale", "run", claimed.ID,
			"lease_owner", claimed.LeaseOwner, "lease_generation", claimed.LeaseGeneration)
		return
	}
	w.logInfo("report run blocked", "run", claimed.ID,
		"schedule", claimed.ScheduleID, "attempt", claimed.AttemptCount)
}

// finishRetry records a no-verdict retry with backoff.
func (w *Runner) finishRetry(ctx context.Context, claimed Run) {
	next := w.clock.Now().Add(runBackoff(int(claimed.AttemptCount)))
	result, ferr := w.runs.FinishRunRetry(ctx,
		claimed.ID, claimed.LeaseOwner, claimed.LeaseGeneration, next, CodeInternalRetry)
	if ferr != nil {
		w.logError("report run finish failed", "run", claimed.ID, "err", ferr.Error())
		return
	}
	if result == RunFinishStale {
		w.logInfo("report run finish stale", "run", claimed.ID,
			"lease_owner", claimed.LeaseOwner, "lease_generation", claimed.LeaseGeneration)
		return
	}
	w.logInfo("report run retry", "run", claimed.ID,
		"schedule", claimed.ScheduleID, "attempt", claimed.AttemptCount)
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

// blockDelivery records a terminal delivery failure under the current
// run lease. Stale owners affect zero rows; the caller re-reads.
func (w *Runner) blockDelivery(ctx context.Context, claimed Run, delivery Delivery, code string) deliveryOutcome {
	applied, err := w.runs.FinishDeliveryBlocked(ctx,
		claimed.ID, delivery.ID, claimed.LeaseOwner, claimed.LeaseGeneration, code)
	if err != nil {
		w.logError("report delivery block failed",
			"run", claimed.ID, "delivery", delivery.ID, "err", err.Error())
		return deliveryAbort
	}
	if !applied {
		return deliveryRetry
	}
	return deliveryBlocked
}

// deliveryCode re-reads one delivery's terminal code for run
// aggregation.
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
