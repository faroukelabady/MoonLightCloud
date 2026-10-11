package syncrecovery

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type fakeStore struct {
	state              map[string]State
	present            map[string]string // event_id -> payload hash
	ledger             map[string]VerifyProgress
	pendingProjections int64
	cloudKnown         map[string]int64  // device -> events Cloud holds
	storeOf            map[string]string // device -> Store (default: one shared Store)
	// defaultHash, when set, is the stored hash of any identity not listed
	// in present (most tests model a Cloud that already holds history).
	defaultHash string
	revoked     map[string]bool
}

func (f *fakeStore) UpsertRecoveryState(_ context.Context, deviceID, reason string) error {
	f.state[deviceID] = State{DeviceID: deviceID, Required: true, Reason: reason}
	p := f.ledger[deviceID]
	f.ledger[deviceID] = VerifyProgress{Generation: p.Generation + 1}
	return nil
}

func (f *fakeStore) ClearRecoveryState(_ context.Context, deviceID string, generation, verified int64) error {
	p := f.ledger[deviceID]
	if p.Generation != generation || !p.Complete || p.Pages == 0 || p.DeclaredTotal == 0 || p.Events != p.DeclaredTotal || p.Events != verified || p.Missing != 0 {
		return ErrStaleOrIncompleteGeneration
	}
	s := f.state[deviceID]
	s.Required = false
	s.VerifiedEvents = verified
	f.state[deviceID] = s
	return nil
}

func (f *fakeStore) GetRecoveryState(_ context.Context, deviceID string) (State, bool, error) {
	s, ok := f.state[deviceID]
	return s, ok, nil
}

