package notifications

import (
	"context"
	"time"
)

// TemplateMapping is one durable logical-to-external template row.
// Repository contracts live with the domain; adapters implement them.
type TemplateMapping struct {
	ProviderKey          string
	TemplateKey          string
	Locale               string
	ExternalTemplateName string
	ExternalLanguageCode string
	ParameterNames       []string
	Enabled              bool
}

// MappingStore is the durable template-mapping boundary.
type MappingStore interface {
	UpsertTemplateMapping(ctx context.Context, mapping TemplateMapping) error
	GetTemplateMapping(ctx context.Context, providerKey, templateKey, locale string) (TemplateMapping, bool, error)
	ListTemplateMappings(ctx context.Context, providerKey string) ([]TemplateMapping, error)
}

// EnqueueIntent is the durable enqueue intent: caller identity plus
// the resolved external snapshot frozen for this notification.
type EnqueueIntent struct {
	ProviderKey       string
	IdempotencyKey    string
	Recipient         string
	TemplateKey       string
	Locale            string
	Parameters        map[string]string
	ExtTemplateName   string
	ExtLanguageCode   string
	ExtParameterOrder []string
}

// EnqueueOutcome makes idempotent enqueue explicit.
type EnqueueOutcome int

const (
	// EnqueueInserted means a new notification row was created.
	EnqueueInserted EnqueueOutcome = iota + 1
	// EnqueueDuplicateIdentical means the same identity and semantic
	// fingerprint already exist: no new work.
	EnqueueDuplicateIdentical
)

// EnqueueStore is the durable outbox boundary.
type EnqueueStore interface {
	EnqueueNotification(ctx context.Context, intent EnqueueIntent) (string, EnqueueOutcome, error)
}

// ClaimedNotification is one leased outbox row with its fencing token.
// SendStarted means a previous attempt durably began sending without
// recording an explicit safe outcome: the dispatcher must route to
// ambiguous without provider I/O.
type ClaimedNotification struct {
	ID                string
	ProviderKey       string
	IdempotencyKey    string
	Recipient         string
	TemplateKey       string
	Locale            string
	Parameters        map[string]string
	ExtTemplateName   string
	ExtLanguageCode   string
	ExtParameterOrder []string
	Dispatch          DispatchStatus
	AttemptCount      int32
	SendStarted       bool
	LeaseOwner        string
	LeaseGeneration   int64
}

// FinishResult reports whether a completion took effect. Stale is a
// safe no-op: another owner already controls the notification.
type FinishResult int

const (
	// FinishApplied means the fenced transition committed.
	FinishApplied FinishResult = iota + 1
	// FinishStale means ownership moved on; the row is untouched.
	FinishStale
)

// DispatchStore is the durable dispatch boundary. Every finish
// requires the claim token on a non-terminal row.
type DispatchStore interface {
	ClaimNotification(ctx context.Context, owner string, lease time.Duration, now time.Time) (ClaimedNotification, bool, error)
	MarkNotificationSendStarted(ctx context.Context, id string, owner string, generation int64) (FinishResult, error)
	FinishNotificationAccepted(ctx context.Context, id string, owner string, generation int64, providerMessageID string) (FinishResult, error)
	FinishNotificationRetry(ctx context.Context, id string, owner string, generation int64, next time.Time, code string) (FinishResult, error)
	FinishNotificationBlocked(ctx context.Context, id string, owner string, generation int64, code string) (FinishResult, error)
	FinishNotificationAmbiguous(ctx context.Context, id string, owner string, generation int64, code string) (FinishResult, error)
}

// DeliveryEvent is one normalized provider status callback.
type DeliveryEvent struct {
	ProviderMessageID string
	RawStatus         string
	Canonical         DeliveryStatus
	ProviderTimestamp *time.Time
	ErrorCode         string
	Fingerprint       [32]byte
}

// DeliveryOutcome reports what a status callback changed.
type DeliveryOutcome struct {
	HistoryInserted bool
	CurrentAdvanced bool
	Ignored         bool
}

// DeliveryStore is the durable delivery-status boundary.
type DeliveryStore interface {
	ApplyDeliveryStatus(ctx context.Context, notificationID string, event DeliveryEvent) (DeliveryOutcome, error)
	LookupByProviderMessage(ctx context.Context, providerKey, providerMessageID string) (notificationID string, found bool, err error)
}
