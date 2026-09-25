package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/returnrefund"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Return projector Store implementation on Devices (shared pool + timeouts).

// Deterministic return-projection failure codes (persisted, operational).
const (
	ErrReturnIDConflict   = "RETURN_REFUND_ID_CONFLICT"
	ErrSaleDependencyWait = "SALE_DEPENDENCY_WAIT"
	ErrReturnLineUnknown  = "RETURN_LINE_UNKNOWN"
	ErrReturnCurrencyMix  = "RETURN_CURRENCY_MISMATCH"
	ErrReturnFxMismatch   = "RETURN_FX_MISMATCH"
	ErrCumulativeOverRet  = "CUMULATIVE_OVER_RETURN"
	ErrCumulativeRefundEx = "CUMULATIVE_REFUND_EXCEEDED"
)

// PendingReturnEvents returns due candidate return event IDs (durable
// discovery, including retroactive Phase 4A backfill: accepted events with
// no processing row are discoverable).
func (d Devices) PendingReturnEvents(ctx context.Context, processor string, limit int) ([]string, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).PendingReturnEvents(ctx, sqlcgen.PendingReturnEventsParams{
		Processor: processor, Limit: int32(limit),
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "scan pending returns", redact(err))
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, uuidString(r))
	}
	return out, nil
}

// LoadReturnEvent loads the immutable inbox row for return projection.
func (d Devices) LoadReturnEvent(ctx context.Context, eventID string) (returnrefund.EventRecord, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(eventID)
	if err != nil {
		return returnrefund.EventRecord{}, false, apperr.New(apperr.InvalidInput, "event_id must be a UUID")
	}
	row, err := sqlcgen.New(d.pool).SaleEventByID(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return returnrefund.EventRecord{}, false, nil
		}
		return returnrefund.EventRecord{}, false, apperr.Wrap(apperr.Internal, "load event", redact(err))
	}
	rec := returnrefund.EventRecord{
		EventID: uuidString(row.EventID), DeviceID: uuidString(row.DeviceID),
		EventType:  row.EventType,
		OccurredAt: row.OccurredAt.Time, ReceivedAt: row.ReceivedAt.Time,
		Payload: json.RawMessage(row.Payload),
	}
	if row.CredentialID.Valid {
		c := uuidString(row.CredentialID)
		rec.CredentialID = &c
	}
	return rec, true, nil
}

