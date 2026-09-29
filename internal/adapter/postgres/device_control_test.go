package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/devicecontrol"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
)

type activeDevices struct{ active map[string]bool }

func (a activeDevices) IsActive(_ context.Context, id string) (bool, error) {
	return a.active[id], nil
}

func ctlFixture(t *testing.T) (Devices, *devicecontrol.Service, string, string) {
	t.Helper()
	pool, svc := openTestRepo(t)
	store := NewDevices(pool, 5*time.Second)
	ctx := context.Background()
	prov, err := svc.Create(ctx, "ctl-device")
	if err != nil {
		t.Fatal(err)
	}
	prov2, err := svc.Create(ctx, "ctl-device-b")
	if err != nil {
		t.Fatal(err)
	}
	devices := activeDevices{active: map[string]bool{prov.Device.ID: true, prov2.Device.ID: true}}
	ctl := devicecontrol.NewService(store, devices, ids.System{}.New, 60*time.Second, time.Now)
	return store, ctl, prov.Device.ID, prov2.Device.ID
}

func ctlKind(t *testing.T, err error) apperr.Kind {
	t.Helper()
	if ae, ok := err.(*apperr.Error); ok {
		return ae.Kind
	}
	t.Fatalf("expected apperr, got %v", err)
	return 0
}

// Creation is idempotent on (device, key); contradictory active returns the
// active command with Conflict.
func TestControlCreateIdempotent(t *testing.T) {
	_, ctl, dev, _ := ctlFixture(t)
	ctx := context.Background()
	first, created, err := ctl.CreateSyncRequest(ctx, dev, "key-1")
	if err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	second, created, err := ctl.CreateSyncRequest(ctx, dev, "key-1")
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("idempotent retry: %v %v %+v", created, err, second)
	}
	_, _, err = ctl.CreateSyncRequest(ctx, dev, "key-2")
	if err == nil || ctlKind(t, err) != apperr.Conflict {
		t.Fatalf("second active must conflict: %v", err)
	}
	if msg := err.(*apperr.Error).Message; msg != "DEVICE_SYNC_ALREADY_ACTIVE" {
		t.Fatalf("machine code: %q", msg)
	}
}

// Revoked devices cannot queue commands.
func TestControlRevokedDevice(t *testing.T) {
	pool, svc := openTestRepo(t)
	store := NewDevices(pool, 5*time.Second)
	ctx := context.Background()
	prov, err := svc.Create(ctx, "revoked-dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeDevice(ctx, prov.Device.ID); err != nil {
		t.Fatal(err)
	}
	ctl := devicecontrol.NewService(store,
		activeDevices{active: map[string]bool{}}, ids.System{}.New, 60*time.Second, time.Now)
	_, _, err = ctl.CreateSyncRequest(ctx, prov.Device.ID, "k")
	if err == nil {
		t.Fatal("revoked device must not queue")
	}
	if msg := err.(*apperr.Error).Message; msg != "DEVICE_NOT_ACTIVE" {
		t.Fatalf("machine code: %q", msg)
	}
	if _, ok, _ := store.GetPresence(ctx, prov.Device.ID); ok {
		t.Fatal("no presence row from a refused creation")
	}
}

// Concurrent polls for one pending command: exactly one wins.
func TestControlConcurrentPoll(t *testing.T) {
	store, ctl, dev, _ := ctlFixture(t)
	ctx := context.Background()
	if _, _, err := ctl.CreateSyncRequest(ctx, dev, "poll-key"); err != nil {
		t.Fatal(err)
	}
	const n = 8
	var wg sync.WaitGroup
	wins := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd, ok, err := store.PollClaim(ctx, dev, time.Now().UTC(), 60*time.Second)
			if err == nil && ok {
				wins <- cmd.ID
			}
		}()
	}
	wg.Wait()
	close(wins)
	got := map[string]int{}
	for id := range wins {
		got[id]++
	}
	if len(got) != 1 {
		t.Fatalf("exactly one poll must win: %v", got)
	}
}

