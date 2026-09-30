package operations

import (
	"context"
	"time"
)

// Detector evaluates the eight v1 rules over frozen domain state and
// maintains incidents. Every step is bounded and idempotent: repeated
// scans with unchanged state converge on identical rows.
type Detector struct {
	store   Store
	service *Service
	alerts  *AlertProcessor
	cfg     DetectorConfig
	newID   func() string
	now     func() time.Time
	metrics *Metrics
}

// DetectorConfig carries thresholds and batch sizes (validated config).
type DetectorConfig struct {
	BatchSize           int
	OfflineAfter        time.Duration
	OnlineWindow        time.Duration
	SyncPendingStale    time.Duration
	SyncRunningStale    time.Duration
	ReportStale         time.Duration
	NotificationStale   time.Duration
	AutoSyncOnReconnect bool
}

func NewDetector(store Store, service *Service, alerts *AlertProcessor, cfg DetectorConfig, newID func() string, now func() time.Time, metrics *Metrics) *Detector {
	if now == nil {
		now = time.Now
	}
	return &Detector{store: store, service: service, alerts: alerts, cfg: cfg, newID: newID, now: now, metrics: metrics}
}

// Scan runs every rule once, oldest-first in bounded batches. A transient
// failure aborts the scan with a safe error; next tick retries. No
// incident state corrupts on failure. All detector writes are atomic,
// so crashes cannot strand intents; ambiguous legacy rows are left for
// explicit operator reconciliation (never auto-fabricated).
func (d *Detector) Scan(ctx context.Context) error {
	now := d.now().UTC()
	if err := d.scanOffline(ctx, now); err != nil {
		return err
	}
	if err := d.resolveOffline(ctx, now); err != nil {
		return err
	}
	if err := d.scanSyncFailed(ctx, now); err != nil {
		return err
	}
	if err := d.scanSyncStale(ctx, now); err != nil {
		return err
	}
	if err := d.resolveSyncStale(ctx, now); err != nil {
		return err
	}
	if err := d.scanReportBlocked(ctx, now); err != nil {
		return err
	}
	if err := d.scanReportStale(ctx, now); err != nil {
		return err
	}
	if err := d.resolveReportStale(ctx, now); err != nil {
		return err
	}
	if err := d.scanNotificationEvents(ctx, now); err != nil {
		return err
	}
	if err := d.scanNotificationStale(ctx, now); err != nil {
		return err
	}
	return d.resolveNotificationStale(ctx, now)
}

func (d *Detector) openStateful(ctx context.Context, rule, subjectType, subjectID string) (Incident, bool, error) {
	id := d.newID()
	at := d.now().UTC()
	deliveries, err := d.alerts.BuildDeliveries(ctx, Incident{ID: id, Rule: rule, SubjectType: subjectType, SubjectID: subjectID, OpenedAt: at}, EventOpened)
	if err != nil {
		return Incident{}, false, err
	}
	incident, created, err := d.service.OpenStatefulAtomic(ctx, id, rule, subjectType, subjectID, "", deliveries)
	if err != nil {
		return Incident{}, false, err
	}
	// N04: opened counting lives at call sites only; blocked-delivery
	// counting lives here only. Exactly one increment per new incident.
	if created {
		d.metrics.deliveriesBlocked.Add(int64(countBlocked(deliveries)))
	}
	return incident, created, nil
}

func (d *Detector) openEvent(ctx context.Context, rule, subjectType, subjectID, key string) (Incident, bool, error) {
	id := d.newID()
	at := d.now().UTC()
	deliveries, err := d.alerts.BuildDeliveries(ctx, Incident{ID: id, Rule: rule, SubjectType: subjectType, SubjectID: subjectID, OpenedAt: at}, EventOpened)
	if err != nil {
		return Incident{}, false, err
	}
	incident, created, err := d.service.OpenEventAtomic(ctx, id, rule, subjectType, subjectID, key, deliveries)
	if err != nil {
		return Incident{}, false, err
	}
	// N04: opened counting lives at call sites only; blocked-delivery
	// counting lives here only. Exactly one increment per new incident.
	if created {
		d.metrics.deliveriesBlocked.Add(int64(countBlocked(deliveries)))
	}
	return incident, created, nil
}

