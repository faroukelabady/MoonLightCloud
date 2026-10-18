package devicecontrol

import (
	"testing"
	"time"
)

// Control state is independent of other contact: a device that uploads
// sync data but has not polled cannot receive Sync Now yet.
func TestDeriveControl(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	window := 2 * time.Minute
	recent, old := now.Add(-time.Minute), now.Add(-time.Hour)
	for _, tc := range []struct {
		poll *time.Time
		want string
	}{{nil, ControlNeverPolled}, {&time.Time{}, ControlNeverPolled}, {&recent, ControlPolling}, {&old, ControlPollStale}} {
		if got := DeriveControl(tc.poll, now, window); got != tc.want {
			t.Fatalf("poll %v: got %s want %s", tc.poll, got, tc.want)
		}
	}
}
