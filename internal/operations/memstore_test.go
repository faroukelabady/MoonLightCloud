package operations

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

func newConflict(msg string) error { return apperr.New(apperr.Conflict, msg) }

// memStore is a deterministic in-memory Store for unit tests. It mirrors
// the database uniqueness contracts (active partial unique, event unique,
// recovery unique, delivery idempotency unique).
type memStore struct {
	mu         sync.Mutex
	recipients map[string]Recipient
	incidents  map[string]*Incident
	deliveries map[string]*Delivery
	recovery   map[string]*Recovery
	// Scan fixtures.
	devices       map[string]memDevice
	commands      map[string]memCommand
	runs          map[string]memRun
	notifications map[string]memNotification
}

type memDevice struct {
	id       string
	name     string
	active   bool
	lastSeen *time.Time
}

type memCommand struct {
	id        string
	device    string
	status    string
	requested time.Time
}

type memRun struct {
	id      string
	status  string
	created time.Time
}

type memNotification struct {
	id       string
	status   string
	opsOwned bool
	created  time.Time
}

func newMemStore() *memStore {
	return &memStore{
		recipients: map[string]Recipient{}, incidents: map[string]*Incident{},
		deliveries: map[string]*Delivery{}, recovery: map[string]*Recovery{},
		devices: map[string]memDevice{}, commands: map[string]memCommand{},
		runs: map[string]memRun{}, notifications: map[string]memNotification{},
	}
}

func (m *memStore) CreateOpsRecipient(_ context.Context, id, label, provider, recipient, locale string, at time.Time) (Recipient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_ = at
	if _, ok := m.recipients[id]; ok {
		return Recipient{}, newConflict("recipient already exists")
	}
	r := Recipient{ID: id, Label: label, ProviderKey: provider, Recipient: recipient, Locale: locale, Enabled: true}
	m.recipients[id] = r
	return r, nil
}

func (m *memStore) ListOpsRecipients(_ context.Context) ([]Recipient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Recipient
	for _, r := range m.recipients {
		out = append(out, r)
	}
	return out, nil
}

