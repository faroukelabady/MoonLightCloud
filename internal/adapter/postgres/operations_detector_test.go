package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
	"github.com/faroukelabady/MoonLightCloud/internal/operations"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	"github.com/jackc/pgx/v5/pgxpool"
)

type opsDetectorEnv struct {
	store    Devices
	pool     *pgxpool.Pool
	svc      *operations.Service
	detector *operations.Detector
	alerts   *operations.AlertProcessor
	recovery *operations.RecoveryWorker
	notify   *fakeOpsNotifier
	command  *fakeOpsCommander
	metrics  *operations.Metrics
}

type fakeOpsNotifier struct {
	sent  int
	calls []notifications.EnqueueTemplateRequest
	fail  bool
}

func (f *fakeOpsNotifier) EnqueueTemplate(_ context.Context, req notifications.EnqueueTemplateRequest) (notifications.EnqueueResult, error) {
	f.calls = append(f.calls, req)
	if f.fail {
		return notifications.EnqueueResult{}, fmt.Errorf("provider down")
	}
	f.sent++
	return notifications.EnqueueResult{ID: ids.System{}.New(), Created: true}, nil
}

type fakeOpsCommander struct {
	created map[string]string
	active  map[string]string
	calls   int
}

func newFakeOpsCommander() *fakeOpsCommander {
	return &fakeOpsCommander{created: map[string]string{}, active: map[string]string{}}
}

func (f *fakeOpsCommander) CreateSyncRequest(_ context.Context, deviceID, key string) (operations.CommandResult, error) {
	f.calls++
	if _, ok := f.active[deviceID]; ok {
		return operations.CommandResult{}, apperr.New(apperr.Conflict, "DEVICE_SYNC_ALREADY_ACTIVE")
	}
	id := ids.System{}.New()
	f.created[key] = id
	f.active[deviceID] = id
	return operations.CommandResult{ID: id}, nil
}

func (f *fakeOpsCommander) ActiveCommand(_ context.Context, deviceID string) (operations.CommandResult, bool, error) {
	id, ok := f.active[deviceID]
	if !ok {
		return operations.CommandResult{}, false, nil
	}
	return operations.CommandResult{ID: id}, true, nil
}

func newOpsDetectorEnv(t *testing.T, autoHeal bool) *opsDetectorEnv {
	t.Helper()
	store, pool := openOpsStore(t)
	svc := opsService(store)
	metrics := operations.NewMetrics()
	notify := &fakeOpsNotifier{}
	alerts := operations.NewAlertProcessor(store, svc, notify, ids.System{}.New, time.Now, metrics)
	commander := newFakeOpsCommander()
	recovery := operations.NewRecoveryWorker(store, commander, time.Now, metrics)
	detector := operations.NewDetector(store, svc, alerts, operations.DetectorConfig{
		BatchSize: 100, OfflineAfter: 5 * time.Minute, OnlineWindow: time.Minute,
		SyncPendingStale: 30 * time.Minute, SyncRunningStale: 30 * time.Minute,
		ReportStale: time.Hour, NotificationStale: time.Hour, AutoSyncOnReconnect: autoHeal,
	}, ids.System{}.New, time.Now, metrics)
	return &opsDetectorEnv{store: store, pool: pool, svc: svc, detector: detector,
		alerts: alerts, recovery: recovery, notify: notify, command: commander, metrics: metrics}
}

func (e *opsDetectorEnv) device(t *testing.T, name string) string {
	t.Helper()
	return opsSeedDevice(t, e.pool, name)
}

func (e *opsDetectorEnv) scan(t *testing.T) {
	t.Helper()
	if err := e.detector.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (e *opsDetectorEnv) drainAlerts(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		more, err := e.alerts.ProcessOne(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			return
		}
	}
	t.Fatal("alert drain did not converge")
}

func (e *opsDetectorEnv) drainRecovery(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		more, err := e.recovery.ProcessOne(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			return
		}
	}
	t.Fatal("recovery drain did not converge")
}

