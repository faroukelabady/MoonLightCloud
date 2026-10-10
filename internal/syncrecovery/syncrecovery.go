// Package syncrecovery implements the Phase 20 R2 Cloud-restore
// recovery protocol.
//
// The problem this closes: a successful pg_restore can leave Cloud
// infrastructurally healthy while its business projections are
// historically incomplete — missing events Retail already delivered
// and acknowledged. Ordinary sync reported "no pending work" and the
// gap stayed silent. R1 added a replay primitive (RequeueDeliveredBatch)
// but nothing detected when it was needed and nothing could reach it.
//
// Protocol (approach A + B from the phase prompt):
//
//  1. DETECT   the supported restore workflow marks every device
//     RECOVERY_RECONCILIATION_REQUIRED (durable, survives
//     Cloud restarts).
//  2. EXPOSE   Retail reads the flag over its normal authenticated
//     control channel; Store sync status refuses to claim
//     full recovery while it is set.
//  3. VERIFY   Retail walks its RETAINED delivered history in bounded
//     pages and asks Cloud which (event_id, payload_hash)
//     identities are missing. Identity comparison, never
//     counts and never a fabricated ordering.
//  4. REPLAY   Retail re-queues missing events and resends them.
//     Cloud ingest is idempotent: duplicates ACK
//     already_accepted; missing events ingest and reproject.
//  5. COMPLETE only after a verification pass reports zero missing
//     events is the flag cleared. A Store is never marked
//     reconciled while a device is unresolved.
//
// Cloud never writes Retail Sales, never fabricates events, never
// replaces inventory and never reissues a business mutation. The
// canonical event bytes are simply re-sent by Retail.
package syncrecovery

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// State is one device's durable recovery requirement.
type State struct {
	DeviceID       string     `json:"device_id"`
	Required       bool       `json:"required"`
	Reason         string     `json:"reason"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	ClearedAt      *time.Time `json:"cleared_at,omitempty"`
	VerifiedEvents int64      `json:"verified_events"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// Report is what the device sees: is reconciliation required and why.
type Report struct {
	Required bool   `json:"required"`
	Reason   string `json:"reason,omitempty"`
	// DevicesUnresolved is how many devices of this Store are still
	// unreconciled. A Store is never reported recovered while any
	// device is outstanding.
	DevicesUnresolved int `json:"devices_unresolved"`
	// Instructions are safe, bounded, operator-actionable text. They
	// never contain credentials, payloads or SQL.
	Instructions string `json:"instructions,omitempty"`
	// ProjectionsPending counts event processors still retrying or
	// blocked (§5 ingest is not projection). Non-zero means business
	// projections have NOT converged, so a Store must never be called
	// reconciled even when event history is verified and replayed.
	ProjectionsPending int `json:"projections_pending"`
	// StoreReconciled is the terminal state: zero devices outstanding
	// AND zero projections pending. Distinct from the event-history
	// verification that ReconcileCloud performs.
	StoreReconciled bool `json:"store_reconciled"`
}

// VerifyRequest is one bounded page of retained Retail history.
type VerifyRequest struct {
	Events []EventIdentity `json:"events"`
	// EndOfHistory marks the FINAL page of the device's retained
	// history. Only an end-of-history page can make verification
	// complete; an early claim is rejected.
	EndOfHistory bool `json:"end_of_history"`
	// DeclaredTotal is the retained-history size the device commits to
	// walking. Sent on the FIRST page of a pass (and again after a
	// restart, which starts a fresh generation). A page carrying a
	// positive DeclaredTotal begins a new pass; later pages omit it.
	//
	// Complete() refuses unless the cumulative verified count reaches
	// DeclaredTotal, so verifying a subset can never look exhaustive.
	DeclaredTotal int64 `json:"declared_total"`
}

// VerifyProgress is the server-side coverage ledger for one device's
// verification pass. It is what makes recovery completion provably
// exhaustive instead of client-asserted.
type VerifyProgress struct {
	Generation  int64  `json:"generation"`
	LastEventID string `json:"last_event_id"`
	Pages       int    `json:"pages"`
	Events      int64  `json:"events"`
	Complete    bool   `json:"complete"`
	// DeclaredTotal is the retained-history size the device declared
	// when the pass began. Complete requires Events to reach it, so
	// skipped pages cannot look exhaustive.
	DeclaredTotal int64 `json:"declared_total"`
}

// EventIdentity is the immutable identity of one canonical event:
// the event ID plus the hash of its exact payload bytes. Two events
// with the same ID and different hashes are a conflict, never a match.
type EventIdentity struct {
	EventID     string `json:"event_id"`
	PayloadHash string `json:"payload_hash"`
}

// VerifyResponse reports which identities Cloud is missing.
type VerifyResponse struct {
	Missing []string `json:"missing"`
	// CloudEventCount is informational only: counts never prove
	// equality, and this field is deliberately not used for it.
	CloudEventCount int64 `json:"cloud_event_count"`
}

// Store is the durable recovery-state boundary.
type Store interface {
	UpsertRecoveryState(ctx context.Context, deviceID, reason string) error
	ClearRecoveryState(ctx context.Context, deviceID string, verifiedEvents int64) error
	GetRecoveryState(ctx context.Context, deviceID string) (State, bool, error)
	CountRecoveryRequired(ctx context.Context) (int, error)
	// MissingSyncEvents returns which of the reported event IDs Cloud
	// does not hold. Payload hashes are matched by the caller.
	MissingSyncEvents(ctx context.Context, eventIDs []string) ([]string, error)
	// VerifyProgress reads the server-side coverage ledger.
	VerifyProgress(ctx context.Context, deviceID string) (VerifyProgress, bool, error)
	// BeginVerifyPass starts a fresh verification generation and
	// records the retained-history size the device will walk.
	BeginVerifyPass(ctx context.Context, deviceID string, declaredTotal int64) error
	// CountDeviceSyncEvents reports how many events Cloud already holds
	// for this device - an independent lower bound on the device's
	// retained history (Phase 20 R5).
	CountDeviceSyncEvents(ctx context.Context, deviceID string) (int64, error)
	// RecordVerifyPage advances the coverage cursor. Returns false when
	// the page does not advance it (skipped or replayed page).
	RecordVerifyPage(ctx context.Context, deviceID, lastEventID string, events int64, endOfHistory bool) (bool, error)
	// CountPendingProjections reports processors still retrying or
	// blocked (§5).
	CountPendingProjections(ctx context.Context) (int64, error)
	// SyncEventPayloadHash returns the stored hash for one event.
	SyncEventPayloadHash(ctx context.Context, eventID string) (string, bool, error)
}

// Service drives the protocol. It holds no network state and never
// contacts Retail: every step is initiated by Retail over its existing
// authenticated control channel.
type Service struct {
	store Store
	now   func() time.Time
}

// NewService wires the recovery service.
func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now}
}

