package postgres

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/operations"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	"github.com/jackc/pgx/v5/pgxpool"
)

func r3ManualReader(store Devices, svc *operations.Service) *operations.OpsReader {
	reader := operations.NewOpsReader(store, svc, operations.NewMetrics())
	reader.SetManualGuard(5 * time.Minute)
	return reader
}

func r3ManualIncident(t *testing.T, store Devices, pool *pgxpool.Pool, event, stale bool) operations.Incident {
	t.Helper()
	ctx := context.Background()
	if event {
		incident, created, err := opsService(store).OpenEventAtomic(ctx, ids.System{}.New(), operations.RuleDeviceSyncFailed,
			operations.SubjectSyncCommand, ids.System{}.New(), ids.System{}.New(), nil)
		if err != nil || !created {
			t.Fatalf("open event: %+v %v %v", incident, created, err)
		}
		return incident
	}
	dev := opsSeedDevice(t, pool, "r3-manual")
	seen := time.Now().UTC()
	if stale {
		seen = seen.Add(-10 * time.Minute)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO device_control_presence (device_id,last_seen_at,last_poll_at,created_at,updated_at)
		VALUES ($1,$2,$2,now(),now())`, mustOpsUUID(t, dev), seen); err != nil {
		t.Fatal(err)
	}
	incident, created, err := opsService(store).OpenStatefulAtomic(ctx, ids.System{}.New(), operations.RuleDeviceOffline,
		operations.SubjectDevice, dev, "", nil)
	if err != nil || !created {
		t.Fatalf("open stateful: %+v %v %v", incident, created, err)
	}
	return incident
}

func r3ResolvedDeliveries(t *testing.T, store Devices, id string) []operations.Delivery {
	t.Helper()
	all, err := store.DeliveriesForIncident(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	var out []operations.Delivery
	for _, d := range all {
		if d.Event == operations.EventResolved {
			out = append(out, d)
		}
	}
	return out
}

func TestR3ManualResolutionCommitsCompleteIntent(t *testing.T) {
	for _, tc := range []struct {
		name       string
		event      bool
		recipients int
	}{
		{"stateful", false, 2}, {"event", true, 2}, {"zero_recipients", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, pool := openOpsStore(t)
			seedOpsMapping(t, store)
			for i := 0; i < tc.recipients; i++ {
				locale := "ar"
				if i == 1 {
					locale = "en"
				}
				opsSeedRecipient(t, store, locale)
			}
			incident := r3ManualIncident(t, store, pool, tc.event, false)
			reader := r3ManualReader(store, opsService(store))
			resolved, err := reader.ResolveOperator(context.Background(), incident.ID, "")
			if err != nil || resolved.State != operations.StateResolved || !resolved.ResolvedIntentMaterialized {
				t.Fatalf("manual resolution: %+v %v", resolved, err)
			}
			first := r3ResolvedDeliveries(t, store, incident.ID)
			if len(first) != tc.recipients {
				t.Fatalf("resolved deliveries=%d, want %d", len(first), tc.recipients)
			}
			if _, found, err := store.RecoveryForIncident(context.Background(), incident.ID); err != nil || found {
				t.Fatalf("manual resolution armed recovery: found=%v err=%v", found, err)
			}
			for _, d := range first {
				if d.Body == "" || len(d.Fingerprint) == 0 || d.NotificationKey == "" || d.TemplateKey != operations.TemplateResolved {
					t.Fatalf("incomplete snapshot: %+v", d)
				}
			}
			for i := 0; i < tc.recipients; i++ {
				processed, err := newAlertProcessorReal(t, store, opsService(store)).ProcessOne(context.Background())
				if err != nil || !processed {
					t.Fatalf("enqueue resolved alert: %v %v", processed, err)
				}
			}
			first = r3ResolvedDeliveries(t, store, incident.ID)
			for _, d := range first {
				if d.NotificationID == nil {
					t.Fatalf("notification ID not persisted: %+v", d)
				}
			}
			replay, err := reader.ResolveOperator(context.Background(), incident.ID, "")
			if err != nil || !replay.ResolvedIntentMaterialized || !reflect.DeepEqual(first, r3ResolvedDeliveries(t, store, incident.ID)) {
				t.Fatalf("replay changed intent: %+v %v", replay, err)
			}
			var notificationRows int
			if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM notification_messages WHERE idempotency_key LIKE 'ops-alert:%'`).Scan(&notificationRows); err != nil || notificationRows != tc.recipients {
				t.Fatalf("notification rows=%d, want %d: %v", notificationRows, tc.recipients, err)
			}
		})
	}
}