// Offline lifecycle: threshold open, rescan dedupe, flap silence,
// reconnect resolve, recurrence episode.
func TestDetectorOfflineLifecycle(t *testing.T) {
	env := newOpsDetectorEnv(t, false)
	ctx := context.Background()
	dev := env.device(t, "offline-dev")
	// Seen 4 minutes ago: below the 5m threshold → no incident (no flap).
	if err := env.store.TouchSeen(ctx, dev, time.Now().UTC().Add(-4*time.Minute), true); err != nil {
		t.Fatal(err)
	}
	env.scan(t)
	active, ok, err := env.store.ActiveIncident(ctx, operations.RuleDeviceOffline, operations.SubjectDevice, dev)
	if err != nil || ok {
		t.Fatalf("flap must not open: %+v %v", active, err)
	}
	// Age past threshold → one open incident.
	if err := env.store.TouchSeen(ctx, dev, time.Now().UTC().Add(-10*time.Minute), true); err != nil {
		t.Fatal(err)
	}
	// Note: TouchSeen is monotonic GREATEST — an older touch cannot move
	// last_seen backwards. Age directly instead.
	if _, err := env.pool.Exec(ctx, `UPDATE device_control_presence SET last_seen_at = now() - interval '10 minutes', last_poll_at = now() - interval '10 minutes' WHERE device_id = $1`, mustOpsUUID(t, dev)); err != nil {
		t.Fatal(err)
	}
	env.scan(t)
	env.scan(t)
	first, ok, err := env.store.ActiveIncident(ctx, operations.RuleDeviceOffline, operations.SubjectDevice, dev)
	if err != nil || !ok || first.State != "open" || first.Episode != 1 {
		t.Fatalf("one open incident: %+v %v %v", first, ok, err)
	}
	// Reconnect → resolved; healing disabled → no recovery action.
	if err := env.store.TouchSeen(ctx, dev, time.Now().UTC(), true); err != nil {
		t.Fatal(err)
	}
	env.scan(t)
	resolved, _ := env.svc.Get(ctx, first.ID)
	if resolved.State != "resolved" {
		t.Fatalf("reconnect resolves: %+v", resolved)
	}
	if _, ok, _ := env.store.RecoveryForIncident(ctx, first.ID); ok {
		t.Fatal("healing disabled: no recovery action")
	}
	// Recurrence → episode 2.
	if _, err := env.pool.Exec(ctx, `UPDATE device_control_presence SET last_seen_at = now() - interval '10 minutes' WHERE device_id = $1`, mustOpsUUID(t, dev)); err != nil {
		t.Fatal(err)
	}
	env.scan(t)
	second, ok, err := env.store.ActiveIncident(ctx, operations.RuleDeviceOffline, operations.SubjectDevice, dev)
	if err != nil || !ok || second.ID == first.ID || second.Episode != 2 {
		t.Fatalf("recurrence episode 2: %+v %v %v", second, ok, err)
	}
}

func mustOpsUUID(t *testing.T, id string) any {
	t.Helper()
	uid, err := parseUUID(id)
	if err != nil {
		t.Fatal(err)
	}
	return uid
}

// Revoked device resolves offline without healing.
func TestDetectorRevokedResolves(t *testing.T) {
	env := newOpsDetectorEnv(t, true)
	ctx := context.Background()
	dev := env.device(t, "rev-dev")
	if _, err := env.pool.Exec(ctx, `INSERT INTO device_control_presence (device_id, last_seen_at, last_poll_at, created_at, updated_at)
		VALUES ($1, now() - interval '10 minutes', now() - interval '10 minutes', now(), now())`, mustOpsUUID(t, dev)); err != nil {
		t.Fatal(err)
	}
	env.scan(t)
	first, ok, err := env.store.ActiveIncident(ctx, operations.RuleDeviceOffline, operations.SubjectDevice, dev)
	if err != nil || !ok {
		t.Fatalf("open: %v %v", ok, err)
	}
	if _, err := env.pool.Exec(ctx, `UPDATE devices SET status = 'revoked' WHERE id = $1`, mustOpsUUID(t, dev)); err != nil {
		t.Fatal(err)
	}
	env.scan(t)
	resolved, _ := env.svc.Get(ctx, first.ID)
	if resolved.State != "resolved" || resolved.ResolutionCode == nil || *resolved.ResolutionCode != operations.ResolutionNoActive {
		t.Fatalf("revoked resolves distinctly: %+v", resolved)
	}
	if _, ok, _ := env.store.RecoveryForIncident(ctx, first.ID); ok {
		t.Fatal("revoked never heals")
	}
}

