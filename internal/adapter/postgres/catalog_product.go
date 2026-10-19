package postgres

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

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
	subs, err := q.CatalogProductSubcategories(ctx, puid)
	if err != nil {
		return catalog.NormalizedProduct{}, false, err
	}
	tags, err := q.CatalogProductTags(ctx, puid)
	if err != nil {
		return catalog.NormalizedProduct{}, false, err
	}
	snapshot := catalog.ProductSnapshot{
		ProductID: uuidString(row.ProductID), ProductTypeID: uuidString(row.ProductTypeID),
		NameAR: row.NameAr, NameEN: optionalTextPtr(row.NameEn),
		DescriptionAR: optionalTextPtr(row.DescriptionAr), DescriptionEN: optionalTextPtr(row.DescriptionEn),
		TopCategoryID: uuidString(row.TopCategoryID), IsActive: row.IsActive,
		CatalogRevision: row.SourceRevision,
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
	for _, sub := range subs {
		snapshot.SubcategoryIDs = append(snapshot.SubcategoryIDs, uuidString(sub))
	}
	for _, tag := range tags {
		snapshot.TagIDs = append(snapshot.TagIDs, uuidString(tag))
	}
	return catalog.NormalizeProductSnapshot(snapshot), true, nil
}

// ProjectProduct projects one product snapshot with revision ordering,
// dependency waits, and structural checks. Either a complete product
// projection commits or nothing does; failure leaves the previous
// revision visible.
func (d CatalogProjection) ProjectProduct(ctx context.Context, event catalog.EventRecord, now time.Time) (catalog.ProjectResult, error) {
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
			catalog.EventProductSnapshotV2, "product_id", valid.ProductID,
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
		catalog.EventProductSnapshotV2, "product_id", valid.ProductID,
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
		ProductID: puid, NameAr: valid.NameAR, NameEn: pgTextPtr(valid.NameEN),
		DescriptionAr: pgTextPtr(valid.DescriptionAR), DescriptionEn: pgTextPtr(valid.DescriptionEN),
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "reevaluation enqueue failed: "+err.Error(), err)
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
func (d CatalogProjection) ProjectProductSalesPolicy(ctx context.Context, event catalog.EventRecord, now time.Time) (catalog.ProjectResult, error) {
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

// decodeProductSnapshotEvent decodes the one current product wire contract.
func decodeProductSnapshotEvent(_ catalog.EventRecord, payload []byte) (catalog.ProductSnapshot, error) {
	return catalog.DecodeProductSnapshotV2(payload)
}

func validateProductSnapshotEvent(_ catalog.EventRecord, snapshot catalog.ProductSnapshot) (catalog.ProductSnapshot, error) {
	return catalog.ValidateProductSnapshotV2(snapshot)
}

// int8Pointer maps a nullable BIGINT to an optional int64.
func int8Pointer(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	v := value.Int64
	return &v
}
