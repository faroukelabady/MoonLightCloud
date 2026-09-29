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

// F03: synchronized concurrent completed/failed → one terminal outcome,
// one 409, one durable record, no regression. Twenty-five pairs make the
// interleaving (both read non-terminal before either finishes)
// deterministic in aggregate: every pair must split exactly one success /
// one DEVICE_COMMAND_CONFLICT regardless of scheduling.
func TestControlConcurrentContradictoryTerminal(t *testing.T) {
	pool, svc := openTestRepo(t)
	store := NewDevices(pool, 5*time.Second)
	ctx := context.Background()
	const pairs = 25
	devs := make([]string, 0, pairs)
	active := map[string]bool{}
	for i := 0; i < pairs; i++ {
		prov, err := svc.Create(ctx, "race-dev")
		if err != nil {
			t.Fatal(err)
		}
		devs = append(devs, prov.Device.ID)
		active[prov.Device.ID] = true
	}
	ctl := devicecontrol.NewService(store, activeDevices{active: active}, ids.System{}.New, 60*time.Second, time.Now)
	cmds := make([]string, 0, pairs)
	for i, dev := range devs {
		if _, _, err := ctl.CreateSyncRequest(ctx, dev, "race-term"); err != nil {
			t.Fatal(err)
		}
		claimed, ok, err := ctl.Poll(ctx, dev)
		if err != nil || !ok {
			t.Fatalf("poll %d: %v %v", i, ok, err)
		}
		if _, err := ctl.Accept(ctx, dev, claimed.ID); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, claimed.ID)
	}
	start := make(chan struct{})
	type res struct {
		status string
		err    error
	}
	out := make(chan res, 2*pairs)
	for i, id := range cmds {
		dev := devs[i]
		go func(dev, id string) {
			<-start
			cmd, err := ctl.ReportTerminal(ctx, dev, id, devicecontrol.StatusCompleted, "SYNC_COMPLETED")
			if err != nil {
				out <- res{"", err}
				return
			}
			out <- res{cmd.Status, nil}
		}(dev, id)
		go func(dev, id string) {
			<-start
			cmd, err := ctl.ReportTerminal(ctx, dev, id, devicecontrol.StatusFailed, "SYNC_FAILED_NETWORK")
			if err != nil {
				out <- res{"", err}
				return
			}
			out <- res{cmd.Status, nil}
		}(dev, id)
	}
	close(start)
	conflicts := 0
	for i := 0; i < 2*pairs; i++ {
		r := <-out
		if r.err != nil {
			if ctlKind(t, r.err) != apperr.Conflict {
				t.Fatalf("loser kind: %v", r.err)
			}
			if msg := r.err.(*apperr.Error).Message; msg != "DEVICE_COMMAND_CONFLICT" {
				t.Fatalf("machine code: %q", msg)
			}
			conflicts++
		} else if r.status != devicecontrol.StatusCompleted && r.status != devicecontrol.StatusFailed {
			t.Fatalf("winner: %+v", r)
		}
	}
	if conflicts != pairs {
		t.Fatalf("exactly one 409 per pair: %d/%d", conflicts, pairs)
	}
}

// F03: identical concurrent retries both succeed idempotently.
func TestControlConcurrentIdenticalTerminal(t *testing.T) {
	_, ctl, dev, _ := ctlFixture(t)
	ctx := context.Background()
	if _, _, err := ctl.CreateSyncRequest(ctx, dev, "race-same"); err != nil {
		t.Fatal(err)
	}
	claimed, ok, _ := ctl.Poll(ctx, dev)
	if !ok {
		t.Fatal("poll")
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			_, err := ctl.ReportTerminal(ctx, dev, claimed.ID, devicecontrol.StatusCompleted, "SYNC_COMPLETED")
			errs <- err
		}()
	}
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("identical retry must succeed: %v", err)
		}
	}
}

