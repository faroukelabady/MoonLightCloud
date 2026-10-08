package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// Catalog projector Store implementation on Devices (shared pool +
// timeouts). Revision ordering, dependency waits, and DAG defense follow
// ADR-0028; the claim/lock/mark machinery is the frozen generic one.

// Deterministic catalog-projection failure codes (persisted, operational).
const (
	ErrCatalogDependencyWait   = "CATALOG_DEPENDENCY_WAIT"
	ErrCatalogRevisionConflict = "CATALOG_REVISION_CONFLICT"
	ErrCatalogCategoryCycle    = "CATALOG_CATEGORY_CYCLE"
	ErrCatalogCategoryDepth    = "CATALOG_CATEGORY_DEPTH"
	ErrCatalogInvalidRelation  = "CATALOG_INVALID_RELATION"
	ErrCatalogGraphConflict    = "CATALOG_GRAPH_CONFLICT"
)

// PendingCatalogEvents returns due candidate catalog event IDs for one
// processor (durable lazy discovery: accepted events with no processing
// row are discoverable, so pre-projector history backfills with no resend).
func (d Devices) PendingCatalogEvents(ctx context.Context, processor, eventType string, limit int) ([]string, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).PendingCatalogEvents(ctx, sqlcgen.PendingCatalogEventsParams{
		Processor: processor, EventType: eventType, Limit: int32(limit),
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "scan pending catalog events", redact(err))
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, uuidString(r))
	}
	return out, nil
}

// NextCatalogRetry reports the earliest future durable retry time for one
// processor; found=false when no retry is scheduled.
func (d Devices) NextCatalogRetry(ctx context.Context, processor, eventType string) (time.Time, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	next, err := sqlcgen.New(d.pool).NextCatalogRetryAt(ctx, sqlcgen.NextCatalogRetryAtParams{
		Processor: processor, EventType: eventType,
	})
	if err != nil {
		return time.Time{}, false, apperr.Wrap(apperr.Internal, "scan next catalog retry", redact(err))
	}
	if !next.Valid {
		return time.Time{}, false, nil
	}
	return next.Time, true, nil
}

// LoadCatalogEvent loads the immutable inbox row for catalog projection.
func (d Devices) LoadCatalogEvent(ctx context.Context, eventID string) (catalog.EventRecord, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(eventID)
	if err != nil {
		return catalog.EventRecord{}, false, apperr.New(apperr.InvalidInput, "event_id must be a UUID")
	}
	row, err := sqlcgen.New(d.pool).SaleEventByID(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.EventRecord{}, false, nil
		}
		return catalog.EventRecord{}, false, apperr.Wrap(apperr.Internal, "load event", redact(err))
	}
	rec := catalog.EventRecord{
		EventID: uuidString(row.EventID), DeviceID: uuidString(row.DeviceID),
		EventType:  row.EventType,
		OccurredAt: row.OccurredAt.Time, ReceivedAt: row.ReceivedAt.Time,
		Payload: json.RawMessage(row.Payload),
	}
	rec.StoreID = storeString(row.StoreID)
	if row.CredentialID.Valid {
		c := uuidString(row.CredentialID)
		rec.CredentialID = &c
	}
	return rec, true, nil
}

// catalogAttempt is the per-event mutable projection state threaded through
// the three entity projectors.
type catalogAttempt struct {
	euid    pgtype.UUID
	duid    pgtype.UUID
	now     time.Time
	payload []byte
}

// startCatalogAttempt parses identity outside any transaction. UUID parse
// failures are deterministic validation blocks, never panics.
func startCatalogAttempt(event catalog.EventRecord, now time.Time) (catalogAttempt, *catalog.ProjectResult) {
	euid, err := parseUUID(event.EventID)
	if err != nil {
		return catalogAttempt{}, &catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: ErrValidation}
	}
	duid, err := parseUUID(event.DeviceID)
	if err != nil {
		return catalogAttempt{}, &catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: ErrValidation}
	}
	return catalogAttempt{euid: euid, duid: duid, now: now, payload: event.Payload}, nil
}

// revisionGate compares the incoming revision against current projection
// state. Higher wins; stale is a terminal no-op. Equal revisions resolve
// by semantic reconstruction comparison (R02), never by stored hash:
// candidate databases may hold raw-byte hashes, so the stored hash is
// informational only and never decides the verdict.
func revisionGate(incomingRevision, storedRevision int64) (proceed bool, stale bool) {
	if incomingRevision > storedRevision {
		return true, false
	}
	if incomingRevision < storedRevision {
		return false, true
	}
	return false, false
}

// adoptingStore reports whether write would establish or change proven
// Store ownership for the aggregate. This is the only transition that can
// create a new contradictory relationship; a same-Store reaffirmation is
// not a transition and a legacy event adopts nothing.
func adoptingStore(existing, write pgtype.UUID) bool {
	return write.Valid && uuidString(existing) != uuidString(write)
}

// storeDependentsCompatible reports whether every proven dependency Store
// agrees with the adopting write Store. NULL (legacy) dependents are
// wildcards: they neither authorize nor block a proven scope. A single
// proven foreign dependent makes the adoption conflict so no durable
// relationship can connect contradictory proven Store ownership.
func storeDependentsCompatible(write pgtype.UUID, dependents []pgtype.UUID) bool {
	if !write.Valid {
		return true
	}
	want := uuidString(write)
	for _, s := range dependents {
		if s.Valid && uuidString(s) != want {
			return false
		}
	}
	return true
}

// lockCatalogEntities serializes same-entity projection decisions across
// Cloud instances (R04): one PostgreSQL advisory transaction-scoped lock
// per (entity_type, entity_id), always acquired in globally sorted key
// order ("category:" sorts before "product:" sorts before "tag:", then by
// ID), so concurrent transactions can never form a lock-wait cycle.
// Different entities proceed in parallel. Locks release automatically at
// commit/rollback. Callers must hold these before reading the revision
// gate inputs they decide on. Combined with SERIALIZABLE transactions
// (see beginCatalogTx), any residual interleaving aborts instead of
// corrupting: aborts map to transient retry, never to terminal states.
func lockCatalogEntities(ctx context.Context, q *sqlcgen.Queries, keys ...[2]string) error {
	ordered := append([][2]string{}, keys...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i][0] != ordered[j][0] {
			return ordered[i][0] < ordered[j][0]
		}
		return ordered[i][1] < ordered[j][1]
	})
	seen := map[[2]string]bool{}
	for _, key := range ordered {
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := q.LockCatalogEntity(ctx, sqlcgen.LockCatalogEntityParams{Column1: key[0], Column2: key[1]}); err != nil {
			return err
		}
	}
	return nil
}

// beginCatalogTx opens a SERIALIZABLE projection transaction. Serializable
// isolation is the backstop behind advisory entity locks: any interleaving
// the locks do not serialize fails with SQLSTATE 40001 instead of
// committing a wrong revision, and 40001 maps to transient retry.
func (d Devices) beginCatalogTx(ctx context.Context) (pgx.Tx, error) {
	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, transient(redact(err))
	}
	return tx, nil
}

// isSerializationFailure reports SQLSTATE 40001 (or a transaction aborted
// by one): always transient, never a terminal verdict.
func isSerializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001" || pgErr.Code == "40P01"
	}
	return false
}

// currentCategorySnapshot reconstructs the normalized semantic state of a
// projected category for equal-revision comparison.
func currentCategorySnapshot(ctx context.Context, q *sqlcgen.Queries, cuid pgtype.UUID) (catalog.NormalizedCategory, bool, error) {
	row, err := q.CatalogCategoryByID(ctx, cuid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.NormalizedCategory{}, false, nil
		}
		return catalog.NormalizedCategory{}, false, err
	}
	parents, err := q.CatalogCategoryParents(ctx, cuid)
	if err != nil {
		return catalog.NormalizedCategory{}, false, err
	}
	names := []catalog.CatalogName{{Locale: catalog.LocaleAR, Name: row.NameAr}}
	if row.NameEn.Valid {
		names = append(names, catalog.CatalogName{Locale: catalog.LocaleEN, Name: row.NameEn.String})
	}
	parentIDs := make([]string, 0, len(parents))
	for _, parent := range parents {
		parentIDs = append(parentIDs, uuidString(parent))
	}
	return catalog.NormalizeCategorySnapshot(catalog.CategorySnapshot{
		CategoryID: uuidString(row.CategoryID), Status: row.Status,
		OnlineEnabled: row.OnlineEnabled,
		Names:         names, ParentIDs: parentIDs, CatalogRevision: row.SourceRevision,
	}), true, nil
}

// currentTagSnapshot reconstructs the normalized semantic state of a
// projected tag.
func currentTagSnapshot(ctx context.Context, q *sqlcgen.Queries, tuid pgtype.UUID) (catalog.NormalizedTag, bool, error) {
	row, err := q.CatalogTagByID(ctx, tuid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.NormalizedTag{}, false, nil
		}
		return catalog.NormalizedTag{}, false, err
	}
	var names []catalog.CatalogName
	if row.NameAr.Valid {
		names = append(names, catalog.CatalogName{Locale: catalog.LocaleAR, Name: row.NameAr.String})
	}
	if row.NameEn.Valid {
		names = append(names, catalog.CatalogName{Locale: catalog.LocaleEN, Name: row.NameEn.String})
	}
	return catalog.NormalizeTagSnapshot(catalog.TagSnapshot{
		TagID: uuidString(row.TagID), Slug: row.Slug, IsActive: row.IsActive,
		Names: names, CatalogRevision: row.SourceRevision,
	}), true, nil
}

// currentProductSnapshot reconstructs the normalized semantic state of a
// projected product.
func currentProductSnapshot(ctx context.Context, q *sqlcgen.Queries, puid pgtype.UUID) (catalog.NormalizedProduct, bool, error) {
	row, err := q.CatalogProductByID(ctx, puid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.NormalizedProduct{}, false, nil
		}
		return catalog.NormalizedProduct{}, false, err
	}
	prices, err := q.CatalogProductPrices(ctx, puid)
	if err != nil {
		return catalog.NormalizedProduct{}, false, err
	}
	translations, err := q.CatalogProductTranslations(ctx, puid)
	if err != nil {
		return catalog.NormalizedProduct{}, false, err
	}
	subs, err := q.CatalogProductSubcategories(ctx, puid)
	if err != nil {
		return catalog.NormalizedProduct{}, false, err
	}
	tags, err := q.CatalogProductTags(ctx, puid)
	if err != nil {
		return catalog.NormalizedProduct{}, false, err
	}
	snapshot := catalog.ProductSnapshot{
		ProductID: uuidString(row.ProductID), ProductTypeID: uuidString(row.ProductTypeID), Name: row.Name,
		TopCategoryID: uuidString(row.TopCategoryID), IsActive: row.IsActive,
		CatalogRevision: row.SourceRevision,
	}
	if row.Description.Valid {
		desc := row.Description.String
		snapshot.Description = &desc
	}
	if row.WidthCm.Valid {
		width := int(row.WidthCm.Int32)
		snapshot.WidthCM = &width
	}
	if row.HeightCm.Valid {
		height := int(row.HeightCm.Int32)
		snapshot.HeightCM = &height
	}
	for _, price := range prices {
		entry := catalog.CatalogPrice{Currency: price.Currency, PriceCents: price.PriceMinor}
		if price.CostMinor.Valid {
			cost := price.CostMinor.Int64
			entry.CostCents = &cost
		}
		snapshot.Prices = append(snapshot.Prices, entry)
	}
	for _, translation := range translations {
		entry := catalog.CatalogProductTranslation{Locale: translation.Locale, Name: translation.Name}
		if translation.Description.Valid {
			desc := translation.Description.String
			entry.Description = &desc
		}
		snapshot.Translations = append(snapshot.Translations, entry)
	}
	for _, sub := range subs {
		snapshot.SubcategoryIDs = append(snapshot.SubcategoryIDs, uuidString(sub))
	}
	for _, tag := range tags {
		snapshot.TagIDs = append(snapshot.TagIDs, uuidString(tag))
	}
	return catalog.NormalizeProductSnapshot(snapshot), true, nil
}

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
			EventType: catalog.EventCategorySnapshotV1, Column2: "category_id", Column3: rawDefaultCategoryID(id, scope),
			Processor: catalog.ProcessorCategoryProjectionV1, Column5: "catalog_revision", StoreID: scope, StoreScopedDefault: defaultCatalogEntity(catalog.EventCategorySnapshotV1, rawDefaultCategoryID(id, scope)),
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

// categoryDescendants returns the changed child plus its transitive
// descendants in the current graph. Product validity can change for
// products referencing any of these (direct reference, or reachability
// through them), so all of them scope locks, orphan checks, and resets.
func categoryDescendants(allEdges []catalog.Edge, childID string) []string {
	children := map[string][]string{}
	for _, edge := range allEdges {
		children[edge.ParentID] = append(children[edge.ParentID], edge.ChildID)
	}
	seen := map[string]bool{childID: true}
	out := []string{childID}
	queue := []string{childID}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		for _, child := range children[node] {
			if !seen[child] {
				seen[child] = true
				out = append(out, child)
				queue = append(queue, child)
			}
		}
	}
	sort.Strings(out)
	return out
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
	affected := categoryDescendants(proposed, childID)
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
				catalog.EventProductSnapshotV1, "product_id", pid,
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
	if hasParents(edges, top) {
		return true, nil
	}
	reachable := reachableFromTop(edges, top)
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

// entitySuperseded reports whether a newer accepted event for the entity
// could still land (R03 noise rule): an older revision that is already
// superseded resolves as a stale no-op instead of a terminal block, while
// the newer event is judged on its own merits. Terminally dead newer
// events (validation/conflict/cycle/depth) do not supersede: the older
// revision is then evaluated normally as the closest valid state.
func entitySuperseded(ctx context.Context, q *sqlcgen.Queries, eventType, idKey, entityID, processor, revKey string, incomingRevision int64, scope pgtype.UUID) (bool, error) {
	events, err := q.CatalogEntityEventRevisions(ctx, sqlcgen.CatalogEntityEventRevisionsParams{
		EventType: eventType, Column2: idKey, Column3: entityID,
		Processor: processor, Column5: revKey, StoreID: scope, StoreScopedDefault: defaultCatalogEntity(eventType, entityID),
	})
	if err != nil {
		return false, err
	}
	for _, event := range events {
		if event.Revision > incomingRevision && graphRepairableEvent(event.ProcessingStatus, event.LastErrorCode) {
			return true, nil
		}
	}
	return false, nil
}

