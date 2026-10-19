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

// ---- Phase 17 product & physical variants ----
//
// SKU and inventory ownership live on ProductVariant. Two independent
// streams project with the exact Phase 5 machinery: one claim+project
// transaction per event, entity advisory locks, revision gates where
// stale is a terminal no-op, equal-revision semantic comparison,
// Store-scoped ownership (00023 conventions), and product/variant
// dependency waits that are retryable, never terminal.

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
	prices, err := q.CatalogProductVariantPrices(ctx, vuid)
	if err != nil {
		return catalog.NormalizedProductVariant{}, false, err
	}
	for _, price := range prices {
		snapshot.Prices = append(snapshot.Prices, catalog.VariantPriceOverride{
			Currency: price.Currency, PriceCents: int8Pointer(price.PriceMinor), CostCents: int8Pointer(price.CostMinor),
		})
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
func (d CatalogProjection) ProjectProductVariant(ctx context.Context, event catalog.EventRecord, now time.Time) (catalog.ProjectResult, error) {
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

	fingerprint := catalog.FingerprintProductVariant(valid)
	if err := q.UpsertCatalogProductVariant(ctx, sqlcgen.UpsertCatalogProductVariantParams{
		VariantID: vuid, ProductID: puid, Sku: valid.SKU,
		IsActive: valid.IsActive, Deleted: valid.Deleted,
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
	if err := q.DeleteCatalogProductVariantPrices(ctx, vuid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrProjection, "price replace failed", err)
	}
	for _, price := range valid.Prices {
		if err := q.InsertCatalogProductVariantPrice(ctx, sqlcgen.InsertCatalogProductVariantPriceParams{
			VariantID: vuid, Currency: price.Currency,
			PriceMinor: int8FromPtr(price.PriceCents), CostMinor: int8FromPtr(price.CostCents),
		}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantProjectionV1, attempt.euid, now, ErrProjection, "price insert failed", err)
		}
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
func (d CatalogProjection) ProjectProductVariantInventory(ctx context.Context, event catalog.EventRecord, now time.Time) (catalog.ProjectResult, error) {
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

	if variantRow.ProductID != puid || variantRow.Sku != valid.SKU {
		_ = tx.Rollback(ctx)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, now, ErrValidation, "inventory does not match variant identity")
	}

	// Read the parent in this serializable decision as well as the Variant.
	// Product adoption racing a foreign inventory write must not both commit.
	productRow, err := q.CatalogProductByID(ctx, puid)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, now, ErrProjection, "inventory product lookup failed", err)
	}
	if !scopeCompatible(effectiveScope(writeStore, existingStore), storeString(productRow.StoreID)) {
		_ = tx.Rollback(ctx)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductVariantInventoryProjectionV1, attempt.euid, now, ErrStoreScopeConflict, "inventory product owned by another store")
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
