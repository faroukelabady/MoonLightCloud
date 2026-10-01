// Package store owns the Phase 9A Cloud Store registry model: minimal
// store rows plus immutable device bindings. Retail owns Store identity;
// Cloud registers and projects it, never invents a competing ID.
// No multi-store business semantics live here.
package store

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// Lifecycle states for stores.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// Store is one registered retail Store.
type Store struct {
	ID          string
	DisplayName string
	Timezone    string
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Binding is one authoritative device→Store link. Immutable after
// creation: conflicts stay conflicts, never silent rebinds.
type Binding struct {
	DeviceID  string
	StoreID   string
	CreatedAt time.Time
}

// RegistrationRequest is the Retail-presented Store identity. The device
// credential (not store_id knowledge) authorizes the binding: only the
// operator-provisioned device channel may call, and bound devices
// presenting a different store conflict deterministically.
type RegistrationRequest struct {
	StoreID     string `json:"store_id"`
	DisplayName string `json:"display_name"`
	Timezone    string `json:"timezone"`
}

// RegistrationResult is the authoritative binding outcome.
type RegistrationResult struct {
	StoreID     string
	DisplayName string
	Timezone    string
	Bound       bool
}

// ValidateRegistration enforces request bounds. No unbounded strings, no
// config blobs; timezone must be a valid IANA identifier.
func ValidateRegistration(request RegistrationRequest) error {
	if !isUUID(request.StoreID) {
		return apperr.New(apperr.InvalidInput, "store_id must be a UUID")
	}
	name := strings.TrimSpace(request.DisplayName)
	if name == "" || len([]rune(name)) > 200 {
		return apperr.New(apperr.InvalidInput, "display_name must be 1..200 runes")
	}
	zone := strings.TrimSpace(request.Timezone)
	if zone == "" || len(zone) > 64 {
		return apperr.New(apperr.InvalidInput, "timezone must be 1..64 chars")
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return apperr.New(apperr.InvalidInput, "timezone must be a valid IANA identifier")
	}
	return nil
}

func isUUID(value string) bool {
	_, err := uuid.Parse(value)
	return err == nil
}

// Summary is one store row with its bound device count for operator
// display. Read-oriented; no financial or inventory data.
type Summary struct {
	ID          string
	DisplayName string
	Timezone    string
	Status      string
	DeviceCount int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