// ProjectReturn projects one return with durable business arbitration
// (ADR-0027), mirroring the frozen sale flow:
//
//  1. pure decode + validate (no tx). Invalid → deterministic blocked;
//     invalid events never claim ownership.
//  2. arbitration tx: INSERT return_refund_ownership ON CONFLICT DO
//     NOTHING RETURNING; loser reads the durable winner in the same tx.
//     The winner is permanent at commit — before any projection row
//     exists. First writer wins; concurrent rivals serialize and read
//     the same winner.
//  3. projection tx: claim/lock processing, re-verify the durable
//     winner, lock the parent sale projection row (serializes all
//     returns for one sale so cumulative guards cannot write-skew),
//     resolve the sale dependency (missing → retryable wait, never
//     terminal), run integrity cross-checks (terminal when committed
//     history makes success impossible), then — and only then — write
//     header + lines + payments, mark processed/blocked.
//
// Either a complete return projection commits or nothing does. Only a
// winning event may populate return projection tables. Rebuilds clear
// projections but never ownership, so the historical winner always
// reproduces.
func (d Devices) ProjectReturn(ctx context.Context, event returnrefund.EventRecord, now time.Time) (returnrefund.ProjectResult, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	euid, err := parseUUID(event.EventID)
	if err != nil {
		return returnrefund.ProjectResult{}, apperr.New(apperr.InvalidInput, "event_id must be a UUID")
	}
	// Defensive decode: ingest validated, but persisted rows may predate or
	// bypass validation. Deterministic failure → blocked, never panic, and
	// never ownership (invalid events own nothing).
	raw, derr := returnrefund.Decode(event.Payload)
	if derr != nil {
		return d.markReturnBlocked(ctx, euid, now, ErrValidation, safeErr(derr))
	}
	valid, verr := returnrefund.Validate(raw)
	if verr != nil {
		return d.markReturnBlocked(ctx, euid, now, ErrValidation, safeErr(verr))
	}
	rid, err := parseUUID(valid.ReturnRefundID)
	if err != nil {
		return d.markReturnBlocked(ctx, euid, now, ErrValidation, "return_refund_id must be a UUID")
	}
	saleUID, err := parseUUID(valid.SaleID)
	if err != nil {
		return d.markReturnBlocked(ctx, euid, now, ErrValidation, "sale_id must be a UUID")
	}
	duid, err := parseUUID(event.DeviceID)
	if err != nil {
		return d.markReturnBlocked(ctx, euid, now, ErrValidation, "device_id must be a UUID")
	}
	// Durable arbitration first: permanent winner before any projection row.
	winner, err := d.arbitrateReturn(ctx, rid, euid, duid, now)
	if err != nil {
		var ierr *integrityError
		if errors.As(err, &ierr) {
			// Impossible invariant (winner bound to another business ID):
			// blocked deterministically, never transient, never replay.
			return d.markReturnBlocked(ctx, euid, now, ErrOwnershipIntegrity, ierr.msg)
		}
		return d.persistReturnRetry(ctx, euid, now, ErrProjection, "return ownership arbitration failed")
	}
	if winner != event.EventID {
		// Another event permanently owns this return_refund_id: ZERO rows.
		return d.markReturnBlocked(ctx, euid, now, ErrReturnIDConflict,
			"return_refund_id already owned by another event")
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return returnrefund.ProjectResult{}, transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	if err := q.ClaimProcessing(ctx, sqlcgen.ClaimProcessingParams{
		EventID: euid, Processor: returnrefund.ProcessorReturnProjectionV1,
	}); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistReturnRetry(ctx, euid, now, ErrProjection, "claim failed")
	}
	claim, err := q.LockProcessing(ctx, sqlcgen.LockProcessingParams{
		EventID: euid, Processor: returnrefund.ProcessorReturnProjectionV1,
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistReturnRetry(ctx, euid, now, ErrProjection, "lock failed")
	}
	mark := func(status string, attempt int32, next *time.Time, processed *time.Time, code, msg string) (returnrefund.ProjectResult, error) {
		if err := q.MarkProcessing(ctx, sqlcgen.MarkProcessingParams{
			EventID: euid, Processor: returnrefund.ProcessorReturnProjectionV1,
			Status: status, AttemptCount: attempt,
			LastAttemptAt: pgTime(now), NextAttemptAt: pgTimePtr(next),
			ProcessedAt:   pgTimePtr(processed),
			LastErrorCode: pgText(code), LastErrorMessage: pgText(boundMsg(msg)),
		}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistReturnRetry(ctx, euid, now, ErrProjection, "mark failed")
		}
		if err := tx.Commit(ctx); err != nil {
			return d.persistReturnRetry(ctx, euid, now, ErrProjection, "commit failed")
		}
		res := returnrefund.ProjectResult{ErrorCode: code}
		switch status {
		case returnrefund.ProcProcessed:
			res.Outcome = returnrefund.OutcomeProcessed
		case returnrefund.ProcBlocked:
			res.Outcome = returnrefund.OutcomeBlocked
		default:
			res.Outcome = returnrefund.OutcomeRetryable
		}
		return res, nil
	}
	switch claim.Status {
	case returnrefund.ProcProcessed:
		return mark(returnrefund.ProcProcessed, claim.AttemptCount, nil, timePtr(claimProcessedAt(claim, now)), "", "")
	case returnrefund.ProcBlocked:
		return returnrefund.ProjectResult{Outcome: returnrefund.OutcomeBlocked}, commitTx(ctx, tx)
	}
	if claim.Status == returnrefund.ProcRetry && claim.NextAttemptAt.Valid && claim.NextAttemptAt.Time.After(now) {
		return returnrefund.ProjectResult{Outcome: returnrefund.OutcomeNotDue}, commitTx(ctx, tx)
	}
	// Re-verify the durable winner inside the projection tx at the write
	// boundary.
	owner, err := q.ReturnOwnershipByID(ctx, rid)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistReturnRetry(ctx, euid, now, ErrProjection, "ownership verify failed")
	}
	if uuidString(owner.WinningEventID) != event.EventID {
		_ = tx.Rollback(ctx)
		return d.markReturnBlocked(ctx, euid, now, ErrReturnIDConflict,
			"return_refund_id already owned by another event")
	}
	// Serialize with sibling returns on the parent sale row, then resolve
	// the sale dependency. A missing sale projection (or missing sale
	// line set) is a retryable wait — the sale may arrive out of order —
	// never a terminal verdict at this stage.
	saleProj, err := q.LockSaleProjectionForReturn(ctx, saleUID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			return d.persistReturnRetry(ctx, euid, now, ErrSaleDependencyWait,
				"original sale projection not yet available")
		}
		_ = tx.Rollback(ctx)
		return d.persistReturnRetry(ctx, euid, now, ErrProjection, "sale lookup failed")
	}
	// Integrity cross-checks against committed history. Each is terminal:
	// committed sale projections are immutable, so no retry can change
	// the verdict.
	if valid.Currency != saleProj.Currency {
		_ = tx.Rollback(ctx)
		return d.markReturnBlocked(ctx, euid, now, ErrReturnCurrencyMix,
			"return currency differs from sale currency")
	}
	if !fxAgrees(saleProj, valid) {
		_ = tx.Rollback(ctx)
		return d.markReturnBlocked(ctx, euid, now, ErrReturnFxMismatch,
			"return FX snapshot differs from sale FX snapshot")
	}
	var newRefund int64
	for i := range valid.Lines {
		line := &valid.Lines[i]
		lineUID, err := parseUUID(line.OriginalSaleLineID)
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.markReturnBlocked(ctx, euid, now, ErrValidation, "original_sale_line_id must be a UUID")
		}
		sold, err := q.SaleLineForReturn(ctx, sqlcgen.SaleLineForReturnParams{
			SaleID: saleUID, SaleItemID: lineUID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				_ = tx.Rollback(ctx)
				return d.markReturnBlocked(ctx, euid, now, ErrReturnLineUnknown,
					"original sale line is not part of the sale projection")
			}
			_ = tx.Rollback(ctx)
			return d.persistReturnRetry(ctx, euid, now, ErrProjection, "sale line lookup failed")
		}
		already, err := q.CumulativeReturnedQty(ctx, sqlcgen.CumulativeReturnedQtyParams{
			SaleID: saleUID, OriginalSaleLineID: lineUID,
		})
		if err != nil {
			_ = tx.Rollback(ctx)
			return d.persistReturnRetry(ctx, euid, now, ErrProjection, "cumulative lookup failed")
		}
		if already+int64(line.Quantity) > int64(sold.Quantity) {
			_ = tx.Rollback(ctx)
			return d.markReturnBlocked(ctx, euid, now, ErrCumulativeOverRet,
				"returned quantity would exceed the sold quantity")
		}
		var err2 error
		newRefund, err2 = addInt64(newRefund, line.Refund.AmountMinor)
		if err2 != nil {
			_ = tx.Rollback(ctx)
			return d.persistReturnRetry(ctx, euid, now, ErrProjection, "refund accounting overflow")
		}
	}
	committed, err := q.CumulativeRefundedForSale(ctx, saleUID)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistReturnRetry(ctx, euid, now, ErrProjection, "cumulative lookup failed")
	}
	if committed+newRefund > saleProj.TotalMinor {
		_ = tx.Rollback(ctx)
		return d.markReturnBlocked(ctx, euid, now, ErrCumulativeRefundEx,
			"cumulative refunds would exceed the sale total")
	}
	// Winner only: populate header, then children. Same-event replay uses
	// ON CONFLICT DO NOTHING throughout and stays idempotent.
	headerParams := returnHeaderParams(rid, euid, duid, event, valid)
	if _, err := q.InsertReturnProjection(ctx, headerParams); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			existing, ferr := q.ReturnProjectionByID(ctx, rid)
			if ferr != nil {
				_ = tx.Rollback(ctx)
				return d.persistReturnRetry(ctx, euid, now, ErrProjection, "header race with rollback")
			}
			if uuidString(existing.SourceEventID) != event.EventID {
				// Header from another event despite durable ownership:
				// projection/ownership integrity failure — never overwrite.
				_ = tx.Rollback(ctx)
				return d.markReturnBlocked(ctx, euid, now, ErrProjection,
					"projection source differs from durable ownership")
			}
			if err := insertReturnChildren(ctx, q, rid, saleUID, valid); err != nil {
				_ = tx.Rollback(ctx)
				return d.persistReturnRetry(ctx, euid, now, ErrProjection, "replay insert failed")
			}
			return mark(returnrefund.ProcProcessed, claim.AttemptCount+1, nil, timePtr(now), "", "")
		}
		_ = tx.Rollback(ctx)
		return d.persistReturnRetry(ctx, euid, now, ErrProjection, "projection insert failed")
	}
	if err := insertReturnChildren(ctx, q, rid, saleUID, valid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistReturnRetry(ctx, euid, now, ErrProjection, "projection insert failed")
	}
	return mark(returnrefund.ProcProcessed, claim.AttemptCount+1, nil, timePtr(now), "", "")
}