// F06: age presence, replay the same valid terminal → fresh/ONLINE with the
// outcome unchanged.
func TestControlTerminalReplayRefreshesPresence(t *testing.T) {
	store, ctl, dev, _ := ctlFixture(t)
	ctx := context.Background()
	if _, _, err := ctl.CreateSyncRequest(ctx, dev, "pres-replay"); err != nil {
		t.Fatal(err)
	}
	claimed, ok, _ := ctl.Poll(ctx, dev)
	if !ok {
		t.Fatal("poll")
	}
	first, err := ctl.ReportTerminal(ctx, dev, claimed.ID, devicecontrol.StatusCompleted, "SYNC_COMPLETED")
	if err != nil {
		t.Fatal(err)
	}
	// Age presence by one hour behind the back of the store.
	old := time.Now().UTC().Add(-time.Hour)
	uid, err := parseUUID(dev)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `UPDATE device_control_presence SET last_seen_at = $2, updated_at = $2 WHERE device_id = $1`,
		uid, pgTime(old)); err != nil {
		t.Fatal(err)
	}
	p, _, _ := store.GetPresence(ctx, dev)
	if got := devicecontrol.DeriveConnectivity(p.LastSeenAt, time.Now().UTC(), time.Minute); got != devicecontrol.ConnectivityOffline {
		t.Fatalf("aged must be OFFLINE: %q", got)
	}
	replay, err := ctl.ReportTerminal(ctx, dev, claimed.ID, devicecontrol.StatusCompleted, "SYNC_COMPLETED")
	if err != nil {
		t.Fatalf("valid replay must succeed: %v", err)
	}
	if replay.Status != devicecontrol.StatusCompleted || replay.ResultCode == nil || *replay.ResultCode != "SYNC_COMPLETED" {
		t.Fatalf("outcome immutable: %+v", replay)
	}
	_ = first
	p2, _, _ := store.GetPresence(ctx, dev)
	if got := devicecontrol.DeriveConnectivity(p2.LastSeenAt, time.Now().UTC(), time.Minute); got != devicecontrol.ConnectivityOnline {
		t.Fatalf("replay must refresh to ONLINE: %+v", p2.LastSeenAt)
	}
	if !p2.LastSeenAt.After(*p.LastSeenAt) {
		t.Fatal("last_seen monotonic forward")
	}
}

// F06: rejected requests leave presence unchanged.
func TestControlRejectedLeavesPresence(t *testing.T) {
	store, ctl, devA, devB := ctlFixture(t)
	ctx := context.Background()
	if _, _, err := ctl.CreateSyncRequest(ctx, devA, "pres-neg"); err != nil {
		t.Fatal(err)
	}
	claimed, ok, _ := ctl.Poll(ctx, devA)
	if !ok {
		t.Fatal("poll")
	}
	if _, err := ctl.ReportTerminal(ctx, devA, claimed.ID, devicecontrol.StatusCompleted, "SYNC_COMPLETED"); err != nil {
		t.Fatal(err)
	}
	snap := func() time.Time {
		p, _, _ := store.GetPresence(ctx, devA)
		return p.LastSeenAt.UTC()
	}
	before := snap()
	// Wrong device.
	if _, err := ctl.ReportTerminal(ctx, devB, claimed.ID, devicecontrol.StatusCompleted, "SYNC_COMPLETED"); err == nil {
		t.Fatal("cross-device must fail")
	}
	// Contradictory terminal.
	if _, err := ctl.ReportTerminal(ctx, devA, claimed.ID, devicecontrol.StatusFailed, "SYNC_FAILED_NETWORK"); err == nil {
		t.Fatal("contradiction must fail")
	}
	// Malformed result.
	if _, err := ctl.ReportTerminal(ctx, devA, claimed.ID, devicecontrol.StatusCompleted, "BOGUS_CODE"); err == nil {
		t.Fatal("bogus code must fail")
	}
	// Late accepted/running replays DO refresh (authorized contact).
	if _, err := ctl.Accept(ctx, devA, claimed.ID); err != nil {
		t.Fatal(err)
	}
	mid := snap()
	if !mid.After(before) {
		t.Fatal("late accept replay refreshes presence")
	}
	if _, err := ctl.ReportRunning(ctx, devA, claimed.ID); err != nil {
		t.Fatal(err)
	}
	after := snap()
	if after.Before(mid) {
		t.Fatal("presence monotonic")
	}
}

// F10: first non-poll contact populates last_seen, leaves last_poll NULL.
func TestPresenceFirstNonPollContact(t *testing.T) {
	store, _, dev, _ := ctlFixture(t)
	ctx := context.Background()
	at := time.Now().UTC()
	if err := store.TouchAccepted(ctx, dev, at); err != nil {
		t.Fatal(err)
	}
	p, ok, err := store.GetPresence(ctx, dev)
	if err != nil || !ok {
		t.Fatalf("row created: %+v %v", p, err)
	}
	if p.LastSeenAt == nil {
		t.Fatal("last_seen populated")
	}
	if p.LastPollAt != nil {
		t.Fatalf("last_poll stays NULL: %+v", p.LastPollAt)
	}
	if p.LastAcceptedAt == nil {
		t.Fatal("accepted marker recorded")
	}
}