func TestR3ManualResolutionPredicateAndRollback(t *testing.T) {
	t.Run("still_active", func(t *testing.T) {
		store, pool := openOpsStore(t)
		opsSeedRecipient(t, store, "ar")
		incident := r3ManualIncident(t, store, pool, false, true)
		_, err := r3ManualReader(store, opsService(store)).ResolveOperator(context.Background(), incident.ID, "")
		current, _ := opsService(store).Get(context.Background(), incident.ID)
		if err == nil || !strings.Contains(err.Error(), "CONDITION_STILL_ACTIVE") || current.State != operations.StateOpen || current.ResolvedIntentMaterialized || len(r3ResolvedDeliveries(t, store, incident.ID)) != 0 {
			t.Fatalf("failed predicate changed intent: %+v %v", current, err)
		}
	})
	t.Run("second_delivery_fails", func(t *testing.T) {
		store, pool := openOpsStore(t)
		opsSeedRecipient(t, store, "ar")
		opsSeedRecipient(t, store, "en")
		incident := r3ManualIncident(t, store, pool, false, false)
		// Abort the second insert after the first was written in the same
		// transaction. The incident update and first delivery must roll back.
		_, err := pool.Exec(context.Background(), `CREATE FUNCTION r3_fail_second_delivery() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN IF (SELECT count(*) FROM operational_alert_deliveries WHERE incident_id=NEW.incident_id AND event_type='resolved') > 0
		THEN RAISE EXCEPTION 'r3 injected failure'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER r3_fail_second_delivery BEFORE INSERT ON operational_alert_deliveries
		FOR EACH ROW EXECUTE FUNCTION r3_fail_second_delivery()`)
		if err != nil {
			t.Fatal(err)
		}
		_, err = r3ManualReader(store, opsService(store)).ResolveOperator(context.Background(), incident.ID, "")
		current, _ := opsService(store).Get(context.Background(), incident.ID)
		if err == nil || current.State != operations.StateOpen || current.ResolvedIntentMaterialized || len(r3ResolvedDeliveries(t, store, incident.ID)) != 0 {
			t.Fatalf("partial commit: %+v %v", current, err)
		}
		var notifications int
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM notification_messages`).Scan(&notifications); err != nil || notifications != 0 {
			t.Fatalf("failed transaction enqueued notifications=%d: %v", notifications, err)
		}
	})
}

func TestR3ManualResolversRaceOneIntent(t *testing.T) {
	store, pool := openOpsStore(t)
	opsSeedRecipient(t, store, "ar")
	incident := r3ManualIncident(t, store, pool, false, false)
	reader := r3ManualReader(store, opsService(store))
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := reader.ResolveOperator(context.Background(), incident.ID, "")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	current, _ := opsService(store).Get(context.Background(), incident.ID)
	if !current.ResolvedIntentMaterialized || len(r3ResolvedDeliveries(t, store, incident.ID)) != 1 {
		t.Fatalf("duplicate or missing intent: %+v", current)
	}
}

func TestR3ManualAndAutomaticResolutionRace(t *testing.T) {
	env := newOpsDetectorEnv(t, true)
	ctx := context.Background()
	seedOpsMapping(t, env.store)
	opsSeedRecipient(t, env.store, "ar")
	incident := r3ManualIncident(t, env.store, env.pool, false, false)
	reader := r3ManualReader(env.store, env.svc)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := reader.ResolveOperator(ctx, incident.ID, "")
		errs <- err
	}()
	go func() {
		defer wg.Done()
		errs <- env.detector.Scan(ctx)
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	current, _ := env.svc.Get(ctx, incident.ID)
	if current.State != operations.StateResolved || !current.ResolvedIntentMaterialized || len(r3ResolvedDeliveries(t, env.store, incident.ID)) != 1 {
		t.Fatalf("race left incomplete or duplicate intent: %+v", current)
	}
	recovery, found, err := env.store.RecoveryForIncident(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.ResolutionCode != nil && *current.ResolutionCode == operations.ResolutionOperator && found {
		t.Fatalf("operator winner armed healing: %+v", recovery)
	}
	processed, err := newAlertProcessorReal(t, env.store, env.svc).ProcessOne(ctx)
	if err != nil || !processed {
		t.Fatalf("enqueue winner intent: %v %v", processed, err)
	}
	var notifications int
	if err := env.pool.QueryRow(ctx, `SELECT count(*) FROM notification_messages WHERE idempotency_key LIKE 'ops-alert:%'`).Scan(&notifications); err != nil || notifications != 1 {
		t.Fatalf("race produced %d notifications: %v", notifications, err)
	}
}

func TestR3ManualReplayPreservesAmbiguousLegacy(t *testing.T) {
	store, pool := openOpsStore(t)
	opsSeedRecipient(t, store, "ar")
	incident := r3ManualIncident(t, store, pool, true, false)
	legacy, err := opsService(store).Resolve(context.Background(), incident.ID, operations.ResolutionOperator)
	if err != nil || legacy.ResolvedIntentMaterialized {
		t.Fatalf("legacy setup: %+v %v", legacy, err)
	}
	replayed, err := r3ManualReader(store, opsService(store)).ResolveOperator(context.Background(), incident.ID, "")
	if err != nil || replayed.ResolvedIntentMaterialized || len(r3ResolvedDeliveries(t, store, incident.ID)) != 0 {
		t.Fatalf("ambiguous legacy state fabricated intent: %+v %v", replayed, err)
	}
}

func r3WaitForLock(t *testing.T, pool *pgxpool.Pool, queryFragment string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		err := pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%' || $1 || '%'`, queryFragment).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no transaction reached lock barrier %s", queryFragment)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestR3RevocationWinsBeforeLifecycleDecision(t *testing.T) {
	env := newOpsDetectorEnv(t, true)
	ctx := context.Background()
	dev := env.device(t, "r3-revoke-first")
	_, err := env.pool.Exec(ctx, `INSERT INTO device_control_presence (device_id,last_seen_at,last_poll_at,created_at,updated_at)
		VALUES ($1,now()-interval '10 minutes',now(),now(),now())`, mustOpsUUID(t, dev))
	if err != nil {
		t.Fatal(err)
	}
	env.scan(t)
	incident, found, err := env.store.ActiveIncident(ctx, operations.RuleDeviceOffline, operations.SubjectDevice, dev)
	if err != nil || !found {
		t.Fatalf("offline incident: %v %v", found, err)
	}
	if err := env.store.TouchSeen(ctx, dev, time.Now().UTC(), true); err != nil {
		t.Fatal(err)
	}
	holder, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx)
	var one int
	if err := holder.QueryRow(ctx, `SELECT 1 FROM operational_incidents WHERE id=$1 FOR UPDATE`, mustOpsUUID(t, incident.ID)).Scan(&one); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- env.detector.Scan(ctx) }()
	r3WaitForLock(t, env.pool, "TouchIncidentObserved")
	if _, err := env.pool.Exec(ctx, `UPDATE devices SET status='revoked' WHERE id=$1`, mustOpsUUID(t, dev)); err != nil {
		t.Fatal(err)
	}
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	final, _ := env.svc.Get(ctx, incident.ID)
	if final.ResolutionCode == nil || *final.ResolutionCode != operations.ResolutionNoActive || !final.ResolvedIntentMaterialized {
		t.Fatalf("revocation did not win: %+v", final)
	}
	if _, found, err := env.store.RecoveryForIncident(ctx, incident.ID); err != nil || found {
		t.Fatalf("revocation armed healing: found=%v err=%v", found, err)
	}
}