// arbitrateReturn durably establishes the permanent winner for
// return_refund_id and returns the winning event ID. The decision commits
// in its own transaction before any projection row exists, so it survives
// projection rollback, restart, and rebuild. First writer wins; concurrent
// rivals serialize and read the same winner. The bare ON CONFLICT absorbs
// either uniqueness race:
//
//   - INSERT returns a row → this event just won.
//   - no row + business ID owned by E → same event already owns:
//     idempotent success.
//   - no row + business ID owned by E2 ≠ E → rival owns: conflict
//     downstream.
//   - no row + no business row + winner E owns another ID → impossible
//     invariant: deterministic integrity error (never transient).
//   - no row + no business row + no winner row → arbitration undecided
//     (concurrent rollback): transient, re-arbitrate later.
func (d Devices) arbitrateReturn(ctx context.Context, rid, euid, duid pgtype.UUID, now time.Time) (string, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return "", transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	row, err := q.ClaimReturnOwnership(ctx, sqlcgen.ClaimReturnOwnershipParams{
		ReturnRefundID: rid, WinningEventID: euid, WinningDeviceID: duid,
		DecidedAt: pgTime(now),
	})
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return "", transient(redact(err))
		}
		return uuidString(row), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", transient(redact(err))
	}
	// No row back: determine explicit state instead of assuming failure.
	owner, err := q.ReturnOwnershipByID(ctx, rid)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return "", transient(redact(err))
		}
		// Same event already owns (INSERT absorbed the winning_event_id
		// race) → idempotent success; rival owner → conflict downstream.
		return uuidString(owner.WinningEventID), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", transient(redact(err))
	}
	// No business row: the conflict (if any) was on winning_event_id.
	// Check whether this winner already owns a different business ID.
	byWinner, err := q.ReturnOwnershipByWinner(ctx, euid)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return "", transient(redact(err))
		}
		if uuidString(byWinner.ReturnRefundID) == uuidString(rid) {
			// Business row appeared between lookups and names this event:
			// idempotent success.
			return uuidString(euid), nil
		}
		return "", &integrityError{msg: "winning event already owns another return_refund_id"}
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", transient(redact(err))
	}
	// Truly undecided (concurrent rollback race): transient, re-arbitrate.
	return "", transient(errors.New("return ownership arbitration undecided"))
}

