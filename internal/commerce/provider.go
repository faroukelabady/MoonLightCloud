// Package commerce owns the provider-neutral Cloud commerce boundary.
// Phase 6A establishes the CommerceProvider abstraction, provider
// registry, product mapping durability, deterministic operation identity,
// provider error taxonomy, and synchronous orchestration that future
// adapters (6B) invoke. No vendor SDKs, no network calls, no credentials,
// no jobs, no webhooks, no orders: see ADR-0031. The Phase 1A placeholder
// Provider/Order sketch is superseded by this surface.
package commerce

import (
	"context"
	"fmt"
	"regexp"
	"time"
)

// ProviderKey is the stable logical identity of one provider instance
// (for example "primary" or "website"). It is never a vendor kind:
// adapter behavior comes from the registered implementation, never from
// parsing this string. Bounded, lowercase, no free text.
type ProviderKey string

var providerKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidateProviderKey rejects empty, overlong, or unsafe provider keys.
func ValidateProviderKey(key string) (ProviderKey, error) {
	if !providerKeyPattern.MatchString(key) {
		return "", fmt.Errorf("invalid provider key %q: use 1..64 lowercase letters, numbers, hyphen, underscore", key)
	}
	return ProviderKey(key), nil
}

// Money is an exact minor-unit amount in a currency code. No floats.
type Money struct {
	Currency    string
	AmountMinor int64
	CostMinor   *int64
}

// LocalizedName preserves one locale/name pair; adapters decide how to
// render provider-specific titles in 6B.
type LocalizedName struct {
	Locale string
	Name   string
}

// CategoryRef carries a stable MoonLight category identity plus current
// display names. No external provider category IDs in 6A.
type CategoryRef struct {
	ID     string
	NameAR string
	NameEN string
}

// TagRef carries a stable MoonLight tag identity plus current metadata.
type TagRef struct {
	ID     string
	Slug   string
	NameAR string
	NameEN string
}

// CommerceProduct is the provider-neutral product snapshot handed to
// adapters. It contains only frozen Cloud state: no sqlc structs, no
// provider identities, no invented fields.
type CommerceProduct struct {
	ProductID       string
	SKU             string
	Names           []LocalizedName
	Descriptions    map[string]string
	Prices          []Money
	WidthCM         *int
	HeightCM        *int
	TopCategory     CategoryRef
	Subcategories   []CategoryRef
	Tags            []TagRef
	IsActive        bool
	SellOnline      bool
	AllocationLimit *int
	CatalogRevision int64
	PolicyRevision  int64
	// Configurations carries the complete current option set (enabled
	// and disabled). Adapters publish enabled choices and converge
	// disabled ones out of customer selection WITHOUT deleting mapping
	// state. Products without configurations keep the simple
	// representation (§113).
	Configurations []CommerceConfiguration
}

// CommerceConfiguration is one provider-neutral ONLINE product option
// (Phase 15): a valid frame configuration attached to the canonical
// MoonLight Product. It is NEVER a Product — no SKU, no stock, no
// inventory identity. Configured price = base Product price + delta per
// currency (exact int64 minor units; no floats, no FX).
type CommerceConfiguration struct {
	ConfigurationID       string
	Kind                  string // "frame"
	StyleCode             string
	StyleNameAR           string
	StyleNameEN           *string
	ColorCode             string
	ColorNameAR           string
	ColorNameEN           *string
	PriceDeltaEGPMinor    int64
	PriceDeltaUSDMinor    *int64
	Enabled               bool
	Position              int
	ConfigurationRevision int64
}

// NoFrameConfigurationID is the stable mapping-key sentinel for the
// implicit NO-FRAME choice (§18): it never identifies a MoonLight
// configuration row and no Product identity is derived from it.
const NoFrameConfigurationID = "00000000-0000-0000-0000-000000000000"

// ProviderProductRef is the minimal external identity an adapter returns.
// Variant/category/media/order identifiers are future phases.
type ProviderProductRef struct {
	ExternalProductID string
}

// ProductUpsertRequest asks the adapter to converge one remote product to
// the desired provider-neutral state.
type ProductUpsertRequest struct {
	ProviderKey      ProviderKey
	ProductID        string
	ExistingExternal *ProviderProductRef
	Product          CommerceProduct
	Published        bool
	CatalogRevision  int64
	PolicyRevision   int64
	OperationKey     string
}