// entityHasNewerRepairableEvent generalizes the repair check across catalog
// entity types: a newer accepted event that is missing, pending, retrying,
// or blocked on a transient graph wait can still advance the entity.
func entityHasNewerRepairableEvent(ctx context.Context, q *sqlcgen.Queries, eventType, idKey, entityID, processor, revKey string, scope pgtype.UUID) (bool, error) {
	projectedID := entityID
	if eventType == catalog.EventCategorySnapshotV1 || eventType == catalog.EventCategorySnapshotV2 {
		projectedID = canonicalDefaultCategoryID(entityID, scope)
	}
	if eventType == catalog.EventTagSnapshotV1 {
		projectedID = canonicalDefaultTagID(entityID, scope)
	}
	uid, err := parseUUID(projectedID)
	if err != nil {
		return false, nil
	}
	projected := int64(-1)
	switch eventType {
	case catalog.EventProductSnapshotV1:
		if row, err := q.CatalogProductByID(ctx, uid); err == nil {
			projected = row.SourceRevision
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
	case catalog.EventCategorySnapshotV1, catalog.EventCategorySnapshotV2:
		if row, err := q.CatalogCategoryByID(ctx, uid); err == nil {
			projected = row.SourceRevision
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
	case catalog.EventTagSnapshotV1:
		if row, err := q.CatalogTagByID(ctx, uid); err == nil {
			projected = row.SourceRevision
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
	default:
		return false, nil
	}
	events, err := q.CatalogEntityEventRevisions(ctx, sqlcgen.CatalogEntityEventRevisionsParams{
		EventType: eventType, Column2: idKey, Column3: entityID,
		Processor: processor, Column5: revKey, StoreID: scope, StoreScopedDefault: defaultCatalogEntity(eventType, entityID),
	})
	if err != nil {
		return false, err
	}
	for _, event := range events {
		if event.Revision > projected && graphRepairableEvent(event.ProcessingStatus, event.LastErrorCode) {
			return true, nil
		}
	}
	return false, nil
}

// claimCatalogRow runs Claim+Lock and honors terminal/not-due states. It
// returns the claim for attempt counting; on terminal states it commits
// the (unchanged) outcome directly.
func claimCatalogRow(ctx context.Context, q *sqlcgen.Queries, tx pgx.Tx, processor string, euid pgtype.UUID, now time.Time) (attempt int32, done catalog.ProjectResult, hasDone bool, err error) {
	if err := q.ClaimProcessing(ctx, sqlcgen.ClaimProcessingParams{
		EventID: euid, Processor: processor,
	}); err != nil {
		return 0, catalog.ProjectResult{}, false, err
	}
	claim, err := q.LockProcessing(ctx, sqlcgen.LockProcessingParams{
		EventID: euid, Processor: processor,
	})
	if err != nil {
		return 0, catalog.ProjectResult{}, false, err
	}
	switch claim.Status {
	case catalog.ProcProcessed:
		return 0, catalog.ProjectResult{Outcome: catalog.OutcomeProcessed}, true, commitTx(ctx, tx)
	case catalog.ProcBlocked:
		return 0, catalog.ProjectResult{Outcome: catalog.OutcomeBlocked}, true, commitTx(ctx, tx)
	}
	if claim.Status == catalog.ProcRetry && claim.NextAttemptAt.Valid && claim.NextAttemptAt.Time.After(now) {
		return 0, catalog.ProjectResult{Outcome: catalog.OutcomeNotDue}, true, commitTx(ctx, tx)
	}
	return claim.AttemptCount, catalog.ProjectResult{}, false, nil
}

// finishCatalogAttempt marks the terminal outcome and commits.
func finishCatalogAttempt(ctx context.Context, q *sqlcgen.Queries, tx pgx.Tx, processor string, euid pgtype.UUID, attempt int32, now time.Time, status, code, msg string) (catalog.ProjectResult, error) {
	if err := q.MarkProcessing(ctx, sqlcgen.MarkProcessingParams{
		EventID: euid, Processor: processor,
		Status: status, AttemptCount: attempt + 1,
		LastAttemptAt: pgTime(now), NextAttemptAt: pgTimePtr(nil),
		ProcessedAt:   pgTimePtr(nilIf(status != catalog.ProcProcessed, now)),
		LastErrorCode: pgText(code), LastErrorMessage: pgText(boundMsg(msg)),
	}); err != nil {
		return catalog.ProjectResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return catalog.ProjectResult{}, err
	}
	res := catalog.ProjectResult{ErrorCode: code}
	switch status {
	case catalog.ProcProcessed:
		res.Outcome = catalog.OutcomeProcessed
	case catalog.ProcBlocked:
		res.Outcome = catalog.OutcomeBlocked
	default:
		res.Outcome = catalog.OutcomeRetryable
	}
	return res, nil
}

func nilIf(cond bool, t time.Time) *time.Time {
	if cond {
		return nil
	}
	return &t
}

// markCatalogBlocked records a deterministic blocked outcome in one short
// transaction. Zero projection writes precede it.
func (d Devices) markCatalogBlocked(ctx context.Context, processor string, euid pgtype.UUID, now time.Time, code, msg string) (catalog.ProjectResult, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return catalog.ProjectResult{}, transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	attempt, done, hasDone, err := claimCatalogRow(ctx, q, tx, processor, euid, now)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, processor, euid, now, ErrProjection, "claim failed", err)
	}
	if hasDone {
		if done.Outcome == catalog.OutcomeProcessed {
			return done, nil
		}
		return catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: code}, nil
	}
	_ = attempt
	return finishCatalogAttempt(ctx, q, tx, processor, euid, attempt, now, catalog.ProcBlocked, code, msg)
}

// catalogRetryDelay keeps dependency waits and proven transaction contention
// responsive without treating arbitrary database failures as local contention.
// The durable deadline prevents hot retry loops; all other failures retain the
// frozen outage backoff. Original driver errors are used only for classification.
func catalogRetryDelay(attempt int, code string, causes ...error) time.Duration {
	delay := catalog.Backoff(attempt)
	local := code == ErrCatalogDependencyWait
	for _, cause := range causes {
		local = local || isSerializationFailure(cause)
	}
	if local && delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
}

// persistCatalogRetry writes retry state in a SEPARATE durable transaction
// after the projection transaction rolled back, mirroring the sale/return
// discipline: the schedule commits even though the projection did not.
func (d Devices) persistCatalogRetry(ctx context.Context, processor string, euid pgtype.UUID, now time.Time, code, msg string, causes ...error) (catalog.ProjectResult, error) {
	// The returned error keeps its frozen exact shape (the F12 retry
	// classifier contracts on it); the branch detail is persisted in
	// sync_event_processing.last_error_message for diagnostics.
	fail := func() (catalog.ProjectResult, error) {
		return catalog.ProjectResult{Outcome: catalog.OutcomeRetryable, ErrorCode: code},
			transient(errors.New("catalog projection transient failure"))
	}
	ctx2, cancel := d.ctx(context.Background())
	defer cancel()
	tx, err := d.pool.Begin(ctx2)
	if err != nil {
		return fail()
	}
	defer func() { _ = tx.Rollback(ctx2) }()
	q := sqlcgen.New(tx)
	attempt, done, hasDone, err := claimCatalogRow(ctx2, q, tx, processor, euid, now)
	if err != nil {
		return fail()
	}
	if hasDone {
		if done.Outcome == catalog.OutcomeBlocked {
			return catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: code}, nil
		}
		return done, nil
	}
	next := now.Add(catalogRetryDelay(int(attempt), code, causes...))
	if err := q.MarkProcessing(ctx2, sqlcgen.MarkProcessingParams{
		EventID: euid, Processor: processor,
		Status: catalog.ProcRetry, AttemptCount: attempt + 1,
		LastAttemptAt: pgTime(now), NextAttemptAt: pgTime(next),
		ProcessedAt:   pgtype.Timestamptz{},
		LastErrorCode: pgText(code), LastErrorMessage: pgText(boundMsg(msg)),
	}); err != nil {
		return fail()
	}
	if err := tx.Commit(ctx2); err != nil {
		return fail()
	}
	return fail()
}

// RearmBlockedProducts implements the durable R2 fallback for the product
// projector: graph-dependent blocked rows whose involved graph has
// advanced since the block decision return to pending. Pure processing
// state transition (no projection writes); the subsequent attempt takes
// the normal entity locks and revision gate, so serialization, staleness,
// and atomicity guarantees are unchanged. Never touches retry rows,
// other processors, or other error codes.
func (d Devices) RearmBlockedProducts(ctx context.Context) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	if err := sqlcgen.New(d.pool).RearmBlockedCatalogProducts(ctx); err != nil {
		return apperr.Wrap(apperr.Internal, "re-arm blocked catalog products", redact(err))
	}
	return nil
}

