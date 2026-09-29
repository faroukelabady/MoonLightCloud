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
// incident state corrupts on failure.
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
	incident, created, err := d.service.OpenStateful(ctx, rule, subjectType, subjectID, "")
	if err != nil {
		return Incident{}, false, err
	}
	if created {
		if derr := d.alerts.CreateDeliveries(ctx, incident, EventOpened); derr != nil {
			return Incident{}, false, derr
		}
	}
	return incident, created, nil
}

func (d *Detector) openEvent(ctx context.Context, rule, subjectType, subjectID, key string) (Incident, bool, error) {
	incident, created, err := d.service.OpenEvent(ctx, rule, subjectType, subjectID, key)
	if err != nil {
		return Incident{}, false, err
	}
	if created {
		if derr := d.alerts.CreateDeliveries(ctx, incident, EventOpened); derr != nil {
			return Incident{}, false, derr
		}
	}
	return incident, created, nil
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

// resolveOffline clears offline incidents on reconnect (fresh presence) or
// revocation, and arms the single reconnect recovery on real reconnects.
func (d *Detector) resolveOffline(ctx context.Context, now time.Time) error {
	freshEdge := now.Add(-d.cfg.OnlineWindow)
	reconnected, err := d.store.ScanReconnected(ctx, freshEdge, d.cfg.BatchSize)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, c := range reconnected {
		seen[c.DeviceID] = true
		incident, ok, err := d.store.ActiveIncident(ctx, RuleDeviceOffline, SubjectDevice, c.DeviceID)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if _, err := d.resolveIncident(ctx, incident, ResolutionReconnect); err != nil {
			return err
		}
		// Only a previously OFFLINE device resolves here: the rule
		// requires last_seen past the (longer) offline threshold, so a
		// first-ever contact (NEVER_SEEN, no incident) never heals.
		if d.cfg.AutoSyncOnReconnect {
			if err := d.armReconnect(ctx, incident, c.DeviceID, now); err != nil {
				return err
			}
		}
	}
	revoked, err := d.store.ScanRevoked(ctx, d.cfg.BatchSize)
	if err != nil {
		return err
	}
	for _, deviceID := range revoked {
		incident, ok, err := d.store.ActiveIncident(ctx, RuleDeviceOffline, SubjectDevice, deviceID)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if _, err := d.resolveIncident(ctx, incident, ResolutionNoActive); err != nil {
			return err
		}
		// Revocation never heals: no recovery action by construction.
	}
	return nil
}

func (d *Detector) resolveIncident(ctx context.Context, incident Incident, code string) (Incident, error) {
	resolved, err := d.service.Resolve(ctx, incident.ID, code)
	if err != nil {
		return Incident{}, err
	}
	d.metrics.resolved(incident.Rule)
	// Resolution is a new event: resolved deliveries go to currently
	// configured enabled recipients (not the open snapshot).
	if derr := d.alerts.CreateDeliveries(ctx, resolved, EventResolved); derr != nil {
		return Incident{}, derr
	}
	return resolved, nil
}

// armReconnect durably records at most one reconnect action per incident.
// Execution belongs to the recovery worker; creation here is idempotent
// across instances and restarts via (incident_id, action_type) uniqueness.
func (d *Detector) armReconnect(ctx context.Context, incident Incident, deviceID string, now time.Time) error {
	status, ok, err := d.store.DeviceStatus(ctx, deviceID)
	if err != nil {
		return err
	}
	if !ok || status != "active" {
		return nil
	}
	_, _, err = d.store.CreateRecovery(ctx, incident.ID, ReconnectIdempotencyKey(incident.ID), now)
	return err
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
	rows, err := d.store.ScanStaleCommands(ctx, pendingEdge, d.cfg.BatchSize)
	if err != nil {
		return err
	}
	runningEdge := now.Add(-d.cfg.SyncRunningStale)
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
		if !ok || status != "pending" && status != "retry" {
			if _, err := d.resolveIncident(ctx, incident, ResolutionTerminal); err != nil {
				return err
			}
		}
	}
	return nil
}

// StillActive re-evaluates a stateful subject predicate for manual
// resolution guard (409 while the condition holds).
func (d *Detector) StillActive(ctx context.Context, incident Incident) (bool, error) {
	now := d.now().UTC()
	switch incident.Rule {
	case RuleDeviceOffline:
		seen, ok, err := d.store.Presence(ctx, incident.SubjectID)
		if err != nil {
			return false, err
		}
		if !ok || seen == nil {
			return false, nil
		}
		return now.Sub(seen.UTC()) >= d.cfg.OfflineAfter, nil
	case RuleDeviceSyncStale:
		status, ok, err := d.store.CommandTerminal(ctx, incident.SubjectID)
		if err != nil {
			return false, err
		}
		return ok && status != "completed" && status != "failed", nil
	case RuleReportStale:
		status, ok, err := d.store.RunStatus(ctx, incident.SubjectID)
		if err != nil {
			return false, err
		}
		return ok && (status == "pending" || status == "retry"), nil
	case RuleNotificationStale:
		status, ok, err := d.store.NotificationDispatch(ctx, incident.SubjectID)
		if err != nil {
			return false, err
		}
		return ok && (status == "pending" || status == "retry"), nil
	}
	return false, nil
}
