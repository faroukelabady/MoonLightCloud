package operations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
)

var testIDs = make(chan string, 10000)

func init() {
	go func() {
		for i := 0; ; i++ {
			testIDs <- "00000000-0000-4000-8000-" + pad12(i)
		}
	}()
}

func pad12(i int) string {
	s := "000000000000"
	n := itoa(i)
	return s[:len(s)-len(n)] + n
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

func testService(store Store) (*Service, *Metrics) {
	return NewService(store, func() string { return <-testIDs }, time.Now), NewMetrics()
}

func testDetector(store *memStore, svc *Service, alerts *AlertProcessor, autoHeal bool) *Detector {
	metrics := NewMetrics()
	return NewDetector(store, svc, alerts, DetectorConfig{
		BatchSize: 100, OfflineAfter: 5 * time.Minute, OnlineWindow: time.Minute,
		SyncPendingStale: 30 * time.Minute, SyncRunningStale: 30 * time.Minute,
		ReportStale: time.Hour, NotificationStale: time.Hour, AutoSyncOnReconnect: autoHeal,
	}, func() string { return <-testIDs }, time.Now, metrics)
}

type stubNotifier struct {
	sent    []notifications.EnqueueTemplateRequest
	missing bool
}

func (s *stubNotifier) EnqueueTemplate(_ context.Context, req notifications.EnqueueTemplateRequest) (notifications.EnqueueResult, error) {
	if s.missing {
		return notifications.EnqueueResult{}, apperr.New(apperr.InvalidInput, "NOTIFICATION_TEMPLATE_MAPPING_MISSING")
	}
	s.sent = append(s.sent, req)
	return notifications.EnqueueResult{ID: <-testIDs, Created: true}, nil
}

type stubCommander struct {
	active  map[string]string
	created map[string]string
	err     error
}

func (s *stubCommander) CreateSyncRequest(_ context.Context, deviceID, key string) (CommandResult, error) {
	if s.err != nil {
		return CommandResult{}, s.err
	}
	if id, ok := s.active[deviceID]; ok {
		_ = id
		return CommandResult{}, apperr.New(apperr.Conflict, "DEVICE_SYNC_ALREADY_ACTIVE")
	}
	id := <-testIDs
	if s.created == nil {
		s.created = map[string]string{}
	}
	s.created[key] = id
	if s.active == nil {
		s.active = map[string]string{}
	}
	s.active[deviceID] = id
	return CommandResult{ID: id}, nil
}

func (s *stubCommander) ActiveCommand(_ context.Context, deviceID string) (CommandResult, bool, error) {
	id, ok := s.active[deviceID]
	if !ok {
		return CommandResult{}, false, nil
	}
	return CommandResult{ID: id}, true, nil
}

func seedRecipient(t *testing.T, store *memStore, locale string) Recipient {
	t.Helper()
	r, err := store.CreateOpsRecipient(context.Background(), <-testIDs, "ops", "whatsapp-main", "201012345678", locale, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Incident lifecycle: open → ack → resolve; ack never resolves.
func TestServiceLifecycle(t *testing.T) {
	store := newMemStore()
	svc, _ := testService(store)
	ctx := context.Background()
	opened, created, err := svc.OpenStateful(ctx, RuleDeviceOffline, SubjectDevice, "d1", "")
	if err != nil || !created || opened.State != StateOpen || opened.Episode != 1 {
		t.Fatalf("open: %+v %v %v", opened, created, err)
	}
	again, created, err := svc.OpenStateful(ctx, RuleDeviceOffline, SubjectDevice, "d1", "")
	if err != nil || created || again.ID != opened.ID {
		t.Fatalf("adopt: %+v %v %v", again, created, err)
	}
	acked, err := svc.Acknowledge(ctx, opened.ID)
	if err != nil || acked.State != StateAcknowledged {
		t.Fatalf("ack: %+v %v", acked, err)
	}
	resolved, err := svc.Resolve(ctx, opened.ID, ResolutionOperator)
	if err != nil || resolved.State != StateResolved {
		t.Fatalf("resolve: %+v %v", resolved, err)
	}
	// Resolve is terminal: acknowledge after resolve is a no-op read.
	after, err := svc.Acknowledge(ctx, opened.ID)
	if err != nil || after.State != StateResolved {
		t.Fatalf("resolved stable: %+v %v", after, err)
	}
}

// Offline rule end to end over the mem store, including flap silence.
func TestDetectorOfflineMem(t *testing.T) {
	store := newMemStore()
	svc, _ := testService(store)
	metrics := NewMetrics()
	notify := &stubNotifier{}
	alerts := NewAlertProcessor(store, svc, notify, func() string { return <-testIDs }, time.Now, metrics)
	d := testDetector(store, svc, alerts, false)
	ctx := context.Background()
	seedRecipient(t, store, "en")
	recent := time.Now().Add(-time.Minute)
	store.devices["d1"] = memDevice{id: "d1", name: "shop", active: true, lastSeen: &recent}
	if err := d.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.ActiveIncident(ctx, RuleDeviceOffline, SubjectDevice, "d1"); ok {
		t.Fatal("flap silence")
	}
	old := time.Now().Add(-time.Hour)
	store.devices["d1"] = memDevice{id: "d1", name: "shop", active: true, lastSeen: &old}
	if err := d.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	opened, ok, _ := store.ActiveIncident(ctx, RuleDeviceOffline, SubjectDevice, "d1")
	if !ok {
		t.Fatal("opens past threshold")
	}
	// Opened deliveries were snapshotted for the enabled recipient.
	dels, _ := store.DeliveriesForIncident(ctx, opened.ID)
	if len(dels) != 1 || dels[0].Event != EventOpened {
		t.Fatalf("opened deliveries: %+v", dels)
	}
	// Alert drain enqueues through the neutral boundary only.
	for i := 0; i < 5; i++ {
		if more, err := alerts.ProcessOne(ctx); err != nil || !more {
			break
		}
	}
	if len(notify.sent) != 1 || notify.sent[0].TemplateKey != TemplateOpen {
		t.Fatalf("one neutral enqueue: %+v", notify.sent)
	}
	finished, _ := store.DeliveriesForIncident(ctx, opened.ID)
	if finished[0].Status != "sent" || finished[0].NotificationID == nil {
		t.Fatalf("sent recorded: %+v", finished[0])
	}
	// Reconnect resolves; resolved deliveries fan out.
	now := time.Now()
	store.devices["d1"] = memDevice{id: "d1", name: "shop", active: true, lastSeen: &now}
	if err := d.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Get(ctx, opened.ID)
	if got.State != StateResolved {
		t.Fatalf("resolved: %+v", got)
	}
	dels, _ = store.DeliveriesForIncident(ctx, opened.ID)
	resolved := 0
	for _, dl := range dels {
		if dl.Event == EventResolved {
			resolved++
		}
	}
	if resolved != 1 {
		t.Fatalf("resolved delivery: %+v", dels)
	}
}

// Reconnect healing through the fake commander: one action, one command,
// deterministic key, idempotent across repeated scans.
func TestRecoveryHealingMem(t *testing.T) {
	store := newMemStore()
	svc, _ := testService(store)
	metrics := NewMetrics()
	notify := &stubNotifier{}
	alerts := NewAlertProcessor(store, svc, notify, func() string { return <-testIDs }, time.Now, metrics)
	commander := &stubCommander{}
	recovery := NewRecoveryWorker(store, commander, time.Now, metrics)
	d := testDetector(store, svc, alerts, true)
	ctx := context.Background()
	old := time.Now().Add(-time.Hour)
	store.devices["d1"] = memDevice{id: "d1", active: true, lastSeen: &old}
	if err := d.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	opened, ok, _ := store.ActiveIncident(ctx, RuleDeviceOffline, SubjectDevice, "d1")
	if !ok {
		t.Fatal("open")
	}
	now := time.Now()
	store.devices["d1"] = memDevice{id: "d1", active: true, lastSeen: &now}
	if err := d.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	rec, ok, _ := store.RecoveryForIncident(ctx, opened.ID)
	if !ok || rec.State != "pending" {
		t.Fatalf("armed: %+v %v", rec, ok)
	}
	if rec.IdempotencyKey != ReconnectIdempotencyKey(opened.ID) {
		t.Fatalf("key: %q", rec.IdempotencyKey)
	}
	more, err := recovery.ProcessOne(ctx)
	if err != nil || !more {
		t.Fatalf("process: %v %v", more, err)
	}
	done, _, _ := store.RecoveryForIncident(ctx, opened.ID)
	if done.State != "completed" || done.TargetEntityID == nil || done.ResultCode == nil || *done.ResultCode != CodeRecoveryQueued {
		t.Fatalf("queued: %+v", done)
	}
	// Repeated scans never duplicate.
	for i := 0; i < 20; i++ {
		if err := d.Scan(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(commander.created) != 1 {
		t.Fatalf("one command: %+v", commander.created)
	}
}

// Existing active command satisfies recovery without a new command.
func TestRecoverySatisfiedMem(t *testing.T) {
	store := newMemStore()
	svc, _ := testService(store)
	metrics := NewMetrics()
	notify := &stubNotifier{}
	alerts := NewAlertProcessor(store, svc, notify, func() string { return <-testIDs }, time.Now, metrics)
	commander := &stubCommander{active: map[string]string{"d1": "manual-cmd"}}
	recovery := NewRecoveryWorker(store, commander, time.Now, metrics)
	d := testDetector(store, svc, alerts, true)
	ctx := context.Background()
	old := time.Now().Add(-time.Hour)
	store.devices["d1"] = memDevice{id: "d1", active: true, lastSeen: &old}
	if err := d.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	opened, _, _ := store.ActiveIncident(ctx, RuleDeviceOffline, SubjectDevice, "d1")
	now := time.Now()
	store.devices["d1"] = memDevice{id: "d1", active: true, lastSeen: &now}
	if err := d.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := recovery.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	done, _, _ := store.RecoveryForIncident(ctx, opened.ID)
	if done.State != "completed" || done.ResultCode == nil || *done.ResultCode != CodeRecoveryExisting {
		t.Fatalf("satisfied: %+v", done)
	}
	if len(commander.created) != 0 {
		t.Fatal("no second command")
	}
}

// Transient commander failure retries bounded, then blocks.
func TestRecoveryRetryCapMem(t *testing.T) {
	store := newMemStore()
	svc, _ := testService(store)
	metrics := NewMetrics()
	commander := &stubCommander{err: errors.New("db unavailable")}
	current := time.Now()
	recovery := NewRecoveryWorker(store, commander, func() time.Time { return current }, metrics)
	ctx := context.Background()
	opened, _, _ := svc.OpenStateful(ctx, RuleDeviceOffline, SubjectDevice, "d1", "")
	rec, _, _ := store.CreateRecovery(ctx, opened.ID, ReconnectIdempotencyKey(opened.ID), current)
	_ = rec
	for i := 0; i < 12; i++ {
		_, _ = recovery.ProcessOne(ctx)
		current = current.Add(31 * time.Minute)
	}
	done, _, _ := store.RecoveryForIncident(ctx, opened.ID)
	if done.State != "blocked" {
		t.Fatalf("bounded retries then blocked: %+v", done)
	}
}

// Missing template mapping blocks delivery without provider traffic.
func TestAlertMissingMappingMem(t *testing.T) {
	store := newMemStore()
	svc, _ := testService(store)
	metrics := NewMetrics()
	notify := &stubNotifier{missing: true}
	alerts := NewAlertProcessor(store, svc, notify, func() string { return <-testIDs }, time.Now, metrics)
	ctx := context.Background()
	seedRecipient(t, store, "ar")
	opened, _, _ := svc.OpenStateful(ctx, RuleReportBlocked, SubjectReportRun, "run-1", "")
	built, err := alerts.BuildDeliveries(ctx, opened, EventOpened)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.OpenStatefulAtomic(ctx, opened.ID, opened.Rule, opened.SubjectType, opened.SubjectID, opened.Severity, "", 1, built, time.Now()); err != nil {
		t.Fatal(err)
	}
	more, err := alerts.ProcessOne(ctx)
	if err != nil || !more {
		t.Fatalf("process: %v %v", more, err)
	}
	dels, _ := store.DeliveriesForIncident(ctx, opened.ID)
	if len(dels) != 1 || dels[0].Status != "blocked" || dels[0].LastErrorCode == nil || *dels[0].LastErrorCode != CodeNoMapping {
		t.Fatalf("blocked mapping: %+v", dels)
	}
	if len(notify.sent) != 0 {
		t.Fatal("no provider traffic without mapping")
	}
}

// Zero recipients: incident exists, no deliveries, no crash.
func TestZeroRecipientsMem(t *testing.T) {
	store := newMemStore()
	svc, _ := testService(store)
	metrics := NewMetrics()
	alerts := NewAlertProcessor(store, svc, &stubNotifier{}, func() string { return <-testIDs }, time.Now, metrics)
	ctx := context.Background()
	opened, created, err := svc.OpenStateful(ctx, RuleDeviceOffline, SubjectDevice, "d9", "")
	if err != nil || !created {
		t.Fatalf("incident exists regardless: %+v %v", opened, err)
	}
	built, err := alerts.BuildDeliveries(ctx, opened, EventOpened)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.OpenStatefulAtomic(ctx, opened.ID, opened.Rule, opened.SubjectType, opened.SubjectID, opened.Severity, "", 1, built, time.Now()); err != nil {
		t.Fatal(err)
	}
	dels, _ := store.DeliveriesForIncident(ctx, opened.ID)
	if len(dels) != 0 {
		t.Fatal("no deliveries without recipients")
	}
}

// Manual resolve guard: stateful active conflicts; events resolve.
func TestManualResolveGuardMem(t *testing.T) {
	store := newMemStore()
	svc, _ := testService(store)
	reader := NewOpsReader(store, svc, NewMetrics())
	ctx := context.Background()
	old := time.Now().Add(-time.Hour)
	store.devices["d1"] = memDevice{id: "d1", active: true, lastSeen: &old}
	opened, _, _ := svc.OpenStateful(ctx, RuleDeviceOffline, SubjectDevice, "d1", "")
	still, err := (&Detector{store: store, cfg: DetectorConfig{OfflineAfter: 5 * time.Minute}, now: time.Now}).StillActive(ctx, opened)
	if err != nil || !still {
		t.Fatalf("still active: %v %v", still, err)
	}
	if _, err := svc.ResolveIfClear(ctx, opened.ID, ResolutionOperator, still); err == nil {
		t.Fatal("conflict while active")
	}
	event, _, _ := svc.OpenEvent(ctx, RuleDeviceSyncFailed, SubjectSyncCommand, "c1", "sync-command:c1")
	resolved, err := reader.ResolveOperator(ctx, event.ID, "")
	if err != nil || resolved.State != StateResolved {
		t.Fatalf("event resolves: %+v %v", resolved, err)
	}
	again, err := reader.ResolveOperator(ctx, event.ID, "")
	if err != nil || again.State != StateResolved {
		t.Fatalf("re-resolve no-op: %+v %v", again, err)
	}
}
