package notifications

import (
	"context"
	"log/slog"
	"time"
)

const (
	// DispatcherInterval is the steady-state scan cadence. Enqueue CLI
	// calls and webhook ingestion wake the worker immediately; the
	// interval only bounds retry-due discovery.
	DispatcherInterval = 15 * time.Second
	// DispatcherBatchSize bounds one scan pass. The worker never fans
	// out unbounded goroutines: notifications dispatch sequentially.
	DispatcherBatchSize = 25
	// DispatchLease bounds one claim: crashed workers release their
	// notifications automatically after this horizon.
	DispatchLease = 5 * time.Minute
	// DispatchMaxDelay caps scheduled backoff.
	DispatchMaxDelay = time.Hour
)

// Clock abstracts time for deterministic tests.
type Clock interface {
	Now() time.Time
}

// SystemClock is production time.
type SystemClock struct{}

// Now implements Clock.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// Dispatcher drains the durable outbox: claim with a bounded lease,
// release the transaction, mark send-start durably, perform provider
// HTTP with no DB locks held, then finish under the claim token.
// Provider HTTP never runs inside a held row lock or write
// transaction.
type Dispatcher struct {
	store     DispatchStore
	providers *Registry
	clock     Clock
	log       *slog.Logger
	wake      chan struct{}
	interval  time.Duration
	owner     string
}

// NewDispatcher wires the worker over the dispatch store and the
// provider registry.
func NewDispatcher(store DispatchStore, providers *Registry, clock Clock, owner string, log *slog.Logger) *Dispatcher {
	return &Dispatcher{
		store: store, providers: providers, clock: clock,
		log: log, wake: make(chan struct{}, 1),
		interval: DispatcherInterval, owner: owner,
	}
}

// Notify wakes the worker after enqueue. Non-blocking.
func (d *Dispatcher) Notify() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Run drains on startup, on wake, and on interval until cancellation.
func (d *Dispatcher) Run(ctx context.Context) {
	d.drain(ctx)
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
			d.drain(ctx)
		case <-ticker.C:
			d.drain(ctx)
		}
	}
}

// DrainForTest runs one bounded scan pass. Test hook only:
// production uses Run with wakeups and intervals.
func (d *Dispatcher) DrainForTest(ctx context.Context) {
	d.drain(ctx)
}

func (d *Dispatcher) drain(ctx context.Context) {
	for i := 0; i < DispatcherBatchSize; i++ {
		if ctx.Err() != nil {
			return
		}
		claimed, ok, err := d.store.ClaimNotification(ctx, d.owner, DispatchLease, d.clock.Now())
		if err != nil {
			d.logError("notification claim failed", "err", err.Error())
			return
		}
		if !ok {
			return
		}
		d.processOne(ctx, claimed)
	}
}

