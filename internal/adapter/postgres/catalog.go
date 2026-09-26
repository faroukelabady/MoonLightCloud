package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres/sqlcgen"
	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/catalog"
	"github.com/jackc/pgx/v5"
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
	ErrCatalogInvalidRelation  = "CATALOG_INVALID_RELATION"
)

// PendingCatalogEvents returns due candidate catalog event IDs for one
// processor (durable lazy discovery: accepted events with no processing
// row are discoverable, so pre-projector history backfills with no resend).
func (d Devices) PendingCatalogEvents(ctx context.Context, processor, eventType string, limit int) ([]string, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).PendingCatalogEvents(ctx, sqlcgen.PendingCatalogEventsParams{
		Processor: processor, EventType: eventType, Limit: int32(limit),
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
	hash    [32]byte
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
	hash := sha256.Sum256(event.Payload)
	return catalogAttempt{euid: euid, duid: duid, now: now, payload: event.Payload, hash: hash}, nil
}

// revisionDisposition compares the incoming revision/hash against current
// projection state: higher wins, stale is a terminal no-op, equal revision
// with an identical payload hash is idempotent, and equal revision with a
// different hash is a deterministic integrity conflict.
func revisionDisposition(storedRevision int64, storedHash []byte, incomingRevision int64, incomingHash [32]byte) (proceed bool, outcome catalog.Outcome, code string) {
	switch {
	case incomingRevision > storedRevision:
		return true, 0, ""
	case incomingRevision < storedRevision:
		return false, catalog.OutcomeProcessed, ""
	default:
		if bytes.Equal(storedHash, incomingHash[:]) {
			return false, catalog.OutcomeProcessed, ""
		}
		return false, catalog.OutcomeBlocked, ErrCatalogRevisionConflict
	}
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
		return d.persistCatalogRetry(ctx, processor, euid, now, ErrProjection, "claim failed")
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

// persistCatalogRetry writes retry state in a SEPARATE durable transaction
// after the projection transaction rolled back, mirroring the sale/return
// discipline: the schedule commits even though the projection did not.
func (d Devices) persistCatalogRetry(ctx context.Context, processor string, euid pgtype.UUID, now time.Time, code, msg string) (catalog.ProjectResult, error) {
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
	next := now.Add(catalog.Backoff(int(attempt)))
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

// hasCatalogParents reports whether every parent ID has a projected row.
func hasCatalogParents(ctx context.Context, q *sqlcgen.Queries, parentIDs []string) (bool, error) {
	for _, parent := range parentIDs {
		uid, err := parseUUID(parent)
		if err != nil {
			return false, nil
		}
		if _, err := q.CatalogCategoryByID(ctx, uid); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}

// checkCategoryGraph validates the replaced edge set for one child: no
// self edge (already enforced at ingestion, rechecked defensively),
// acyclic, and every root→leaf path at most maxCatalogDepthNodes nodes
// (mirrors the Retail max-3-node DAG invariant as a global property).
func checkCategoryGraph(allEdges []catalog.Edge, childID string, newParents []string) error {
	const maxCatalogDepthNodes = 3
	children := map[string]map[string]bool{}
	edgeSet := map[[2]string]bool{}
	add := func(parent, child string) {
		if parent == child {
			return
		}
		if children[parent] == nil {
			children[parent] = map[string]bool{}
		}
		if !children[parent][child] {
			children[parent][child] = true
			edgeSet[[2]string{parent, child}] = true
		}
	}
	for _, edge := range allEdges {
		if edge.ChildID == childID {
			continue
		}
		add(edge.ParentID, edge.ChildID)
	}
	for _, parent := range newParents {
		if parent == childID {
			return errors.New("self edge")
		}
		add(parent, childID)
	}
	// Iterative DFS from every node: cycle detection + longest path.
	const visiting = 1
	const done = 2
	state := map[string]int{}
	depth := map[string]int{}
	var visit func(node string, stack int) error
	visit = func(node string, stack int) error {
		if stack > 10000 {
			return errors.New("graph too deep")
		}
		switch state[node] {
		case done:
			return nil
		case visiting:
			return errors.New("cycle")
		}
		state[node] = visiting
		longest := 1
		for child := range children[node] {
			if err := visit(child, stack+1); err != nil {
				return err
			}
			if depth[child]+1 > longest {
				longest = depth[child] + 1
			}
		}
		if longest > maxCatalogDepthNodes {
			return errors.New("depth exceeded")
		}
		depth[node] = longest
		state[node] = done
		return nil
	}
	nodes := map[string]bool{childID: true}
	for pair := range edgeSet {
		nodes[pair[0]] = true
		nodes[pair[1]] = true
	}
	for node := range nodes {
		if state[node] == done {
			continue
		}
		if err := visit(node, 0); err != nil {
			return err
		}
	}
	return nil
}

// reachableFromTop reports the descendant set of top over current edges
// (top itself excluded, mirroring the Retail reachableFrom helper).
func reachableFromTop(allEdges []catalog.Edge, topID string) map[string]bool {
	children := map[string][]string{}
	for _, edge := range allEdges {
		children[edge.ParentID] = append(children[edge.ParentID], edge.ChildID)
	}
	reached := map[string]bool{}
	queue := append([]string{}, children[topID]...)
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		if reached[node] {
			continue
		}
		reached[node] = true
		queue = append(queue, children[node]...)
	}
	return reached
}

// hasParents reports whether a category currently has any parent edge.
func hasParents(allEdges []catalog.Edge, categoryID string) bool {
	for _, edge := range allEdges {
		if edge.ChildID == categoryID {
			return true
		}
	}
	return false
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
	raw, derr := catalog.DecodeCategorySnapshot(attempt.payload)
	if derr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrValidation, safeErr(derr))
	}
	valid, verr := catalog.ValidateCategorySnapshot(raw)
	if verr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrValidation, safeErr(verr))
	}
	cuid, err := parseUUID(valid.CategoryID)
	if err != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrValidation, "category_id must be a UUID")
	}

	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return catalog.ProjectResult{}, transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	count, done, hasDone, err := claimCatalogRow(ctx, q, tx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "claim failed")
	}
	if hasDone {
		if done.Outcome == catalog.OutcomeBlocked {
			return catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: ErrProjection}, nil
		}
		return done, nil
	}

	// Revision gate against current projection (missing row = first write).
	current, err := q.CatalogCategoryByID(ctx, cuid)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "category lookup failed")
	}
	if err == nil {
		proceed, outcome, code := revisionDisposition(current.SourceRevision, current.SourcePayloadHash, valid.CatalogRevision, attempt.hash)
		if !proceed {
			if outcome == catalog.OutcomeBlocked {
				_ = tx.Rollback(ctx)
				return d.markCatalogBlocked(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, code, "equal revision with conflicting payload")
			}
			return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorCategoryProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
		}
	}

	// Parent dependency: missing parents wait retryably, never terminally.
	parentsOK, err := hasCatalogParents(ctx, q, valid.ParentIDs)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "parent lookup failed")
	}
	if !parentsOK {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrCatalogDependencyWait, "parent category not yet projected")
	}

	// DAG defense over (current edges with this child's edges replaced).
	edgeRows, err := q.AllCatalogCategoryEdges(ctx)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "edge lookup failed")
	}
	allEdges := make([]catalog.Edge, 0, len(edgeRows))
	for _, edge := range edgeRows {
		allEdges = append(allEdges, catalog.Edge{ParentID: uuidString(edge.ParentID), ChildID: uuidString(edge.ChildID)})
	}
	if err := checkCategoryGraph(allEdges, valid.CategoryID, valid.ParentIDs); err != nil {
		_ = tx.Rollback(ctx)
		return d.markCatalogBlocked(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrCatalogCategoryCycle, "category graph violates DAG invariants")
	}

	nameAR := ""
	nameEN := pgtype.Text{}
	for _, name := range valid.Names {
		if name.Locale == catalog.LocaleAR && nameAR == "" {
			nameAR = name.Name
		}
		if name.Locale == catalog.LocaleEN && !nameEN.Valid {
			nameEN = pgText(name.Name)
		}
	}
	if nameAR == "" && len(valid.Names) > 0 {
		nameAR = valid.Names[0].Name
	}
	hash := attempt.hash[:]
	if err := q.UpsertCatalogCategory(ctx, sqlcgen.UpsertCatalogCategoryParams{
		CategoryID: cuid, Status: valid.Status, NameAr: nameAR, NameEn: nameEN,
		SourceRevision: valid.CatalogRevision, SourceEventID: attempt.euid, SourceDeviceID: attempt.duid,
		SourcePayloadHash: hash, SourceReceivedAt: pgTime(event.ReceivedAt),
	}); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "category upsert failed")
	}
	if err := q.DeleteCatalogCategoryEdges(ctx, cuid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "edge replace failed")
	}
	for position, parent := range valid.ParentIDs {
		puid, _ := parseUUID(parent)
		if err := q.InsertCatalogCategoryEdge(ctx, sqlcgen.InsertCatalogCategoryEdgeParams{
			ParentID: puid, ChildID: cuid, Position: int32(position),
		}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorCategoryProjectionV1, attempt.euid, now, ErrProjection, "edge insert failed")
		}
	}
	return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorCategoryProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
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
	tuid, err := parseUUID(valid.TagID)
	if err != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrValidation, "tag_id must be a UUID")
	}

	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return catalog.ProjectResult{}, transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	count, done, hasDone, err := claimCatalogRow(ctx, q, tx, catalog.ProcessorTagProjectionV1, attempt.euid, now)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "claim failed")
	}
	if hasDone {
		if done.Outcome == catalog.OutcomeBlocked {
			return catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: ErrProjection}, nil
		}
		return done, nil
	}

	current, err := q.CatalogTagByID(ctx, tuid)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "tag lookup failed")
	}
	if err == nil {
		proceed, outcome, code := revisionDisposition(current.SourceRevision, current.SourcePayloadHash, valid.CatalogRevision, attempt.hash)
		if !proceed {
			if outcome == catalog.OutcomeBlocked {
				_ = tx.Rollback(ctx)
				return d.markCatalogBlocked(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, code, "equal revision with conflicting payload")
			}
			return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorTagProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
		}
	}

	var nameAR, nameEN pgtype.Text
	for _, name := range valid.Names {
		switch name.Locale {
		case catalog.LocaleAR:
			nameAR = pgText(name.Name)
		case catalog.LocaleEN:
			nameEN = pgText(name.Name)
		}
	}
	hash := attempt.hash[:]
	if err := q.UpsertCatalogTag(ctx, sqlcgen.UpsertCatalogTagParams{
		TagID: tuid, Slug: valid.Slug, IsActive: valid.IsActive,
		NameAr: nameAR, NameEn: nameEN,
		SourceRevision: valid.CatalogRevision, SourceEventID: attempt.euid, SourceDeviceID: attempt.duid,
		SourcePayloadHash: hash, SourceReceivedAt: pgTime(event.ReceivedAt),
	}); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorTagProjectionV1, attempt.euid, now, ErrProjection, "tag upsert failed")
	}
	return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorTagProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
}

