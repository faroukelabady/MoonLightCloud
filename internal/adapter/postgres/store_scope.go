package postgres

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// Phase 9B projection Store-scope decisions.
//
// Ownership hierarchy (frozen Phase 9A trust boundary):
//   - sync_events.store_id is derived server-side from the authenticated
//     device binding at ingress. It is the ONLY Store source for
//     projections: never current bindings, never payloads, never lookups.
//   - It is always present (ADR-0055): unbound devices cannot ingest, and
//     every projection row carries a Store.
//
// Deterministic projection-failure codes (persisted, operational).
const (
	// ErrStoreScopeConflict marks a permanent cross-Store ownership
	// collision: the aggregate is already owned by another proven Store.
	// Never overwrite, merge, or reassign; the losing event stays
	// blocked without hot-looping.
	ErrStoreScopeConflict = "STORE_SCOPE_CONFLICT"
)

// Phase 9-R1/R2 default reference-catalog identity.
//
// Retail seeds the same reference catalog (fixed UUIDs) into every fresh
// installation. Those IDs are installation-invariant *labels*, but their
// mutable state (names, status, parents) is independently managed by each
// Store. Cloud therefore treats exactly these enumerated IDs as
// Store-scoped identities: the same raw ID under different Stores maps to
// distinct current-state rows (see canonicalDefaultCategoryID). A Store's
// independent revision stream then orders only that Store's own row.
//
// This is narrow and explicit. Store-created categories/tags keep their
// global aggregate identity and cross-Store same-ID takeovers still fail
// with STORE_SCOPE_CONFLICT. Identity comes from the fixed IDs and the
// server-derived Store scope, never from mutable labels. No durable event
// bytes are rewritten.
var sharedCategoryIDs = map[string]struct{}{
	"00000000-0000-0000-0000-000000000101": {},
	"00000000-0000-0000-0000-000000000102": {},
	"00000000-0000-0000-0000-000000000201": {},
	"00000000-0000-0000-0000-000000000202": {},
	"00000000-0000-0000-0000-000000000203": {},
	"00000000-0000-0000-0000-000000000204": {},
	"00000000-0000-0000-0000-000000000301": {},
}

var sharedTagIDs = map[string]struct{}{
	"10000000-0000-0000-0000-000000000001": {},
	"10000000-0000-0000-0000-000000000002": {},
	"10000000-0000-0000-0000-000000000003": {},
	"10000000-0000-0000-0000-000000000004": {},
	"10000000-0000-0000-0000-000000000005": {},
}

// isSharedCategoryID reports whether a category identity is part of the
// shared reference catalog.
func isSharedCategoryID(id string) bool {
	_, ok := sharedCategoryIDs[id]
	return ok
}

// isSharedTagID reports whether a tag identity is part of the shared
// reference catalog.
func isSharedTagID(id string) bool {
	_, ok := sharedTagIDs[id]
	return ok
}

// defaultNamespaceUUID is the fixed namespace used to derive a Store-scoped
// canonical identity for a shared default reference ID. It is a constant
// (not a random namespace), so the mapping is stable across rebuilds,
// restarts, and every Cloud instance.
var defaultNamespaceUUID = uuid.MustParse("d3f4a1b2-5c6d-4e7f-8a90-123456789abc")

// canonicalDefaultCategoryID returns the Store-scoped projected identity
// for a default reference category: every Store maps the seeded ID to a
// deterministic per-Store UUID. Without a Store the raw ID is returned;
// such an event never projects (resolveProjectionScope blocks it).
func canonicalDefaultCategoryID(rawID string, store pgtype.UUID) string {
	if !store.Valid || !isSharedCategoryID(rawID) {
		return rawID
	}
	return uuid.NewSHA1(defaultNamespaceUUID, []byte(uuidString(store)+":category:"+rawID)).String()
}

// canonicalDefaultTagID is canonicalDefaultCategoryID for default tags.
func canonicalDefaultTagID(rawID string, store pgtype.UUID) string {
	if !store.Valid || !isSharedTagID(rawID) {
		return rawID
	}
	return uuid.NewSHA1(defaultNamespaceUUID, []byte(uuidString(store)+":tag:"+rawID)).String()
}

// storeUUID converts an ingress Store UUID string to a database value
// (invalid when absent).
func storeUUID(storeID *string) pgtype.UUID {
	if storeID == nil || *storeID == "" {
		return pgtype.UUID{}
	}
	uid, err := parseUUID(*storeID)
	if err != nil {
		return pgtype.UUID{}
	}
	return uid
}

// storeString converts a database Store back to a string pointer (nil when
// absent).
func storeString(store pgtype.UUID) *string {
	if !store.Valid {
		return nil
	}
	s := uuidString(store)
	return &s
}

// resolveProjectionScope decides Store ownership for one current-state
// projection write from the event's own ingress Store (incoming) against
// the currently projected owner (existing, invalid = no row yet).
//
//   - event without a Store: never projects (fail closed; ingress rejects
//     unbound devices, so this is a defensive guard).
//   - no existing row, or the same owner: proceed with the event's Store.
//   - a different owner: STORE_SCOPE_CONFLICT. The caller must block
//     permanently without writing.
func resolveProjectionScope(existing pgtype.UUID, incoming *string) (write pgtype.UUID, proceed bool) {
	write = storeUUID(incoming)
	if !write.Valid {
		return pgtype.UUID{}, false
	}
	if existing.Valid && uuidString(existing) != uuidString(write) {
		return pgtype.UUID{}, false
	}
	return write, true
}

// effectiveScope resolves the Store a projection carries after a write:
// the decided write value when valid, else the current owner.
func effectiveScope(write, existing pgtype.UUID) *string {
	if write.Valid {
		return storeString(write)
	}
	return storeString(existing)
}

// scopeCompatible reports whether a referenced row may back a projection
// owned by ownerStore: both Stores must be known and equal.
func scopeCompatible(ownerStore, refStore *string) bool {
	return ownerStore != nil && refStore != nil && *ownerStore != "" && *ownerStore == *refStore
}
