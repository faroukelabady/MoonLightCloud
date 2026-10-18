package postgres

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// parentScopeCompatible reports whether a row referenced by an incoming
// category ID may back a product/child effectively owned by effStore. A
// default reference ID under a Store-scoped owner resolves to that Store's
// canonical row and matches; a legacy owner treats it as the raw row; a
// Store-created ID keeps the frozen scopeCompatible gate.
func parentScopeCompatible(effStore *string, incomingID string, refStore *string) bool {
	if effStore == nil || *effStore == "" {
		return scopeCompatible(effStore, refStore)
	}
	if isSharedCategoryID(incomingID) {
		return refStore == nil || *refStore == *effStore
	}
	return scopeCompatible(effStore, refStore)
}

// tagScopeCompatible is parentScopeCompatible for tags.
func tagScopeCompatible(effStore *string, incomingID string, refStore *string) bool {
	if effStore == nil || *effStore == "" {
		return scopeCompatible(effStore, refStore)
	}
	if isSharedTagID(incomingID) {
		return refStore == nil || *refStore == *effStore
	}
	return scopeCompatible(effStore, refStore)
}

// withCategoryGraph returns v with its identity and parent set replaced by
// the physical projected IDs, so an equal-revision semantic comparison is
// not confused by the raw-vs-canonical default identity mapping.
func withCategoryGraph(v catalog.CategorySnapshot, id string, parents []string) catalog.CategorySnapshot {
	v.CategoryID = id
	v.ParentIDs = append([]string{}, parents...)
	return v
}

// withTagID is withCategoryID for tags.
func withTagID(v catalog.TagSnapshot, id string) catalog.TagSnapshot {
	v.TagID = id
	return v
}

// resolvedParent carries both the raw payload parent ID and the physical
// projection ID it resolves to for the current child's Store scope.
type resolvedParent struct {
	raw       string
	projected string
}

// resolveCategoryParents maps each incoming parent ID to the physical row
// it must reference. A default reference parent under a Store-scoped child
// resolves to that Store's canonical row (created when the parent's own
// event projected); a raw/Store-created parent resolves to itself. It
// reports parentOK=false when a parent row is not projected yet, which the
// caller turns into a retryable dependency wait.
func resolveCategoryParents(ctx context.Context, q *sqlcgen.Queries, parentIDs []string, store pgtype.UUID) ([]resolvedParent, bool, error) {
	out := make([]resolvedParent, 0, len(parentIDs))
	scopedStore := store.Valid
	for _, parent := range parentIDs {
		projected := parent
		if scopedStore && isSharedCategoryID(parent) {
			projected = canonicalDefaultCategoryID(parent, store)
		}
		puid, err := parseUUID(projected)
		if err != nil {
			return nil, false, nil
		}
		if _, err := q.CatalogCategoryByID(ctx, puid); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, false, nil
			}
			return nil, false, err
		}
		out = append(out, resolvedParent{raw: parent, projected: projected})
	}
	return out, true, nil
}

// graphRepairableEvent reports whether an accepted catalog event can still
// advance the graph: missing, pending, retry, or blocked on a transient
// graph wait. Terminally blocked events (validation, cycle, depth,
// conflict) are dead history and never repair anything.
func graphRepairableEvent(status, code string) bool {
	switch status {
	case "missing", "pending", "retry":
		return true
	case "blocked":
		return code == ErrCatalogDependencyWait || code == ErrCatalogInvalidRelation
	default:
		return false
	}
}