// RecoverDefaultCatalogBlocked re-arms at most 100 authoritative source
// events for missing canonical defaults and raw current-state references.
// Obsolete blocked defaults require concrete foreign ownership or same-Store
// raw slug-collision provenance; an error code alone never authorizes reset.
// Repeat after catalog workers converge until zero. Durable processing rows
// make interruption restart-safe.
func (d Devices) RecoverDefaultCatalogBlocked(ctx context.Context) (int64, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	// Serialize provenance selection with projection state changes. A race
	// aborts the whole reset batch rather than rearming a newly genuine conflict.
	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return 0, apperr.Wrap(apperr.Unavailable, "catalog recovery unavailable", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	categories := []string{}
	for id := range sharedCategoryIDs {
		categories = append(categories, id)
	}
	tags := []string{}
	for id := range sharedTagIDs {
		tags = append(tags, id)
	}
	candidates, err := q.DefaultCatalogRecoveryCandidates(ctx, sqlcgen.DefaultCatalogRecoveryCandidatesParams{CategoryIds: categories, TagIds: tags})
	if err != nil {
		return 0, apperr.Wrap(apperr.Internal, "catalog recovery lookup failed", redact(err))
	}
	var n int64
	for _, candidate := range candidates {
		count, err := q.RearmDefaultCatalogRecovery(ctx, sqlcgen.RearmDefaultCatalogRecoveryParams{EventID: candidate.EventID, Processor: candidate.Processor})
		if err != nil {
			return 0, apperr.Wrap(apperr.Internal, "catalog recovery failed", redact(err))
		}
		n += count
	}
	if n < 100 {
		types, err := q.SharedProductTypeRecoveryCandidates(ctx, sqlcgen.SharedProductTypeRecoveryCandidatesParams{SeedID: mustProductTypeUUID(sharedProductTypeID), BatchLimit: int32(100 - n)})
		if err != nil {
			return 0, apperr.Wrap(apperr.Internal, "type recovery lookup failed", redact(err))
		}
		for _, candidate := range types {
			count, err := q.RearmDefaultCatalogRecovery(ctx, sqlcgen.RearmDefaultCatalogRecoveryParams{EventID: candidate.EventID, Processor: candidate.Processor})
			if err != nil {
				return 0, apperr.Wrap(apperr.Internal, "type recovery failed", redact(err))
			}
			n += count
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, apperr.Wrap(apperr.Unavailable, "catalog recovery unavailable", redact(err))
	}
	return n, nil
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

// Graph violation sentinels: cycles and depth excess are distinct safe
// diagnostics (R07), both terminal.
var (
	ErrGraphCycle = errors.New("category cycle")
	ErrGraphDepth = errors.New("category depth exceeded")
)

// checkCategoryGraph validates the replaced edge set for one child: no
// self edge (already enforced at ingestion, rechecked defensively),
// acyclic, and every root→leaf path at most maxCatalogDepthNodes nodes
// (mirrors the Retail max-3-node DAG invariant as a global property).
func checkCategoryGraph(allEdges []catalog.Edge, childID string, newParents []string) error {
	const maxCatalogDepthNodes = 3
	children := map[string]map[string]bool{}
	edgeSet := map[[2]string]bool{}
	add := func(parent, child string) {
		if parent == child {
			return
		}
		if children[parent] == nil {
			children[parent] = map[string]bool{}
		}
		if !children[parent][child] {
			children[parent][child] = true
			edgeSet[[2]string{parent, child}] = true
		}
	}
	for _, edge := range allEdges {
		if edge.ChildID == childID {
			continue
		}
		add(edge.ParentID, edge.ChildID)
	}
	for _, parent := range newParents {
		if parent == childID {
			return ErrGraphCycle
		}
		add(parent, childID)
	}
	// Iterative DFS from every node: cycle detection + longest path.
	const visiting = 1
	const done = 2
	state := map[string]int{}
	depth := map[string]int{}
	var visit func(node string, stack int) error
	visit = func(node string, stack int) error {
		if stack > 10000 {
			return ErrGraphDepth
		}
		switch state[node] {
		case done:
			return nil
		case visiting:
			return ErrGraphCycle
		}
		state[node] = visiting
		longest := 1
		for child := range children[node] {
			if err := visit(child, stack+1); err != nil {
				return err
			}
			if depth[child]+1 > longest {
				longest = depth[child] + 1
			}
		}
		if longest > maxCatalogDepthNodes {
			return ErrGraphDepth
		}
		depth[node] = longest
		state[node] = done
		return nil
	}
	nodes := map[string]bool{childID: true}
	for pair := range edgeSet {
		nodes[pair[0]] = true
		nodes[pair[1]] = true
	}
	for node := range nodes {
		if state[node] == done {
			continue
		}
		if err := visit(node, 0); err != nil {
			return err
		}
	}
	return nil
}

// reachableFromTop reports the descendant set of top over current edges
// (top itself excluded, mirroring the Retail reachableFrom helper).
func reachableFromTop(allEdges []catalog.Edge, topID string) map[string]bool {
	children := map[string][]string{}
	for _, edge := range allEdges {
		children[edge.ParentID] = append(children[edge.ParentID], edge.ChildID)
	}
	reached := map[string]bool{}
	queue := append([]string{}, children[topID]...)
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		if reached[node] {
			continue
		}
		reached[node] = true
		queue = append(queue, children[node]...)
	}
	return reached
}

// hasParents reports whether a category currently has any parent edge.
func hasParents(allEdges []catalog.Edge, categoryID string) bool {
	for _, edge := range allEdges {
		if edge.ChildID == categoryID {
			return true
		}
	}
	return false
}

// ProjectCategory projects one category snapshot with revision ordering,
// parent dependency waits, and DAG defense. Either a complete category
// projection commits or nothing does.
func (d Devices) ProjectCategory(ctx context.Context, event catalog.EventRecord, now time.Time) (catalog.ProjectResult, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	attempt, blocked := startCatalogAttempt(event, now)
	if blocked != nil {
		euid, _ := parseUUID(event.EventID)
		return d.markCatalogBlocked(ctx, catalog.ProcessorCategoryProjectionV1, euid, now, blocked.ErrorCode, "event identity must be UUIDs")
	}
	// Phase 13 §28: dual-version support. v1 is immutable history and
	// normalizes to online_enabled = true (exact pre-Phase13 semantics);
	// v2 carries the ONLINE channel policy as required semantic state.
	var raw catalog.CategorySnapshot
	var derr error
	if event.EventType == catalog.EventCategorySnapshotV2 {
		raw, derr = catalog.DecodeCategorySnapshotV2(attempt.payload)
	} else {
		raw, derr = catalog.DecodeCategorySnapshot(attempt.payload)
	}
	if derr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrValidation, safeErr(derr))
	}
	valid, verr := catalog.ValidateCategorySnapshot(raw)
	if verr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrValidation, safeErr(verr))
	}
	// Phase 9-R2: a default reference category is a Store-scoped identity.
	// The server-derived ingress Store determines which physical row this
	// event addresses, so independent Stores never arbitrate one mutable
	// row with unrelated local revision counters. Legacy (unbound) events
	// keep the raw seeded identity so pre-existing global rows stay
	// addressable. Only the *raw* payload ID is tested for default
	// membership; the canonical ID is deliberately outside that set.
	sharedNode := isSharedCategoryID(valid.CategoryID) && event.StoreID != nil && *event.StoreID != ""
	eventWriteStore := storeUUID(event.StoreID)
	projectedID := valid.CategoryID
	if sharedNode {
		projectedID = canonicalDefaultCategoryID(valid.CategoryID, eventWriteStore)
	}
	cuid, err := parseUUID(projectedID)
	if err != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrValidation, "category_id must be a UUID")
	}

	tx, err := d.beginCatalogTx(ctx)
	if err != nil {
		return catalog.ProjectResult{}, transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	count, done, hasDone, err := claimCatalogRow(ctx, q, tx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "claim failed", err)
	}
	if hasDone {
		if done.Outcome == catalog.OutcomeBlocked {
			return catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: ErrProjection}, nil
		}
		return done, nil
	}

	// Entity lock before any revision decision (R04): concurrent revisions
	// of this category serialize; different entities proceed in parallel.
	keys := [][2]string{{"category", projectedID}}
	if sharedNode {
		keys = append(keys, [2]string{"category", valid.CategoryID})
	}
	if err := lockCatalogEntities(ctx, q, keys...); err != nil {
		_ = tx.Rollback(ctx)
		if isSerializationFailure(err) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict", err)
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "entity lock failed", err)
	}

	// Revision gate against current projection (missing row = first write).
	// Equal revisions compare reconstructed semantic state (R02): the
	// stored hash may predate semantic fingerprinting and never decides.
	storedRevision := int64(-1)
	var existingStore pgtype.UUID
	if row, err := q.CatalogCategoryByID(ctx, cuid); err == nil {
		storedRevision = row.SourceRevision
		existingStore = row.StoreID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		if isSerializationFailure(err) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict", err)
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "category lookup failed", err)
	}
	// Phase 9B ownership gate: a Store-scoped event resolves against the
	// row for its own Store; a legacy event uses the raw seeded identity
	// and the legacy NULL wildcard. The gate is intentionally not applied
	// to Store-scoped default events: their physical identity already
	// encodes the Store, so a pre-R2 Store annotation on the raw row
	// cannot block the new event.
	var writeStore pgtype.UUID
	if sharedNode {
		writeStore = eventWriteStore
	} else {
		var scopeOK bool
		writeStore, scopeOK = resolveProjectionScope(existingStore, event.StoreID)
		if !scopeOK {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "category owned by another store")
		}
	}
	arbitrationScope := storeUUID(effectiveScope(writeStore, existingStore))
	// Canonical parent IDs for this event's Store scope, used by the
	// equal-revision semantic comparison before the dependency resolution
	// below re-derives them with existence checks.
	scopedParentIDs := make([]string, 0, len(valid.ParentIDs))
	for _, parent := range valid.ParentIDs {
		if arbitrationScope.Valid && isSharedCategoryID(parent) {
			scopedParentIDs = append(scopedParentIDs, canonicalDefaultCategoryID(parent, arbitrationScope))
		} else {
			scopedParentIDs = append(scopedParentIDs, parent)
		}
	}
	// Retirement is part of the same serialized transaction even when the
	// canonical revision is already identical or newer. Conflicting equal
	// state never reaches this completion path.
	finishNoop := func() (catalog.ProjectResult, error) {
		if sharedNode {
			established, err := q.EstablishedDefaultCatalog(ctx, sqlcgen.EstablishedDefaultCatalogParams{
				Kind: "category", CanonicalID: cuid, StoreID: eventWriteStore, RawID: valid.CategoryID,
			})
			if err == nil && established.Valid && established.Bool {
				_, err = q.RetireRawDefaultCategory(ctx, sqlcgen.RetireRawDefaultCategoryParams{CategoryID: mustParseUUID(valid.CategoryID), StoreID: eventWriteStore})
			}
			if err != nil {
				_ = tx.Rollback(ctx)
				return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "default transition failed", err)
			}
		}
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorCategoryProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}
	proceed, stale := revisionGate(valid.CatalogRevision, storedRevision)
	if stale {
		return finishNoop()
	}
	if !proceed {
		// A newer repairable revision moots this one even at equal
		// revision: skip revalidation and resolve as a stale no-op.
		supersededEqual, err := entitySuperseded(ctx, q,
			catalog.EventCategorySnapshotV1, "category_id", valid.CategoryID,
			catalog.ProcessorCategoryProjectionV1, "catalog_revision", valid.CatalogRevision, arbitrationScope)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed", err)
		}
		if supersededEqual {
			return finishNoop()
		}
		currentSnapshot, exists, err := currentCategorySnapshot(ctx, q, cuid)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "current state read failed", err)
		}
		// Phase 9-R2 F08: the pre-R2 projector stored raw seeded IDs for
		// default categories. A Store-scoped default event must re-project
		// (writing the canonical row) rather than treat the absent canonical
		// row as an equal-revision conflict. The stored snapshot carries the
		// projected identity, so compare against the same identity.
		if sharedNode && !exists {
			proceed = true
		} else if !exists || !reflect.DeepEqual(catalog.NormalizeCategorySnapshot(withCategoryGraph(valid, projectedID, scopedParentIDs)), canonicalCategorySnapshot(currentSnapshot, arbitrationScope)) {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "equal revision with conflicting state")
		} else if reflect.DeepEqual(catalog.NormalizeCategorySnapshot(withCategoryGraph(valid, projectedID, scopedParentIDs)), currentSnapshot) {
			return finishNoop()
		} else {
			proceed = true
		}
	}
	superseded, err := entitySuperseded(ctx, q,
		catalog.EventCategorySnapshotV1, "category_id", valid.CategoryID,
		catalog.ProcessorCategoryProjectionV1, "catalog_revision", valid.CatalogRevision, arbitrationScope)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed", err)
	}
	if superseded {
		return finishNoop()
	}

	// Parent dependency: missing parents wait retryably, never terminally.
	// Phase 9B/9-R2: every edge must resolve a node that belongs to the
	// same effective Store. A Store-scoped default child resolves its
	// Store-scoped default parents to their Store-local canonical rows, so
	// it may sit under a local root (F09). Legacy NULL rows are wildcards.
	effStore := effectiveScope(writeStore, existingStore)
	resolvedParents, parentOK, err := resolveCategoryParents(ctx, q, valid.ParentIDs, storeUUID(effStore))
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "parent scope lookup failed", err)
	}
	if !parentOK {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrCatalogDependencyWait, "parent category not yet projected")
	}
	for _, parent := range resolvedParents {
		puid, _ := parseUUID(parent.projected)
		prow, err := q.CatalogCategoryByID(ctx, puid)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				_ = tx.Rollback(ctx)
				return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrCatalogDependencyWait, "parent category not yet projected")
			}
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "parent scope lookup failed", err)
		}
		if !scopeCompatible(effStore, storeString(prow.StoreID)) {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "category edge crosses store ownership")
		}
	}

	// Phase 9-R1 F02: a Store adoption must not leave an existing child
	// edge or referencing product owned by another proven Store. Legacy
	// NULL dependents remain wildcards; a proven foreign dependent fails
	// the adoption permanently with the durable state untouched.
	if adoptingStore(existingStore, writeStore) {
		childStores, err := q.CatalogCategoryChildStores(ctx, cuid)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "child scope lookup failed", err)
		}
		if !storeDependentsCompatible(writeStore, childStores) {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "category adoption would cross an existing child store edge")
		}
		productStores, err := q.CatalogCategoryProductStores(ctx, cuid)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "referencing product scope lookup failed", err)
		}
		if !storeDependentsCompatible(writeStore, productStores) {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "category adoption would cross a referencing product store")
		}
	}

	// DAG defense over (current edges with this child's edges replaced).
	// Cycles and depth excess are distinct terminal diagnostics (R07).
	edgeRows, err := q.AllCatalogCategoryEdges(ctx)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "edge lookup failed", err)
	}
	allEdges := make([]catalog.Edge, 0, len(edgeRows))
	for _, edge := range edgeRows {
		allEdges = append(allEdges, catalog.Edge{ParentID: uuidString(edge.ParentID), ChildID: uuidString(edge.ChildID)})
	}
	projectedParentIDs := make([]string, 0, len(resolvedParents))
	for _, p := range resolvedParents {
		projectedParentIDs = append(projectedParentIDs, p.projected)
	}
	if err := checkCategoryGraph(allEdges, projectedID, projectedParentIDs); err != nil {
		_ = tx.Rollback(ctx)
		code := ErrCatalogCategoryCycle
		if errors.Is(err, ErrGraphDepth) {
			code = ErrCatalogCategoryDepth
		}
		return d.markCatalogBlocked(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, code, "category graph violates DAG invariants")
	}

	// Cross-aggregate orphan guard (R03 §38): a parent-set change that
	// would leave currently projected products structurally invalid must
	// not commit unless newer accepted product revisions can repair them.
	// Affected products (child + transitive descendants) are locked with
	// the category key in globally sorted order, so concurrent product
	// projections serialize instead of racing the decision.
	descendants := categoryDescendants(allEdges, projectedID)
	// A revision that keeps the same parent set cannot orphan any product:
	// label/status-only revisions never change graph validity, so the
	// product lock sweep and repair requirement apply only to genuine
	// reparents. F04 needs ordinary label mutation to project.
	// Both additions and removals can invalidate Product structure. Apply
	// the affected-Product locks and full proposed-graph guard to either.
	parentsChanged, err := categoryParentsChanged(ctx, q, cuid, projectedParentIDs)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "parent comparison failed", err)
	}
	if parentsChanged {
		affectedKeys := [][2]string{{"category", projectedID}}
		for _, id := range descendants {
			uid, err := parseUUID(id)
			if err != nil {
				_ = tx.Rollback(ctx)
				return d.markCatalogBlocked(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrValidation, "category graph identity must be UUIDs")
			}
			productIDs, err := q.CatalogProductsReferencingCategory(ctx, uid)
			if err != nil {
				_ = tx.Rollback(ctx)
				return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "affected product lookup failed", err)
			}
			for _, productID := range productIDs {
				affectedKeys = append(affectedKeys, [2]string{"product", uuidString(productID)})
			}
		}
		if err := lockCatalogEntities(ctx, q, affectedKeys...); err != nil {
			_ = tx.Rollback(ctx)
			if isSerializationFailure(err) {
				return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict", err)
			}
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "entity lock failed", err)
		}
		// Only a product that would ACTUALLY become structurally invalid
		// under the proposed graph requires a repair. A removal that leaves
		// every referencing product reachable (e.g. dropping one of several
		// parents, or removing an edge the product's top never used) is a
		// valid frozen-Retail operation and must not block (F09).
		repairable, err := categoryChangeRepairable(ctx, q, allEdges, projectedID, projectedParentIDs)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "repair check failed", err)
		}
		if !repairable {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrCatalogGraphConflict, "category change would orphan current products with no accepted repair")
		}
	}

	if sharedNode {
		if _, err := q.RetireRawDefaultCategory(ctx, sqlcgen.RetireRawDefaultCategoryParams{CategoryID: mustParseUUID(valid.CategoryID), StoreID: eventWriteStore}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "default transition failed", err)
		}
	}
	fingerprint := catalog.FingerprintCategory(valid)
	nameAR := ""
	nameEN := pgtype.Text{}
	for _, name := range valid.Names {
		if name.Locale == catalog.LocaleAR && nameAR == "" {
			nameAR = name.Name
		}
		if name.Locale == catalog.LocaleEN && !nameEN.Valid {
			nameEN = pgText(name.Name)
		}
	}
	if nameAR == "" && len(valid.Names) > 0 {
		nameAR = valid.Names[0].Name
	}
	defaultAlgorithm := int16(0)
	if sharedNode {
		defaultAlgorithm = 1
	}
	// Phase 13 §82: capture the pre-write policy/hierarchy state so the
	// durable commerce re-evaluation fan-out (below, same transaction)
	// fires only on a real eligibility-context change — pure renames and
	// replayed duplicates never trigger provider work.
	previous, prevFound, prevErr := currentCategorySnapshot(ctx, q, cuid)
	if prevErr != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "category state read failed", prevErr)
	}
	if err := q.UpsertCatalogCategory(ctx, sqlcgen.UpsertCatalogCategoryParams{
		CategoryID: cuid, Status: valid.Status, NameAr: nameAR, NameEn: nameEN,
		OnlineEnabled:  valid.OnlineEnabled,
		SourceRevision: valid.CatalogRevision, SourceEventID: attempt.euid, SourceDeviceID: attempt.duid,
		SourcePayloadHash: fingerprint[:], SourceReceivedAt: pgTime(event.ReceivedAt),
		StoreID: writeStore, DefaultAlgorithm: defaultAlgorithm,
	}); err != nil {
		_ = tx.Rollback(ctx)
		if isUniqueViolation(err) {
			return d.markCatalogBlocked(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "category identity collision")
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "category upsert failed", err)
	}
	if err := q.DeleteCatalogCategoryEdges(ctx, cuid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "edge replace failed", err)
	}
	for position, parent := range resolvedParents {
		puid, _ := parseUUID(parent.projected)
		if err := q.InsertCatalogCategoryEdge(ctx, sqlcgen.InsertCatalogCategoryEdgeParams{
			ParentID: puid, ChildID: cuid, Position: int32(position),
		}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "edge insert failed", err)
		}
	}
	// Phase 13 §79/§82: durable commerce re-evaluation fan-out,
	// committed in the SAME transaction as the category projection.
	// Trigger only on a real eligibility-context change: online policy
	// change or parent-set change (renames and equal replays never fan
	// out). Affected Products = every Product classified at this node or
	// any DAG descendant (§85), deduplicated by the queue's Product
	// primary key (§86), Store-proven only (§107). Pure SQL inside the
	// projection transaction: no provider I/O while locks are held (§81).
	projectedParents := make([]string, 0, len(resolvedParents))
	for _, parent := range resolvedParents {
		projectedParents = append(projectedParents, parent.projected)
	}
	if categoryPolicyChanged(previous, prevFound, valid, projectedParents) {
		if _, err := q.EnqueueCategoryAffectedReevaluations(ctx, sqlcgen.EnqueueCategoryAffectedReevaluationsParams{
			Column1: cuid,
			Column2: "category_policy",
			Column3: writeStore,
		}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "reevaluation enqueue failed: "+err.Error(), err)
		}
	}
	// Re-evaluation trigger (R03 §39): waiting or graph-blocked product
	// events referencing the changed subgraph become pending again, so
	// they re-validate against the new graph with no operator retry and
	// no Retail resend. This runs AFTER the category commit in a separate
	// READ COMMITTED transaction: coupling it into the SERIALIZABLE
	// category transaction lets concurrent product attempts abort the
	// category commit itself in an endless retry loop. A failed reset only
	// delays re-arming (products still converge via their own backoff and
	// rescan); it never fails the committed category projection.
	committed, commitErr := finishCatalogAttempt(ctx, q, tx, catalog.ProcessorCategoryProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	if commitErr != nil {
		if isSerializationFailure(commitErr) {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict", commitErr)
		}
		return committed, commitErr
	}
	d.resetCatalogProductsForGraph(context.Background(), descendants)
	return committed, nil
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
	if hasParents(allEdges, valid.TopCategoryID) {
		return d.settleProductStructure(ctx, q, valid, "top category is not a root")
	}
	reachable := reachableFromTop(allEdges, valid.TopCategoryID)
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

// ProjectTag projects one tag snapshot with revision ordering. Tags carry
// no dependencies.
func (d Devices) ProjectTag(ctx context.Context, event catalog.EventRecord, now time.Time) (catalog.ProjectResult, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	attempt, blocked := startCatalogAttempt(event, now)
	if blocked != nil {
		euid, _ := parseUUID(event.EventID)
		return d.markCatalogBlocked(ctx, catalog.ProcessorTagProjectionV1, euid, now, blocked.ErrorCode, "event identity must be UUIDs")
	}
	raw, derr := catalog.DecodeTagSnapshot(attempt.payload)
	if derr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrValidation, safeErr(derr))
	}
	valid, verr := catalog.ValidateTagSnapshot(raw)
	if verr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrValidation, safeErr(verr))
	}
	// Phase 9-R2: default reference tags are Store-scoped identities; the
	// server-derived ingress Store determines the physical row.
	sharedNode := isSharedTagID(valid.TagID) && event.StoreID != nil && *event.StoreID != ""
	eventWriteStore := storeUUID(event.StoreID)
	projectedID := valid.TagID
	if sharedNode {
		projectedID = canonicalDefaultTagID(valid.TagID, eventWriteStore)
	}
	tuid, err := parseUUID(projectedID)
	if err != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrValidation, "tag_id must be a UUID")
	}

	tx, err := d.beginCatalogTx(ctx)
	if err != nil {
		return catalog.ProjectResult{}, transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	count, done, hasDone, err := claimCatalogRow(ctx, q, tx, catalog.ProcessorTagProjectionV1, attempt.euid, now)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "claim failed", err)
	}
	if hasDone {
		if done.Outcome == catalog.OutcomeBlocked {
			return catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: ErrProjection}, nil
		}
		return done, nil
	}

	keys := [][2]string{{"tag", projectedID}}
	if sharedNode {
		keys = append(keys, [2]string{"tag", valid.TagID})
	}
	if err := lockCatalogEntities(ctx, q, keys...); err != nil {
		_ = tx.Rollback(ctx)
		if isSerializationFailure(err) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict", err)
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "entity lock failed", err)
	}
	storedRevision := int64(-1)
	var existingStore pgtype.UUID
	if row, err := q.CatalogTagByID(ctx, tuid); err == nil {
		storedRevision = row.SourceRevision
		existingStore = row.StoreID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		if isSerializationFailure(err) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict", err)
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "tag lookup failed", err)
	}
	// Phase 9B ownership gate: see ProjectCategory. A Store-scoped default
	// tag resolves against its own Store-scoped row; a legacy or
	// Store-created tag keeps the frozen global gate.
	var writeStore pgtype.UUID
	if sharedNode {
		writeStore = eventWriteStore
	} else {
		var scopeOK bool
		writeStore, scopeOK = resolveProjectionScope(existingStore, event.StoreID)
		if !scopeOK {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "tag owned by another store")
		}
	}
	// Retirement is part of the same serialized transaction even when the
	// canonical revision is already identical or newer. Conflicting equal
	// state never reaches this completion path.
	finishNoop := func() (catalog.ProjectResult, error) {
		if sharedNode {
			established, err := q.EstablishedDefaultCatalog(ctx, sqlcgen.EstablishedDefaultCatalogParams{
				Kind: "tag", CanonicalID: tuid, StoreID: eventWriteStore, RawID: valid.TagID,
			})
			if err == nil && established.Valid && established.Bool {
				_, err = q.RetireRawDefaultTag(ctx, sqlcgen.RetireRawDefaultTagParams{TagID: mustParseUUID(valid.TagID), StoreID: eventWriteStore})
			}
			if err != nil {
				_ = tx.Rollback(ctx)
				return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "default transition failed", err)
			}
		}
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorTagProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}
	proceed, stale := revisionGate(valid.CatalogRevision, storedRevision)
	if stale {
		return finishNoop()
	}
	if !proceed {
		supersededEqual, err := entitySuperseded(ctx, q,
			catalog.EventTagSnapshotV1, "tag_id", valid.TagID,
			catalog.ProcessorTagProjectionV1, "catalog_revision", valid.CatalogRevision, storeUUID(effectiveScope(writeStore, existingStore)))
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed", err)
		}
		if supersededEqual {
			return finishNoop()
		}
		currentSnapshot, exists, err := currentTagSnapshot(ctx, q, tuid)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "current state read failed", err)
		}
		// Phase 9-R2 F08: pre-R2 rows used raw seeded IDs; a Store-scoped
		// default event must re-project to write its canonical row.
		if sharedNode && !exists {
			proceed = true
		} else if !exists || !reflect.DeepEqual(catalog.NormalizeTagSnapshot(withTagID(valid, projectedID)), currentSnapshot) {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "equal revision with conflicting state")
		} else {
			return finishNoop()
		}
	}
	superseded, err := entitySuperseded(ctx, q,
		catalog.EventTagSnapshotV1, "tag_id", valid.TagID,
		catalog.ProcessorTagProjectionV1, "catalog_revision", valid.CatalogRevision, storeUUID(effectiveScope(writeStore, existingStore)))
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed", err)
	}
	if superseded {
		return finishNoop()
	}

	// Phase 9-R1 F02: a tag adopting a Store must not remain attached to a
	// product owned by another proven Store. Legacy NULL products are
	// wildcards; a proven foreign one fails the adoption permanently.
	if adoptingStore(existingStore, writeStore) {
		productStores, err := q.CatalogTagProductStores(ctx, tuid)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "tag product scope lookup failed", err)
		}
		if !storeDependentsCompatible(writeStore, productStores) {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "tag adoption would cross an attached product store")
		}
	}

	var nameAR, nameEN pgtype.Text
	for _, name := range valid.Names {
		switch name.Locale {
		case catalog.LocaleAR:
			nameAR = pgText(name.Name)
		case catalog.LocaleEN:
			nameEN = pgText(name.Name)
		}
	}
	if sharedNode {
		if _, err := q.RetireRawDefaultTag(ctx, sqlcgen.RetireRawDefaultTagParams{TagID: mustParseUUID(valid.TagID), StoreID: eventWriteStore}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "default transition failed", err)
		}
	}
	fingerprint := catalog.FingerprintTag(valid)
	defaultAlgorithm := int16(0)
	if sharedNode {
		defaultAlgorithm = 1
	}
	if err := q.UpsertCatalogTag(ctx, sqlcgen.UpsertCatalogTagParams{
		TagID: tuid, Slug: valid.Slug, IsActive: valid.IsActive,
		NameAr: nameAR, NameEn: nameEN,
		SourceRevision: valid.CatalogRevision, SourceEventID: attempt.euid, SourceDeviceID: attempt.duid,
		SourcePayloadHash: fingerprint[:], SourceReceivedAt: pgTime(event.ReceivedAt),
		StoreID: writeStore, DefaultAlgorithm: defaultAlgorithm,
	}); err != nil {
		_ = tx.Rollback(ctx)
		if isUniqueViolation(err) {
			return d.markCatalogBlocked(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "tag identity collision")
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "tag upsert failed", err)
	}
	return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorTagProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
}