// NEVER_SEEN devices never open offline and never heal.
func TestDetectorNeverSeenSilent(t *testing.T) {
	env := newOpsDetectorEnv(t, true)
	env.device(t, "ghost-dev")
	env.scan(t)
	rows, err := env.store.ActiveByRule(context.Background(), operations.RuleDeviceOffline, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("never seen is not an outage: %+v %v", rows, err)
	}
}

// Sync failed: one event incident, stable across rescans.
func TestDetectorSyncFailed(t *testing.T) {
	env := newOpsDetectorEnv(t, false)
	ctx := context.Background()
	dev := env.device(t, "fail-dev")
	cmdID := ids.System{}.New()
	if _, err := env.pool.Exec(ctx, `INSERT INTO device_control_commands
		(id, device_id, command_type, command_version, idempotency_key, status, requested_at, finished_at, result_code, created_at, updated_at)
		VALUES ($1, $2, 'sync_now', 1, 'k-fail', 'failed', now() - interval '1 minute', now(), 'SYNC_FAILED_NETWORK', now(), now())`,
		mustOpsUUID(t, cmdID), mustOpsUUID(t, dev)); err != nil {
		t.Fatal(err)
	}
	env.scan(t)
	env.scan(t)
	in, ok, err := env.store.IncidentByEventKey(ctx, "sync-command:"+cmdID)
	if err != nil || !ok || in.Severity != "urgent" || in.State != "open" {
		t.Fatalf("one urgent incident: %+v %v %v", in, ok, err)
	}
	_ = dev
}

// Sync stale boundaries: fresh none, old opens, terminal resolves.
func TestDetectorSyncStale(t *testing.T) {
	env := newOpsDetectorEnv(t, false)
	ctx := context.Background()
	dev := env.device(t, "stale-dev")
	dev2 := env.device(t, "stale-dev-2")
	mkCmd := func(target, key, status string, requested time.Time) string {
		id := ids.System{}.New()
		if _, err := env.pool.Exec(ctx, `INSERT INTO device_control_commands
			(id, device_id, command_type, command_version, idempotency_key, status, requested_at, created_at, updated_at)
			VALUES ($1, $2, 'sync_now', 1, $3, $4, $5, now(), now())`,
			mustOpsUUID(t, id), mustOpsUUID(t, target), key, status, requested); err != nil {
			t.Fatal(err)
		}
		return id
	}
	freshID := mkCmd(dev, "k-fresh", "pending", time.Now().UTC().Add(-29*time.Minute))
	oldID := mkCmd(dev2, "k-old", "pending", time.Now().UTC().Add(-31*time.Minute))
	env.scan(t)
	if _, ok, _ := env.store.ActiveIncident(ctx, operations.RuleDeviceSyncStale, operations.SubjectSyncCommand, freshID); ok {
		t.Fatal("threshold-1m: no incident")
	}
	old, ok, err := env.store.ActiveIncident(ctx, operations.RuleDeviceSyncStale, operations.SubjectSyncCommand, oldID)
	if err != nil || !ok {
		t.Fatalf("stale opens: %v %v", ok, err)
	}
	// Terminal transition resolves.
	if _, err := env.pool.Exec(ctx, `UPDATE device_control_commands SET status='completed', finished_at=now(), result_code='SYNC_COMPLETED' WHERE id=$1`, mustOpsUUID(t, oldID)); err != nil {
		t.Fatal(err)
	}
	env.scan(t)
	resolved, _ := env.svc.Get(ctx, old.ID)
	if resolved.State != "resolved" {
		t.Fatalf("terminal resolves: %+v", resolved)
	}
}