func countBlocked(deliveries []Delivery) int {
	n := 0
	for _, del := range deliveries {
		if del.LastErrorCode != nil {
			n++
		}
	}
	return n
}

// scanOffline opens DEVICE_OFFLINE for active devices unseen past the
// threshold. NEVER_SEEN devices (no presence row) are not outages.
func (d *Detector) scanOffline(ctx context.Context, now time.Time) error {
	candidates, err := d.store.ScanOffline(ctx, now.Add(-d.cfg.OfflineAfter), d.cfg.BatchSize)
	if err != nil {
		return err
	}
	for _, c := range candidates {
		if _, created, err := d.openStateful(ctx, RuleDeviceOffline, SubjectDevice, c.DeviceID); err != nil {
			return err
		} else if created {
			d.metrics.opened(RuleDeviceOffline)
		}
	}
	return nil
}

// resolveOffline clears offline incidents from active-incident state, so
// unrelated fresh devices never consume the batch. For each active
// offline incident: fresh presence resolves with reconnect healing armed
// atomically; revocation resolves distinctly with no recovery.
func (d *Detector) resolveOffline(ctx context.Context, now time.Time) error {
	freshEdge := now.Add(-d.cfg.OnlineWindow)
	active, err := d.store.ActiveByRule(ctx, RuleDeviceOffline, d.cfg.BatchSize)
	if err != nil {
		return err
	}
	for _, incident := range active {
		// Lifecycle takes precedence over presence: a revoked device
		// resolves as no-longer-active even with recent contact, and
		// never heals.
		status, ok, err := d.store.DeviceStatus(ctx, incident.SubjectID)
		if err != nil {
			return err
		}
		if ok && status != "active" {
			if _, err := d.resolveIncident(ctx, incident, ResolutionNoActive); err != nil {
				return err
			}
			continue
		}
		seen, ok, err := d.store.Presence(ctx, incident.SubjectID)
		if err != nil {
			return err
		}
		// Touch every checked row so the least-recently-checked ordering
		// round-robins: no batch starves later rows.
		_ = d.store.TouchObserved(ctx, incident.ID, now)
		if ok && seen != nil && !seen.UTC().Before(freshEdge) {
			var recovery *RecoveryIntent
			if d.cfg.AutoSyncOnReconnect {
				recovery = &RecoveryIntent{DeviceID: incident.SubjectID, Key: ReconnectIdempotencyKey(incident.ID)}
			}
			if _, err := d.resolveIncidentWithRecovery(ctx, incident, ResolutionReconnect, recovery); err != nil {
				return err
			}
			continue
		}
		// Still offline and active: touch so the least-recently-checked
		// ordering round-robins and no batch starves later rows.
		_ = d.store.TouchObserved(ctx, incident.ID, now)
	}
	return nil
}

func (d *Detector) resolveIncidentWithRecovery(ctx context.Context, incident Incident, code string, recovery *RecoveryIntent) (Incident, error) {
	deliveries, err := d.alerts.BuildDeliveries(ctx, incident, EventResolved)
	if err != nil {
		return Incident{}, err
	}
	resolved, won, err := d.service.ResolveAtomic(ctx, incident.ID, code, deliveries, recovery)
	if err != nil {
		return Incident{}, err
	}
	if !won {
		current, rerr := d.service.Get(ctx, incident.ID)
		if rerr != nil {
			return Incident{}, rerr
		}
		return current, nil
	}
	d.metrics.resolved(incident.Rule)
	d.metrics.deliveriesBlocked.Add(int64(countBlocked(deliveries)))
	return resolved, nil
}