// ProjectProduct projects one product snapshot with revision ordering,
// dependency waits, and structural checks. Either a complete product
// projection commits or nothing does; failure leaves the previous
// revision visible.
func (d Devices) ProjectProduct(ctx context.Context, event catalog.EventRecord, now time.Time) (catalog.ProjectResult, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	attempt, blocked := startCatalogAttempt(event, now)
	if blocked != nil {
		euid, _ := parseUUID(event.EventID)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, euid, now, blocked.ErrorCode, "event identity must be UUIDs")
	}
	raw, derr := catalog.DecodeProductSnapshot(attempt.payload)
	if derr != nil {
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrValidation, safeErr(derr))
	}
	valid, verr := catalog.ValidateProductSnapshot(raw)
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

	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return catalog.ProjectResult{}, transient(redact(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlcgen.New(tx)
	count, done, hasDone, err := claimCatalogRow(ctx, q, tx, catalog.ProcessorProductProjectionV1, attempt.euid, now)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "claim failed")
	}
	if hasDone {
		if done.Outcome == catalog.OutcomeBlocked {
			return catalog.ProjectResult{Outcome: catalog.OutcomeBlocked, ErrorCode: ErrProjection}, nil
		}
		return done, nil
	}

	current, err := q.CatalogProductByID(ctx, puid)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "product lookup failed")
	}
	if err == nil {
		proceed, outcome, code := revisionDisposition(current.SourceRevision, current.SourcePayloadHash, valid.CatalogRevision, attempt.hash)
		if !proceed {
			if outcome == catalog.OutcomeBlocked {
				_ = tx.Rollback(ctx)
				return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, code, "equal revision with conflicting payload")
			}
			return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
		}
	}

	// Dependency resolution: every referenced category and tag must exist.
	// Absence is a retryable wait (out-of-order arrival), never terminal.
	wait := func(msg string) (catalog.ProjectResult, error) {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrCatalogDependencyWait, msg)
	}
	if _, err := q.CatalogCategoryByID(ctx, topUID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return wait("top category not yet projected")
		}
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "top category lookup failed")
	}
	for _, sub := range valid.SubcategoryIDs {
		suid, _ := parseUUID(sub)
		if _, err := q.CatalogCategoryByID(ctx, suid); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return wait("subcategory not yet projected")
			}
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "subcategory lookup failed")
		}
	}
	for _, tagID := range valid.TagIDs {
		guid, _ := parseUUID(tagID)
		if _, err := q.CatalogTagByID(ctx, guid); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return wait("tag not yet projected")
			}
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "tag lookup failed")
		}
	}

	// Structural integrity against current projection: the top must be a
	// root (no parents) and every sub must be reachable from it. Active
	// flags are deliberately not required (mirrors Retail
	// retained-assignment semantics).
	edgeRows, err := q.AllCatalogCategoryEdges(ctx)
	if err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "edge lookup failed")
	}
	allEdges := make([]catalog.Edge, 0, len(edgeRows))
	for _, edge := range edgeRows {
		allEdges = append(allEdges, catalog.Edge{ParentID: uuidString(edge.ParentID), ChildID: uuidString(edge.ChildID)})
	}
	if hasParents(allEdges, valid.TopCategoryID) {
		_ = tx.Rollback(ctx)
		return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrCatalogInvalidRelation, "top category is not a root")
	}
	reachable := reachableFromTop(allEdges, valid.TopCategoryID)
	for _, sub := range valid.SubcategoryIDs {
		if !reachable[sub] {
			_ = tx.Rollback(ctx)
			return d.markCatalogBlocked(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrCatalogInvalidRelation, "subcategory not reachable from top category")
		}
	}

	var description pgtype.Text
	if valid.Description != nil {
		description = pgText(*valid.Description)
	}
	var width, height pgtype.Int4
	if valid.WidthCM != nil {
		width = pgtype.Int4{Int32: int32(*valid.WidthCM), Valid: true}
	}
	if valid.HeightCM != nil {
		height = pgtype.Int4{Int32: int32(*valid.HeightCM), Valid: true}
	}
	hash := attempt.hash[:]
	if err := q.UpsertCatalogProduct(ctx, sqlcgen.UpsertCatalogProductParams{
		ProductID: puid, Sku: valid.SKU, Name: valid.Name, Description: description,
		TopCategoryID: topUID, WidthCm: width, HeightCm: height, IsActive: valid.IsActive,
		SourceRevision: valid.CatalogRevision, SourceEventID: attempt.euid, SourceDeviceID: attempt.duid,
		SourcePayloadHash: hash, SourceReceivedAt: pgTime(event.ReceivedAt),
	}); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "product upsert failed")
	}
	if err := q.DeleteCatalogProductPrices(ctx, puid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "price replace failed")
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
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "price insert failed")
		}
	}
	if err := q.DeleteCatalogProductTranslations(ctx, puid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "translation replace failed")
	}
	for _, translation := range valid.Translations {
		var tdesc pgtype.Text
		if translation.Description != nil {
			tdesc = pgText(*translation.Description)
		}
		if err := q.InsertCatalogProductTranslation(ctx, sqlcgen.InsertCatalogProductTranslationParams{
			ProductID: puid, Locale: translation.Locale, Name: translation.Name, Description: tdesc,
		}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "translation insert failed")
		}
	}
	if err := q.DeleteCatalogProductSubcategories(ctx, puid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "subcategory replace failed")
	}
	for position, sub := range valid.SubcategoryIDs {
		suid, _ := parseUUID(sub)
		if err := q.InsertCatalogProductSubcategory(ctx, sqlcgen.InsertCatalogProductSubcategoryParams{
			ProductID: puid, CategoryID: suid, Position: int32(position),
		}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "subcategory insert failed")
		}
	}
	if err := q.DeleteCatalogProductTags(ctx, puid); err != nil {
		_ = tx.Rollback(ctx)
		return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "tag replace failed")
	}
	for _, tagID := range valid.TagIDs {
		guid, _ := parseUUID(tagID)
		if err := q.InsertCatalogProductTag(ctx, sqlcgen.InsertCatalogProductTagParams{
			ProductID: puid, TagID: guid,
		}); err != nil {
			_ = tx.Rollback(ctx)
			return d.persistCatalogRetry(ctx, catalog.ProcessorProductProjectionV1, attempt.euid, now, ErrProjection, "tag insert failed")
		}
	}
	return finishCatalogAttempt(ctx, q, tx, catalog.ProcessorProductProjectionV1, attempt.euid, count, now, catalog.ProcProcessed, "", "")
}

