// Package notifications owns the provider-neutral Cloud notification
// boundary. Phase 7A establishes the NotificationProvider abstraction,
// provider registry, template-mapping durability, deterministic enqueue
// identity, provider error taxonomy, and synchronous orchestration that
// future phases (7B reports, 7D alerts) invoke. No vendor SDKs, no
// network calls in the domain, no credentials, no schedules, no
// automatic business triggers: see ADR-0034.
package notifications

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// ProviderKey is the stable logical identity of one notification
// provider instance (for example "whatsapp-main"). It is never a
// vendor kind: adapter behavior comes from the registered
// implementation, never from parsing this string.
type ProviderKey string

var providerKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidateProviderKey rejects empty, overlong, or unsafe provider keys.
func ValidateProviderKey(key string) (ProviderKey, error) {
	if !providerKeyPattern.MatchString(key) {
		return "", fmt.Errorf("invalid provider key %q: use 1..64 lowercase letters, numbers, hyphen, underscore", key)
	}
	return ProviderKey(key), nil
}

// TemplateSendRequest asks the adapter to deliver one template message.
// Parameters are logical (template_key + named values); the adapter
// resolves them through the durable template mapping snapshot carried
// in Resolved.
type TemplateSendRequest struct {
	ProviderKey ProviderKey
	Recipient   string
	Resolved    ResolvedTemplate
}

// ResolvedTemplate is the immutable provider view snapshotted at
// enqueue: the logical key plus the external mapping frozen for this
// notification, so later mapping edits cannot silently transform
// already-queued business messages.
type ResolvedTemplate struct {
	TemplateKey          string
	Locale               string
	ExternalTemplateName string
	ExternalLanguageCode string
	ParameterOrder       []string
	Parameters           map[string]string
}

// SendResult carries the provider message identity. Exactly one
// non-empty bounded ID is required for acceptance; anything else is a
// provider-level ambiguity the adapter must surface, never success.
type SendResult struct {
	ProviderMessageID string
}

// NotificationProvider is the single provider-neutral send boundary.
// Template-only in v1: no free-form text, no media, no interactivity.
type NotificationProvider interface {
	Key() ProviderKey
	SendTemplate(ctx context.Context, req TemplateSendRequest) (SendResult, error)
}

// ErrorKind classifies notification provider failures. Ambiguous is
// terminal for automatic retry: the request may already have reached
// the provider, so resending could duplicate a business message.
type ErrorKind string

const (
	// ErrorTemporary is an explicit safely-retryable failure (408/5xx
	// or a proven before-write transport failure).
	ErrorTemporary ErrorKind = "temporary"
	// ErrorRateLimited is throttling: retryable, honoring RetryAfter.
	ErrorRateLimited ErrorKind = "rate_limited"
	// ErrorAuthentication is credential/configuration failure: terminal
	// until corrected.
	ErrorAuthentication ErrorKind = "authentication"
	// ErrorValidation means the request itself is unacceptable:
	// terminal for the same payload.
	ErrorValidation ErrorKind = "validation"
	// ErrorConflict means remote state disagrees in a way that needs
	// manual handling: terminal for automatic retry.
	ErrorConflict ErrorKind = "conflict"
	// ErrorAmbiguous means the send outcome is unknown (after-write
	// transport failure, missing/malformed success ID): terminal for
	// automatic retry. Human assessment decides the next step.
	ErrorAmbiguous ErrorKind = "ambiguous"
)

// NotificationError is a classified, retryability-explicit failure.
// Messages must stay free of tokens, secrets, recipients, parameter
// values, and response bodies; adapters scrub before returning.
type NotificationError struct {
	Kind          ErrorKind
	Message       string
	RetryAfter    time.Duration
	hasRetryAfter bool
}

// Error implements error.
func (e *NotificationError) Error() string { return string(e.Kind) + ": " + e.Message }

