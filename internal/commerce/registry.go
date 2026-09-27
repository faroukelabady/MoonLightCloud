package commerce

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
)

// Registry is the provider instance registry. It may be empty in
// production: Phase 6A ships no real adapter and Cloud startup must not
// depend on commerce credentials.
type Registry struct {
	mu        sync.RWMutex
	providers map[ProviderKey]CommerceProvider
}

// NewRegistry returns an empty provider registry.
func NewRegistry() *Registry {
	return &Registry{providers: map[ProviderKey]CommerceProvider{}}
}

// Register adds one provider instance. Duplicate keys, nil providers
// (including typed-nil pointers inside the interface, which would
// otherwise panic on Key()), and providers whose Key disagrees with the
// registration key fail.
func (r *Registry) Register(key ProviderKey, provider CommerceProvider) error {
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
func (r *Registry) Get(key ProviderKey) (CommerceProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	provider, ok := r.providers[key]
	if !ok {
		return nil, apperr.New(apperr.NotFound, fmt.Sprintf("unknown provider key %q", key))
	}
	return provider, nil
}

// isNilProvider detects literal nil interfaces and typed-nil concrete
// values (nil pointers, maps, slices, channels, funcs) stored inside the
// interface. Calling methods on those would panic.
func isNilProvider(provider CommerceProvider) bool {
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

// UnknownProvider reports whether err is a registry unknown-key failure.
func UnknownProvider(err error) bool {
	var appErr *apperr.Error
	if errors.As(err, &appErr) {
		return appErr.Kind == apperr.NotFound
	}
	return false
}

// IsMappingConflict reports whether err is a mapping-conflict failure.
func IsMappingConflict(err error) bool {
	var appErr *apperr.Error
	if errors.As(err, &appErr) {
		return appErr.Kind == apperr.Conflict
	}
	return false
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