// Report blocked opens once, never resends; stale respects age.
func TestDetectorReports(t *testing.T) {
	env := newOpsDetectorEnv(t, false)
	ctx := context.Background()
	sched := ids.System{}.New()
	run := ids.System{}.New()
	if _, err := env.pool.Exec(ctx, `INSERT INTO business_report_schedules (id, name, report_kind, timezone, local_time, enabled, revision, next_run_local_date, next_run_at, created_at, updated_at)
		VALUES ($1, 'ops-test', 'DAILY', 'Africa/Cairo', '08:00', TRUE, 1, CURRENT_DATE, now(), now(), now())`, mustOpsUUID(t, sched)); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx, `INSERT INTO business_report_runs (id, schedule_id, run_kind, slot_local_date, scheduled_for, period_start, period_end, schedule_revision, status, created_at, updated_at)
		VALUES ($1, $2, 'scheduled', CURRENT_DATE - 1, now(), now(), now(), 1, 'blocked', now(), now())`, mustOpsUUID(t, run), mustOpsUUID(t, sched)); err != nil {
		t.Fatal(err)
	}
	env.scan(t)
	env.scan(t)
	in, ok, err := env.store.IncidentByEventKey(ctx, "report-blocked:"+run)
	if err != nil || !ok || in.State != "open" {
		t.Fatalf("blocked opens once: %+v %v %v", in, ok, err)
	}
	var runs int
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM business_report_runs`).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("no report resend: %d %v", runs, err)
	}
}

// Notification blocked/ambiguous open; 7D-owned rows excluded (no storm).
func TestDetectorNotifications(t *testing.T) {
	env := newOpsDetectorEnv(t, false)
	ctx := context.Background()
	seedOpsMapping(t, env.store)
	// Business notification that becomes blocked + ambiguous.
	bizBlocked := opsSeedNotification(t, env, "biz-1", "blocked", false)
	bizAmb := opsSeedNotification(t, env, "biz-2", "ambiguous", false)
	// 7D-owned alert notification that becomes blocked: must not incident.
	opsSeedNotification(t, env, "ops-alert:inc:opened:del", "blocked", true)
	env.scan(t)
	env.scan(t)
	if _, ok, _ := env.store.IncidentByEventKey(ctx, "notification-blocked:"+bizBlocked); !ok {
		t.Fatal("blocked business notification incidents")
	}
	amb, ok, _ := env.store.IncidentByEventKey(ctx, "notification-ambiguous:"+bizAmb)
	if !ok || amb.Severity != "urgent" {
		t.Fatalf("urgent ambiguous: %+v %v", amb, ok)
	}
	var storm int
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM operational_incidents WHERE rule_key IN ('NOTIFICATION_BLOCKED','NOTIFICATION_AMBIGUOUS')`).Scan(&storm); err != nil || storm != 2 {
		t.Fatalf("no recursive incidents: %d %v", storm, err)
	}
	// Provider sends: only 7D alert enqueues (2 incidents × 0 recipients = 0).
	if env.notify.sent != 0 {
		t.Fatalf("zero recipients: no provider traffic: %d", env.notify.sent)
	}
}

func opsSeedNotification(t *testing.T, env *opsDetectorEnv, key, status string, opsOwned bool) string {
	t.Helper()
	tpl := "daily_business_report_v1"
	idem := key
	if opsOwned {
		tpl = operations.TemplateOpen
	}
	id := ids.System{}.New()
	if _, err := env.pool.Exec(context.Background(), `INSERT INTO notification_messages
		(id, provider_key, idempotency_key, semantic_fingerprint, recipient, template_key, locale,
		 ext_template_name, ext_language_code, dispatch_status, delivery_status)
		VALUES ($1, 'whatsapp-main', $2, '\x01', '201012345678', $3, 'ar', 'ext', 'ar', $4, 'UNKNOWN')`,
		mustOpsUUID(t, id), idem, tpl, status); err != nil {
		t.Fatal(err)
	}
	return id
}

