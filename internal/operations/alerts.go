package operations

import (
	"context"
	"errors"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
)

// Notifier is the frozen provider-neutral enqueue boundary. The operations
// engine never imports notifications/whatsapp and never sends directly.
type Notifier interface {
	EnqueueTemplate(ctx context.Context, req notifications.EnqueueTemplateRequest) (notifications.EnqueueResult, error)
}

// AlertProcessor drains pending alert deliveries: snapshot-first enqueue
// through Phase 7A with deterministic idempotency, adopting the same
// notification on crash-retry. One delivery, one notification row.
type AlertProcessor struct {
	store   Store
	service *Service
	notify  Notifier
	newID   func() string
	now     func() time.Time
	metrics *Metrics
}

func NewAlertProcessor(store Store, service *Service, notify Notifier, newID func() string, now func() time.Time, metrics *Metrics) *AlertProcessor {
	if now == nil {
		now = time.Now
	}
	return &AlertProcessor{store: store, service: service, notify: notify, newID: newID, now: now, metrics: metrics}
}

// BuildDeliveries composes immutable per-recipient delivery snapshots for
// one incident event without writing anything. Entries whose body cannot
// be built carry LastErrorCode preset so the atomic commit records them
// blocked in the same transaction. Zero recipients yields zero
// deliveries; callers distinguish that from a missing intent via the
// materialization flags.
func (p *AlertProcessor) BuildDeliveries(ctx context.Context, incident Incident, event string) ([]Delivery, error) {
	recipients, err := p.store.ListEnabledOpsRecipients(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Delivery, 0, len(recipients))
	for _, r := range recipients {
		template := TemplateOpen
		if event == EventResolved {
			template = TemplateResolved
		}
		body, berr := AlertBody(r.Locale, event, incident.Rule, SafeSubject(incident.SubjectID, ""), incident.OpenedAt, "")
		if berr != nil || ValidateLabel(r.Label) != nil {
			deliveryID := p.newID()
			out = append(out, Delivery{
				ID: deliveryID, IncidentID: incident.ID, RecipientID: r.ID, Event: event,
				ProviderKey: r.ProviderKey, RecipientSnapshot: r.Recipient,
				Locale: r.Locale, TemplateKey: template, Body: "(withheld: too large)",
				Fingerprint:     Fingerprint(incident.Rule, incident.ID, event, r.Locale, "(withheld)"),
				NotificationKey: AlertIdempotencyKey(incident.ID, event, deliveryID),
				LastErrorCode:   strptr(CodeBodyTooLarge),
			})
			continue
		}
		deliveryID := p.newID()
		out = append(out, Delivery{
			ID: deliveryID, IncidentID: incident.ID, RecipientID: r.ID, Event: event,
			ProviderKey: r.ProviderKey, RecipientSnapshot: r.Recipient,
			Locale: r.Locale, TemplateKey: template, Body: body,
			Fingerprint:     Fingerprint(incident.Rule, incident.ID, event, r.Locale, body),
			NotificationKey: AlertIdempotencyKey(incident.ID, event, deliveryID),
		})
	}
	return out, nil
}

func strptr(s string) *string { return &s }

// ProcessOne claims the oldest pending delivery and enqueues it. Missing
// template mappings block the delivery (dashboard-visible config problem)
// without touching Phase 7A; transient failures stay pending for retry.
func (p *AlertProcessor) ProcessOne(ctx context.Context) (bool, error) {
	delivery, ok, err := p.store.ClaimDelivery(ctx)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	now := p.now().UTC()
	result, err := p.notify.EnqueueTemplate(ctx, notifications.EnqueueTemplateRequest{
		ProviderKey: delivery.ProviderKey, IdempotencyKey: delivery.NotificationKey,
		Recipient: delivery.RecipientSnapshot, TemplateKey: delivery.TemplateKey,
		Locale: delivery.Locale, Parameters: map[string]string{ParamAlertBody: delivery.Body},
	})
	if err != nil {
		if isMappingMissing(err) {
			p.metrics.deliveriesBlocked.Add(1)
			if ferr := p.store.FinishOpsDeliveryBlocked(ctx, delivery.ID, CodeNoMapping, now); ferr != nil {
				return false, ferr
			}
			return true, nil
		}
		// Transient (database/driver) failure: stay pending for retry.
		return false, err
	}
	// Crash-after-enqueue converges here: the same idempotency key adopts
	// the same notification ID on retry (Created=false, same ID).
	if err := p.store.FinishOpsDeliverySent(ctx, delivery.ID, result.ID, now); err != nil {
		return false, err
	}
	p.metrics.deliveriesEnqueued.Add(1)
	return true, nil
}

func isMappingMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range []string{"NOTIFICATION_TEMPLATE_MAPPING_MISSING", "template mapping"} {
		if len(msg) >= len(marker) {
			for i := 0; i+len(marker) <= len(msg); i++ {
				if msg[i:i+len(marker)] == marker {
					return true
				}
			}
		}
	}
	return false
}

func isConflict(err error) bool {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae.Kind == apperr.Conflict
	}
	return false
}