// ProjectProduct projects one product snapshot with revision ordering,
// dependency waits, and structural checks. Either a complete product
// projection commits or nothing does; failure leaves the previous
// revision visible.
func (d Devices) ProjectProduct(ctx context.Context, event catalog.EventRecord, now time.Time) (catalog.ProjectResult, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	attempt, blocked := startCatalogAttempt(event, now)
	if blocked != nil {
		euid, _ := parseUUID(event.EventID)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, euid, now, blocked.ErrorCode, "event identity must be UUIDs")
	}
	raw, derr := decodeProductSnapshotEvent(event, attempt.payload)
	if derr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrValidation, safeErr(derr))
	}
	valid, verr := validateProductSnapshotEvent(event, raw)
	if verr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrValidation, safeErr(verr))
	}
	puid, err := parseUUID(valid.ProductID)
	if err != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrValidation, "product_id must be a UUID")
	}
	topUID, err := parseUUID(valid.TopCategoryID)
	if err != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrValidation, "top_category_id must be a UUID")
	}
	// Phase 17-R2: structural type identity is validated at ingestion;
	// legacy v1 products (no type) project with NULL type.
	var typeUID pgtype.UUID
	if strings.TrimSpace(valid.ProductTypeID) != "" {
		typeUID, err = parseUUID(valid.ProductTypeID)
		if err != nil {
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrValidation, "product_type_id must be a UUID")
		}
	}

	tx, err := d.beginCatalogTx(ctx)
	if err != nil {
		return catalog.ProjectResult{}, transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	count, done, hasDone, err := claimCatalogRow(ctx, q, tx, catalog.ProcessorProductProjectionV1, attempt.euid, now)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "claim failed", err)
	}
	if hasDone {
		if done.Outcome == catalog.OutcomeBlocked {
			return catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: ErrProjection}, nil
		}
		return done, nil
	}

	// Entity locks (R04): own product plus referenced categories, taken in
	// globally sorted order before any revision decision. Tags are
	// existence-only (never deleted, revisions irrelevant to product
	// validity) and need no lock.
	lockKeys := [][2]string{
		{"product", valid.ProductID},
		{"category", canonicalDefaultCategoryID(valid.TopCategoryID, storeUUID(event.StoreID))},
		// Inventory decisions use their own namespace; taking it here makes
		// product adoption and concurrent inventory writes for the same
		// product serialize (F02 adoption-race protection).
		{"product-inventory", valid.ProductID},
	}
	for _, sub := range valid.SubcategoryIDs {
		lockKeys = append(lockKeys, [2]string{"category", canonicalDefaultCategoryID(sub, storeUUID(event.StoreID))})
	}
	if err := lockCatalogEntities(ctx, q, lockKeys...); err != nil {
		_ = tx.Rollback(ctx)
		if isSerializationFailure(err) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict", err)
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "entity lock failed", err)
	}

	storedRevision := int64(-1)
	var existingStore pgtype.UUID
	if row, err := q.CatalogProductByID(ctx, puid); err == nil {
		storedRevision = row.SourceRevision
		existingStore = row.StoreID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		if isSerializationFailure(err) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict", err)
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "product lookup failed", err)
	}
	// Phase 9B ownership gate: see ProjectCategory. A scoped event for a
	// product owned by another Store blocks here; a scoped event adopts a
	// NULL legacy row by aggregate-ID + revision continuity.
	writeStore, scopeOK := resolveProjectionScope(existingStore, event.StoreID)
	if !scopeOK {
		_ = tx.Rollback(ctx)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "product owned by another store")
	}
	// Phase 9-R2: resolve the product's default-category/tag references to
	// this Store's canonical identities once, so the equal-revision
	// comparison and every structural check use the projected graph.
	effStore := effectiveScope(writeStore, existingStore)
	storeScope := storeUUID(effStore)
	topProjected := canonicalDefaultCategoryID(valid.TopCategoryID, storeScope)
	resolvedSubs := make([]string, 0, len(valid.SubcategoryIDs))
	for _, sub := range valid.SubcategoryIDs {
		resolvedSubs = append(resolvedSubs, canonicalDefaultCategoryID(sub, storeScope))
	}
	resolvedTags := make([]string, 0, len(valid.TagIDs))
	for _, tagID := range valid.TagIDs {
		resolvedTags = append(resolvedTags, canonicalDefaultTagID(tagID, storeScope))
	}
	projectedValid := valid
	projectedValid.TopCategoryID = topProjected
	projectedValid.SubcategoryIDs = resolvedSubs
	projectedValid.TagIDs = resolvedTags
	if valid.ProductTypeID != "" {
		resolvedType, err := projectedProductTypeID(ctx, q, valid.ProductTypeID, storeScope)
		if err != nil {
			_ = tx.Rollback(ctx)
			if errors.Is(err, ErrProductTypeIdentityCollision) {
				return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, productTypeCollisionMessage(err))
			}
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "product type identity lookup failed", err)
		}
		projectedValid.ProductTypeID = resolvedType
		typeUID, _ = parseUUID(resolvedType)
	}
	proceed, stale := revisionGate(valid.CatalogRevision, storedRevision)
	if stale {
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}
	if !proceed {
		// A newer repairable revision moots this one: resolve as a stale
		// no-op without revalidation.
		supersededEqual, err := entitySuperseded(ctx, q,
			catalog.EventProductSnapshotV1, "product_id", valid.ProductID,
			catalog.ProcessorProductProjectionV1, "catalog_revision", valid.CatalogRevision, storeScope)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed", err)
		}
		if supersededEqual {
			return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
		}
		// Equal revision: identical state still revalidates structure
		// against the CURRENT graph (a graph change may have landed since
		// this revision committed), so post-graph-change redelivery
		// converges instead of idempotent-skipping into invalidity.
		currentSnapshot, exists, err := currentProductSnapshot(ctx, q, puid)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "current state read failed", err)
		}
		normalizedCurrent := currentSnapshot
		normalizedCurrent.SubcategoryIDs = append([]string{}, currentSnapshot.SubcategoryIDs...)
		normalizedCurrent.TagIDs = append([]string{}, currentSnapshot.TagIDs...)
		normalizedCurrent.TopCategoryID = canonicalDefaultCategoryID(currentSnapshot.TopCategoryID, storeScope)
		for i, id := range normalizedCurrent.SubcategoryIDs {
			normalizedCurrent.SubcategoryIDs[i] = canonicalDefaultCategoryID(id, storeScope)
		}
		for i, id := range normalizedCurrent.TagIDs {
			normalizedCurrent.TagIDs[i] = canonicalDefaultTagID(id, storeScope)
		}
		sort.Strings(normalizedCurrent.TagIDs)
		if !exists || !reflect.DeepEqual(catalog.NormalizeProductSnapshot(projectedValid), normalizedCurrent) {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "equal revision with conflicting state")
		}
		if reflect.DeepEqual(catalog.NormalizeProductSnapshot(projectedValid), currentSnapshot) {
			return d.assertProductStructure(ctx, q, tx, attempt, projectedValid, count, now)
		}
		// Same semantics, old physical references: rewrite current state only.
		proceed = true
	}

	superseded, err := entitySuperseded(ctx, q,
		catalog.EventProductSnapshotV1, "product_id", valid.ProductID,
		catalog.ProcessorProductProjectionV1, "catalog_revision", valid.CatalogRevision, storeScope)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed", err)
	}
	if superseded {
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}

	// Dependency resolution: every referenced category and tag must exist.
	// Absence is a retryable wait (out-of-order arrival), never terminal.
	wait := func(msg string) (catalog.ProjectResult, error) {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrCatalogDependencyWait, msg)
	}
	topUID, err = parseUUID(topProjected)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrValidation, "top_category_id must be a UUID")
	}
	topRow, err := q.CatalogCategoryByID(ctx, topUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return wait("top category not yet projected")
		}
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "top category lookup failed", err)
	}
	if !parentScopeCompatible(effStore, valid.TopCategoryID, storeString(topRow.StoreID)) {
		_ = tx.Rollback(ctx)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "top category owned by another store")
	}
	for _, sub := range valid.SubcategoryIDs {
		projectedSub := canonicalDefaultCategoryID(sub, storeScope)
		suid, err := parseUUID(projectedSub)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrValidation, "subcategory_id must be a UUID")
		}
		subRow, err := q.CatalogCategoryByID(ctx, suid)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return wait("subcategory not yet projected")
			}
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "subcategory lookup failed", err)
		}
		if !parentScopeCompatible(effStore, sub, storeString(subRow.StoreID)) {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "subcategory owned by another store")
		}
	}
	for _, tagID := range valid.TagIDs {
		projectedTag := canonicalDefaultTagID(tagID, storeScope)
		guid, err := parseUUID(projectedTag)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrValidation, "tag_id must be a UUID")
		}
		tagRow, err := q.CatalogTagByID(ctx, guid)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return wait("tag not yet projected")
			}
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "tag lookup failed", err)
		}
		if !tagScopeCompatible(effStore, tagID, storeString(tagRow.StoreID)) {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "tag owned by another store")
		}
	}
	// Phase 17-R2 §41/§77: product → type Store ownership. A product cannot
	// assign a type owned by another Store; a missing type is a wait.
	if strings.TrimSpace(valid.ProductTypeID) != "" {
		tuid, err := parseUUID(projectedValid.ProductTypeID)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrValidation, "product_type_id must be a UUID")
		}
		typeRow, err := q.CatalogProductTypeByID(ctx, tuid)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return wait("product type not yet projected")
			}
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "product type lookup failed", err)
		}
		if !tagScopeCompatible(effStore, valid.ProductTypeID, storeString(typeRow.StoreID)) {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "product type owned by another store")
		}
	}
	// Phase 9-R1 F02: adopting a product into a Store must not leave an
	// inventory, policy, or provider mapping owned by another proven Store.
	// The outgoing classification/tag edges were checked above; this closes
	// the incoming dependency side so availability/publication can never
	// consume a foreign Store's stock or policy.
	if adoptingStore(existingStore, writeStore) {
		dependentStores, err := q.CatalogProductDependentStores(ctx, puid)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "product dependency scope lookup failed", err)
		}
		if !storeDependentsCompatible(writeStore, dependentStores) {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "product adoption would cross an existing dependent store")
		}
	}

	// Structural verdict under the current graph (R03): valid proceeds to
	// the atomic write; unsettled waits; settled-invalid blocks. Structure
	// is evaluated on the projected (canonical) identities so default-ID
	// Store scoping does not falsely break reachability.
	verdict, err := d.checkProductStructure(ctx, q, projectedValid)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "structure evaluation failed", err)
	}
	switch verdict {
	case structureWait:
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrCatalogDependencyWait, "graph may still advance")
	case structureTerminal:
		_ = tx.Rollback(ctx)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrCatalogInvalidRelation, "product relation invalid under settled graph")
	}

	var description pgtype.Text
	if valid.Description != nil {
		description = pgText(*valid.Description)
	}
	var width, height pgtype.Int4
	if valid.WidthCM != nil {
		width = pgtype.Int4{Int32: int32(*valid.WidthCM), Valid: true}
	}
	if valid.HeightCM != nil {
		height = pgtype.Int4{Int32: int32(*valid.HeightCM), Valid: true}
	}
	fingerprint := catalog.FingerprintProduct(valid)
	// Phase 13 §42/§84: classification changes (top category or
	// subcategory assignments) move the Product's effective ONLINE
	// eligibility, so they durably schedule the same generic commerce
	// re-evaluation as Category policy changes. Metadata-only product
	// edits (names, prices, tags, stock) do not: publication keeps its
	// frozen manual cadence for those.
	preClassification, preFound, preErr := currentProductSnapshot(ctx, q, puid)
	if preErr != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "product state read failed", preErr)
	}
	if err := q.UpsertCatalogProduct(ctx, sqlcgen.UpsertCatalogProductParams{
		ProductID: puid, Name: valid.Name, Description: description,
		TopCategoryID: topUID, WidthCm: width, HeightCm: height, IsActive: valid.IsActive,
		ProductTypeID:  typeUID,
		SourceRevision: valid.CatalogRevision, SourceEventID: attempt.euid, SourceDeviceID: attempt.duid,
		SourcePayloadHash: fingerprint[:], SourceReceivedAt: pgTime(event.ReceivedAt),
		StoreID: writeStore,
	}); err != nil {
		_ = tx.Rollback(ctx)
		if isUniqueViolation(err) {
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "product identity collision")
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "product upsert failed", err)
	}
	if err := q.DeleteCatalogProductPrices(ctx, puid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "price replace failed", err)
	}
	for _, price := range valid.Prices {
		var cost pgtype.Int8
		if price.CostCents != nil {
			cost = pgtype.Int8{Int64: *price.CostCents, Valid: true}
		}
		if err := q.InsertCatalogProductPrice(ctx, sqlcgen.InsertCatalogProductPriceParams{
			ProductID: puid, Currency: price.Currency,
			PriceMinor: price.PriceCents, CostMinor: cost,
		}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "price insert failed", err)
		}
	}
	if err := q.DeleteCatalogProductTranslations(ctx, puid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "translation replace failed", err)
	}
	for _, translation := range valid.Translations {
		var tdesc pgtype.Text
		if translation.Description != nil {
			tdesc = pgText(*translation.Description)
		}
		if err := q.InsertCatalogProductTranslation(ctx, sqlcgen.InsertCatalogProductTranslationParams{
			ProductID: puid, Locale: translation.Locale, Name: translation.Name, Description: tdesc,
		}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "translation insert failed", err)
		}
	}
	if err := q.DeleteCatalogProductSubcategories(ctx, puid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "subcategory replace failed", err)
	}
	for position, sub := range resolvedSubs {
		suid, _ := parseUUID(sub)
		if err := q.InsertCatalogProductSubcategory(ctx, sqlcgen.InsertCatalogProductSubcategoryParams{
			ProductID: puid, CategoryID: suid, Position: int32(position),
		}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "subcategory insert failed", err)
		}
	}
	if err := q.DeleteCatalogProductTags(ctx, puid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "tag replace failed", err)
	}
	for _, tagID := range resolvedTags {
		guid, _ := parseUUID(tagID)
		if err := q.InsertCatalogProductTag(ctx, sqlcgen.InsertCatalogProductTagParams{
			ProductID: puid, TagID: guid,
		}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "tag insert failed", err)
		}
	}
	// Phase 13 §79: durable re-evaluation signal, atomic with the product
	// projection commit (same transaction — crash-safe, restart-safe).
	if productClassificationChanged(preClassification, preFound, uuidString(topUID), resolvedSubs) {
		if err := q.EnqueueProductReevaluation(ctx, sqlcgen.EnqueueProductReevaluationParams{
			ProductID: puid, StoreID: writeStore, Reason: "product_classification",
		}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "reevaluation enqueue failed", err)
		}
	}
	return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
}

