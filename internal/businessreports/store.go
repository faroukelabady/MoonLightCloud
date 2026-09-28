package businessreports

import (
	"context"
	"time"
)

// Recipient is one durable report destination. Provider-neutral: only
// a provider key, never vendor credentials or accounts.
type Recipient struct {
	ID          string
	Label       string
	ProviderKey string
	Recipient   string
	Locale      string
	Enabled     bool
}

// RecipientStore is the durable recipient boundary.
type RecipientStore interface {
	CreateRecipient(ctx context.Context, id, label, providerKey, recipient, locale string, enabled bool) error
	GetRecipient(ctx context.Context, id string) (Recipient, bool, error)
	ListRecipients(ctx context.Context) ([]Recipient, error)
	SetRecipientEnabled(ctx context.Context, id string, enabled bool) error
}

// Schedule is one durable report cadence with its materialization
// cursor. next_run_* always describe the next unmaterialized slot.
type Schedule struct {
	ID               string
	Name             string
	Kind             ReportKind
	Timezone         string
	LocalTime        string
	AnchorLocalDate  string
	Enabled          bool
	Revision         int64
	NextRunLocalDate string
	NextRunAt        time.Time
}

// ScheduleStore is the durable schedule boundary.
type ScheduleStore interface {
	CreateSchedule(ctx context.Context, schedule Schedule) error
	CreateScheduleWithRecipients(ctx context.Context, schedule Schedule, recipientIDs []string) error
	EnableScheduleIfDisabled(ctx context.Context, id string, nextDate string, nextAt time.Time) (Schedule, bool, error)
	GetSchedule(ctx context.Context, id string) (Schedule, bool, error)
	ListSchedules(ctx context.Context) ([]Schedule, error)
	UpdateScheduleEnablement(ctx context.Context, id string, enabled bool, revision int64, nextDate string, nextAt time.Time) error
	LinkScheduleRecipient(ctx context.Context, scheduleID, recipientID string) error
	ListScheduleRecipients(ctx context.Context, scheduleID string) ([]Recipient, error)
}

// MaterializeOutcome reports one planner transaction.
type MaterializeOutcome struct {
	// Ran is false when the schedule was no longer due/enabled inside
	// the transaction (another instance won or state changed).
	Ran      bool
	RunID    string
	Created  bool
	SlotDate string
	Revision int64
}

// SchedulePlannerStore is the atomic planner boundary: due listing
// plus single-transaction slot materialization.
type SchedulePlannerStore interface {
	ClaimDueSchedules(ctx context.Context, limit int32) ([]Schedule, error)
	MaterializeNextSlot(ctx context.Context, scheduleID string, now time.Time, loc *time.Location) (MaterializeOutcome, error)
}

// Run is one durable report execution: scheduled slot or manual run.
type Run struct {
	ID                   string
	ScheduleID           string
	Kind                 RunKind
	SlotLocalDate        string
	ManualIdempotencyKey string
	ScheduledFor         time.Time
	PeriodStart          time.Time
	PeriodEnd            time.Time
	ScheduleRevision     int64
	Status               RunStatus
	AttemptCount         int32
	LeaseOwner           string
	LeaseGeneration      int64
	LastErrorCode        string
}

// Delivery is one per-recipient report handoff with immutable
// snapshots once composed.
type Delivery struct {
	ID                         string
	RunID                      string
	RecipientID                string
	ProviderKey                string
	Recipient                  string
	Locale                     string
	TemplateKey                string
	Body                       string
	HasBody                    bool
	Fingerprint                []byte
	NotificationIdempotencyKey string
	NotificationID             string
	Status                     DeliveryStatus
	LastErrorCode              string
}

// SnapshotBody is one composed immutable delivery body with its
// deterministic fingerprint, persisted atomically per run barrier.
type SnapshotBody struct {
	Body        string
	Fingerprint []byte
}

// RunFinish reports whether a run completion took effect. Stale is a
// safe no-op: another owner already controls the run.
type RunFinish int

const (
	// RunFinishApplied means the fenced transition committed.
	RunFinishApplied RunFinish = iota + 1
	// RunFinishStale means ownership moved on; the row is untouched.
	RunFinishStale
)

// RunStore is the durable run/delivery boundary. Every finish
// requires the claim token on a non-terminal row.
type RunStore interface {
	CreateManualRun(ctx context.Context, run Run, deliveries []Delivery) (string, bool, error)
	GetRun(ctx context.Context, id string) (Run, bool, error)
	ListRuns(ctx context.Context, limit int32) ([]Run, error)
	ListRunDeliveries(ctx context.Context, runID string) ([]Delivery, error)
	ClaimRun(ctx context.Context, owner string, lease time.Duration, now time.Time) (Run, bool, error)
	FinishRunCompleted(ctx context.Context, id, owner string, generation int64) (RunFinish, error)
	FinishRunRetry(ctx context.Context, id, owner string, generation int64, next time.Time, code string) (RunFinish, error)
	FinishRunBlocked(ctx context.Context, id, owner string, generation int64, code string) (RunFinish, error)
	PersistDeliverySnapshot(ctx context.Context, runID, deliveryID, owner string, generation int64, body string, fingerprint []byte) (bool, error)
	PersistRunDeliverySnapshots(ctx context.Context, runID, owner string, generation int64, snapshots map[string]SnapshotBody) (bool, error)
	FinishDeliveryEnqueued(ctx context.Context, runID, deliveryID, owner string, generation int64, notificationID string) (bool, error)
	FinishDeliveryBlocked(ctx context.Context, runID, deliveryID, owner string, generation int64, code string) (bool, error)
}

// ReportStats is the operational scheduler summary.
type ReportStats struct {
	Pending       int64
	Retry         int64
	Completed     int64
	Blocked       int64
	OldestPending *time.Time
}
