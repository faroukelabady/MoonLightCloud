package postgres

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/platform/clock"
	isync "github.com/faroukelabady/MoonLightCloud/internal/sync"
	"github.com/jackc/pgx/v5/pgxpool"
)

// openSync wires device + sync services against an isolated database and
// provisions one device. Returns pool, sync service, device ID, credential ID.
func openSync(t *testing.T) (*pgxpool.Pool, isync.Service, string, string) {
	t.Helper()
	pool, authSvc := openTestRepo(t)
	p, err := authSvc.Create(context.Background(), "shop-dev")
	if err != nil {
		t.Fatal(err)
	}
	store := NewDevices(pool, 5*time.Second)
	return pool, isync.NewService(store, clock.System{}), p.Device.ID, p.Credential.ID
}

func ev(id string, payload string) string {
	return fmt.Sprintf(`{"event_id":%q,"event_type":"system.test.v1","occurred_at":"2026-09-19T10:20:30Z","payload":%s}`,
		id, payload)
}

func batch(events ...string) string {
	return `{"events":[` + strings.Join(events, ",") + `]}`
}

func ingest(t *testing.T, svc isync.Service, deviceID, credID, body string) isync.BatchResult {
	t.Helper()
	res, err := svc.Ingest(context.Background(), deviceID, credID, []byte(body))
	if err != nil {
		t.Fatalf("ingest failed: %v", err)
	}
	return res
}

func kindOf(err error) apperr.Kind {
	for err != nil {
		if ae, ok := err.(*apperr.Error); ok {
			return ae.Kind
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return apperr.Internal
		}
		err = u.Unwrap()
	}
	return apperr.Internal
}

func TestFirstAcceptanceAndDurableRow(t *testing.T) {
	pool, svc, deviceID, credID := openSync(t)
	id := "22222222-2222-7222-8222-222222222222"
	res := ingest(t, svc, deviceID, credID, batch(ev(id, `{"a":1}`)))
	if len(res.Events) != 1 || res.Events[0].Status != isync.StatusAccepted {
		t.Fatalf("bad result: %+v", res)
	}
	var count int
	var storedDevice, storedType string
	err := pool.QueryRow(context.Background(),
		`SELECT count(*), device_id::text, event_type FROM sync_events WHERE event_id=$1 GROUP BY device_id, event_type`,
		id).Scan(&count, &storedDevice, &storedType)
	if err != nil {
		t.Fatal(err)
	}
	if storedDevice != deviceID || storedType != "system.test.v1" {
		t.Fatalf("bad stored row: %s %s", storedDevice, storedType)
	}
}

func TestDuplicateRetryAccepted(t *testing.T) {
	_, svc, deviceID, credID := openSync(t)
	id := "22222222-2222-7222-8222-222222222222"
	body := batch(ev(id, `{"a":1}`))
	ingest(t, svc, deviceID, credID, body)
	// Lost ACK simulation: same bytes again → already_accepted, still one row.
	res := ingest(t, svc, deviceID, credID, body)
	if res.Events[0].Status != isync.StatusAlreadyAccepted {
		t.Fatalf("want already_accepted, got %+v", res)
	}
}

func TestPayloadMismatchConflict(t *testing.T) {
	_, svc, deviceID, credID := openSync(t)
	id := "22222222-2222-7222-8222-222222222222"
	ingest(t, svc, deviceID, credID, batch(ev(id, `{"a":1}`)))
	_, err := svc.Ingest(context.Background(), deviceID, credID, []byte(batch(ev(id, `{"a":2}`))))
	if err == nil || kindOf(err) != apperr.Conflict {
		t.Fatalf("want EVENT_ID_REUSE conflict, got %v", err)
	}
	if !strings.Contains(err.Error(), "EVENT_ID_REUSE") {
		t.Fatalf("want EVENT_ID_REUSE marker, got %v", err)
	}
}