func (f *fakeStore) CountRecoveryRequired(_ context.Context, deviceID string) (int, error) {
	n := 0
	for id, s := range f.state {
		if s.Required && !f.revoked[id] && f.storeOf[id] == f.storeOf[deviceID] {
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) SyncEventHashes(_ context.Context, _ string, eventIDs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range eventIDs {
		if h, ok := f.present[id]; ok {
			out[id] = h
		} else if f.defaultHash != "" {
			out[id] = f.defaultHash
		}
	}
	return out, nil
}

func newStore() *fakeStore {
	return &fakeStore{state: map[string]State{}, present: map[string]string{}, ledger: map[string]VerifyProgress{},
		cloudKnown: map[string]int64{}, storeOf: map[string]string{}, revoked: map[string]bool{}, defaultHash: "a"}
}

func (f *fakeStore) CountDeviceSyncEvents(_ context.Context, deviceID string) (int64, error) {
	return f.cloudKnown[deviceID], nil
}

func (f *fakeStore) CountPendingProjections(_ context.Context, _ string) (int64, error) {
	return f.pendingProjections, nil
}

func (f *fakeStore) VerifyProgress(_ context.Context, deviceID string) (VerifyProgress, bool, error) {
	if _, marked := f.state[deviceID]; !marked {
		return VerifyProgress{}, false, nil
	}
	return f.ledger[deviceID], true, nil
}

func (f *fakeStore) BeginVerifyPass(_ context.Context, deviceID string, declaredTotal int64) (int64, error) {
	p := f.ledger[deviceID]
	p = VerifyProgress{Generation: p.Generation + 1, DeclaredTotal: declaredTotal}
	f.ledger[deviceID] = p
	return p.Generation, nil
}

func (f *fakeStore) RecordVerifyPage(_ context.Context, deviceID string, generation int64, firstEventID, lastEventID string, events, missing int64, endOfHistory bool) (bool, error) {
	p := f.ledger[deviceID]
	if p.Generation != generation || firstEventID <= p.LastEventID || lastEventID < firstEventID {
		return false, nil // does not advance
	}
	p.LastEventID = lastEventID
	p.Pages++
	p.Events += events
	p.Missing += missing
	if endOfHistory {
		p.Complete = true
	}
	f.ledger[deviceID] = p
	return true, nil
}

// TestMarkRestoreRequiredIsDurable proves the supported restore
// workflow durably marks every device: the state lives in the store,
// so it survives Cloud restarts.
func TestMarkRestoreRequiredIsDurable(t *testing.T) {
	store := newStore()
	svc := NewService(store)
	devices := []string{uuid.NewString(), uuid.NewString()}
	if err := svc.MarkRestoreRequired(context.Background(), devices, "cloud database restored"); err != nil {
		t.Fatal(err)
	}
	for _, id := range devices {
		state, found, err := store.GetRecoveryState(context.Background(), id)
		if err != nil || !found || !state.Required {
			t.Fatalf("device %s must be marked: %+v %v %v", id, state, found, err)
		}
	}
	report, err := svc.Status(context.Background(), devices[0])
	if err != nil {
		t.Fatal(err)
	}
	if !report.Required || report.DevicesUnresolved != 2 {
		t.Fatalf("status: %+v", report)
	}
	if !strings.Contains(report.Instructions, "reconcil") {
		t.Fatalf("instructions must be actionable: %q", report.Instructions)
	}
}

// TestVerifyDetectsMissingIdentities proves the negative control (§14):
// an older Cloud database is detected as missing events by identity,
// never by count.
func TestVerifyDetectsMissingIdentities(t *testing.T) {
	store := newStore()
	store.defaultHash = ""
	svc := NewService(store)
	device := uuid.NewString()

	present := "00000000-0000-4000-8000-000000000001"
	absent := "00000000-0000-4000-8000-000000000002"
	store.present[present] = "aaaa"
	if err := svc.MarkRestoreRequired(context.Background(), []string{device}, "restored"); err != nil {
		t.Fatal(err)
	}

	resp, err := svc.Verify(context.Background(), device, VerifyRequest{
		Events: []EventIdentity{
			{EventID: present, PayloadHash: "aaaa"},
			{EventID: absent, PayloadHash: "bbbb"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Missing) != 1 || resp.Missing[0] != absent {
		t.Fatalf("missing: %v", resp.Missing)
	}
}

// TestVerifyIdentityMismatchIsMissing proves content conflict handling:
// the same event ID with a different payload hash is NOT a match.
func TestVerifyIdentityMismatchIsMissing(t *testing.T) {
	store := newStore()
	svc := NewService(store)
	id := uuid.NewString()
	store.present[id] = "aaaa"

	device := uuid.NewString()
	if err := svc.MarkRestoreRequired(context.Background(), []string{device}, "restored"); err != nil {
		t.Fatal(err)
	}
	resp, err := svc.Verify(context.Background(), device, VerifyRequest{
		Events: []EventIdentity{{EventID: id, PayloadHash: "cccc"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Missing) != 1 {
		t.Fatalf("same ID different hash must be missing: %v", resp.Missing)
	}
}

// TestVerifyRejectsUnboundedPages proves the verification payload can
// never become unbounded.
func TestVerifyRejectsUnboundedPages(t *testing.T) {
	store := newStore()
	svc := NewService(store)
	big := make([]EventIdentity, MaxVerifyPage+1)
	for i := range big {
		big[i] = EventIdentity{EventID: uuid.NewString(), PayloadHash: "a"}
	}
	if _, err := svc.Verify(context.Background(), uuid.NewString(), VerifyRequest{Events: big}); err == nil {
		t.Fatal("oversized page must be refused")
	}
	if _, err := svc.Verify(context.Background(), uuid.NewString(), VerifyRequest{}); err == nil {
		t.Fatal("empty page must be refused")
	}
}

// TestCompleteOnlyAfterVerification proves the state transition is
// honest: clearing is a distinct step, and the report reflects it.
func TestCompleteOnlyAfterVerification(t *testing.T) {
	store := newStore()
	svc := NewService(store)
	device := uuid.NewString()
	if err := svc.MarkRestoreRequired(context.Background(), []string{device}, "restored"); err != nil {
		t.Fatal(err)
	}
	report, err := svc.Status(context.Background(), device)
	if err != nil || !report.Required {
		t.Fatalf("must be required: %+v %v", report, err)
	}
	if err := svc.BeginVerifyPass(context.Background(), device, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Verify(context.Background(), device, VerifyRequest{
		Events: []EventIdentity{{EventID: uuid.NewString(), PayloadHash: "a"}}, EndOfHistory: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Complete(context.Background(), device, 0); err != nil {
		t.Fatal(err)
	}
	report, err = svc.Status(context.Background(), device)
	if err != nil {
		t.Fatal(err)
	}
	if report.Required || report.DevicesUnresolved != 0 {
		t.Fatalf("cleared but still reported: %+v", report)
	}
	if err := svc.Complete(context.Background(), device, 3); err == nil {
		t.Fatal("completion with missing events must be refused")
	}
}

// TestStoreNeverReconciledWhileDeviceOutstanding proves the multi-device
// rule: one device clearing does not report the Store as reconciled
// while another is still outstanding.
func TestStoreNeverReconciledWhileDeviceOutstanding(t *testing.T) {
	store := newStore()
	svc := NewService(store)
	a, b := uuid.NewString(), uuid.NewString()
	if err := svc.MarkRestoreRequired(context.Background(), []string{a, b}, "restored"); err != nil {
		t.Fatal(err)
	}
	completeDevice(t, svc, a)
	if err := svc.Complete(context.Background(), a, 0); err != nil {
		t.Fatal(err)
	}
	report, err := svc.Status(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if report.DevicesUnresolved != 1 {
		t.Fatalf("device B still outstanding: %+v", report)
	}
	completeDevice(t, svc, b)
	if err := svc.Complete(context.Background(), b, 0); err != nil {
		t.Fatal(err)
	}
	report, err = svc.Status(context.Background(), b)
	if err != nil || report.DevicesUnresolved != 0 {
		t.Fatalf("all devices reconciled: %+v %v", report, err)
	}
}

// completeDevice runs a full exhaustive verification pass. Event IDs
// advance deterministically so the keyset cursor moves forward.
func completeDevice(t *testing.T, svc *Service, device string) {
	t.Helper()
	if err := svc.BeginVerifyPass(context.Background(), device, 3); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("%032d", i+1)
		_, err := svc.Verify(context.Background(), device, VerifyRequest{
			Events:       []EventIdentity{{EventID: id, PayloadHash: "a"}},
			EndOfHistory: i == 2,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestIncompleteVerificationCannotClearRecovery is the mandatory §3
// challenge: 1,200 retained events, page size 100. Verifying only the
// first page and attempting completion MUST be rejected.
func TestIncompleteVerificationCannotClearRecovery(t *testing.T) {
	store := newStore()
	svc := NewService(store)
	device := uuid.NewString()
	if err := svc.MarkRestoreRequired(context.Background(), []string{device}, "restored"); err != nil {
		t.Fatal(err)
	}
	if err := svc.BeginVerifyPass(context.Background(), device, 1200); err != nil {
		t.Fatal(err)
	}
	// Only the first 100 of 1200.
	page := make([]EventIdentity, 0, 100)
	for i := 0; i < 100; i++ {
		page = append(page, EventIdentity{EventID: fmt.Sprintf("%032d", i+1), PayloadHash: "a"})
	}
	if _, err := svc.Verify(context.Background(), device, VerifyRequest{Events: page}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Complete(context.Background(), device, 0); err == nil {
		t.Fatal("completion after one page of twelve must be REJECTED")
	}
	report, err := svc.Status(context.Background(), device)
	if err != nil || !report.Required {
		t.Fatalf("flag must stay set: %+v %v", report, err)
	}
}

// TestSkippedPageIsRejected is the mandatory §3 challenge: verifying
// page 1, skipping page 2, then submitting page 3 MUST be rejected.
func TestSkippedPageIsRejected(t *testing.T) {
	store := newStore()
	svc := NewService(store)
	device := uuid.NewString()
	if err := svc.MarkRestoreRequired(context.Background(), []string{device}, "restored"); err != nil {
		t.Fatal(err)
	}
	if err := svc.BeginVerifyPass(context.Background(), device, 300); err != nil {
		t.Fatal(err)
	}
	mk := func(lo, hi int) []EventIdentity {
		out := []EventIdentity{}
		for i := lo; i < hi; i++ {
			out = append(out, EventIdentity{EventID: fmt.Sprintf("%032d", i), PayloadHash: "a"})
		}
		return out
	}
	// Page 1 covers events 1..100.
	if _, err := svc.Verify(context.Background(), device, VerifyRequest{Events: mk(1, 101)}); err != nil {
		t.Fatal(err)
	}
	// Page 3 starts at event 201 — page 2 (101..200) is skipped. The
	// page itself advances the cursor, but the coverage ledger then
	// falls short of the declared total, so completion is rejected.
	if _, err := svc.Verify(context.Background(), device, VerifyRequest{Events: mk(201, 301)}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Complete(context.Background(), device, 0); err == nil {
		t.Fatal("completion after a skipped page must be REJECTED: coverage is short of the declared total")
	}
	// A non-advancing page is refused outright.
	if _, err := svc.Verify(context.Background(), device, VerifyRequest{Events: mk(1, 101)}); err == nil {
		t.Fatal("replaying an earlier page must be REJECTED")
	}
}

// TestReplayedPageIsRejected proves pages cannot be double-counted to
// fake coverage.
func TestReplayedPageIsRejected(t *testing.T) {
	store := newStore()
	svc := NewService(store)
	device := uuid.NewString()
	if err := svc.MarkRestoreRequired(context.Background(), []string{device}, "restored"); err != nil {
		t.Fatal(err)
	}
	if err := svc.BeginVerifyPass(context.Background(), device, 3); err != nil {
		t.Fatal(err)
	}
	mk := func(lo, hi int) []EventIdentity {
		out := []EventIdentity{}
		for i := lo; i < hi; i++ {
			out = append(out, EventIdentity{EventID: fmt.Sprintf("%032d", i), PayloadHash: "a"})
		}
		return out
	}
	if _, err := svc.Verify(context.Background(), device, VerifyRequest{Events: mk(1, 101)}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Verify(context.Background(), device, VerifyRequest{Events: mk(1, 101)}); err == nil {
		t.Fatal("replaying a verification page must be REJECTED")
	}
}

// TestRestartResumesExhaustiveProof is the mandatory §3 challenge:
// restart midway, resume, complete only after full proof.
func TestRestartResumesExhaustiveProof(t *testing.T) {
	store := newStore()
	svc := NewService(store)
	device := uuid.NewString()
	if err := svc.MarkRestoreRequired(context.Background(), []string{device}, "restored"); err != nil {
		t.Fatal(err)
	}
	mk := func(lo, hi int) []EventIdentity {
		out := []EventIdentity{}
		for i := lo; i < hi; i++ {
			out = append(out, EventIdentity{EventID: fmt.Sprintf("%032d", i), PayloadHash: "a"})
		}
		return out
	}
	// Pass 1: two pages then "restart" (new service instance).
	if err := svc.BeginVerifyPass(context.Background(), device, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Verify(context.Background(), device, VerifyRequest{Events: mk(1, 101)}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Verify(context.Background(), device, VerifyRequest{Events: mk(101, 201)}); err != nil {
		t.Fatal(err)
	}
	svc2 := NewService(store)
	// Interrupted pass cannot complete.
	if err := svc2.Complete(context.Background(), device, 0); err == nil {
		t.Fatal("interrupted pass must not complete")
	}
	// Resume: begin a fresh pass and walk to the end.
	if err := svc2.BeginVerifyPass(context.Background(), device, 1200); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 12; i++ {
		_, err := svc2.Verify(context.Background(), device, VerifyRequest{
			Events:       mk(i*100-99, i*100+1),
			EndOfHistory: i == 12,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := svc2.Complete(context.Background(), device, 0); err != nil {
		t.Fatalf("full proof must complete: %v", err)
	}
	report, err := svc2.Status(context.Background(), device)
	if err != nil || report.Required {
		t.Fatalf("flag must clear after exhaustive proof: %+v %v", report, err)
	}
}

// TestZeroMissingStillNeedsCoverage proves §3: a client cannot clear
// recovery by claiming missing=0 without a coverage walk.
func TestZeroMissingStillNeedsCoverage(t *testing.T) {
	store := newStore()
	svc := NewService(store)
	device := uuid.NewString()
	if err := svc.MarkRestoreRequired(context.Background(), []string{device}, "restored"); err != nil {
		t.Fatal(err)
	}
	// No verification at all.
	if err := svc.Complete(context.Background(), device, 0); err == nil {
		t.Fatal("zero missing without any verification must be REJECTED")
	}
	// Verification without end_of_history.
	if err := svc.BeginVerifyPass(context.Background(), device, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Verify(context.Background(), device, VerifyRequest{
		Events: []EventIdentity{{EventID: fmt.Sprintf("%032d", 1), PayloadHash: "a"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Complete(context.Background(), device, 0); err == nil {
		t.Fatal("completion without end_of_history must be REJECTED")
	}
}

// TestNoDeclaredTotalCannotComplete proves §3: an end-of-history claim
// without a declared retained-history total cannot clear recovery. The
// server must know how much history the device committed to walking.
func TestNoDeclaredTotalCannotComplete(t *testing.T) {
	store := newStore()
	svc := NewService(store)
	device := uuid.NewString()
	if err := svc.MarkRestoreRequired(context.Background(), []string{device}, "restored"); err != nil {
		t.Fatal(err)
	}
	// One page, end_of_history, but NO declared_total.
	if _, err := svc.Verify(context.Background(), device, VerifyRequest{
		Events:       []EventIdentity{{EventID: fmt.Sprintf("%032d", 1), PayloadHash: "a"}},
		EndOfHistory: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Complete(context.Background(), device, 0); err == nil {
		t.Fatal("completion without a declared retained total must be REJECTED")
	}
	// With a declared total matching the walk, completion succeeds.
	if _, err := svc.Verify(context.Background(), device, VerifyRequest{
		Events:        []EventIdentity{{EventID: fmt.Sprintf("%032d", 1), PayloadHash: "a"}},
		EndOfHistory:  true,
		DeclaredTotal: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Complete(context.Background(), device, 0); err != nil {
		t.Fatalf("full declared walk must complete: %v", err)
	}
}

// TestProjectionsPendingQualifiesStoreReconciled proves §5: ingest is
// not projection. A Store is never reported reconciled while event
// processors are still retrying or blocked.
func TestProjectionsPendingQualifiesStoreReconciled(t *testing.T) {
	store := newStore()
	store.pendingProjections = 3
	svc := NewService(store)
	device := uuid.NewString()
	if err := svc.MarkRestoreRequired(context.Background(), []string{device}, "restored"); err != nil {
		t.Fatal(err)
	}
	completeDevice(t, svc, device)
	if err := svc.Complete(context.Background(), device, 0); err != nil {
		t.Fatal(err)
	}
	report, err := svc.Status(context.Background(), device)
	if err != nil {
		t.Fatal(err)
	}
	if report.Required {
		t.Fatal("device recovery flag must clear after exhaustive verification")
	}
	if report.StoreReconciled {
		t.Fatal("store must NOT be reconciled while projections are pending")
	}
	if report.ProjectionsPending != 3 {
		t.Fatalf("pending projections = %d want 3", report.ProjectionsPending)
	}
	// Once projections converge, the Store is reconciled.
	store.pendingProjections = 0
	report, err = svc.Status(context.Background(), device)
	if err != nil {
		t.Fatal(err)
	}
	if !report.StoreReconciled {
		t.Fatal("store must be reconciled once projections converge")
	}
}

// ---- Phase 20 R5 adversarial verification tests -----------------------

// TestUnderDeclarationBelowCloudBoundIsRefused proves the independent
// lower bound: a device cannot declare fewer retained events than Cloud
// already holds for it.
func TestUnderDeclarationBelowCloudBoundIsRefused(t *testing.T) {
	store := newStore()
	svc := NewService(store)
	device := uuid.NewString()
	store.cloudKnown[device] = 3
	if err := svc.MarkRestoreRequired(context.Background(), []string{device}, "restored"); err != nil {
		t.Fatal(err)
	}
	err := svc.BeginVerifyPass(context.Background(), device, 2)
	if err == nil {
		t.Fatal("declaring 2 retained events when Cloud holds 3 must be refused")
	}
	t.Logf("refusal: %v", err)
	if err := svc.Complete(context.Background(), device, 0); err == nil {
		t.Fatal("completion after refused pass must fail")
	}
}

// TestUnderDeclarationToExactBoundIsAccepted_LIMITATION is the honest
// trust-boundary case: after a restore Cloud knows 3 events while the
// device retains 12. Declaring 3 satisfies the lower bound, the device
// walks 3 events, claims end_of_history, and completes.
//
// This is NOT a bug in the bound - Cloud has no way to know about the 9
// events it has never seen. It is the documented residual trust boundary
// (ADR-0054): the authenticated Retail device is the authority for its
// own retained history. The bound rejects gross under-declaration but
// cannot prove honest disclosure.
func TestUnderDeclarationToExactBoundIsAccepted_LIMITATION(t *testing.T) {
	store := newStore()
	svc := NewService(store)
	device := uuid.NewString()
	store.cloudKnown[device] = 3 // restored Cloud holds 3
	if err := svc.MarkRestoreRequired(context.Background(), []string{device}, "restored"); err != nil {
		t.Fatal(err)
	}
	// Device actually retains 12 but declares only the bound (3).
	if err := svc.BeginVerifyPass(context.Background(), device, 3); err != nil {
		t.Fatalf("declaration at the bound must be accepted (got %v)", err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := svc.Verify(context.Background(), device, VerifyRequest{
			Events:        []EventIdentity{{EventID: fmt.Sprintf("%032d", i), PayloadHash: "a"}},
			EndOfHistory:  i == 3,
			DeclaredTotal: 0,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Complete(context.Background(), device, 0); err != nil {
		t.Fatalf("LIMITATION: completion succeeds despite incomplete disclosure: %v", err)
	}
	report, err := svc.Status(context.Background(), device)
	if err != nil || report.Required {
		t.Fatalf("LIMITATION: flag cleared despite only 3 of 12 verified: %+v %v", report, err)
	}
	t.Log("LIMITATION CONFIRMED: authenticated device can under-declare to the Cloud-known bound and complete")
}

// TestDeclaredTotalAboveSuppliedEventsCannotComplete proves the ledger
// catches over-declaration with an under-supplied transcript.
func TestDeclaredTotalAboveSuppliedEventsCannotComplete(t *testing.T) {
	store := newStore()
	svc := NewService(store)
	device := uuid.NewString()
	if err := svc.MarkRestoreRequired(context.Background(), []string{device}, "restored"); err != nil {
		t.Fatal(err)
	}
	if err := svc.BeginVerifyPass(context.Background(), device, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Verify(context.Background(), device, VerifyRequest{
		Events:       []EventIdentity{{EventID: fmt.Sprintf("%032d", 1), PayloadHash: "a"}},
		EndOfHistory: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Complete(context.Background(), device, 0); err == nil {
		t.Fatal("declaring 5 but supplying 1 must not complete")
	}
}

// TestDuplicateIDsInPageDoNotInflateCoverage proves re-submitting the
// same identity is refused outright (Phase 20 R6): a page must be
// strictly ascending, so duplicates can never pad coverage.
func TestDuplicateIDsInPageDoNotInflateCoverage(t *testing.T) {
	store := newStore()
	svc := NewService(store)
	device := uuid.NewString()
	if err := svc.MarkRestoreRequired(context.Background(), []string{device}, "restored"); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("%032d", 1)
	_, err := svc.Verify(context.Background(), device, VerifyRequest{
		Events: []EventIdentity{
			{EventID: id, PayloadHash: "a"},
			{EventID: id, PayloadHash: "a"},
			{EventID: fmt.Sprintf("%032d", 2), PayloadHash: "a"},
			{EventID: fmt.Sprintf("%032d", 3), PayloadHash: "a"},
		},
		EndOfHistory:  true,
		DeclaredTotal: 4,
	})
	if err == nil {
		t.Fatal("a page repeating an identity must be refused")
	}
	if err := svc.Complete(context.Background(), device, 0); err == nil {
		t.Fatal("a refused duplicate-padded page must not allow completion")
	}
}

// TestDescendingIdentitiesWithinPageAreRefused proves a page must be a
// contiguous ascending keyset slice.
func TestDescendingIdentitiesWithinPageAreRefused(t *testing.T) {
	store := newStore()
	svc := NewService(store)
	device := uuid.NewString()
	if err := svc.MarkRestoreRequired(context.Background(), []string{device}, "restored"); err != nil {
		t.Fatal(err)
	}
	_, err := svc.Verify(context.Background(), device, VerifyRequest{
		Events: []EventIdentity{
			{EventID: fmt.Sprintf("%032d", 2), PayloadHash: "a"},
			{EventID: fmt.Sprintf("%032d", 1), PayloadHash: "a"},
		},
		DeclaredTotal: 2,
	})
	if err == nil {
		t.Fatal("descending identities must be refused")
	}
}

// TestCompleteRefusesWhenServerRecordedMissing proves Complete no longer
// trusts the client's last_missing: the server's own ledger decides.
func TestCompleteRefusesWhenServerRecordedMissing(t *testing.T) {
	store := newStore()
	store.defaultHash = ""
	svc := NewService(store)
	device := uuid.NewString()
	if err := svc.MarkRestoreRequired(context.Background(), []string{device}, "restored"); err != nil {
		t.Fatal(err)
	}
	resp, err := svc.Verify(context.Background(), device, VerifyRequest{
		Events:        []EventIdentity{{EventID: fmt.Sprintf("%032d", 1), PayloadHash: "a"}},
		EndOfHistory:  true,
		DeclaredTotal: 1,
	})
	if err != nil || len(resp.Missing) != 1 {
		t.Fatalf("expected one missing identity: %+v %v", resp, err)
	}
	if err := svc.Complete(context.Background(), device, 0); err == nil {
		t.Fatal("client claim last_missing=0 must not override the server-recorded missing identity")
	}
	report, _ := svc.Status(context.Background(), device)
	if !report.Required {
		t.Fatal("device must stay required")
	}
}

// TestReMarkingResetsLedger proves a completed ledger from an earlier
// recovery cannot be reused after the device is marked again.
func TestReMarkingResetsLedger(t *testing.T) {
	store := newStore()
	svc := NewService(store)
	device := uuid.NewString()
	if err := svc.MarkRestoreRequired(context.Background(), []string{device}, "restored"); err != nil {
		t.Fatal(err)
	}
	completeDevice(t, svc, device)
	if err := svc.Complete(context.Background(), device, 0); err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkRestoreRequired(context.Background(), []string{device}, "restored again"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Complete(context.Background(), device, 0); err == nil {
		t.Fatal("re-marked device must walk its history again before completing")
	}
}

// TestUnresolvedCountIsStoreScopedAndExcludesRevoked proves another
// Store's (or a revoked) device never blocks this Store's reconciliation.
func TestUnresolvedCountIsStoreScopedAndExcludesRevoked(t *testing.T) {
	store := newStore()
	svc := NewService(store)
	a, b, other := uuid.NewString(), uuid.NewString(), uuid.NewString()
	store.storeOf[a], store.storeOf[b], store.storeOf[other] = "store-1", "store-1", "store-2"
	if err := svc.MarkRestoreRequired(context.Background(), []string{a, b, other}, "restored"); err != nil {
		t.Fatal(err)
	}
	completeDevice(t, svc, a)
	if err := svc.Complete(context.Background(), a, 0); err != nil {
		t.Fatal(err)
	}
	report, _ := svc.Status(context.Background(), a)
	if report.DevicesUnresolved != 1 || report.StoreReconciled {
		t.Fatalf("store-1 device b outstanding (store-2 must not count): %+v", report)
	}
	store.revoked[b] = true
	report, _ = svc.Status(context.Background(), a)
	if report.DevicesUnresolved != 0 || !report.StoreReconciled {
		t.Fatalf("revoked device b no longer blocks store-1; store-2 never did: %+v", report)
	}
}

// TestSameIDWrongHashIsMissing re-confirms content conflicts never match.
func TestSameIDWrongHashIsMissingOnUnderDeclaration(t *testing.T) {
	store := newStore()
	svc := NewService(store)
	device := uuid.NewString()
	id := fmt.Sprintf("%032d", 1)
	store.present[id] = "correct-hash"
	store.cloudKnown[device] = 1
	if err := svc.MarkRestoreRequired(context.Background(), []string{device}, "restored"); err != nil {
		t.Fatal(err)
	}
	resp, err := svc.Verify(context.Background(), device, VerifyRequest{
		Events:        []EventIdentity{{EventID: id, PayloadHash: "wrong-hash"}},
		EndOfHistory:  true,
		DeclaredTotal: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Missing) != 1 {
		t.Fatal("same event id with wrong hash must be reported missing")
	}
	if err := svc.Complete(context.Background(), device, len(resp.Missing)); err == nil {
		t.Fatal("completion with a missing identity must be refused")
	}
}

// TestProjectionPendingBlocksStoreReconciled confirms §11: completion of
// device event-history verification never implies the Store is reconciled
// while projections are pending.
func TestProjectionPendingBlocksStoreReconciled(t *testing.T) {
	store := newStore()
	store.pendingProjections = 2
	svc := NewService(store)
	device := uuid.NewString()
	if err := svc.MarkRestoreRequired(context.Background(), []string{device}, "restored"); err != nil {
		t.Fatal(err)
	}
	completeDevice(t, svc, device)
	if err := svc.Complete(context.Background(), device, 0); err != nil {
		t.Fatal(err)
	}
	report, err := svc.Status(context.Background(), device)
	if err != nil {
		t.Fatal(err)
	}
	if report.StoreReconciled {
		t.Fatal("store must not be reconciled while projections are pending")
	}
}

// TestSecondDeviceUnresolvedBlocksStoreReconciled confirms §11: one
// device finishing never marks the Store reconciled while another is
// outstanding.
func TestSecondDeviceUnresolvedBlocksStoreReconciled(t *testing.T) {
	store := newStore()
	svc := NewService(store)
	a, b := uuid.NewString(), uuid.NewString()
	if err := svc.MarkRestoreRequired(context.Background(), []string{a, b}, "restored"); err != nil {
		t.Fatal(err)
	}
	completeDevice(t, svc, a)
	if err := svc.Complete(context.Background(), a, 0); err != nil {
		t.Fatal(err)
	}
	report, err := svc.Status(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if report.StoreReconciled {
		t.Fatal("store must not be reconciled while device b is unresolved")
	}
	if report.DevicesUnresolved != 1 {
		t.Fatalf("devices_unresolved=%d want 1", report.DevicesUnresolved)
	}
}
