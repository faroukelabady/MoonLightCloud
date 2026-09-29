package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/businessreports"
	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
	"github.com/google/uuid"
)

// fencingFixture creates a run with the requested number of pending
// deliveries and returns its IDs. All deliveries start bodyless.
func fencingFixture(t *testing.T, env *saleEnv, service *businessreports.Service, scheduleID, manualKey string) (string, []string) {
	t.Helper()
	runID, _, err := service.RunNow(context.Background(), scheduleID, manualKey)
	if err != nil {
		t.Fatalf("run-now: %v", err)
	}
	detail, err := service.GetRunStatus(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(detail.Deliveries))
	for _, delivery := range detail.Deliveries {
		ids = append(ids, delivery.ID)
	}
	return runID, ids
}

func fencingSchedule(t *testing.T, env *saleEnv, service *businessreports.Service, n int) string {
	t.Helper()
	ctx := context.Background()
	var ids []string
	for i := 0; i < n; i++ {
		recipient, err := service.AddRecipient(ctx, "owner", "whatsapp-main", "201012345678", "ar")
		if err != nil {
			t.Fatalf("recipient: %v", err)
		}
		ids = append(ids, recipient.ID)
	}
	schedule, err := service.CreateSchedule(ctx, "fencing", "DAILY", "21:00", "", ids)
	if err != nil {
		t.Fatalf("schedule: %v", err)
	}
	return schedule.ID
}

func expireRunLease(t *testing.T, env *saleEnv, runID string) {
	t.Helper()
	if _, err := env.pool.Exec(context.Background(),
		`UPDATE business_report_runs SET lease_until = now() - interval '1 minute' WHERE id = $1`,
		mustReportPgID(runID)); err != nil {
		t.Fatal(err)
	}
}

