package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/apperr"
	"github.com/faroukelabady/MoonLightCloud/internal/returnrefund"
)

func returnFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("../../returnrefund/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func ingestReturn(t *testing.T, env *saleEnv, eventID, payload string) error {
	t.Helper()
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.return_refund.finalized.v1","occurred_at":"2026-09-21T12:00:00Z","payload":%s}]}`,
		eventID, payload)
	_, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body))
	return err
}

func mustIngestReturn(t *testing.T, env *saleEnv, eventID, payload string) {
	t.Helper()
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.return_refund.finalized.v1","occurred_at":"2026-09-21T12:00:00Z","payload":%s}]}`,
		eventID, payload)
	res, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body))
	if err != nil {
		t.Fatalf("ingest return: %v", err)
	}
	if res.Events[0].Status != "accepted" {
		t.Fatalf("want accepted, got %+v", res)
	}
}

func kindOfReturnErr(err error) apperr.Kind {
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

// Valid partial return is durably accepted with inbox lineage intact; no
// projection exists in Phase 4A and reporting stays untouched.
func TestReturnPartialDurableAccept(t *testing.T) {
	env := openSaleEnv(t)
	eventID := "55555555-5555-7555-8555-555555555555"
	mustIngestReturn(t, env, eventID, returnFixture(t, "return_partial.json"))

	var eventType, deviceID, occurred, received string
	var hashVer int
	var payload []byte
	err := env.pool.QueryRow(context.Background(), `
		SELECT event_type, device_id::text, occurred_at::text, received_at::text, payload, payload_hash_version
		FROM sync_events WHERE event_id=$1`, eventID).Scan(&eventType, &deviceID, &occurred, &received, &payload, &hashVer)
	if err != nil {
		t.Fatal(err)
	}
	if eventType != returnrefund.EventReturnRefundFinalizedV1 || deviceID != env.devID || hashVer != 2 {
		t.Fatalf("bad inbox row: %s %s v%d", eventType, deviceID, hashVer)
	}
	if occurred == "" || received == "" {
		t.Fatal("occurred_at and received_at must persist")
	}
	var stored map[string]any
	if err := json.Unmarshal(payload, &stored); err != nil {
		t.Fatal(err)
	}
	if stored["return_refund_id"] != "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" || stored["sale_id"] != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("bad stored payload identity: %v", stored["return_refund_id"])
	}
	// No projection rows exist for returns in Phase 4A.
	var projections int
	if err := env.pool.QueryRow(context.Background(), `SELECT count(*) FROM sales_projection`).Scan(&projections); err != nil || projections != 0 {
		t.Fatalf("no return projection in 4A: %d (%v)", projections, err)
	}
	// Reporting stays finalized-sale-only: sale-scoped inbox counts unchanged.
	var saleEvents int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM sync_events WHERE event_type='sale.finalized.v1'`).Scan(&saleEvents); err != nil || saleEvents != 0 {
		t.Fatalf("sale inbox untouched: %d (%v)", saleEvents, err)
	}
}

// Full void return accepted.
func TestReturnFullVoidAccept(t *testing.T) {
	env := openSaleEnv(t)
	mustIngestReturn(t, env, "66666666-6666-7666-8666-666666666666", returnFixture(t, "return_full.json"))
}

// Identical retry is idempotent: one durable identity, both ACK.
func TestReturnIdenticalRetryIdempotent(t *testing.T) {
	env := openSaleEnv(t)
	eventID := "55555555-5555-7555-8555-555555555555"
	payload := returnFixture(t, "return_partial.json")
	mustIngestReturn(t, env, eventID, payload)
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.return_refund.finalized.v1","occurred_at":"2026-09-21T12:00:00Z","payload":%s}]}`,
		eventID, payload)
	res, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if res.Events[0].Status != "already_accepted" {
		t.Fatalf("want already_accepted, got %+v", res)
	}
	var n int
	if err := env.pool.QueryRow(context.Background(), `SELECT count(*) FROM sync_events`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("one durable identity: %d (%v)", n, err)
	}
}

