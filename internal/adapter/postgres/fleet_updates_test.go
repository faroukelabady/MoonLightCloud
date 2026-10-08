package postgres

// Phase 18 fleet update control against real PostgreSQL (ADR-0052):
// release immutability, Store isolation, target snapshots, lifecycle,
// truthful outcomes, app-clock retry under skew, and concurrency races.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/fleetupdate"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	"github.com/faroukelabady/MoonLightCloud/internal/release"
	"github.com/faroukelabady/MoonLightCloud/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fleetFixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	authSvc auth.Service
	devices Devices
	svc     *fleetupdate.Service
	priv    ed25519.PrivateKey
	clock   atomic.Pointer[time.Time]
	storeA  string
	storeB  string
	devA    string
	devA2   string
	devB    string
}

func (f *fleetFixture) now() time.Time  { return *f.clock.Load() }
func (f *fleetFixture) set(t time.Time) { f.clock.Store(&t) }

func newFleetFixture(t *testing.T) *fleetFixture {
	t.Helper()
	pool, authSvc := openTestRepo(t)
	f := &fleetFixture{t: t, pool: pool, authSvc: authSvc, devices: NewDevices(pool, 5*time.Second),
		storeA: uuid.NewString(), storeB: uuid.NewString()}
	f.set(time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC))
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.priv = priv
	trust, err := release.NewTrustSet(pub)
	if err != nil {
		t.Fatal(err)
	}
	f.svc = fleetupdate.NewService(NewFleetUpdates(pool, 10*time.Second), trust, nil, f.now, ids.System{}.New, fleetupdate.Config{})
	f.devA = f.device("fleet-a", f.storeA)
	f.devA2 = f.device("fleet-a2", f.storeA)
	f.devB = f.device("fleet-b", f.storeB)
	return f
}

func (f *fleetFixture) device(name, storeID string) string {
	f.t.Helper()
	p, err := f.authSvc.Create(context.Background(), name)
	if err != nil {
		f.t.Fatal(err)
	}
	_, err = f.devices.RegisterStore(context.Background(), p.Device.ID, store.RegistrationRequest{
		StoreID: storeID, DisplayName: "Store " + storeID[:4], Timezone: "Africa/Cairo"})
	if err != nil {
		// Additional devices of an existing Store need operator enrollment
		// (Phase 9); fixtures bind them directly.
		if _, ierr := f.pool.Exec(context.Background(), `INSERT INTO device_store_bindings (device_id, store_id) VALUES ($1, $2)`, p.Device.ID, storeID); ierr != nil {
			f.t.Fatalf("bind %s: %v / %v", name, err, ierr)
		}
	}
	return p.Device.ID
}

