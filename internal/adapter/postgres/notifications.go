package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
)

// Notification template mappings and the idempotent outbox live on
// Devices (shared pool + timeouts). They are Cloud-only durable
// integration state: no catalog/policy/inventory/order rebuild path
// touches these tables.

// UpsertTemplateMapping creates or replaces one mapping. Parameter
// names define the deterministic provider order.
func (d Devices) UpsertTemplateMapping(ctx context.Context, mapping notifications.TemplateMapping) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	if _, err := notifications.ValidateProviderKey(mapping.ProviderKey); err != nil {
		return apperr.New(apperr.InvalidInput, err.Error())
	}
	if err := notifications.ValidateTemplateKey(mapping.TemplateKey); err != nil {
		return apperr.New(apperr.InvalidInput, err.Error())
	}
	if err := notifications.ValidateLocale(mapping.Locale); err != nil {
		return apperr.New(apperr.InvalidInput, err.Error())
	}
	if err := sqlcgen.New(d.pool).UpsertTemplateMapping(ctx, sqlcgen.UpsertTemplateMappingParams{
		ProviderKey: mapping.ProviderKey, TemplateKey: mapping.TemplateKey, Locale: mapping.Locale,
		ExternalTemplateName: mapping.ExternalTemplateName, ExternalLanguageCode: mapping.ExternalLanguageCode,
		ParameterNames: mapping.ParameterNames, Enabled: mapping.Enabled,
	}); err != nil {
		return apperr.Wrap(apperr.Internal, "template mapping", redact(err))
	}
	return nil
}

// GetTemplateMapping resolves one mapping or reports found=false.
func (d Devices) GetTemplateMapping(ctx context.Context, providerKey, templateKey, locale string) (notifications.TemplateMapping, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	row, err := sqlcgen.New(d.pool).GetTemplateMapping(ctx, sqlcgen.GetTemplateMappingParams{
		ProviderKey: providerKey, TemplateKey: templateKey, Locale: locale,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notifications.TemplateMapping{}, false, nil
		}
		return notifications.TemplateMapping{}, false, apperr.Wrap(apperr.Internal, "template mapping", redact(err))
	}
	return notifications.TemplateMapping{
		ProviderKey: row.ProviderKey, TemplateKey: row.TemplateKey, Locale: row.Locale,
		ExternalTemplateName: row.ExternalTemplateName, ExternalLanguageCode: row.ExternalLanguageCode,
		ParameterNames: row.ParameterNames, Enabled: row.Enabled,
	}, true, nil
}

// ListTemplateMappings lists mappings for one provider ("" for all).
func (d Devices) ListTemplateMappings(ctx context.Context, providerKey string) ([]notifications.TemplateMapping, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).ListTemplateMappings(ctx, providerKey)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "template mapping", redact(err))
	}
	mappings := make([]notifications.TemplateMapping, 0, len(rows))
	for _, row := range rows {
		mappings = append(mappings, notifications.TemplateMapping{
			ProviderKey: row.ProviderKey, TemplateKey: row.TemplateKey, Locale: row.Locale,
			ExternalTemplateName: row.ExternalTemplateName, ExternalLanguageCode: row.ExternalLanguageCode,
			ParameterNames: row.ParameterNames, Enabled: row.Enabled,
		})
	}
	return mappings, nil
}