// markReturnBlocked records a deterministic blocked outcome (validation,
// conflict, or integrity) in one short transaction. Zero projection writes
// precede it.
func (d Devices) markReturnBlocked(ctx context.Context, euid pgtype.UUID, now time.Time, code, msg string) (returnrefund.ProjectResult, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return returnrefund.ProjectResult{}, transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	if err := q.ClaimProcessing(ctx, sqlcgen.ClaimProcessingParams{
		EventID: euid, Processor: returnrefund.ProcessorReturnProjectionV1,
	}); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistReturnRetry(ctx, euid, now, ErrProjection, "claim failed")
	}
	claim, err := q.LockProcessing(ctx, sqlcgen.LockProcessingParams{
		EventID: euid, Processor: returnrefund.ProcessorReturnProjectionV1,
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistReturnRetry(ctx, euid, now, ErrProjection, "lock failed")
	}
	if claim.Status == returnrefund.ProcProcessed {
		if err := tx.Commit(ctx); err != nil {
			return d.persistReturnRetry(ctx, euid, now, ErrProjection, "commit failed")
		}
		return returnrefund.ProjectResult{Outcome: returnrefund.OutcomeProcessed}, nil
	}
	if err := q.MarkProcessing(ctx, sqlcgen.MarkProcessingParams{
		EventID: euid, Processor: returnrefund.ProcessorReturnProjectionV1,
		Status: returnrefund.ProcBlocked, AttemptCount: claim.AttemptCount + 1,
		LastAttemptAt: pgTime(now), NextAttemptAt: pgtype.Timestamptz{},
		ProcessedAt:   pgtype.Timestamptz{},
		LastErrorCode: pgText(code), LastErrorMessage: pgText(boundMsg(msg)),
	}); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistReturnRetry(ctx, euid, now, ErrProjection, "mark failed")
	}
	if err := tx.Commit(ctx); err != nil {
		return d.persistReturnRetry(ctx, euid, now, ErrProjection, "commit failed")
	}
	return returnrefund.ProjectResult{Outcome: returnrefund.OutcomeBlocked, ErrorCode: code}, nil
}