// graphSettledForProduct reports whether every accepted category event for
// the involved IDs is terminally settled (R03): no future graph revision
// can arrive from accepted history. Only then is CATALOG_INVALID_RELATION
// terminal; otherwise the product waits retryably.
func graphSettledForProduct(ctx context.Context, q *sqlcgen.Queries, topID string, subIDs []string) (bool, error) {
	ids := append([]string{topID}, subIDs...)
	for _, id := range ids {
		uid, err := parseUUID(id)
		if err != nil {
			return false, nil
		}
		projected := int64(-1)
		var scope pgtype.UUID
		if row, err := q.CatalogCategoryByID(ctx, uid); err == nil {
			projected = row.SourceRevision
			scope = row.StoreID
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
		events, err := q.CatalogEntityEventRevisions(ctx, sqlcgen.CatalogEntityEventRevisionsParams{
			EventType: catalog.EventCategorySnapshotV2, Column2: "category_id", Column3: rawDefaultCategoryID(id, scope),
			Processor: catalog.ProcessorCategoryProjectionV1, Column5: "catalog_revision", StoreID: scope,
		})
		if err != nil {
			return false, err
		}
		for _, event := range events {
			if event.Revision > projected && graphRepairableEvent(event.ProcessingStatus, event.LastErrorCode) {
				return false, nil
			}
		}
	}
	return true, nil
}

// categoryParentsChanged reports whether the incoming parent set differs
// from the currently projected one. A revision that only changes labels or
// status cannot orphan products, so the expensive orphan guard and product
// lock sweep are skipped for it; genuine reparents still take the full path.
func categoryParentsChanged(ctx context.Context, q *sqlcgen.Queries, cuid pgtype.UUID, newParents []string) (bool, error) {
	current, err := q.CatalogCategoryParents(ctx, cuid)
	if err != nil {
		return false, err
	}
	if len(current) != len(newParents) {
		return true, nil
	}
	cur := make([]string, 0, len(current))
	for _, p := range current {
		cur = append(cur, uuidString(p))
	}
	sort.Strings(cur)
	incoming := append([]string{}, newParents...)
	sort.Strings(incoming)
	for i := range cur {
		if cur[i] != incoming[i] {
			return true, nil
		}
	}
	return false, nil
}

// categoryChangeRepairable reports whether every projected product that
// would ACTUALLY become structurally invalid under the proposed graph has
// a newer accepted (and still repairable) product revision. The proposed
// graph replaces the changed child's outgoing-to-parent edges and keeps
// every other edge. A change that leaves all referencing products
// reachable is always repairable; only a genuine orphaning requires a
// product repair (R03 §38), so valid frozen-Retail reparents converge.
func categoryChangeRepairable(ctx context.Context, q *sqlcgen.Queries, allEdges []catalog.Edge, childID string, newParents []string) (bool, error) {
	proposed := make([]catalog.Edge, 0, len(allEdges)+len(newParents))
	for _, edge := range allEdges {
		if edge.ChildID == childID {
			continue
		}
		proposed = append(proposed, edge)
	}
	for _, parent := range newParents {
		proposed = append(proposed, catalog.Edge{ParentID: parent, ChildID: childID})
	}
	seenProducts := map[string]bool{}
	affected := catalog.CategoryDescendants(proposed, childID)
	for _, categoryID := range affected {
		uid, err := parseUUID(categoryID)
		if err != nil {
			return false, nil
		}
		productIDs, err := q.CatalogProductsReferencingCategory(ctx, uid)
		if err != nil {
			return false, err
		}
		for _, productID := range productIDs {
			pid := uuidString(productID)
			if seenProducts[pid] {
				continue
			}
			seenProducts[pid] = true
			orphaned, err := productOrphanedByGraph(ctx, q, pid, proposed)
			if err != nil {
				return false, err
			}
			if !orphaned {
				continue
			}
			productRow, err := q.CatalogProductByID(ctx, productID)
			if err != nil {
				return false, err
			}
			repairable, err := entityHasNewerRepairableEvent(ctx, q,
				catalog.EventProductSnapshotV2, "product_id", pid,
				catalog.ProcessorProductProjectionV1, "catalog_revision", productRow.StoreID)
			if err != nil {
				return false, err
			}
			if !repairable {
				// Upgrade recovery can replay the current source revision
				// solely to remap raw default references. Authorize that
				// narrow pending replay only when its canonical references
				// satisfy this proposed graph; ordinary equal conflicts
				// cannot serve as accepted repairs.
				repairable, err = defaultReferenceRepairPending(ctx, q, productRow, proposed)
				if err != nil {
					return false, err
				}
				if !repairable {
					return false, nil
				}
			}
		}
	}
	return true, nil
}

// productOrphanedByGraph reports whether a projected product would violate
// the structural rules (root top, reachable subcategories) under a
// proposed edge set. It mirrors checkProductStructure on stored state.
func productOrphanedByGraph(ctx context.Context, q *sqlcgen.Queries, productID string, edges []catalog.Edge) (bool, error) {
	pid, err := parseUUID(productID)
	if err != nil {
		return false, nil
	}
	row, err := q.CatalogProductByID(ctx, pid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	top := uuidString(row.TopCategoryID)
	if catalog.HasParents(edges, top) {
		return true, nil
	}
	reachable := catalog.ReachableFromTop(edges, top)
	subs, err := q.CatalogProductSubcategories(ctx, pid)
	if err != nil {
		return false, err
	}
	for _, sub := range subs {
		if !reachable[uuidString(sub)] {
			return true, nil
		}
	}
	return false, nil
}

// hasCatalogParents reports whether every parent ID has a projected row.
func hasCatalogParents(ctx context.Context, q *sqlcgen.Queries, parentIDs []string) (bool, error) {
	for _, parent := range parentIDs {
		uid, err := parseUUID(parent)
		if err != nil {
			return false, nil
		}
		if _, err := q.CatalogCategoryByID(ctx, uid); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}

// resetCatalogProductsForGraph re-arms waiting/graph-blocked product events
// referencing the given categories. Best-effort by design: errors are
// logged and absorbed because product backoff+rescan converges regardless.
func (d Devices) resetCatalogProductsForGraph(ctx context.Context, categoryIDs []string) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		d.logResetFailure(categoryIDs, err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	for _, id := range categoryIDs {
		if err := q.ResetCatalogProductRetriesForGraph(ctx, mustParseUUID(id)); err != nil {
			_ = tx.Rollback(ctx)
			d.logResetFailure(categoryIDs, err)
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		d.logResetFailure(categoryIDs, err)
		return
	}
}

func (d Devices) logResetFailure(categoryIDs []string, err error) {
	// Devices has no logger; the projector logs outcomes per event, and
	// convergence does not depend on this reset (see above).
	_ = categoryIDs
	_ = err
}

// structureVerdict is the outcome of structural evaluation against the
// current projected graph.
type structureVerdict int

const (
	// structureValid means the relation holds under the current graph.
	structureValid structureVerdict = iota
	// structureWait means the relation is currently invalid but accepted
	// category history may still advance the graph: retry, never terminal.
	structureWait
	// structureTerminal means the relation is invalid and every relevant
	// accepted category event is settled: terminal block.
	structureTerminal
)

// checkProductStructure evaluates top-root-ness and sub reachability
// against the current graph (R03). Active flags are deliberately not
// required (mirrors Retail retained-assignment semantics). Invalidity
// under an unsettled graph waits; invalidity under a settled graph blocks.
func (d Devices) checkProductStructure(ctx context.Context, q *sqlcgen.Queries, valid catalog.ProductSnapshot) (structureVerdict, error) {
	// Phase 17-R2 §77: product → type dependency ordering. If the product
	// references a type not yet projected, wait (the type event may still
	// arrive). Never create fake types, never drop the relation.
	if strings.TrimSpace(valid.ProductTypeID) != "" {
		tuid, err := parseUUID(valid.ProductTypeID)
		if err != nil {
			return structureTerminal, nil
		}
		if _, err := q.CatalogProductTypeByID(ctx, tuid); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return structureWait, nil
			}
			return structureWait, err
		}
	}
	edgeRows, err := q.AllCatalogCategoryEdges(ctx)
	if err != nil {
		return structureWait, err
	}
	allEdges := make([]catalog.Edge, 0, len(edgeRows))
	for _, edge := range edgeRows {
		allEdges = append(allEdges, catalog.Edge{ParentID: uuidString(edge.ParentID), ChildID: uuidString(edge.ChildID)})
	}
	if catalog.HasParents(allEdges, valid.TopCategoryID) {
		return d.settleProductStructure(ctx, q, valid, "top category is not a root")
	}
	reachable := catalog.ReachableFromTop(allEdges, valid.TopCategoryID)
	for _, sub := range valid.SubcategoryIDs {
		if !reachable[sub] {
			return d.settleProductStructure(ctx, q, valid, "subcategory not reachable from top category")
		}
	}
	return structureValid, nil
}

// settleProductStructure applies the settled rule: terminal block only
// when no accepted category event can still advance the involved graph.
func (d Devices) settleProductStructure(ctx context.Context, q *sqlcgen.Queries, valid catalog.ProductSnapshot, _ string) (structureVerdict, error) {
	settled, err := graphSettledForProduct(ctx, q, valid.TopCategoryID, valid.SubcategoryIDs)
	if err != nil {
		return structureWait, err
	}
	if !settled {
		return structureWait, nil
	}
	return structureTerminal, nil
}

// assertProductStructure maps the structural verdict to a projection
// outcome for the equal-revision-identical path (no writes needed).
func (d Devices) assertProductStructure(ctx context.Context, q *sqlcgen.Queries, tx pgx.Tx, attempt catalogAttempt, valid catalog.ProductSnapshot, count int32, now time.Time) (catalog.ProjectResult, error) {
	// Dependencies must still exist (they cannot be deleted, but defense
	// in depth never assumes it).
	if _, err := q.CatalogCategoryByID(ctx, mustParseUUID(valid.TopCategoryID)); err != nil {
		_ = tx.Rollback(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrCatalogDependencyWait, "top category not yet projected")
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "top category lookup failed", err)
	}
	verdict, err := d.checkProductStructure(ctx, q, valid)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "structure evaluation failed", err)
	}
	switch verdict {
	case structureValid:
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	case structureWait:
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrCatalogDependencyWait, "graph may still advance")
	default:
		_ = tx.Rollback(ctx)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrCatalogInvalidRelation, "product relation invalid under settled graph")
	}
}