// EnqueueNotification inserts one notification or classifies the
// idempotent replay. Same key + same fingerprint is idempotent; same
// key + different fingerprint is a Conflict that never overwrites.
func (d Devices) EnqueueNotification(ctx context.Context, enqueue notifications.EnqueueIntent) (string, notifications.EnqueueOutcome, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	id := uuid.NewString()
	parameters, err := json.Marshal(enqueue.Parameters)
	if err != nil {
		return "", notifications.EnqueueInserted, apperr.New(apperr.InvalidInput, "invalid parameters")
	}
	q := sqlcgen.New(d.pool)
	returned, err := q.InsertNotification(ctx, sqlcgen.InsertNotificationParams{
		ID: mustPgID(id), ProviderKey: enqueue.ProviderKey, IdempotencyKey: enqueue.IdempotencyKey,
		SemanticFingerprint: enqueueFingerprint(ctx, enqueue),
		Recipient:           enqueue.Recipient, TemplateKey: enqueue.TemplateKey, Locale: enqueue.Locale,
		Parameters: parameters, ExtTemplateName: enqueue.ExtTemplateName,
		ExtLanguageCode: enqueue.ExtLanguageCode, ExtParameterOrder: enqueue.ExtParameterOrder,
	})
	if err == nil {
		return mustGoID(returned), notifications.EnqueueInserted, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", notifications.EnqueueInserted, apperr.Wrap(apperr.Internal, "notification enqueue", redact(err))
	}
	existing, err := q.GetNotificationByIdempotency(ctx, sqlcgen.GetNotificationByIdempotencyParams{
		ProviderKey: enqueue.ProviderKey, IdempotencyKey: enqueue.IdempotencyKey,
	})
	if err != nil {
		return "", notifications.EnqueueInserted, apperr.Wrap(apperr.Internal, "notification enqueue", redact(err))
	}
	if string(existing.SemanticFingerprint) != string(enqueueFingerprint(ctx, enqueue)) {
		return "", notifications.EnqueueInserted, apperr.New(apperr.Conflict, "idempotency key already used with different payload")
	}
	return mustGoID(existing.ID), notifications.EnqueueDuplicateIdentical, nil
}

// enqueueFingerprint recomputes the semantic digest from the enqueue
// intent so replays compare against current resolution rules.
func enqueueFingerprint(_ context.Context, enqueue notifications.EnqueueIntent) []byte {
	sum := notifications.FingerprintSemantic(
		enqueue.ProviderKey, enqueue.Recipient, enqueue.TemplateKey, enqueue.Locale,
		enqueue.Parameters,
		notifications.ResolvedTemplate{
			ExternalTemplateName: enqueue.ExtTemplateName,
			ExternalLanguageCode: enqueue.ExtLanguageCode,
			ParameterOrder:       enqueue.ExtParameterOrder,
		})
	return sum[:]
}

// ClaimNotification leases one due notification. Row-level SKIP LOCKED
// keeps multi-instance dispatchers apart; the bounded lease (not a
// permanent state) makes crashed claims eligible again. A claimed row
// whose send-start marker is already set must NOT be sent again: the
// dispatcher routes it to ambiguous without provider I/O.
func (d Devices) ClaimNotification(ctx context.Context, owner string, lease time.Duration, now time.Time) (notifications.ClaimedNotification, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	leaseUntil := pgtype.Timestamptz{Time: now.Add(lease).UTC(), Valid: true}
	row, err := sqlcgen.New(d.pool).ClaimNotification(ctx, sqlcgen.ClaimNotificationParams{
		LeaseOwner: pgText(owner), LeaseUntil: leaseUntil,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notifications.ClaimedNotification{}, false, nil
		}
		return notifications.ClaimedNotification{}, false, apperr.Wrap(apperr.Internal, "notification claim", redact(err))
	}
	parameters := map[string]string{}
	if len(row.Parameters) > 0 {
		if err := json.Unmarshal(row.Parameters, &parameters); err != nil {
			return notifications.ClaimedNotification{}, false, apperr.Wrap(apperr.Internal, "notification claim", redact(err))
		}
	}
	return notifications.ClaimedNotification{
		ID: mustGoID(row.ID), ProviderKey: row.ProviderKey, IdempotencyKey: row.IdempotencyKey,
		Recipient: row.Recipient, TemplateKey: row.TemplateKey, Locale: row.Locale,
		Parameters:      parameters,
		ExtTemplateName: row.ExtTemplateName, ExtLanguageCode: row.ExtLanguageCode,
		ExtParameterOrder: row.ExtParameterOrder,
		Dispatch:          notifications.DispatchStatus(row.DispatchStatus), AttemptCount: row.AttemptCount,
		SendStarted: row.SendStartedAt.Valid,
		LeaseOwner:  row.LeaseOwner.String, LeaseGeneration: row.LeaseGeneration,
	}, true, nil
}

