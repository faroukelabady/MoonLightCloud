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

// ProjectProductMedia projects one ADR-0056 media snapshot (every gallery of
// one Product at one media revision) in one atomic claim+project
// transaction, with the configuration stream's revision rules: older
// revisions are processed no-ops, an equal revision must match the stored
// galleries, a newer one replaces them. Image bytes are not touched here.
func (d CatalogProjection) ProjectProductMedia(ctx context.Context, event catalog.EventRecord, now time.Time) (catalog.ProjectResult, error) {
	const processor = catalog.ProcessorProductMediaProjectionV1
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	attempt, blocked := startCatalogAttempt(event, now)
	if blocked != nil {
		euid, _ := parseUUID(event.EventID)
		return d.markCatalogBlocked(ctx, processor, euid, now, blocked.ErrorCode, "event identity must be UUIDs")
	}
	raw, derr := catalog.DecodeProductMediaSnapshot(attempt.payload)
	if derr != nil {
		return d.markCatalogBlocked(ctx, processor, attempt.euid, now, ErrValidation, safeErr(derr))
	}
	valid, verr := catalog.ValidateProductMediaSnapshot(raw)
	if verr != nil {
		return d.markCatalogBlocked(ctx, processor, attempt.euid, now, ErrValidation, safeErr(verr))
	}
	puid, _ := parseUUID(valid.ProductID)
	tx, err := d.beginCatalogTx(ctx)
	if err != nil {
		return catalog.ProjectResult{}, transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	count, done, hasDone, err := claimCatalogRow(ctx, q, tx, processor, attempt.euid, now)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, processor, attempt.euid, now, ErrProjection, "claim failed", err)
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
			return d.persistCatalogRetry(ctx, processor, attempt.euid, now, ErrProjection, "serialization conflict", err)
		}
		return d.persistCatalogRetry(ctx, processor, attempt.euid, now, ErrProjection, "entity lock failed", err)
	}
	productRow, err := q.CatalogProductByID(ctx, puid)
	if err != nil {
		_ = tx.Rollback(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			return d.persistCatalogRetry(ctx, processor, attempt.euid, now, ErrCatalogDependencyWait, "product not yet projected")
		}
		return d.persistCatalogRetry(ctx, processor, attempt.euid, now, ErrProjection, "product lookup failed", err)
	}
	// The trusted ingress Store must own the Product; payloads never name it.
	if _, scopeOK := resolveProjectionScope(productRow.StoreID, event.StoreID); !scopeOK {
		_ = tx.Rollback(ctx)
		return d.markCatalogBlocked(ctx, processor, attempt.euid, now, ErrStoreScopeConflict, "media event not authorized for product store")
	}
	storedRevision, err := q.CatalogProductMediaRevision(ctx, puid)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, processor, attempt.euid, now, ErrProjection, "media revision read failed", err)
	}
	if valid.MediaRevision < storedRevision {
		return finishCatalogAttempt(ctx, q, tx, processor, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}
	storedRows, err := q.CatalogProductMediaByProduct(ctx, puid)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, processor, attempt.euid, now, ErrProjection, "media state read failed", err)
	}
	if valid.MediaRevision == storedRevision {
		if !reflect.DeepEqual(catalog.NormalizeProductMedia(valid.Images), storedProductMedia(storedRows)) {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, processor, attempt.euid, now, ErrCatalogRevisionConflict, "equal revision with conflicting state")
		}
		return finishCatalogAttempt(ctx, q, tx, processor, attempt.euid, count, now, catalog.ProcProcessed, "", "")
	}
	// Variant galleries need their Variant: missing waits, foreign blocks.
	checked := map[string]bool{}
	imageIDs := make([]pgtype.UUID, 0, len(valid.Images))
	for _, entry := range valid.Images {
		iuid, _ := parseUUID(entry.ImageID)
		imageIDs = append(imageIDs, iuid)
		if entry.VariantID == nil || checked[*entry.VariantID] {
			continue
		}
		checked[*entry.VariantID] = true
		vuid, _ := parseUUID(*entry.VariantID)
		variant, err := q.CatalogProductVariantByID(ctx, vuid)
		if err != nil {
			_ = tx.Rollback(ctx)
			if errors.Is(err, pgx.ErrNoRows) {
				return d.persistCatalogRetry(ctx, processor, attempt.euid, now, ErrCatalogDependencyWait, "variant not yet projected")
			}
			return d.persistCatalogRetry(ctx, processor, attempt.euid, now, ErrProjection, "variant lookup failed", err)
		}
		if uuidString(variant.ProductID) != valid.ProductID {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, processor, attempt.euid, now, ErrCatalogRevisionConflict, "variant belongs to another product")
		}
	}
	owners, err := q.CatalogProductMediaOwners(ctx, imageIDs)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, processor, attempt.euid, now, ErrProjection, "image ownership lookup failed", err)
	}
	for _, owner := range owners {
		if uuidString(owner.ProductID) != valid.ProductID {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, processor, attempt.euid, now, ErrCatalogRevisionConflict, "image identity owned by another product")
		}
	}
	if err := q.DeleteCatalogProductMedia(ctx, puid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, processor, attempt.euid, now, ErrProjection, "media replace failed", err)
	}
	for i, entry := range valid.Images {
		params := sqlcgen.InsertCatalogProductMediaParams{
			ImageID: imageIDs[i], ProductID: puid, Position: int32(entry.Position), IsPrimary: entry.IsPrimary,
			Sha256: entry.SHA256, ContentType: entry.ContentType, ByteSize: int32(entry.ByteSize),
			Width: int32(entry.Width), Height: int32(entry.Height),
			SourceRevision: valid.MediaRevision, SourceEventID: attempt.euid,
			SourceDeviceID: attempt.duid, SourceReceivedAt: pgTime(event.ReceivedAt),
		}
		if entry.VariantID != nil {
			params.VariantID, _ = parseUUID(*entry.VariantID)
		}
		if err := q.InsertCatalogProductMedia(ctx, params); err != nil {
			_ = tx.Rollback(ctx)
			if isUniqueViolation(err) {
				return d.markCatalogBlocked(ctx, processor, attempt.euid, now, ErrCatalogRevisionConflict, "media identity collision")
			}
			return d.persistCatalogRetry(ctx, processor, attempt.euid, now, ErrProjection, "media insert failed", err)
		}
	}
	if _, err := q.SetCatalogProductMediaRevision(ctx, sqlcgen.SetCatalogProductMediaRevisionParams{
		ProductID: puid, MediaRevision: valid.MediaRevision,
	}); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, processor, attempt.euid, now, ErrProjection, "media revision write failed", err)
	}
	return finishCatalogAttempt(ctx, q, tx, processor, attempt.euid, count, now, catalog.ProcProcessed, "", "")
}

// storedProductMedia rebuilds comparison form from projected rows.
func storedProductMedia(rows []sqlcgen.CatalogProductMediaByProductRow) []catalog.ProductMediaEntry {
	entries := make([]catalog.ProductMediaEntry, 0, len(rows))
	for _, row := range rows {
		entry := catalog.ProductMediaEntry{
			ImageID: uuidString(row.ImageID), Position: int(row.Position), IsPrimary: row.IsPrimary,
			SHA256: row.Sha256, ContentType: row.ContentType, ByteSize: int(row.ByteSize),
			Width: int(row.Width), Height: int(row.Height),
		}
		if row.VariantID.Valid {
			value := uuidString(row.VariantID)
			entry.VariantID = &value
		}
		entries = append(entries, entry)
	}
	return catalog.NormalizeProductMedia(entries)
}