// Retryable reports whether the same notification may be sent again
// automatically. Ambiguous is deliberately NOT retryable.
func (e *NotificationError) Retryable() bool {
	return e.Kind == ErrorTemporary || e.Kind == ErrorRateLimited
}

// GetRetryAfter returns throttling metadata when present.
func (e *NotificationError) GetRetryAfter() (time.Duration, bool) {
	return e.RetryAfter, e.hasRetryAfter
}

// TemporaryError builds a safely-retryable failure.
func TemporaryError(message string) *NotificationError {
	return &NotificationError{Kind: ErrorTemporary, Message: message}
}

// RateLimitedError builds throttling with optional backoff metadata.
func RateLimitedError(message string, retryAfter time.Duration) *NotificationError {
	return &NotificationError{Kind: ErrorRateLimited, Message: message, RetryAfter: retryAfter, hasRetryAfter: true}
}

// AuthenticationError builds a terminal credential failure.
func AuthenticationError(message string) *NotificationError {
	return &NotificationError{Kind: ErrorAuthentication, Message: message}
}

// ValidationError builds a terminal same-payload failure.
func ValidationError(message string) *NotificationError {
	return &NotificationError{Kind: ErrorValidation, Message: message}
}

// ConflictError builds a terminal state-disagreement failure.
func ConflictError(message string) *NotificationError {
	return &NotificationError{Kind: ErrorConflict, Message: message}
}

// AmbiguousError builds an unknown-outcome failure. The request may
// already have been accepted by the provider.
func AmbiguousError(message string) *NotificationError {
	return &NotificationError{Kind: ErrorAmbiguous, Message: message}
}

// AsNotificationError extracts a classified failure.
func AsNotificationError(err error, target **NotificationError) bool {
	return errors.As(err, target)
}

// Registry is the notification provider instance registry. It may be
// empty in production: Cloud startup must not depend on notification
// credentials.
type Registry struct {
	mu        sync.RWMutex
	providers map[ProviderKey]NotificationProvider
}

// NewRegistry returns an empty provider registry.
func NewRegistry() *Registry {
	return &Registry{providers: map[ProviderKey]NotificationProvider{}}
}

// Register adds one provider instance. Duplicate keys, nil providers
// (including typed-nil pointers inside the interface), and providers
// whose Key disagrees with the registration key fail.
func (r *Registry) Register(key ProviderKey, provider NotificationProvider) error {
	if _, err := ValidateProviderKey(string(key)); err != nil {
		return apperr.New(apperr.InvalidInput, err.Error())
	}
	if isNilProvider(provider) {
		return apperr.New(apperr.InvalidInput, fmt.Sprintf("cannot register nil provider for key %q", key))
	}
	if provider.Key() != key {
		return apperr.New(apperr.InvalidInput, fmt.Sprintf("provider key mismatch: registered %q but provider reports %q", key, provider.Key()))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[key]; exists {
		return apperr.New(apperr.Conflict, fmt.Sprintf("duplicate provider key %q", key))
	}
	r.providers[key] = provider
	return nil
}

// Get resolves one provider instance or a typed not-found error.
func (r *Registry) Get(key ProviderKey) (NotificationProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	provider, ok := r.providers[key]
	if !ok {
		return nil, apperr.New(apperr.NotFound, fmt.Sprintf("unknown provider key %q", key))
	}
	return provider, nil
}

// List returns every registered provider key in sorted order.
func (r *Registry) List() []ProviderKey {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := make([]ProviderKey, 0, len(r.providers))
	for key := range r.providers {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

// Count reports how many providers are registered.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.providers)
}

// isNilProvider detects literal nil interfaces and typed-nil concrete
// values stored inside the interface.
func isNilProvider(provider NotificationProvider) bool {
	if provider == nil {
		return true
	}
	value := reflect.ValueOf(provider)
	switch value.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Interface:
		return value.IsNil()
	default:
		return false
	}
}
