package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/syncrecovery"
)

// Current clients open the server generation before capturing the local
// outbox snapshot. Events accepted afterward are outside this finite pass and
// remain governed by ordinary sync and the separate projection-status gate.
func TestR12BoundaryHandshakePrecedesRetailSnapshot(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryHTTPFixture(t, nil)
	deviceID, token := f.newToken("r12-boundary-handshake")
	f.bind(t, deviceID, f.storeA)
	initialID := "10000000-0000-4000-8000-000000000001"
	f.insertEvent(t, initialID, deviceID, &f.storeA, "aa")
	if err := f.service.MarkRestoreRequired(ctx, []string{deviceID}, "restore"); err != nil {
		t.Fatal(err)
	}

	code, body := f.request(t, token, http.MethodPost, "/api/v1/device-control/recovery/verify/begin", "")
	if code != http.StatusOK {
		t.Fatalf("begin verification HTTP %d: %s", code, body)
	}
	var started struct {
		Generation int64 `json:"generation"`
	}
	if err := json.Unmarshal(body, &started); err != nil || started.Generation <= 0 {
		t.Fatalf("invalid generation response %s: %v", body, err)
	}

	lateID := "20000000-0000-4000-8000-000000000002"
	if _, err := f.pool.Exec(ctx, `INSERT INTO sync_events
		(event_id,device_id,event_type,occurred_at,payload,payload_hash,store_id)
		VALUES($1,$2,'sale.finalized.v3',now(),'{}',decode('bb','hex'),$3)`, lateID, deviceID, f.storeA); err != nil {
		t.Fatal(err)
	}
	page, _ := json.Marshal(map[string]any{
		"generation": started.Generation, "declared_total": 1, "end_of_history": true,
		"events": []syncrecovery.EventIdentity{{EventID: initialID, PayloadHash: "aa"}},
	})
	code, body = f.request(t, token, http.MethodPost, "/api/v1/device-control/recovery/verify", string(page))
	if code != http.StatusOK {
		t.Fatalf("snapshot page HTTP %d: %s", code, body)
	}
	progress, found, err := f.devices.VerifyProgress(ctx, deviceID)
	if err != nil || !found || progress.Generation != started.Generation || progress.DeclaredTotal != 1 || progress.Events != 1 || progress.Missing != 0 || !progress.Complete {
		t.Fatalf("unexpected finite-boundary ledger: %+v found=%v err=%v", progress, found, err)
	}
	code, body = f.request(t, token, http.MethodPost, "/api/v1/device-control/recovery/complete", `{"last_missing":0}`)
	if code != http.StatusOK {
		t.Fatalf("boundary completion HTTP %d: %s", code, body)
	}
	report, err := f.service.Status(ctx, deviceID)
	if err != nil || report.Required || report.ProjectionsPending != 1 || report.StoreReconciled {
		t.Fatalf("event verification must not imply projection convergence: %+v err=%v", report, err)
	}
}

func TestR12StaleGenerationCannotRecordSnapshotPage(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryHTTPFixture(t, nil)
	deviceID, token := f.newToken("r12-stale-page-generation")
	f.bind(t, deviceID, f.storeA)
	if err := f.service.MarkRestoreRequired(ctx, []string{deviceID}, "restore"); err != nil {
		t.Fatal(err)
	}
	start := func() int64 {
		t.Helper()
		code, body := f.request(t, token, http.MethodPost, "/api/v1/device-control/recovery/verify/begin", "")
		if code != http.StatusOK {
			t.Fatalf("begin HTTP %d: %s", code, body)
		}
		var result struct {
			Generation int64 `json:"generation"`
		}
		if err := json.Unmarshal(body, &result); err != nil || result.Generation <= 0 {
			t.Fatalf("invalid begin response %s: %v", body, err)
		}
		return result.Generation
	}
	oldGeneration := start()
	currentGeneration := start()
	eventID := "30000000-0000-4000-8000-000000000003"
	f.insertEvent(t, eventID, deviceID, &f.storeA, "cc")
	page, _ := json.Marshal(map[string]any{
		"generation": oldGeneration, "declared_total": 1, "end_of_history": true,
		"events": []syncrecovery.EventIdentity{{EventID: eventID, PayloadHash: "cc"}},
	})
	code, body := f.request(t, token, http.MethodPost, "/api/v1/device-control/recovery/verify", string(page))
	if code != http.StatusBadRequest || !strings.Contains(string(body), "stale") {
		t.Fatalf("stale page should be rejected, HTTP %d: %s", code, body)
	}
	progress, found, err := f.devices.VerifyProgress(ctx, deviceID)
	if err != nil || !found || progress.Generation != currentGeneration || progress.Pages != 0 || progress.Events != 0 {
		t.Fatalf("stale page changed current ledger: %+v found=%v err=%v", progress, found, err)
	}
}

