package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// PendingSaleV3Events returns due sale.finalized.v3 candidate event IDs
// under the v3 processor. v1/v2 rows are never returned here.
func (d Devices) PendingSaleV3Events(ctx context.Context, limit int) ([]string, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).PendingSaleV3Events(ctx, sqlcgen.PendingSaleV3EventsParams{
		Processor: sale.ProcessorSaleProjectionV3, Limit: int32(limit),
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "pending v3 sales", redact(err))
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, uuidString(row))
	}
	return out, nil
}

// ProjectSaleV3 projects one sale.finalized.v3 with the same durable
// ownership arbitration as v1/v2 (shared sale_event_ownership: the first
// event wins regardless of version), then header, children (INCLUDING the
// frozen per-line variant snapshots from 00036 — sourced ONLY from the
// event, never joined from catalog), historical tag snapshots, and the
// tag-capture mark. Same-event replay uses ON CONFLICT DO NOTHING
// throughout and stays idempotent. v1/v2 events keep flowing through
// their own projectors untouched.
func (d Devices) ProjectSaleV3(ctx context.Context, event sale.EventRecord, now time.Time) (sale.ProjectResult, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	euid, err := parseUUID(event.EventID)
	if err != nil {
		return sale.ProjectResult{}, apperr.New(apperr.InvalidInput, "event_id must be a UUID")
	}
	raw, derr := sale.DecodeV3(event.Payload)
	if derr != nil {
		return d.markBlocked(ctx, sale.ProcessorSaleProjectionV3, euid, now, ErrValidation, safeErr(derr))
	}
	valid, verr := sale.ValidateV3(raw)
	if verr != nil {
		return d.markBlocked(ctx, sale.ProcessorSaleProjectionV3, euid, now, ErrValidation, safeErr(verr))
	}
	saleUID, err := parseUUID(valid.SaleID)
	if err != nil {
		return d.markBlocked(ctx, sale.ProcessorSaleProjectionV3, euid, now, ErrValidation, "sale_id must be a UUID")
	}
	duid, err := parseUUID(event.DeviceID)
	if err != nil {
		return d.markBlocked(ctx, sale.ProcessorSaleProjectionV3, euid, now, ErrValidation, "device_id must be a UUID")
	}
	winner, err := d.arbitrate(ctx, saleUID, euid, duid, now)
	if err != nil {
		var ierr *integrityError
		if errors.As(err, &ierr) {
			return d.markBlocked(ctx, sale.ProcessorSaleProjectionV3, euid, now, ErrOwnershipIntegrity, ierr.msg)
		}
		return d.persistRetry(ctx, sale.ProcessorSaleProjectionV3, euid, now, ErrProjection, "ownership arbitration failed")
	}
	if winner != event.EventID {
		return d.markBlocked(ctx, sale.ProcessorSaleProjectionV3, euid, now, ErrSaleIDConflict,
			"sale_id already owned by another event")
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return sale.ProjectResult{}, transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	if err := q.ClaimProcessing(ctx, sqlcgen.ClaimProcessingParams{
		EventID: euid, Processor: sale.ProcessorSaleProjectionV3,
	}); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistRetry(ctx, sale.ProcessorSaleProjectionV3, euid, now, ErrProjection, "claim failed")
	}
	claim, err := q.LockProcessing(ctx, sqlcgen.LockProcessingParams{
		EventID: euid, Processor: sale.ProcessorSaleProjectionV3,
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistRetry(ctx, sale.ProcessorSaleProjectionV3, euid, now, ErrProjection, "lock failed")
	}
	mark := func(status string, attempt int32, next *time.Time, processed *time.Time, code, msg string) (sale.ProjectResult, error) {
		if err := q.MarkProcessing(ctx, sqlcgen.MarkProcessingParams{
			EventID: euid, Processor: sale.ProcessorSaleProjectionV3,
			Status: status, AttemptCount: attempt,
			LastAttemptAt: pgTime(now), NextAttemptAt: pgTimePtr(next),
			ProcessedAt:   pgTimePtr(processed),
			LastErrorCode: pgText(code), LastErrorMessage: pgText(boundMsg(msg)),
		}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistRetry(ctx, sale.ProcessorSaleProjectionV3, euid, now, ErrProjection, "mark failed")
		}
		if err := tx.Commit(ctx); err != nil {
			return d.persistRetry(ctx, sale.ProcessorSaleProjectionV3, euid, now, ErrProjection, "commit failed")
		}
		res := sale.ProjectResult{ErrorCode: code}
		switch status {
		case sale.ProcProcessed:
			res.Outcome = sale.OutcomeProcessed
		case sale.ProcBlocked:
			res.Outcome = sale.OutcomeBlocked
		default:
			res.Outcome = sale.OutcomeRetryable
		}
		return res, nil
	}
	switch claim.Status {
	case sale.ProcProcessed:
		return mark(sale.ProcProcessed, claim.AttemptCount, nil, timePtr(claimProcessedAt(claim, now)), "", "")
	case sale.ProcBlocked:
		return sale.ProjectResult{Outcome: sale.OutcomeBlocked}, commitTx(ctx, tx)
	}
	if claim.Status == sale.ProcRetry && claim.NextAttemptAt.Valid && claim.NextAttemptAt.Time.After(now) {
		return sale.ProjectResult{Outcome: sale.OutcomeNotDue}, commitTx(ctx, tx)
	}
	owner, err := q.SaleOwnershipBySaleID(ctx, saleUID)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistRetry(ctx, sale.ProcessorSaleProjectionV3, euid, now, ErrProjection, "ownership verify failed")
	}
	if uuidString(owner.WinningEventID) != event.EventID {
		_ = tx.Rollback(ctx)
		return d.markBlocked(ctx, sale.ProcessorSaleProjectionV3, euid, now, ErrSaleIDConflict,
			"sale_id already owned by another event")
	}
	headerParams := projectionHeaderParams(saleUID, euid, duid, event, valid.Validated)
	if _, err := q.InsertSaleProjection(ctx, headerParams); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			existing, ferr := q.SaleProjectionBySaleID(ctx, saleUID)
			if ferr != nil {
				_ = tx.Rollback(ctx)
				return d.persistRetry(ctx, sale.ProcessorSaleProjectionV3, euid, now, ErrProjection, "header race with rollback")
			}
			if uuidString(existing.SourceEventID) != event.EventID {
				_ = tx.Rollback(ctx)
				if scope := saleScopeCheck(existing.StoreID, uuidString(existing.SourceEventID), event); scope != nil {
					return d.markBlocked(ctx, sale.ProcessorSaleProjectionV3, euid, now, scope.ErrorCode,
						"sale owned by another store")
				}
				return d.markBlocked(ctx, sale.ProcessorSaleProjectionV3, euid, now, ErrProjection,
					"projection source differs from durable ownership")
			}
			if err := insertProjectionChildrenV3(ctx, q, saleUID, valid); err != nil {
				_ = tx.Rollback(ctx)
				return d.persistRetry(ctx, sale.ProcessorSaleProjectionV3, euid, now, ErrProjection, "replay insert failed")
			}
			if err := insertProjectionTagsV3(ctx, q, saleUID, valid); err != nil {
				_ = tx.Rollback(ctx)
				return d.persistRetry(ctx, sale.ProcessorSaleProjectionV3, euid, now, ErrProjection, "replay tag insert failed")
			}
			if err := q.MarkSaleTagCaptured(ctx, saleUID); err != nil {
				_ = tx.Rollback(ctx)
				return d.persistRetry(ctx, sale.ProcessorSaleProjectionV3, euid, now, ErrProjection, "replay capture mark failed")
			}
			return mark(sale.ProcProcessed, claim.AttemptCount+1, nil, timePtr(now), "", "")
		}
		_ = tx.Rollback(ctx)
		return d.persistRetry(ctx, sale.ProcessorSaleProjectionV3, euid, now, ErrProjection, "projection insert failed")
	}
	if err := insertProjectionChildrenV3(ctx, q, saleUID, valid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistRetry(ctx, sale.ProcessorSaleProjectionV3, euid, now, ErrProjection, "projection insert failed")
	}
	if err := insertProjectionTagsV3(ctx, q, saleUID, valid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistRetry(ctx, sale.ProcessorSaleProjectionV3, euid, now, ErrProjection, "tag insert failed")
	}
	if err := q.MarkSaleTagCaptured(ctx, saleUID); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistRetry(ctx, sale.ProcessorSaleProjectionV3, euid, now, ErrProjection, "capture mark failed")
	}
	return mark(sale.ProcProcessed, claim.AttemptCount+1, nil, timePtr(now), "", "")
}