// MarkNotificationSendStarted durably records that the current dispatch
// attempt actually started before any provider HTTP. It runs in its own
// short transaction; the claim transaction is long released.
func (d Devices) MarkNotificationSendStarted(ctx context.Context, id string, owner string, generation int64) (notifications.FinishResult, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	affected, err := sqlcgen.New(d.pool).MarkNotificationSendStarted(ctx, sqlcgen.MarkNotificationSendStartedParams{
		ID: mustPgID(id), LeaseOwner: pgText(owner), LeaseGeneration: generation,
	})
	if err != nil {
		return notifications.FinishStale, apperr.Wrap(apperr.Internal, "notification send-start", redact(err))
	}
	if affected == 0 {
		return notifications.FinishStale, nil
	}
	return notifications.FinishApplied, nil
}

// fencedFinish runs one fenced terminal transition against a
// non-terminal row. Stale claims affect zero rows: safe no-op.
func (d Devices) fencedFinish(ctx context.Context, id string, owner string, generation int64, op string,
	exec func(q *sqlcgen.Queries) (int64, error)) (notifications.FinishResult, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	affected, err := exec(sqlcgen.New(d.pool))
	if err != nil {
		return notifications.FinishStale, apperr.Wrap(apperr.Internal, "notification "+op, redact(err))
	}
	if affected == 0 {
		return notifications.FinishStale, nil
	}
	return notifications.FinishApplied, nil
}

// FinishNotificationAccepted records provider acceptance with exactly
// one message ID. A provider-message-ID collision fails safely here:
// the row keeps its send-start marker and converges to ambiguous on
// the next claim instead of reassigning ownership.
func (d Devices) FinishNotificationAccepted(ctx context.Context, id string, owner string, generation int64, providerMessageID string) (notifications.FinishResult, error) {
	return d.fencedFinish(ctx, id, owner, generation, "accept", func(q *sqlcgen.Queries) (int64, error) {
		return q.FinishNotificationAccepted(ctx, sqlcgen.FinishNotificationAcceptedParams{
			ID: mustPgID(id), LeaseOwner: pgText(owner), LeaseGeneration: generation,
			ProviderMessageID: pgText(providerMessageID),
		})
	})
}

// FinishNotificationRetry records an explicitly safe retry outcome and
// clears the send-start marker, authorizing one new send attempt.
func (d Devices) FinishNotificationRetry(ctx context.Context, id string, owner string, generation int64, next time.Time, code string) (notifications.FinishResult, error) {
	return d.fencedFinish(ctx, id, owner, generation, "retry", func(q *sqlcgen.Queries) (int64, error) {
		return q.FinishNotificationRetry(ctx, sqlcgen.FinishNotificationRetryParams{
			ID: mustPgID(id), LeaseOwner: pgText(owner), LeaseGeneration: generation,
			NextAttemptAt: pgTime(next), LastErrorCode: pgText(code),
		})
	})
}

// FinishNotificationBlocked records a terminal non-retryable outcome.
func (d Devices) FinishNotificationBlocked(ctx context.Context, id string, owner string, generation int64, code string) (notifications.FinishResult, error) {
	return d.fencedFinish(ctx, id, owner, generation, "block", func(q *sqlcgen.Queries) (int64, error) {
		return q.FinishNotificationBlocked(ctx, sqlcgen.FinishNotificationBlockedParams{
			ID: mustPgID(id), LeaseOwner: pgText(owner), LeaseGeneration: generation,
			LastErrorCode: pgText(code),
		})
	})
}

