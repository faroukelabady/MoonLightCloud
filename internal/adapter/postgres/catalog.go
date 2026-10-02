package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
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
		Names: names, ParentIDs: parentIDs, CatalogRevision: row.SourceRevision,
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
		ProductID: uuidString(row.ProductID), SKU: row.Sku, Name: row.Name,
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
	if eventType == catalog.EventCategorySnapshotV1 {
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
	case catalog.EventCategorySnapshotV1:
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
		return d.persistCatalogRetry(ctx, processor, euid, now, ErrProjection, "claim failed")
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

// persistCatalogRetry writes retry state in a SEPARATE durable transaction
// after the projection transaction rolled back, mirroring the sale/return
// discipline: the schedule commits even though the projection did not.
func (d Devices) persistCatalogRetry(ctx context.Context, processor string, euid pgtype.UUID, now time.Time, code, msg string) (catalog.ProjectResult, error) {
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
	next := now.Add(catalog.Backoff(int(attempt)))
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
	raw, derr := catalog.DecodeCategorySnapshot(attempt.payload)
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
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "claim failed")
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict")
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "entity lock failed")
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict")
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "category lookup failed")
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
				return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "default transition failed")
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed")
		}
		if supersededEqual {
			return finishNoop()
		}
		currentSnapshot, exists, err := currentCategorySnapshot(ctx, q, cuid)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "current state read failed")
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
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed")
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
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "parent scope lookup failed")
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "parent scope lookup failed")
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "child scope lookup failed")
		}
		if !storeDependentsCompatible(writeStore, childStores) {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "category adoption would cross an existing child store edge")
		}
		productStores, err := q.CatalogCategoryProductStores(ctx, cuid)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "referencing product scope lookup failed")
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
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "edge lookup failed")
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
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "parent comparison failed")
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
				return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "affected product lookup failed")
			}
			for _, productID := range productIDs {
				affectedKeys = append(affectedKeys, [2]string{"product", uuidString(productID)})
			}
		}
		if err := lockCatalogEntities(ctx, q, affectedKeys...); err != nil {
			_ = tx.Rollback(ctx)
			if isSerializationFailure(err) {
				return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict")
			}
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "entity lock failed")
		}
		// Only a product that would ACTUALLY become structurally invalid
		// under the proposed graph requires a repair. A removal that leaves
		// every referencing product reachable (e.g. dropping one of several
		// parents, or removing an edge the product's top never used) is a
		// valid frozen-Retail operation and must not block (F09).
		repairable, err := categoryChangeRepairable(ctx, q, allEdges, projectedID, projectedParentIDs)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "repair check failed")
		}
		if !repairable {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrCatalogGraphConflict, "category change would orphan current products with no accepted repair")
		}
	}

	if sharedNode {
		if _, err := q.RetireRawDefaultCategory(ctx, sqlcgen.RetireRawDefaultCategoryParams{CategoryID: mustParseUUID(valid.CategoryID), StoreID: eventWriteStore}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "default transition failed")
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
	if err := q.UpsertCatalogCategory(ctx, sqlcgen.UpsertCatalogCategoryParams{
		CategoryID: cuid, Status: valid.Status, NameAr: nameAR, NameEn: nameEN,
		SourceRevision: valid.CatalogRevision, SourceEventID: attempt.euid, SourceDeviceID: attempt.duid,
		SourcePayloadHash: fingerprint[:], SourceReceivedAt: pgTime(event.ReceivedAt),
		StoreID: writeStore, DefaultAlgorithm: defaultAlgorithm,
	}); err != nil {
		_ = tx.Rollback(ctx)
		if isUniqueViolation(err) {
			return d.markCatalogBlocked(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "category identity collision")
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "category upsert failed")
	}
	if err := q.DeleteCatalogCategoryEdges(ctx, cuid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "edge replace failed")
	}
	for position, parent := range resolvedParents {
		puid, _ := parseUUID(parent.projected)
		if err := q.InsertCatalogCategoryEdge(ctx, sqlcgen.InsertCatalogCategoryEdgeParams{
			ParentID: puid, ChildID: cuid, Position: int32(position),
		}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "edge insert failed")
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict")
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
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "top category lookup failed")
	}
	verdict, err := d.checkProductStructure(ctx, q, valid)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "structure evaluation failed")
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
		return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "claim failed")
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict")
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "entity lock failed")
	}
	storedRevision := int64(-1)
	var existingStore pgtype.UUID
	if row, err := q.CatalogTagByID(ctx, tuid); err == nil {
		storedRevision = row.SourceRevision
		existingStore = row.StoreID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		if isSerializationFailure(err) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict")
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "tag lookup failed")
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
				return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "default transition failed")
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed")
		}
		if supersededEqual {
			return finishNoop()
		}
		currentSnapshot, exists, err := currentTagSnapshot(ctx, q, tuid)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "current state read failed")
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
		return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed")
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "tag product scope lookup failed")
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "default transition failed")
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
		return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "tag upsert failed")
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
	raw, derr := catalog.DecodeProductSnapshot(attempt.payload)
	if derr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrValidation, safeErr(derr))
	}
	valid, verr := catalog.ValidateProductSnapshot(raw)
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

	tx, err := d.beginCatalogTx(ctx)
	if err != nil {
		return catalog.ProjectResult{}, transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	count, done, hasDone, err := claimCatalogRow(ctx, q, tx, catalog.ProcessorProductProjectionV1, attempt.euid, now)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "claim failed")
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict")
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "entity lock failed")
	}

	storedRevision := int64(-1)
	var existingStore pgtype.UUID
	if row, err := q.CatalogProductByID(ctx, puid); err == nil {
		storedRevision = row.SourceRevision
		existingStore = row.StoreID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		if isSerializationFailure(err) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict")
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "product lookup failed")
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed")
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "current state read failed")
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
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed")
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
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "top category lookup failed")
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "subcategory lookup failed")
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "tag lookup failed")
		}
		if !tagScopeCompatible(effStore, tagID, storeString(tagRow.StoreID)) {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "tag owned by another store")
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "product dependency scope lookup failed")
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
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "structure evaluation failed")
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
	if err := q.UpsertCatalogProduct(ctx, sqlcgen.UpsertCatalogProductParams{
		ProductID: puid, Sku: valid.SKU, Name: valid.Name, Description: description,
		TopCategoryID: topUID, WidthCm: width, HeightCm: height, IsActive: valid.IsActive,
		SourceRevision: valid.CatalogRevision, SourceEventID: attempt.euid, SourceDeviceID: attempt.duid,
		SourcePayloadHash: fingerprint[:], SourceReceivedAt: pgTime(event.ReceivedAt),
		StoreID: writeStore,
	}); err != nil {
		_ = tx.Rollback(ctx)
		if isUniqueViolation(err) {
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "product identity collision")
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "product upsert failed")
	}
	if err := q.DeleteCatalogProductPrices(ctx, puid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "price replace failed")
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "price insert failed")
		}
	}
	if err := q.DeleteCatalogProductTranslations(ctx, puid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "translation replace failed")
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "translation insert failed")
		}
	}
	if err := q.DeleteCatalogProductSubcategories(ctx, puid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "subcategory replace failed")
	}
	for position, sub := range resolvedSubs {
		suid, _ := parseUUID(sub)
		if err := q.InsertCatalogProductSubcategory(ctx, sqlcgen.InsertCatalogProductSubcategoryParams{
			ProductID: puid, CategoryID: suid, Position: int32(position),
		}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "subcategory insert failed")
		}
	}
	if err := q.DeleteCatalogProductTags(ctx, puid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "tag replace failed")
	}
	for _, tagID := range resolvedTags {
		guid, _ := parseUUID(tagID)
		if err := q.InsertCatalogProductTag(ctx, sqlcgen.InsertCatalogProductTagParams{
			ProductID: puid, TagID: guid,
		}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "tag insert failed")
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
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrProjection, "claim failed")
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict")
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrProjection, "entity lock failed")
	}
	storedRevision := int64(-1)
	var existingStore pgtype.UUID
	if row, err := q.CatalogProductSalesPolicyByID(ctx, puid); err == nil {
		storedRevision = row.SourceRevision
		existingStore = row.StoreID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		if isSerializationFailure(err) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrProjection, "policy lookup failed")
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrProjection, "policy lookup failed")
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed")
		}
		if supersededEqual {
			return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
		}
		currentSnapshot, exists, err := currentProductSalesPolicySnapshot(ctx, q, puid)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrProjection, "current state read failed")
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
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed")
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
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrProjection, "product lookup failed")
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
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductSalesPolicyProjectionV1, attempt.euid, now, ErrProjection, "policy upsert failed")
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
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrProjection, "claim failed")
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrProjection, "serialization conflict")
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrProjection, "entity lock failed")
	}
	storedRevision := int64(-1)
	var existingStore pgtype.UUID
	if row, err := q.CatalogProductInventoryByID(ctx, puid); err == nil {
		storedRevision = row.SourceRevision
		existingStore = row.StoreID
	} else if !errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		if isSerializationFailure(err) {
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrProjection, "inventory lookup failed")
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrProjection, "inventory lookup failed")
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed")
		}
		if supersededEqual {
			return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
		}
		currentSnapshot, exists, err := currentProductInventorySnapshot(ctx, q, puid)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrProjection, "current state read failed")
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
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrProjection, "supersede check failed")
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
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrProjection, "product lookup failed")
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
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductInventoryProjectionV1, attempt.euid, now, ErrProjection, "inventory upsert failed")
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
			ID: uuidString(row.ProductID), SKU: row.Sku, Name: row.Name,
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
			ID: uuidString(row.ProductID), SKU: row.Sku, Name: row.Name,
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
	return eventType == catalog.EventCategorySnapshotV1 && isSharedCategoryID(id) || eventType == catalog.EventTagSnapshotV1 && isSharedTagID(id)
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