func (f *fleetFixture) capable(deviceID string, seq int64) {
	f.t.Helper()
	if err := f.svc.RecordStatus(context.Background(), deviceID, fleetupdate.StatusReport{Version: "1.0.0", BuildCommit: strings.Repeat("a", 40),
		ReleaseSequence: seq, OS: "linux", Arch: "amd64", UpdaterProtocol: 1, UpdaterCapable: true, UpdateState: "IDLE"}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fleetFixture) signed(seq uint64, arches ...string) []byte {
	f.t.Helper()
	if len(arches) == 0 {
		arches = []string{"amd64"}
	}
	m := release.Manifest{ReleaseSequence: seq, Version: "1.1.0", BuildCommit: strings.Repeat("b", 40)}
	for _, a := range arches {
		m.Artifacts = append(m.Artifacts, release.Artifact{OS: "linux", Arch: a, Package: "tar.gz", FileName: "r-" + a + ".tar.gz", Size: 100, SHA256: strings.Repeat("c", 64)})
	}
	env, _, err := release.Sign(m, f.priv)
	if err != nil {
		f.t.Fatal(err)
	}
	return env
}

func urls(arches ...string) map[string]string {
	if len(arches) == 0 {
		arches = []string{"amd64"}
	}
	out := map[string]string{}
	for _, a := range arches {
		out["r-"+a+".tar.gz"] = "https://artifacts.example.test/r-" + a + ".tar.gz"
	}
	return out
}

func (f *fleetFixture) release(seq uint64, arches ...string) fleetupdate.ReleaseView {
	f.t.Helper()
	view, created, err := f.svc.ImportRelease(context.Background(), "op", f.signed(seq, arches...), urls(arches...))
	if err != nil || !created {
		f.t.Fatalf("import: %v created=%v", err, created)
	}
	return view
}

func (f *fleetFixture) rollout(req fleetupdate.RolloutRequest, start bool) fleetupdate.RolloutView {
	f.t.Helper()
	view, err := f.svc.CreateRollout(context.Background(), "op", req)
	if err != nil {
		f.t.Fatal(err)
	}
	if start {
		if view, err = f.svc.StartRollout(context.Background(), "op", view.ID); err != nil {
			f.t.Fatal(err)
		}
	}
	return view
}

func (f *fleetFixture) poll(deviceID string) *fleetupdate.Command {
	f.t.Helper()
	cmd, err := f.svc.PollCommand(context.Background(), deviceID)
	if err != nil {
		f.t.Fatal(err)
	}
	return cmd
}

func (f *fleetFixture) report(deviceID, targetID, state, code string, retryable bool) (fleetupdate.Ack, error) {
	return f.svc.ReportTarget(context.Background(), deviceID, targetID, fleetupdate.TargetReport{State: state, ErrorCode: code, Retryable: retryable})
}

func kind(err error) apperr.Kind {
	var app *apperr.Error
	if errors.As(err, &app) {
		return app.Kind
	}
	return 0
}

func TestReleaseImportIsVerifiedIdempotentAndImmutable(t *testing.T) {
	f := newFleetFixture(t)
	ctx := context.Background()
	env := f.signed(5)
	view, created, err := f.svc.ImportRelease(ctx, "op", env, urls())
	if err != nil || !created || view.ReleaseSequence != 5 || view.Status != "ACTIVE" {
		t.Fatalf("%+v %v %v", view, created, err)
	}
	again, created, err := f.svc.ImportRelease(ctx, "op", env, urls())
	if err != nil || created || again.ID != view.ID {
		t.Fatalf("re-import must be idempotent: %+v %v %v", again, created, err)
	}
	// Same sequence, different signed identity: refused (prompt §132).
	other := release.Manifest{ReleaseSequence: 5, Version: "9.9.9", BuildCommit: strings.Repeat("d", 40),
		Artifacts: []release.Artifact{{OS: "linux", Arch: "amd64", Package: "tar.gz", FileName: "r-amd64.tar.gz", Size: 1, SHA256: strings.Repeat("e", 64)}}}
	otherEnv, _, _ := release.Sign(other, f.priv)
	if _, _, err := f.svc.ImportRelease(ctx, "op", otherEnv, urls()); kind(err) != apperr.Conflict {
		t.Fatalf("conflicting sequence accepted: %v", err)
	}
	// Unsigned / tampered / insecure-location / incomplete-location imports.
	tampered := []byte(strings.Replace(string(f.signed(6)), `"signature":"`, `"signature":"A`, 1))
	_, foreignPriv, _ := ed25519.GenerateKey(rand.Reader)
	foreignEnv, _, _ := release.Sign(release.Manifest{ReleaseSequence: 7, Version: "1.0.0", BuildCommit: strings.Repeat("f", 40),
		Artifacts: []release.Artifact{{OS: "linux", Arch: "amd64", Package: "tar.gz", FileName: "r-amd64.tar.gz", Size: 1, SHA256: strings.Repeat("e", 64)}}}, foreignPriv)
	for name, tc := range map[string]struct {
		env  []byte
		urls map[string]string
	}{
		"tampered signature": {tampered, urls()},
		"untrusted key":      {foreignEnv, urls()},
		"http location":      {f.signed(8), map[string]string{"r-amd64.tar.gz": "http://artifacts.example.test/x"}},
		"missing location":   {f.signed(9), map[string]string{}},
		"extra location":     {f.signed(10), map[string]string{"r-amd64.tar.gz": "https://a.test/x", "evil.sh": "https://a.test/y"}},
		"not json":           {[]byte("sh -c id"), urls()},
	} {
		if _, _, err := f.svc.ImportRelease(ctx, "op", tc.env, tc.urls); kind(err) != apperr.InvalidInput {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// Storage-level immutability: only lifecycle status may change.
	for _, stmt := range []string{
		`UPDATE releases SET version = 'evil' WHERE id = $1`,
		`UPDATE releases SET envelope = '{}' WHERE id = $1`,
		`DELETE FROM releases WHERE id = $1`,
		`UPDATE release_artifacts SET sha256 = repeat('0', 64) WHERE release_id = $1`,
		`DELETE FROM release_artifacts WHERE release_id = $1`,
	} {
		if _, err := f.pool.Exec(ctx, stmt, view.ID); err == nil {
			t.Fatalf("immutable release changed by %q", stmt)
		}
	}
	revoked, err := f.svc.SetReleaseStatus(ctx, "op", view.ID, "REVOKED")
	if err != nil || revoked.Status != "REVOKED" {
		t.Fatalf("%+v %v", revoked, err)
	}
	if _, err := f.svc.CreateRollout(ctx, "op", fleetupdate.RolloutRequest{ReleaseID: view.ID, Scope: "ALL", Mode: "OPTIONAL", Percentage: 100}); kind(err) != apperr.Conflict {
		t.Fatalf("rollout of a revoked release accepted: %v", err)
	}
	events, err := f.svc.Audit(ctx, "", 0, 50)
	if err != nil || len(events) < 2 || events[0].Action != "release.revoked" {
		t.Fatalf("audit: %+v %v", events, err)
	}
}

func TestStoreScopedRolloutIsolationAndLifecycle(t *testing.T) {
	f := newFleetFixture(t)
	ctx := context.Background()
	for _, d := range []string{f.devA, f.devA2, f.devB} {
		f.capable(d, 10)
	}
	rel := f.release(11)
	ro := f.rollout(fleetupdate.RolloutRequest{ReleaseID: rel.ID, Scope: "STORE", StoreID: f.storeA, Mode: "MANDATORY", Percentage: 100}, false)
	if ro.TargetCount != 2 || ro.Status != "DRAFT" {
		t.Fatalf("snapshot: %+v", ro)
	}
	if f.poll(f.devA) != nil {
		t.Fatal("DRAFT rollouts deliver nothing")
	}
	if _, err := f.svc.StartRollout(ctx, "op", ro.ID); err != nil {
		t.Fatal(err)
	}
	if f.poll(f.devB) != nil {
		t.Fatal("Store B device received a Store A rollout")
	}
	cmd := f.poll(f.devA)
	if cmd == nil || cmd.Type != fleetupdate.CommandType || cmd.Mode != "MANDATORY" || cmd.ManifestDigest != rel.ManifestDigest || cmd.ReleaseStatus != "ACTIVE" {
		t.Fatalf("command: %+v", cmd)
	}
	if !strings.HasPrefix(cmd.ArtifactURL, "https://") || cmd.Envelope == "" {
		t.Fatalf("command artifact/envelope: %+v", cmd)
	}
	// Store B's device can neither report on nor learn about A's target.
	if _, err := f.report(f.devB, cmd.TargetID, "DOWNLOADING", "", false); kind(err) != apperr.NotFound {
		t.Fatalf("foreign device report accepted: %v", err)
	}
	if _, err := f.report(f.devA2, cmd.TargetID, "DOWNLOADING", "", false); kind(err) != apperr.NotFound {
		t.Fatalf("same-Store other device report accepted: %v", err)
	}
	targets, _, err := f.svc.ListTargets(ctx, ro.ID, f.storeB, "", 50)
	if err != nil || len(targets) != 0 {
		t.Fatalf("Store B filter leaked A targets: %+v %v", targets, err)
	}
	fleetB, _, err := f.svc.Fleet(ctx, f.storeB, "", 50)
	if err != nil || len(fleetB) != 1 || fleetB[0].DeviceID != f.devB || fleetB[0].TargetID != "" {
		t.Fatalf("Store B fleet view: %+v %v", fleetB, err)
	}
	// Cursors are bound to their Store scope.
	_, nextA, err := f.svc.Fleet(ctx, f.storeA, "", 1)
	if err != nil || nextA == "" {
		t.Fatal(err)
	}
	if _, _, err := f.svc.Fleet(ctx, f.storeB, nextA, 1); kind(err) != apperr.InvalidInput {
		t.Fatalf("cross-Store cursor reuse accepted: %v", err)
	}

	// Pause stops new deliveries but never the device already working.
	if _, err := f.svc.PauseRollout(ctx, "op", ro.ID); err != nil {
		t.Fatal(err)
	}
	if f.poll(f.devA2) != nil {
		t.Fatal("paused rollout delivered a new target")
	}
	for _, s := range []string{"DOWNLOADING", "STAGED", "WAITING_SAFE_BOUNDARY", "INSTALLING"} {
		if _, err := f.report(f.devA, cmd.TargetID, s, "", false); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if _, err := f.svc.ResumeRollout(ctx, "op", ro.ID); err != nil {
		t.Fatal(err)
	}
	cmd2 := f.poll(f.devA2)
	if cmd2 == nil {
		t.Fatal("resume must deliver again")
	}
	// Success is reported only by the device, then the rollout completes
	// once every target is terminal.
	ack, err := f.report(f.devA, cmd.TargetID, "SUCCEEDED", "", false)
	if err != nil || ack.TargetState != "SUCCEEDED" {
		t.Fatalf("%+v %v", ack, err)
	}
	got, _ := f.svc.GetRollout(ctx, ro.ID)
	if got.Status != "ACTIVE" {
		t.Fatalf("rollout completed while a target was open: %+v", got)
	}
	if _, err := f.report(f.devA2, cmd2.TargetID, "ROLLED_BACK", "UPDATE_HEALTH_FAILED", false); err != nil {
		t.Fatal(err)
	}
	got, _ = f.svc.GetRollout(ctx, ro.ID)
	if got.Status != "COMPLETED" || got.Counts["SUCCEEDED"] != 1 || got.Counts["ROLLED_BACK"] != 1 {
		t.Fatalf("truthful completion: %+v", got)
	}
	// Terminal outcomes are final; late/duplicate reports change nothing.
	if ack, _ := f.report(f.devA, cmd.TargetID, "FAILED", "UPDATE_HASH_MISMATCH", false); ack.TargetState != "SUCCEEDED" {
		t.Fatalf("terminal success rewritten: %+v", ack)
	}
	history, err := f.svc.TargetHistory(ctx, cmd.TargetID)
	if err != nil || len(history) != 5 {
		t.Fatalf("append-only history: %+v %v", history, err)
	}
}

func TestDeviceEligibilityIsTruthful(t *testing.T) {
	f := newFleetFixture(t)
	ctx := context.Background()
	old := f.device("pre-phase18", f.storeA) // never reports an updater
	incapable := f.device("deb-install", f.storeA)
	newer := f.device("newer", f.storeA)
	same := f.device("same", f.storeA)
	arm := f.device("arm", f.storeA)
	rel := f.release(11)
	f.capable(f.devA, 10)
	ro := f.rollout(fleetupdate.RolloutRequest{ReleaseID: rel.ID, Scope: "STORE", StoreID: f.storeA, Mode: "OPTIONAL", Percentage: 100}, true)
	if f.poll(old) != nil {
		t.Fatal("Cloud cannot bootstrap an updater into pre-Phase-18 Retail")
	}
	if err := f.svc.RecordStatus(ctx, incapable, fleetupdate.StatusReport{Version: "1.0.0", BuildCommit: "x", OS: "linux", Arch: "amd64",
		UpdaterProtocol: 1, UpdaterCapable: false, UnsupportedReason: "UPDATE_PLATFORM_UNSUPPORTED", UpdateState: "IDLE"}); err != nil {
		t.Fatal(err)
	}
	f.capable(newer, 12)
	if err := f.svc.RecordStatus(ctx, same, fleetupdate.StatusReport{Version: "1.1.0", BuildCommit: strings.Repeat("b", 40),
		ReleaseSequence: 11, OS: "linux", Arch: "amd64", UpdaterProtocol: 1, UpdaterCapable: true, UpdateState: "SUCCEEDED"}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RecordStatus(ctx, arm, fleetupdate.StatusReport{Version: "1.0.0", BuildCommit: strings.Repeat("a", 40),
		ReleaseSequence: 10, OS: "linux", Arch: "arm64", UpdaterProtocol: 1, UpdaterCapable: true, UpdateState: "IDLE"}); err != nil {
		t.Fatal(err)
	}
	if f.poll(arm) != nil {
		t.Fatal("no artifact for this platform")
	}
	states := map[string]string{}
	targets, _, err := f.svc.ListTargets(ctx, ro.ID, "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, tg := range targets {
		states[tg.DeviceID] = tg.State
	}
	want := map[string]string{old: "PENDING", incapable: "UNSUPPORTED", newer: "SKIPPED_NEWER", same: "ALREADY_COMPLIANT", arm: "UNSUPPORTED", f.devA: "PENDING"}
	for d, s := range want {
		if states[d] != s {
			t.Fatalf("device %s: %s want %s (%v)", d, states[d], s, states)
		}
	}
	fleet, _, err := f.svc.Fleet(ctx, f.storeA, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range fleet {
		if row.DeviceID == old && (row.UpdaterReported || row.UpdaterCapable) {
			t.Fatalf("pre-Phase-18 device must show updater unsupported/manual: %+v", row)
		}
	}
}

func TestRetryScheduleUsesApplicationClockUnderSkew(t *testing.T) {
	f := newFleetFixture(t)
	f.capable(f.devA, 10)
	rel := f.release(11)
	f.rollout(fleetupdate.RolloutRequest{ReleaseID: rel.ID, Scope: "DEVICE", StoreID: f.storeA, DeviceID: f.devA, Mode: "OPTIONAL", Percentage: 100}, true)
	cmd := f.poll(f.devA)
	if cmd == nil {
		t.Fatal("no command")
	}
	ack, err := f.report(f.devA, cmd.TargetID, "FAILED", "UPDATE_DOWNLOAD_FAILED", true)
	if err != nil || ack.TargetState != "PENDING" {
		t.Fatalf("%+v %v", ack, err)
	}
	failedAt := f.now()
	// Cloud clock -60s / +60s around the scheduling point: never earlier
	// than the persisted deadline, and no hot loop of re-deliveries.
	for _, skew := range []time.Duration{-60 * time.Second, 60 * time.Second, 14 * time.Minute} {
		f.set(failedAt.Add(skew))
		for i := 0; i < 20; i++ {
			if f.poll(f.devA) != nil {
				t.Fatalf("re-delivered before the application-clock deadline (skew %v)", skew)
			}
		}
	}
	f.set(failedAt.Add(15*time.Minute + time.Second))
	again := f.poll(f.devA)
	if again == nil || again.TargetID != cmd.TargetID {
		t.Fatal("retry not delivered after the deadline")
	}
	var attempts int
	if err := f.pool.QueryRow(context.Background(), `SELECT attempt_count FROM update_rollout_targets WHERE id = $1`, cmd.TargetID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("attempts %d %v", attempts, err)
	}
	// One permanently failing device never blocks another (§140).
	f.capable(f.devA2, 10)
	ro2 := f.rollout(fleetupdate.RolloutRequest{ReleaseID: rel.ID, Scope: "STORE", StoreID: f.storeA, Mode: "OPTIONAL", Percentage: 100}, true)
	_ = ro2
	if f.poll(f.devA2) == nil {
		t.Fatal("healthy device blocked by a failing device")
	}
}

func TestCancelRacesAndRevocationAndRebinding(t *testing.T) {
	f := newFleetFixture(t)
	ctx := context.Background()
	f.capable(f.devA, 10)
	f.capable(f.devA2, 10)
	rel := f.release(11)
	ro := f.rollout(fleetupdate.RolloutRequest{ReleaseID: rel.ID, Scope: "STORE", StoreID: f.storeA, Mode: "OPTIONAL", Percentage: 100}, true)
	cmd := f.poll(f.devA)
	if _, err := f.report(f.devA, cmd.TargetID, "WAITING_SAFE_BOUNDARY", "", false); err != nil {
		t.Fatal(err)
	}
	cancelled, err := f.svc.CancelRollout(ctx, "op", ro.ID)
	if err != nil || cancelled.Counts["CANCELLED"] != 2 {
		t.Fatalf("%+v %v", cancelled, err)
	}
	// The device had already begun activation: truth wins over the cancel.
	ack, err := f.report(f.devA, cmd.TargetID, "INSTALLING", "", false)
	if err != nil || ack.TargetState != "INSTALLING" {
		t.Fatalf("activation hidden behind cancel: %+v %v", ack, err)
	}
	// Pre-activation progress cannot undo a cancel; the ack tells Retail.
	if ack, _ := f.report(f.devA2, mustTargetFor(t, f, ro.ID, f.devA2), "DOWNLOADING", "", false); ack.TargetState != "CANCELLED" {
		t.Fatalf("cancel lost: %+v", ack)
	}
	events, _ := f.svc.Audit(ctx, f.storeA, 0, 50)
	found := false
	for _, e := range events {
		found = found || e.Action == "target.cancel_superseded"
	}
	if !found {
		t.Fatal("cancel supersession not audited")
	}

	// Revocation: no new delivery; reports learn REVOKED.
	rel2 := f.release(12)
	ro2 := f.rollout(fleetupdate.RolloutRequest{ReleaseID: rel2.ID, Scope: "STORE", StoreID: f.storeA, Mode: "OPTIONAL", Percentage: 100}, true)
	cmd2 := f.poll(f.devA2)
	if cmd2 == nil {
		t.Fatal("no command for release 12")
	}
	if _, err := f.svc.SetReleaseStatus(ctx, "op", rel2.ID, "REVOKED"); err != nil {
		t.Fatal(err)
	}
	if ack, _ := f.report(f.devA2, cmd2.TargetID, "DOWNLOADING", "", false); ack.ReleaseStatus != "REVOKED" {
		t.Fatalf("revocation not relayed: %+v", ack)
	}
	_ = ro2
	if _, err := f.svc.SetReleaseStatus(ctx, "op", rel2.ID, "ACTIVE"); err != nil {
		t.Fatal(err)
	}
	// Rebinding: a device moved to Store B no longer receives Store A's
	// snapshot target (prompt §95).
	if _, err := f.pool.Exec(ctx, `UPDATE device_store_bindings SET store_id = $2 WHERE device_id = $1`, f.devA2, f.storeB); err != nil {
		t.Fatal(err)
	}
	if f.poll(f.devA2) != nil {
		t.Fatal("rebound device received its old Store's target")
	}
}

func mustTargetFor(t *testing.T, f *fleetFixture, rolloutID, deviceID string) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), `SELECT id::text FROM update_rollout_targets WHERE rollout_id = $1 AND device_id = $2`, rolloutID, deviceID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestStagedRolloutIsDeterministicAndSnapshotFrozen(t *testing.T) {
	f := newFleetFixture(t)
	ctx := context.Background()
	var ids []string
	for i := 0; i < 40; i++ {
		ids = append(ids, f.device("bulk", f.storeA))
	}
	rel := f.release(11)
	ro := f.rollout(fleetupdate.RolloutRequest{ReleaseID: rel.ID, Scope: "STORE", StoreID: f.storeA, Mode: "OPTIONAL", Percentage: 25}, true)
	expect := func(pct int) int {
		n := 0
		for _, d := range append(ids, f.devA, f.devA2) {
			if fleetupdate.Bucket(ro.ID, d) < pct {
				n++
			}
		}
		return n
	}
	got, _ := f.svc.GetRollout(ctx, ro.ID)
	if int(got.Counts["PENDING"]) != expect(25) || got.TargetCount != 42 {
		t.Fatalf("25%%: %+v want %d", got, expect(25))
	}
	late := f.device("late-enrollment", f.storeA)
	got, err := f.svc.IncreasePercentage(ctx, "op", ro.ID, 60)
	if err != nil || int(got.Counts["PENDING"]) != expect(60) || got.TargetCount != 42 {
		t.Fatalf("60%%: %+v %v want %d", got, err, expect(60))
	}
	var lateTargets int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM update_rollout_targets WHERE device_id = $1`, late).Scan(&lateTargets); err != nil || lateTargets != 0 {
		t.Fatalf("late enrollment joined an existing rollout: %d %v", lateTargets, err)
	}
	if _, err := f.svc.IncreasePercentage(ctx, "op", ro.ID, 30); kind(err) != apperr.Conflict {
		t.Fatalf("percentage decrease accepted: %v", err)
	}
}

// Pause vs target claim: after a pause commits no delivery happens, under
// repeated concurrent polling (prompt §110).
func TestPauseVersusClaimRace(t *testing.T) {
	for round := 0; round < 10; round++ {
		f := newFleetFixture(t)
		var devs []string
		for i := 0; i < 8; i++ {
			d := f.device("race", f.storeA)
			f.capable(d, 10)
			devs = append(devs, d)
		}
		rel := f.release(11)
		ro := f.rollout(fleetupdate.RolloutRequest{ReleaseID: rel.ID, Scope: "STORE", StoreID: f.storeA, Mode: "OPTIONAL", Percentage: 100}, true)
		var wg sync.WaitGroup
		var paused atomic.Bool
		var lateDeliveries atomic.Int64
		for _, d := range devs {
			wg.Add(1)
			go func(d string) {
				defer wg.Done()
				before := paused.Load()
				cmd, err := f.svc.PollCommand(context.Background(), d)
				if err != nil {
					t.Error(err)
					return
				}
				if before && cmd != nil {
					lateDeliveries.Add(1)
				}
			}(d)
		}
		if _, err := f.svc.PauseRollout(context.Background(), "op", ro.ID); err != nil {
			t.Fatal(err)
		}
		paused.Store(true)
		wg.Wait()
		if lateDeliveries.Load() != 0 {
			t.Fatalf("delivered after pause committed: %d", lateDeliveries.Load())
		}
		for _, d := range devs {
			if f.poll(d) != nil {
				t.Fatal("paused rollout delivered")
			}
		}
	}
}

// Duplicate concurrent reports record one transition (prompt §110).
func TestConcurrentDuplicateReports(t *testing.T) {
	f := newFleetFixture(t)
	f.capable(f.devA, 10)
	rel := f.release(11)
	f.rollout(fleetupdate.RolloutRequest{ReleaseID: rel.ID, Scope: "DEVICE", StoreID: f.storeA, DeviceID: f.devA, Mode: "OPTIONAL", Percentage: 100}, true)
	cmd := f.poll(f.devA)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.report(f.devA, cmd.TargetID, "SUCCEEDED", "", false); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	history, err := f.svc.TargetHistory(context.Background(), cmd.TargetID)
	if err != nil || len(history) != 1 {
		t.Fatalf("duplicate reports recorded %d events: %v", len(history), err)
	}
}