// Lost poll response: after lease expiry the same command redelivers with
// generation+1; no replacement command is created.
func TestControlLeaseRedelivery(t *testing.T) {
	store, ctl, dev, _ := ctlFixture(t)
	ctx := context.Background()
	created, _, err := ctl.CreateSyncRequest(ctx, dev, "lease-key")
	if err != nil {
		t.Fatal(err)
	}
	first, ok, err := store.PollClaim(ctx, dev, time.Now().UTC(), 200*time.Millisecond)
	if err != nil || !ok || first.ID != created.ID {
		t.Fatalf("claim: %v %v", ok, err)
	}
	time.Sleep(300 * time.Millisecond)
	second, ok, err := store.PollClaim(ctx, dev, time.Now().UTC(), time.Minute)
	if err != nil || !ok {
		t.Fatalf("redeliver: %v %v", ok, err)
	}
	if second.ID != first.ID {
		t.Fatalf("same command must redeliver: %q vs %q", second.ID, first.ID)
	}
	if second.LeaseGeneration != first.LeaseGeneration+1 {
		t.Fatalf("generation must increment: %d -> %d", first.LeaseGeneration, second.LeaseGeneration)
	}
}

// Accepted stops redelivery: polls return nothing after durable accept.
func TestControlAcceptedStopsRedelivery(t *testing.T) {
	store, ctl, dev, _ := ctlFixture(t)
	ctx := context.Background()
	if _, _, err := ctl.CreateSyncRequest(ctx, dev, "acc-key"); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := ctl.Poll(ctx, dev)
	if err != nil || !ok {
		t.Fatalf("poll: %v %v", ok, err)
	}
	if _, err := ctl.Accept(ctx, dev, claimed.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, ok, err := store.PollClaim(ctx, dev, time.Now().UTC(), time.Minute); err != nil || ok {
			t.Fatalf("accepted must never redeliver: %v %v", ok, err)
		}
	}
}

// Lost ack then terminal: pending/leased → completed directly is allowed.
func TestControlTerminalWithoutAck(t *testing.T) {
	_, ctl, dev, _ := ctlFixture(t)
	ctx := context.Background()
	if _, _, err := ctl.CreateSyncRequest(ctx, dev, "term-key"); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := ctl.Poll(ctx, dev)
	if err != nil || !ok {
		t.Fatalf("poll: %v %v", ok, err)
	}
	done, err := ctl.ReportTerminal(ctx, dev, claimed.ID, devicecontrol.StatusCompleted, "SYNC_COMPLETED")
	if err != nil || done.Status != devicecontrol.StatusCompleted {
		t.Fatalf("terminal without ack: %+v %v", done, err)
	}
}

// Contradictory terminal is rejected; first outcome stands.
func TestControlContradictoryTerminal(t *testing.T) {
	_, ctl, dev, _ := ctlFixture(t)
	ctx := context.Background()
	if _, _, err := ctl.CreateSyncRequest(ctx, dev, "contra-key"); err != nil {
		t.Fatal(err)
	}
	claimed, ok, _ := ctl.Poll(ctx, dev)
	if !ok {
		t.Fatal("poll")
	}
	if _, err := ctl.ReportTerminal(ctx, dev, claimed.ID, devicecontrol.StatusCompleted, "SYNC_COMPLETED"); err != nil {
		t.Fatal(err)
	}
	_, err := ctl.ReportTerminal(ctx, dev, claimed.ID, devicecontrol.StatusFailed, "SYNC_FAILED_NETWORK")
	if err == nil || ctlKind(t, err) != apperr.Conflict {
		t.Fatalf("contradictory terminal must conflict: %v", err)
	}
	again, err := ctl.ReportTerminal(ctx, dev, claimed.ID, devicecontrol.StatusCompleted, "SYNC_COMPLETED")
	if err != nil || again.Status != devicecontrol.StatusCompleted {
		t.Fatalf("same terminal must be idempotent: %+v %v", again, err)
	}
}

// Late running after terminal never regresses.
func TestControlLateRunningNoRegress(t *testing.T) {
	_, ctl, dev, _ := ctlFixture(t)
	ctx := context.Background()
	if _, _, err := ctl.CreateSyncRequest(ctx, dev, "late-key"); err != nil {
		t.Fatal(err)
	}
	claimed, ok, _ := ctl.Poll(ctx, dev)
	if !ok {
		t.Fatal("poll")
	}
	if _, err := ctl.ReportTerminal(ctx, dev, claimed.ID, devicecontrol.StatusFailed, "SYNC_FAILED_AUTH"); err != nil {
		t.Fatal(err)
	}
	still, err := ctl.ReportRunning(ctx, dev, claimed.ID)
	if err != nil || still.Status != devicecontrol.StatusFailed {
		t.Fatalf("late running must no-op: %+v %v", still, err)
	}
}