func deliveryBodies(t *testing.T, env *saleEnv, runID string) map[string]bool {
	t.Helper()
	rows, err := env.pool.Query(context.Background(),
		`SELECT id, report_body_snapshot IS NOT NULL FROM business_report_deliveries WHERE run_id = $1`,
		mustReportPgID(runID))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		var has bool
		if err := rows.Scan(&id, &has); err != nil {
			t.Fatal(err)
		}
		out[id] = has
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func notificationCount(t *testing.T, env *saleEnv) int {
	t.Helper()
	var n int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM notification_messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// waitForLockWait polls pg_stat_activity until a backend is
// observed waiting on a lock while running a query matching pattern,
// proving the mutator reached its guarded statement before the test
// advances the scenario.
func waitForLockWait(t *testing.T, env *saleEnv, pattern string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var n int
		if err := env.pool.QueryRow(context.Background(), `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database()
			  AND wait_event_type = 'Lock'
			  AND query ILIKE $1`, pattern).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n >= 1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("mutator never reached the lock wait for %s", pattern)
}

// waitForDeliveryLockWait proves the mutator reached its guarded
// delivery UPDATE before the test advances the scenario.
func waitForDeliveryLockWait(t *testing.T, env *saleEnv, timeout time.Duration) {
	t.Helper()
	waitForLockWait(t, env, "%business_report_deliveries%", timeout)
}

// TestDeliveryFencingExpiredSnapshot is scenario A: an expired,
// unreclaimed claim cannot persist snapshots.
func TestDeliveryFencingExpiredSnapshot(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	scheduleID := fencingSchedule(t, env, service, 2)
	seedReportTemplateMapping(t, env)
	runID, deliveryIDs := fencingFixture(t, env, service, scheduleID, "manual-a")
	claimed, ok, err := store.ClaimRun(ctx, "worker-A", 5*time.Minute, time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	_ = claimed
	expireRunLease(t, env, runID)
	snapshots := map[string]businessreports.SnapshotBody{}
	for _, id := range deliveryIDs {
		snapshots[id] = businessreports.SnapshotBody{Body: "x", Fingerprint: []byte{1}}
	}
	applied, err := store.PersistRunDeliverySnapshots(ctx, runID,
		claimed.LeaseOwner, claimed.LeaseGeneration, snapshots)
	if err != nil {
		t.Fatalf("stale persist must not error: %v", err)
	}
	if applied {
		t.Fatal("expired claim must not persist snapshots")
	}
	for id, has := range deliveryBodies(t, env, runID) {
		if has {
			t.Fatalf("zero bodies persisted, got body on %s", id)
		}
	}
	if n := notificationCount(t, env); n != 0 {
		t.Fatalf("zero notifications, got %d", n)
	}
}

// TestDeliveryFencingSupersededSnapshot is scenario B: after takeover,
// the old generation's snapshot write stores nothing.
func TestDeliveryFencingSupersededSnapshot(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	scheduleID := fencingSchedule(t, env, service, 1)
	seedReportTemplateMapping(t, env)
	runID, deliveryIDs := fencingFixture(t, env, service, scheduleID, "manual-b")
	claimedA, ok, err := store.ClaimRun(ctx, "worker-A", 5*time.Minute, time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("claim A: %v %v", ok, err)
	}
	expireRunLease(t, env, runID)
	claimedB, ok, err := store.ClaimRun(ctx, "worker-B", 5*time.Minute, time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("claim B: %v %v", ok, err)
	}
	if claimedB.LeaseGeneration != claimedA.LeaseGeneration+1 {
		t.Fatalf("generation advanced: %+v %+v", claimedA, claimedB)
	}
	applied, err := store.PersistRunDeliverySnapshots(ctx, runID,
		claimedA.LeaseOwner, claimedA.LeaseGeneration,
		map[string]businessreports.SnapshotBody{deliveryIDs[0]: {Body: "stale", Fingerprint: []byte{9}}})
	if err != nil {
		t.Fatalf("stale persist must not error: %v", err)
	}
	if applied {
		t.Fatal("superseded claim must not persist snapshots")
	}
	for id, has := range deliveryBodies(t, env, runID) {
		if has {
			t.Fatalf("zero bodies persisted, got body on %s", id)
		}
	}
}

// TestDeliveryFencingSnapshotRollback is scenario C: a mid-batch guard
// failure rolls back every snapshot write in the barrier transaction.
func TestDeliveryFencingSnapshotRollback(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	scheduleID := fencingSchedule(t, env, service, 2)
	seedReportTemplateMapping(t, env)
	runID, deliveryIDs := fencingFixture(t, env, service, scheduleID, "manual-c")
	claimed, ok, err := store.ClaimRun(ctx, "worker-A", 5*time.Minute, time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	// Pre-existing body on the second delivery (legacy partial state).
	pre, err := store.PersistDeliverySnapshot(ctx, runID, deliveryIDs[1],
		claimed.LeaseOwner, claimed.LeaseGeneration, "original", []byte{2})
	if err != nil || !pre {
		t.Fatalf("pre-persist: %v %v", pre, err)
	}
	// Barrier over both deliveries must fail atomically.
	applied, err := store.PersistRunDeliverySnapshots(ctx, runID,
		claimed.LeaseOwner, claimed.LeaseGeneration,
		map[string]businessreports.SnapshotBody{
			deliveryIDs[0]: {Body: "new-a", Fingerprint: []byte{1}},
			deliveryIDs[1]: {Body: "new-b", Fingerprint: []byte{2}},
		})
	if err != nil {
		t.Fatalf("barrier must not error: %v", err)
	}
	if applied {
		t.Fatal("partial barrier must roll back")
	}
	bodies := deliveryBodies(t, env, runID)
	if bodies[deliveryIDs[0]] {
		t.Fatal("first delivery must have no body after rollback")
	}
	if !bodies[deliveryIDs[1]] {
		t.Fatal("pre-existing body must survive rollback")
	}
	var body string
	if err := env.pool.QueryRow(ctx,
		`SELECT report_body_snapshot FROM business_report_deliveries WHERE id = $1`,
		mustReportPgID(deliveryIDs[1])).Scan(&body); err != nil || body != "original" {
		t.Fatalf("original body intact: %q %v", body, err)
	}
	if n := notificationCount(t, env); n != 0 {
		t.Fatalf("zero notifications, got %d", n)
	}
}

// TestDeliveryFencingDelayedBlocked is scenario D: a blocked mutation
// that waits on a child-row lock while its lease expires must apply
// zero rows once released.
func TestDeliveryFencingDelayedBlocked(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	scheduleID := fencingSchedule(t, env, service, 1)
	seedReportTemplateMapping(t, env)
	runID, deliveryIDs := fencingFixture(t, env, service, scheduleID, "manual-d")
	deliveryID := deliveryIDs[0]
	claimTime := time.Now().UTC()
	claimedA, ok, err := store.ClaimRun(ctx, "worker-A", 2*time.Second, claimTime)
	if err != nil || !ok {
		t.Fatalf("claim A: %v %v", ok, err)
	}
	// Hold the delivery row lock in an open transaction.
	holder, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx,
		`SELECT id FROM business_report_deliveries WHERE id = $1 FOR UPDATE`,
		mustReportPgID(deliveryID)); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		applied bool
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		applied, err := store.FinishDeliveryBlocked(ctx, runID, deliveryID,
			claimedA.LeaseOwner, claimedA.LeaseGeneration, "BLOCKED_FOR_TEST")
		done <- outcome{applied, err}
	}()
	waitForDeliveryLockWait(t, env, 4*time.Second)
	// Let A's lease expire while it waits, measured from claim time.
	if wait := time.Until(claimTime.Add(2300 * time.Millisecond)); wait > 0 {
		time.Sleep(wait)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.err != nil {
		t.Fatalf("stale finish must not error: %v", result.err)
	}
	if result.applied {
		t.Fatal("expired waiter must apply zero rows")
	}
	var status string
	if err := env.pool.QueryRow(ctx,
		`SELECT status FROM business_report_deliveries WHERE id = $1`,
		mustReportPgID(deliveryID)).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("delivery still pending: %q %v", status, err)
	}
	// B claims the next generation and finishes normally.
	claimedB, ok, err := store.ClaimRun(ctx, "worker-B", 5*time.Minute, time.Now().UTC())
	if err != nil || !ok || claimedB.LeaseGeneration != claimedA.LeaseGeneration+1 {
		t.Fatalf("claim B: %v %v %+v", ok, err, claimedB)
	}
	applied, err := store.FinishDeliveryBlocked(ctx, runID, deliveryID,
		claimedB.LeaseOwner, claimedB.LeaseGeneration, "BLOCKED_FOR_TEST")
	if err != nil || !applied {
		t.Fatalf("current owner applies: %v %v", applied, err)
	}
}

// TestDeliveryFencingDelayedEnqueued is scenario E: the enqueued
// variant, including notification-ID persistence fencing.
func TestDeliveryFencingDelayedEnqueued(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	scheduleID := fencingSchedule(t, env, service, 1)
	seedReportTemplateMapping(t, env)
	runID, deliveryIDs := fencingFixture(t, env, service, scheduleID, "manual-e")
	deliveryID := deliveryIDs[0]
	claimTime := time.Now().UTC()
	claimedA, ok, err := store.ClaimRun(ctx, "worker-A", 2*time.Second, claimTime)
	if err != nil || !ok {
		t.Fatalf("claim A: %v %v", ok, err)
	}
	holder, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx,
		`SELECT id FROM business_report_deliveries WHERE id = $1 FOR UPDATE`,
		mustReportPgID(deliveryID)); err != nil {
		t.Fatal(err)
	}
	wamid := uuid.NewString()
	type outcome struct {
		applied bool
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		applied, err := store.FinishDeliveryEnqueued(ctx, runID, deliveryID,
			claimedA.LeaseOwner, claimedA.LeaseGeneration, wamid)
		done <- outcome{applied, err}
	}()
	waitForDeliveryLockWait(t, env, 4*time.Second)
	if wait := time.Until(claimTime.Add(2300 * time.Millisecond)); wait > 0 {
		time.Sleep(wait)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.err != nil {
		t.Fatalf("stale finish must not error: %v", result.err)
	}
	if result.applied {
		t.Fatal("expired waiter must apply zero rows")
	}
	var status string
	var notificationID *string
	if err := env.pool.QueryRow(ctx,
		`SELECT status, notification_id::text FROM business_report_deliveries WHERE id = $1`,
		mustReportPgID(deliveryID)).Scan(&status, &notificationID); err != nil || status != "pending" || notificationID != nil {
		t.Fatalf("no stale write: %q %v %v", status, notificationID, err)
	}
}

// TestDeliveryFencingParentLockExpiry is scenario F: a mutation that
// waits on the parent lock past its own lease expiry is rejected
// after acquiring the lock.
func TestDeliveryFencingParentLockExpiry(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	scheduleID := fencingSchedule(t, env, service, 1)
	seedReportTemplateMapping(t, env)
	runID, deliveryIDs := fencingFixture(t, env, service, scheduleID, "manual-f")
	deliveryID := deliveryIDs[0]
	claimedA, ok, err := store.ClaimRun(ctx, "worker-A", 1500*time.Millisecond, time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("claim A: %v %v", ok, err)
	}
	claimTime := time.Now().UTC()
	// Hold the parent run lock; A's mutation must wait on it.
	holder, err := env.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx,
		`SELECT id FROM business_report_runs WHERE id = $1 FOR UPDATE`,
		mustReportPgID(runID)); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		applied bool
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		applied, err := store.FinishDeliveryBlocked(ctx, runID, deliveryID,
			claimedA.LeaseOwner, claimedA.LeaseGeneration, "BLOCKED_FOR_TEST")
		done <- outcome{applied, err}
	}()
	// Confirm A is waiting on the parent lock while its lease is still
	// valid; only then let time pass the expiry.
	waitForLockWait(t, env, "%business_report_runs%", 4*time.Second)
	sleepUntil := claimTime.Add(1800 * time.Millisecond)
	if wait := time.Until(sleepUntil); wait > 0 {
		time.Sleep(wait)
	}
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.err != nil {
		t.Fatalf("stale finish must not error: %v", result.err)
	}
	if result.applied {
		t.Fatal("post-lock expiry must reject the mutation")
	}
	var status string
	if err := env.pool.QueryRow(ctx,
		`SELECT status FROM business_report_deliveries WHERE id = $1`,
		mustReportPgID(deliveryID)).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("delivery still pending: %q %v", status, err)
	}
}