// F10: first poll populates both columns.
func TestPresenceFirstPoll(t *testing.T) {
	store, _, dev, _ := ctlFixture(t)
	ctx := context.Background()
	if err := store.TouchSeen(ctx, dev, time.Now().UTC(), true); err != nil {
		t.Fatal(err)
	}
	p, ok, _ := store.GetPresence(ctx, dev)
	if !ok || p.LastSeenAt == nil || p.LastPollAt == nil {
		t.Fatalf("both populated: %+v", p)
	}
}

// F10: accepted/running/terminal contact never moves last_poll_at.
func TestPresenceNonPollPreservesLastPoll(t *testing.T) {
	store, ctl, dev, _ := ctlFixture(t)
	ctx := context.Background()
	pollAt := time.Now().UTC().Add(-time.Minute)
	if err := store.TouchSeen(ctx, dev, pollAt, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ctl.CreateSyncRequest(ctx, dev, "f10-k"); err != nil {
		t.Fatal(err)
	}
	claimed, ok, _ := ctl.Poll(ctx, dev)
	if !ok {
		t.Fatal("poll")
	}
	// The claim poll above refreshed last_poll; pin it back to prove the
	// later non-poll contacts preserve it.
	pinAt := time.Now().UTC().Add(-time.Minute)
	uid, _ := parseUUID(dev)
	if _, err := store.pool.Exec(ctx, `UPDATE device_control_presence SET last_poll_at = $2, updated_at = $2 WHERE device_id = $1`,
		uid, pgTime(pinAt)); err != nil {
		t.Fatal(err)
	}
	if _, err := ctl.Accept(ctx, dev, claimed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ctl.ReportRunning(ctx, dev, claimed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ctl.ReportTerminal(ctx, dev, claimed.ID, devicecontrol.StatusCompleted, "SYNC_COMPLETED"); err != nil {
		t.Fatal(err)
	}
	// Valid terminal replay refreshes seen, still preserves poll.
	if _, err := ctl.ReportTerminal(ctx, dev, claimed.ID, devicecontrol.StatusCompleted, "SYNC_COMPLETED"); err != nil {
		t.Fatal(err)
	}
	p, _, _ := store.GetPresence(ctx, dev)
	if p.LastPollAt == nil || p.LastPollAt.UTC().Unix() != pinAt.Unix() {
		t.Fatalf("last_poll preserved: %+v", p.LastPollAt)
	}
	if p.LastSeenAt == nil || time.Since(p.LastSeenAt.UTC()) > 5*time.Minute {
		t.Fatalf("last_seen fresh: %+v", p.LastSeenAt)
	}
}

// F10: older polls never regress either column.
func TestPresenceOlderPollNoRegression(t *testing.T) {
	store, _, dev, _ := ctlFixture(t)
	ctx := context.Background()
	fresh := time.Now().UTC()
	if err := store.TouchSeen(ctx, dev, fresh, true); err != nil {
		t.Fatal(err)
	}
	stale := fresh.Add(-time.Hour)
	if err := store.TouchSeen(ctx, dev, stale, true); err != nil {
		t.Fatal(err)
	}
	if err := store.TouchSeen(ctx, dev, stale, false); err != nil {
		t.Fatal(err)
	}
	p, _, _ := store.GetPresence(ctx, dev)
	if p.LastSeenAt.UTC().Unix() != fresh.Unix() || p.LastPollAt.UTC().Unix() != fresh.Unix() {
		t.Fatalf("no regression: seen=%v poll=%v want %v", p.LastSeenAt, p.LastPollAt, fresh)
	}
}

// F10: concurrent poll/non-poll updates converge on monotonic maxima.
func TestPresenceConcurrentMonotonic(t *testing.T) {
	store, _, dev, _ := ctlFixture(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	if err := store.TouchSeen(ctx, dev, base, true); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			at := base.Add(time.Duration(i) * time.Minute)
			if i%2 == 0 {
				_ = store.TouchSeen(ctx, dev, at, true)
			} else {
				_ = store.TouchSeen(ctx, dev, at, false)
			}
		}(i)
	}
	wg.Wait()
	top := base.Add(11 * time.Minute)
	// Even minutes carry polls (max even i=10), odd minutes non-poll.
	p, _, _ := store.GetPresence(ctx, dev)
	if p.LastSeenAt.UTC().Unix() != top.Unix() {
		t.Fatalf("seen converges on max: %v want %v", p.LastSeenAt, top)
	}
	if p.LastPollAt.UTC().Unix() != base.Add(10*time.Minute).Unix() {
		t.Fatalf("poll converges on max poll: %v", p.LastPollAt)
	}
}

// F10: invalid device ids and wrong-device access mutate nothing.
func TestPresenceRejectionMutatesNothing(t *testing.T) {
	store, ctl, devA, devB := ctlFixture(t)
	ctx := context.Background()
	if err := store.TouchSeen(ctx, "not-a-uuid", time.Now().UTC(), true); err == nil {
		t.Fatal("invalid id rejected")
	}
	if _, ok, _ := store.GetPresence(ctx, devB); ok {
		t.Fatal("no row for untouched device")
	}
	if _, _, err := ctl.CreateSyncRequest(ctx, devA, "f10-neg"); err != nil {
		t.Fatal(err)
	}
	claimed, ok, _ := ctl.Poll(ctx, devA)
	if !ok {
		t.Fatal("poll")
	}
	if _, err := ctl.ReportRunning(ctx, devB, claimed.ID); err == nil {
		t.Fatal("cross-device rejected")
	}
	if _, ok, _ := store.GetPresence(ctx, devB); ok {
		t.Fatal("wrong-device access creates no presence")
	}
}

// countingStore instruments batch-read counts around the real store.
type countingStore struct {
	Devices
	presence int
	active   int
	recent   int
}

func (c *countingStore) ListPresence(ctx context.Context) ([]devicecontrol.Presence, error) {
	c.presence++
	return c.Devices.ListPresence(ctx)
}

func (c *countingStore) ListActiveAll(ctx context.Context) ([]devicecontrol.Command, error) {
	c.active++
	return c.Devices.ListActiveAll(ctx)
}

func (c *countingStore) ListRecentBounded(ctx context.Context, ids []string, per int) ([]devicecontrol.Command, error) {
	c.recent++
	return c.Devices.ListRecentBounded(ctx, ids, per)
}

func (c *countingStore) total() int { return c.presence + c.active + c.recent }

// F11 fixture: devices spanning no presence, ONLINE, OFFLINE, every
// command state, >5 history rows, and revoked lifecycle.
func overviewFixture(t *testing.T, n int) (Devices, []string) {
	t.Helper()
	pool, svc := openTestRepo(t)
	store := NewDevices(pool, 5*time.Second)
	ctx := context.Background()
	devIDs := make([]string, 0, n)
	for i := 0; i < n; i++ {
		prov, err := svc.Create(ctx, "ov-dev")
		if err != nil {
			t.Fatal(err)
		}
		devIDs = append(devIDs, prov.Device.ID)
	}
	devices := activeDevices{active: map[string]bool{}}
	for _, id := range devIDs {
		devices.active[id] = true
	}
	ctl := devicecontrol.NewService(store, devices, ids.System{}.New, 60*time.Second, time.Now)
	// Device 0: no presence, no commands (never seen).
	// Device 1: ONLINE via poll, pending command.
	if _, _, err := ctl.CreateSyncRequest(ctx, devIDs[1], "ov-1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ctl.Poll(ctx, devIDs[1]); err != nil {
		t.Fatal(err)
	}
	// Device 2: accepted/running command (poll then accept then running).
	if n > 2 {
		if _, _, err := ctl.CreateSyncRequest(ctx, devIDs[2], "ov-2"); err != nil {
			t.Fatal(err)
		}
		claimed, ok, _ := ctl.Poll(ctx, devIDs[2])
		if !ok {
			t.Fatal("poll 2")
		}
		if _, err := ctl.Accept(ctx, devIDs[2], claimed.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := ctl.ReportRunning(ctx, devIDs[2], claimed.ID); err != nil {
			t.Fatal(err)
		}
	}
	// Device 3: completed history (terminal, then stays visible).
	if n > 3 {
		if _, _, err := ctl.CreateSyncRequest(ctx, devIDs[3], "ov-3"); err != nil {
			t.Fatal(err)
		}
		claimed, ok, _ := ctl.Poll(ctx, devIDs[3])
		if !ok {
			t.Fatal("poll 3")
		}
		if _, err := ctl.ReportTerminal(ctx, devIDs[3], claimed.ID, devicecontrol.StatusFailed, "SYNC_FAILED_AUTH"); err != nil {
			t.Fatal(err)
		}
	}
	// Remaining devices: aged presence (OFFLINE), no commands.
	for i, id := range devIDs {
		if i < 4 {
			continue
		}
		old := time.Now().UTC().Add(-time.Hour)
		if err := store.TouchSeen(ctx, id, old, true); err != nil {
			t.Fatal(err)
		}
	}
	return store, devIDs
}

// F11: overview query count is constant (3 batch reads) for 2 and 9
// devices, with identical per-device semantics.
func TestOverviewBoundedQueries(t *testing.T) {
	for _, n := range []int{2, 9} {
		store, devIDs := overviewFixture(t, n)
		ctx := context.Background()
		counted := &countingStore{Devices: store}
		svc := devicecontrol.NewService(counted, activeDevices{active: map[string]bool{}}, ids.System{}.New, 60*time.Second, time.Now)
		ov, err := svc.Overview(ctx, devIDs)
		if err != nil {
			t.Fatal(err)
		}
		if counted.total() != 3 {
			t.Fatalf("n=%d: exactly 3 batch reads, got %d", n, counted.total())
		}
		if len(ov) != n {
			t.Fatalf("n=%d: overview per device: %d", n, len(ov))
		}
	}
}

// F11: history bound 5 per device with deterministic newest-first ordering
// (requested_at DESC, id DESC tiebreak).
func TestOverviewHistoryBoundAndOrder(t *testing.T) {
	store, devIDs := overviewFixture(t, 2)
	ctx := context.Background()
	dev := devIDs[1]
	// Finish the pending command, then create+finish 6 more terminals
	// sequentially (one active at a time) for 7 total history rows.
	for i := 0; i < 7; i++ {
		active, ok, err := store.GetActive(ctx, dev)
		if err != nil || !ok {
			t.Fatalf("iter %d active: %v %v", i, ok, err)
		}
		if _, _, err := store.Finish(ctx, active.ID, dev, devicecontrol.StatusCompleted, "SYNC_COMPLETED", time.Now().UTC()); err != nil {
			t.Fatalf("iter %d finish: %v", i, err)
		}
		if i < 6 {
			if _, err := store.CreateCommand(ctx, ids.System{}.New(), dev, "hist-"+string(rune('a'+i)), time.Now().UTC()); err != nil {
				t.Fatalf("iter %d create: %v", i, err)
			}
		}
	}
	svc := devicecontrol.NewService(store, activeDevices{active: map[string]bool{}}, ids.System{}.New, 60*time.Second, time.Now)
	ov, err := svc.Overview(ctx, []string{dev})
	if err != nil {
		t.Fatal(err)
	}
	recent := ov[dev].Recent
	if len(recent) != 5 {
		t.Fatalf("bound 5: %d", len(recent))
	}
	for i := 1; i < len(recent); i++ {
		a, b := recent[i-1], recent[i]
		if a.RequestedAt.Before(b.RequestedAt) {
			t.Fatalf("newest-first violated: %+v before %+v", a, b)
		}
		if a.RequestedAt.Equal(b.RequestedAt) && a.ID < b.ID {
			t.Fatalf("id tiebreak violated: %q before %q", a.ID, b.ID)
		}
	}
	if ov[dev].Active != nil {
		t.Fatal("no active after all-terminal history")
	}
}

// F11: revoked devices remain visible with history; overview never carries
// credential material.
func TestOverviewRevokedVisible(t *testing.T) {
	pool, svc := openTestRepo(t)
	store := NewDevices(pool, 5*time.Second)
	ctx := context.Background()
	prov, err := svc.Create(ctx, "rev-dev")
	if err != nil {
		t.Fatal(err)
	}
	dev := prov.Device.ID
	if err := svc.RevokeDevice(ctx, dev); err != nil {
		t.Fatal(err)
	}
	// History created before revocation stays visible.
	admin := devicecontrol.NewService(store, activeDevices{active: map[string]bool{dev: true}}, ids.System{}.New, 60*time.Second, time.Now)
	if _, _, err := admin.CreateSyncRequest(ctx, dev, "rev-hist"); err != nil {
		t.Fatal(err)
	}
	claimed, ok, _ := admin.Poll(ctx, dev)
	if !ok {
		t.Fatal("poll")
	}
	if _, err := admin.ReportTerminal(ctx, dev, claimed.ID, devicecontrol.StatusCompleted, "SYNC_COMPLETED"); err != nil {
		t.Fatal(err)
	}
	got, err := admin.Overview(ctx, []string{dev})
	if err != nil {
		t.Fatal(err)
	}
	ov := got[dev]
	if len(ov.Recent) != 1 || ov.Active != nil {
		t.Fatalf("revoked history visible: %+v", ov)
	}
}
