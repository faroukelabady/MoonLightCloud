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

// CreateDeliveries snapshots currently-enabled recipients for one incident
// event. Incident existence never depends on notification success; with
// zero recipients the incident simply has no deliveries.
func (p *AlertProcessor) CreateDeliveries(ctx context.Context, incident Incident, event string) error {
	recipients, err := p.store.ListEnabledOpsRecipients(ctx)
	if err != nil {
		return err
	}
	now := p.now().UTC()
	for _, r := range recipients {
		body, berr := AlertBody(r.Locale, event, incident.Rule, SafeSubject(incident.SubjectID, ""), incident.OpenedAt, "")
		if berr != nil {
			// Body too large by construction: record a blocked delivery so
			// the dashboard shows the configuration problem explicitly.
			if err := p.blockDelivery(ctx, incident, r, event, CodeBodyTooLarge, now); err != nil {
				return err
			}
			continue
		}
		if err := ValidateLabel(r.Label); err != nil {
			if berr := p.blockDelivery(ctx, incident, r, event, CodeBodyTooLarge, now); berr != nil {
				return berr
			}
			continue
		}
		deliveryID := p.newID()
		template := TemplateOpen
		if event == EventResolved {
			template = TemplateResolved
		}
		_, err := p.store.CreateDelivery(ctx, Delivery{
			ID: deliveryID, IncidentID: incident.ID, RecipientID: r.ID, Event: event,
			ProviderKey: r.ProviderKey, RecipientSnapshot: r.Recipient,
			Locale: r.Locale, TemplateKey: template, Body: body,
			Fingerprint:     Fingerprint(incident.Rule, incident.ID, event, r.Locale, body),
			NotificationKey: AlertIdempotencyKey(incident.ID, event, deliveryID),
		}, now)
		if err != nil {
			if isConflict(err) {
				continue
			}
			return err
		}
	}
	return nil
}

func (p *AlertProcessor) blockDelivery(ctx context.Context, incident Incident, r Recipient, event, code string, now time.Time) error {
	deliveryID := p.newID()
	template := TemplateOpen
	if event == EventResolved {
		template = TemplateResolved
	}
	del, err := p.store.CreateDelivery(ctx, Delivery{
		ID: deliveryID, IncidentID: incident.ID, RecipientID: r.ID, Event: event,
		ProviderKey: r.ProviderKey, RecipientSnapshot: r.Recipient,
		Locale: r.Locale, TemplateKey: template, Body: "(withheld: too large)",
		Fingerprint:     Fingerprint(incident.Rule, incident.ID, event, r.Locale, "(withheld)"),
		NotificationKey: AlertIdempotencyKey(incident.ID, event, deliveryID),
	}, now)
	if err != nil {
		if isConflict(err) {
			return nil
		}
		return err
	}
	p.metrics.deliveriesBlocked.Add(1)
	return p.store.FinishOpsDeliveryBlocked(ctx, del.ID, code, now)
}

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