// currentProductSalesPolicySnapshot reconstructs the normalized semantic
// state of a projected sales policy.
func currentProductSalesPolicySnapshot(ctx context.Context, q *sqlcgen.Queries, puid pgtype.UUID) (catalog.NormalizedProductSalesPolicy, bool, error) {
	row, err := q.CatalogProductSalesPolicyByID(ctx, puid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.NormalizedProductSalesPolicy{}, false, nil
		}
		return catalog.NormalizedProductSalesPolicy{}, false, err
	}
	var limit *int
	if row.OnlineAllocationLimit.Valid {
		v := int(row.OnlineAllocationLimit.Int64)
		limit = &v
	}
	return catalog.NormalizeProductSalesPolicySnapshot(catalog.ProductSalesPolicySnapshot{
		ProductID: uuidString(row.ProductID), SalesPolicyRevision: row.SourceRevision,
		SellOffline: row.SellOffline, SellOnline: row.SellOnline,
		OnlineAllocationLimit: limit,
	}), true, nil
}

// ProjectProductSalesPolicy projects one sales-policy snapshot with
// revision ordering and a product dependency wait. Either the policy row
// commits or nothing does; failure leaves the previous revision visible.
// Inactive products still project policy rows: lifecycle stays separate.
func (d Devices) ProjectProductSalesPolicy(ctx context.Context, event catalog.EventRecord, now time.Time) (catalog.ProjectResult, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	attempt, blocked := startCatalogAttempt(event, now)
	if blocked != nil {
		euid, _ := parseUUID(event.EventID)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, euid, now, blocked.ErrorCode, "event identity must be UUIDs")
	}
	raw, derr := catalog.DecodeProductSalesPolicySnapshot(attempt.payload)
	if derr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrValidation, safeErr(derr))
	}
	valid, verr := catalog.ValidateProductSalesPolicySnapshot(raw)
	if verr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrValidation, safeErr(verr))
	}
	puid, err := parseUUID(valid.ProductID)
	if err != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrValidation, "product_id must be a UUID")
	}

	tx, err := d.beginCatalogTx(ctx)
	if err != nil {
		return catalog.ProjectResult{}, transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	count, done, hasDone, err := claimCatalogRow(ctx, q, tx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrProjection, "claim failed", err)
	}
	if hasDone {
		if done.Outcome == catalog.OutcomeBlocked {
			return catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: ErrProjection}, nil
		}
		return done, nil
	}

	if err := lockCatalogEntities(ctx, q, [2]string{"product", valid.ProductID}); err != nil {
		_ = tx.Rollback(ctx)
		if isSerializationFailure(err) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict", err)
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrProjection, "entity lock failed", err)
	}
	storedRevision := int64(-1)
	var existingStore pgtype.UUID
	if row, err := q.CatalogProductSalesPolicyByID(ctx, puid); err == nil {
		storedRevision = row.SourceRevision
		existingStore = row.StoreID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		if isSerializationFailure(err) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrProjection, "policy lookup failed", err)
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrProjection, "policy lookup failed", err)
	}
	// Phase 9B ownership gate: see ProjectCategory.
	writeStore, scopeOK := resolveProjectionScope(existingStore, event.StoreID)
	if !scopeOK {
		_ = tx.Rollback(ctx)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "sales policy owned by another store")
	}
	proceed, stale := revisionGate(valid.SalesPolicyRevision, storedRevision)
	if stale {
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}
	if !proceed {
		supersededEqual, err := entitySuperseded(ctx, q,
			catalog.EventProductSalesPolicySnapshotV1, "product_id", valid.ProductID,
			catalog.ProcessorProductSalesPolicyProjectionV1, "sales_policy_revision", valid.SalesPolicyRevision, storeUUID(event.StoreID))
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed", err)
		}
		if supersededEqual {
			return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
		}
		currentSnapshot, exists, err := currentProductSalesPolicySnapshot(ctx, q, puid)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrProjection, "current state read failed", err)
		}
		if !exists || !reflect.DeepEqual(catalog.NormalizeProductSalesPolicySnapshot(valid), currentSnapshot) {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "equal revision with conflicting state")
		}
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}
	superseded, err := entitySuperseded(ctx, q,
		catalog.EventProductSalesPolicySnapshotV1, "product_id", valid.ProductID,
		catalog.ProcessorProductSalesPolicyProjectionV1, "sales_policy_revision", valid.SalesPolicyRevision, storeUUID(event.StoreID))
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed", err)
	}
	if superseded {
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}

	// Dependency resolution: the core product must exist. Absence is a
	// retryable wait (out-of-order arrival), never terminal. A product
	// owned by another proven Store can never back this policy.
	productRow, err := q.CatalogProductByID(ctx, puid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrCatalogDependencyWait, "product not yet projected")
		}
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrProjection, "product lookup failed", err)
	}
	if !scopeCompatible(effectiveScope(writeStore, existingStore), storeString(productRow.StoreID)) {
		_ = tx.Rollback(ctx)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "product owned by another store")
	}

	var allocation pgtype.Int8
	if valid.OnlineAllocationLimit != nil {
		allocation = pgtype.Int8{Int64: int64(*valid.OnlineAllocationLimit), Valid: true}
	}
	fingerprint := catalog.FingerprintProductSalesPolicy(valid)
	if err := q.UpsertCatalogProductSalesPolicy(ctx, sqlcgen.UpsertCatalogProductSalesPolicyParams{
		ProductID: puid, SellOffline: valid.SellOffline, SellOnline: valid.SellOnline,
		OnlineAllocationLimit: allocation,
		SourceRevision:        valid.SalesPolicyRevision, SourceEventID: attempt.euid, SourceDeviceID: attempt.duid,
		SourcePayloadHash: fingerprint[:], SourceReceivedAt: pgTime(event.ReceivedAt),
		StoreID: writeStore,
	}); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrProjection, "policy upsert failed", err)
	}
	return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
}

// currentProductInventorySnapshot reconstructs the normalized semantic
// state of a projected inventory row.
func currentProductInventorySnapshot(ctx context.Context, q *sqlcgen.Queries, puid pgtype.UUID) (catalog.NormalizedProductInventory, bool, error) {
	row, err := q.CatalogProductInventoryByID(ctx, puid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.NormalizedProductInventory{}, false, nil
		}
		return catalog.NormalizedProductInventory{}, false, err
	}
	return catalog.NormalizeProductInventorySnapshot(catalog.ProductInventorySnapshot{
		ProductID: uuidString(row.ProductID), InventoryRevision: row.SourceRevision,
		StockQuantity: int(row.StockQuantity),
	}), true, nil
}

// ProjectProductInventory projects one inventory snapshot with revision
// ordering and a product dependency wait. Either the inventory row commits
// or nothing does; failure leaves the previous revision visible.
// Inactive products and sell_online=false still project rows: lifecycle
// and policy affect availability, never inventory truth.
func (d Devices) ProjectProductInventory(ctx context.Context, event catalog.EventRecord, now time.Time) (catalog.ProjectResult, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	attempt, blocked := startCatalogAttempt(event, now)
	if blocked != nil {
		euid, _ := parseUUID(event.EventID)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductInventoryProjectionV1, euid, now, blocked.ErrorCode, "event identity must be UUIDs")
	}
	raw, derr := catalog.DecodeProductInventorySnapshot(attempt.payload)
	if derr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrValidation, safeErr(derr))
	}
	valid, verr := catalog.ValidateProductInventorySnapshot(raw)
	if verr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrValidation, safeErr(verr))
	}
	puid, err := parseUUID(valid.ProductID)
	if err != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrValidation, "product_id must be a UUID")
	}

	tx, err := d.beginCatalogTx(ctx)
	if err != nil {
		return catalog.ProjectResult{}, transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	count, done, hasDone, err := claimCatalogRow(ctx, q, tx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrProjection, "claim failed", err)
	}
	if hasDone {
		if done.Outcome == catalog.OutcomeBlocked {
			return catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: ErrProjection}, nil
		}
		return done, nil
	}

	// Distinct lock namespace: inventory decisions serialize per product
	// without joining product/policy lock traffic.
	if err := lockCatalogEntities(ctx, q, [2]string{"product-inventory", valid.ProductID}); err != nil {
		_ = tx.Rollback(ctx)
		if isSerializationFailure(err) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict", err)
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrProjection, "entity lock failed", err)
	}
	storedRevision := int64(-1)
	var existingStore pgtype.UUID
	if row, err := q.CatalogProductInventoryByID(ctx, puid); err == nil {
		storedRevision = row.SourceRevision
		existingStore = row.StoreID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		if isSerializationFailure(err) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrProjection, "inventory lookup failed", err)
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrProjection, "inventory lookup failed", err)
	}
	// Phase 9B ownership gate: see ProjectCategory. A high revision
	// from one Store never suppresses another Store's inventory: it
	// conflicts instead, leaving the proven row untouched.
	writeStore, scopeOK := resolveProjectionScope(existingStore, event.StoreID)
	if !scopeOK {
		_ = tx.Rollback(ctx)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "inventory owned by another store")
	}
	proceed, stale := revisionGate(valid.InventoryRevision, storedRevision)
	if stale {
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}
	if !proceed {
		supersededEqual, err := entitySuperseded(ctx, q,
			catalog.EventInventoryProductSnapshotV1, "product_id", valid.ProductID,
			catalog.ProcessorProductInventoryProjectionV1, "inventory_revision", valid.InventoryRevision, storeUUID(event.StoreID))
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed", err)
		}
		if supersededEqual {
			return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
		}
		currentSnapshot, exists, err := currentProductInventorySnapshot(ctx, q, puid)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrProjection, "current state read failed", err)
		}
		if !exists || !reflect.DeepEqual(catalog.NormalizeProductInventorySnapshot(valid), currentSnapshot) {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "equal revision with conflicting state")
		}
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}
	superseded, err := entitySuperseded(ctx, q,
		catalog.EventInventoryProductSnapshotV1, "product_id", valid.ProductID,
		catalog.ProcessorProductInventoryProjectionV1, "inventory_revision", valid.InventoryRevision, storeUUID(event.StoreID))
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed", err)
	}
	if superseded {
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}

	// Dependency resolution: the core product must exist. Absence is a
	// retryable wait (out-of-order arrival), never terminal. A product
	// owned by another proven Store can never back this inventory.
	productRow, err := q.CatalogProductByID(ctx, puid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrCatalogDependencyWait, "product not yet projected")
		}
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrProjection, "product lookup failed", err)
	}
	if !scopeCompatible(effectiveScope(writeStore, existingStore), storeString(productRow.StoreID)) {
		_ = tx.Rollback(ctx)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "product owned by another store")
	}

	fingerprint := catalog.FingerprintProductInventory(valid)
	if err := q.UpsertCatalogProductInventory(ctx, sqlcgen.UpsertCatalogProductInventoryParams{
		ProductID: puid, StockQuantity: int64(valid.StockQuantity),
		SourceRevision: valid.InventoryRevision, SourceEventID: attempt.euid, SourceDeviceID: attempt.duid,
		SourcePayloadHash: fingerprint[:], SourceReceivedAt: pgTime(event.ReceivedAt),
		StoreID: writeStore,
	}); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrProjection, "inventory upsert failed", err)
	}
	return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
}

// CatalogOnlineConfiguredProductsForStore enumerates the online-
// configured products of exactly one proven Store through canonical
// projected state. Provider publication must use this (or a single
// authoritative product) rather than any global enumeration, so one
// Store's write can never sweep another Store's products.
func (d Devices) CatalogOnlineConfiguredProductsForStore(ctx context.Context, storeID string, limit int) ([]string, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	suid, err := parseUUID(storeID)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "store_id must be a UUID")
	}
	if limit <= 0 || limit > 1000 {
		return nil, apperr.New(apperr.InvalidInput, "limit must be 1..1000")
	}
	rows, err := sqlcgen.New(d.pool).CatalogOnlineConfiguredProductsForStore(ctx, sqlcgen.CatalogOnlineConfiguredProductsForStoreParams{
		StoreID: suid, Limit: int32(limit),
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "online products for store", redact(err))
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, uuidString(row.ProductID))
	}
	return out, nil
}

// Catalog read API for future phases (internal/catalog.Repository).

func catalogNotFound(what string) error {
	return apperr.New(apperr.NotFound, what+" not found")
}

// CatalogProduct assembles the full projected product aggregate.
func (d Devices) CatalogProduct(ctx context.Context, id string) (catalog.Product, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return catalog.Product{}, catalogNotFound("product")
	}
	q := sqlcgen.New(d.pool)
	row, err := q.CatalogProductByID(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.Product{}, catalogNotFound("product")
		}
		return catalog.Product{}, apperr.Wrap(apperr.Internal, "catalog product", redact(err))
	}
	prices, err := q.CatalogProductPrices(ctx, uid)
	if err != nil {
		return catalog.Product{}, apperr.Wrap(apperr.Internal, "catalog prices", redact(err))
	}
	translations, err := q.CatalogProductTranslations(ctx, uid)
	if err != nil {
		return catalog.Product{}, apperr.Wrap(apperr.Internal, "catalog translations", redact(err))
	}
	subs, err := q.CatalogProductSubcategories(ctx, uid)
	if err != nil {
		return catalog.Product{}, apperr.Wrap(apperr.Internal, "catalog subcategories", redact(err))
	}
	tags, err := q.CatalogProductTags(ctx, uid)
	if err != nil {
		return catalog.Product{}, apperr.Wrap(apperr.Internal, "catalog tags", redact(err))
	}
	product := catalog.Product{
		ProductHeader: catalog.ProductHeader{
			ID: uuidString(row.ProductID), Name: row.Name,
			IsActive: row.IsActive, Revision: row.SourceRevision,
			SourceEventID: uuidString(row.SourceEventID),
			StoreID:       storeString(row.StoreID),
		},
		TopCategoryID: uuidString(row.TopCategoryID),
	}
	if row.Description.Valid {
		desc := row.Description.String
		product.Description = &desc
	}
	if row.WidthCm.Valid {
		width := int(row.WidthCm.Int32)
		product.WidthCM = &width
	}
	if row.HeightCm.Valid {
		height := int(row.HeightCm.Int32)
		product.HeightCM = &height
	}
	for _, price := range prices {
		entry := catalog.Price{Currency: price.Currency, PriceCents: price.PriceMinor}
		if price.CostMinor.Valid {
			cost := price.CostMinor.Int64
			entry.CostCents = &cost
		}
		product.Prices = append(product.Prices, entry)
	}
	for _, translation := range translations {
		entry := catalog.CatalogProductTranslation{Locale: translation.Locale, Name: translation.Name}
		if translation.Description.Valid {
			desc := translation.Description.String
			entry.Description = &desc
		}
		product.Translations = append(product.Translations, entry)
	}
	for _, sub := range subs {
		product.SubcategoryIDs = append(product.SubcategoryIDs, uuidString(sub))
	}
	for _, tag := range tags {
		product.TagIDs = append(product.TagIDs, uuidString(tag))
	}
	return product, nil
}

// CatalogActiveProducts lists active product headers.
func (d Devices) CatalogActiveProducts(ctx context.Context, limit int) ([]catalog.ProductHeader, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).CatalogActiveProducts(ctx, int32(limit))
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "catalog products", redact(err))
	}
	out := make([]catalog.ProductHeader, 0, len(rows))
	for _, row := range rows {
		out = append(out, catalog.ProductHeader{
			ID: uuidString(row.ProductID), Name: row.Name,
			IsActive: true, Revision: row.SourceRevision,
			SourceEventID: uuidString(row.SourceEventID),
		})
	}
	return out, nil
}

