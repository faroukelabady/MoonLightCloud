package http

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	"github.com/faroukelabady/MoonLightCloud/internal/store"
	"github.com/faroukelabady/MoonLightCloud/internal/syncrecovery"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type completionReadBarrier struct {
	syncrecovery.Store
	armed   atomic.Bool
	read    chan struct{}
	release chan struct{}
}

func (b *completionReadBarrier) VerifyProgress(ctx context.Context, deviceID string) (syncrecovery.VerifyProgress, bool, error) {
	p, ok, err := b.Store.VerifyProgress(ctx, deviceID)
	if err == nil && b.armed.CompareAndSwap(true, false) {
		close(b.read)
		<-b.release
	}
	return p, ok, err
}

type recoveryHTTPFixture struct {
	pool     *pgxpool.Pool
	devices  postgres.Devices
	service  *syncrecovery.Service
	server   *httptest.Server
	storeA   string
	newToken func(string) (string, string)
}

func newRecoveryHTTPFixture(t *testing.T, serviceStore syncrecovery.Store) *recoveryHTTPFixture {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testutil.Isolated(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	devices := postgres.NewDevices(pool, 5*time.Second)
	hasher, err := auth.NewHasher([]byte(strings.Repeat("r", 32)))
	if err != nil {
		t.Fatal(err)
	}
	authService := auth.NewService(devices, hasher, "", 1, clock.System{}, ids.System{})
	newToken := func(name string) (string, string) {
		t.Helper()
		created, err := authService.Create(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		token := created.Device.ID + "." + created.Credential.ID + "." + created.RawSecret
		return created.Device.ID, token
	}
	storeA := uuid.NewString()
	if serviceStore == nil {
		serviceStore = devices
	} else if barrier, ok := serviceStore.(*completionReadBarrier); ok {
		barrier.Store = devices
	}
	service := syncrecovery.NewService(serviceStore)
	handler := &SyncRecoveryHandlers{Svc: service, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/device-control/recovery/status", DeviceAuth(authService)(http.HandlerFunc(handler.RecoveryStatus)))
	mux.Handle("POST /api/v1/device-control/recovery/verify", DeviceAuth(authService)(http.HandlerFunc(handler.RecoveryVerify)))
	mux.Handle("POST /api/v1/device-control/recovery/complete", DeviceAuth(authService)(http.HandlerFunc(handler.RecoveryComplete)))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return &recoveryHTTPFixture{pool: pool, devices: devices, service: service, server: server, storeA: storeA, newToken: newToken}
}

func (f *recoveryHTTPFixture) bind(t *testing.T, deviceID, storeID string) {
	t.Helper()
	if _, err := f.devices.RegisterStore(context.Background(), deviceID, store.RegistrationRequest{
		StoreID: storeID, DisplayName: "R10 Store", Timezone: "Africa/Cairo",
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *recoveryHTTPFixture) request(t *testing.T, token, method, path, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, data
}

func (f *recoveryHTTPFixture) insertEvent(t *testing.T, id, deviceID string, storeID *string, hash string) {
	t.Helper()
	var owner any
	if storeID != nil {
		owner = *storeID
	}
	ct, err := f.pool.Exec(context.Background(), `INSERT INTO sync_events
		(event_id,device_id,event_type,occurred_at,payload,payload_hash,store_id)
		VALUES($1,$2,'system.recovery_test.v1',now(),'{}',decode($3,'hex'),$4)`, id, deviceID, hash, owner)
	if err != nil || ct.RowsAffected() != 1 {
		t.Fatalf("insert event affected=%d err=%v", ct.RowsAffected(), err)
	}
}

func (f *recoveryHTTPFixture) verify(t *testing.T, token string, events []syncrecovery.EventIdentity, declared int64, end bool, extra string) (int, []byte) {
	t.Helper()
	encoded, err := json.Marshal(struct {
		Events       []syncrecovery.EventIdentity `json:"events"`
		Declared     int64                        `json:"declared_total"`
		End          bool                         `json:"end_of_history"`
		SpoofedScope string                       `json:"store_id,omitempty"`
		DeviceID     string                       `json:"device_id,omitempty"`
	}{Events: events, Declared: declared, End: end})
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	if extra != "" {
		body = strings.TrimSuffix(body, "}") + "," + extra + "}"
	}
	return f.request(t, token, http.MethodPost, "/api/v1/device-control/recovery/verify", body)
}

// This adapts the independent F05 acceptance barrier: the completion
// handler pauses only after PostgreSQL returned the ledger it validated.
func TestR10StaleCompletionCannotClearNewGenerationAuthenticatedPostgres(t *testing.T) {
	ctx := context.Background()
	barrier := &completionReadBarrier{read: make(chan struct{}), release: make(chan struct{})}
	f := newRecoveryHTTPFixture(t, barrier)
	deviceID, token := f.newToken("r10-f05-device")
	f.bind(t, deviceID, f.storeA)
	if err := f.service.MarkRestoreRequired(ctx, []string{deviceID}, "restore"); err != nil {
		t.Fatal(err)
	}
	existingID, missingID := "10000000-0000-4000-8000-000000000001", "20000000-0000-4000-8000-000000000002"
	f.insertEvent(t, existingID, deviceID, &f.storeA, "aa")
	first, body := f.verify(t, token, []syncrecovery.EventIdentity{{EventID: existingID, PayloadHash: "aa"}}, 1, true, "")
	if first != http.StatusOK {
		t.Fatalf("initial verification HTTP %d: %s", first, body)
	}
	var initial syncrecovery.VerifyResponse
	if err := json.Unmarshal(body, &initial); err != nil || len(initial.Missing) != 0 {
		t.Fatalf("initial verification response=%+v err=%v", initial, err)
	}
	before, _, err := f.devices.VerifyProgress(ctx, deviceID)
	if err != nil || before.Generation != 1 || before.Missing != 0 || !before.Complete {
		t.Fatalf("initial durable generation=%+v err=%v", before, err)
	}

	barrier.armed.Store(true)
	completion := make(chan struct {
		code int
		body []byte
	}, 1)
	go func() {
		code, b := f.request(t, token, http.MethodPost, "/api/v1/device-control/recovery/complete", `{"last_missing":0}`)
		completion <- struct {
			code int
			body []byte
		}{code: code, body: b}
	}()
	select {
	case <-barrier.read:
	case <-time.After(10 * time.Second):
		t.Fatal("completion did not reach post-ledger barrier")
	}
	second, body := f.verify(t, token, []syncrecovery.EventIdentity{
		{EventID: existingID, PayloadHash: "aa"}, {EventID: missingID, PayloadHash: "bb"},
	}, 2, true, "")
	if second != http.StatusOK {
		t.Fatalf("new-generation verification HTTP %d: %s", second, body)
	}
	var newer syncrecovery.VerifyResponse
	if err := json.Unmarshal(body, &newer); err != nil || len(newer.Missing) != 1 || newer.Missing[0] != missingID {
		t.Fatalf("new generation must record missing identity: %+v err=%v", newer, err)
	}
	current, _, err := f.devices.VerifyProgress(ctx, deviceID)
	if err != nil || current.Generation != before.Generation+1 || current.Missing != 1 || current.Events != 2 || current.LastEventID != missingID {
		t.Fatalf("new ledger before stale completion release=%+v err=%v", current, err)
	}
	close(barrier.release)
	var stale struct {
		code int
		body []byte
	}
	select {
	case stale = <-completion:
	case <-time.After(10 * time.Second):
		t.Fatal("completion did not finish")
	}
	if stale.code != http.StatusBadRequest || !strings.Contains(string(stale.body), "stale or incomplete") {
		t.Fatalf("stale completion must return a truthful 400: HTTP=%d %s", stale.code, stale.body)
	}
	after, _, err := f.devices.VerifyProgress(ctx, deviceID)
	if err != nil || after != current {
		t.Fatalf("stale completion modified current ledger: before=%+v after=%+v err=%v", current, after, err)
	}
	state, found, err := f.devices.GetRecoveryState(ctx, deviceID)
	if err != nil || !found || !state.Required {
		t.Fatalf("stale completion cleared recovery: state=%+v found=%v err=%v", state, found, err)
	}
	statusCode, statusBody := f.request(t, token, http.MethodGet, "/api/v1/device-control/recovery/status", "")
	var status syncrecovery.Report
	if statusCode != http.StatusOK || json.Unmarshal(statusBody, &status) != nil || !status.Required || status.StoreReconciled {
		t.Fatalf("status after stale completion HTTP=%d report=%+v body=%s", statusCode, status, statusBody)
	}

	// Recreate the service to model a Cloud restart: the newer required
	// generation and missing count remain durable in PostgreSQL.
	restarted := syncrecovery.NewService(f.devices)
	restartedReport, err := restarted.Status(ctx, deviceID)
	if err != nil || !restartedReport.Required || restartedReport.StoreReconciled {
		t.Fatalf("restarted recovery status=%+v err=%v", restartedReport, err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO sync_events(event_id,device_id,event_type,occurred_at,payload,payload_hash,store_id)
		VALUES($1,$2,'system.recovery_test.v1',now(),'{}',decode('bb','hex'),$3)`, missingID, deviceID, f.storeA); err != nil {
		t.Fatal(err)
	}
	finalCode, finalBody := f.verify(t, token, []syncrecovery.EventIdentity{
		{EventID: existingID, PayloadHash: "aa"}, {EventID: missingID, PayloadHash: "bb"},
	}, 2, true, "")
	if finalCode != http.StatusOK {
		t.Fatalf("repaired verification HTTP %d: %s", finalCode, finalBody)
	}
	completeCode, completeBody := f.request(t, token, http.MethodPost, "/api/v1/device-control/recovery/complete", `{"last_missing":0}`)
	if completeCode != http.StatusOK {
		t.Fatalf("valid current completion HTTP %d: %s", completeCode, completeBody)
	}
	finalReport, err := restarted.Status(ctx, deviceID)
	if err != nil || finalReport.Required || !finalReport.StoreReconciled {
		t.Fatalf("valid later pass did not reconcile: %+v err=%v", finalReport, err)
	}

	// Concurrent retries of the same successful completion remain
	// idempotent for the exact completed generation.
	var wg sync.WaitGroup
	results := make(chan int, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			code, _ := f.request(t, token, http.MethodPost, "/api/v1/device-control/recovery/complete", `{"last_missing":0}`)
			results <- code
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for code := range results {
		if code != http.StatusOK {
			t.Fatalf("same-generation completion retry HTTP %d", code)
		}
	}
}

func TestR10RecoveryEventHashesAreAuthenticatedStoreScopedHTTPPostgres(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryHTTPFixture(t, nil)
	aID, aToken := f.newToken("r10-f06-a")
	replacementID, replacementToken := f.newToken("r10-f06-replacement")
	bID, bToken := f.newToken("r10-f06-b")
	unboundID, unboundToken := f.newToken("r10-f06-unbound")
	f.bind(t, aID, f.storeA)
	if err := f.devices.EnrollStore(ctx, replacementID, f.storeA); err != nil {
		t.Fatalf("operator same-Store replacement enrollment: %v", err)
	}
	storeB := uuid.NewString()
	f.bind(t, bID, storeB)
	for _, id := range []string{aID, replacementID, bID, unboundID} {
		if err := f.service.MarkRestoreRequired(ctx, []string{id}, "restore"); err != nil {
			t.Fatal(err)
		}
	}
	ids := []string{
		"10000000-0000-4000-8000-000000000011",
		"20000000-0000-4000-8000-000000000022",
		"30000000-0000-4000-8000-000000000033",
		"40000000-0000-4000-8000-000000000044",
	}
	sort.Strings(ids)
	f.insertEvent(t, ids[0], aID, &f.storeA, "aa")
	f.insertEvent(t, ids[1], replacementID, &f.storeA, "bb")
	f.insertEvent(t, ids[2], aID, nil, "cc") // legacy, authorized only to its original bound device
	f.insertEvent(t, ids[3], bID, &storeB, "dd")

	identities := []syncrecovery.EventIdentity{
		{EventID: ids[0], PayloadHash: "aa"}, {EventID: ids[1], PayloadHash: "bb"},
		{EventID: ids[2], PayloadHash: "cc"}, {EventID: ids[3], PayloadHash: "dd"},
	}
	code, body := f.verify(t, aToken, identities, int64(len(identities)), true, "")
	var own syncrecovery.VerifyResponse
	if code != http.StatusOK || json.Unmarshal(body, &own) != nil || len(own.Missing) != 1 || own.Missing[0] != ids[3] {
		t.Fatalf("Store A matches should include same-Store and own legacy events only: HTTP=%d response=%+v body=%s", code, own, body)
	}

	// A replacement device in the same Store sees event identities from
	// the Store, but cannot claim unowned legacy history from the old device.
	code, body = f.verify(t, replacementToken, identities[:2], 2, true, "")
	var replacement syncrecovery.VerifyResponse
	if code != http.StatusOK || json.Unmarshal(body, &replacement) != nil || len(replacement.Missing) != 0 {
		t.Fatalf("same-Store replacement must preserve scoped continuity: HTTP=%d response=%+v body=%s", code, replacement, body)
	}
	code, body = f.verify(t, replacementToken, identities[2:3], 1, true, "")
	var legacy syncrecovery.VerifyResponse
	if code != http.StatusOK || json.Unmarshal(body, &legacy) != nil || len(legacy.Missing) != 1 {
		t.Fatalf("replacement must not adopt unauthorized NULL-Store event: HTTP=%d response=%+v body=%s", code, legacy, body)
	}
	aProgressBeforeForeign, _, err := f.devices.VerifyProgress(ctx, aID)
	if err != nil {
		t.Fatal(err)
	}

	foreign := []syncrecovery.EventIdentity{{EventID: ids[0], PayloadHash: "aa"}}
	code, body = f.verify(t, bToken, foreign, 1, true, `"store_id":"`+f.storeA+`","device_id":"`+aID+`"`)
	var crossStore syncrecovery.VerifyResponse
	if code != http.StatusOK || json.Unmarshal(body, &crossStore) != nil || len(crossStore.Missing) != 1 || crossStore.Missing[0] != ids[0] {
		t.Fatalf("Store B spoofed request must not match Store A identity: HTTP=%d response=%+v body=%s", code, crossStore, body)
	}
	code, body = f.verify(t, unboundToken, foreign, 1, true, `"store_id":"`+f.storeA+`","device_id":"`+aID+`"`)
	var unbound syncrecovery.VerifyResponse
	if code != http.StatusOK || json.Unmarshal(body, &unbound) != nil || len(unbound.Missing) != 1 {
		t.Fatalf("unbound identity lookup must fail closed: HTTP=%d response=%+v body=%s", code, unbound, body)
	}
	aProgressAfterForeign, _, err := f.devices.VerifyProgress(ctx, aID)
	if err != nil || aProgressAfterForeign != aProgressBeforeForeign {
		t.Fatalf("foreign verification changed Store A ledger: before=%+v after=%+v err=%v", aProgressBeforeForeign, aProgressAfterForeign, err)
	}

	// A matching event ID with different immutable payload hash is not
	// acknowledged as identical.
	code, body = f.verify(t, aToken, []syncrecovery.EventIdentity{{EventID: ids[0], PayloadHash: "ff"}}, 2, true, "")
	var mismatch syncrecovery.VerifyResponse
	if code != http.StatusOK || json.Unmarshal(body, &mismatch) != nil || len(mismatch.Missing) != 1 {
		t.Fatalf("hash mismatch must be reported missing: HTTP=%d response=%+v body=%s", code, mismatch, body)
	}

	aProgress, _, err := f.devices.VerifyProgress(ctx, aID)
	if err != nil || aProgress.Generation != aProgressBeforeForeign.Generation+1 || aProgress.Events != 1 || aProgress.Missing != 1 {
		t.Fatalf("Store A durable verification state=%+v err=%v", aProgress, err)
	}
	if code, body = f.request(t, "human-session-token", http.MethodPost, "/api/v1/device-control/recovery/verify", `{"events":[]}`); code != http.StatusUnauthorized {
		t.Fatalf("human session must not authenticate device endpoint: HTTP=%d body=%s", code, body)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE devices SET status='revoked', revoked_at=now() WHERE id=$1`, bID); err != nil {
		t.Fatal(err)
	}
	if code, body = f.verify(t, bToken, foreign, 1, true, ""); code != http.StatusUnauthorized {
		t.Fatalf("revoked credential must be rejected: HTTP=%d body=%s", code, body)
	}
}

func TestR10CompleteAndNewRecoveryMarkAreGenerationSerialized(t *testing.T) {
	ctx := context.Background()
	barrier := &completionReadBarrier{read: make(chan struct{}), release: make(chan struct{})}
	f := newRecoveryHTTPFixture(t, barrier)
	deviceID, token := f.newToken("r10-f05-mark-race")
	f.bind(t, deviceID, f.storeA)
	if err := f.service.MarkRestoreRequired(ctx, []string{deviceID}, "restore"); err != nil {
		t.Fatal(err)
	}
	eventID := "10000000-0000-4000-8000-000000000099"
	f.insertEvent(t, eventID, deviceID, &f.storeA, "aa")
	if code, body := f.verify(t, token, []syncrecovery.EventIdentity{{EventID: eventID, PayloadHash: "aa"}}, 1, true, ""); code != http.StatusOK {
		t.Fatalf("verify HTTP %d: %s", code, body)
	}
	barrier.armed.Store(true)
	done := make(chan int, 1)
	go func() {
		code, _ := f.request(t, token, http.MethodPost, "/api/v1/device-control/recovery/complete", `{"last_missing":0}`)
		done <- code
	}()
	select {
	case <-barrier.read:
	case <-time.After(10 * time.Second):
		t.Fatal("completion did not pause after its ledger read")
	}
	if err := f.devices.UpsertRecoveryState(ctx, deviceID, "new recovery mark"); err != nil {
		t.Fatal(err)
	}
	close(barrier.release)
	select {
	case code := <-done:
		if code == http.StatusOK {
			t.Fatal("old completion must not clear a newly marked recovery generation")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("completion did not finish after release")
	}
	state, found, err := f.devices.GetRecoveryState(ctx, deviceID)
	if err != nil || !found || !state.Required {
		t.Fatalf("new recovery requirement was cleared: %+v found=%v err=%v", state, found, err)
	}
	progress, _, err := f.devices.VerifyProgress(ctx, deviceID)
	if err != nil || progress.Generation != 2 || progress.Events != 0 || progress.Pages != 0 || progress.Missing != 0 || progress.Complete {
		t.Fatalf("mark racing completion did not preserve a clean newer ledger: %+v err=%v", progress, err)
	}
}

func TestR10RecoveryPageRecordRejectsStaleGeneration(t *testing.T) {
	ctx := context.Background()
	f := newRecoveryHTTPFixture(t, nil)
	deviceID, token := f.newToken("r10-page-generation")
	f.bind(t, deviceID, f.storeA)
	if err := f.service.MarkRestoreRequired(ctx, []string{deviceID}, "restore"); err != nil {
		t.Fatal(err)
	}
	oldEvent, newEvent := "10000000-0000-4000-8000-000000000101", "20000000-0000-4000-8000-000000000202"
	f.insertEvent(t, oldEvent, deviceID, &f.storeA, "aa")
	f.insertEvent(t, newEvent, deviceID, &f.storeA, "bb")
	start := make(chan struct{})
	type beginResult struct {
		generation int64
		declared   int64
		err        error
	}
	results := make(chan beginResult, 2)
	for _, declared := range []int64{2, 3} {
		declared := declared
		go func() {
			<-start
			generation, err := f.devices.BeginVerifyPass(ctx, deviceID, declared)
			results <- beginResult{generation: generation, declared: declared, err: err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.generation == second.generation {
		t.Fatalf("concurrent pass initialization: first=%+v second=%+v", first, second)
	}
	old, current := first, second
	if current.generation < old.generation {
		old, current = current, old
	}
	progress, _, err := f.devices.VerifyProgress(ctx, deviceID)
	if err != nil || progress.Generation != current.generation || progress.DeclaredTotal != current.declared {
		t.Fatalf("initialization did not preserve newest generation/cursor: current=%+v ledger=%+v err=%v", current, progress, err)
	}
	advanced, err := f.devices.RecordVerifyPage(ctx, deviceID, old.generation, oldEvent, oldEvent, 1, 0, true)
	if err != nil || advanced {
		t.Fatalf("stale page record advanced new generation: advanced=%v err=%v", advanced, err)
	}
	progress, _, err = f.devices.VerifyProgress(ctx, deviceID)
	if err != nil || progress.Generation != current.generation || progress.Events != 0 || progress.Pages != 0 || progress.LastEventID != "" {
		t.Fatalf("stale page mutated current generation: %+v err=%v", progress, err)
	}
	advanced, err = f.devices.RecordVerifyPage(ctx, deviceID, current.generation, oldEvent, oldEvent, 1, 0, false)
	if err != nil || !advanced {
		t.Fatalf("current generation first page did not advance: advanced=%v err=%v", advanced, err)
	}
	advanced, err = f.devices.RecordVerifyPage(ctx, deviceID, current.generation, newEvent, newEvent, 1, 0, true)
	if err != nil || !advanced {
		t.Fatalf("current generation second page did not advance: advanced=%v err=%v", advanced, err)
	}
	progress, _, err = f.devices.VerifyProgress(ctx, deviceID)
	if err != nil || progress.Generation != current.generation || progress.Events != 2 || progress.Pages != 2 || progress.LastEventID != newEvent || !progress.Complete {
		t.Fatalf("page accounting/cursor changed under concurrent initialization: %+v err=%v", progress, err)
	}
	code, body := f.verify(t, token, []syncrecovery.EventIdentity{
		{EventID: oldEvent, PayloadHash: "aa"}, {EventID: newEvent, PayloadHash: "bb"},
	}, 2, true, "")
	if code != http.StatusOK {
		t.Fatalf("current pass verification HTTP %d: %s", code, body)
	}
	progress, _, err = f.devices.VerifyProgress(ctx, deviceID)
	if err != nil || progress.Generation != current.generation+1 || progress.Events != 2 || progress.Pages != 1 {
		t.Fatalf("current verification did not advance expected generation: %+v err=%v", progress, err)
	}
}
