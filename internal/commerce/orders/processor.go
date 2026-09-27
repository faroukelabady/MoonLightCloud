package orders

import (
	"context"
	"log/slog"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/commerce"
)

// Webhook event processing states.
const (
	WebhookPending   = "pending"
	WebhookRetry     = "retry"
	WebhookProcessed = "processed"
	WebhookBlocked   = "blocked"
)

// Processor tunables.
const (
	// ProcessorInterval is the steady-state scan cadence. Webhook
	// ingestion wakes the worker immediately; the interval only bounds
	// retry-due discovery.
	ProcessorInterval = 15 * time.Second
	// ProcessorBatchSize bounds one scan pass. The worker never fans out
	// unbounded goroutines: events process sequentially.
	ProcessorBatchSize = 25
	// ClaimLease bounds one claim: crashed workers release their events
	// automatically after this horizon.
	ClaimLease = 5 * time.Minute
	// MaxRetryDelay caps scheduled backoff.
	MaxRetryDelay = time.Hour
)

// WebhookStore is the durable inbox boundary for the processor.
type WebhookStore interface {
	ClaimOrderWebhookEvent(ctx context.Context, owner string, lease time.Duration, now time.Time) (ClaimedWebhookEvent, bool, error)
	FinishOrderWebhookEvent(ctx context.Context, providerKey, deliveryID, status string, nextAttemptAt *time.Time, processed bool, errorCode string) error
}

// ClaimedWebhookEvent is one leased inbox row.
type ClaimedWebhookEvent struct {
	ProviderKey     string
	DeliveryID      string
	Topic           WebhookTopic
	ExternalOrderID string
	AttemptCount    int32
}

// Processor drains webhook events: claim with a bounded lease, release
// the transaction, reconcile over provider I/O, then finish. Provider
// HTTP never runs inside a held row lock or write transaction.
type Processor struct {
	webhooks WebhookStore
	service  *OrderService
	clock    Clock
	log      *slog.Logger
	wake     chan struct{}
	interval time.Duration
	owner    string
}

// Clock abstracts time for deterministic tests.
type Clock interface {
	Now() time.Time
}

// SystemClock is production time.
type SystemClock struct{}

// Now implements Clock.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// NewProcessor wires the worker over the inbox store and the
// reconciliation service.
func NewProcessor(webhooks WebhookStore, service *OrderService, clock Clock, owner string, log *slog.Logger) *Processor {
	return &Processor{
		webhooks: webhooks, service: service, clock: clock,
		log: log, wake: make(chan struct{}, 1),
		interval: ProcessorInterval, owner: owner,
	}
}

// Notify wakes the worker after webhook ingestion. Non-blocking.
func (p *Processor) Notify() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// Run drains on startup, on wake, and on interval until cancellation.
func (p *Processor) Run(ctx context.Context) {
	p.drain(ctx)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
			p.drain(ctx)
		case <-ticker.C:
			p.drain(ctx)
		}
	}
}

func (p *Processor) drain(ctx context.Context) {
	for i := 0; i < ProcessorBatchSize; i++ {
		if ctx.Err() != nil {
			return
		}
		claimed, ok, err := p.webhooks.ClaimOrderWebhookEvent(ctx, p.owner, ClaimLease, p.clock.Now())
		if err != nil {
			p.logError("order webhook claim failed", "err", err.Error())
			return
		}
		if !ok {
			return
		}
		p.processOne(ctx, claimed)
	}
}

// processOne reconciles one claimed event. The claim transaction is
// already released: provider I/O and projection run outside any held
// lock, and Finish records the outcome durably afterwards.
func (p *Processor) processOne(ctx context.Context, claimed ClaimedWebhookEvent) {
	now := p.clock.Now()
	finish := func(status string, next *time.Time, processed bool, code string) {
		if err := p.webhooks.FinishOrderWebhookEvent(ctx,
			claimed.ProviderKey, claimed.DeliveryID, status, next, processed, code); err != nil {
			p.logError("order webhook finish failed",
				"provider", claimed.ProviderKey, "delivery", claimed.DeliveryID, "err", err.Error())
		}
	}
	var result ReconcileResult
	var err error
	if claimed.Topic.IsDeletion() {
		result, err = p.service.ReconcileDeletion(ctx, claimed.ProviderKey, claimed.ExternalOrderID)
	} else {
		result, err = p.service.ReconcileOrder(ctx, claimed.ProviderKey, claimed.ExternalOrderID)
	}
	if err == nil {
		p.logInfo("order webhook processed",
			"provider", claimed.ProviderKey, "delivery", claimed.DeliveryID,
			"order", claimed.ExternalOrderID, "revision", result.Revision, "changed", result.Changed)
		finish(WebhookProcessed, nil, true, "")
		return
	}
	if code, blocked := IsBlocked(err); blocked {
		p.logInfo("order webhook blocked",
			"provider", claimed.ProviderKey, "delivery", claimed.DeliveryID, "code", code)
		finish(WebhookBlocked, nil, false, code)
		return
	}
	var providerErr *commerce.ProviderError
	if asProviderError(err, &providerErr) {
		switch providerErr.Kind {
		case commerce.ErrorTemporary, commerce.ErrorRateLimited:
			next := now.Add(webhookBackoff(int(claimed.AttemptCount)))
			if providerErr.Kind == commerce.ErrorRateLimited {
				if after, hasAfter := providerErr.GetRetryAfter(); hasAfter {
					if hinted := now.Add(after); hinted.After(next) && after <= MaxRetryDelay {
						next = hinted
					} else if after > MaxRetryDelay {
						next = now.Add(MaxRetryDelay)
					}
				}
			}
			p.logInfo("order webhook retry",
				"provider", claimed.ProviderKey, "delivery", claimed.DeliveryID,
				"kind", string(providerErr.Kind), "next", next.UTC().Format(time.RFC3339))
			finish(WebhookRetry, &next, false, "ORDER_PROVIDER_RETRY")
			return
		default:
			code := "ORDER_PROVIDER_" + string(providerErr.Kind)
			p.logInfo("order webhook blocked",
				"provider", claimed.ProviderKey, "delivery", claimed.DeliveryID, "code", code)
			finish(WebhookBlocked, nil, false, code)
			return
		}
	}
	// Unknown failures (typically infrastructure) retry with backoff
	// rather than blocking on an unclassified cause.
	next := now.Add(webhookBackoff(int(claimed.AttemptCount)))
	p.logInfo("order webhook retry",
		"provider", claimed.ProviderKey, "delivery", claimed.DeliveryID, "next", next.UTC().Format(time.RFC3339))
	finish(WebhookRetry, &next, false, "ORDER_RECONCILE_ERROR")
}

// webhookBackoff spaces retries: 10s doubling per attempt, capped.
func webhookBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := 10 * time.Second
	for i := 1; i < attempt && delay < MaxRetryDelay; i++ {
		delay *= 2
	}
	if delay > MaxRetryDelay {
		return MaxRetryDelay
	}
	return delay
}

func (p *Processor) logInfo(msg string, args ...any) {
	if p.log != nil {
		p.log.Info(msg, args...)
	}
}

func (p *Processor) logError(msg string, args ...any) {
	if p.log != nil {
		p.log.Error(msg, args...)
	}
}