func TestR3RevokedWithFreshPresenceNeverHeals(t *testing.T) {
	env := newOpsDetectorEnv(t, true)
	ctx := context.Background()
	dev := env.device(t, "r3-revoked-fresh")
	_, err := env.pool.Exec(ctx, `INSERT INTO device_control_presence (device_id,last_seen_at,last_poll_at,created_at,updated_at)
		VALUES ($1,now()-interval '10 minutes',now(),now(),now())`, mustOpsUUID(t, dev))
	if err != nil {
		t.Fatal(err)
	}
	env.scan(t)
	incident, found, err := env.store.ActiveIncident(ctx, operations.RuleDeviceOffline, operations.SubjectDevice, dev)
	if err != nil || !found {
		t.Fatalf("offline incident: %v %v", found, err)
	}
	if err := env.store.TouchSeen(ctx, dev, time.Now().UTC(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(ctx, `UPDATE devices SET status='revoked' WHERE id=$1`, mustOpsUUID(t, dev)); err != nil {
		t.Fatal(err)
	}
	env.scan(t)
	final, _ := env.svc.Get(ctx, incident.ID)
	if final.ResolutionCode == nil || *final.ResolutionCode != operations.ResolutionNoActive || !final.ResolvedIntentMaterialized {
		t.Fatalf("fresh contact overrode revocation: %+v", final)
	}
	if _, found, err := env.store.RecoveryForIncident(ctx, incident.ID); err != nil || found {
		t.Fatalf("revoked device armed recovery: found=%v err=%v", found, err)
	}
	env.drainRecovery(t)
	if env.command.calls != 0 {
		t.Fatalf("revoked device queued commands: %d", env.command.calls)
	}
}

func TestR3ReconnectDecisionPrecedesRevocation(t *testing.T) {
	env := newOpsDetectorEnv(t, true)
	ctx := context.Background()
	dev := env.device(t, "r3-resolve-first")
	_, err := env.pool.Exec(ctx, `INSERT INTO device_control_presence (device_id,last_seen_at,last_poll_at,created_at,updated_at)
		VALUES ($1,now()-interval '10 minutes',now(),now(),now())`, mustOpsUUID(t, dev))
	if err != nil {
		t.Fatal(err)
	}
	env.scan(t)
	incident, found, err := env.store.ActiveIncident(ctx, operations.RuleDeviceOffline, operations.SubjectDevice, dev)
	if err != nil || !found {
		t.Fatalf("offline incident: %v %v", found, err)
	}
	if err := env.store.TouchSeen(ctx, dev, time.Now().UTC(), true); err != nil {
		t.Fatal(err)
	}
	// The trigger parks only the terminal incident UPDATE. By then the
	// resolver owns the device lifecycle row; revocation must wait.
	_, err = env.pool.Exec(ctx, `CREATE FUNCTION r3_park_resolution() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN IF NEW.state='resolved' AND OLD.state<>'resolved' THEN PERFORM pg_advisory_xact_lock(734003); END IF; RETURN NEW; END $$;
	CREATE TRIGGER r3_park_resolution BEFORE UPDATE ON operational_incidents
	FOR EACH ROW EXECUTE FUNCTION r3_park_resolution()`)
	if err != nil {
		t.Fatal(err)
	}
	holder, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(ctx)
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock(734003)`); err != nil {
		t.Fatal(err)
	}
	resolved := make(chan error, 1)
	go func() { resolved <- env.detector.Scan(ctx) }()
	r3WaitForLock(t, env.pool, "ResolveIncident")
	revoked := make(chan error, 1)
	go func() {
		_, err := env.pool.Exec(ctx, `UPDATE devices SET status='revoked' WHERE id=$1`, mustOpsUUID(t, dev))
		revoked <- err
	}()
	r3WaitForLock(t, env.pool, "UPDATE devices SET status='revoked'")
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-resolved; err != nil {
		t.Fatal(err)
	}
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	final, _ := env.svc.Get(ctx, incident.ID)
	if final.ResolutionCode == nil || *final.ResolutionCode != operations.ResolutionReconnect || !final.ResolvedIntentMaterialized {
		t.Fatalf("committed reconnect decision changed: %+v", final)
	}
	if _, found, err := env.store.RecoveryForIncident(ctx, incident.ID); err != nil || !found {
		t.Fatalf("committed reconnect omitted healing: found=%v err=%v", found, err)
	}
}
