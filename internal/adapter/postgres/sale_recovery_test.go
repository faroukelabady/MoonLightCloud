package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/faroukelabady/MoonLightCloud/internal/sale"
)

// TestProjectorRestartRecovery ingests without any projector running, then
// wires a brand-new projector instance: the event must project with no
// desktop interaction and no in-memory state carried over.
func TestProjectorRestartRecovery(t *testing.T) {
	env := openSaleEnv(t)
	eventID := "22222222-2222-7222-8222-222222222222"
	env.ingest(t, eventID, fixture(t, "sale_egp.json"))
	// No drain yet: prove nothing projected without a worker.
	if n := saleCount(t, env.pool, "sales_projection"); n != 0 {
		t.Fatalf("ingest must not project synchronously, got %d", n)
	}
	// "Restart": a fresh projector instance over the same database.
	fresh := sale.NewProjector(NewDevices(env.pool, 5*time.Second), testSystemClock(), nilLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); fresh.Run(ctx) }()
	waitFor(t, 12*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "restart recovery")
	cancel()
	<-done
}

// TestProjectionRollback forces a failure after child inserts via a
// trigger, then proves zero partial rows, recoverable processing state,
// and success after removing the trigger.
func TestProjectionRollback(t *testing.T) {
	env := openSaleEnv(t)
	eventID := "22222222-2222-7222-8222-222222222222"
	payload := twoLineSale(t)
	env.ingest(t, eventID, payload)
	// Sabotage: fail the second line insert inside the projection tx.
	if _, err := env.pool.Exec(context.Background(), `
		CREATE OR REPLACE FUNCTION fail_second_line() RETURNS trigger AS $$
		BEGIN
			IF NEW.position = 1 THEN RAISE EXCEPTION 'injected failure'; END IF;
			RETURN NEW;
		END; $$ LANGUAGE plpgsql;
		CREATE TRIGGER trg_fail_second_line BEFORE INSERT ON sale_lines_projection
		FOR EACH ROW EXECUTE FUNCTION fail_second_line();`); err != nil {
		t.Fatal(err)
	}
	store := NewDevices(env.pool, 5*time.Second)
	rec, ok, err := store.LoadSaleEvent(context.Background(), eventID)
	if err != nil || !ok {
		t.Fatal("event must load")
	}
	if _, err := store.ProjectSale(context.Background(), rec, time.Now()); err == nil {
		t.Fatal("injected failure must surface")
	}
	// Zero partial projection rows; source event intact.
	for _, table := range []string{"sales_projection", "sale_lines_projection", "sale_payments_projection", "sale_line_classifications_projection"} {
		if n := saleCount(t, env.pool, table); n != 0 {
			t.Fatalf("%s must be empty after rollback, got %d", table, n)
		}
	}
	// HIGH-04: durable retry state committed despite the rollback.
	var status string
	var attempts int
	var nextAt *time.Time
	if err := env.pool.QueryRow(context.Background(),
		`SELECT status, attempt_count, next_attempt_at FROM sync_event_processing WHERE event_id=$1`, eventID).Scan(&status, &attempts, &nextAt); err != nil {
		t.Fatalf("retry state: %v", err)
	}
	if status != "retry" || attempts < 1 || nextAt == nil || !nextAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("want retry with future next_attempt_at, got %q/%d/%v", status, attempts, nextAt)
	}
	var processed int
	if err := env.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM sync_event_processing WHERE event_id=$1 AND status='processed'`, eventID).Scan(&processed); err != nil || processed != 0 {
		t.Fatalf("must not be marked processed: %d (%v)", processed, err)
	}
	// Not retried before due time: a drain inside the backoff window
	// projects nothing.
	env.drain(t)
	if n := saleCount(t, env.pool, "sales_projection"); n != 0 {
		t.Fatalf("must not retry before due time, got %d sales", n)
	}
	// Remove sabotage and simulate clock advance past due: retry succeeds
	// completely (restart-safe: a fresh projector instance does the work).
	if _, err := env.pool.Exec(context.Background(),
		`DROP TRIGGER trg_fail_second_line ON sale_lines_projection; DROP FUNCTION fail_second_line();`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.pool.Exec(context.Background(),
		`UPDATE sync_event_processing SET next_attempt_at = now() - interval '1 second' WHERE event_id=$1`, eventID); err != nil {
		t.Fatal(err)
	}
	fresh := sale.NewProjector(NewDevices(env.pool, 5*time.Second), testSystemClock(), nilLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); fresh.Run(ctx) }()
	waitFor(t, 10*time.Second, func() bool { return saleCount(t, env.pool, "sales_projection") == 1 }, "retry after rollback")
	cancel()
	<-done
	if n := saleCount(t, env.pool, "sale_lines_projection"); n != 2 {
		t.Fatalf("want 2 lines after retry, got %d", n)
	}
	var final string
	if err := env.pool.QueryRow(context.Background(),
		`SELECT status FROM sync_event_processing WHERE event_id=$1`, eventID).Scan(&final); err != nil || final != "processed" {
		t.Fatalf("successful retry must mark processed, got %q (%v)", final, err)
	}
}

// TestMalformedPersistedEvent bypasses HTTP validation with a corrupt
// inbox row: the projector must block deterministically, never panic,
// never write projection rows.
func TestMalformedPersistedEvent(t *testing.T) {
	env := openSaleEnv(t)
	eventID := "22222222-2222-7222-8222-222222222222"
	if _, err := env.pool.Exec(context.Background(), `
		INSERT INTO sync_events (event_id, device_id, event_type, occurred_at, received_at, payload, payload_hash)
		VALUES ($1, $2, 'sale.finalized.v1', now(), now(), '{}', '\x00')`,
		eventID, env.devID); err != nil {
		t.Fatal(err)
	}
	env.drain(t)
	waitFor(t, 10*time.Second, func() bool {
		var n int
		_ = env.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM sync_event_processing WHERE event_id=$1 AND status='blocked'`, eventID).Scan(&n)
		return n == 1
	}, "blocked malformed event")
	if n := saleCount(t, env.pool, "sales_projection"); n != 0 {
		t.Fatalf("no sale rows from malformed event, got %d", n)
	}
	var code string
	_ = env.pool.QueryRow(context.Background(),
		`SELECT last_error_code FROM sync_event_processing WHERE event_id=$1`, eventID).Scan(&code)
	if code != "VALIDATION_FAILED" {
		t.Fatalf("want VALIDATION_FAILED, got %q", code)
	}
}

// twoLineSale returns a small valid 2-line EGP sale for rollback tests.
func twoLineSale(t *testing.T) string {
	t.Helper()
	return bigSale(t, 2, 1)
}