func (m *memStore) ListEnabledOpsRecipients(_ context.Context) ([]Recipient, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Recipient
	for _, r := range m.recipients {
		if r.Enabled {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memStore) DisableOpsRecipient(_ context.Context, id string, _ time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.recipients[id]
	if !ok || !r.Enabled {
		return false, nil
	}
	r.Enabled = false
	m.recipients[id] = r
	return true, nil
}

func (m *memStore) activeFor(rule, stype, sid string) *Incident {
	for _, in := range m.incidents {
		if in.Rule == rule && in.SubjectType == stype && in.SubjectID == sid &&
			(in.State == StateOpen || in.State == StateAcknowledged) {
			return in
		}
	}
	return nil
}

func (m *memStore) OpenStateful(_ context.Context, id, rule, stype, sid, severity, _ string, episode int, at time.Time) (Incident, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing := m.activeFor(rule, stype, sid); existing != nil {
		existing.LastObservedAt = at
		return *existing, false, nil
	}
	in := &Incident{ID: id, Rule: rule, SubjectType: stype, SubjectID: sid, Severity: severity,
		State: StateOpen, Episode: episode, OpenedAt: at, LastObservedAt: at}
	m.incidents[id] = in
	return *in, true, nil
}

func (m *memStore) OpenEvent(_ context.Context, id, rule, stype, sid, severity, key string, at time.Time) (Incident, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, in := range m.incidents {
		if in.SourceEventKey != nil && *in.SourceEventKey == key {
			return *in, false, nil
		}
	}
	in := &Incident{ID: id, Rule: rule, SubjectType: stype, SubjectID: sid, Severity: severity,
		State: StateOpen, Episode: 1, SourceEventKey: &key, OpenedAt: at, LastObservedAt: at}
	m.incidents[id] = in
	return *in, true, nil
}

func (m *memStore) ActiveIncident(_ context.Context, rule, stype, sid string) (Incident, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if in := m.activeFor(rule, stype, sid); in != nil {
		return *in, true, nil
	}
	return Incident{}, false, nil
}

func (m *memStore) IncidentByEventKey(_ context.Context, key string) (Incident, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, in := range m.incidents {
		if in.SourceEventKey != nil && *in.SourceEventKey == key {
			return *in, true, nil
		}
	}
	return Incident{}, false, nil
}

func (m *memStore) IncidentByID(_ context.Context, id string) (Incident, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, ok := m.incidents[id]
	if !ok {
		return Incident{}, false, nil
	}
	return *in, true, nil
}

func (m *memStore) TouchObserved(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if in, ok := m.incidents[id]; ok {
		in.LastObservedAt = at
	}
	return nil
}

func (m *memStore) Acknowledge(_ context.Context, id string, at time.Time) (Incident, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, ok := m.incidents[id]
	if !ok {
		return Incident{}, false, nil
	}
	if in.State == StateOpen {
		in.State = StateAcknowledged
		return *in, true, nil
	}
	return *in, false, nil
}

func (m *memStore) Resolve(_ context.Context, id, code string, at time.Time) (Incident, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	in, ok := m.incidents[id]
	if !ok {
		return Incident{}, false, nil
	}
	if in.State == StateResolved {
		return *in, false, nil
	}
	in.State = StateResolved
	in.ResolutionCode = &code
	return *in, true, nil
}

func (m *memStore) MaxEpisode(_ context.Context, rule, stype, sid string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	max := 0
	for _, in := range m.incidents {
		if in.Rule == rule && in.SubjectType == stype && in.SubjectID == sid && in.Episode > max {
			max = in.Episode
		}
	}
	return max, nil
}

func (m *memStore) ListPage(_ context.Context, state, severity, rule string, cursorAt *time.Time, cursorID string, limit int) ([]Incident, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var all []Incident
	for _, in := range m.incidents {
		if state != "" && in.State != state {
			continue
		}
		if severity != "" && in.Severity != severity {
			continue
		}
		if rule != "" && in.Rule != rule {
			continue
		}
		if cursorAt != nil && (in.OpenedAt.After(*cursorAt) || (in.OpenedAt.Equal(*cursorAt) && in.ID >= cursorID)) {
			continue
		}
		all = append(all, *in)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].OpenedAt.Equal(all[j].OpenedAt) {
			return all[i].ID > all[j].ID
		}
		return all[i].OpenedAt.After(all[j].OpenedAt)
	})
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func (m *memStore) DeviceSummary(_ context.Context) ([]DeviceSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	byDev := map[string]*DeviceSummary{}
	for _, in := range m.incidents {
		if in.SubjectType != SubjectDevice || (in.State != StateOpen && in.State != StateAcknowledged) {
			continue
		}
		s, ok := byDev[in.SubjectID]
		if !ok {
			s = &DeviceSummary{DeviceID: in.SubjectID, MaxSeverity: SeverityWarning}
			byDev[in.SubjectID] = s
		}
		s.OpenCount++
		if in.Severity == SeverityUrgent {
			s.MaxSeverity = SeverityUrgent
		}
	}
	var out []DeviceSummary
	for _, s := range byDev {
		out = append(out, *s)
	}
	return out, nil
}

func (m *memStore) CreateDelivery(_ context.Context, d Delivery, _ time.Time) (Delivery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.deliveries {
		if e.NotificationKey == d.NotificationKey {
			return Delivery{}, newConflict("delivery already exists")
		}
	}
	d.Status = "pending"
	m.deliveries[d.ID] = &d
	cp := d
	return cp, nil
}

func (m *memStore) ClaimDelivery(_ context.Context) (Delivery, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best *Delivery
	for _, d := range m.deliveries {
		if d.Status != "pending" {
			continue
		}
		if best == nil || d.ID < best.ID {
			best = d
		}
	}
	if best == nil {
		return Delivery{}, false, nil
	}
	return *best, true, nil
}

func (m *memStore) FinishOpsDeliverySent(_ context.Context, id, nid string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.deliveries[id]
	if !ok || d.Status != "pending" {
		return fmt.Errorf("already finished")
	}
	d.Status = "sent"
	d.NotificationID = &nid
	return nil
}

func (m *memStore) FinishOpsDeliveryBlocked(_ context.Context, id, code string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.deliveries[id]
	if !ok || d.Status != "pending" {
		return fmt.Errorf("already finished")
	}
	d.Status = "blocked"
	d.LastErrorCode = &code
	return nil
}

func (m *memStore) DeliveriesForIncident(_ context.Context, incidentID string) ([]Delivery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Delivery
	for _, d := range m.deliveries {
		if d.IncidentID == incidentID {
			out = append(out, *d)
		}
	}
	return out, nil
}

func (m *memStore) CreateRecovery(_ context.Context, incidentID, key string, _ time.Time) (Recovery, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.recovery {
		if r.IncidentID == incidentID {
			return *r, false, nil
		}
	}
	r := &Recovery{IncidentID: incidentID, ActionType: ActionReconnectSync, State: "pending", IdempotencyKey: key}
	m.recovery[incidentID] = r
	cp := *r
	cp.ID = incidentID + "-action"
	r.ID = cp.ID
	return cp, true, nil
}

func (m *memStore) RecoveryForIncident(_ context.Context, incidentID string) (Recovery, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.recovery[incidentID]
	if !ok {
		return Recovery{}, false, nil
	}
	return *r, true, nil
}

func (m *memStore) ClaimRecovery(_ context.Context, now time.Time) (Recovery, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_ = now
	for _, r := range m.recovery {
		if r.State == "pending" && (r.NextAttemptAt == nil || !r.NextAttemptAt.After(now)) {
			return *r, true, nil
		}
	}
	return Recovery{}, false, nil
}

func (m *memStore) FinishRecoveryCompleted(_ context.Context, id, target, result string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.recovery {
		if r.ID == id {
			r.State = "completed"
			r.TargetEntityID = &target
			r.ResultCode = &result
			return nil
		}
	}
	return fmt.Errorf("missing")
}

func (m *memStore) RetryRecoveryLater(_ context.Context, id, code string, next, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.recovery {
		if r.ID == id {
			r.AttemptCount++
			r.NextAttemptAt = &next
			r.LastErrorCode = &code
			return nil
		}
	}
	return fmt.Errorf("missing")
}

func (m *memStore) FinishRecoveryBlocked(_ context.Context, id, code string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.recovery {
		if r.ID == id {
			r.State = "blocked"
			r.LastErrorCode = &code
			return nil
		}
	}
	return fmt.Errorf("missing")
}

func (m *memStore) RecoveriesForIncident(_ context.Context, incidentID string) ([]Recovery, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Recovery
	for _, r := range m.recovery {
		if r.IncidentID == incidentID {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (m *memStore) ScanOffline(_ context.Context, olderThan time.Time, limit int) ([]OfflineCandidate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []OfflineCandidate
	for _, d := range m.devices {
		if !d.active || d.lastSeen == nil || !d.lastSeen.Before(olderThan) {
			continue
		}
		out = append(out, OfflineCandidate{DeviceID: d.id, Name: d.name, LastSeen: *d.lastSeen})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *memStore) ScanReconnected(_ context.Context, newerThan time.Time, limit int) ([]OfflineCandidate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []OfflineCandidate
	for _, d := range m.devices {
		if !d.active || d.lastSeen == nil || d.lastSeen.Before(newerThan) {
			continue
		}
		out = append(out, OfflineCandidate{DeviceID: d.id, Name: d.name, LastSeen: *d.lastSeen})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *memStore) ScanRevoked(_ context.Context, limit int) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, d := range m.devices {
		if d.active {
			continue
		}
		out = append(out, d.id)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *memStore) ScanFailedCommands(_ context.Context, limit int) ([]FailedCommand, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []FailedCommand
	for _, c := range m.commands {
		if c.status == "failed" {
			out = append(out, FailedCommand{CommandID: c.id, DeviceID: c.device})
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *memStore) ScanStaleCommands(_ context.Context, olderThan time.Time, limit int) ([]StaleCommand, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []StaleCommand
	for _, c := range m.commands {
		if (c.status == "pending" || c.status == "accepted" || c.status == "running") && c.requested.Before(olderThan) {
			out = append(out, StaleCommand{CommandID: c.id, DeviceID: c.device, Status: c.status, RequestedAt: c.requested})
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *memStore) ScanBlockedRuns(_ context.Context, limit int) ([]BlockedRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []BlockedRun
	for _, r := range m.runs {
		if r.status == "blocked" {
			out = append(out, BlockedRun{RunID: r.id, CreatedAt: r.created})
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *memStore) ScanStaleRuns(_ context.Context, olderThan time.Time, limit int) ([]StaleRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []StaleRun
	for _, r := range m.runs {
		if (r.status == "pending" || r.status == "retry") && r.created.Before(olderThan) {
			out = append(out, StaleRun{RunID: r.id, Status: r.status, CreatedAt: r.created})
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *memStore) scanBad(status string, older *time.Time, limit int) []BadNotification {
	var out []BadNotification
	for _, n := range m.notifications {
		if n.status != status || n.opsOwned {
			continue
		}
		if older != nil && !n.created.Before(*older) {
			continue
		}
		out = append(out, BadNotification{NotificationID: n.id, CreatedAt: n.created})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (m *memStore) ScanBlockedNotifications(_ context.Context, limit int) ([]BadNotification, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.scanBad("blocked", nil, limit), nil
}

func (m *memStore) ScanAmbiguousNotifications(_ context.Context, limit int) ([]BadNotification, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.scanBad("ambiguous", nil, limit), nil
}

func (m *memStore) ScanRetryStaleNotifications(_ context.Context, olderThan time.Time, limit int) ([]BadNotification, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.scanBad("retry", &olderThan, limit), nil
}

func (m *memStore) CommandTerminal(_ context.Context, id string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.commands[id]
	if !ok {
		return "", false, nil
	}
	return c.status, true, nil
}

func (m *memStore) RunStatus(_ context.Context, id string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[id]
	if !ok {
		return "", false, nil
	}
	return r.status, true, nil
}

func (m *memStore) NotificationDispatch(_ context.Context, id string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.notifications[id]
	if !ok {
		return "", false, nil
	}
	return n.status, true, nil
}

func (m *memStore) DeviceStatus(_ context.Context, id string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[id]
	if !ok {
		return "", false, nil
	}
	if d.active {
		return "active", true, nil
	}
	return "revoked", true, nil
}

func (m *memStore) Presence(_ context.Context, id string) (*time.Time, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[id]
	if !ok {
		return nil, false, nil
	}
	return d.lastSeen, true, nil
}

func (m *memStore) ActiveByRule(_ context.Context, rule string, limit int) ([]Incident, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Incident
	for _, in := range m.incidents {
		if in.Rule == rule && (in.State == StateOpen || in.State == StateAcknowledged) {
			out = append(out, *in)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}
