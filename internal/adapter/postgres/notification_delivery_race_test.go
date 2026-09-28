package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/notifications"
)

// barrierNotification enqueues, claims, and accepts one notification,
// returning its id and provider message ID for delivery-race tests.
func barrierNotification(t *testing.T, env *saleEnv, key, wamid string) string {
	t.Helper()
	ctx := context.Background()
	store := catalogStore(env)
	intent := notificationTestIntent("whatsapp-main", key)
	id := enqueueNotification(t, env, intent)
	claimed := claimNotification(t, env, "worker-A")
	if claimed.ID != id {
		t.Fatalf("claim order: %v %v", claimed.ID, id)
	}
	if result, err := store.FinishNotificationAccepted(ctx, id, claimed.LeaseOwner, claimed.LeaseGeneration, wamid); err != nil || result != notifications.FinishApplied {
		t.Fatalf("accept: %v %v", result, err)
	}
	return id
}

// statusApplier applies one raw status at a second offset from base.
func statusApplier(env *saleEnv, id, wamid, raw string, base time.Time, offset int64) func() {
	return func() {
		at := base.Add(time.Duration(offset) * time.Second)
		event := notifications.DeliveryEvent{
			ProviderMessageID: wamid, RawStatus: raw,
			Canonical:         notifications.MapProviderStatus(raw),
			ProviderTimestamp: &at,
		}
		event.Fingerprint = notifications.DeliveryEventFingerprint(
			"whatsapp-main", event.ProviderMessageID, event.RawStatus,
			event.Canonical, event.ProviderTimestamp, "")
		if _, err := catalogStore(env).ApplyDeliveryStatus(context.Background(), id, event); err != nil {
			panic(err)
		}
	}
}

// runConcurrent launches all callbacks behind a start barrier so their
// transactions genuinely overlap, then waits.
func runConcurrent(t *testing.T, callbacks ...func()) {
	t.Helper()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, callback := range callbacks {
		wg.Add(1)
		go func(callback func()) {
			defer wg.Done()
			<-start
			callback()
		}(callback)
	}
	close(start)
	wg.Wait()
}