func rawDefaultCategoryID(id string, scope pgtype.UUID) string {
	for raw := range sharedCategoryIDs {
		if canonicalDefaultCategoryID(raw, scope) == id {
			return raw
		}
	}
	return id
}

func canonicalCategorySnapshot(value catalog.NormalizedCategory, scope pgtype.UUID) catalog.NormalizedCategory {
	value.ParentIDs = append([]string{}, value.ParentIDs...)
	for i, id := range value.ParentIDs {
		value.ParentIDs[i] = canonicalDefaultCategoryID(id, scope)
	}
	return value
}

func defaultReferenceRepairPending(ctx context.Context, q *sqlcgen.Queries, row sqlcgen.CatalogProductByIDRow, edges []catalog.Edge) (bool, error) {
	if !row.StoreID.Valid {
		return false, nil
	}
	current, exists, err := currentProductSnapshot(ctx, q, row.ProductID)
	if err != nil || !exists {
		return false, err
	}
	top := canonicalDefaultCategoryID(current.TopCategoryID, row.StoreID)
	changed := top != current.TopCategoryID
	if catalog.HasParents(edges, top) {
		return false, nil
	}
	reachable := catalog.ReachableFromTop(edges, top)
	for _, sub := range current.SubcategoryIDs {
		canonical := canonicalDefaultCategoryID(sub, row.StoreID)
		changed = changed || canonical != sub
		if !reachable[canonical] {
			return false, nil
		}
	}
	if !changed {
		return false, nil
	}
	events, err := q.CatalogEntityEventRevisions(ctx, sqlcgen.CatalogEntityEventRevisionsParams{EventType: catalog.EventProductSnapshotV2, Column2: "product_id", Column3: uuidString(row.ProductID), Processor: catalog.ProcessorProductProjectionV1, Column5: "catalog_revision", StoreID: row.StoreID})
	if err != nil {
		return false, err
	}
	for _, event := range events {
		if event.EventID == row.SourceEventID && (event.ProcessingStatus == "pending" || event.ProcessingStatus == "retry") {
			return true, nil
		}
	}
	return false, nil
}

