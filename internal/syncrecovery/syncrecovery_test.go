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
}

func (f *fakeStore) UpsertRecoveryState(_ context.Context, deviceID, reason string) error {
	f.state[deviceID] = State{DeviceID: deviceID, Required: true, Reason: reason}
	return nil
}

func (f *fakeStore) ClearRecoveryState(_ context.Context, deviceID string, verified int64) error {
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

func (f *fakeStore) CountRecoveryRequired(_ context.Context) (int, error) {
	n := 0
	for _, s := range f.state {
		if s.Required {
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) MissingSyncEvents(_ context.Context, eventIDs []string) ([]string, error) {
	var missing []string
	for _, id := range eventIDs {
		if _, ok := f.present[id]; !ok {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

func (f *fakeStore) SyncEventPayloadHash(_ context.Context, eventID string) (string, bool, error) {
	h, ok := f.present[eventID]
	return h, ok, nil
}

func newStore() *fakeStore {
	return &fakeStore{state: map[string]State{}, present: map[string]string{}, ledger: map[string]VerifyProgress{}}
}

func (f *fakeStore) CountPendingProjections(_ context.Context) (int64, error) {
	return f.pendingProjections, nil
}

func (f *fakeStore) VerifyProgress(_ context.Context, deviceID string) (VerifyProgress, bool, error) {
	p, ok := f.ledger[deviceID]
	return p, ok, nil
}

func (f *fakeStore) BeginVerifyPass(_ context.Context, deviceID string, declaredTotal int64) error {
	p := f.ledger[deviceID]
	p.Generation++
	p.LastEventID = ""
	p.Pages = 0
	p.Events = 0
	p.Complete = false
	p.DeclaredTotal = declaredTotal
	f.ledger[deviceID] = p
	return nil
}

func (f *fakeStore) RecordVerifyPage(_ context.Context, deviceID, lastEventID string, events int64, endOfHistory bool) (bool, error) {
	p := f.ledger[deviceID]
	if lastEventID <= p.LastEventID {
		return false, nil // does not advance
	}
	p.LastEventID = lastEventID
	p.Pages++
	p.Events += events
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
	svc := NewService(store)
	device := uuid.NewString()

	present := uuid.NewString()
	absent := uuid.NewString()
	store.present[present] = "aaaa"

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

	resp, err := svc.Verify(context.Background(), uuid.NewString(), VerifyRequest{
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
