package postgres

import (
	"context"
	"encoding/json"
	"errors"
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
func (d Devices) PendingCatalogEvents(ctx context.Context, processor, eventType string, limit int, asOf time.Time) ([]string, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).PendingCatalogEvents(ctx, sqlcgen.PendingCatalogEventsParams{
		Processor: processor, EventType: eventType, Limit: int32(limit),
		AsOf: pgTime(asOf),
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

// NextCatalogRetry reports the earliest future durable retry time for one
// processor; found=false when no retry is scheduled.
func (d Devices) NextCatalogRetry(ctx context.Context, processor, eventType string, asOf time.Time) (time.Time, bool, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	next, err := sqlcgen.New(d.pool).NextCatalogRetryAt(ctx, sqlcgen.NextCatalogRetryAtParams{
		Processor: processor, EventType: eventType,
		AsOf: pgTime(asOf),
	})
	if err != nil {
		return time.Time{}, false, apperr.Wrap(apperr.Internal, "scan next catalog retry", redact(err))
	}
	if !next.Valid {
		return time.Time{}, false, nil
	}
	return next.Time, true, nil
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

// entitySuperseded reports whether a newer accepted event for the entity
// could still land (R03 noise rule): an older revision that is already
// superseded resolves as a stale no-op instead of a terminal block, while
// the newer event is judged on its own merits. Terminally dead newer
// events (validation/conflict/cycle/depth) do not supersede: the older
// revision is then evaluated normally as the closest valid state.
func entitySuperseded(ctx context.Context, q *sqlcgen.Queries, eventType, idKey, entityID, processor, revKey string, incomingRevision int64, scope pgtype.UUID) (bool, error) {
	events, err := q.CatalogEntityEventRevisions(ctx, sqlcgen.CatalogEntityEventRevisionsParams{
		EventType: eventType, Column2: idKey, Column3: entityID,
		Processor: processor, Column5: revKey, StoreID: scope,
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
	if eventType == catalog.EventCategorySnapshotV2 {
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
	case catalog.EventProductSnapshotV2:
		if row, err := q.CatalogProductByID(ctx, uid); err == nil {
			projected = row.SourceRevision
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
	case catalog.EventCategorySnapshotV2:
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
		Processor: processor, Column5: revKey, StoreID: scope,
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
		return d.persistCatalogRetry(ctx, processor, euid, now, ErrProjection, "claim failed", err)
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

// catalogRetryDelay separates local dependency/transaction waits from outages.
// A mixed bootstrap can cross several dependent streams; the outage schedule
// accumulated minutes even after each dependency landed. Local retries wait
// 500ms, 1s, then 2s, always through the durable deadline (never a hot loop).
// All other failures retain the frozen outage backoff. Original driver errors
// are used only for classification; isolation and revision fencing are unchanged.
func catalogRetryDelay(attempt int, code string, causes ...error) time.Duration {
	local := code == ErrCatalogDependencyWait
	for _, cause := range causes {
		local = local || isSerializationFailure(cause)
	}
	if local {
		if attempt <= 0 {
			return 500 * time.Millisecond
		}
		if attempt == 1 {
			return time.Second
		}
		return 2 * time.Second
	}
	return catalog.Backoff(attempt)
}

// persistCatalogRetry writes retry state in a SEPARATE durable transaction
// after the projection transaction rolled back, mirroring the sale/return
// discipline: the schedule commits even though the projection did not.
func (d Devices) persistCatalogRetry(ctx context.Context, processor string, euid pgtype.UUID, now time.Time, code, msg string, causes ...error) (catalog.ProjectResult, error) {
	// The returned error keeps its frozen exact shape (the F12 retry
	// classifier contracts on it); the branch detail is persisted in
	// sync_event_processing.last_error_message for diagnostics.
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
	next := now.Add(catalogRetryDelay(int(attempt), code, causes...))
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
