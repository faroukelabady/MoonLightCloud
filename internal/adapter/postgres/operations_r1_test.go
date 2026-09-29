package postgres

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/devicecontrol"
	"github.com/faroukelabady/MoonLightCloud/internal/operations"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	"github.com/jackc/pgx/v5/pgxpool"
)

// H01: 10 simultaneous detectors race one offline resolution with two
// recipients and real Phase 7A enqueue behavior. Overlap is forced
// deterministically: a holder transaction locks the incident row while
// all racers pile up on the resolve UPDATE, then releases them at once.
// Exactly one durable resolved event and one delivery per
// (incident_id, event_type, recipient_id); one notification per
// recipient; no duplicate semantic sends.
func TestResolveRaceSingleDelivery(t *testing.T) {
	store, pool := openOpsStore(t)
	ctx := context.Background()
	svc := opsService(store)
	seedOpsMapping(t, store)
	// Two recipients: distinct rows (one per locale).
	r1, err := store.CreateOpsRecipient(ctx, ids.System{}.New(), "ops-ar", "whatsapp-main", "201012345678", "ar", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	r2, err := store.CreateOpsRecipient(ctx, ids.System{}.New(), "ops-en", "whatsapp-main", "201012345679", "en", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	dev := opsSeedDevice(t, pool, "h01-dev")
	if _, err := pool.Exec(ctx, `INSERT INTO device_control_presence (device_id, last_seen_at, last_poll_at, created_at, updated_at)
		VALUES ($1, now() - interval '10 minutes', now() - interval '10 minutes', now(), now())`, mustOpsUUID(t, dev)); err != nil {
		t.Fatal(err)
	}
	// Open the incident once (first detector wins the open race too).
	opener := operations.NewDetector(store, svc,
		operations.NewAlertProcessor(store, svc, newOpsNotifier(store), ids.System{}.New, time.Now, operations.NewMetrics()),
		detectorTestConfig(false), ids.System{}.New, time.Now, operations.NewMetrics())
	if err := opener.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	incident, ok, err := store.ActiveIncident(ctx, operations.RuleDeviceOffline, operations.SubjectDevice, dev)
	if err != nil || !ok {
		t.Fatalf("open: %v %v", ok, err)
	}
	// Reconnect so every racer attempts the same resolution.
	if err := store.TouchSeen(ctx, dev, time.Now().UTC(), true); err != nil {
		t.Fatal(err)
	}
	// Hold the incident row: every racer piles up on the resolve UPDATE.
	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	var one int
	if err := holder.QueryRow(ctx, `SELECT 1 FROM operational_incidents WHERE id = $1 FOR UPDATE`, mustOpsUUID(t, incident.ID)).Scan(&one); err != nil {
		t.Fatal(err)
	}
	const racers = 10
	metrics := make([]*operations.Metrics, racers)
	var wg sync.WaitGroup
	errs := make(chan error, racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			metrics[i] = operations.NewMetrics()
			d := operations.NewDetector(store, svc,
				operations.NewAlertProcessor(store, svc, newOpsNotifier(store), ids.System{}.New, time.Now, metrics[i]),
				detectorTestConfig(false), ids.System{}.New, time.Now, metrics[i])
			if err := d.Scan(ctx); err != nil {
				errs <- err
			}
		}(i)
	}
	// Let every racer reach the resolve UPDATE (local scans arrive in ms).
	close(start)
	time.Sleep(1500 * time.Millisecond)
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	// Exactly one resolved delivery per recipient.
	dels, err := store.DeliveriesForIncident(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	var resolved int
	seenRecipients := map[string]int{}
	for _, d := range dels {
		if d.Event == operations.EventResolved {
			resolved++
			seenRecipients[d.RecipientID]++
		}
	}
	if resolved != 2 || seenRecipients[r1.ID] != 1 || seenRecipients[r2.ID] != 1 {
		t.Fatalf("one resolved delivery per recipient: %+v", dels)
	}
	// Only the committing scanner counts the resolution.
	var resolvedTotal int64
	for _, m := range metrics {
		resolvedTotal += m.Snapshot()["incidents_resolved_total"]
	}
	if resolvedTotal != 1 {
		t.Fatalf("one winner counts: %d", resolvedTotal)
	}
	// Drain through real Phase 7A enqueue: the two opened plus the two
	// resolved deliveries converge to one notification each.
	drained := 0
	for i := 0; i < 10; i++ {
		more, err := newAlertProcessorReal(t, store, svc).ProcessOne(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
		drained++
	}
	if drained != 4 {
		t.Fatalf("drained deliveries: %d", drained)
	}
	var notifications int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notification_messages WHERE idempotency_key LIKE 'ops-alert:%'`).Scan(&notifications); err != nil {
		t.Fatal(err)
	}
	if notifications != 4 {
		t.Fatalf("one notification per delivery: %d", notifications)
	}
	// Distinct idempotency keys: no duplicate semantic sends possible.
	var distinct int
	if err := pool.QueryRow(ctx, `SELECT count(DISTINCT idempotency_key) FROM notification_messages WHERE idempotency_key LIKE 'ops-alert:%'`).Scan(&distinct); err != nil {
		t.Fatal(err)
	}
	if distinct != 4 {
		t.Fatalf("distinct keys: %d", distinct)
	}
}

func detectorTestConfig(autoHeal bool) operations.DetectorConfig {
	return operations.DetectorConfig{
		BatchSize: 100, OfflineAfter: 5 * time.Minute, OnlineWindow: time.Minute,
		SyncPendingStale: 30 * time.Minute, SyncRunningStale: 30 * time.Minute,
		ReportStale: time.Hour, NotificationStale: time.Hour, AutoSyncOnReconnect: autoHeal,
	}
}

func newAlertProcessorReal(t *testing.T, store Devices, svc *operations.Service) *operations.AlertProcessor {
	t.Helper()
	return operations.NewAlertProcessor(store, svc, newOpsNotifier(store), ids.System{}.New, time.Now, operations.NewMetrics())
}

// H01: acknowledge-versus-auto-resolve converges on resolved with exactly
// one delivery set. The ack path (conditional UPDATE) and the resolve
// path (atomic resolve) serialize on the row; final state is coherent.
func TestAckVsResolveRace(t *testing.T) {
	store, pool := openOpsStore(t)
	ctx := context.Background()
	svc := opsService(store)
	seedOpsMapping(t, store)
	r1, err := store.CreateOpsRecipient(ctx, ids.System{}.New(), "ops-ar", "whatsapp-main", "201012345678", "ar", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	dev := opsSeedDevice(t, pool, "ackrace-dev")
	if _, err := pool.Exec(ctx, `INSERT INTO device_control_presence (device_id, last_seen_at, last_poll_at, created_at, updated_at)
		VALUES ($1, now() - interval '10 minutes', now() - interval '10 minutes', now(), now())`, mustOpsUUID(t, dev)); err != nil {
		t.Fatal(err)
	}
	mkDetector := func() (*operations.Detector, *operations.Metrics) {
		metrics := operations.NewMetrics()
		d := operations.NewDetector(store, svc,
			operations.NewAlertProcessor(store, svc, newOpsNotifier(store), ids.System{}.New, time.Now, metrics),
			detectorTestConfig(false), ids.System{}.New, time.Now, metrics)
		return d, metrics
	}
	opener, _ := mkDetector()
	if err := opener.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	incident, ok, err := store.ActiveIncident(ctx, operations.RuleDeviceOffline, operations.SubjectDevice, dev)
	if err != nil || !ok {
		t.Fatalf("open: %v %v", ok, err)
	}
	if err := store.TouchSeen(ctx, dev, time.Now().UTC(), true); err != nil {
		t.Fatal(err)
	}
	// Hold the row so ack and auto-resolve truly overlap.
	holder, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	var one int
	if err := holder.QueryRow(ctx, `SELECT 1 FROM operational_incidents WHERE id = $1 FOR UPDATE`, mustOpsUUID(t, incident.ID)).Scan(&one); err != nil {
		t.Fatal(err)
	}
	d, _ := mkDetector()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		if _, err := svc.Acknowledge(ctx, incident.ID); err != nil {
			errs <- err
		}
	}()
	go func() {
		defer wg.Done()
		if err := d.Scan(ctx); err != nil {
			errs <- err
		}
	}()
	time.Sleep(500 * time.Millisecond)
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	final, err := svc.Get(ctx, incident.ID)
	if err != nil || final.State != "resolved" {
		t.Fatalf("coherent resolved: %+v %v", final, err)
	}
	// At most one opened + one resolved delivery for the single recipient.
	dels, err := store.DeliveriesForIncident(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dels) != 2 {
		t.Fatalf("one delivery per event: %+v", dels)
	}
	_ = r1
}

// H01: fast open→resolve in immediate succession yields deterministic
// opened + resolved delivery sets with distinct idempotency keys.
func TestFastOpenResolveSequence(t *testing.T) {
	store, pool := openOpsStore(t)
	ctx := context.Background()
	svc := opsService(store)
	seedOpsMapping(t, store)
	if _, err := store.CreateOpsRecipient(ctx, ids.System{}.New(), "ops-ar", "whatsapp-main", "201012345678", "ar", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	dev := opsSeedDevice(t, pool, "fast-dev")
	if _, err := pool.Exec(ctx, `INSERT INTO device_control_presence (device_id, last_seen_at, last_poll_at, created_at, updated_at)
		VALUES ($1, now() - interval '10 minutes', now() - interval '10 minutes', now(), now())`, mustOpsUUID(t, dev)); err != nil {
		t.Fatal(err)
	}
	metrics := operations.NewMetrics()
	d := operations.NewDetector(store, svc,
		operations.NewAlertProcessor(store, svc, newOpsNotifier(store), ids.System{}.New, time.Now, metrics),
		detectorTestConfig(false), ids.System{}.New, time.Now, metrics)
	if err := d.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	incident, ok, err := store.ActiveIncident(ctx, operations.RuleDeviceOffline, operations.SubjectDevice, dev)
	if err != nil || !ok {
		t.Fatalf("open: %v %v", ok, err)
	}
	if err := store.TouchSeen(ctx, dev, time.Now().UTC(), true); err != nil {
		t.Fatal(err)
	}
	if err := d.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	dels, err := store.DeliveriesForIncident(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	byEvent := map[string][]string{}
	for _, dl := range dels {
		byEvent[dl.Event] = append(byEvent[dl.Event], dl.NotificationKey)
	}
	if len(byEvent[operations.EventOpened]) != 1 || len(byEvent[operations.EventResolved]) != 1 {
		t.Fatalf("one delivery per event: %+v", dels)
	}
	if byEvent[operations.EventOpened][0] == byEvent[operations.EventResolved][0] {
		t.Fatal("opened and resolved keys must differ")
	}
	final, _ := svc.Get(ctx, incident.ID)
	if final.State != "resolved" {
		t.Fatalf("resolved: %+v", final)
	}
}

// M02: event scans are progressive. Five old failed commands occupy more
// than two batches; a new urgent ambiguous notification and a new failed
// Sync Now must both be reached within a bounded number of ticks.
func TestDetectorEventStarvationBounded(t *testing.T) {
	store, pool := openOpsStore(t)
	ctx := context.Background()
	svc := opsService(store)
	seedOpsMapping(t, store)
	dev := opsSeedDevice(t, pool, "m02-dev")
	mkFailed := func(key string) string {
		id := ids.System{}.New()
		if _, err := pool.Exec(ctx, `INSERT INTO device_control_commands
			(id, device_id, command_type, command_version, idempotency_key, status, requested_at, finished_at, result_code, created_at, updated_at)
			VALUES ($1, $2, 'sync_now', 1, $3, 'failed', now() - interval '1 minute', now(), 'SYNC_FAILED_NETWORK', now(), now())`,
			mustOpsUUID(t, id), mustOpsUUID(t, dev), key); err != nil {
			t.Fatal(err)
		}
		return id
	}
	for i := 0; i < 5; i++ {
		mkFailed("k-old-" + string(rune('a'+i)))
	}
	d := batchDetector(t, store, svc, 2, false)
	for i := 0; i < 3; i++ {
		if err := d.Scan(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// Five old commands processed across batches; now add urgent news.
	newFailed := mkFailed("k-new")
	ambID := opsSeedNotificationFresh(t, pool, "amb-new", "ambiguous")
	var foundFailed, foundAmb bool
	for i := 0; i < 4; i++ {
		if err := d.Scan(ctx); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := store.IncidentByEventKey(ctx, "sync-command:"+newFailed); ok {
			foundFailed = true
		}
		if _, ok, _ := store.IncidentByEventKey(ctx, "notification-ambiguous:"+ambID); ok {
			foundAmb = true
		}
		if foundFailed && foundAmb {
			break
		}
	}
	if !foundFailed || !foundAmb {
		t.Fatalf("bounded ticks reach news: failed=%v amb=%v", foundFailed, foundAmb)
	}
}

// M02: stateful resolution starts from active incidents. Five active
// offline incidents exceed two batches; the one cleared device resolves
// within bounded ticks while the rest stay open.
func TestDetectorResolveStarvationBounded(t *testing.T) {
	store, pool := openOpsStore(t)
	ctx := context.Background()
	svc := opsService(store)
	devs := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		dev := opsSeedDevice(t, pool, "m02r-dev")
		devs = append(devs, dev)
		if _, err := pool.Exec(ctx, `INSERT INTO device_control_presence (device_id, last_seen_at, last_poll_at, created_at, updated_at)
			VALUES ($1, now() - interval '10 minutes', now() - interval '10 minutes', now(), now())`, mustOpsUUID(t, dev)); err != nil {
			t.Fatal(err)
		}
	}
	d := batchDetector(t, store, svc, 2, false)
	for i := 0; i < 3; i++ {
		if err := d.Scan(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, dev := range devs {
		if _, ok, _ := store.ActiveIncident(ctx, operations.RuleDeviceOffline, operations.SubjectDevice, dev); !ok {
			t.Fatalf("all five open: %s", dev)
		}
	}
	// Clear the middle device; bounded ticks must resolve exactly it.
	if err := store.TouchSeen(ctx, devs[2], time.Now().UTC(), true); err != nil {
		t.Fatal(err)
	}
	var resolved bool
	for i := 0; i < 4; i++ {
		if err := d.Scan(ctx); err != nil {
			t.Fatal(err)
		}
		in, _ := svc.Get(ctx, mustActiveID(t, store, ctx, devs[2]))
		if in.State == "resolved" {
			resolved = true
			break
		}
	}
	if !resolved {
		t.Fatal("cleared incident resolved within bounded ticks")
	}
	for _, dev := range []string{devs[0], devs[1], devs[3], devs[4]} {
		if _, ok, _ := store.ActiveIncident(ctx, operations.RuleDeviceOffline, operations.SubjectDevice, dev); !ok {
			t.Fatalf("others stay open: %s", dev)
		}
	}
}

func mustActiveID(t *testing.T, store Devices, ctx context.Context, dev string) string {
	t.Helper()
	in, ok, err := store.ActiveIncident(ctx, operations.RuleDeviceOffline, operations.SubjectDevice, dev)
	if err != nil || !ok {
		// Already resolved; fetch by latest episode instead.
		row := store.pool.QueryRow(ctx, `SELECT id FROM operational_incidents WHERE rule_key='DEVICE_OFFLINE' AND subject_id=$1 ORDER BY episode DESC LIMIT 1`, dev)
		var id string
		if err := row.Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	return in.ID
}

func batchDetector(t *testing.T, store Devices, svc *operations.Service, batch int, autoHeal bool) *operations.Detector {
	t.Helper()
	metrics := operations.NewMetrics()
	return operations.NewDetector(store, svc,
		operations.NewAlertProcessor(store, svc, newOpsNotifier(store), ids.System{}.New, time.Now, metrics),
		operations.DetectorConfig{
			BatchSize: batch, OfflineAfter: 5 * time.Minute, OnlineWindow: time.Minute,
			SyncPendingStale: 30 * time.Minute, SyncRunningStale: 30 * time.Minute,
			ReportStale: time.Hour, NotificationStale: time.Hour, AutoSyncOnReconnect: autoHeal,
		}, ids.System{}.New, time.Now, metrics)
}

func opsSeedNotificationFresh(t *testing.T, pool *pgxpool.Pool, key, status string) string {
	t.Helper()
	id := ids.System{}.New()
	if _, err := pool.Exec(context.Background(), `INSERT INTO notification_messages
		(id, provider_key, idempotency_key, semantic_fingerprint, recipient, template_key, locale,
		 ext_template_name, ext_language_code, dispatch_status, delivery_status)
		VALUES ($1, 'whatsapp-main', $2, '\x01', '201012345678', 'daily_business_report_v1', 'ar', 'ext', 'ar', $3, 'UNKNOWN')`,
		mustOpsUUID(t, id), key, status); err != nil {
		t.Fatal(err)
	}
	return id
}

// M04: leased commands join the overall-age stale predicate. A single
// lease expiry never alerts alone; long-expired leases stay covered;
// terminal transition resolves.
func TestDetectorLeasedStale(t *testing.T) {
	store, pool := openOpsStore(t)
	ctx := context.Background()
	svc := opsService(store)
	dev := opsSeedDevice(t, pool, "m04-dev")
	dev2 := opsSeedDevice(t, pool, "m04-dev2")
	dev3 := opsSeedDevice(t, pool, "m04-dev3")
	mkLeased := func(target, key string, requestedAgo, leaseUntilAgo time.Duration) string {
		id := ids.System{}.New()
		now := time.Now().UTC()
		if _, err := pool.Exec(ctx, `INSERT INTO device_control_commands
			(id, device_id, command_type, command_version, idempotency_key, status, requested_at, leased_at, lease_until, lease_generation, created_at, updated_at)
			VALUES ($1, $2, 'sync_now', 1, $3, 'leased', $4, $4, $5, 2, now(), now())`,
			mustOpsUUID(t, id), mustOpsUUID(t, target), key,
			now.Add(-requestedAgo), now.Add(-leaseUntilAgo)); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// Threshold-epsilon: requested 29m ago (running threshold 30m) → none.
	fresh := mkLeased(dev, "k-fresh", 29*time.Minute, 28*time.Minute)
	// At threshold: requested 31m ago → incident.
	old := mkLeased(dev2, "k-old", 31*time.Minute, 30*time.Minute)
	// Long after expiry: requested 3h ago, lease long dead → incident.
	ancient := mkLeased(dev3, "k-ancient", 3*time.Hour, 2*time.Hour)
	d := batchDetector(t, store, svc, 10, false)
	if err := d.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.ActiveIncident(ctx, operations.RuleDeviceSyncStale, operations.SubjectSyncCommand, fresh); ok {
		t.Fatal("threshold-epsilon: no incident")
	}
	for _, id := range []string{old, ancient} {
		if _, ok, _ := store.ActiveIncident(ctx, operations.RuleDeviceSyncStale, operations.SubjectSyncCommand, id); !ok {
			t.Fatalf("stale leased opens: %s", id)
		}
	}
	// Terminal transition resolves.
	if _, err := pool.Exec(ctx, `UPDATE device_control_commands SET status='failed', finished_at=now(), result_code='SYNC_FAILED_NETWORK' WHERE id=$1`, mustOpsUUID(t, ancient)); err != nil {
		t.Fatal(err)
	}
	if err := d.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Get(ctx, mustActiveOrResolvedID(t, store, ctx, ancient))
	if got.State != "resolved" {
		t.Fatalf("terminal resolves: %+v", got)
	}
}

func mustActiveOrResolvedID(t *testing.T, store Devices, ctx context.Context, cmd string) string {
	t.Helper()
	if in, ok, _ := store.ActiveIncident(ctx, operations.RuleDeviceSyncStale, operations.SubjectSyncCommand, cmd); ok {
		return in.ID
	}
	row := store.pool.QueryRow(ctx, `SELECT id FROM operational_incidents WHERE rule_key='DEVICE_SYNC_STALE' AND subject_id=$1 ORDER BY episode DESC LIMIT 1`, cmd)
	var id string
	if err := row.Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

type alwaysActiveDevices struct{}

func (alwaysActiveDevices) IsActive(_ context.Context, id string) (bool, error) {
	_ = id
	return true, nil
}

// Real Phase 7C manual-versus-auto race: operator creates a command, then
// the recovery worker runs against the real command service. Exactly one
// active command exists; the action is satisfied by the existing one.
func TestRecoveryRealManualVsAutoRace(t *testing.T) {
	store, pool := openOpsStore(t)
	ctx := context.Background()
	svc := opsService(store)
	dev := opsSeedDevice(t, pool, "race7c-dev")
	dctl := devicecontrol.NewService(store, alwaysActiveDevices{}, ids.System{}.New, time.Minute, time.Now)
	manual, _, err := dctl.CreateSyncRequest(ctx, dev, "manual-key")
	if err != nil {
		t.Fatal(err)
	}
	incident, _, err := svc.OpenStateful(ctx, operations.RuleDeviceOffline, operations.SubjectDevice, dev, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateRecovery(ctx, incident.ID, operations.ReconnectIdempotencyKey(incident.ID), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	worker := operations.NewRecoveryWorker(store, operations.NewDeviceCommander(dctl), time.Now, operations.NewMetrics())
	more, err := worker.ProcessOne(ctx)
	if err != nil || !more {
		t.Fatalf("process: %v %v", more, err)
	}
	done, _, err := store.RecoveryForIncident(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.State != "completed" || done.ResultCode == nil || *done.ResultCode != operations.CodeRecoveryExisting {
		t.Fatalf("satisfied by existing: %+v", done)
	}
	if done.TargetEntityID == nil || *done.TargetEntityID != manual.ID {
		t.Fatalf("adopts manual command: %+v vs %s", done, manual.ID)
	}
	var active int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM device_control_commands WHERE device_id=$1 AND status IN ('pending','leased','accepted','running')`, mustOpsUUID(t, dev)).Scan(&active); err != nil || active != 1 {
		t.Fatalf("one active command: %d (%v)", active, err)
	}
}

// Real crash-after-command-create adoption: the 7C command is durably
// created (simulating a crash before the recovery row stores its ID);
// retry with the same deterministic key adopts the same command.
func TestRecoveryRealCrashAdoption(t *testing.T) {
	store, pool := openOpsStore(t)
	ctx := context.Background()
	svc := opsService(store)
	dev := opsSeedDevice(t, pool, "adopt-dev")
	dctl := devicecontrol.NewService(store, alwaysActiveDevices{}, ids.System{}.New, time.Minute, time.Now)
	incident, _, err := svc.OpenStateful(ctx, operations.RuleDeviceOffline, operations.SubjectDevice, dev, "")
	if err != nil {
		t.Fatal(err)
	}
	key := operations.ReconnectIdempotencyKey(incident.ID)
	// First attempt creates the command, then "crashes" before finishing.
	commander := operations.NewDeviceCommander(dctl)
	first, err := commander.CreateSyncRequest(ctx, dev, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateRecovery(ctx, incident.ID, key, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// Retry (new worker, same deterministic key) adopts the same command.
	worker := operations.NewRecoveryWorker(store, operations.NewDeviceCommander(dctl), time.Now, operations.NewMetrics())
	more, err := worker.ProcessOne(ctx)
	if err != nil || !more {
		t.Fatalf("process: %v %v", more, err)
	}
	done, _, err := store.RecoveryForIncident(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.State != "completed" || done.TargetEntityID == nil || *done.TargetEntityID != first.ID {
		t.Fatalf("adopts same command: %+v vs %s", done, first.ID)
	}
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM device_control_commands WHERE device_id=$1`, mustOpsUUID(t, dev)).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("one command row: %d (%v)", rows, err)
	}
}

// M02: anti-join detector scans stay indexed on representative data.
// Seeds hundreds of rows (half already represented by incidents), then
// asserts the plans use index access rather than full-table rescans.
func TestDetectorScanPlansIndexed(t *testing.T) {
	store, pool := openOpsStore(t)
	ctx := context.Background()
	svc := opsService(store)
	dev := opsSeedDevice(t, pool, "plan-dev")
	if _, err := pool.Exec(ctx, `INSERT INTO device_control_presence (device_id, last_seen_at, last_poll_at, created_at, updated_at)
		VALUES ($1, now() - interval '10 minutes', now() - interval '10 minutes', now(), now())`, mustOpsUUID(t, dev)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3000; i++ {
		id := ids.System{}.New()
		status := "completed"
		// Sparse failures amid mostly terminal history, like production.
		if i%60 == 0 {
			status = "failed"
		}
		if _, err := pool.Exec(ctx, `INSERT INTO device_control_commands
			(id, device_id, command_type, command_version, idempotency_key, status, requested_at, finished_at, result_code, created_at, updated_at)
			VALUES ($1, $2, 'sync_now', 1, $3, $4, now() - interval '1 minute', now(), 'SYNC_FAILED_NETWORK', now(), now())`,
			mustOpsUUID(t, id), mustOpsUUID(t, dev), "k-plan-"+strconv.Itoa(i), status); err != nil {
			t.Fatal(err)
		}
		if status == "failed" && i%120 == 0 {
			if _, _, err := svc.OpenEvent(ctx, operations.RuleDeviceSyncFailed, operations.SubjectSyncCommand, id, "sync-command:"+id); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := 0; i < 3000; i++ {
		id := ids.System{}.New()
		status := "accepted"
		if i%60 == 0 {
			status = "blocked"
		}
		if _, err := pool.Exec(ctx, `INSERT INTO notification_messages
			(id, provider_key, idempotency_key, semantic_fingerprint, recipient, template_key, locale,
			 ext_template_name, ext_language_code, dispatch_status, delivery_status)
			VALUES ($1, 'whatsapp-main', $2, '\x01', '201012345678', 'daily_business_report_v1', 'ar', 'ext', 'ar', $3, 'UNKNOWN')`,
			mustOpsUUID(t, id), "nb-plan-"+strconv.Itoa(i), status); err != nil {
			t.Fatal(err)
		}
		if status == "blocked" && i%120 == 0 {
			if _, _, err := svc.OpenEvent(ctx, operations.RuleNotificationBlocked, operations.SubjectNotification, id, "notification-blocked:"+id); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := pool.Exec(ctx, `ANALYZE device_control_commands, notification_messages, operational_incidents`); err != nil {
		t.Fatal(err)
	}
	plan := func(q string) string {
		t.Helper()
		rows, err := pool.Query(ctx, "EXPLAIN "+q)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var sb strings.Builder
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			sb.WriteString(line + "\n")
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return sb.String()
	}
	failedPlan := plan(`SELECT id FROM device_control_commands WHERE status = 'failed'
		AND NOT EXISTS (SELECT 1 FROM operational_incidents i WHERE i.source_event_key = 'sync-command:' || device_control_commands.id::text)
		ORDER BY device_control_commands.finished_at, device_control_commands.id LIMIT 100`)
	if strings.Contains(failedPlan, "Seq Scan on device_control_commands") {
		t.Fatalf("failed scan must be indexed:\n%s", failedPlan)
	}
	blockedPlan := plan(`SELECT id FROM notification_messages WHERE dispatch_status = 'blocked'
		AND idempotency_key NOT LIKE 'ops-alert:%' AND template_key NOT LIKE 'operational\_alert\_%'
		AND NOT EXISTS (SELECT 1 FROM operational_incidents i WHERE i.source_event_key = 'notification-blocked:' || notification_messages.id::text)
		ORDER BY notification_messages.created_at, notification_messages.id LIMIT 100`)
	if strings.Contains(blockedPlan, "Seq Scan on notification_messages") {
		t.Fatalf("blocked scan must be indexed:\n%s", blockedPlan)
	}
	_ = store
	_ = dev
}