// Reconnect healing: one action, one command, deterministic key.
func TestDetectorReconnectHealing(t *testing.T) {
	env := newOpsDetectorEnv(t, true)
	ctx := context.Background()
	dev := env.device(t, "heal-dev")
	if _, err := env.pool.Exec(ctx, `INSERT INTO device_control_presence (device_id, last_seen_at, last_poll_at, created_at, updated_at)
		VALUES ($1, now() - interval '10 minutes', now() - interval '10 minutes', now(), now())`, mustOpsUUID(t, dev)); err != nil {
		t.Fatal(err)
	}
	env.scan(t)
	first, ok, err := env.store.ActiveIncident(ctx, operations.RuleDeviceOffline, operations.SubjectDevice, dev)
	if err != nil || !ok {
		t.Fatalf("open: %v %v", ok, err)
	}
	if err := env.store.TouchSeen(ctx, dev, time.Now().UTC(), true); err != nil {
		t.Fatal(err)
	}
	env.scan(t)
	resolved, _ := env.svc.Get(ctx, first.ID)
	if resolved.State != "resolved" {
		t.Fatalf("resolved: %+v", resolved)
	}
	rec, ok, err := env.store.RecoveryForIncident(ctx, first.ID)
	if err != nil || !ok || rec.State != "pending" {
		t.Fatalf("one pending action: %+v %v %v", rec, ok, err)
	}
	if rec.IdempotencyKey != operations.ReconnectIdempotencyKey(first.ID) {
		t.Fatalf("deterministic key: %q", rec.IdempotencyKey)
	}
	env.drainRecovery(t)
	done, _, _ := env.store.RecoveryForIncident(ctx, first.ID)
	if done.State != "completed" || done.TargetEntityID == nil || done.ResultCode == nil || *done.ResultCode != operations.CodeRecoveryQueued {
		t.Fatalf("queued: %+v", done)
	}
	if env.command.calls != 1 || len(env.command.created) != 1 {
		t.Fatalf("one command: calls=%d rows=%d", env.command.calls, len(env.command.created))
	}
	// 100 more detector ticks: still one action, one command.
	for i := 0; i < 100; i++ {
		env.scan(t)
	}
	if env.command.calls != 1 {
		t.Fatalf("no duplicate healing: %d", env.command.calls)
	}
}

// Existing active command at reconnect: satisfied, no second command.
func TestDetectorReconnectSatisfied(t *testing.T) {
	env := newOpsDetectorEnv(t, true)
	ctx := context.Background()
	dev := env.device(t, "sat-dev")
	env.command.active[dev] = "manual-cmd-id"
	if _, err := env.pool.Exec(ctx, `INSERT INTO device_control_presence (device_id, last_seen_at, last_poll_at, created_at, updated_at)
		VALUES ($1, now() - interval '10 minutes', now() - interval '10 minutes', now(), now())`, mustOpsUUID(t, dev)); err != nil {
		t.Fatal(err)
	}
	env.scan(t)
	first, ok, _ := env.store.ActiveIncident(ctx, operations.RuleDeviceOffline, operations.SubjectDevice, dev)
	if !ok {
		t.Fatal("open")
	}
	if err := env.store.TouchSeen(ctx, dev, time.Now().UTC(), true); err != nil {
		t.Fatal(err)
	}
	env.scan(t)
	env.drainRecovery(t)
	done, _, _ := env.store.RecoveryForIncident(ctx, first.ID)
	if done.State != "completed" || done.ResultCode == nil || *done.ResultCode != operations.CodeRecoveryExisting {
		t.Fatalf("satisfied: %+v", done)
	}
	if done.TargetEntityID == nil || *done.TargetEntityID != "manual-cmd-id" {
		t.Fatalf("adopts existing: %+v", done)
	}
	if len(env.command.created) != 0 {
		t.Fatal("no second command")
	}
}

