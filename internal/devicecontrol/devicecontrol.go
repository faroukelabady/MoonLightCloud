// Package devicecontrol owns the Phase 7C Cloud control-plane domain:
// durable sync_now commands, monotonic state transitions, and derived
// connectivity. No pgx, no net/http; adapters own I/O.
package devicecontrol

import (
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// Closed command vocabulary: exactly one Cloud→Retail command in v1.
const (
	CommandSyncNow    = "sync_now"
	CommandVersionV1  = 1
	MaxIdempotencyLen = 128
	MaxResultCodeLen  = 64
)

// Durable command states.
const (
	StatusPending   = "pending"
	StatusLeased    = "leased"
	StatusAccepted  = "accepted"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

// Derived connectivity (never stored as a mutable flag).
const (
	ConnectivityNeverSeen = "NEVER_SEEN"
	ConnectivityOnline    = "ONLINE"
	ConnectivityOffline   = "OFFLINE"
)

// Bounded machine result codes. Cloud never accepts free-form text.
var allowedResultCodes = map[string]bool{
	"SYNC_COMPLETED":       true,
	"SYNC_FAILED_NETWORK":  true,
	"SYNC_FAILED_AUTH":     true,
	"SYNC_FAILED_CONFLICT": true,
	"SYNC_FAILED_INTERNAL": true,
}

// Command is the durable Cloud control command.
type Command struct {
	ID              string
	DeviceID        string
	Type            string
	Version         int
	IdempotencyKey  string
	Status          string
	RequestedAt     time.Time
	LeasedAt        *time.Time
	LeaseUntil      *time.Time
	LeaseGeneration int64
	AcceptedAt      *time.Time
	RunningAt       *time.Time
	FinishedAt      *time.Time
	ResultCode      *string
}

// Presence is server-observed control-plane contact.
type Presence struct {
	DeviceID       string
	LastSeenAt     *time.Time
	LastPollAt     *time.Time
	LastAcceptedAt *time.Time
	LastFinishedAt *time.Time
}

// ValidateIdempotencyKey enforces bounded ASCII per existing conventions.
// Failures carry a specific bounded machine code (never the key itself).
func ValidateIdempotencyKey(key string) error {
	if len(key) == 0 || len(key) > MaxIdempotencyLen {
		return apperr.New(apperr.InvalidInput, "DEVICE_COMMAND_INVALID_IDEMPOTENCY_KEY")
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '_' || c == ':' || c == '.' || c == '~' || c == '-' {
			continue
		}
		return apperr.New(apperr.InvalidInput, "DEVICE_COMMAND_INVALID_IDEMPOTENCY_KEY")
	}
	return nil
}

// ValidateResultCode allows only bounded machine codes.
func ValidateResultCode(code string) error {
	if len(code) == 0 || len(code) > MaxResultCodeLen {
		return apperr.New(apperr.InvalidInput, "invalid result code")
	}
	if !allowedResultCodes[code] {
		return apperr.New(apperr.InvalidInput, "unknown result code")
	}
	return nil
}

// IsTerminal reports completed/failed.
func IsTerminal(status string) bool {
	return status == StatusCompleted || status == StatusFailed
}

// IsActive reports non-terminal sync_now states.
func IsActive(status string) bool {
	switch status {
	case StatusPending, StatusLeased, StatusAccepted, StatusRunning:
		return true
	}
	return false
}

// DeriveConnectivity computes NEVER_SEEN/ONLINE/OFFLINE from server time.
func DeriveConnectivity(lastSeen *time.Time, now time.Time, onlineWindow time.Duration) string {
	if lastSeen == nil || lastSeen.IsZero() {
		return ConnectivityNeverSeen
	}
	if now.Sub(lastSeen.UTC()) <= onlineWindow {
		return ConnectivityOnline
	}
	return ConnectivityOffline
}

// UserFacing maps durable states to dashboard labels (no delivery semantics).
func UserFacing(status string) string {
	switch status {
	case StatusPending:
		return "Queued"
	case StatusLeased:
		return "Delivering"
	case StatusAccepted:
		return "Received by device"
	case StatusRunning:
		return "Syncing"
	case StatusCompleted:
		return "Completed"
	case StatusFailed:
		return "Failed"
	default:
		return status
	}
}