// processOne dispatches one claimed notification. Crash states route by
// durable evidence, never by assumption:
//
//   - send-start marker already set → a previous attempt may have
//     reached the provider: ambiguous, no provider I/O.
//   - provider returns exactly one message ID → accepted.
//   - explicit safely-retryable provider outcome → retry with backoff.
//   - anything else unknown → ambiguous, never automatic retry.
func (d *Dispatcher) processOne(ctx context.Context, claimed ClaimedNotification) {
	finish := func(result FinishResult, err error, what string) {
		if err != nil {
			d.logError("notification finish failed",
				"notification", claimed.ID, "err", err.Error())
			return
		}
		if result == FinishStale {
			d.logInfo("notification finish stale",
				"notification", claimed.ID,
				"lease_owner", claimed.LeaseOwner, "lease_generation", claimed.LeaseGeneration)
			return
		}
		d.logInfo("notification "+what, "notification", claimed.ID,
			"provider", claimed.ProviderKey, "attempt", claimed.AttemptCount)
	}
	if claimed.SendStarted {
		// A previous attempt began sending without recording an
		// explicit safe outcome. The provider may already have the
		// message: converge to ambiguous without touching the network.
		result, err := d.store.FinishNotificationAmbiguous(ctx,
			claimed.ID, claimed.LeaseOwner, claimed.LeaseGeneration, CodeSendAmbiguous)
		finish(result, err, "ambiguous")
		return
	}
	provider, err := d.providers.Get(ProviderKey(claimed.ProviderKey))
	if err != nil {
		result, ferr := d.store.FinishNotificationBlocked(ctx,
			claimed.ID, claimed.LeaseOwner, claimed.LeaseGeneration, CodeProviderUnknown)
		finish(result, ferr, "blocked")
		return
	}
	started, err := d.store.MarkNotificationSendStarted(ctx,
		claimed.ID, claimed.LeaseOwner, claimed.LeaseGeneration)
	if err != nil {
		d.logError("notification send-start failed", "notification", claimed.ID, "err", err.Error())
		return
	}
	if started == FinishStale {
		return
	}
	sendReq := TemplateSendRequest{
		ProviderKey: provider.Key(), Recipient: claimed.Recipient,
		Resolved: ResolvedTemplate{
			TemplateKey: claimed.TemplateKey, Locale: claimed.Locale,
			ExternalTemplateName: claimed.ExtTemplateName,
			ExternalLanguageCode: claimed.ExtLanguageCode,
			ParameterOrder:       append([]string(nil), claimed.ExtParameterOrder...),
			Parameters:           claimed.Parameters,
		},
	}
	sendResult, err := provider.SendTemplate(ctx, sendReq)
	if err == nil {
		result, ferr := d.store.FinishNotificationAccepted(ctx,
			claimed.ID, claimed.LeaseOwner, claimed.LeaseGeneration, sendResult.ProviderMessageID)
		if ferr != nil {
			// Accepted by the provider but the durable record failed
			// (for example a message-ID collision): the send-start
			// marker stands, so the next claim converges to ambiguous
			// instead of blindly resending.
			d.logError("notification accept persistence failed",
				"notification", claimed.ID, "err", ferr.Error())
			return
		}
		finish(result, nil, "accepted")
		return
	}
	if ctx.Err() != nil {
		// Caller gone after send start with no provider response
		// known: leave the marker; the next claim converges to
		// ambiguous. Never finish terminal work on a dead context.
		d.logInfo("notification send cancelled", "notification", claimed.ID)
		return
	}
	var notificationErr *NotificationError
	if AsNotificationError(err, &notificationErr) {
		switch notificationErr.Kind {
		case ErrorTemporary, ErrorRateLimited:
			next := d.clock.Now().Add(dispatchBackoff(int(claimed.AttemptCount)))
			if notificationErr.Kind == ErrorRateLimited {
				if after, hasAfter := notificationErr.GetRetryAfter(); hasAfter {
					if hinted := d.clock.Now().Add(after); hinted.After(next) && after <= DispatchMaxDelay {
						next = hinted
					} else if after > DispatchMaxDelay {
						next = d.clock.Now().Add(DispatchMaxDelay)
					}
				}
			}
			code := CodeProviderRetry
			if notificationErr.Kind == ErrorRateLimited {
				code = CodeProviderRateLimited
			}
			result, ferr := d.store.FinishNotificationRetry(ctx,
				claimed.ID, claimed.LeaseOwner, claimed.LeaseGeneration, next, code)
			finish(result, ferr, "retry")
			return
		case ErrorAmbiguous:
			result, ferr := d.store.FinishNotificationAmbiguous(ctx,
				claimed.ID, claimed.LeaseOwner, claimed.LeaseGeneration, CodeSendAmbiguous)
			finish(result, ferr, "ambiguous")
			return
		default:
			result, ferr := d.store.FinishNotificationBlocked(ctx,
				claimed.ID, claimed.LeaseOwner, claimed.LeaseGeneration, providerErrorCode(notificationErr.Kind))
			finish(result, ferr, "blocked")
			return
		}
	}
	// Unknown failures after send start are ambiguous, never retried:
	// the request may already have reached the provider.
	result, ferr := d.store.FinishNotificationAmbiguous(ctx,
		claimed.ID, claimed.LeaseOwner, claimed.LeaseGeneration, CodeSendAmbiguous)
	finish(result, ferr, "ambiguous")
}

// providerErrorCode maps terminal provider kinds to machine codes.
func providerErrorCode(kind ErrorKind) string {
	switch kind {
	case ErrorAuthentication:
		return CodeProviderAuth
	case ErrorValidation:
		return CodeProviderValidation
	case ErrorConflict:
		return CodeProviderConflict
	default:
		return CodeDispatchError
	}
}

// dispatchBackoff spaces retries: 10s doubling per attempt, capped.
func dispatchBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := 10 * time.Second
	for i := 1; i < attempt && delay < DispatchMaxDelay; i++ {
		delay *= 2
	}
	if delay > DispatchMaxDelay {
		return DispatchMaxDelay
	}
	return delay
}

func (d *Dispatcher) logInfo(msg string, args ...any) {
	if d.log != nil {
		d.log.Info(msg, args...)
	}
}

func (d *Dispatcher) logError(msg string, args ...any) {
	if d.log != nil {
		d.log.Error(msg, args...)
	}
}