// Same event ID + different payload conflicts; original preserved.
func TestReturnEventIDCollision(t *testing.T) {
	env := openSaleEnv(t)
	eventID := "55555555-5555-7555-8555-555555555555"
	mustIngestReturn(t, env, eventID, returnFixture(t, "return_partial.json"))
	var m map[string]any
	if err := json.Unmarshal([]byte(returnFixture(t, "return_partial.json")), &m); err != nil {
		t.Fatal(err)
	}
	m["return_number"] = "RET-20260921-collision"
	raw, _ := json.Marshal(m)
	if err := ingestReturn(t, env, eventID, string(raw)); err == nil {
		t.Fatal("colliding payload must conflict")
	} else if kindOfReturnErr(err) != apperr.Conflict {
		t.Fatalf("want 409, got %v", err)
	}
	var stored string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT payload::text FROM sync_events WHERE event_id=$1`, eventID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb") {
		t.Fatal("original immutable event must be preserved")
	}
}

// Two distinct returns against one sale both transport successfully: no
// "sale already has return" conflict — business identity is the return.
func TestReturnTwoDistinctEventsOneSale(t *testing.T) {
	env := openSaleEnv(t)
	base := returnFixture(t, "return_partial.json")
	var m map[string]any
	if err := json.Unmarshal([]byte(base), &m); err != nil {
		t.Fatal(err)
	}
	m["return_refund_id"] = "77777777-7777-4777-8777-777777777777"
	m["return_number"] = "RET-20260921-77777777"
	second, _ := json.Marshal(m)
	mustIngestReturn(t, env, "55555555-5555-7555-8555-555555555555", base)
	mustIngestReturn(t, env, "77777777-7777-7777-8777-777777777777", string(second))
	var n int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM sync_events WHERE event_type='sale.return_refund.finalized.v1'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("both returns accepted: %d (%v)", n, err)
	}
}

// Return accepted when the original sale projection row is absent: transport
// never waits for projection dependencies.
func TestReturnAcceptedWithoutSaleProjection(t *testing.T) {
	env := openSaleEnv(t)
	mustIngestReturn(t, env, "55555555-5555-7555-8555-555555555555", returnFixture(t, "return_partial.json"))
	// No sale ingested, no projection rows — accept stands.
}

// Invalid return payloads reject the whole batch with zero rows.
func TestReturnInvalidRejectsWholeBatch(t *testing.T) {
	env := openSaleEnv(t)
	bad := mutateReturnPayload(t, returnFixture(t, "return_partial.json"), func(m map[string]any) {
		m["totals"].(map[string]any)["refund_total"].(map[string]any)["amount_minor"] = 1
	})
	body := fmt.Sprintf(`{"events":[{"event_id":"55555555-5555-7555-8555-555555555555","event_type":"sale.return_refund.finalized.v1","occurred_at":"2026-09-21T12:00:00Z","payload":%s}]}`, bad)
	if _, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body)); err == nil {
		t.Fatal("invalid return must reject the batch")
	} else if kindOfReturnErr(err) != apperr.Unprocessable {
		t.Fatalf("want 422, got %v", err)
	}
	var n int
	if err := env.pool.QueryRow(context.Background(), `SELECT count(*) FROM sync_events`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("zero rows on invalid batch: %d (%v)", n, err)
	}
}

// Malformed IDs, quantities, and money fail validation, not storage.
func TestReturnMalformedFields(t *testing.T) {
	env := openSaleEnv(t)
	base := returnFixture(t, "return_partial.json")
	cases := []struct {
		name string
		fn   func(map[string]any)
	}{
		{"bad return id", func(m map[string]any) { m["return_refund_id"] = "x" }},
		{"bad sale id", func(m map[string]any) { m["sale_id"] = "x" }},
		{"zero quantity", func(m map[string]any) {
			m["lines"].([]any)[0].(map[string]any)["quantity"] = 0
		}},
		{"negative money", func(m map[string]any) {
			m["lines"].([]any)[0].(map[string]any)["refund"].(map[string]any)["amount_minor"] = -1
		}},
		{"fractional money", func(m map[string]any) {
			m["totals"].(map[string]any)["refund_total"].(map[string]any)["amount_minor"] = 1250.5
		}},
		{"bad kind", func(m map[string]any) { m["kind"] = "exchange" }},
		{"bad reason", func(m map[string]any) { m["reason"] = "nope" }},
		{"bad method", func(m map[string]any) {
			m["refunds"].([]any)[0].(map[string]any)["method"] = "store_credit"
		}},
	}
	for _, tc := range cases {
		if err := ingestReturn(t, env, "55555555-5555-7555-8555-555555555555", mutateReturnPayload(t, base, tc.fn)); err == nil {
			t.Fatalf("%s: must fail", tc.name)
		} else if kindOfReturnErr(err) != apperr.Unprocessable {
			t.Fatalf("%s: want 422, got %v", tc.name, err)
		}
	}
}

// Unsupported version is rejected, never interpreted as v1.
func TestReturnUnsupportedVersionRejected(t *testing.T) {
	env := openSaleEnv(t)
	body := fmt.Sprintf(`{"events":[{"event_id":"55555555-5555-7555-8555-555555555555","event_type":"sale.return_refund.finalized.v2","occurred_at":"2026-09-21T12:00:00Z","payload":%s}]}`,
		returnFixture(t, "return_partial.json"))
	if _, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body)); err == nil {
		t.Fatal("v2 must be rejected")
	} else if kindOfReturnErr(err) != apperr.Unprocessable {
		t.Fatalf("want 422, got %v", err)
	}
}

// Payload size envelope enforced for returns like sales.
func TestReturnPayloadSizeLimits(t *testing.T) {
	env := openSaleEnv(t)
	base := returnFixture(t, "return_partial.json")
	// Comfortably below limit: accepted.
	mustIngestReturn(t, env, "55555555-5555-7555-8555-555555555555", base)
	// Over limit: 413, nothing stored.
	big := mutateReturnPayload(t, base, func(m map[string]any) {
		m["note"] = strings.Repeat("مرحبا ", 60000)
	})
	if len(big) <= 256*1024 {
		t.Fatalf("test setup: payload must exceed 256 KiB, got %d", len(big))
	}
	body := fmt.Sprintf(`{"events":[{"event_id":"66666666-6666-7666-8666-666666666666","event_type":"sale.return_refund.finalized.v1","occurred_at":"2026-09-21T12:00:00Z","payload":%s}]}`, big)
	if _, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body)); err == nil {
		t.Fatal("oversize return must be rejected")
	} else if kindOfReturnErr(err) != apperr.TooLarge {
		t.Fatalf("want 413, got %v", err)
	}
	var n int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM sync_events WHERE event_id='66666666-6666-7666-8666-666666666666'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("oversize event stored nothing: %d (%v)", n, err)
	}
}

// Concurrent identical deliveries converge on one durable identity.
func TestReturnConcurrentIdenticalDelivery(t *testing.T) {
	env := openSaleEnv(t)
	payload := returnFixture(t, "return_partial.json")
	body := fmt.Sprintf(`{"events":[{"event_id":"55555555-5555-7555-8555-555555555555","event_type":"sale.return_refund.finalized.v1","occurred_at":"2026-09-21T12:00:00Z","payload":%s}]}`,
		payload)
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body))
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent identical ingest must succeed: %v", err)
		}
	}
	var n int
	if err := env.pool.QueryRow(context.Background(), `SELECT count(*) FROM sync_events`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("one durable identity: %d (%v)", n, err)
	}
}

// Concurrent conflicting deliveries never overwrite nondeterministically.
func TestReturnConcurrentConflictingDelivery(t *testing.T) {
	env := openSaleEnv(t)
	base := returnFixture(t, "return_partial.json")
	var m map[string]any
	if err := json.Unmarshal([]byte(base), &m); err != nil {
		t.Fatal(err)
	}
	m["return_number"] = "RET-20260921-conflict"
	alt, _ := json.Marshal(m)
	bodies := []string{
		fmt.Sprintf(`{"events":[{"event_id":"55555555-5555-7555-8555-555555555555","event_type":"sale.return_refund.finalized.v1","occurred_at":"2026-09-21T12:00:00Z","payload":%s}]}`, base),
		fmt.Sprintf(`{"events":[{"event_id":"55555555-5555-7555-8555-555555555555","event_type":"sale.return_refund.finalized.v1","occurred_at":"2026-09-21T12:00:00Z","payload":%s}]}`, string(alt)),
	}
	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(bodies[i]))
		}(i)
	}
	wg.Wait()
	succeeded := 0
	for _, err := range results {
		if err == nil {
			succeeded++
		} else if kindOfReturnErr(err) != apperr.Conflict {
			t.Fatalf("conflict path must be 409, got %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("exactly one conflicting delivery wins, got %d", succeeded)
	}
	var stored string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT payload::text FROM sync_events WHERE event_id='55555555-5555-7555-8555-555555555555'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var sm map[string]any
	if err := json.Unmarshal([]byte(stored), &sm); err != nil {
		t.Fatal(err)
	}
	if sm["return_number"] != "RET-20260921-bbbbbbbb" && sm["return_number"] != "RET-20260921-conflict" {
		t.Fatalf("stored payload must be one of the two racers, got %v", sm["return_number"])
	}
	var n int
	if err := env.pool.QueryRow(context.Background(), `SELECT count(*) FROM sync_events`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("single row after conflict race: %d (%v)", n, err)
	}
}

// Lost ACK: durable commit followed by identical retry is idempotent.
func TestReturnLostAckRetry(t *testing.T) {
	env := openSaleEnv(t)
	eventID := "55555555-5555-7555-8555-555555555555"
	payload := returnFixture(t, "return_partial.json")
	mustIngestReturn(t, env, eventID, payload)
	// Simulate the lost response: retry the identical bytes.
	body := fmt.Sprintf(`{"events":[{"event_id":%q,"event_type":"sale.return_refund.finalized.v1","occurred_at":"2026-09-21T12:00:00Z","payload":%s}]}`,
		eventID, payload)
	res, err := env.syncSvc.Ingest(context.Background(), env.devID, env.credID, []byte(body))
	if err != nil {
		t.Fatalf("lost-ACK retry: %v", err)
	}
	if res.Events[0].Status != "already_accepted" {
		t.Fatalf("want already_accepted, got %+v", res)
	}
}

func mutateReturnPayload(t *testing.T, payload string, fn func(map[string]any)) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatal(err)
	}
	fn(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

var _ = time.Second