// TestDeliveryFencingAdoptionAfterLeaseLoss is scenario G: A durably
// enqueues via Phase 7A, loses the lease before storing the ID, B
// adopts the same notification under generation 2.
func TestDeliveryFencingAdoptionAfterLeaseLoss(t *testing.T) {
	env := openSaleEnv(t)
	ctx := context.Background()
	store := catalogStore(env)
	service := reportTestService(t, env, &stubReportSummary{summary: cannedSummary()})
	seedReportTemplateMapping(t, env)
	scheduleID := fencingSchedule(t, env, service, 1)
	runID, deliveryIDs := fencingFixture(t, env, service, scheduleID, "manual-g")
	deliveryID := deliveryIDs[0]
	claimedA, ok, err := store.ClaimRun(ctx, "worker-A", 5*time.Minute, time.Now().UTC())
	if err != nil || !ok {
		t.Fatalf("claim A: %v %v", ok, err)
	}
	detail, err := service.GetRunStatus(ctx, runID)
	if err != nil || len(detail.Deliveries) != 1 {
		t.Fatalf("one delivery: %+v %v", detail, err)
	}
	delivery := detail.Deliveries[0]
	notificationService := notifications.NewService(store, store, nilLogger())
	first, err := notificationService.EnqueueTemplate(ctx, notifications.EnqueueTemplateRequest{
		ProviderKey: delivery.ProviderKey, IdempotencyKey: delivery.NotificationIdempotencyKey,
		Recipient: delivery.Recipient, TemplateKey: delivery.TemplateKey, Locale: delivery.Locale,
		Parameters: map[string]string{"report_body": "adopted-body"},
	})
	if err != nil || !first.Created {
		t.Fatalf("A enqueue: %+v %v", first, err)
	}
	// A loses the lease before storing notification_id.
	expireRunLease(t, env, runID)
	claimedB, ok, err := store.ClaimRun(ctx, "worker-B", 5*time.Minute, time.Now().UTC())
	if err != nil || !ok || claimedB.LeaseGeneration != claimedA.LeaseGeneration+1 {
		t.Fatalf("claim B: %v %v %+v", ok, err, claimedB)
	}
	// A's late store attempt affects zero rows.
	applied, err := store.FinishDeliveryEnqueued(ctx, runID, deliveryID,
		claimedA.LeaseOwner, claimedA.LeaseGeneration, first.ID)
	if err != nil {
		t.Fatalf("stale store must not error: %v", err)
	}
	if applied {
		t.Fatal("stale store must apply zero rows")
	}
	// B retries the same stable key and adopts the same notification.
	second, err := notificationService.EnqueueTemplate(ctx, notifications.EnqueueTemplateRequest{
		ProviderKey: delivery.ProviderKey, IdempotencyKey: delivery.NotificationIdempotencyKey,
		Recipient: delivery.Recipient, TemplateKey: delivery.TemplateKey, Locale: delivery.Locale,
		Parameters: map[string]string{"report_body": "adopted-body"},
	})
	if err != nil {
		t.Fatalf("B enqueue: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("same notification adopted: %q vs %q", second.ID, first.ID)
	}
	applied, err = store.FinishDeliveryEnqueued(ctx, runID, deliveryID,
		claimedB.LeaseOwner, claimedB.LeaseGeneration, second.ID)
	if err != nil || !applied {
		t.Fatalf("B stores under gen2: %v %v", applied, err)
	}
	var storedID string
	if err := env.pool.QueryRow(ctx,
		`SELECT notification_id::text FROM business_report_deliveries WHERE id = $1`,
		mustReportPgID(deliveryID)).Scan(&storedID); err != nil || storedID != first.ID {
		t.Fatalf("notification adopted: %q %v", storedID, err)
	}
	if n := notificationCount(t, env); n != 1 {
		t.Fatalf("one notification row, got %d", n)
	}
}
