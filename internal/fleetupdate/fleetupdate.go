// Package fleetupdate is the Phase 18 Cloud fleet-update domain
// (ADR-0052). It decides WHICH already-signed release is requested for
// WHICH device and WHEN; it never executes anything on Retail and never
// owns Retail's local update state. Retail reports its locally
// authoritative state; this package maps those reports onto truthful
// fleet target states. Pure: no SQL, no HTTP, no clock reads.
package fleetupdate

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"regexp"
	"strings"
	"time"
)

// Rollout scopes, modes and lifecycle.
const (
	ScopeDevice = "DEVICE"
	ScopeStore  = "STORE"
	ScopeAll    = "ALL"

	ModeOptional  = "OPTIONAL"
	ModeMandatory = "MANDATORY"

	RolloutDraft     = "DRAFT"
	RolloutActive    = "ACTIVE"
	RolloutPaused    = "PAUSED"
	RolloutCompleted = "COMPLETED"
	RolloutCancelled = "CANCELLED"

	ReleaseActive  = "ACTIVE"
	ReleaseRevoked = "REVOKED"

	// Package delivered to updater protocol 1.
	PackageTarGz = "tar.gz"
	// CommandType is the only device update intent (protocol v1).
	CommandType     = "retail.update.install.v1"
	UpdaterProtocol = 1
	// MaxTargets bounds one rollout snapshot.
	MaxTargets = 10000
	// MaxCloudReoffers bounds Cloud re-delivery after retryable failures.
	MaxCloudReoffers = 5
)

// Target states (fleet view; never Retail's local authority).
const (
	TargetNotSelected   = "NOT_SELECTED"
	TargetPending       = "PENDING"
	TargetDelivered     = "DELIVERED"
	TargetDownloading   = "DOWNLOADING"
	TargetVerified      = "VERIFIED"
	TargetWaiting       = "WAITING_SAFE_BOUNDARY"
	TargetInstalling    = "INSTALLING"
	TargetAwaitHealth   = "AWAITING_HEALTH"
	TargetSucceeded     = "SUCCEEDED"
	TargetCompliant     = "ALREADY_COMPLIANT"
	TargetFailed        = "FAILED"
	TargetRolledBack    = "ROLLED_BACK"
	TargetManual        = "MANUAL_ACTION_REQUIRED"
	TargetSkippedNewer  = "SKIPPED_NEWER"
	TargetUnsupported   = "UNSUPPORTED"
	TargetCancelled     = "CANCELLED"
	codeAlreadyInstalld = "UPDATE_ALREADY_INSTALLED"
)

var (
	ErrInvalid  = errors.New("invalid update request")
	ErrConflict = errors.New("update state conflict")
	ErrNotFound = errors.New("not found")
)

var codePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// ValidCode reports a bounded machine error code.
func ValidCode(code string) bool { return codePattern.MatchString(code) }

// IsTerminal reports a final target state.
func IsTerminal(state string) bool {
	switch state {
	case TargetSucceeded, TargetCompliant, TargetFailed, TargetRolledBack, TargetManual,
		TargetSkippedNewer, TargetUnsupported, TargetCancelled:
		return true
	}
	return false
}

// activating states are irreversible on the device: cancellation and
// pause cannot stop them (prompt §54-§55).
func activating(state string) bool {
	return state == TargetInstalling || state == TargetAwaitHealth
}

// MapReport maps one Retail-reported local state onto a fleet target
// state. ok=false rejects an unknown state value.
func MapReport(localState, errorCode string) (string, bool) {
	switch localState {
	case "AVAILABLE":
		return TargetDelivered, true
	case "DOWNLOADING":
		return TargetDownloading, true
	case "DOWNLOADED", "VERIFYING", "VERIFIED", "STAGED":
		return TargetVerified, true
	case "WAITING_SAFE_BOUNDARY":
		return TargetWaiting, true
	case "INSTALLING", "SWITCHED":
		return TargetInstalling, true
	case "AWAITING_STARTUP_HEALTH":
		return TargetAwaitHealth, true
	case "SUCCEEDED":
		if errorCode == codeAlreadyInstalld {
			return TargetCompliant, true
		}
		return TargetSucceeded, true
	case "ROLLED_BACK", "ROLLING_BACK":
		if localState == "ROLLING_BACK" {
			return TargetInstalling, true
		}
		return TargetRolledBack, true
	case "MANUAL_ACTION_REQUIRED":
		return TargetManual, true
	case "FAILED":
		switch errorCode {
		case "UPDATE_DOWNGRADE_REJECTED":
			return TargetSkippedNewer, true
		case "UPDATE_PLATFORM_UNSUPPORTED", "UPDATE_NOT_CONFIGURED":
			return TargetUnsupported, true
		}
		return TargetFailed, true
	}
	return "", false
}