// Catalog read API for future phases (internal/catalog.Repository).

func catalogNotFound(what string) error {
	return apperr.New(apperr.NotFound, what+" not found")
}

// CatalogProduct assembles the full projected product aggregate.
func (d Devices) CatalogProduct(ctx context.Context, id string) (catalog.Product, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return catalog.Product{}, catalogNotFound("product")
	}
	q := sqlcgen.New(d.pool)
	row, err := q.CatalogProductByID(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.Product{}, catalogNotFound("product")
		}
		return catalog.Product{}, apperr.Wrap(apperr.Internal, "catalog product", redact(err))
	}
	prices, err := q.CatalogProductPrices(ctx, uid)
	if err != nil {
		return catalog.Product{}, apperr.Wrap(apperr.Internal, "catalog prices", redact(err))
	}
	translations, err := q.CatalogProductTranslations(ctx, uid)
	if err != nil {
		return catalog.Product{}, apperr.Wrap(apperr.Internal, "catalog translations", redact(err))
	}
	subs, err := q.CatalogProductSubcategories(ctx, uid)
	if err != nil {
		return catalog.Product{}, apperr.Wrap(apperr.Internal, "catalog subcategories", redact(err))
	}
	tags, err := q.CatalogProductTags(ctx, uid)
	if err != nil {
		return catalog.Product{}, apperr.Wrap(apperr.Internal, "catalog tags", redact(err))
	}
	product := catalog.Product{
		ProductHeader: catalog.ProductHeader{
			ID: uuidString(row.ProductID), SKU: row.Sku, Name: row.Name,
			IsActive: row.IsActive, Revision: row.SourceRevision,
			SourceEventID: uuidString(row.SourceEventID),
		},
		TopCategoryID: uuidString(row.TopCategoryID),
	}
	if row.Description.Valid {
		desc := row.Description.String
		product.Description = &desc
	}
	if row.WidthCm.Valid {
		width := int(row.WidthCm.Int32)
		product.WidthCM = &width
	}
	if row.HeightCm.Valid {
		height := int(row.HeightCm.Int32)
		product.HeightCM = &height
	}
	for _, price := range prices {
		entry := catalog.Price{Currency: price.Currency, PriceCents: price.PriceMinor}
		if price.CostMinor.Valid {
			cost := price.CostMinor.Int64
			entry.CostCents = &cost
		}
		product.Prices = append(product.Prices, entry)
	}
	for _, translation := range translations {
		entry := catalog.CatalogProductTranslation{Locale: translation.Locale, Name: translation.Name}
		if translation.Description.Valid {
			desc := translation.Description.String
			entry.Description = &desc
		}
		product.Translations = append(product.Translations, entry)
	}
	for _, sub := range subs {
		product.SubcategoryIDs = append(product.SubcategoryIDs, uuidString(sub))
	}
	for _, tag := range tags {
		product.TagIDs = append(product.TagIDs, uuidString(tag))
	}
	return product, nil
}

