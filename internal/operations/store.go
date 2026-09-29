package operations

import (
	"context"
	"time"
)

// Recipient is one durable alert recipient (full address persisted;
// masked on every operator surface).
type Recipient struct {
	ID          string
	Label       string
	ProviderKey string
	Recipient   string
	Locale      string
	Enabled     bool
}

// Delivery is one durable per-recipient alert delivery snapshot.
type Delivery struct {
	ID                   string
	IncidentID           string
	RecipientID          string
	Event                string
	ProviderKey          string
	RecipientSnapshot    string
	Locale               string
	TemplateKey          string
	Body                 string
	Fingerprint          []byte
	NotificationID       *string
	NotificationKey      string
	Status               string
	LastErrorCode        *string
	NotificationDispatch *string
	NotificationDelivery *string
}

// Recovery is one durable reconnect recovery action.
type Recovery struct {
	ID             string
	IncidentID     string
	ActionType     string
	State          string
	IdempotencyKey string
	TargetEntityID *string
	ResultCode     *string
	AttemptCount   int
	NextAttemptAt  *time.Time
	LastErrorCode  *string
}

// DeviceSummary is the batched per-device active-incident aggregate.
type DeviceSummary struct {
	DeviceID    string
	OpenCount   int
	MaxSeverity string
}

// OfflineCandidate is one device past the offline threshold.
type OfflineCandidate struct {
	DeviceID string
	Name     string
	LastSeen time.Time
}

// FailedCommand is one terminally failed sync command.
type FailedCommand struct {
	CommandID  string
	DeviceID   string
	ResultCode *string
	FinishedAt *time.Time
}

// StaleCommand is one non-terminal command past its age threshold.
type StaleCommand struct {
	CommandID   string
	DeviceID    string
	Status      string
	RequestedAt time.Time
}

// BlockedRun is one terminally blocked report run.
type BlockedRun struct {
	RunID     string
	Schedule  string
	Kind      string
	Slot      *string
	CreatedAt time.Time
}

// StaleRun is one pending/retry report run past its age threshold.
type StaleRun struct {
	RunID     string
	Status    string
	CreatedAt time.Time
}

// BadNotification is one blocked/ambiguous/stale notification.
type BadNotification struct {
	NotificationID string
	TemplateKey    string
	CreatedAt      time.Time
}

// Store is the persistence contract; postgres implements it.
type Store interface {
	// Recipients.
	CreateOpsRecipient(ctx context.Context, id, label, provider, recipient, locale string, at time.Time) (Recipient, error)
	ListOpsRecipients(ctx context.Context) ([]Recipient, error)
	ListEnabledOpsRecipients(ctx context.Context) ([]Recipient, error)
	DisableOpsRecipient(ctx context.Context, id string, at time.Time) (bool, error)
	// Incidents.
	OpenStateful(ctx context.Context, id, rule, subjectType, subjectID, severity, sourceKey string, episode int, at time.Time) (Incident, bool, error)
	OpenEvent(ctx context.Context, id, rule, subjectType, subjectID, severity, sourceKey string, at time.Time) (Incident, bool, error)
	ActiveIncident(ctx context.Context, rule, subjectType, subjectID string) (Incident, bool, error)
	IncidentByEventKey(ctx context.Context, key string) (Incident, bool, error)
	IncidentByID(ctx context.Context, id string) (Incident, bool, error)
	TouchObserved(ctx context.Context, id string, at time.Time) error
	Acknowledge(ctx context.Context, id string, at time.Time) (Incident, bool, error)
	Resolve(ctx context.Context, id, code string, at time.Time) (Incident, bool, error)
	MaxEpisode(ctx context.Context, rule, subjectType, subjectID string) (int, error)
	ActiveByRule(ctx context.Context, rule string, limit int) ([]Incident, error)
	ListPage(ctx context.Context, state, severity, rule string, cursorAt *time.Time, cursorID string, limit int) ([]Incident, error)
	DeviceSummary(ctx context.Context) ([]DeviceSummary, error)
	// Deliveries.
	CreateDelivery(ctx context.Context, d Delivery, at time.Time) (Delivery, error)
	ClaimDelivery(ctx context.Context) (Delivery, bool, error)
	FinishOpsDeliverySent(ctx context.Context, id, notificationID string, at time.Time) error
	FinishOpsDeliveryBlocked(ctx context.Context, id, code string, at time.Time) error
	DeliveriesForIncident(ctx context.Context, incidentID string) ([]Delivery, error)
	// Recovery.
	CreateRecovery(ctx context.Context, incidentID, idempotencyKey string, at time.Time) (Recovery, bool, error)
	RecoveryForIncident(ctx context.Context, incidentID string) (Recovery, bool, error)
	ClaimRecovery(ctx context.Context, now time.Time) (Recovery, bool, error)
	FinishRecoveryCompleted(ctx context.Context, id, target, result string, at time.Time) error
	RetryRecoveryLater(ctx context.Context, id, code string, next, at time.Time) error
	FinishRecoveryBlocked(ctx context.Context, id, code string, at time.Time) error
	RecoveriesForIncident(ctx context.Context, incidentID string) ([]Recovery, error)
	// Detector scans (bounded, server-time).
	ScanOffline(ctx context.Context, olderThan time.Time, limit int) ([]OfflineCandidate, error)
	ScanReconnected(ctx context.Context, newerThan time.Time, limit int) ([]OfflineCandidate, error)
	ScanRevoked(ctx context.Context, limit int) ([]string, error)
	ScanFailedCommands(ctx context.Context, limit int) ([]FailedCommand, error)
	ScanStaleCommands(ctx context.Context, olderThan time.Time, limit int) ([]StaleCommand, error)
	ScanBlockedRuns(ctx context.Context, limit int) ([]BlockedRun, error)
	ScanStaleRuns(ctx context.Context, olderThan time.Time, limit int) ([]StaleRun, error)
	ScanBlockedNotifications(ctx context.Context, limit int) ([]BadNotification, error)
	ScanAmbiguousNotifications(ctx context.Context, limit int) ([]BadNotification, error)
	ScanRetryStaleNotifications(ctx context.Context, olderThan time.Time, limit int) ([]BadNotification, error)
	// Predicate reads for resolution.
	CommandTerminal(ctx context.Context, commandID string) (string, bool, error)
	RunStatus(ctx context.Context, runID string) (string, bool, error)
	NotificationDispatch(ctx context.Context, notificationID string) (string, bool, error)
	DeviceStatus(ctx context.Context, deviceID string) (string, bool, error)
	Presence(ctx context.Context, deviceID string) (*time.Time, bool, error)
}