// MaxVerifyPage bounds one verification request so a long retained
// history cannot become an unbounded payload.
const MaxVerifyPage = 200

// MarkRestoreRequired is called by the supported Cloud restore
// workflow. It durably marks every provided device as needing
// reconciliation. The state survives restarts because it is a table.
func (s *Service) MarkRestoreRequired(ctx context.Context, deviceIDs []string, reason string) error {
	if len(deviceIDs) == 0 {
		return fmt.Errorf("no devices to mark")
	}
	if strings.TrimSpace(reason) == "" {
		reason = "cloud database restored from backup"
	}
	if len(reason) > 200 {
		reason = reason[:200]
	}
	for _, id := range deviceIDs {
		if err := s.store.UpsertRecoveryState(ctx, id, reason); err != nil {
			return err
		}
	}
	return nil
}

// Status reports the device's recovery requirement. A device that is
// not marked is NOT required — but a Store with any other device still
// outstanding reports DevicesUnresolved so the operator cannot mistake
// partial progress for full recovery.
func (s *Service) Status(ctx context.Context, deviceID string) (Report, error) {
	state, found, err := s.store.GetRecoveryState(ctx, deviceID)
	if err != nil {
		return Report{}, err
	}
	unresolved, err := s.store.CountRecoveryRequired(ctx)
	if err != nil {
		return Report{}, err
	}
	pending, err := s.store.CountPendingProjections(ctx)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		Required:           found && state.Required,
		DevicesUnresolved:  unresolved,
		ProjectionsPending: int(pending),
		StoreReconciled:    unresolved == 0 && pending == 0,
	}
	if report.Required {
		report.Reason = state.Reason
		report.Instructions = "Cloud was restored from a backup and may be missing events this device already delivered. " +
			"Open Settings -> Cloud Sync -> Cloud recovery and run the Reconcile action so retained delivered events " +
			"are replayed through reconciliation, then verify until no events are missing. Do not treat this Store as synchronized before then."
	}
	return report, nil
}

