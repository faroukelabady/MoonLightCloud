package orders

// DeriveOrderStore resolves the single proven Store for a reconciled
// order from mapped-line evidence, or reports mixed ownership:
//
//   - no mapped line, or every mapped line legacy: NULL (unproven);
//   - mapped lines unanimous on one proven Store: that Store;
//   - mapped lines spanning proven Stores: mixed (caller blocks).
//
// Unmapped lines (variations, empty external IDs, missing mappings) carry
// no evidence and never force attribution. A mapped line whose own
// mapping is legacy makes the whole order incomplete: a sibling's proven
// Store must not absorb it by majority.
func DeriveOrderStore(snapshot OrderSnapshot, lineStores map[string]*string) (store *string, mixed bool) {
	seen := map[string]bool{}
	proven, incomplete := 0, false
	for i := range snapshot.Lines {
		line := &snapshot.Lines[i]
		if !line.Mapped || line.MoonlightProduct == nil {
			continue
		}
		mappingStore, ok := lineStores[*line.MoonlightProduct]
		if !ok || mappingStore == nil || *mappingStore == "" {
			incomplete = true
			continue
		}
		proven++
		seen[*mappingStore] = true
		if len(seen) > 1 {
			return nil, true
		}
	}
	if len(seen) > 1 {
		return nil, true
	}
	if incomplete || proven == 0 {
		return nil, false
	}
	for store := range seen {
		owned := store
		return &owned, false
	}
	return nil, false
}

// DecideOrderStore resolves the write Store for an existing order row.
// Tombstones preserve the established Store and never block on mixed
// evidence: deletion changes lifecycle, not attribution. Live
// reconciliations block on mixed or contradictory ownership with zero
// writes; NULL adoption forces a write even when the content fingerprint
// is unchanged, so the newly proven Store becomes durable.
func DecideOrderStore(existing, derived *string, mixed, tombstone bool) (*string, bool, error) {
	if tombstone {
		if existing != nil {
			return existing, false, nil
		}
		return derived, false, nil
	}
	if mixed {
		return nil, false, &BlockedError{Code: CodeCommerceStoreScopeConflict, Message: "order lines resolve to more than one store"}
	}
	switch {
	case existing == nil && derived == nil:
		return nil, false, nil
	case existing == nil:
		return derived, true, nil
	case derived == nil:
		return existing, false, nil
	case *existing == *derived:
		return existing, false, nil
	default:
		return nil, false, &BlockedError{Code: CodeCommerceStoreScopeConflict, Message: "order owned by another store"}
	}
}

// DecideNewOrderStore resolves the write Store for a missing order row:
// unanimous derived ownership (or tombstone evidence) writes through,
// mixed live evidence blocks before any row exists.
func DecideNewOrderStore(derived *string, mixed, tombstone bool) (*string, error) {
	if !tombstone && mixed {
		return nil, &BlockedError{Code: CodeCommerceStoreScopeConflict, Message: "order lines resolve to more than one store"}
	}
	return derived, nil
}