// Decision is the outcome of applying one report.
type Decision struct {
	State         string
	AttemptCount  int
	NextAttemptAt *time.Time
	Finished      bool
	Changed       bool
	// CancelSuperseded: device truth (activation) overrode a racing cancel.
	CancelSuperseded bool
}

// ApplyReport decides the next target state from the current state and a
// device report, using the single application clock now.
//
//   - Terminal states are final, except that a CANCELLED target accepts
//     the device's report of real activation or its outcome: Cloud must
//     not claim a cancellation the device never observed (§55).
//   - A retryable FAILED report is re-offered later with bounded backoff
//     on the application clock (§45, §139), up to MaxCloudReoffers.
func ApplyReport(current string, attempts int, reported string, retryable bool, now time.Time) Decision {
	d := Decision{State: current, AttemptCount: attempts}
	if current == TargetCancelled {
		if activating(reported) || reported == TargetSucceeded || reported == TargetRolledBack || reported == TargetManual {
			d.State, d.Changed, d.CancelSuperseded = reported, true, true
			d.Finished = IsTerminal(reported)
		}
		return d
	}
	if IsTerminal(current) || current == reported {
		return d
	}
	if current == TargetNotSelected {
		return d // never delivered: a report cannot refer to it
	}
	d.Changed = true
	if reported == TargetFailed && retryable && attempts+1 < MaxCloudReoffers {
		d.AttemptCount = attempts + 1
		next := now.Add(Backoff(d.AttemptCount))
		d.State, d.NextAttemptAt = TargetPending, &next
		return d
	}
	if reported == TargetFailed {
		d.AttemptCount = attempts + 1
	}
	d.State = reported
	d.Finished = IsTerminal(reported)
	return d
}

// Backoff is the Cloud re-offer delay: 15m, 30m, 1h … capped at 12h.
func Backoff(attempt int) time.Duration {
	d := 15 * time.Minute
	for i := 1; i < attempt && d < 12*time.Hour; i++ {
		d *= 2
	}
	if d > 12*time.Hour {
		d = 12 * time.Hour
	}
	return d
}

// Bucket is the deterministic percentage slot of a device in a rollout:
// stable across polls, restarts and Cloud instances (prompt §52).
func Bucket(rolloutID, deviceID string) int {
	sum := sha256.Sum256([]byte(strings.ToLower(rolloutID) + ":" + strings.ToLower(deviceID)))
	return int(binary.BigEndian.Uint64(sum[:8]) % 100)
}

// RolloutRequest is validated operator intent.
type RolloutRequest struct {
	ReleaseID  string
	Scope      string
	StoreID    string
	DeviceID   string
	Mode       string
	Percentage int
	NotBefore  *time.Time
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ValidUUID reports a canonical lowercase UUID.
func ValidUUID(s string) bool { return uuidPattern.MatchString(s) }

// Validate checks a rollout request's closed vocabulary.
func (r RolloutRequest) Validate() error {
	switch {
	case !ValidUUID(r.ReleaseID):
		return ErrInvalid
	case r.Mode != ModeOptional && r.Mode != ModeMandatory:
		return ErrInvalid
	case r.Percentage < 1 || r.Percentage > 100:
		return ErrInvalid
	}
	switch r.Scope {
	case ScopeDevice:
		if !ValidUUID(r.StoreID) || !ValidUUID(r.DeviceID) {
			return ErrInvalid
		}
	case ScopeStore:
		if !ValidUUID(r.StoreID) || r.DeviceID != "" {
			return ErrInvalid
		}
	case ScopeAll:
		if r.StoreID != "" || r.DeviceID != "" {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}