// insertProjectionChildrenV3 writes v3 lines with their frozen variant
// snapshots (00036), plus payments and classifications. The variant
// columns come ONLY from the event snapshot — this path never reads
// catalog tables. ON CONFLICT DO NOTHING keeps replay idempotent.
func insertProjectionChildrenV3(ctx context.Context, q *sqlcgen.Queries, saleUID pgtype.UUID, v sale.ValidatedV3) error {
	for i := range v.Lines {
		line := &v.Lines[i]
		itemUID, _ := parseUUID(line.SaleItemID)
		var variantAttrs []byte
		if line.VariantAttributes != nil {
			encoded, err := json.Marshal(line.VariantAttributes)
			if err != nil {
				return redact(err)
			}
			variantAttrs = encoded
		}
		params := sqlcgen.InsertSaleLineWithVariantSnapshotParams{
			SaleID: saleUID, SaleItemID: itemUID, Position: int32(i),
			ProductID: pgUUIDPtr(line.ProductID), VariantID: pgUUIDPtr(line.VariantID),
			Sku: line.SKU, ProductName: line.ProductName,
			WidthCm: pgIntPtr(line.WidthCM), HeightCm: pgIntPtr(line.HeightCM),
			Quantity:       int32(line.Quantity),
			UnitPriceMinor: line.UnitPrice.AmountMinor, UnitCurrency: line.UnitPrice.Currency,
			CostMinor: pgInt64Ptr(line.Cost), CostCurrency: pgText(pgStrPtr(line.Cost)),
			LineTotalMinor: line.LineTotal.AmountMinor, LineCurrency: line.LineTotal.Currency,
			VariantSku:           pgText(line.VariantSKU),
			VariantAttributes:    variantAttrs,
			VariantPriceEgpCents: pgInt64Value(line.VariantPriceEGPCents),
			VariantPriceUsdCents: pgInt64Value(line.VariantPriceUSDCents),
			ProductTypeID:        pgTextPtr(line.ProductTypeID),
			ProductTypeCode:      pgTextPtr(line.ProductTypeCode),
			ProductTypeNameAr:    pgTextPtr(line.ProductTypeNameAR),
			ProductTypeNameEn:    pgTextPtr(line.ProductTypeNameEN),
		}
		if line.VariantSKU == "" {
			params.VariantSku = pgtype.Text{}
		}
		if line.ProductTypeID == nil {
			params.ProductTypeID = pgtype.Text{}
			params.ProductTypeCode = pgtype.Text{}
			params.ProductTypeNameAr = pgtype.Text{}
			params.ProductTypeNameEn = pgtype.Text{}
		}
		if err := q.InsertSaleLineWithVariantSnapshot(ctx, params); err != nil {
			return redact(err)
		}
		for j := range line.Classifications.Roots {
			if err := insertClassification(ctx, q, saleUID, itemUID, "root", j, &line.Classifications.Roots[j]); err != nil {
				return err
			}
		}
		for j := range line.Classifications.Subcategories {
			if err := insertClassification(ctx, q, saleUID, itemUID, "subcategory", j, &line.Classifications.Subcategories[j]); err != nil {
				return err
			}
		}
	}
	for i := range v.Payments {
		pay := &v.Payments[i]
		if err := q.InsertSalePayment(ctx, sqlcgen.InsertSalePaymentParams{
			SaleID: saleUID, Position: int32(i), Method: pay.Method,
			AmountMinor: pay.Amount.AmountMinor, AmountCurrency: pay.Amount.Currency,
			ChangeMinor: pay.ChangeGiven.AmountMinor, ChangeCurrency: pay.ChangeGiven.Currency,
			TransactionRef: pgText(strPtr(pay.TransactionRef)),
		}); err != nil {
			return redact(err)
		}
	}
	return nil
}

// insertProjectionTagsV3 writes historical tag snapshots for v3 lines
// (v3 keeps the v2 tag contract). Historical data only: never consults
// catalog_tags.
func insertProjectionTagsV3(ctx context.Context, q *sqlcgen.Queries, saleUID pgtype.UUID, v sale.ValidatedV3) error {
	for i := range v.Lines {
		line := &v.Lines[i]
		itemUID, _ := parseUUID(line.SaleItemID)
		for j := range line.Tags {
			tag := &line.Tags[j]
			tagUID, _ := parseUUID(tag.TagID)
			if err := q.InsertSaleItemTag(ctx, sqlcgen.InsertSaleItemTagParams{
				SaleID: saleUID, SaleItemID: itemUID, TagID: tagUID,
				Slug: tag.Slug, NameAr: tag.NameAR, NameEn: tag.NameEN,
			}); err != nil {
				return redact(err)
			}
		}
	}
	return nil
}

// pgInt64Value maps a nullable int64 minor-unit value.
func pgInt64Value(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}