// This preserves the legacy page-declared protocol: excess coverage remains
// valid. Current Retail clients use the explicit generation/snapshot
// handshake tested below and do not extend a live walk.
func TestR11LiveHistoryGrowthMayExceedDeclaredCount(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryHTTPFixture(t, nil)
	deviceID, token := f.newToken("r11-live-history-growth")
	f.bind(t, deviceID, f.storeA)
	if err := f.service.MarkRestoreRequired(ctx, []string{deviceID}, "restore"); err != nil {
		t.Fatal(err)
	}
	events := []syncrecovery.EventIdentity{
		{EventID: "10000000-0000-4000-8000-000000000001", PayloadHash: "aa"},
		{EventID: "20000000-0000-4000-8000-000000000002", PayloadHash: "bb"},
		{EventID: "30000000-0000-4000-8000-000000000003", PayloadHash: "cc"},
	}
	f.insertEvent(t, events[0].EventID, deviceID, &f.storeA, events[0].PayloadHash)
	code, body := f.verify(t, token, events[:1], 1, false, "")
	if code != http.StatusOK {
		t.Fatalf("initial page should open lower-bound pass: HTTP %d %s", code, body)
	}
	// The initial retained count was one. New Sales/inventory events arrive
	// during the pass and are visible before Retail's terminal page.
	for _, event := range events[1:] {
		f.insertEvent(t, event.EventID, deviceID, &f.storeA, event.PayloadHash)
	}
	code, body = f.verify(t, token, events[1:], 0, true, "")
	if code != http.StatusOK {
		t.Fatalf("terminal live walk with excess coverage should be accepted: HTTP %d %s", code, body)
	}
	progress, found, err := f.devices.VerifyProgress(ctx, deviceID)
	if err != nil || !found || progress.DeclaredTotal != 1 || progress.Events != 3 || progress.Missing != 0 || !progress.Complete {
		t.Fatalf("expected complete zero-missing 3-of-1 lower-bound ledger, got %+v found=%v err=%v", progress, found, err)
	}
	code, body = f.request(t, token, http.MethodPost, "/api/v1/device-control/recovery/complete", `{"last_missing":0}`)
	if code != http.StatusOK {
		t.Fatalf("generation-fenced completion rejected valid excess coverage: HTTP %d %s", code, body)
	}
	state, found, err := f.devices.GetRecoveryState(ctx, deviceID)
	if err != nil || !found || state.Required || state.VerifiedEvents != 3 {
		t.Fatalf("completion did not durably clear recovery at verified count: state=%+v found=%v err=%v", state, found, err)
	}
	report, err := f.service.Status(ctx, deviceID)
	if err != nil || report.Required || !report.StoreReconciled {
		t.Fatalf("status should report recovered after zero-missing complete walk: %+v err=%v", report, err)
	}
}

// Legacy page-declared clients may have Cloud accept events after their local
// count capture. The first-page initializer still raises the effective floor
// atomically; current Retail starts the generation before taking its snapshot.
func TestR11BeginPassCapturesCloudEventsAcceptedBeforeFirstPage(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryHTTPFixture(t, nil)
	deviceID, token := f.newToken("r11-begin-pass-floor-race")
	f.bind(t, deviceID, f.storeA)
	if err := f.service.MarkRestoreRequired(ctx, []string{deviceID}, "restore"); err != nil {
		t.Fatal(err)
	}
	events := []syncrecovery.EventIdentity{
		{EventID: "40000000-0000-4000-8000-000000000001", PayloadHash: "aa"},
		{EventID: "50000000-0000-4000-8000-000000000002", PayloadHash: "bb"},
		{EventID: "60000000-0000-4000-8000-000000000003", PayloadHash: "cc"},
	}
	f.insertEvent(t, events[0].EventID, deviceID, &f.storeA, events[0].PayloadHash)
	// Retail has already captured 1 as the initial count. New events are
	// accepted before it sends that first count-bearing page.
	for _, event := range events[1:] {
		f.insertEvent(t, event.EventID, deviceID, &f.storeA, event.PayloadHash)
	}
	code, body := f.verify(t, token, events[:1], 1, false, "")
	if code != http.StatusOK {
		t.Fatalf("first page rejected after concurrent Cloud events: HTTP %d %s", code, body)
	}
	progress, found, err := f.devices.VerifyProgress(ctx, deviceID)
	if err != nil || !found || progress.DeclaredTotal != 3 || progress.Events != 1 || progress.Complete {
		t.Fatalf("pass must atomically raise its effective lower bound to Cloud's current count: %+v found=%v err=%v", progress, found, err)
	}
	code, body = f.verify(t, token, events[1:], 0, true, "")
	if code != http.StatusOK {
		t.Fatalf("terminal page after the concurrent count increase: HTTP %d %s", code, body)
	}
	code, body = f.request(t, token, http.MethodPost, "/api/v1/device-control/recovery/complete", `{"last_missing":0}`)
	if code != http.StatusOK {
		t.Fatalf("completion at the atomically captured Cloud floor: HTTP %d %s", code, body)
	}
	progress, found, err = f.devices.VerifyProgress(ctx, deviceID)
	if err != nil || !found || progress.DeclaredTotal != 3 || progress.Events != 3 || progress.Missing != 0 || !progress.Complete {
		t.Fatalf("expected complete generation at captured floor: %+v found=%v err=%v", progress, found, err)
	}
}