// Failed auto command: failure incident opens, no recursive command.
func TestDetectorFailedAutoNoRecursion(t *testing.T) {
	env := newOpsDetectorEnv(t, true)
	ctx := context.Background()
	dev := env.device(t, "failauto-dev")
	if _, err := env.pool.Exec(ctx, `INSERT INTO device_control_presence (device_id, last_seen_at, last_poll_at, created_at, updated_at)
		VALUES ($1, now() - interval '10 minutes', now() - interval '10 minutes', now(), now())`, mustOpsUUID(t, dev)); err != nil {
		t.Fatal(err)
	}
	env.scan(t)
	first, ok, _ := env.store.ActiveIncident(ctx, operations.RuleDeviceOffline, operations.SubjectDevice, dev)
	if !ok {
		t.Fatal("open")
	}
	if err := env.store.TouchSeen(ctx, dev, time.Now().UTC(), true); err != nil {
		t.Fatal(err)
	}
	env.scan(t)
	env.drainRecovery(t)
	done, _, _ := env.store.RecoveryForIncident(ctx, first.ID)
	if done.TargetEntityID == nil {
		t.Fatal("command queued")
	}
	autoCmd := *done.TargetEntityID
	// The auto command later fails in 7C (terminal row, same identity).
	if _, err := env.pool.Exec(ctx, `INSERT INTO device_control_commands
		(id, device_id, command_type, command_version, idempotency_key, status, requested_at, finished_at, result_code, created_at, updated_at)
		VALUES ($1, $2, 'sync_now', 1, $3, 'failed', now(), now(), 'SYNC_FAILED_NETWORK', now(), now())`,
		mustOpsUUID(t, autoCmd), mustOpsUUID(t, dev), operations.ReconnectIdempotencyKey(first.ID)); err != nil {
		t.Fatal(err)
	}
	env.scan(t)
	fail, ok, _ := env.store.IncidentByEventKey(ctx, "sync-command:"+autoCmd)
	if !ok || fail.Rule != operations.RuleDeviceSyncFailed || fail.State != "open" {
		t.Fatalf("failure incident: %+v %v", fail, ok)
	}
	calls := env.command.calls
	env.scan(t)
	env.scan(t)
	if env.command.calls != calls {
		t.Fatal("no recursive second command")
	}
}

// Acknowledge-then-resolve race converges on resolved.
func TestDetectorAckResolveRace(t *testing.T) {
	env := newOpsDetectorEnv(t, false)
	ctx := context.Background()
	dev := env.device(t, "race-dev")
	if _, err := env.pool.Exec(ctx, `INSERT INTO device_control_presence (device_id, last_seen_at, last_poll_at, created_at, updated_at)
		VALUES ($1, now() - interval '10 minutes', now() - interval '10 minutes', now(), now())`, mustOpsUUID(t, dev)); err != nil {
		t.Fatal(err)
	}
	env.scan(t)
	first, ok, _ := env.store.ActiveIncident(ctx, operations.RuleDeviceOffline, operations.SubjectDevice, dev)
	if !ok {
		t.Fatal("open")
	}
	if err := env.store.TouchSeen(ctx, dev, time.Now().UTC(), true); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = env.svc.Acknowledge(ctx, first.ID)
	}()
	env.scan(t)
	<-done
	final, _ := env.svc.Get(ctx, first.ID)
	if final.State != "resolved" {
		t.Fatalf("resolved wins: %+v", final)
	}
}

// Manual resolve of an active offline incident conflicts.
func TestDetectorManualResolveConflict(t *testing.T) {
	env := newOpsDetectorEnv(t, false)
	ctx := context.Background()
	dev := env.device(t, "man-dev")
	if _, err := env.pool.Exec(ctx, `INSERT INTO device_control_presence (device_id, last_seen_at, last_poll_at, created_at, updated_at)
		VALUES ($1, now() - interval '10 minutes', now() - interval '10 minutes', now(), now())`, mustOpsUUID(t, dev)); err != nil {
		t.Fatal(err)
	}
	env.scan(t)
	first, ok, _ := env.store.ActiveIncident(ctx, operations.RuleDeviceOffline, operations.SubjectDevice, dev)
	if !ok {
		t.Fatal("open")
	}
	still, err := env.detector.StillActive(ctx, first)
	if err != nil || !still {
		t.Fatalf("still active: %v %v", still, err)
	}
	if _, err := env.svc.ResolveIfClear(ctx, first.ID, operations.ResolutionOperator, still); err == nil {
		t.Fatal("must conflict while active")
	} else if !strings.Contains(err.Error(), "CONDITION_STILL_ACTIVE") {
		t.Fatalf("code: %v", err)
	}
}