// CatalogActiveProducts lists active product headers.
func (d Devices) CatalogActiveProducts(ctx context.Context, limit int) ([]catalog.ProductHeader, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).CatalogActiveProducts(ctx, int32(limit))
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "catalog products", redact(err))
	}
	out := make([]catalog.ProductHeader, 0, len(rows))
	for _, row := range rows {
		out = append(out, catalog.ProductHeader{
			ID: uuidString(row.ProductID), SKU: row.Sku, Name: row.Name,
			IsActive: true, Revision: row.SourceRevision,
			SourceEventID: uuidString(row.SourceEventID),
		})
	}
	return out, nil
}

// CatalogCategory assembles the projected category with its parent set.
func (d Devices) CatalogCategory(ctx context.Context, id string) (catalog.Category, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return catalog.Category{}, catalogNotFound("category")
	}
	q := sqlcgen.New(d.pool)
	row, err := q.CatalogCategoryByID(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.Category{}, catalogNotFound("category")
		}
		return catalog.Category{}, apperr.Wrap(apperr.Internal, "catalog category", redact(err))
	}
	parents, err := q.CatalogCategoryParents(ctx, uid)
	if err != nil {
		return catalog.Category{}, apperr.Wrap(apperr.Internal, "catalog parents", redact(err))
	}
	category := catalog.Category{
		ID: uuidString(row.CategoryID), Status: row.Status, NameAR: row.NameAr,
		Revision: row.SourceRevision, SourceEventID: uuidString(row.SourceEventID),
	}
	if row.NameEn.Valid {
		name := row.NameEn.String
		category.NameEN = &name
	}
	for _, parent := range parents {
		category.ParentIDs = append(category.ParentIDs, uuidString(parent))
	}
	return category, nil
}