func (d *Detector) resolveIncident(ctx context.Context, incident Incident, code string) (Incident, error) {
	deliveries, err := d.alerts.BuildDeliveries(ctx, incident, EventResolved)
	if err != nil {
		return Incident{}, err
	}
	resolved, won, err := d.service.ResolveAtomic(ctx, incident.ID, code, deliveries, nil)
	if err != nil {
		return Incident{}, err
	}
	if !won {
		// Lost the resolution race: the winner owns metrics and intents.
		current, rerr := d.service.Get(ctx, incident.ID)
		if rerr != nil {
			return Incident{}, rerr
		}
		return current, nil
	}
	d.metrics.resolved(incident.Rule)
	d.metrics.deliveriesBlocked.Add(int64(countBlocked(deliveries)))
	return resolved, nil
}

// scanSyncFailed opens one event incident per failed command (stable key).
func (d *Detector) scanSyncFailed(ctx context.Context, now time.Time) error {
	_ = now
	rows, err := d.store.ScanFailedCommands(ctx, d.cfg.BatchSize)
	if err != nil {
		return err
	}
	for _, r := range rows {
		key := "sync-command:" + r.CommandID
		if _, created, err := d.openEvent(ctx, RuleDeviceSyncFailed, SubjectSyncCommand, r.CommandID, key); err != nil {
			return err
		} else if created {
			d.metrics.opened(RuleDeviceSyncFailed)
		}
	}
	return nil
}

// scanSyncStale opens staleness for old non-terminal commands: pending
// uses the pending threshold, accepted/running the running threshold.
// Leased commands are covered by overall age (7C already self-heals the
// individual lease); single-expiry leases never alert alone.
func (d *Detector) scanSyncStale(ctx context.Context, now time.Time) error {
	pendingEdge := now.Add(-d.cfg.SyncPendingStale)
	runningEdge := now.Add(-d.cfg.SyncRunningStale)
	rows, err := d.store.ScanStaleCommands(ctx, pendingEdge, runningEdge, d.cfg.BatchSize)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if (r.Status == "accepted" || r.Status == "running" || r.Status == "leased") && r.RequestedAt.After(runningEdge) {
			continue
		}
		if _, created, err := d.openStateful(ctx, RuleDeviceSyncStale, SubjectSyncCommand, r.CommandID); err != nil {
			return err
		} else if created {
			d.metrics.opened(RuleDeviceSyncStale)
		}
	}
	return nil
}

// resolveSyncStale clears staleness once commands leave non-terminal state.
func (d *Detector) resolveSyncStale(ctx context.Context, now time.Time) error {
	_ = now
	active, err := d.store.ActiveByRule(ctx, RuleDeviceSyncStale, d.cfg.BatchSize)
	if err != nil {
		return err
	}
	for _, incident := range active {
		status, ok, err := d.store.CommandTerminal(ctx, incident.SubjectID)
		if err != nil {
			return err
		}
		_ = d.store.TouchObserved(ctx, incident.ID, d.now().UTC())
		if !ok || status == "completed" || status == "failed" {
			if _, err := d.resolveIncident(ctx, incident, ResolutionTerminal); err != nil {
				return err
			}
		}
	}
	return nil
}

// scanReportBlocked opens one event incident per blocked run. Alert only:
// never resend or recreate the report.
func (d *Detector) scanReportBlocked(ctx context.Context, now time.Time) error {
	_ = now
	rows, err := d.store.ScanBlockedRuns(ctx, d.cfg.BatchSize)
	if err != nil {
		return err
	}
	for _, r := range rows {
		key := "report-blocked:" + r.RunID
		if _, created, err := d.openEvent(ctx, RuleReportBlocked, SubjectReportRun, r.RunID, key); err != nil {
			return err
		} else if created {
			d.metrics.opened(RuleReportBlocked)
		}
	}
	return nil
}