// Verify checks one bounded page of retained Retail history against
// what Cloud actually holds, and records server-side coverage progress.
//
// Phase 20 R3 (§3) closes a material defect: R2's Complete trusted a
// client-supplied "verified count", so a client could verify one page
// of twelve and still clear the flag. The server now owns the proof:
//
//   - pages MUST advance the keyset cursor (verify_last_event_id);
//     skipping a page or revisiting one is rejected;
//   - each page is recorded (verify_pages / verify_events);
//   - a page may declare end_of_history, which is the ONLY way
//     verify_complete becomes true;
//   - Complete refuses unless verify_complete is true.
//
// Identity is (event_id, payload_hash): an event ID present with a
// different hash is reported missing — a content conflict, never
// silently accepted as a match.
func (s *Service) Verify(ctx context.Context, deviceID string, req VerifyRequest) (VerifyResponse, error) {
	if len(req.Events) == 0 {
		return VerifyResponse{}, fmt.Errorf("empty verification page")
	}
	if len(req.Events) > MaxVerifyPage {
		return VerifyResponse{}, fmt.Errorf("verification page exceeds %d events", MaxVerifyPage)
	}
	// A page carrying a declared total opens a fresh verification
	// generation. This is the only way the server learns how much
	// retained history the device commits to walking; without it the
	// ledger's total is zero and any short walk would look complete.
	if req.DeclaredTotal > 0 {
		if err := s.BeginVerifyPass(ctx, deviceID, req.DeclaredTotal); err != nil {
			return VerifyResponse{}, err
		}
	}
	ids := make([]string, 0, len(req.Events))
	for _, e := range req.Events {
		if strings.TrimSpace(e.EventID) == "" {
			return VerifyResponse{}, fmt.Errorf("empty event id")
		}
		ids = append(ids, e.EventID)
	}
	missingIDs, err := s.store.MissingSyncEvents(ctx, ids)
	if err != nil {
		return VerifyResponse{}, err
	}
	missing := map[string]bool{}
	for _, id := range missingIDs {
		missing[id] = true
	}
	out := make([]string, 0, len(missingIDs))
	for _, e := range req.Events {
		if missing[e.EventID] {
			out = append(out, e.EventID)
			continue
		}
		stored, found, err := s.store.SyncEventPayloadHash(ctx, e.EventID)
		if err != nil {
			return VerifyResponse{}, err
		}
		if !found || !strings.EqualFold(stored, strings.TrimSpace(e.PayloadHash)) {
			out = append(out, e.EventID)
		}
	}

	// Record coverage. The cursor must ADVANCE: the page's highest
	// event ID is the new keyset position, and the row update only
	// applies when it moves forward. A skipped or replayed page is
	// refused by the UPDATE affecting zero rows.
	lastID := req.Events[len(req.Events)-1].EventID
	recorded, err := s.store.RecordVerifyPage(ctx, deviceID, lastID, int64(len(req.Events)), req.EndOfHistory)
	if err != nil {
		return VerifyResponse{}, err
	}
	if !recorded {
		return VerifyResponse{}, fmt.Errorf(
			"verification page does not advance the recorded cursor: pages must be submitted in retained-history order with none skipped")
	}
	return VerifyResponse{Missing: out, CloudEventCount: 0}, nil
}

