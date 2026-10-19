// Package projection owns only neutral durable-processing diagnostics and retry
// policy. Business projectors retain their own DTOs, transactions and scheduling.
package projection

import "time"

// Processing statuses mirror sync_event_processing's CHECK constraint.
const (
	Pending   = "pending"
	Retry     = "retry"
	Blocked   = "blocked"
	Processed = "processed"
)

// Stats is operator visibility over processing state.
type Stats struct {
	Counts         map[string]int64
	OldestPending  *time.Time
	PendingCount   int64
	LastErrorEvent string
	LastErrorCode  string
	LastErrorMsg   string
}

// Backoff computes the next attempt delay: 5s × 2^attempt, capped at 1h.
// Overflow-safe: attempt saturates (no 2^attempt loop blowup, no duration
// multiplication overflow); the 1h policy maximum is retained.
func Backoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 20 {
		attempt = 20 // 5s × 2^20 already exceeds the cap
	}
	d := 5 * time.Second
	for i := 0; i < attempt; i++ {
		if d >= time.Hour/2 {
			return time.Hour
		}
		d *= 2
	}
	if d > time.Hour {
		return time.Hour
	}
	return d
}