// scanReportStale opens staleness for old pending/retry runs. One normal
// retry never alerts alone: only age past the threshold opens.
func (d *Detector) scanReportStale(ctx context.Context, now time.Time) error {
	rows, err := d.store.ScanStaleRuns(ctx, now.Add(-d.cfg.ReportStale), d.cfg.BatchSize)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if _, created, err := d.openStateful(ctx, RuleReportStale, SubjectReportRun, r.RunID); err != nil {
			return err
		} else if created {
			d.metrics.opened(RuleReportStale)
		}
	}
	return nil
}

// resolveReportStale clears staleness for runs that completed or blocked.
func (d *Detector) resolveReportStale(ctx context.Context, now time.Time) error {
	_ = now
	active, err := d.store.ActiveByRule(ctx, RuleReportStale, d.cfg.BatchSize)
	if err != nil {
		return err
	}
	for _, incident := range active {
		status, ok, err := d.store.RunStatus(ctx, incident.SubjectID)
		if err != nil {
			return err
		}
		_ = d.store.TouchObserved(ctx, incident.ID, d.now().UTC())
		if !ok || status == "completed" || status == "blocked" {
			if _, err := d.resolveIncident(ctx, incident, ResolutionTerminal); err != nil {
				return err
			}
		}
	}
	return nil
}

// scanNotificationEvents opens one event incident per non-7D blocked or
// ambiguous notification. Ambiguous is urgent and never triggers resend:
// the alert text states the outcome is uncertain.
func (d *Detector) scanNotificationEvents(ctx context.Context, now time.Time) error {
	_ = now
	blocked, err := d.store.ScanBlockedNotifications(ctx, d.cfg.BatchSize)
	if err != nil {
		return err
	}
	for _, n := range blocked {
		key := "notification-blocked:" + n.NotificationID
		if _, created, err := d.openEvent(ctx, RuleNotificationBlocked, SubjectNotification, n.NotificationID, key); err != nil {
			return err
		} else if created {
			d.metrics.opened(RuleNotificationBlocked)
		}
	}
	ambiguous, err := d.store.ScanAmbiguousNotifications(ctx, d.cfg.BatchSize)
	if err != nil {
		return err
	}
	for _, n := range ambiguous {
		key := "notification-ambiguous:" + n.NotificationID
		if _, created, err := d.openEvent(ctx, RuleNotificationAmb, SubjectNotification, n.NotificationID, key); err != nil {
			return err
		} else if created {
			d.metrics.opened(RuleNotificationAmb)
		}
	}
	return nil
}

// scanNotificationStale opens staleness for old retry notifications owned
// outside Phase 7D. Normal short backoff never alerts alone.
func (d *Detector) scanNotificationStale(ctx context.Context, now time.Time) error {
	rows, err := d.store.ScanRetryStaleNotifications(ctx, now.Add(-d.cfg.NotificationStale), d.cfg.BatchSize)
	if err != nil {
		return err
	}
	for _, n := range rows {
		if _, created, err := d.openStateful(ctx, RuleNotificationStale, SubjectNotification, n.NotificationID); err != nil {
			return err
		} else if created {
			d.metrics.opened(RuleNotificationStale)
		}
	}
	return nil
}

// resolveNotificationStale clears staleness once the notification leaves
// retryable state (accepted, blocked, ambiguous, or gone).
func (d *Detector) resolveNotificationStale(ctx context.Context, now time.Time) error {
	_ = now
	active, err := d.store.ActiveByRule(ctx, RuleNotificationStale, d.cfg.BatchSize)
	if err != nil {
		return err
	}
	for _, incident := range active {
		status, ok, err := d.store.NotificationDispatch(ctx, incident.SubjectID)
		if err != nil {
			return err
		}
		_ = d.store.TouchObserved(ctx, incident.ID, d.now().UTC())
		if !ok || status != "pending" && status != "retry" {
			if _, err := d.resolveIncident(ctx, incident, ResolutionTerminal); err != nil {
				return err
			}
		}
	}
	return nil
}
