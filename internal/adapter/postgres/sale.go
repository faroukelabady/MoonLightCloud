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

// Sale projector Store implementation on Devices (shared pool + timeouts).

// PendingSaleEvents returns due candidate event IDs (durable discovery).
func (d Devices) PendingSaleEvents(ctx context.Context, processor string, limit int) ([]string, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).PendingSaleEvents(ctx, sqlcgen.PendingSaleEventsParams{
		Processor: processor, Limit: int32(limit),
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "scan pending sales", redact(err))
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, uuidString(r))
	}
	return out, nil
}

// LoadSaleEvent loads the immutable inbox row for projection.
func (d Devices) LoadSaleEvent(ctx context.Context, eventID string) (sale.EventRecord, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(eventID)
	if err != nil {
		return sale.EventRecord{}, false, apperr.New(apperr.InvalidInput, "event_id must be a UUID")
	}
	row, err := sqlcgen.New(d.pool).SaleEventByID(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sale.EventRecord{}, false, nil
		}
		return sale.EventRecord{}, false, apperr.Wrap(apperr.Internal, "load event", redact(err))
	}
	rec := sale.EventRecord{
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

// Deterministic projection-failure codes (persisted, operational).
const (
	ErrSaleIDConflict = "SALE_ID_CONFLICT"
	ErrValidation     = "VALIDATION_FAILED"
	ErrEventMissing   = "EVENT_MISSING"
	ErrProjection     = "PROJECTION_FAILED"
)

// ProjectSale projects one sale with durable ownership arbitration
// (P2D-HIGH-02, CRIT-01):
//
//  1. pure decode + validate (no tx). Invalid → deterministic blocked.
//     Invalid events never claim ownership.
//  2. arbitration tx: INSERT sale_event_ownership ON CONFLICT DO NOTHING
//     RETURNING; loser reads the durable winner in the same tx. The winner
//     is permanent the moment this tx commits — before any projection row
//     exists. A first owner's later projection failure never transfers
//     ownership: late events become SALE_ID_CONFLICT and the owner retries
//     through durable retry semantics.
//  3. projection tx: claim/lock processing, re-verify the durable winner,
//     then (and only then) write header + children, mark processed/blocked.
//
// Either a complete Sale projection commits or nothing does. Only a winning
// event may populate Sale projection tables. Rebuilds clear projections but
// never ownership, so the historical winner always reproduces.
func (d Devices) ProjectSale(ctx context.Context, event sale.EventRecord, now time.Time) (sale.ProjectResult, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	euid, err := parseUUID(event.EventID)
	if err != nil {
		return sale.ProjectResult{}, apperr.New(apperr.InvalidInput, "event_id must be a UUID")
	}
	// Defensive decode: ingest validated, but persisted rows may predate or
	// bypass validation. Deterministic failure → blocked, never panic, and
	// never ownership (invalid events own nothing).
	raw, derr := sale.Decode(event.Payload)
	if derr != nil {
		return d.markBlocked(ctx, euid, now, ErrValidation, safeErr(derr))
	}
	valid, verr := sale.Validate(raw)
	if verr != nil {
		return d.markBlocked(ctx, euid, now, ErrValidation, safeErr(verr))
	}
	saleUID, err := parseUUID(valid.SaleID)
	if err != nil {
		return d.markBlocked(ctx, euid, now, ErrValidation, "sale_id must be a UUID")
	}
	duid, err := parseUUID(event.DeviceID)
	if err != nil {
		return d.markBlocked(ctx, euid, now, ErrValidation, "device_id must be a UUID")
	}
	// Durable arbitration first: permanent winner before any projection row.
	winner, err := d.arbitrate(ctx, saleUID, euid, duid, now)
	if err != nil {
		return d.persistRetry(ctx, euid, now, ErrProjection, "ownership arbitration failed")
	}
	if winner != event.EventID {
		// Another event permanently owns this sale_id: ZERO children.
		return d.markBlocked(ctx, euid, now, ErrSaleIDConflict,
			"sale_id already owned by another event")
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return sale.ProjectResult{}, transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	if err := q.ClaimProcessing(ctx, sqlcgen.ClaimProcessingParams{
		EventID: euid, Processor: sale.ProcessorSaleProjectionV1,
	}); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistRetry(ctx, euid, now, ErrProjection, "claim failed")
	}
	claim, err := q.LockProcessing(ctx, sqlcgen.LockProcessingParams{
		EventID: euid, Processor: sale.ProcessorSaleProjectionV1,
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistRetry(ctx, euid, now, ErrProjection, "lock failed")
	}
	mark := func(status string, attempt int32, next *time.Time, processed *time.Time, code, msg string) (sale.ProjectResult, error) {
		if err := q.MarkProcessing(ctx, sqlcgen.MarkProcessingParams{
			EventID: euid, Processor: sale.ProcessorSaleProjectionV1,
			Status: status, AttemptCount: attempt,
			LastAttemptAt: pgTime(now), NextAttemptAt: pgTimePtr(next),
			ProcessedAt:   pgTimePtr(processed),
			LastErrorCode: pgText(code), LastErrorMessage: pgText(boundMsg(msg)),
		}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistRetry(ctx, euid, now, ErrProjection, "mark failed")
		}
		if err := tx.Commit(ctx); err != nil {
			return d.persistRetry(ctx, euid, now, ErrProjection, "commit failed")
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
	// Re-verify the durable winner inside the projection tx: ownership could
	// only have been established by this event (arbitration above), but the
	// check makes the invariant explicit at the write boundary.
	owner, err := q.SaleOwnershipBySaleID(ctx, saleUID)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistRetry(ctx, euid, now, ErrProjection, "ownership verify failed")
	}
	if uuidString(owner.WinningEventID) != event.EventID {
		_ = tx.Rollback(ctx)
		return d.markBlocked(ctx, euid, now, ErrSaleIDConflict,
			"sale_id already owned by another event")
	}
	// Winner only: populate header, then children. Same-event replay uses
	// ON CONFLICT DO NOTHING throughout and stays idempotent.
	headerParams := projectionHeaderParams(saleUID, euid, duid, event, valid)
	if _, err := q.InsertSaleProjection(ctx, headerParams); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			existing, ferr := q.SaleProjectionBySaleID(ctx, saleUID)
			if ferr != nil {
				_ = tx.Rollback(ctx)
				return d.persistRetry(ctx, euid, now, ErrProjection, "header race with rollback")
			}
			if uuidString(existing.SourceEventID) != event.EventID {
				// Header from another event despite durable ownership:
				// projection/ownership integrity failure — never overwrite.
				_ = tx.Rollback(ctx)
				return d.markBlocked(ctx, euid, now, ErrProjection,
					"projection source differs from durable ownership")
			}
			if err := insertProjectionChildren(ctx, q, saleUID, valid); err != nil {
				_ = tx.Rollback(ctx)
				return d.persistRetry(ctx, euid, now, ErrProjection, "replay insert failed")
			}
			return mark(sale.ProcProcessed, claim.AttemptCount+1, nil, timePtr(now), "", "")
		}
		_ = tx.Rollback(ctx)
		return d.persistRetry(ctx, euid, now, ErrProjection, "projection insert failed")
	}
	if err := insertProjectionChildren(ctx, q, saleUID, valid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistRetry(ctx, euid, now, ErrProjection, "projection insert failed")
	}
	return mark(sale.ProcProcessed, claim.AttemptCount+1, nil, timePtr(now), "", "")
}

// arbitrate durably establishes the permanent winner for sale_id and returns
// the winning event ID. The decision commits in its own transaction before
// any projection row exists, so it survives projection rollback, restart,
// and rebuild. First writer wins; concurrent losers serialize on the row
// lock and read the same winner. A transient failure here decides nothing —
// the caller persists retry state and a later attempt re-arbitrates.
func (d Devices) arbitrate(ctx context.Context, saleUID, euid, duid pgtype.UUID, now time.Time) (string, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return "", transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	row, err := q.ClaimSaleOwnership(ctx, sqlcgen.ClaimSaleOwnershipParams{
		SaleID: saleUID, WinningEventID: euid, WinningDeviceID: duid,
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
	owner, err := q.SaleOwnershipBySaleID(ctx, saleUID)
	if err != nil {
		return "", transient(redact(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return "", transient(redact(err))
	}
	return uuidString(owner.WinningEventID), nil
}

// markBlocked records a deterministic blocked outcome (validation or
// conflict) in one short transaction. Zero projection writes precede it.
func (d Devices) markBlocked(ctx context.Context, euid pgtype.UUID, now time.Time, code, msg string) (sale.ProjectResult, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return sale.ProjectResult{}, transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	if err := q.ClaimProcessing(ctx, sqlcgen.ClaimProcessingParams{
		EventID: euid, Processor: sale.ProcessorSaleProjectionV1,
	}); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistRetry(ctx, euid, now, ErrProjection, "claim failed")
	}
	claim, err := q.LockProcessing(ctx, sqlcgen.LockProcessingParams{
		EventID: euid, Processor: sale.ProcessorSaleProjectionV1,
	})
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistRetry(ctx, euid, now, ErrProjection, "lock failed")
	}
	if claim.Status == sale.ProcProcessed {
		if err := tx.Commit(ctx); err != nil {
			return d.persistRetry(ctx, euid, now, ErrProjection, "commit failed")
		}
		return sale.ProjectResult{Outcome: sale.OutcomeProcessed}, nil
	}
	if err := q.MarkProcessing(ctx, sqlcgen.MarkProcessingParams{
		EventID: euid, Processor: sale.ProcessorSaleProjectionV1,
		Status: sale.ProcBlocked, AttemptCount: claim.AttemptCount + 1,
		LastAttemptAt: pgTime(now), NextAttemptAt: pgtype.Timestamptz{},
		ProcessedAt:   pgtype.Timestamptz{},
		LastErrorCode: pgText(code), LastErrorMessage: pgText(boundMsg(msg)),
	}); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistRetry(ctx, euid, now, ErrProjection, "mark failed")
	}
	if err := tx.Commit(ctx); err != nil {
		return d.persistRetry(ctx, euid, now, ErrProjection, "commit failed")
	}
	return sale.ProjectResult{Outcome: sale.OutcomeBlocked, ErrorCode: code}, nil
}

// persistRetry writes retry state in a SEPARATE durable transaction after
// the projection transaction rolled back (HIGH-04). The schedule
// (attempt_count, next_attempt_at, bounded diagnostic) commits even though
// the projection itself did not, so failures never hot-loop and survive
// restarts. Deterministic conflicts never reach here (they commit blocked).
// The returned error stays non-nil so callers observe the failed attempt;
// the retry state is already committed when it does.
func (d Devices) persistRetry(ctx context.Context, euid pgtype.UUID, now time.Time, code, msg string) (sale.ProjectResult, error) {
	fail := func() (sale.ProjectResult, error) {
		return sale.ProjectResult{Outcome: sale.OutcomeRetryable, ErrorCode: code},
			transient(errors.New("projection transient failure"))
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
		EventID: euid, Processor: sale.ProcessorSaleProjectionV1,
	}); err != nil {
		return fail()
	}
	claim, err := q.LockProcessing(ctx2, sqlcgen.LockProcessingParams{
		EventID: euid, Processor: sale.ProcessorSaleProjectionV1,
	})
	if err != nil {
		return fail()
	}
	if claim.Status == sale.ProcProcessed || claim.Status == sale.ProcBlocked {
		if err := tx.Commit(ctx2); err != nil {
			return fail()
		}
		if claim.Status == sale.ProcProcessed {
			return sale.ProjectResult{Outcome: sale.OutcomeProcessed}, nil
		}
		return sale.ProjectResult{Outcome: sale.OutcomeBlocked, ErrorCode: code}, nil
	}
	next := now.Add(sale.Backoff(int(claim.AttemptCount)))
	if err := q.MarkProcessing(ctx2, sqlcgen.MarkProcessingParams{
		EventID: euid, Processor: sale.ProcessorSaleProjectionV1,
		Status: sale.ProcRetry, AttemptCount: claim.AttemptCount + 1,
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

// projectionHeaderParams builds the ownership-claim header insert.
func projectionHeaderParams(saleUID, euid, duid pgtype.UUID, event sale.EventRecord, v sale.Validated) sqlcgen.InsertSaleProjectionParams {
	var fxBase, fxQuote, fxRate pgtype.Text
	var fxMicro pgtype.Int8
	if v.Fx != nil {
		fxBase = pgText(v.Fx.Base)
		fxQuote = pgText(v.Fx.Quote)
		fxRate = pgText(v.Fx.Rate)
		fxMicro = pgtype.Int8{Int64: v.Fx.RateMicrorate, Valid: true}
	}
	return sqlcgen.InsertSaleProjectionParams{
		SaleID: saleUID, SourceEventID: euid, SourceDeviceID: duid,
		SaleNumber: v.SaleNumber, Channel: v.Channel,
		OccurredAt: pgTime(v.Occurred), PaidAt: pgTime(v.Paid),
		ShopNameAr: v.Shop.NameAR, ShopNameEn: v.Shop.NameEN,
		ShopAddressAr: v.Shop.AddressAR, ShopAddressEn: v.Shop.AddressEN,
		ShopPhone:           v.Shop.Phone,
		ShopReceiptFooterAr: v.Shop.ReceiptFooterAR, ShopReceiptFooterEn: v.Shop.ReceiptFooterEN,
		CashierID: pgText(strPtr(v.Actor.CashierID)), CashierName: pgText(strPtr(v.Actor.CashierName)),
		Currency:      v.Currency,
		SubtotalMinor: v.Totals.Subtotal.AmountMinor, DiscountMinor: v.Totals.Discount.AmountMinor,
		TaxMinor: v.Totals.Tax.AmountMinor, TotalMinor: v.Totals.Total.AmountMinor,
		FxBase: fxBase, FxQuote: fxQuote, FxRate: fxRate, FxRateMicrorate: fxMicro,
		ReceivedAt: pgTime(event.ReceivedAt),
	}
}

// insertProjectionChildren writes lines + payments + classifications with
// deterministic identities and ON CONFLICT DO NOTHING, so replaying the
// owning event yields identical rows. It must only run after ownership of
// sale_id is proven inside the same transaction. Historical snapshots only:
// no lookups against mutable state anywhere on this path.
func insertProjectionChildren(ctx context.Context, q *sqlcgen.Queries, saleUID pgtype.UUID, v sale.Validated) error {
	for i := range v.Lines {
		line := &v.Lines[i]
		itemUID, _ := parseUUID(line.SaleItemID)
		if err := q.InsertSaleLine(ctx, sqlcgen.InsertSaleLineParams{
			SaleID: saleUID, SaleItemID: itemUID, Position: int32(i),
			ProductID: pgUUIDPtr(line.ProductID), VariantID: pgUUIDPtr(line.VariantID),
			Sku: line.SKU, ProductName: line.ProductName,
			WidthCm: pgIntPtr(line.WidthCM), HeightCm: pgIntPtr(line.HeightCM),
			Quantity:       int32(line.Quantity),
			UnitPriceMinor: line.UnitPrice.AmountMinor, UnitCurrency: line.UnitPrice.Currency,
			CostMinor: pgInt64Ptr(line.Cost), CostCurrency: pgText(pgStrPtr(line.Cost)),
			LineTotalMinor: line.LineTotal.AmountMinor, LineCurrency: line.LineTotal.Currency,
		}); err != nil {
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

func insertClassification(ctx context.Context, q *sqlcgen.Queries, saleUID, itemUID pgtype.UUID, kind string, pos int, c *sale.ClassificationSnapshot) error {
	cuid, _ := parseUUID(c.CategoryID)
	if err := q.InsertSaleClassification(ctx, sqlcgen.InsertSaleClassificationParams{
		SaleID: saleUID, SaleItemID: itemUID, ClassificationKind: kind,
		ClassificationID: cuid, NameAr: c.NameAR, NameEn: c.NameEN, Position: int32(pos),
	}); err != nil {
		return redact(err)
	}
	return nil
}

func strPtr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func pgStrPtr(m *sale.Money) string {
	if m == nil {
		return ""
	}
	return m.Currency
}

func pgInt64Ptr(m *sale.Money) pgtype.Int8 {
	if m == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: m.AmountMinor, Valid: true}
}

func pgIntPtr(v *int) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(*v), Valid: true}
}

func pgUUIDPtr(s *string) pgtype.UUID {
	if s == nil {
		return pgtype.UUID{}
	}
	u, err := parseUUID(*s)
	if err != nil {
		return pgtype.UUID{}
	}
	return u
}

// ProcessingStats returns operator visibility: counts by status, oldest
// pending age, and the latest error. Safe values only.
func (d Devices) ProcessingStats(ctx context.Context, processor string) (sale.Stats, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	q := sqlcgen.New(d.pool)
	stats := sale.Stats{Counts: map[string]int64{}}
	rows, err := q.ProcessingStatus(ctx, processor)
	if err != nil {
		return sale.Stats{}, apperr.Wrap(apperr.Internal, "processing stats", redact(err))
	}
	for _, r := range rows {
		stats.Counts[r.Status] = r.Total
	}
	age, err := q.OldestPendingAge(ctx, processor)
	if err != nil {
		return sale.Stats{}, apperr.Wrap(apperr.Internal, "processing stats", redact(err))
	}
	stats.PendingCount = age.Total
	if age.Oldest.Valid {
		t := age.Oldest.Time
		stats.OldestPending = &t
	}
	last, err := q.LastProcessingError(ctx, processor)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return sale.Stats{}, apperr.Wrap(apperr.Internal, "processing stats", redact(err))
		}
	} else {
		stats.LastErrorEvent = uuidString(last.EventID)
		stats.LastErrorCode = last.LastErrorCode.String
		stats.LastErrorMsg = last.LastErrorMessage.String
	}
	return stats, nil
}

// ResetProcessing returns a retry/blocked event to pending for manual
// recovery. The immutable source event is untouched.
func (d Devices) ResetProcessing(ctx context.Context, processor, eventID string) error {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(eventID)
	if err != nil {
		return apperr.New(apperr.InvalidInput, "event_id must be a UUID")
	}
	if err := sqlcgen.New(d.pool).ResetProcessing(ctx, sqlcgen.ResetProcessingParams{
		EventID: uid, Processor: processor,
	}); err != nil {
		return apperr.Wrap(apperr.Internal, "reset processing", redact(err))
	}
	return nil
}

// transient wraps retryable DB failures. The tx rolls back via defer;
// the event stays discoverable for the next scan.
func transient(err error) error {
	return apperr.Wrap(apperr.Unavailable, "projection transient failure", err)
}

func commitTx(ctx context.Context, tx pgx.Tx) error {
	if err := tx.Commit(ctx); err != nil {
		return transient(redact(err))
	}
	return nil
}

func claimProcessedAt(claim sqlcgen.LockProcessingRow, now time.Time) time.Time { return now }

func timePtr(t time.Time) *time.Time { return &t }

func pgTimePtr(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgTime(*t)
}

func pgText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

// boundMsg keeps diagnostics safe and bounded: validator messages contain
// no secrets, DB errors are redacted before reaching here; cap length.
func boundMsg(s string) string {
	const max = 500
	s = firstLine(s)
	if len(s) > max {
		return s[:max]
	}
	return s
}

func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' || c == '\r' {
			return s[:i]
		}
	}
	return s
}

func safeErr(err error) string {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae.Message
	}
	return "projection failed"
}