// FinishNotificationAmbiguous records an unknown remote outcome.
// Terminal for automatic retry: only a new explicit notification may
// supersede it after human assessment.
func (d Devices) FinishNotificationAmbiguous(ctx context.Context, id string, owner string, generation int64, code string) (notifications.FinishResult, error) {
	return d.fencedFinish(ctx, id, owner, generation, "ambiguous", func(q *sqlcgen.Queries) (int64, error) {
		return q.FinishNotificationAmbiguous(ctx, sqlcgen.FinishNotificationAmbiguousParams{
			ID: mustPgID(id), LeaseOwner: pgText(owner), LeaseGeneration: generation,
			LastErrorCode: pgText(code),
		})
	})
}

// mustPgID converts a domain UUID string; enqueue/claim paths always
// carry valid UUIDs.
func mustPgID(id string) pgtype.UUID {
	converted, err := parseUUID(id)
	if err != nil {
		panic("notification id must be a UUID")
	}
	return converted
}

// mustGoID converts a stored UUID back to domain string form.
func mustGoID(id pgtype.UUID) string {
	return uuidString(id)
}

// LookupByProviderMessage correlates a status callback to exactly one
// notification by provider message ID. Unknown IDs report found=false;
// correlation never uses recipient data.
func (d Devices) LookupByProviderMessage(ctx context.Context, providerKey, providerMessageID string) (string, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	row, err := sqlcgen.New(d.pool).GetNotificationByProviderMessage(ctx, sqlcgen.GetNotificationByProviderMessageParams{
		ProviderKey: providerKey, ProviderMessageID: pgText(providerMessageID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, apperr.Wrap(apperr.Internal, "notification lookup", redact(err))
	}
	return mustGoID(row.ID), true, nil
}

// ApplyDeliveryStatus records one normalized provider callback
// atomically: history insert plus conditional current-state advance in
// a single transaction, so history and current state can never mix.
// Duplicate callbacks dedupe via the event fingerprint; older or
// UNKNOWN events record history without regressing current state.
//
// Concurrency: the notification row is locked FOR UPDATE inside this
// same transaction before history insert and the ordering decision, so
// concurrent callbacks serialize on the one row they share and the
// decision always uses post-lock current state. Lock order is fixed:
// notification row, then history insert, then current update. Different
// notifications never block each other.
func (d Devices) ApplyDeliveryStatus(ctx context.Context, notificationID string, event notifications.DeliveryEvent) (notifications.DeliveryOutcome, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	id, err := parseUUID(notificationID)
	if err != nil {
		return notifications.DeliveryOutcome{}, apperr.New(apperr.InvalidInput, "invalid notification id")
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return notifications.DeliveryOutcome{}, apperr.Wrap(apperr.Internal, "notification delivery", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	current, err := q.GetNotificationByIDForUpdate(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notifications.DeliveryOutcome{}, apperr.New(apperr.NotFound, "notification not found")
		}
		return notifications.DeliveryOutcome{}, apperr.Wrap(apperr.Internal, "notification delivery", redact(err))
	}
	if !current.ProviderMessageID.Valid || current.ProviderMessageID.String != event.ProviderMessageID {
		_ = tx.Rollback(ctx)
		return notifications.DeliveryOutcome{}, apperr.New(apperr.Conflict, "provider message correlation mismatch")
	}
	var providerAt pgtype.Timestamptz
	if event.ProviderTimestamp != nil {
		providerAt = pgTime(*event.ProviderTimestamp)
	}
	_, err = q.InsertDeliveryStatusHistory(ctx, sqlcgen.InsertDeliveryStatusHistoryParams{
		NotificationID: id, ProviderKey: current.ProviderKey,
		ProviderMessageID: event.ProviderMessageID,
		ProviderStatusRaw: event.RawStatus, CanonicalStatus: string(event.Canonical),
		ProviderTimestamp: providerAt, EventFingerprint: event.Fingerprint[:],
		ProviderErrorCode: pgText(event.ErrorCode),
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return notifications.DeliveryOutcome{}, apperr.Wrap(apperr.Internal, "notification delivery", redact(err))
		}
		// Duplicate callback: committed nothing, changed nothing.
		if err := tx.Commit(ctx); err != nil {
			return notifications.DeliveryOutcome{}, apperr.Wrap(apperr.Internal, "notification delivery", redact(err))
		}
		return notifications.DeliveryOutcome{}, nil
	}
	outcome := notifications.DeliveryOutcome{HistoryInserted: true}
	var currentAt *time.Time
	if current.DeliveryStatusAt.Valid {
		at := current.DeliveryStatusAt.Time
		currentAt = &at
	}
	if notifications.AdvanceDelivery(
		notifications.DeliveryStatus(current.DeliveryStatus), currentAt,
		event.Canonical, event.ProviderTimestamp) {
		if _, err := q.UpdateNotificationDelivery(ctx, sqlcgen.UpdateNotificationDeliveryParams{
			ID: id, DeliveryStatus: string(event.Canonical), DeliveryStatusAt: providerAt,
		}); err != nil {
			return notifications.DeliveryOutcome{}, apperr.Wrap(apperr.Internal, "notification delivery", redact(err))
		}
		outcome.CurrentAdvanced = true
	}
	if err := tx.Commit(ctx); err != nil {
		return notifications.DeliveryOutcome{}, apperr.Wrap(apperr.Internal, "notification delivery", redact(err))
	}
	return outcome, nil
}

// NotificationQueueStats is the operational outbox summary for
// operator visibility. OldestPending is the earliest queued creation
// time; nil when no pending/retry work exists.
type NotificationQueueStats struct {
	Pending       int64
	Retry         int64
	Accepted      int64
	Blocked       int64
	Ambiguous     int64
	OldestPending *time.Time
}

// NotificationQueueStats returns outbox counts plus the oldest pending
// age. Safe low-cardinality fields only.
func (d Devices) NotificationQueueStats(ctx context.Context) (NotificationQueueStats, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	row, err := sqlcgen.New(d.pool).NotificationQueueStats(ctx)
	if err != nil {
		return NotificationQueueStats{}, apperr.Wrap(apperr.Internal, "notification stats", redact(err))
	}
	stats := NotificationQueueStats{
		Pending: row.Pending, Retry: row.Retry, Accepted: row.Accepted,
		Blocked: row.Blocked, Ambiguous: row.Ambiguous,
	}
	if row.OldestPending.Valid {
		at := row.OldestPending.Time
		stats.OldestPending = &at
	}
	return stats, nil
}

// NotificationStatus is the safe operator-visible notification state:
// identity, template, dispatch/delivery, attempts, and machine codes.
// Recipient and parameter contents are never included.
type NotificationStatus struct {
	ID                string
	ProviderKey       string
	TemplateKey       string
	Locale            string
	Dispatch          notifications.DispatchStatus
	Delivery          notifications.DeliveryStatus
	AttemptCount      int32
	ProviderMessageID string
	LastErrorCode     string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// GetNotificationStatus reads one notification's safe operator state.
func (d Devices) GetNotificationStatus(ctx context.Context, id string) (NotificationStatus, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	parsed, err := parseUUID(id)
	if err != nil {
		return NotificationStatus{}, apperr.New(apperr.InvalidInput, "invalid notification id")
	}
	row, err := sqlcgen.New(d.pool).GetNotificationByID(ctx, parsed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return NotificationStatus{}, apperr.New(apperr.NotFound, "notification not found")
		}
		return NotificationStatus{}, apperr.Wrap(apperr.Internal, "notification status", redact(err))
	}
	status := NotificationStatus{
		ID: mustGoID(row.ID), ProviderKey: row.ProviderKey,
		TemplateKey: row.TemplateKey, Locale: row.Locale,
		Dispatch:      notifications.DispatchStatus(row.DispatchStatus),
		Delivery:      notifications.DeliveryStatus(row.DeliveryStatus),
		AttemptCount:  row.AttemptCount,
		LastErrorCode: row.LastErrorCode.String,
		CreatedAt:     row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
	if row.ProviderMessageID.Valid {
		status.ProviderMessageID = row.ProviderMessageID.String
	}
	return status, nil
}