// persistReturnRetry writes retry state in a SEPARATE durable transaction
// after the projection transaction rolled back. The schedule
// (attempt_count, next_attempt_at, bounded diagnostic) commits even though
// the projection itself did not, so dependency waits never hot-loop and
// survive restarts. Deterministic outcomes never reach here (they commit
// blocked). The returned error stays non-nil so callers observe the failed
// attempt; the retry state is already committed when it does.
func (d Devices) persistReturnRetry(ctx context.Context, euid pgtype.UUID, now time.Time, code, msg string) (returnrefund.ProjectResult, error) {
	fail := func() (returnrefund.ProjectResult, error) {
		return returnrefund.ProjectResult{Outcome: returnrefund.OutcomeRetryable, ErrorCode: code},
			transient(errors.New("return projection transient failure"))
	}
	ctx2, cancel := d.ctx(context.Background())
	defer cancel()
	tx, err := d.pool.Begin(ctx2)
	if err != nil {
		return fail()
	}
	defer func() { _ = tx.Rollback(ctx2) }()
	q := sqlcgen.New(tx)
	if err := q.ClaimProcessing(ctx2, sqlcgen.ClaimProcessingParams{
		EventID: euid, Processor: returnrefund.ProcessorReturnProjectionV1,
	}); err != nil {
		return fail()
	}
	claim, err := q.LockProcessing(ctx2, sqlcgen.LockProcessingParams{
		EventID: euid, Processor: returnrefund.ProcessorReturnProjectionV1,
	})
	if err != nil {
		return fail()
	}
	if claim.Status == returnrefund.ProcProcessed || claim.Status == returnrefund.ProcBlocked {
		if err := tx.Commit(ctx2); err != nil {
			return fail()
		}
		if claim.Status == returnrefund.ProcProcessed {
			return returnrefund.ProjectResult{Outcome: returnrefund.OutcomeProcessed}, nil
		}
		return returnrefund.ProjectResult{Outcome: returnrefund.OutcomeBlocked, ErrorCode: code}, nil
	}
	next := now.Add(returnrefund.Backoff(int(claim.AttemptCount)))
	if err := q.MarkProcessing(ctx2, sqlcgen.MarkProcessingParams{
		EventID: euid, Processor: returnrefund.ProcessorReturnProjectionV1,
		Status: returnrefund.ProcRetry, AttemptCount: claim.AttemptCount + 1,
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

// fxAgrees checks the return FX snapshot against the authoritative sale
// projection snapshot: both absent, or all four fields exactly equal.
// Historical snapshots only — never a live rate on either side.
func fxAgrees(sale sqlcgen.LockSaleProjectionForReturnRow, v returnrefund.Validated) bool {
	saleHas := sale.FxBase.Valid || sale.FxQuote.Valid || sale.FxRate.Valid || sale.FxRateMicrorate.Valid
	if v.Fx == nil {
		return !saleHas
	}
	if !saleHas {
		return false
	}
	return sale.FxBase.String == v.Fx.Base &&
		sale.FxQuote.String == v.Fx.Quote &&
		sale.FxRate.String == v.Fx.Rate &&
		sale.FxRateMicrorate.Int64 == v.Fx.RateMicrorate
}

// returnHeaderParams builds the return header insert from the validated
// event. Shop/actor snapshots come from the event (historical); the sale
// linkage was resolved before the call.
func returnHeaderParams(rid, euid, duid pgtype.UUID, event returnrefund.EventRecord, v returnrefund.Validated) sqlcgen.InsertReturnProjectionParams {
	var fxBase, fxQuote, fxRate pgtype.Text
	var fxMicro pgtype.Int8
	if v.Fx != nil {
		fxBase = pgText(v.Fx.Base)
		fxQuote = pgText(v.Fx.Quote)
		fxRate = pgText(v.Fx.Rate)
		fxMicro = pgtype.Int8{Int64: v.Fx.RateMicrorate, Valid: true}
	}
	var saleEvent pgtype.UUID
	if v.SaleEventID != nil {
		if parsed, err := parseUUID(*v.SaleEventID); err == nil {
			saleEvent = parsed
		}
	}
	var actorID pgtype.UUID
	if v.Actor.UserID != nil {
		if parsed, err := parseUUID(*v.Actor.UserID); err == nil {
			actorID = parsed
		}
	}
	var note pgtype.Text
	if v.Note != nil {
		note = pgText(*v.Note)
	}
	return sqlcgen.InsertReturnProjectionParams{
		ReturnRefundID: rid, SourceEventID: euid, SourceDeviceID: duid,
		ReturnNumber: v.ReturnNumber, Kind: v.Kind,
		Reason: v.Reason, Note: note,
		SaleID: mustParseUUID(v.SaleID), SaleNumber: v.SaleNumber, SaleEventID: saleEvent,
		Channel:            v.Channel,
		OccurredAt:         pgTime(v.Occurred),
		Currency:           v.Currency,
		GrossRefundedMinor: v.Totals.Gross.AmountMinor, DiscountRefundedMinor: v.Totals.Discount.AmountMinor,
		TaxRefundedMinor: v.Totals.Tax.AmountMinor, RefundTotalMinor: v.Totals.RefundTotal.AmountMinor,
		FxBase: fxBase, FxQuote: fxQuote, FxRate: fxRate, FxRateMicrorate: fxMicro,
		ShopNameAr: v.Shop.NameAR, ShopNameEn: v.Shop.NameEN,
		ShopAddressAr: v.Shop.AddressAR, ShopAddressEn: v.Shop.AddressEN,
		ShopPhone:           v.Shop.Phone,
		ShopReceiptFooterAr: v.Shop.ReceiptFooterAR, ShopReceiptFooterEn: v.Shop.ReceiptFooterEN,
		ActorUserID: actorID, ActorUserName: pgText(strOrEmpty(v.Actor.UserName)),
		ReceivedAt: pgTime(event.ReceivedAt),
	}
}

// insertReturnChildren writes lines + refund payments with deterministic
// identities and ON CONFLICT DO NOTHING, so replaying the owning event
// yields identical rows. It must only run after ownership of
// return_refund_id is proven inside the same transaction, against the
// locked parent sale. Historical snapshots only: no lookups against
// mutable state anywhere on this path. Restock flags flow through
// untouched — financial reversal never depends on restocking.
func insertReturnChildren(ctx context.Context, q *sqlcgen.Queries, rid, saleUID pgtype.UUID, v returnrefund.Validated) error {
	for i := range v.Lines {
		line := &v.Lines[i]
		itemUID, _ := parseUUID(line.OriginalSaleLineID)
		var cost pgtype.Int8
		var costCur pgtype.Text
		if line.Cost != nil {
			cost = pgtype.Int8{Int64: line.Cost.AmountMinor, Valid: true}
			costCur = pgText(line.Cost.Currency)
		}
		if err := q.InsertReturnLine(ctx, sqlcgen.InsertReturnLineParams{
			ReturnRefundID: rid, SaleID: saleUID, OriginalSaleLineID: itemUID, Position: int32(i),
			ProductID: pgUUIDPtr(line.ProductID),
			Quantity:  int32(line.Quantity), Restocked: line.Restocked,
			GrossMinor: line.Gross.AmountMinor, GrossCurrency: line.Gross.Currency,
			DiscountMinor: line.Discount.AmountMinor, DiscountCurrency: line.Discount.Currency,
			TaxMinor: line.Tax.AmountMinor, TaxCurrency: line.Tax.Currency,
			RefundMinor: line.Refund.AmountMinor, RefundCurrency: line.Refund.Currency,
			CostMinor: cost, CostCurrency: costCur,
		}); err != nil {
			return redact(err)
		}
	}
	for i := range v.Refunds {
		pay := &v.Refunds[i]
		if err := q.InsertReturnPayment(ctx, sqlcgen.InsertReturnPaymentParams{
			ReturnRefundID: rid, Position: int32(i), Method: pay.Method,
			AmountMinor: pay.Amount.AmountMinor, AmountCurrency: pay.Amount.Currency,
			TransactionRef: pgText(strOrEmpty(pay.TransactionRef)),
		}); err != nil {
			return redact(err)
		}
	}
	return nil
}

func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func mustParseUUID(s string) pgtype.UUID {
	u, err := parseUUID(s)
	if err != nil {
		return pgtype.UUID{}
	}
	return u
}

func addInt64(a, b int64) (int64, error) {
	const maxInt64 = int64(^uint64(0) >> 1)
	const minInt64 = -maxInt64 - 1
	if b > 0 && a > maxInt64-b {
		return 0, errors.New("overflow")
	}
	if b < 0 && a < minInt64-b {
		return 0, errors.New("overflow")
	}
	return a + b, nil
}
