package postgres

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

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
	parentIDs := make([]string, 0, len(parents))
	for _, parent := range parents {
		parentIDs = append(parentIDs, uuidString(parent))
	}
	return catalog.NormalizeCategorySnapshot(catalog.CategorySnapshot{
		CategoryID: uuidString(row.CategoryID), Status: row.Status,
		OnlineEnabled: row.OnlineEnabled,
		NameAR:        row.NameAr, NameEN: optionalTextPtr(row.NameEn), ParentIDs: parentIDs, CatalogRevision: row.SourceRevision,
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
	return catalog.NormalizeTagSnapshot(catalog.TagSnapshot{
		TagID: uuidString(row.TagID), Slug: row.Slug, IsActive: row.IsActive,
		NameAR: row.NameAr, NameEN: optionalTextPtr(row.NameEn), CatalogRevision: row.SourceRevision,
	}), true, nil
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
	raw, derr := catalog.DecodeCategorySnapshotV2(attempt.payload)
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
	finishNoop := func() (catalog.ProjectResult, error) {
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
			catalog.EventCategorySnapshotV2, "category_id", valid.CategoryID,
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
		catalog.EventCategorySnapshotV2, "category_id", valid.CategoryID,
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
	if err := catalog.CheckCategoryGraph(allEdges, projectedID, projectedParentIDs); err != nil {
		_ = tx.Rollback(ctx)
		code := ErrCatalogCategoryCycle
		if errors.Is(err, catalog.ErrGraphDepth) {
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
	descendants := catalog.CategoryDescendants(allEdges, projectedID)
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

	fingerprint := catalog.FingerprintCategory(valid)
	nameAR, nameEN := valid.NameAR, pgTextPtr(valid.NameEN)
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
		StoreID: writeStore,
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
	finishNoop := func() (catalog.ProjectResult, error) {
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

	nameAR, nameEN := valid.NameAR, pgTextPtr(valid.NameEN)
	fingerprint := catalog.FingerprintTag(valid)
	if err := q.UpsertCatalogTag(ctx, sqlcgen.UpsertCatalogTagParams{
		TagID: tuid, Slug: valid.Slug, IsActive: valid.IsActive,
		NameAr: nameAR, NameEn: nameEN,
		SourceRevision: valid.CatalogRevision, SourceEventID: attempt.euid, SourceDeviceID: attempt.duid,
		SourcePayloadHash: fingerprint[:], SourceReceivedAt: pgTime(event.ReceivedAt),
		StoreID: writeStore,
	}); err != nil {
		_ = tx.Rollback(ctx)
		if isUniqueViolation(err) {
			return d.markCatalogBlocked(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrCatalogRevisionConflict, "tag identity collision")
		}
		return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "tag upsert failed", err)
	}
	return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorTagProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
}
