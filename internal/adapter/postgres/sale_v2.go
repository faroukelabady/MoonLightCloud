package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// PendingSaleV2Events returns due sale.finalized.v2 candidate event IDs
// under the v2 processor. v1 rows are never returned here.
func (d Devices) PendingSaleV2Events(ctx context.Context, limit int) ([]string, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).PendingSaleV2Events(ctx, sqlcgen.PendingSaleV2EventsParams{
		Processor: sale.ProcessorSaleProjectionV2, Limit: int32(limit),
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "pending v2 sales", redact(err))
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, uuidString(row))
	}
	return out, nil
}

// ProjectSaleV2 projects one sale.finalized.v2 with the same durable
// ownership arbitration as v1 (shared sale_event_ownership: the first
// event wins regardless of version), then header, children, historical
// tag snapshots, and the tag-capture mark. Same-event replay uses ON
// CONFLICT DO NOTHING throughout and stays idempotent. v1 events keep
// flowing through ProjectSale untouched with tag_capture NULL.
func (d Devices) ProjectSaleV2(ctx context.Context, event sale.EventRecord, now time.Time) (sale.ProjectResult, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	euid, err := parseUUID(event.EventID)
	if err != nil {
		return sale.ProjectResult{}, apperr.New(apperr.InvalidInput, "event_id must be a UUID")
	}
	raw, derr := sale.DecodeV2(event.Payload)
	if derr != nil {
		return d.markBlocked(ctx, sale.ProcessorSaleProjectionV2, euid, now, ErrValidation, safeErr(derr))
	}
	valid, verr := sale.ValidateV2(raw)
	if verr != nil {
		return d.markBlocked(ctx, sale.ProcessorSaleProjectionV2, euid, now, ErrValidation, safeErr(verr))
	}
	saleUID, err := parseUUID(valid.SaleID)
	if err != nil {
		return d.markBlocked(ctx, sale.ProcessorSaleProjectionV2, euid, now, ErrValidation, "sale_id must be a UUID")
	}
	duid, err := parseUUID(event.DeviceID)
	if err != nil {
		return d.markBlocked(ctx, sale.ProcessorSaleProjectionV2, euid, now, ErrValidation, "device_id must be a UUID")
	}
	winner, err := d.arbitrate(ctx, saleUID, euid, duid, now)
	if err != nil {
		var ierr *integrityError
		if errors.As(err, &ierr) {
			return d.markBlocked(ctx, sale.ProcessorSaleProjectionV2, euid, now, ErrOwnershipIntegrity, ierr.msg)
		}
		return d.persistRetry(ctx, sale.ProcessorSaleProjectionV2, euid, now, ErrProjection, "ownership arbitration failed")
	}
	if winner != event.EventID {
		return d.markBlocked(ctx, sale.ProcessorSaleProjectionV2, euid, now, ErrSaleIDConflict,
			"sale_id already owned by another event")
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return sale.ProjectResult{}, transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	if err := q.ClaimProcessing(ctx, sqlcgen.ClaimProcessingParams{
		EventID: euid, Processor: sale.ProcessorSaleProjectionV2,
	}); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistRetry(ctx, sale.ProcessorSaleProjectionV2, euid, now, ErrProjection, "claim failed")
	}
	claim, err := q.LockProcessing(ctx, sqlcgen.LockProcessingParams{
		EventID: euid, Processor: sale.ProcessorSaleProjectionV2,
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistRetry(ctx, sale.ProcessorSaleProjectionV2, euid, now, ErrProjection, "lock failed")
	}
	mark := func(status string, attempt int32, next *time.Time, processed *time.Time, code, msg string) (sale.ProjectResult, error) {
		if err := q.MarkProcessing(ctx, sqlcgen.MarkProcessingParams{
			EventID: euid, Processor: sale.ProcessorSaleProjectionV2,
			Status: status, AttemptCount: attempt,
			LastAttemptAt: pgTime(now), NextAttemptAt: pgTimePtr(next),
			ProcessedAt:   pgTimePtr(processed),
			LastErrorCode: pgText(code), LastErrorMessage: pgText(boundMsg(msg)),
		}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistRetry(ctx, sale.ProcessorSaleProjectionV2, euid, now, ErrProjection, "mark failed")
		}
		if err := tx.Commit(ctx); err != nil {
			return d.persistRetry(ctx, sale.ProcessorSaleProjectionV2, euid, now, ErrProjection, "commit failed")
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
		return d.persistRetry(ctx, sale.ProcessorSaleProjectionV2, euid, now, ErrProjection, "ownership verify failed")
	}
	if uuidString(owner.WinningEventID) != event.EventID {
		_ = tx.Rollback(ctx)
		return d.markBlocked(ctx, sale.ProcessorSaleProjectionV2, euid, now, ErrSaleIDConflict,
			"sale_id already owned by another event")
	}
	headerParams := projectionHeaderParams(saleUID, euid, duid, event, valid.Validated)
	if _, err := q.InsertSaleProjection(ctx, headerParams); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			existing, ferr := q.SaleProjectionBySaleID(ctx, saleUID)
			if ferr != nil {
				_ = tx.Rollback(ctx)
				return d.persistRetry(ctx, sale.ProcessorSaleProjectionV2, euid, now, ErrProjection, "header race with rollback")
			}
			if uuidString(existing.SourceEventID) != event.EventID {
				_ = tx.Rollback(ctx)
				return d.markBlocked(ctx, sale.ProcessorSaleProjectionV2, euid, now, ErrProjection,
					"projection source differs from durable ownership")
			}
			if err := insertProjectionChildren(ctx, q, saleUID, valid.Validated); err != nil {
				_ = tx.Rollback(ctx)
				return d.persistRetry(ctx, sale.ProcessorSaleProjectionV2, euid, now, ErrProjection, "replay insert failed")
			}
			if err := insertProjectionTags(ctx, q, saleUID, valid); err != nil {
				_ = tx.Rollback(ctx)
				return d.persistRetry(ctx, sale.ProcessorSaleProjectionV2, euid, now, ErrProjection, "replay tag insert failed")
			}
			if err := q.MarkSaleTagCaptured(ctx, saleUID); err != nil {
				_ = tx.Rollback(ctx)
				return d.persistRetry(ctx, sale.ProcessorSaleProjectionV2, euid, now, ErrProjection, "replay capture mark failed")
			}
			return mark(sale.ProcProcessed, claim.AttemptCount+1, nil, timePtr(now), "", "")
		}
		_ = tx.Rollback(ctx)
		return d.persistRetry(ctx, sale.ProcessorSaleProjectionV2, euid, now, ErrProjection, "projection insert failed")
	}
	if err := insertProjectionChildren(ctx, q, saleUID, valid.Validated); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistRetry(ctx, sale.ProcessorSaleProjectionV2, euid, now, ErrProjection, "projection insert failed")
	}
	if err := insertProjectionTags(ctx, q, saleUID, valid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistRetry(ctx, sale.ProcessorSaleProjectionV2, euid, now, ErrProjection, "tag insert failed")
	}
	if err := q.MarkSaleTagCaptured(ctx, saleUID); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistRetry(ctx, sale.ProcessorSaleProjectionV2, euid, now, ErrProjection, "capture mark failed")
	}
	return mark(sale.ProcProcessed, claim.AttemptCount+1, nil, timePtr(now), "", "")
}

// insertProjectionTags writes historical tag snapshots with deterministic
// identities and ON CONFLICT DO NOTHING. Historical data only: never
// consults catalog_tags.
func insertProjectionTags(ctx context.Context, q *sqlcgen.Queries, saleUID pgtype.UUID, v sale.ValidatedV2) error {
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