// categoryPolicyChanged reports whether the projected category state
// changed the ONLINE eligibility context of any Product (Phase 13 §42):
// the node's online policy or its parent set. Names/labels, status and
// parent order are deliberately excluded — a rename must never
// republish/suppress Products, and pure reordering is not a policy
// change (set semantics). An inserted node always counts as a change.
func categoryPolicyChanged(previous catalog.NormalizedCategory, prevFound bool, next catalog.CategorySnapshot, resolvedParents []string) bool {
	if !prevFound {
		return true
	}
	if previous.OnlineEnabled != next.OnlineEnabled {
		return true
	}
	return !sameParentSet(previous.ParentIDs, resolvedParents)
}

// sameParentSet compares parent sets independent of order.
func sameParentSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]int, len(a))
	for _, id := range a {
		set[id]++
	}
	for _, id := range b {
		set[id]--
		if set[id] < 0 {
			return false
		}
	}
	return true
}

// productClassificationChanged reports whether the projected product
// changed its classification (top category or subcategory set) — the
// only Product-side trigger for effective-online re-evaluation (Phase 13
// §42). Comparison uses resolved (canonical) identities, and subcategory
// order is not semantic (set equality). Metadata-only edits never fan
// out: publication keeps its frozen manual cadence for those.
func productClassificationChanged(previous catalog.NormalizedProduct, prevFound bool, topCategoryID string, subcategoryIDs []string) bool {
	if !prevFound {
		return true
	}
	if previous.TopCategoryID != topCategoryID {
		return true
	}
	return !sameParentSet(previous.SubcategoryIDs, subcategoryIDs)
}
