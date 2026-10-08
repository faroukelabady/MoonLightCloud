package fleetupdate

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func TestMapReportIsTruthful(t *testing.T) {
	cases := map[[2]string]string{
		{"AVAILABLE", ""}:                                    TargetDelivered,
		{"STAGED", ""}:                                       TargetVerified,
		{"SWITCHED", ""}:                                     TargetInstalling,
		{"AWAITING_STARTUP_HEALTH", ""}:                      TargetAwaitHealth,
		{"SUCCEEDED", ""}:                                    TargetSucceeded,
		{"SUCCEEDED", "UPDATE_ALREADY_INSTALLED"}:            TargetCompliant,
		{"FAILED", "UPDATE_DOWNGRADE_REJECTED"}:              TargetSkippedNewer,
		{"FAILED", "UPDATE_PLATFORM_UNSUPPORTED"}:            TargetUnsupported,
		{"FAILED", "UPDATE_SIGNATURE_INVALID"}:               TargetFailed,
		{"ROLLED_BACK", "UPDATE_HEALTH_FAILED"}:              TargetRolledBack,
		{"ROLLING_BACK", ""}:                                 TargetInstalling,
		{"MANUAL_ACTION_REQUIRED", "UPDATE_ROLLBACK_FAILED"}: TargetManual,
	}
	for in, want := range cases {
		got, ok := MapReport(in[0], in[1])
		if !ok || got != want {
			t.Fatalf("%v: got %q ok=%v want %q", in, got, ok, want)
		}
	}
	if _, ok := MapReport("UPDATED", ""); ok {
		t.Fatal("unknown local states are rejected")
	}
}

func TestApplyReportTransitions(t *testing.T) {
	// Success is never claimed early and terminal states are final.
	d := ApplyReport(TargetDelivered, 0, TargetInstalling, false, t0)
	if d.State != TargetInstalling || d.Finished {
		t.Fatalf("%+v", d)
	}
	d = ApplyReport(TargetSucceeded, 0, TargetFailed, false, t0)
	if d.Changed || d.State != TargetSucceeded {
		t.Fatalf("terminal state rewritten: %+v", d)
	}
	// Retryable failure: re-offered later on the application clock.
	d = ApplyReport(TargetDownloading, 0, TargetFailed, true, t0)
	if d.State != TargetPending || d.AttemptCount != 1 || d.NextAttemptAt == nil || !d.NextAttemptAt.Equal(t0.Add(15*time.Minute)) {
		t.Fatalf("retry not scheduled: %+v", d)
	}
	// Bounded: exhausted re-offers become terminal FAILED.
	d = ApplyReport(TargetDelivered, MaxCloudReoffers-1, TargetFailed, true, t0)
	if d.State != TargetFailed || !d.Finished {
		t.Fatalf("re-offers not bounded: %+v", d)
	}
	// Cancel raced with activation: device truth wins, flagged.
	d = ApplyReport(TargetCancelled, 0, TargetInstalling, false, t0)
	if d.State != TargetInstalling || !d.CancelSuperseded {
		t.Fatalf("activation hidden behind cancel: %+v", d)
	}
	d = ApplyReport(TargetCancelled, 0, TargetDownloading, false, t0)
	if d.Changed {
		t.Fatalf("pre-activation progress must not undo a cancel: %+v", d)
	}
	if ApplyReport(TargetNotSelected, 0, TargetInstalling, false, t0).Changed {
		t.Fatal("unselected target cannot be reported on")
	}
}

func TestBackoffIsBoundedAndClockIndependent(t *testing.T) {
	prev := time.Duration(0)
	for i := 1; i < 20; i++ {
		b := Backoff(i)
		if b < prev || b > 12*time.Hour {
			t.Fatalf("attempt %d backoff %v", i, b)
		}
		prev = b
	}
	// The deadline is computed from the passed clock: ±60s skew shifts it
	// by exactly the skew and never yields an immediate (hot-loop) retry.
	for _, skew := range []time.Duration{-60 * time.Second, 60 * time.Second} {
		d := ApplyReport(TargetDelivered, 0, TargetFailed, true, t0.Add(skew))
		if !d.NextAttemptAt.Equal(t0.Add(skew).Add(15 * time.Minute)) {
			t.Fatalf("skew %v: %v", skew, d.NextAttemptAt)
		}
	}
}

func TestBucketIsDeterministicAndSpread(t *testing.T) {
	rollout := "11111111-1111-4111-8111-111111111111"
	device := "22222222-2222-4222-8222-222222222222"
	if Bucket(rollout, device) != Bucket(rollout, device) {
		t.Fatal("bucket must be stable")
	}
	counts := make([]int, 100)
	for i := 0; i < 10000; i++ {
		id := "00000000-0000-4000-8000-" + padHex(i)
		counts[Bucket(rollout, id)]++
	}
	for b, n := range counts {
		if n < 50 || n > 160 {
			t.Fatalf("bucket %d badly skewed: %d", b, n)
		}
	}
}

func padHex(i int) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 12)
	for j := 11; j >= 0; j-- {
		out[j] = hexdigits[i%16]
		i /= 16
	}
	return string(out)
}

func TestRolloutRequestValidation(t *testing.T) {
	store := "33333333-3333-4333-8333-333333333333"
	device := "44444444-4444-4444-8444-444444444444"
	release := "55555555-5555-4555-8555-555555555555"
	valid := []RolloutRequest{
		{ReleaseID: release, Scope: ScopeAll, Mode: ModeOptional, Percentage: 10},
		{ReleaseID: release, Scope: ScopeStore, StoreID: store, Mode: ModeMandatory, Percentage: 100},
		{ReleaseID: release, Scope: ScopeDevice, StoreID: store, DeviceID: device, Mode: ModeOptional, Percentage: 100},
	}
	for _, r := range valid {
		if err := r.Validate(); err != nil {
			t.Fatalf("%+v: %v", r, err)
		}
	}
	invalid := []RolloutRequest{
		{ReleaseID: release, Scope: ScopeAll, StoreID: store, Mode: ModeOptional, Percentage: 10},
		{ReleaseID: release, Scope: ScopeStore, Mode: ModeOptional, Percentage: 10},
		{ReleaseID: release, Scope: ScopeAll, Mode: "FORCE", Percentage: 10},
		{ReleaseID: release, Scope: ScopeAll, Mode: ModeOptional, Percentage: 0},
		{ReleaseID: "rm -rf /", Scope: ScopeAll, Mode: ModeOptional, Percentage: 10},
	}
	for _, r := range invalid {
		if r.Validate() == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
}
