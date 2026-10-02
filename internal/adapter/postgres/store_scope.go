package postgres

import (
	"github.com/jackc/pgx/v5/pgtype"
)

// Phase 9B projection Store-scope decisions.
//
// Ownership hierarchy (frozen Phase 9A trust boundary):
//   - sync_events.store_id is derived server-side from the authenticated
//     device binding at ingress. It is the ONLY Store source for
//     projections: never current bindings, never payloads, never lookups.
//   - NULL means unscoped legacy (ownership not proven) and stays
//     truthful: historical rows are never backfilled from membership.
//
// Deterministic projection-failure codes (persisted, operational).
const (
	// ErrStoreScopeConflict marks a permanent cross-Store ownership
	// collision: the aggregate is already owned by another proven Store.
	// Never overwrite, merge, or reassign; the losing event stays
	// blocked without hot-looping.
	ErrStoreScopeConflict = "STORE_SCOPE_CONFLICT"
	// ErrLegacyScopeAmbiguous marks a scoped event whose target
	// historical row is unscoped legacy with no device-provenance
	// continuity: attributing it to any Store would invent ownership.
	ErrLegacyScopeAmbiguous = "LEGACY_SCOPE_AMBIGUOUS"
)

// Phase 9-R1 F04 shared default catalog identity.
//
// Retail seeds the same reference catalog (fixed UUIDs) into every fresh
// installation. Those identities are deliberately installation-invariant
// reference data, not Store-owned aggregates: "Islamic", "Cats",
// "tutankhamun" mean the same thing in every shop. Cloud therefore treats
// exactly these enumerated IDs as a shared reference namespace (owned by
// no Store); any Store may reference them and they never collide.
//
// This is narrow and explicit. Store-created categories/tags keep their
// per-event Store ownership and cross-Store same-ID takeovers still fail
// with STORE_SCOPE_CONFLICT. Identity is derived from the fixed IDs, never
// from mutable labels. No durable event bytes are rewritten.
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

// storeUUID converts an ingress Store UUID string (nil = legacy) to a
// nullable database value.
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

// storeString converts a nullable database Store back to a string pointer
// (nil = unscoped legacy).
func storeString(store pgtype.UUID) *string {
	if !store.Valid {
		return nil
	}
	s := uuidString(store)
	return &s
}

// resolveProjectionScope decides Store ownership for one current-state
// projection write from the event's own ingress context (incoming, nil =
// legacy/unscoped) against the currently projected owner (existing,
// invalid = missing row or NULL legacy row).
//
//   - legacy event: no scope claim; proceed and let COALESCE upserts
//     preserve whatever ownership exists.
//   - scoped event with no proven owner: proceed, adopting the ingress
//     Store (one-time legacy adoption by aggregate-ID + revision
//     continuity under the existing arbitration model).
//   - scoped event with the same proven owner: proceed.
//   - scoped event with a different proven owner: STORE_SCOPE_CONFLICT.
//     The caller must block permanently without writing.
//
// Historical (immutable) projections must NOT use adoption: callers pass
// only real owners there, so any mismatch is a conflict.
func resolveProjectionScope(existing pgtype.UUID, incoming *string) (write pgtype.UUID, proceed bool) {
	if incoming == nil || *incoming == "" {
		return pgtype.UUID{}, true
	}
	write = storeUUID(incoming)
	if !write.Valid {
		return pgtype.UUID{}, true
	}
	if !existing.Valid {
		return write, true
	}
	if uuidString(existing) == uuidString(write) {
		return write, true
	}
	return pgtype.UUID{}, false
}

// effectiveScope resolves the Store a projection would carry after a
// write: the decided write value when valid, else the current owner,
// else nil (fully legacy). Relation checks use it so legacy events and
// rows stay wildcards while proven Stores must match exactly.
func effectiveScope(write, existing pgtype.UUID) *string {
	if write.Valid {
		return storeString(write)
	}
	return storeString(existing)
}

// scopeCompatible reports whether a referenced row may back a projection
// owned (effectively) by ownerStore. NULL on either side is the legacy
// wildcard: unscoped rows never force and never block scope. Two proven
// Stores must match exactly.
func scopeCompatible(ownerStore, refStore *string) bool {
	if ownerStore == nil || *ownerStore == "" {
		return true
	}
	if refStore == nil || *refStore == "" {
		return true
	}
	return *ownerStore == *refStore
}