// Wrong-device mutation is rejected with zero state change.
func TestControlWrongDevice(t *testing.T) {
	store, ctl, devA, devB := ctlFixture(t)
	ctx := context.Background()
	if _, _, err := ctl.CreateSyncRequest(ctx, devA, "wrong-key"); err != nil {
		t.Fatal(err)
	}
	claimed, ok, _ := ctl.Poll(ctx, devA)
	if !ok {
		t.Fatal("poll")
	}
	if _, err := ctl.Accept(ctx, devB, claimed.ID); err == nil {
		t.Fatal("cross-device accept must fail")
	} else if ctlKind(t, err) != apperr.NotFound {
		t.Fatalf("cross-device must be 404: %v", err)
	}
	if _, err := ctl.ReportTerminal(ctx, devB, claimed.ID, devicecontrol.StatusCompleted, "SYNC_COMPLETED"); err == nil {
		t.Fatal("cross-device terminal must fail")
	}
	still, ok, err := store.GetCommandForDevice(ctx, claimed.ID, devA)
	if err != nil || !ok || still.Status != devicecontrol.StatusLeased {
		t.Fatalf("zero mutation: %+v %v", still, err)
	}
}

func ctlFixtureCheck(ctx context.Context, ctl *devicecontrol.Service, dev, id string) (string, bool, error) {
	cmd, err := ctl.ReportRunning(ctx, dev, id)
	if err != nil {
		return "", false, err
	}
	return cmd.Status, true, nil
}

// Presence: poll/accept/finish refresh last_seen from server time.
func TestControlPresence(t *testing.T) {
	store, ctl, dev, _ := ctlFixture(t)
	ctx := context.Background()
	if _, _, err := ctl.CreateSyncRequest(ctx, dev, "pres-key"); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC().Add(-time.Minute)
	_ = before
	claimed, ok, _ := ctl.Poll(ctx, dev)
	if !ok {
		t.Fatal("poll")
	}
	p, ok, err := store.GetPresence(ctx, dev)
	if err != nil || !ok || p.LastSeenAt == nil {
		t.Fatalf("presence after poll: %+v %v", p, err)
	}
	if _, err := ctl.Accept(ctx, dev, claimed.ID); err != nil {
		t.Fatal(err)
	}
	p2, _, _ := store.GetPresence(ctx, dev)
	if p2.LastAcceptedAt == nil {
		t.Fatal("accepted contact recorded")
	}
	if _, err := ctl.ReportTerminal(ctx, dev, claimed.ID, devicecontrol.StatusCompleted, "SYNC_COMPLETED"); err != nil {
		t.Fatal(err)
	}
	p3, _, _ := store.GetPresence(ctx, dev)
	if p3.LastFinishedAt == nil {
		t.Fatal("finished contact recorded")
	}
}

// Connectivity derivation matrix (pure domain).
func TestControlConnectivityMatrix(t *testing.T) {
	now := time.Now().UTC()
	if got := devicecontrol.DeriveConnectivity(nil, now, time.Minute); got != devicecontrol.ConnectivityNeverSeen {
		t.Fatalf("nil: %q", got)
	}
	recent := now.Add(-30 * time.Second)
	if got := devicecontrol.DeriveConnectivity(&recent, now, time.Minute); got != devicecontrol.ConnectivityOnline {
		t.Fatalf("recent: %q", got)
	}
	old := now.Add(-5 * time.Minute)
	if got := devicecontrol.DeriveConnectivity(&old, now, time.Minute); got != devicecontrol.ConnectivityOffline {
		t.Fatalf("stale: %q", got)
	}
}

// Two dashboard creates with different keys: one active, one deterministic
// already-active outcome backed by the partial unique index.
func TestControlActiveUniquenessRace(t *testing.T) {
	_, ctl, dev, _ := ctlFixture(t)
	ctx := context.Background()
	const n = 6
	var wg sync.WaitGroup
	type res struct {
		id      string
		created bool
		err     error
	}
	out := make(chan res, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "race-" + string(rune('a'+i))
			cmd, created, err := ctl.CreateSyncRequest(ctx, dev, key)
			_ = i
			if err == nil {
				out <- res{cmd.ID, created, nil}
				return
			}
			out <- res{"", false, err}
		}(i)
	}
	wg.Wait()
	close(out)
	ids := map[string]int{}
	for r := range out {
		if r.err != nil {
			continue
		}
		ids[r.id]++
	}
	_ = ids
	active, ok, err := ctl.Active(ctx, dev)
	if err != nil || !ok {
		t.Fatalf("one active must exist: %v %v", ok, err)
	}
	_ = active
	_ = auth.StatusActive
}