// Complete clears the device's recovery requirement.
//
// Phase 20 R3: the flag is cleared ONLY when the server's own coverage
// ledger proves an exhaustive walk. A client-claimed "verified count"
// is never trusted. The preconditions are:
//
//   - verify_complete is TRUE, which requires a page submitted as
//     end_of_history (an incomplete walk cannot set it);
//   - at least one page was recorded (never a zero-work shortcut);
//   - the last verification pass reported zero missing events.
//
// If any precondition fails the flag stays set and the device remains
// RECOVERY_RECONCILIATION_REQUIRED.
func (s *Service) Complete(ctx context.Context, deviceID string, lastMissing int) error {
	if lastMissing != 0 {
		return fmt.Errorf("recovery cannot complete while %d event(s) are still missing", lastMissing)
	}
	progress, ok, err := s.store.VerifyProgress(ctx, deviceID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no verification progress recorded for this device")
	}
	if progress.Pages == 0 {
		return fmt.Errorf("recovery cannot complete without a verified page")
	}
	if !progress.Complete {
		return fmt.Errorf(
			"recovery cannot complete: verification history has not been walked to the end (pages=%d events=%d). "+
				"Submit every retained-history page in order and mark the final page end_of_history",
			progress.Pages, progress.Events)
	}
	if progress.DeclaredTotal == 0 {
		return fmt.Errorf(
			"recovery cannot complete: no retained-history total was declared for this pass. " +
				"Open the pass with declared_total so exhaustiveness can be proven")
	}
	if progress.Events < progress.DeclaredTotal {
		return fmt.Errorf(
			"recovery cannot complete: verified %d of %d declared retained events. "+
				"Every retained-history page must be submitted; skipped pages leave the device unreconciled",
			progress.Events, progress.DeclaredTotal)
	}
	return s.store.ClearRecoveryState(ctx, deviceID, progress.Events)
}

// VerifyProgress exposes the server-side coverage ledger for operator
// evidence and tests.
func (s *Service) VerifyProgress(ctx context.Context, deviceID string) (VerifyProgress, bool, error) {
	return s.store.VerifyProgress(ctx, deviceID)
}

// BeginVerifyPass starts a fresh verification generation. An
// interrupted reconciliation resumes by beginning a new pass; prior
// progress is discarded so a partial walk can never be mistaken for a
// complete one.
func (s *Service) BeginVerifyPass(ctx context.Context, deviceID string, declaredTotal int64) error {
	if declaredTotal < 0 {
		return fmt.Errorf("declared total must not be negative")
	}
	// Phase 20 R5: refuse an implausibly small declaration. Every event
	// Cloud accepted from this device came from that device's retained
	// outbox, so its retained history cannot be smaller than what Cloud
	// already holds. This turns deliberate under-declaration into a
	// refused pass instead of a false completion.
	//
	// It is a LOWER BOUND, not a proof of honest disclosure: after a
	// restore Cloud may hold fewer events than the device retains, so a
	// device that under-declares to the bound can still omit history
	// Cloud has never seen. The device remains the authority for its own
	// retained history (see ADR-0054).
	known, err := s.store.CountDeviceSyncEvents(ctx, deviceID)
	if err != nil {
		return err
	}
	if declaredTotal < known {
		return fmt.Errorf(
			"refusing verification pass: declared %d retained events but Cloud already holds %d for this device. "+
				"A device cannot retain fewer events than it delivered; under-declaration is refused",
			declaredTotal, known)
	}
	return s.store.BeginVerifyPass(ctx, deviceID, declaredTotal)
}
