package http

import (
	"context"
	"net/http"
	"testing"

	"github.com/faroukelabady/MoonLightCloud/internal/syncrecovery"
)

// This is the database-backed completion contract for Retail's live retained
// outbox walk: a count captured before the walk is a lower bound, because
// ordinary business activity can add events before the terminal page.
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

// A Sale can be accepted by Cloud after Retail captures its count but before
// its first verification page. The begin-pass SQL captures the current
// Cloud-known floor atomically with generation initialization.
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