func TestBatchAllOrNothing(t *testing.T) {
	pool, svc, deviceID, credID := openSync(t)
	good1 := ev("22222222-2222-7222-8222-222222222222", `{"a":1}`)
	good2 := ev("33333333-3333-7333-8333-333333333333", `{"b":2}`)
	bad := `{"event_type":"system.test.v1","occurred_at":"2026-09-19T10:20:30Z","payload":{}}`
	_, err := svc.Ingest(context.Background(), deviceID, credID, []byte(batch(good1, bad, good2)))
	if err == nil {
		t.Fatal("malformed batch must fail")
	}
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM sync_events`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("all-or-nothing violated: %d rows, err %v", n, err)
	}
}

func TestUnsupportedEventType(t *testing.T) {
	_, svc, deviceID, credID := openSync(t)
	_, err := svc.Ingest(context.Background(), deviceID, credID,
		[]byte(batch(ev("22222222-2222-7222-8222-222222222222", `{"a":1}`)[:0]+
			`{"event_id":"22222222-2222-7222-8222-222222222222","event_type":"sale.finalized.v1","occurred_at":"2026-09-19T10:20:30Z","payload":{}}`)))
	if err == nil || kindOf(err) != apperr.Unprocessable {
		t.Fatalf("want 422 unsupported, got %v", err)
	}
}

func TestConcurrentIdenticalIngest(t *testing.T) {
	pool, svc, deviceID, credID := openSync(t)
	id := "22222222-2222-7222-8222-222222222222"
	body := batch(ev(id, `{"n":1}`))
	var wg sync.WaitGroup
	errs := make([]error, 8)
	results := make([]isync.BatchResult, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = svc.Ingest(context.Background(), deviceID, credID, []byte(body))
		}(i)
	}
	wg.Wait()
	accepted := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: concurrent identical ingest must succeed: %v", i, err)
		}
		if results[i].Events[0].Status == isync.StatusAccepted {
			accepted++
		} else if results[i].Events[0].Status != isync.StatusAlreadyAccepted {
			t.Fatalf("worker %d: bad status %+v", i, results[i])
		}
	}
	if accepted != 1 {
		t.Fatalf("want exactly one accepted, got %d", accepted)
	}
	var n int
	_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM sync_events`).Scan(&n)
	if n != 1 {
		t.Fatalf("want one stored row, got %d", n)
	}
}

func TestConcurrentMismatchOneWins(t *testing.T) {
	_, svc, deviceID, credID := openSync(t)
	id := "22222222-2222-7222-8222-222222222222"
	var wg sync.WaitGroup
	errs := make([]error, 2)
	payloads := []string{`{"v":1}`, `{"v":2}`}
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = svc.Ingest(context.Background(), deviceID, credID, []byte(batch(ev(id, payloads[i]))))
		}(i)
	}
	wg.Wait()
	conflicts := 0
	for _, err := range errs {
		if err != nil {
			if kindOf(err) != apperr.Conflict {
				t.Fatalf("loser must get conflict, got %v", err)
			}
			conflicts++
		}
	}
	if conflicts != 1 {
		t.Fatalf("want exactly one conflict, errs=%v", errs)
	}
}

func TestConcurrentRotationsSingleActive(t *testing.T) {
	pool, authSvc := openTestRepo(t)
	p, err := authSvc.Create(context.Background(), "shop-dev")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = authSvc.Rotate(context.Background(), p.Device.ID)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent rotation must succeed: %v", err)
		}
	}
	var active int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM device_credentials WHERE device_id=$1 AND status='active'`,
		p.Device.ID).Scan(&active); err != nil || active != 1 {
		t.Fatalf("want exactly one active credential, got %d (%v)", active, err)
	}
}

func TestDBFailureNoPartialACK(t *testing.T) {
	pool, svc, deviceID, credID := openSync(t)
	pool.Close() // simulate database outage
	_, err := svc.Ingest(context.Background(), deviceID, credID,
		[]byte(batch(ev("22222222-2222-7222-8222-222222222222", `{"a":1}`))))
	if err == nil {
		t.Fatal("ingestion without DB must fail")
	}
	if kindOf(err) != apperr.Unavailable {
		t.Fatalf("want retryable UNAVAILABLE, got %v", err)
	}
}

func TestCredentialRotationDuringRetry(t *testing.T) {
	pool, authSvc := openTestRepo(t)
	p, err := authSvc.Create(context.Background(), "shop-dev")
	if err != nil {
		t.Fatal(err)
	}
	store := NewDevices(pool, 5*time.Second)
	syncSvc := isync.NewService(store, clock.System{})
	id := "22222222-2222-7222-8222-222222222222"
	body := batch(ev(id, `{"a":1}`))
	ingest(t, syncSvc, p.Device.ID, p.Credential.ID, body)
	// Rotate, then retry the same event with the NEW credential.
	r, err := authSvc.Rotate(context.Background(), p.Device.ID)
	if err != nil {
		t.Fatal(err)
	}
	res := ingest(t, syncSvc, p.Device.ID, r.Credential.ID, body)
	if res.Events[0].Status != isync.StatusAlreadyAccepted {
		t.Fatalf("retry with rotated credential must deduplicate: %+v", res)
	}
}
