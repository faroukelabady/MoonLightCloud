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

// ProjectProductType projects one catalog.product_type.snapshot.v1 with
// revision ordering (ADR-0050 §75). Types are roots (no product
// dependency): the only waits are store scoping and revision order.
// Either the type row plus its dimension/capability sets commit, or
// nothing does; failure leaves the previous revision visible.
func (d CatalogProjection) ProjectProductType(ctx context.Context, event catalog.EventRecord, now time.Time) (catalog.ProjectResult, error) {
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
	// Compare both sides through the same set normalization. Dimensions are
	// stored in display order, which can differ from lexical comparison order.
	return catalog.NormalizeProductTypeSnapshot(catalog.ProductTypeSnapshot{
		ProductTypeID: uuidString(tuid), Code: row.Code,
		NameAR: row.NameAr, NameEN: row.NameEn,
		DescriptionAR: descAR, DescriptionEN: descEN,
		IsActive: row.IsActive, Position: int(row.Position),
		Dimensions: dims, Capabilities: caps, TypeRevision: row.TypeRevision,
	}), true, nil
}