// ProductUpsertResult carries the external identity plus (Phase 15) the
// provider-side identity of each published configuration choice —
// keyed by MoonLight configuration ID, with NoFrameConfigurationID for
// the implicit NO-FRAME choice. Persisted as durable configuration
// mappings by the service seam (never label-matched).
type ProductUpsertResult struct {
	ExternalProductID string
	Configurations    map[string]string
}

// InventoryUpdateRequest asks the adapter to set provider-facing
// availability. AvailableQuantity is Phase 5C derived availability
// (safe-zero when not ready), never raw Retail stock.
type InventoryUpdateRequest struct {
	ProviderKey       ProviderKey
	ProductID         string
	ExternalProductID string
	AvailableQuantity int64
	InventoryRevision int64
	CatalogRevision   int64
	PolicyRevision    int64
	Ready             bool
	OperationKey      string
}

// CommerceProvider is the single provider-neutral adapter boundary.
// UpsertProduct converges remote product metadata/publication state;
// SetInventory sets remote availability. Orders, refunds, fulfillment,
// customers, shipping, webhooks, and payments are later phases and must
// not be added here speculatively.
type CommerceProvider interface {
	Key() ProviderKey
	UpsertProduct(ctx context.Context, req ProductUpsertRequest) (ProductUpsertResult, error)
	SetInventory(ctx context.Context, req InventoryUpdateRequest) error
}

// ErrorKind classifies provider failures for future orchestration.
type ErrorKind string

const (
	// ErrorTemporary is a transient failure: safe to retry the same
	// operation key.
	ErrorTemporary ErrorKind = "temporary"
	// ErrorRateLimited is throttling: retryable, honoring RetryAfter.
	ErrorRateLimited ErrorKind = "rate_limited"
	// ErrorAuthentication is credential/configuration failure: terminal
	// until corrected.
	ErrorAuthentication ErrorKind = "authentication"
	// ErrorValidation means the desired state itself is unacceptable:
	// terminal for the same operation key.
	ErrorValidation ErrorKind = "validation"
	// ErrorConflict means the remote state disagrees in a way that needs
	// manual or domain handling: terminal for automatic retry.
	ErrorConflict ErrorKind = "conflict"
)

// ProviderError is a classified, retryability-explicit adapter failure.
// Messages must stay free of API tokens, authorization headers, request
// bodies, and credentials; adapters wrap safely before returning.
type ProviderError struct {
	Kind          ErrorKind
	Message       string
	RetryAfter    time.Duration
	hasRetryAfter bool
}

// Error implements error.
func (e *ProviderError) Error() string { return string(e.Kind) + ": " + e.Message }

// Retryable reports whether the same operation key may be retried.
func (e *ProviderError) Retryable() bool {
	return e.Kind == ErrorTemporary || e.Kind == ErrorRateLimited
}

// GetRetryAfter returns throttling metadata when present.
func (e *ProviderError) GetRetryAfter() (time.Duration, bool) {
	return e.RetryAfter, e.hasRetryAfter
}

// TemporaryError builds a retryable transient failure.
func TemporaryError(message string) *ProviderError {
	return &ProviderError{Kind: ErrorTemporary, Message: message}
}

// RateLimitedError builds a retryable throttling failure with optional
// backoff metadata.
func RateLimitedError(message string, retryAfter time.Duration) *ProviderError {
	return &ProviderError{Kind: ErrorRateLimited, Message: message, RetryAfter: retryAfter, hasRetryAfter: true}
}

// AuthenticationError builds a terminal credential/configuration failure.
func AuthenticationError(message string) *ProviderError {
	return &ProviderError{Kind: ErrorAuthentication, Message: message}
}

// ValidationError builds a terminal same-state failure.
func ValidationError(message string) *ProviderError {
	return &ProviderError{Kind: ErrorValidation, Message: message}
}

// ConflictError builds a terminal state-disagreement failure.
func ConflictError(message string) *ProviderError {
	return &ProviderError{Kind: ErrorConflict, Message: message}
}