// CatalogCategory assembles the projected category with its parent set.
func (d Devices) CatalogCategory(ctx context.Context, id string) (catalog.Category, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return catalog.Category{}, catalogNotFound("category")
	}
	q := sqlcgen.New(d.pool)
	row, err := q.CatalogCategoryByID(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.Category{}, catalogNotFound("category")
		}
		return catalog.Category{}, apperr.Wrap(apperr.Internal, "catalog category", redact(err))
	}
	parents, err := q.CatalogCategoryParents(ctx, uid)
	if err != nil {
		return catalog.Category{}, apperr.Wrap(apperr.Internal, "catalog parents", redact(err))
	}
	category := catalog.Category{
		ID: uuidString(row.CategoryID), Status: row.Status, NameAR: row.NameAr,
		Revision: row.SourceRevision, SourceEventID: uuidString(row.SourceEventID),
	}
	if row.NameEn.Valid {
		name := row.NameEn.String
		category.NameEN = &name
	}
	for _, parent := range parents {
		category.ParentIDs = append(category.ParentIDs, uuidString(parent))
	}
	return category, nil
}

// CatalogCategoryEdges lists every projected DAG edge.
func (d Devices) CatalogCategoryEdges(ctx context.Context) ([]catalog.Edge, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).AllCatalogCategoryEdges(ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "catalog edges", redact(err))
	}
	out := make([]catalog.Edge, 0, len(rows))
	for _, row := range rows {
		out = append(out, catalog.Edge{ParentID: uuidString(row.ParentID), ChildID: uuidString(row.ChildID)})
	}
	return out, nil
}

// CatalogTag returns the projected tag.
func (d Devices) CatalogTag(ctx context.Context, id string) (catalog.Tag, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return catalog.Tag{}, catalogNotFound("tag")
	}
	row, err := sqlcgen.New(d.pool).CatalogTagByID(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.Tag{}, catalogNotFound("tag")
		}
		return catalog.Tag{}, apperr.Wrap(apperr.Internal, "catalog tag", redact(err))
	}
	tag := catalog.Tag{
		ID: uuidString(row.TagID), Slug: row.Slug, IsActive: row.IsActive,
		Revision: row.SourceRevision, SourceEventID: uuidString(row.SourceEventID),
	}
	if row.NameAr.Valid {
		name := row.NameAr.String
		tag.NameAR = &name
	}
	if row.NameEn.Valid {
		name := row.NameEn.String
		tag.NameEN = &name
	}
	return tag, nil
}

// CatalogProductSalesPolicy returns the projected channel/allocation
// configuration for one product.
func (d Devices) CatalogProductSalesPolicy(ctx context.Context, id string) (catalog.ProductSalesPolicy, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return catalog.ProductSalesPolicy{}, catalogNotFound("sales policy")
	}
	row, err := sqlcgen.New(d.pool).CatalogProductSalesPolicyByID(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.ProductSalesPolicy{}, catalogNotFound("sales policy")
		}
		return catalog.ProductSalesPolicy{}, apperr.Wrap(apperr.Internal, "catalog sales policy", redact(err))
	}
	policy := catalog.ProductSalesPolicy{
		ProductID: uuidString(row.ProductID), SellOffline: row.SellOffline, SellOnline: row.SellOnline,
		Revision: row.SourceRevision, SourceEventID: uuidString(row.SourceEventID),
	}
	if row.OnlineAllocationLimit.Valid {
		limit := int(row.OnlineAllocationLimit.Int64)
		policy.OnlineAllocationLimit = &limit
	}
	return policy, nil
}

// CatalogProductInventory returns the projected last-known inventory.
func (d Devices) CatalogProductInventory(ctx context.Context, id string) (catalog.ProductInventory, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return catalog.ProductInventory{}, catalogNotFound("inventory")
	}
	row, err := sqlcgen.New(d.pool).CatalogProductInventoryByID(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.ProductInventory{}, catalogNotFound("inventory")
		}
		return catalog.ProductInventory{}, apperr.Wrap(apperr.Internal, "catalog inventory", redact(err))
	}
	return catalog.ProductInventory{
		ProductID: uuidString(row.ProductID), StockQuantity: int(row.StockQuantity),
		Revision: row.SourceRevision, SourceEventID: uuidString(row.SourceEventID),
		SourceReceivedAt: row.SourceReceivedAt.Time, ProjectedAt: row.ProjectedAt.Time,
	}, nil
}

// CatalogProductAvailability derives provider-neutral ONLINE availability
// from current product, policy, and inventory rows in one read
// transaction, so the three inputs are mutually consistent. The read
// performs no writes.
func (d Devices) CatalogProductAvailability(ctx context.Context, id string) (catalog.ProductAvailability, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return catalog.ProductAvailability{}, catalogNotFound("availability")
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return catalog.ProductAvailability{}, apperr.Wrap(apperr.Internal, "catalog availability", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := sqlcgen.New(tx).CatalogAvailabilityByProductID(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.ProductAvailability{ProductID: id}, nil
		}
		return catalog.ProductAvailability{}, apperr.Wrap(apperr.Internal, "catalog availability", redact(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return catalog.ProductAvailability{}, apperr.Wrap(apperr.Internal, "catalog availability", redact(err))
	}
	availability := catalog.ProductAvailability{
		ProductID: uuidString(row.ProductID), ProductActive: row.ProductActive,
		MissingPolicy: !row.SellOnline.Valid, MissingInventory: !row.StockQuantity.Valid,
	}
	if row.SellOnline.Valid {
		availability.SellOnline = row.SellOnline.Bool
	}
	if row.OnlineAllocationLimit.Valid {
		limit := int(row.OnlineAllocationLimit.Int64)
		availability.OnlineAllocationLimit = &limit
	}
	var stock int
	inventoryFound := row.StockQuantity.Valid
	if inventoryFound {
		stock = int(row.StockQuantity.Int64)
		quantity := stock
		availability.StockQuantity = &quantity
		availability.InventoryRevision = row.InventoryRevision.Int64
		availability.InventoryEventID = uuidString(row.InventoryEventID)
		if row.InventoryProjectedAt.Valid {
			projected := row.InventoryProjectedAt.Time
			availability.InventoryProjectedAt = &projected
		}
	}
	online, ready := catalog.ComputeProductAvailability(
		row.ProductActive, true,
		availability.SellOnline, !availability.MissingPolicy, availability.OnlineAllocationLimit,
		stock, inventoryFound,
	)
	availability.OnlineAvailable = online
	availability.Ready = ready
	return availability, nil
}

func defaultCatalogEntity(eventType, id string) bool {
	return (eventType == catalog.EventCategorySnapshotV1 || eventType == catalog.EventCategorySnapshotV2) && isSharedCategoryID(id) || eventType == catalog.EventTagSnapshotV1 && isSharedTagID(id) || eventType == catalog.EventProductTypeSnapshotV1 && id == sharedProductTypeID
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
	if hasParents(edges, top) {
		return false, nil
	}
	reachable := reachableFromTop(edges, top)
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
	events, err := q.CatalogEntityEventRevisions(ctx, sqlcgen.CatalogEntityEventRevisionsParams{EventType: catalog.EventProductSnapshotV1, Column2: "product_id", Column3: uuidString(row.ProductID), Processor: catalog.ProcessorProductProjectionV1, Column5: "catalog_revision", StoreID: row.StoreID})
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

// CatalogProductOnlinePolicy reads the canonical effective-online policy
// for one Product (Phase 13). Read-only: no provider calls, no writes,
// no barrier interaction — durable Cloud state only.
func (d Devices) CatalogProductOnlinePolicy(ctx context.Context, id string) (catalog.ProductOnlinePolicy, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return catalog.ProductOnlinePolicy{}, err
	}
	row, err := sqlcgen.New(d.pool).CatalogProductOnlineState(ctx, uid)
	if err != nil {
		return catalog.ProductOnlinePolicy{}, reportErr("catalog product online policy", err)
	}
	policy := catalog.ProductOnlinePolicy{
		ProductID:          uuidString(row.ProductID),
		StoreID:            uuidPtr(row.StoreID),
		Allowed:            row.CategoryAllowsOnline.Bool,
		Reason:             catalog.OnlineAllowed,
		BlockingCategoryID: uuidPtr(row.BlockingCategoryID),
		PolicyFingerprint:  string(row.PolicyFingerprint),
		PolicyVersion:      string(row.PolicyVersion),
	}
	if !row.CategoryAllowsOnline.Bool {
		policy.Reason = row.BlockReason
		if policy.Reason == "" {
			policy.Reason = catalog.OnlineBlockedCategory
		}
	}
	return policy, nil
}

// ProjectProductConfigurations projects one Phase 15 configuration
// snapshot (the complete ONLINE product-option set of one Product at one
// aggregate configuration revision). One atomic claim+project
// SERIALIZABLE transaction: aggregate revision gate under the frozen
// rules (stale no-ops; equal revisions compare normalized semantic
// state; contradictions block), replace-set of configuration rows, and —
// when effective option state actually changed — a durable commerce
// re-evaluation intent in the SAME transaction (crash-safe; no provider
// I/O inside the transaction).
func (d Devices) ProjectProductConfigurations(ctx context.Context, event catalog.EventRecord, now time.Time) (catalog.ProjectResult, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	attempt, blocked := startCatalogAttempt(event, now)
	if blocked != nil {
		euid, _ := parseUUID(event.EventID)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductConfigurationProjectionV1, euid, now, blocked.ErrorCode, "event identity must be UUIDs")
	}
	raw, derr := catalog.DecodeProductConfigurationsSnapshot(attempt.payload)
	if derr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrValidation, safeErr(derr))
	}
	valid, verr := catalog.ValidateProductConfigurationsSnapshot(raw)
	if verr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrValidation, safeErr(verr))
	}
	puid, perr := parseUUID(valid.ProductID)
	if perr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrValidation, "product_id must be a UUID")
	}
	tx, err := d.beginCatalogTx(ctx)
	if err != nil {
		return catalog.ProjectResult{}, transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	count, done, hasDone, err := claimCatalogRow(ctx, q, tx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrProjection, "claim failed", err)
	}
	if hasDone {
		if done.Outcome == catalog.OutcomeBlocked {
			return catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: ErrProjection}, nil
		}
		return done, nil
	}
	lockKeys := [][2]string{{"product", valid.ProductID}}
	for _, entry := range valid.Configurations {
		lockKeys = append(lockKeys, [2]string{"product-configuration", entry.ConfigurationID})
	}
	if err := lockCatalogEntities(ctx, q, lockKeys...); err != nil {
		_ = tx.Rollback(ctx)
		if isSerializationFailure(err) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict", err)
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrProjection, "entity lock failed", err)
	}
	// The Product must already exist: configurations never invent
	// Products (§7) and never carry their own identity.
	productRow, err := q.CatalogProductByID(ctx, puid)
	if err != nil {
		_ = tx.Rollback(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrCatalogDependencyWait, "product not yet projected")
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrProjection, "product lookup failed", err)
	}
	// Phase 15-R1 F17: the TRUSTED ingress Store context recorded with
	// the accepted event must authorize mutation of this Product's
	// configurations. The payload never supplies Store identity and the
	// device's CURRENT binding is never re-derived here.
	if _, scopeOK := resolveProjectionScope(productRow.StoreID, event.StoreID); !scopeOK {
		_ = tx.Rollback(ctx)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "configuration event not authorized for product store")
	}
	// Phase 15-R1 F17: configuration ID → Product ownership is immutable.
	// A supplied ID already owned by another Product/Store is rejected —
	// an upsert may never reassign product_id.
	for _, entry := range valid.Configurations {
		configurationUID, err := parseUUID(entry.ConfigurationID)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrValidation, "configuration_id must be a UUID")
		}
		row, err := q.CatalogProductConfigurationByID(ctx, configurationUID)
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				_ = tx.Rollback(ctx)
				return d.persistCatalogRetry(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrProjection, "configuration ownership lookup failed", err)
			}
			continue
		}
		if uuidString(row.ProductID) != valid.ProductID {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "configuration identity owned by another product")
		}
	}
	storedRows, err := q.CatalogProductConfigurationsByProduct(ctx, puid)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrProjection, "configuration state read failed", err)
	}
	storedRevision := productRow.ConfigurationRevision
	if valid.ConfigurationRevision < storedRevision {
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}
	if valid.ConfigurationRevision == storedRevision {
		if !reflect.DeepEqual(catalog.NormalizeProductConfigurations(valid), normalizeStoredConfigurations(puid, storedRows, storedRevision)) {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "equal revision with conflicting state")
		}
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}
	fingerprint := catalog.FingerprintProductConfigurations(valid)
	if err := q.DeleteCatalogProductConfigurations(ctx, puid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrProjection, "configuration replace failed", err)
	}
	for _, entry := range valid.Configurations {
		cuid, cerr := parseUUID(entry.ConfigurationID)
		if cerr != nil {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrValidation, "configuration_id must be a UUID")
		}
		params := sqlcgen.UpsertCatalogProductConfigurationParams{
			ConfigurationID: cuid, ProductID: puid, Kind: entry.Kind,
			StyleCode: entry.StyleCode, StyleNameAr: entry.StyleNameAR,
			ColorCode: entry.ColorCode, ColorNameAr: entry.ColorNameAR,
			PriceDeltaEgpMinor: entry.PriceDeltaEGPCents,
			Enabled:            entry.Enabled, Position: int32(entry.Position),
			ConfigurationRevision: entry.ConfigurationRevision,
			SourceRevision:        valid.ConfigurationRevision, SourceEventID: attempt.euid,
			SourceDeviceID: attempt.duid, SourcePayloadHash: fingerprint[:],
			SourceReceivedAt: pgTime(event.ReceivedAt),
		}
		if entry.StyleNameEN != nil {
			params.StyleNameEn = pgText(*entry.StyleNameEN)
		}
		if entry.ColorNameEN != nil {
			params.ColorNameEn = pgText(*entry.ColorNameEN)
		}
		if entry.PriceDeltaUSDCents != nil {
			params.PriceDeltaUsdMinor = pgtype.Int8{Int64: *entry.PriceDeltaUSDCents, Valid: true}
		}
		written, err := q.UpsertCatalogProductConfiguration(ctx, params)
		if err != nil {
			_ = tx.Rollback(ctx)
			if isUniqueViolation(err) {
				return d.markCatalogBlocked(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "configuration identity collision")
			}
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrProjection, "configuration upsert failed", err)
		}
		// The guarded ON CONFLICT clause never moves product_id (F17):
		// zero affected rows means the ID belongs to another Product.
		if written == 0 {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "configuration identity owned by another product")
		}
	}
	if _, err := q.BumpCatalogProductConfigurationRevision(ctx, sqlcgen.BumpCatalogProductConfigurationRevisionParams{
		ProductID: puid, ConfigurationRevision: valid.ConfigurationRevision,
	}); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrProjection, "configuration revision write failed", err)
	}
	// Durable commerce re-evaluation (§147): coalesced per Product in the
	// projection transaction. The worker reads CURRENT state at run time
	// (§149), so rapid mutation sequences converge to the latest durable
	// projection (§150).
	if err := q.EnqueueProductReevaluation(ctx, sqlcgen.EnqueueProductReevaluationParams{
		ProductID: puid, Reason: "product_configuration",
	}); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, now, ErrProjection, "reevaluation enqueue failed: "+err.Error(), err)
	}
	_ = productRow
	return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductConfigurationProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
}

// normalizeStoredConfigurations reconstructs comparison form from the
// projected configuration rows (frozen equal-revision semantics).
func normalizeStoredConfigurations(productID pgtype.UUID, rows []sqlcgen.CatalogProductConfigurationsByProductRow, storedRevision int64) catalog.NormalizedProductConfigurations {
	entries := make([]catalog.ProductConfigurationEntry, 0, len(rows))
	for _, row := range rows {
		entry := catalog.ProductConfigurationEntry{
			ConfigurationID: uuidString(row.ConfigurationID), Kind: row.Kind,
			StyleCode: row.StyleCode, StyleNameAR: row.StyleNameAr,
			ColorCode: row.ColorCode, ColorNameAR: row.ColorNameAr,
			PriceDeltaEGPCents: row.PriceDeltaEgpMinor,
			Enabled:            row.Enabled, Position: int(row.Position),
			ConfigurationRevision: row.ConfigurationRevision,
		}
		if row.StyleNameEn.Valid {
			value := row.StyleNameEn.String
			entry.StyleNameEN = &value
		}
		if row.ColorNameEn.Valid {
			value := row.ColorNameEn.String
			entry.ColorNameEN = &value
		}
		if row.PriceDeltaUsdMinor.Valid {
			value := row.PriceDeltaUsdMinor.Int64
			entry.PriceDeltaUSDCents = &value
		}
		entries = append(entries, entry)
	}
	return catalog.NormalizeProductConfigurations(catalog.ProductConfigurationsSnapshot{
		ProductID: uuidString(productID), Configurations: entries,
		ConfigurationRevision: storedRevision,
	})
}