func deliveryCurrent(t *testing.T, env *saleEnv, id string) string {
	t.Helper()
	var status string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT delivery_status FROM notification_messages WHERE id = $1`,
		mustPgID(id)).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func deliveryHistoryCount(t *testing.T, env *saleEnv, id string) int {
	t.Helper()
	var count int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM notification_delivery_status_history WHERE notification_id = $1`,
		mustPgID(id)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// TestDeliveryBarrierReadVsDelivered is the M-7A-001 gate: initial
// SENT T1, then concurrent READ T3 vs DELIVERED T2. Both launch orders
// must converge on READ T3 with complete unique history.
func TestDeliveryBarrierReadVsDelivered(t *testing.T) {
	for _, order := range []struct {
		name  string
		first bool
	}{
		{"read launches first", true},
		{"delivered launches first", false},
	} {
		t.Run(order.name, func(t *testing.T) {
			env := openSaleEnv(t)
			base := time.Now().UTC().Truncate(time.Second)
			id := barrierNotification(t, env, "barrier-1", "wamid.barrier1")
			statusApplier(env, id, "wamid.barrier1", "sent", base, 10)()
			read := statusApplier(env, id, "wamid.barrier1", "read", base, 30)
			delivered := statusApplier(env, id, "wamid.barrier1", "delivered", base, 20)
			if order.first {
				// Slight stagger still overlaps inside the transactions.
				go read()
				time.Sleep(20 * time.Millisecond)
				delivered()
			} else {
				go delivered()
				time.Sleep(20 * time.Millisecond)
				read()
			}
			if status := deliveryCurrent(t, env, id); status != "READ" {
				t.Fatalf("final READ, got %s", status)
			}
			if count := deliveryHistoryCount(t, env, id); count != 3 {
				t.Fatalf("history complete: %d", count)
			}
		})
	}
	t.Run("fully overlapped", func(t *testing.T) {
		env := openSaleEnv(t)
		base := time.Now().UTC().Truncate(time.Second)
		id := barrierNotification(t, env, "barrier-2", "wamid.barrier2")
		statusApplier(env, id, "wamid.barrier2", "sent", base, 10)()
		runConcurrent(t,
			statusApplier(env, id, "wamid.barrier2", "read", base, 30),
			statusApplier(env, id, "wamid.barrier2", "delivered", base, 20),
		)
		if status := deliveryCurrent(t, env, id); status != "READ" {
			t.Fatalf("final READ, got %s", status)
		}
		if count := deliveryHistoryCount(t, env, id); count != 3 {
			t.Fatalf("history complete: %d", count)
		}
	})
}

// TestDeliveryThreeStatusConcurrent synchronizes SENT T1, DELIVERED
// T2, READ T3 from ACCEPTED: final READ, each unique event once.
func TestDeliveryThreeStatusConcurrent(t *testing.T) {
	env := openSaleEnv(t)
	base := time.Now().UTC().Truncate(time.Second)
	id := barrierNotification(t, env, "three-1", "wamid.three1")
	runConcurrent(t,
		statusApplier(env, id, "wamid.three1", "sent", base, 10),
		statusApplier(env, id, "wamid.three1", "delivered", base, 20),
		statusApplier(env, id, "wamid.three1", "read", base, 30),
	)
	if status := deliveryCurrent(t, env, id); status != "READ" {
		t.Fatalf("final READ, got %s", status)
	}
	if count := deliveryHistoryCount(t, env, id); count != 3 {
		t.Fatalf("history: %d", count)
	}
}

// TestDeliverySameTimestampConcurrent proves same-instant precedence
// under concurrency in both launch orders.
func TestDeliverySameTimestampConcurrent(t *testing.T) {
	for _, name := range []string{"read-first", "delivered-first"} {
		t.Run(name, func(t *testing.T) {
			env := openSaleEnv(t)
			base := time.Now().UTC().Truncate(time.Second)
			id := barrierNotification(t, env, "samets-1", "wamid.samets1")
			read := statusApplier(env, id, "wamid.samets1", "read", base, 30)
			delivered := statusApplier(env, id, "wamid.samets1", "delivered", base, 30)
			if name == "read-first" {
				runConcurrent(t, read, delivered)
			} else {
				runConcurrent(t, delivered, read)
			}
			if status := deliveryCurrent(t, env, id); status != "READ" {
				t.Fatalf("same-timestamp READ, got %s", status)
			}
		})
	}
}

// TestDeliveryFailedSameTimestampConcurrent proves FAILED stays
// terminal-conservative at equal timestamps per AdvanceDelivery,
// independent of launch order.
func TestDeliveryFailedSameTimestampConcurrent(t *testing.T) {
	for _, name := range []string{"failed-first", "read-first"} {
		t.Run(name, func(t *testing.T) {
			env := openSaleEnv(t)
			base := time.Now().UTC().Truncate(time.Second)
			id := barrierNotification(t, env, "failts-1", "wamid.failts1")
			failed := statusApplier(env, id, "wamid.failts1", "failed", base, 30)
			read := statusApplier(env, id, "wamid.failts1", "read", base, 30)
			if name == "failed-first" {
				runConcurrent(t, failed, read)
			} else {
				runConcurrent(t, read, failed)
			}
			if status := deliveryCurrent(t, env, id); status != "FAILED" {
				t.Fatalf("same-timestamp FAILED, got %s", status)
			}
			var dispatch string
			if err := env.pool.QueryRow(context.Background(),
				`SELECT dispatch_status FROM notification_messages WHERE id = $1`,
				mustPgID(id)).Scan(&dispatch); err != nil || dispatch != "accepted" {
				t.Fatalf("dispatch stays accepted: %s %v", dispatch, err)
			}
		})
	}
}

// TestDeliveryUnknownConcurrent proves a concurrent UNKNOWN cannot
// regress known state, while its raw event stays in history.
func TestDeliveryUnknownConcurrent(t *testing.T) {
	env := openSaleEnv(t)
	base := time.Now().UTC().Truncate(time.Second)
	id := barrierNotification(t, env, "unk-1", "wamid.unk1")
	unknown := func() {
		at := base.Add(40 * time.Second)
		event := notifications.DeliveryEvent{
			ProviderMessageID: "wamid.unk1", RawStatus: "future_status_x",
			Canonical: notifications.DeliveryUnknown, ProviderTimestamp: &at,
		}
		event.Fingerprint = notifications.DeliveryEventFingerprint(
			"whatsapp-main", event.ProviderMessageID, event.RawStatus,
			event.Canonical, event.ProviderTimestamp, "")
		if _, err := catalogStore(env).ApplyDeliveryStatus(context.Background(), id, event); err != nil {
			panic(err)
		}
	}
	runConcurrent(t,
		unknown,
		statusApplier(env, id, "wamid.unk1", "read", base, 30),
	)
	if status := deliveryCurrent(t, env, id); status != "READ" {
		t.Fatalf("UNKNOWN must not regress: %s", status)
	}
	if count := deliveryHistoryCount(t, env, id); count != 2 {
		t.Fatalf("unknown retained in history: %d", count)
	}
}

// TestDeliveryDuplicateRace proves two identical racing callbacks
// record one history row with correct current state.
func TestDeliveryDuplicateRace(t *testing.T) {
	env := openSaleEnv(t)
	base := time.Now().UTC().Truncate(time.Second)
	id := barrierNotification(t, env, "dup-1", "wamid.dup1")
	first := statusApplier(env, id, "wamid.dup1", "delivered", base, 20)
	second := statusApplier(env, id, "wamid.dup1", "delivered", base, 20)
	runConcurrent(t, first, second)
	if status := deliveryCurrent(t, env, id); status != "DELIVERED" {
		t.Fatalf("current: %s", status)
	}
	if count := deliveryHistoryCount(t, env, id); count != 1 {
		t.Fatalf("one history row: %d", count)
	}
}

// TestDeliveryDifferentNotificationsConcurrent proves callbacks for
// distinct notifications proceed independently with no deadlock and
// correct per-notification finals.
func TestDeliveryDifferentNotificationsConcurrent(t *testing.T) {
	env := openSaleEnv(t)
	base := time.Now().UTC().Truncate(time.Second)
	idA := barrierNotification(t, env, "diff-a", "wamid.diffA")
	idB := barrierNotification(t, env, "diff-b", "wamid.diffB")
	runConcurrent(t,
		statusApplier(env, idA, "wamid.diffA", "read", base, 30),
		statusApplier(env, idB, "wamid.diffB", "delivered", base, 20),
		statusApplier(env, idA, "wamid.diffA", "sent", base, 10),
		statusApplier(env, idB, "wamid.diffB", "read", base, 40),
	)
	if status := deliveryCurrent(t, env, idA); status != "READ" {
		t.Fatalf("A READ: %s", status)
	}
	if status := deliveryCurrent(t, env, idB); status != "READ" {
		t.Fatalf("B READ: %s", status)
	}
}

// TestDeliveryHighConcurrency mixes 16 racing callbacks (statuses,
// duplicates, older events): current always equals the domain-max
// event and unique history stays complete.
func TestDeliveryHighConcurrency(t *testing.T) {
	env := openSaleEnv(t)
	base := time.Now().UTC().Truncate(time.Second)
	id := barrierNotification(t, env, "high-1", "wamid.high1")
	specs := []struct {
		raw    string
		offset int64
	}{
		{"sent", 10}, {"delivered", 20}, {"read", 30},
		{"sent", 10}, {"delivered", 20}, {"read", 30},
		{"sent", 5}, {"delivered", 15}, {"read", 25},
		{"sent", 12}, {"delivered", 22}, {"read", 28},
		{"failed", 26}, {"future_status_x", 35}, {"sent", 10}, {"read", 30},
	}
	var callbacks []func()
	for _, spec := range specs {
		callbacks = append(callbacks, statusApplier(env, id, "wamid.high1", spec.raw, base, spec.offset))
	}
	// future_status_x maps to UNKNOWN: history only, never current.
	runConcurrent(t, callbacks...)
	if status := deliveryCurrent(t, env, id); status != "READ" {
		t.Fatalf("domain-max READ, got %s", status)
	}
	// Unique events: sent@5,10,12 + delivered@15,20,22 + read@25,28,30
	// + failed@26 + unknown@35 = 11 distinct fingerprints.
	if count := deliveryHistoryCount(t, env, id); count != 11 {
		t.Fatalf("unique history: %d", count)
	}
	// History/current consistency invariant: current equals the max
	// provider-timestamp event under domain precedence, restricted to
	// state-advancing statuses (UNKNOWN is history-only by design).
	var maxRaw string
	var maxTs time.Time
	rows, err := env.pool.Query(context.Background(),
		`SELECT provider_status_raw, canonical_status, provider_timestamp
		 FROM notification_delivery_status_history WHERE notification_id = $1
		 AND canonical_status <> 'UNKNOWN'`, mustPgID(id))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	best := -1
	for rows.Next() {
		var raw, canonical string
		var ts time.Time
		if err := rows.Scan(&raw, &canonical, &ts); err != nil {
			t.Fatal(err)
		}
		if best == -1 || ts.After(maxTs) || (ts.Equal(maxTs) &&
			notifications.DeliveryPrecedence(notifications.DeliveryStatus(canonical)) >
				notifications.DeliveryPrecedence(notifications.DeliveryStatus(maxRaw))) {
			maxRaw, maxTs, best = canonical, ts, 0
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if current := deliveryCurrent(t, env, id); current != maxRaw {
		t.Fatalf("current %s != history max %s", current, maxRaw)
	}
}

// TestDeliveryAtomicityRollback proves a history-insert failure rolls
// back the whole callback: an event violating the canonical-status
// constraint leaves neither history nor current-state change.
func TestDeliveryAtomicityRollback(t *testing.T) {
	env := openSaleEnv(t)
	base := time.Now().UTC().Truncate(time.Second)
	id := barrierNotification(t, env, "atomic-1", "wamid.atomic1")
	statusApplier(env, id, "wamid.atomic1", "sent", base, 10)()
	at := base.Add(20 * time.Second)
	event := notifications.DeliveryEvent{
		ProviderMessageID: "wamid.atomic1", RawStatus: "bogus",
		Canonical: "BOGUS", ProviderTimestamp: &at,
	}
	event.Fingerprint = notifications.DeliveryEventFingerprint(
		"whatsapp-main", event.ProviderMessageID, event.RawStatus,
		event.Canonical, event.ProviderTimestamp, "")
	if _, err := catalogStore(env).ApplyDeliveryStatus(context.Background(), id, event); err == nil {
		t.Fatal("constraint violation must fail")
	}
	if status := deliveryCurrent(t, env, id); status != "SENT" {
		t.Fatalf("current intact: %s", status)
	}
	if count := deliveryHistoryCount(t, env, id); count != 1 {
		t.Fatalf("no partial history: %d", count)
	}
}