// CatalogCategoryEdges lists every projected DAG edge.
func (d Devices) CatalogCategoryEdges(ctx context.Context) ([]catalog.Edge, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	rows, err := sqlcgen.New(d.pool).AllCatalogCategoryEdges(ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, "catalog edges", redact(err))
	}
	out := make([]catalog.Edge, 0, len(rows))
	for _, row := range rows {
		out = append(out, catalog.Edge{ParentID: uuidString(row.ParentID), ChildID: uuidString(row.ChildID)})
	}
	return out, nil
}

// CatalogTag returns the projected tag.
func (d Devices) CatalogTag(ctx context.Context, id string) (catalog.Tag, error) {
	ctx, cancel := d.ctx(ctx)
	defer cancel()
	uid, err := parseUUID(id)
	if err != nil {
		return catalog.Tag{}, catalogNotFound("tag")
	}
	row, err := sqlcgen.New(d.pool).CatalogTagByID(ctx, uid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return catalog.Tag{}, catalogNotFound("tag")
		}
		return catalog.Tag{}, apperr.Wrap(apperr.Internal, "catalog tag", redact(err))
	}
	tag := catalog.Tag{
		ID: uuidString(row.TagID), Slug: row.Slug, IsActive: row.IsActive,
		Revision: row.SourceRevision, SourceEventID: uuidString(row.SourceEventID),
	}
	if row.NameAr.Valid {
		name := row.NameAr.String
		tag.NameAR = &name
	}
	if row.NameEn.Valid {
		name := row.NameEn.String
		tag.NameEN = &name
	}
	return tag, nil
}