// CatalogProductConfigurations reads one Product's projected ONLINE
// configurations (Phase 15). Read-only durable Cloud state.
func (d Devices) CatalogProductConfigurations(ctx context.Context, id string) ([]catalog.ProductConfiguration, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(d.pool).CatalogProductConfigurationsByProduct(ctx, uid)
	if err != nil {
		return nil, reportErr("catalog product configurations", err)
	}
	out := make([]catalog.ProductConfiguration, 0, len(rows))
	for _, row := range rows {
		entry := catalog.ProductConfiguration{
			ID: uuidString(row.ConfigurationID), Kind: row.Kind,
			StyleCode: row.StyleCode, StyleNameAR: row.StyleNameAr,
			ColorCode: row.ColorCode, ColorNameAR: row.ColorNameAr,
			PriceDeltaEGPCents: row.PriceDeltaEgpMinor,
			Enabled:            row.Enabled, Position: int(row.Position), Revision: row.ConfigurationRevision,
		}
		if row.StyleNameEn.Valid {
			value := row.StyleNameEn.String
			entry.StyleNameEN = &value
		}
		if row.ColorNameEn.Valid {
			value := row.ColorNameEn.String
			entry.ColorNameEN = &value
		}
		if row.PriceDeltaUsdMinor.Valid {
			value := row.PriceDeltaUsdMinor.Int64
			entry.PriceDeltaUSDCents = &value
		}
		out = append(out, entry)
	}
	return out, nil
}

// ---- Phase 17 product & physical variants ----
//
// SKU and inventory ownership live on ProductVariant. Two independent
// streams project with the exact Phase 5 machinery: one claim+project
// transaction per event, entity advisory locks, revision gates where
// stale is a terminal no-op, equal-revision semantic comparison,
// Store-scoped ownership (00023 conventions), and product/variant
// dependency waits that are retryable, never terminal.

// decodeProductSnapshotEvent decodes the product stream by wire version:
// v1 carries `sku`; v2 (Phase 17) replaced it with the deprecated
// `primary_variant_sku` display mirror. Both normalize to one shape so a
// single projector, revision gate, and fingerprint serve the stream.
func decodeProductSnapshotEvent(event catalog.EventRecord, payload []byte) (catalog.ProductSnapshot, error) {
	if event.EventType == catalog.EventProductSnapshotV2 {
		return catalog.DecodeProductSnapshotV2(payload)
	}
	return catalog.DecodeProductSnapshot(payload)
}

// validateProductSnapshotEvent validates the decoded product snapshot
// with the wire version's own contract: v2 carries NO SKU-bearing field,
// so the frozen v1 SKU requirement must never run against it (17-R0).
func validateProductSnapshotEvent(event catalog.EventRecord, snapshot catalog.ProductSnapshot) (catalog.ProductSnapshot, error) {
	if event.EventType == catalog.EventProductSnapshotV2 {
		return catalog.ValidateProductSnapshotV2(snapshot)
	}
	return catalog.ValidateProductSnapshot(snapshot)
}

// currentProductVariantSnapshot reconstructs the normalized semantic
// state of a projected variant (identity, prices, attributes) for
// equal-revision comparison. Inventory mirrors never participate.
func currentProductVariantSnapshot(ctx context.Context, q *sqlcgen.Queries, vuid pgtype.UUID) (catalog.NormalizedProductVariant, bool, error) {
	row, err := q.CatalogProductVariantByID(ctx, vuid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.NormalizedProductVariant{}, false, nil
		}
		return catalog.NormalizedProductVariant{}, false, err
	}
	attrs, err := q.CatalogProductVariantAttributes(ctx, vuid)
	if err != nil {
		return catalog.NormalizedProductVariant{}, false, err
	}
	snapshot := catalog.ProductVariantSnapshot{
		VariantID: uuidString(row.VariantID), ProductID: uuidString(row.ProductID),
		SKU: row.Sku, IsActive: row.IsActive, Deleted: row.Deleted,
		Position: int(row.Position), CombinationKey: row.CombinationKey,
		VariantRevision: row.VariantRevision, CatalogRevision: row.CatalogRevision,
	}
	if row.PriceEgpCents.Valid {
		price := row.PriceEgpCents.Int64
		snapshot.PriceEGPCents = &price
	}
	if row.PriceUsdCents.Valid {
		price := row.PriceUsdCents.Int64
		snapshot.PriceUSDCents = &price
	}
	for _, attr := range attrs {
		entry := catalog.VariantAttribute{
			DefinitionCode: attr.DefinitionCode, ValueCode: attr.ValueCode,
			NameAR: attr.NameAr, DefinitionNameAR: attr.DefinitionNameAr,
			Position: int(attr.Position),
		}
		if attr.NameEn.Valid {
			name := attr.NameEn.String
			entry.NameEN = &name
		}
		if attr.DefinitionNameEn.Valid {
			name := attr.DefinitionNameEn.String
			entry.DefinitionNameEN = &name
		}
		snapshot.Attributes = append(snapshot.Attributes, entry)
	}
	return catalog.NormalizeProductVariantSnapshot(snapshot), true, nil
}

// currentProductVariantInventorySnapshot reconstructs the normalized
// semantic state of a projected variant inventory row.
func currentProductVariantInventorySnapshot(ctx context.Context, q *sqlcgen.Queries, vuid pgtype.UUID) (catalog.NormalizedProductVariantInventory, bool, error) {
	row, err := q.CatalogProductVariantInventoryByID(ctx, vuid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.NormalizedProductVariantInventory{}, false, nil
		}
		return catalog.NormalizedProductVariantInventory{}, false, err
	}
	return catalog.NormalizeProductVariantInventorySnapshot(catalog.ProductVariantInventorySnapshot{
		VariantID: uuidString(row.VariantID), ProductID: uuidString(row.ProductID),
		InventoryRevision: row.SourceRevision, StockQuantity: int(row.StockQuantity),
	}), true, nil
}

// ProjectProductVariant projects one catalog.product_variant.snapshot.v1
// with revision ordering and a product dependency wait. Either the
// variant row and its attribute values commit or nothing does. Tombstones
// (`deleted: true`) are stored as rows, never deletes. Legacy (unscoped)
// rows may carry duplicate combination keys: the Store-scoped unique
// constraints never fire for NULL ownership (NULL is never equal to
// NULL), so duplicate combination data is tolerated at the projection
// layer and surfaced as VARIANT_DUPLICATE_COMBINATION health instead.
func (d Devices) ProjectProductVariant(ctx context.Context, event catalog.EventRecord, now time.Time) (catalog.ProjectResult, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	attempt, blocked := startCatalogAttempt(event, now)
	if blocked != nil {
		euid, _ := parseUUID(event.EventID)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductVariantProjectionV1, euid, now, blocked.ErrorCode, "event identity must be UUIDs")
	}
	raw, derr := catalog.DecodeProductVariantSnapshot(attempt.payload)
	if derr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrValidation, safeErr(derr))
	}
	valid, verr := catalog.ValidateProductVariantSnapshot(raw)
	if verr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrValidation, safeErr(verr))
	}
	vuid, err := parseUUID(valid.VariantID)
	if err != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrValidation, "variant_id must be a UUID")
	}
	puid, err := parseUUID(valid.ProductID)
	if err != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrValidation, "product_id must be a UUID")
	}

	tx, err := d.beginCatalogTx(ctx)
	if err != nil {
		return catalog.ProjectResult{}, transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	count, done, hasDone, err := claimCatalogRow(ctx, q, tx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrProjection, "claim failed", err)
	}
	if hasDone {
		if done.Outcome == catalog.OutcomeBlocked {
			return catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: ErrProjection}, nil
		}
		return done, nil
	}

	// Entity locks (R04): own the variant plus its inventory namespace so
	// variant adoption and concurrent variant inventory writes serialize
	// (F02 adoption-race protection, mirroring ProjectProduct).
	if err := lockCatalogEntities(ctx, q,
		[2]string{"product-variant", valid.VariantID},
		[2]string{"product-variant-inventory", valid.VariantID},
	); err != nil {
		_ = tx.Rollback(ctx)
		if isSerializationFailure(err) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict", err)
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrProjection, "entity lock failed", err)
	}

	storedRevision := int64(-1)
	var existingStore pgtype.UUID
	if row, err := q.CatalogProductVariantByID(ctx, vuid); err == nil {
		storedRevision = row.VariantRevision
		existingStore = row.StoreID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		if isSerializationFailure(err) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict", err)
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrProjection, "variant lookup failed", err)
	}
	// Phase 9B ownership gate: see ProjectCategory. A scoped event for a
	// variant owned by another Store blocks here; a scoped event adopts a
	// NULL legacy row by aggregate-ID + revision continuity.
	writeStore, scopeOK := resolveProjectionScope(existingStore, event.StoreID)
	if !scopeOK {
		_ = tx.Rollback(ctx)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "variant owned by another store")
	}
	storeScope := storeUUID(effectiveScope(writeStore, existingStore))
	proceed, stale := revisionGate(valid.VariantRevision, storedRevision)
	if stale {
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}
	if !proceed {
		supersededEqual, err := entitySuperseded(ctx, q,
			catalog.EventProductVariantSnapshotV1, "variant_id", valid.VariantID,
			catalog.ProcessorProductVariantProjectionV1, "variant_revision", valid.VariantRevision, storeScope)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed", err)
		}
		if supersededEqual {
			return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
		}
		currentSnapshot, exists, err := currentProductVariantSnapshot(ctx, q, vuid)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrProjection, "current state read failed", err)
		}
		if !exists || !reflect.DeepEqual(catalog.NormalizeProductVariantSnapshot(valid), currentSnapshot) {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "equal revision with conflicting state")
		}
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}
	superseded, err := entitySuperseded(ctx, q,
		catalog.EventProductVariantSnapshotV1, "variant_id", valid.VariantID,
		catalog.ProcessorProductVariantProjectionV1, "variant_revision", valid.VariantRevision, storeScope)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed", err)
	}
	if superseded {
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}

	// Dependency resolution: the core product must exist. Absence is a
	// retryable wait (out-of-order arrival), never terminal. A product
	// owned by another proven Store can never back this variant.
	productRow, err := q.CatalogProductByID(ctx, puid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrCatalogDependencyWait, "product not yet projected")
		}
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrProjection, "product lookup failed", err)
	}
	if !scopeCompatible(effectiveScope(writeStore, existingStore), storeString(productRow.StoreID)) {
		_ = tx.Rollback(ctx)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "product owned by another store")
	}
	// Phase 9-R1 F02 mirror: adopting a variant into a Store must not
	// leave its inventory or provider mapping owned by another proven
	// Store (see ProjectProduct).
	if adoptingStore(existingStore, writeStore) {
		dependentStores, err := q.CatalogProductVariantDependentStores(ctx, vuid)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrProjection, "variant dependency scope lookup failed", err)
		}
		if !storeDependentsCompatible(writeStore, dependentStores) {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "variant adoption would cross an existing dependent store")
		}
	}

	var priceEGP, priceUSD pgtype.Int8
	if valid.PriceEGPCents != nil {
		priceEGP = pgtype.Int8{Int64: *valid.PriceEGPCents, Valid: true}
	}
	if valid.PriceUSDCents != nil {
		priceUSD = pgtype.Int8{Int64: *valid.PriceUSDCents, Valid: true}
	}
	fingerprint := catalog.FingerprintProductVariant(valid)
	if err := q.UpsertCatalogProductVariant(ctx, sqlcgen.UpsertCatalogProductVariantParams{
		VariantID: vuid, ProductID: puid, Sku: valid.SKU,
		IsActive: valid.IsActive, Deleted: valid.Deleted,
		PriceEgpCents: priceEGP, PriceUsdCents: priceUSD,
		Position: int32(valid.Position), CombinationKey: valid.CombinationKey,
		VariantRevision: valid.VariantRevision, CatalogRevision: valid.CatalogRevision,
		SourceEventID: attempt.euid, SourceDeviceID: attempt.duid,
		SourcePayloadHash: fingerprint[:], SourceReceivedAt: pgTime(event.ReceivedAt),
		StoreID: writeStore,
	}); err != nil {
		_ = tx.Rollback(ctx)
		if isUniqueViolation(err) {
			// A proven-Store identity collision (duplicate SKU or
			// combination) is deterministic, never a crash. Legacy NULL
			// ownership can never collide here (NULLS DISTINCT).
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "variant identity collision")
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrProjection, "variant upsert failed", err)
	}
	if err := q.DeleteCatalogProductVariantAttributes(ctx, vuid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrProjection, "attribute replace failed", err)
	}
	for _, attr := range valid.Attributes {
		var nameEN, defNameEN pgtype.Text
		if attr.NameEN != nil {
			nameEN = pgText(*attr.NameEN)
		}
		if attr.DefinitionNameEN != nil {
			defNameEN = pgText(*attr.DefinitionNameEN)
		}
		if err := q.InsertCatalogProductVariantAttribute(ctx, sqlcgen.InsertCatalogProductVariantAttributeParams{
			VariantID: vuid, DefinitionCode: attr.DefinitionCode, ValueCode: attr.ValueCode,
			NameAr: attr.NameAR, NameEn: nameEN,
			DefinitionNameAr: attr.DefinitionNameAR, DefinitionNameEn: defNameEN,
			Position: int32(attr.Position),
		}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrProjection, "attribute insert failed", err)
		}
	}
	return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
}

// ProjectProductVariantInventory projects one
// inventory.product_variant.snapshot.v1 with revision ordering and a
// variant dependency wait. Either the inventory row commits or nothing
// does; failure leaves the previous revision visible. Inactive/tombstoned
// variants and sell_online=false still project rows: lifecycle and policy
// affect availability, never inventory truth.
func (d Devices) ProjectProductVariantInventory(ctx context.Context, event catalog.EventRecord, now time.Time) (catalog.ProjectResult, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	attempt, blocked := startCatalogAttempt(event, now)
	if blocked != nil {
		euid, _ := parseUUID(event.EventID)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductVariantInventoryProjectionV1, euid, now, blocked.ErrorCode, "event identity must be UUIDs")
	}
	raw, derr := catalog.DecodeProductVariantInventorySnapshot(attempt.payload)
	if derr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, now, ErrValidation, safeErr(derr))
	}
	valid, verr := catalog.ValidateProductVariantInventorySnapshot(raw)
	if verr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, now, ErrValidation, safeErr(verr))
	}
	vuid, err := parseUUID(valid.VariantID)
	if err != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, now, ErrValidation, "variant_id must be a UUID")
	}
	puid, err := parseUUID(valid.ProductID)
	if err != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, now, ErrValidation, "product_id must be a UUID")
	}

	tx, err := d.beginCatalogTx(ctx)
	if err != nil {
		return catalog.ProjectResult{}, transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	count, done, hasDone, err := claimCatalogRow(ctx, q, tx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, now)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, now, ErrProjection, "claim failed", err)
	}
	if hasDone {
		if done.Outcome == catalog.OutcomeBlocked {
			return catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: ErrProjection}, nil
		}
		return done, nil
	}

	// Distinct lock namespace: variant inventory decisions serialize per
	// variant without joining catalog stream lock traffic.
	if err := lockCatalogEntities(ctx, q, [2]string{"product-variant-inventory", valid.VariantID}); err != nil {
		_ = tx.Rollback(ctx)
		if isSerializationFailure(err) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict", err)
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, now, ErrProjection, "entity lock failed", err)
	}
	storedRevision := int64(-1)
	var existingStore pgtype.UUID
	if row, err := q.CatalogProductVariantInventoryByID(ctx, vuid); err == nil {
		storedRevision = row.SourceRevision
		existingStore = row.StoreID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		if isSerializationFailure(err) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict", err)
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, now, ErrProjection, "variant inventory lookup failed", err)
	}
	// Phase 9B ownership gate: see ProjectCategory.
	writeStore, scopeOK := resolveProjectionScope(existingStore, event.StoreID)
	if !scopeOK {
		_ = tx.Rollback(ctx)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "variant inventory owned by another store")
	}
	proceed, stale := revisionGate(valid.InventoryRevision, storedRevision)
	if stale {
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}
	if !proceed {
		supersededEqual, err := entitySuperseded(ctx, q,
			catalog.EventInventoryProductVariantSnapshotV1, "variant_id", valid.VariantID,
			catalog.ProcessorProductVariantInventoryProjectionV1, "inventory_revision", valid.InventoryRevision, storeUUID(event.StoreID))
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed", err)
		}
		if supersededEqual {
			return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
		}
		currentSnapshot, exists, err := currentProductVariantInventorySnapshot(ctx, q, vuid)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, now, ErrProjection, "current state read failed", err)
		}
		if !exists || !reflect.DeepEqual(catalog.NormalizeProductVariantInventorySnapshot(valid), currentSnapshot) {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "equal revision with conflicting state")
		}
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}
	superseded, err := entitySuperseded(ctx, q,
		catalog.EventInventoryProductVariantSnapshotV1, "variant_id", valid.VariantID,
		catalog.ProcessorProductVariantInventoryProjectionV1, "inventory_revision", valid.InventoryRevision, storeUUID(event.StoreID))
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed", err)
	}
	if superseded {
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}

	// Dependency resolution: the variant must exist. Absence is a
	// retryable wait (out-of-order arrival), never terminal. A variant
	// owned by another proven Store can never back this inventory.
	variantRow, err := q.CatalogProductVariantByID(ctx, vuid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, now, ErrCatalogDependencyWait, "variant not yet projected")
		}
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, now, ErrProjection, "variant lookup failed", err)
	}
	if !scopeCompatible(effectiveScope(writeStore, existingStore), storeString(variantRow.StoreID)) {
		_ = tx.Rollback(ctx)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "variant owned by another store")
	}

	var allocation pgtype.Int8
	if valid.OnlineAllocationLimit != nil {
		allocation = pgtype.Int8{Int64: int64(*valid.OnlineAllocationLimit), Valid: true}
	}
	fingerprint := catalog.FingerprintProductVariantInventory(valid)
	if err := q.UpsertCatalogProductVariantInventory(ctx, sqlcgen.UpsertCatalogProductVariantInventoryParams{
		VariantID: vuid, ProductID: puid, Sku: valid.SKU,
		StockQuantity: int64(valid.StockQuantity),
		Ready:         valid.Ready, SellOnline: valid.SellOnline, OnlineAllocationLimit: allocation,
		PolicyRevision: valid.PolicyRevision, CatalogRevision: valid.CatalogRevision,
		SourceRevision: valid.InventoryRevision,
		SourceEventID:  attempt.euid, SourceDeviceID: attempt.duid,
		SourcePayloadHash: fingerprint[:], SourceReceivedAt: pgTime(event.ReceivedAt),
		StoreID: writeStore,
	}); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, now, ErrProjection, "variant inventory upsert failed", err)
	}
	return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
}

