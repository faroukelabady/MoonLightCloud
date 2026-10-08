package catalog

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
)

// wakeStore is a minimal Store for projector wake tests. Unused Store
// methods are left to the embedded nil interface and must not be called.
type wakeStore struct {
	Store
	mu        sync.Mutex
	pending   func(scan int) []string
	nextRetry func() (time.Time, bool)
	scans     int
}

func (s *wakeStore) PendingCatalogEvents(context.Context, string, string, int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scans++
	if s.pending == nil {
		return nil, nil
	}
	return s.pending(s.scans), nil
}

func (s *wakeStore) NextCatalogRetry(context.Context, string, string) (time.Time, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nextRetry == nil {
		return time.Time{}, false, nil
	}
	next, ok := s.nextRetry()
	return next, ok, nil
}

func (s *wakeStore) LoadCatalogEvent(_ context.Context, id string) (EventRecord, bool, error) {
	return EventRecord{EventID: id}, true, nil
}

func (s *wakeStore) scanCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scans
}

func wakeTestProjector(s *wakeStore, interval time.Duration, project func(EventRecord) Outcome) *Projector {
	p := newProjector(s, "test.processor", "test.event",
		func(_ context.Context, e EventRecord, _ time.Time) (ProjectResult, error) {
			return ProjectResult{Outcome: project(e)}, nil
		}, clock.System{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.interval = interval
	return p
}

func runProjector(t *testing.T, p *Projector) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func TestProjectorWakesDependentsAfterProgress(t *testing.T) {
	s := &wakeStore{pending: func(scan int) []string {
		if scan == 1 {
			return []string{"e1"}
		}
		return nil
	}}
	p := wakeTestProjector(s, time.Hour, func(EventRecord) Outcome { return OutcomeProcessed })
	woke := make(chan struct{}, 1)
	p.WakeOnProgress(func() {
		select {
		case woke <- struct{}{}:
		default:
		}
	})
	runProjector(t, p)
	select {
	case <-woke:
	case <-time.After(5 * time.Second):
		t.Fatal("processed work did not wake dependent projectors")
	}
}

func TestProjectorDoesNotWakeDependentsWithoutProgress(t *testing.T) {
	for _, outcome := range []Outcome{OutcomeRetryable, OutcomeBlocked, OutcomeAlready, OutcomeNotDue} {
		s := &wakeStore{pending: func(scan int) []string {
			if scan == 1 {
				return []string{"e1"}
			}
			return nil
		}}
		p := wakeTestProjector(s, time.Hour, func(EventRecord) Outcome { return outcome })
		var wakes atomic.Int32
		p.WakeOnProgress(func() { wakes.Add(1) })
		runProjector(t, p)
		deadline := time.Now().Add(2 * time.Second)
		for s.scanCount() < 2 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if s.scanCount() < 2 {
			t.Fatalf("outcome %v: drain did not complete", outcome)
		}
		if n := wakes.Load(); n != 0 {
			t.Fatalf("outcome %v woke dependents %d times without progress", outcome, n)
		}
	}
}

// A durable retry scheduled for the near future must run when it falls
// due, not at the (here one-hour) safety scan.
func TestProjectorRunsScheduledRetryWhenDue(t *testing.T) {
	due := time.Now().Add(700 * time.Millisecond)
	var released atomic.Bool
	s := &wakeStore{
		pending: func(int) []string {
			if time.Now().Before(due) || released.Load() {
				return nil
			}
			released.Store(true)
			return []string{"retry-1"}
		},
		nextRetry: func() (time.Time, bool) {
			if released.Load() {
				return time.Time{}, false
			}
			return due, true
		},
	}
	processed := make(chan time.Time, 1)
	p := wakeTestProjector(s, time.Hour, func(EventRecord) Outcome {
		processed <- time.Now()
		return OutcomeProcessed
	})
	runProjector(t, p)
	select {
	case at := <-processed:
		if at.Before(due) {
			t.Fatalf("retry ran before it was due: %v < %v", at, due)
		}
		if late := at.Sub(due); late > 2*time.Second {
			t.Fatalf("retry ran %v after it was due", late)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scheduled retry did not run before the safety scan")
	}
}

// A retry time already in the past (application/database clock skew) is
// floored, so the projector cannot spin.
func TestProjectorRetryWakeIsFloored(t *testing.T) {
	s := &wakeStore{nextRetry: func() (time.Time, bool) {
		return time.Now().Add(-time.Minute), true
	}}
	p := wakeTestProjector(s, time.Hour, func(EventRecord) Outcome { return OutcomeProcessed })
	runProjector(t, p)
	time.Sleep(1200 * time.Millisecond)
	// Startup scan plus at most two floored (500ms) wakes in 1.2s.
	if n := s.scanCount(); n > 4 {
		t.Fatalf("retry wake not floored: %d scans in 1.2s", n)
	}
}

// Retries due later than the safety scan are left to the ticker.
func TestProjectorIgnoresRetryBeyondSafetyScan(t *testing.T) {
	s := &wakeStore{nextRetry: func() (time.Time, bool) {
		return time.Now().Add(200 * time.Millisecond), true
	}}
	p := wakeTestProjector(s, 100*time.Millisecond, func(EventRecord) Outcome { return OutcomeProcessed })
	runProjector(t, p)
	time.Sleep(550 * time.Millisecond)
	if n := s.scanCount(); n < 3 {
		t.Fatalf("safety scan stalled: %d scans", n)
	}
}
