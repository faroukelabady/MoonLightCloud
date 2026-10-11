package http

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/adapter/postgres"
	"github.com/faroukelabady/MoonLightCloud/internal/auth"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/ids"
	"github.com/faroukelabady/MoonLightCloud/internal/sale"
	"github.com/faroukelabady/MoonLightCloud/internal/store"
	syncsvc "github.com/faroukelabady/MoonLightCloud/internal/sync"
	"github.com/faroukelabady/MoonLightCloud/internal/syncrecovery"
	"github.com/faroukelabady/MoonLightCloud/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This exercises authenticated Cloud HTTP, the durable PostgreSQL inbox,
// required-processor status, the canonical Sale projector and exact totals.
func TestRecoveryStatusWaitsForAcceptedSaleProjectionHTTPPostgres(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, testutil.Isolated(t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	devices := postgres.NewDevices(pool, 5*time.Second)
	hasher, err := auth.NewHasher([]byte(strings.Repeat("t", 32)))
	if err != nil {
		t.Fatal(err)
	}
	authService := auth.NewService(devices, hasher, "", 1, clock.System{}, ids.System{})
	created, err := authService.Create(ctx, "recovery-projection-http")
	if err != nil {
		t.Fatal(err)
	}
	token := created.Device.ID + "." + created.Credential.ID + "." + created.RawSecret
	if _, err := devices.RegisterStore(ctx, created.Device.ID, store.RegistrationRequest{
		StoreID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", DisplayName: "Recovery HTTP", Timezone: "Africa/Cairo",
	}); err != nil {
		t.Fatal(err)
	}
	recovery := syncrecovery.NewService(devices)
	if err := recovery.MarkRestoreRequired(ctx, []string{created.Device.ID}, "restore regression"); err != nil {
		t.Fatal(err)
	}
	syncsvc.RegisterEventType(sale.EventSaleFinalizedV1, func(raw json.RawMessage) error {
		payload, err := sale.Decode(raw)
		if err != nil {
			return err
		}
		_, err = sale.Validate(payload)
		return err
	})
	batchService := syncsvc.NewService(devices, clock.System{})
	recoveryHandler := &SyncRecoveryHandlers{Svc: recovery, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/device-control/recovery/status", DeviceAuth(authService)(http.HandlerFunc(recoveryHandler.RecoveryStatus)))
	mux.Handle("POST /api/v1/sync/batches", DeviceAuth(authService)(SyncBatch(batchService, nil)))
	server := httptest.NewServer(mux)
	defer server.Close()
	request := func(method, path, body string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := server.Client().Do(req)
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
	readStatus := func(wantPending int, wantReconciled bool) syncrecovery.Report {
		t.Helper()
		code, body := request(http.MethodGet, "/api/v1/device-control/recovery/status", "")
		var report syncrecovery.Report
		if code != http.StatusOK || json.Unmarshal(body, &report) != nil {
			t.Fatalf("recovery status HTTP %d: %s", code, body)
		}
		if report.ProjectionsPending != wantPending || report.StoreReconciled != wantReconciled {
			t.Fatalf("recovery status = %+v, want pending=%d reconciled=%v", report, wantPending, wantReconciled)
		}
		return report
	}
	readStatus(0, false) // Event-history requirement alone keeps Store unreconciled.

	payload, err := os.ReadFile("../../sale/testdata/sale_egp.json")
	if err != nil {
		t.Fatal(err)
	}
	eventID := "a1111111-1111-4111-8111-111111111111"
	batch := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":%q,"occurred_at":"2026-09-20T11:00:00Z","payload":%s}]}`,
		eventID, sale.EventSaleFinalizedV1, payload)
	code, body := request(http.MethodPost, "/api/v1/sync/batches", batch)
	if code != http.StatusOK {
		t.Fatalf("sale ingestion HTTP %d: %s", code, body)
	}
	assertStatus := func(want string) {
		t.Helper()
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sync_event_processing WHERE event_id=$1 AND processor='sale_projection.v1')`, eventID).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			if want != "<absent>" {
				t.Fatalf("sale processor row is absent, want %s", want)
			}
			readStatus(1, false)
			return
		}
		var state string
		if err := pool.QueryRow(ctx, `SELECT status FROM sync_event_processing WHERE event_id=$1 AND processor='sale_projection.v1'`, eventID).Scan(&state); err != nil {
			t.Fatalf("read sale processor status: %v", err)
		}
		if state != want {
			t.Fatalf("sale processor status=%s, want %s", state, want)
		}
		pending := 1
		if state == "processed" {
			pending = 0
		}
		readStatus(pending, false)
	}
	assertStatus("<absent>")
	if _, err := pool.Exec(ctx, `INSERT INTO sync_event_processing(event_id,processor,status) VALUES($1,'sale_projection.v1','pending')`, eventID); err != nil {
		t.Fatal(err)
	}
	assertStatus("pending")
	for _, state := range []string{"retry", "blocked"} {
		if _, err := pool.Exec(ctx, `UPDATE sync_event_processing SET status=$1 WHERE event_id=$2 AND processor='sale_projection.v1'`, state, eventID); err != nil {
			t.Fatal(err)
		}
		assertStatus(state)
	}
	if _, err := pool.Exec(ctx, `UPDATE sync_event_processing SET status='pending',last_error_code=NULL WHERE event_id=$1 AND processor='sale_projection.v1'`, eventID); err != nil {
		t.Fatal(err)
	}
	projector := sale.NewProjector(devices, clock.System{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	projector.Notify()
	projectCtx, cancel := context.WithCancel(ctx)
	finished := make(chan struct{})
	go func() { defer close(finished); projector.Run(projectCtx) }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var state string
		if err := pool.QueryRow(ctx, `SELECT status FROM sync_event_processing WHERE event_id=$1 AND processor='sale_projection.v1'`, eventID).Scan(&state); err == nil && state == "processed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-finished
	assertStatus("processed")
	var count int
	var total int64
	if err := pool.QueryRow(ctx, `SELECT count(*),COALESCE(sum(total_minor),0) FROM sales_projection`).Scan(&count, &total); err != nil {
		t.Fatal(err)
	}
	if count != 1 || total != 200000 {
		t.Fatalf("canonical Sale projection count=%d total_minor=%d, want 1 / 200000 EGP", count, total)
	}
	var hash []byte
	if err := pool.QueryRow(ctx, `SELECT payload_hash FROM sync_events WHERE event_id=$1`, eventID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	verify, err := recovery.Verify(ctx, created.Device.ID, syncrecovery.VerifyRequest{
		Events:       []syncrecovery.EventIdentity{{EventID: eventID, PayloadHash: hex.EncodeToString(hash)}},
		EndOfHistory: true, DeclaredTotal: 1,
	})
	if err != nil || len(verify.Missing) != 0 {
		t.Fatalf("verify complete history: %+v %v", verify, err)
	}
	if err := recovery.Complete(ctx, created.Device.ID, 0); err != nil {
		t.Fatal(err)
	}
	readStatus(0, true)
	code, body = request(http.MethodPost, "/api/v1/sync/batches", batch)
	if code != http.StatusOK {
		t.Fatalf("idempotent Sale replay HTTP %d: %s", code, body)
	}
	var replay syncsvc.BatchResult
	if err := json.Unmarshal(body, &replay); err != nil || len(replay.Events) != 1 || replay.Events[0].Status != syncsvc.StatusAlreadyAccepted {
		t.Fatalf("replay response=%+v err=%v", replay, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*),COALESCE(sum(total_minor),0) FROM sales_projection`).Scan(&count, &total); err != nil || count != 1 || total != 200000 {
		t.Fatalf("idempotent replay changed canonical Sale: count=%d total=%d err=%v", count, total, err)
	}
}