// CatalogProductVariants returns the projected variants of one product
// with attributes and last-known stock. Tombstoned variants are excluded
// (historical rows stay queryable through admin/health surfaces).
func (d Devices) CatalogProductVariants(ctx context.Context, productID string) ([]catalog.Variant, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	puid, err := parseUUID(productID)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "product_id must be a UUID")
	}
	q := sqlcgen.New(d.pool)
	rows, err := q.CatalogProductVariantsByProduct(ctx, puid)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "catalog variants", redact(err))
	}
	out := make([]catalog.Variant, 0, len(rows))
	for _, row := range rows {
		if row.Deleted {
			continue
		}
		variant := catalog.Variant{
			VariantID: uuidString(row.VariantID), ProductID: uuidString(row.ProductID),
			SKU: row.Sku, IsActive: row.IsActive,
			Position: int(row.Position), CombinationKey: row.CombinationKey,
			VariantRevision: row.VariantRevision, CatalogRevision: row.CatalogRevision,
			SourceEventID: uuidString(row.SourceEventID), Attributes: []catalog.VariantAttribute{},
		}
		if row.PriceEgpCents.Valid {
			price := row.PriceEgpCents.Int64
			variant.PriceEGPCents = &price
		}
		if row.PriceUsdCents.Valid {
			price := row.PriceUsdCents.Int64
			variant.PriceUSDCents = &price
		}
		attrs, err := q.CatalogProductVariantAttributes(ctx, row.VariantID)
		if err != nil {
			return nil, apperr.Wrap(apperr.Internal, "catalog variant attributes", redact(err))
		}
		for _, attr := range attrs {
			entry := catalog.VariantAttribute{
				DefinitionCode: attr.DefinitionCode, ValueCode: attr.ValueCode,
				NameAR: attr.NameAr, DefinitionNameAR: attr.DefinitionNameAr,
				Position: int(attr.Position),
			}
			if attr.NameEn.Valid {
				name := attr.NameEn.String
				entry.NameEN = &name
			}
			if attr.DefinitionNameEn.Valid {
				name := attr.DefinitionNameEn.String
				entry.DefinitionNameEN = &name
			}
			variant.Attributes = append(variant.Attributes, entry)
		}
		if inv, err := q.CatalogProductVariantInventoryByID(ctx, row.VariantID); err == nil {
			stock := int(inv.StockQuantity)
			variant.StockQuantity = &stock
			variant.InventoryRevision = inv.SourceRevision
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, apperr.Wrap(apperr.Internal, "catalog variant inventory", redact(err))
		}
		out = append(out, variant)
	}
	return out, nil
}

// CatalogProductVariantInventory returns the projected last-known
// variant inventory with its policy/catalog mirrors.
func (d Devices) CatalogProductVariantInventory(ctx context.Context, id string) (catalog.VariantInventory, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	vuid, err := parseUUID(id)
	if err != nil {
		return catalog.VariantInventory{}, catalogNotFound("variant inventory")
	}
	row, err := sqlcgen.New(d.pool).CatalogProductVariantInventoryByID(ctx, vuid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.VariantInventory{}, catalogNotFound("variant inventory")
		}
		return catalog.VariantInventory{}, apperr.Wrap(apperr.Internal, "catalog variant inventory", redact(err))
	}
	out := catalog.VariantInventory{
		VariantID: uuidString(row.VariantID), ProductID: uuidString(row.ProductID),
		SKU: row.Sku, StockQuantity: int(row.StockQuantity), Revision: row.SourceRevision,
		Ready: row.Ready, SellOnline: row.SellOnline,
		PolicyRevision: row.PolicyRevision, CatalogRevision: row.CatalogRevision,
		SourceEventID:    uuidString(row.SourceEventID),
		SourceReceivedAt: row.SourceReceivedAt.Time, ProjectedAt: row.ProjectedAt.Time,
	}
	if row.OnlineAllocationLimit.Valid {
		limit := int(row.OnlineAllocationLimit.Int64)
		out.OnlineAllocationLimit = &limit
	}
	return out, nil
}

// CatalogProductVariantAvailability derives per-variant provider-neutral
// ONLINE availability from current variant, product, policy, and variant
// inventory rows in one read transaction (no writes). The variant-level
// policy mirror (sell_online, online_allocation_limit) carried by the
// inventory stream tightens the product-level policy when present.
func (d Devices) CatalogProductVariantAvailability(ctx context.Context, id string) (catalog.VariantAvailability, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	vuid, err := parseUUID(id)
	if err != nil {
		return catalog.VariantAvailability{}, catalogNotFound("availability")
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return catalog.VariantAvailability{}, apperr.Wrap(apperr.Internal, "catalog variant availability", redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row, err := sqlcgen.New(tx).CatalogVariantAvailabilityByVariantID(ctx, vuid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.VariantAvailability{VariantID: id, MissingVariant: true}, nil
		}
		return catalog.VariantAvailability{}, apperr.Wrap(apperr.Internal, "catalog variant availability", redact(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return catalog.VariantAvailability{}, apperr.Wrap(apperr.Internal, "catalog variant availability", redact(err))
	}
	availability := catalog.VariantAvailability{
		VariantID: uuidString(row.VariantID), ProductID: uuidString(row.ProductID),
		VariantActive:  row.VariantActive && !row.VariantDeleted,
		MissingProduct: !row.ProductActive.Valid, MissingPolicy: !row.SellOnline.Valid,
		MissingInventory: !row.StockQuantity.Valid,
	}
	productActive := row.ProductActive.Valid && row.ProductActive.Bool
	sellOnline := row.SellOnline.Valid && row.SellOnline.Bool
	if availability.MissingPolicy {
		sellOnline = false
	}
	// Effective variant channel policy: the variant inventory mirror
	// tightens (never widens) the product policy.
	if row.VariantSellOnline.Valid && !row.VariantSellOnline.Bool {
		sellOnline = false
	}
	availability.ProductActive = productActive
	availability.SellOnline = sellOnline
	limit := row.OnlineAllocationLimit
	if row.VariantAllocationLimit.Valid {
		limit = row.VariantAllocationLimit
	}
	if limit.Valid {
		cap := int(limit.Int64)
		availability.OnlineAllocationLimit = &cap
	}
	var stock int
	inventoryFound := row.StockQuantity.Valid
	if inventoryFound {
		stock = int(row.StockQuantity.Int64)
		quantity := stock
		availability.StockQuantity = &quantity
		availability.InventoryRevision = row.InventoryRevision.Int64
		availability.InventoryEventID = uuidString(row.InventoryEventID)
		if row.InventoryProjectedAt.Valid {
			projected := row.InventoryProjectedAt.Time
			availability.InventoryProjectedAt = &projected
		}
	}
	availability.OnlineAvailable = catalog.ComputeVariantAvailability(
		productActive && sellOnline, availability.VariantActive,
		stock, availability.OnlineAllocationLimit,
	)
	availability.Ready = !availability.MissingProduct && !availability.MissingPolicy && !availability.MissingInventory
	return availability, nil
}

// ProjectProductType projects one catalog.product_type.snapshot.v1 with
// revision ordering (ADR-0050 §75). Types are roots (no product
// dependency): the only waits are store scoping and revision order.
// Either the type row plus its dimension/capability sets commit, or
// nothing does; failure leaves the previous revision visible.
func (d Devices) ProjectProductType(ctx context.Context, event catalog.EventRecord, now time.Time) (catalog.ProjectResult, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	attempt, blocked := startCatalogAttempt(event, now)
	if blocked != nil {
		euid, _ := parseUUID(event.EventID)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductTypeProjectionV1, euid, now, blocked.ErrorCode, "event identity must be UUIDs")
	}
	raw, derr := catalog.DecodeProductTypeSnapshot(attempt.payload)
	if derr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, now, ErrValidation, safeErr(derr))
	}
	valid, verr := catalog.ValidateProductTypeSnapshot(raw)
	if verr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, now, ErrValidation, safeErr(verr))
	}
	if reservedProductTypeStorageID(valid.ProductTypeID, storeUUID(event.StoreID)) {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "product type storage identity is reserved")
	}
	tuid, err := parseUUID(valid.ProductTypeID)
	if err != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, now, ErrValidation, "product_type_id must be a UUID")
	}

	tx, err := d.beginCatalogTx(ctx)
	if err != nil {
		return catalog.ProjectResult{}, transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	count, done, hasDone, err := claimCatalogRow(ctx, q, tx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, now)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, now, ErrProjection, "claim failed", err)
	}
	if hasDone {
		if done.Outcome == catalog.OutcomeBlocked {
			return catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: ErrProjection}, nil
		}
		return done, nil
	}

	projectedID, err := projectedProductTypeID(ctx, q, valid.ProductTypeID, storeUUID(event.StoreID))
	if err != nil {
		_ = tx.Rollback(ctx)
		if errors.Is(err, ErrProductTypeIdentityCollision) {
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, productTypeCollisionMessage(err))
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, now, ErrProjection, "type identity lookup failed", err)
	}
	tuid, _ = parseUUID(projectedID)
	projectedValid := valid
	projectedValid.ProductTypeID = projectedID
	// Entity lock: own the type row so concurrent type writes serialize.
	if err := lockCatalogEntities(ctx, q,
		[2]string{"product-type", projectedID},
	); err != nil {
		_ = tx.Rollback(ctx)
		if isSerializationFailure(err) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict", err)
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, now, ErrProjection, "entity lock failed", err)
	}

	storedRevision := int64(-1)
	var existingStore pgtype.UUID
	if row, err := q.CatalogProductTypeByID(ctx, tuid); err == nil {
		storedRevision = row.TypeRevision
		existingStore = row.StoreID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		if isSerializationFailure(err) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict", err)
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, now, ErrProjection, "type lookup failed", err)
	}
	// Phase 9B ownership gate: a scoped event for a type owned by another
	// Store blocks here; a scoped event adopts a NULL legacy row by
	// aggregate-ID + revision continuity.
	writeStore, scopeOK := resolveProjectionScope(existingStore, event.StoreID)
	if !scopeOK {
		_ = tx.Rollback(ctx)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "product type owned by another store")
	}
	storeScope := storeUUID(effectiveScope(writeStore, existingStore))
	proceed, stale := revisionGate(valid.TypeRevision, storedRevision)
	if stale {
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}
	if !proceed {
		supersededEqual, err := entitySuperseded(ctx, q,
			catalog.EventProductTypeSnapshotV1, "product_type_id", valid.ProductTypeID,
			catalog.ProcessorProductTypeProjectionV1, "type_revision", valid.TypeRevision, storeScope)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed", err)
		}
		if supersededEqual {
			return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
		}
		currentSnapshot, exists, err := currentProductTypeSnapshot(ctx, q, tuid)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, now, ErrProjection, "current state read failed", err)
		}
		if !exists || !reflect.DeepEqual(catalog.NormalizeProductTypeSnapshot(projectedValid), currentSnapshot) {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "equal revision with conflicting state")
		}
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}
	superseded, err := entitySuperseded(ctx, q,
		catalog.EventProductTypeSnapshotV1, "product_type_id", valid.ProductTypeID,
		catalog.ProcessorProductTypeProjectionV1, "type_revision", valid.TypeRevision, storeScope)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed", err)
	}
	if superseded {
		return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}

	var descriptionAR, descriptionEN pgtype.Text
	if valid.DescriptionAR != nil {
		descriptionAR = pgText(*valid.DescriptionAR)
	}
	if valid.DescriptionEN != nil {
		descriptionEN = pgText(*valid.DescriptionEN)
	}
	fingerprint := catalog.FingerprintProductType(valid)
	if err := q.UpsertCatalogProductType(ctx, sqlcgen.UpsertCatalogProductTypeParams{
		TypeID: tuid, Code: valid.Code, NameAr: valid.NameAR, NameEn: valid.NameEN,
		DescriptionAr: descriptionAR, DescriptionEn: descriptionEN,
		IsActive: valid.IsActive, Position: int32(valid.Position),
		TypeRevision:  valid.TypeRevision,
		SourceEventID: attempt.euid, SourceDeviceID: attempt.duid,
		SourcePayloadHash: fingerprint[:], SourceReceivedAt: pgTime(event.ReceivedAt),
		StoreID: writeStore,
	}); err != nil {
		_ = tx.Rollback(ctx)
		if isUniqueViolation(err) {
			// A proven-Store identity collision (duplicate code) is
			// deterministic, never a crash. Legacy NULL ownership can
			// never collide here (NULLS DISTINCT).
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "product type identity collision")
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, now, ErrProjection, "type upsert failed", err)
	}
	if err := q.DeleteCatalogProductTypeDimensions(ctx, tuid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, now, ErrProjection, "dimension replace failed", err)
	}
	for position, code := range valid.Dimensions {
		if err := q.InsertCatalogProductTypeDimension(ctx, sqlcgen.InsertCatalogProductTypeDimensionParams{
			TypeID: tuid, DefinitionCode: code, Position: int32(position),
		}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, now, ErrProjection, "dimension insert failed", err)
		}
	}
	if err := q.DeleteCatalogProductTypeCapabilities(ctx, tuid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, now, ErrProjection, "capability replace failed", err)
	}
	for _, code := range valid.Capabilities {
		if err := q.InsertCatalogProductTypeCapability(ctx, sqlcgen.InsertCatalogProductTypeCapabilityParams{
			TypeID: tuid, CapabilityCode: code,
		}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, now, ErrProjection, "capability insert failed", err)
		}
	}
	return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductTypeProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
}

// currentProductTypeSnapshot reads the canonical current state for
// equal-revision conflict comparison.
func currentProductTypeSnapshot(ctx context.Context, q *sqlcgen.Queries, tuid pgtype.UUID) (catalog.NormalizedProductType, bool, error) {
	row, err := q.CatalogProductTypeByID(ctx, tuid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.NormalizedProductType{}, false, nil
		}
		return catalog.NormalizedProductType{}, false, err
	}
	dims, err := q.CatalogProductTypeDimensions(ctx, tuid)
	if err != nil {
		return catalog.NormalizedProductType{}, false, err
	}
	caps, err := q.CatalogProductTypeCapabilities(ctx, tuid)
	if err != nil {
		return catalog.NormalizedProductType{}, false, err
	}
	var descAR, descEN *string
	if row.DescriptionAr.Valid {
		descAR = &row.DescriptionAr.String
	}
	if row.DescriptionEn.Valid {
		descEN = &row.DescriptionEn.String
	}
	return catalog.NormalizedProductType{
		ProductTypeID: uuidString(tuid), Code: row.Code,
		NameAR: row.NameAr, NameEN: row.NameEn,
		DescriptionAR: descAR, DescriptionEN: descEN,
		IsActive: row.IsActive, Position: int(row.Position),
		Dimensions: dims, Capabilities: caps, Revision: row.TypeRevision,
	}, true, nil
}
